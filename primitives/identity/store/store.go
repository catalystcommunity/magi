// Package store keeps pending logins and login sessions in PostgreSQL.
package store

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var schema string

// ErrNotFound means no record matches, or the record is not in the
// required state.
var ErrNotFound = errors.New("not found")

// Login statuses.
const (
	Pending    = "pending"    // created; start page not used
	Started    = "started"    // browser bound; at the IDP
	Completing = "completing" // callback taken; checks running
	Complete   = "complete"   // verified; token not yet returned
	Delivered  = "delivered"  // token returned to the poller
	Failed     = "failed"
)

// Hash returns the stored form of a secret.
func Hash(secret string) string {
	s := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(s[:])
}

// Login is one pending login row.
type Login struct {
	LoginHash, StartHash, StateHash     string
	Identity, Domain, Client            string
	Nonce, APIBase, CallbackURL, IDPURL string
	BindingHash, Status, Message        string
	Subject, SessionID, ReturnURL       string
	CreatedMs, ExpiresMs                int64
}

// Session is one login session.
type Session struct {
	ID, Subject, UserID, Domain, Name string
	AuthenticatedMs, ExpiresMs        int64
}

// DB holds identity state.
type DB struct{ pool *pgxpool.Pool }

// Open connects to url and applies the schema.
func Open(ctx context.Context, url string) (*DB, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, err
	}
	db := &DB{pool: pool}
	err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(7311009)"); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, schema)
		return err
	})
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return db, nil
}

func (db *DB) Close()                         { db.pool.Close() }
func (db *DB) Ping(ctx context.Context) error { return db.pool.Ping(ctx) }

const loginCols = `login_hash, start_hash, state_hash, identity, domain, client, nonce,
  api_base, callback_url, idp_url, binding_hash, status, message, subject, session_id, return_url, created_ms, expires_ms`

func scanLogin(row pgx.Row) (Login, error) {
	var l Login
	err := row.Scan(&l.LoginHash, &l.StartHash, &l.StateHash, &l.Identity, &l.Domain, &l.Client, &l.Nonce,
		&l.APIBase, &l.CallbackURL, &l.IDPURL, &l.BindingHash, &l.Status, &l.Message, &l.Subject, &l.SessionID, &l.ReturnURL, &l.CreatedMs, &l.ExpiresMs)
	if errors.Is(err, pgx.ErrNoRows) {
		return l, ErrNotFound
	}
	return l, err
}

// CreateLogin inserts a pending login.
func (db *DB) CreateLogin(ctx context.Context, l Login) error {
	_, err := db.pool.Exec(ctx, `INSERT INTO identity_logins (`+loginCols+`)
	  VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)`,
		l.LoginHash, l.StartHash, l.StateHash, l.Identity, l.Domain, l.Client, l.Nonce,
		l.APIBase, l.CallbackURL, l.IDPURL, l.BindingHash, l.Status, l.Message, l.Subject, l.SessionID, l.ReturnURL, l.CreatedMs, l.ExpiresMs)
	return err
}

func (db *DB) one(ctx context.Context, where string, args ...any) (Login, error) {
	return scanLogin(db.pool.QueryRow(ctx, "SELECT "+loginCols+" FROM identity_logins WHERE "+where, args...))
}

// LoginByID returns a login by login_hash.
func (db *DB) LoginByID(ctx context.Context, loginHash string) (Login, error) {
	return db.one(ctx, "login_hash = $1", loginHash)
}

// LoginByStart returns a login by start_hash.
func (db *DB) LoginByStart(ctx context.Context, startHash string) (Login, error) {
	return db.one(ctx, "start_hash = $1", startHash)
}

// Start binds a pending, unexpired login to a browser. It succeeds once.
func (db *DB) Start(ctx context.Context, startHash, bindingHash string, nowMs int64) (Login, error) {
	return scanLogin(db.pool.QueryRow(ctx, `UPDATE identity_logins SET status = $3, binding_hash = $2
	  WHERE start_hash = $1 AND status = $4 AND expires_ms > $5 RETURNING `+loginCols,
		startHash, bindingHash, Started, Pending, nowMs))
}

// TakeCallback moves a started login to completing and returns it. It
// succeeds once per login, so a replayed callback gets ErrNotFound.
func (db *DB) TakeCallback(ctx context.Context, stateHash string) (Login, error) {
	return scanLogin(db.pool.QueryRow(ctx, `UPDATE identity_logins SET status = $2
	  WHERE state_hash = $1 AND status = $3 RETURNING `+loginCols, stateHash, Completing, Started))
}

// Finish records the result of a completing login.
func (db *DB) Finish(ctx context.Context, loginHash, status, message, subject, sessionID string) error {
	_, err := db.pool.Exec(ctx, `UPDATE identity_logins SET status = $2, message = $3, subject = $4, session_id = $5
	  WHERE login_hash = $1 AND status = $6`, loginHash, status, message, subject, sessionID, Completing)
	return err
}

// Deliver moves a complete login to delivered. It succeeds once.
func (db *DB) Deliver(ctx context.Context, loginHash string) (Login, error) {
	return scanLogin(db.pool.QueryRow(ctx, `UPDATE identity_logins SET status = $2
	  WHERE login_hash = $1 AND status = $3 RETURNING `+loginCols, loginHash, Delivered, Complete))
}

// CreateSession inserts a login session.
func (db *DB) CreateSession(ctx context.Context, s Session) error {
	_, err := db.pool.Exec(ctx, `INSERT INTO identity_sessions (session_id, subject, user_id, domain, name, authenticated_ms, expires_ms)
	  VALUES ($1,$2,$3,$4,$5,$6,$7)`, s.ID, s.Subject, s.UserID, s.Domain, s.Name, s.AuthenticatedMs, s.ExpiresMs)
	return err
}

// ActiveSession returns the newest unrevoked session of subject that is
// live at nowMs and started at or before issuedMs.
func (db *DB) ActiveSession(ctx context.Context, subject string, issuedMs, nowMs int64) (Session, error) {
	var s Session
	err := db.pool.QueryRow(ctx, `SELECT session_id, subject, user_id, domain, name, authenticated_ms, expires_ms
	  FROM identity_sessions WHERE subject = $1 AND revoked_ms IS NULL AND expires_ms > $2 AND authenticated_ms <= $3
	  ORDER BY authenticated_ms DESC LIMIT 1`, subject, nowMs, issuedMs).
		Scan(&s.ID, &s.Subject, &s.UserID, &s.Domain, &s.Name, &s.AuthenticatedMs, &s.ExpiresMs)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, ErrNotFound
	}
	return s, err
}

// Prune deletes logins that expired before cutoffMs and sessions that
// ended before cutoffMs.
func (db *DB) Prune(ctx context.Context, cutoffMs int64) error {
	if _, err := db.pool.Exec(ctx, "DELETE FROM identity_logins WHERE expires_ms < $1", cutoffMs); err != nil {
		return err
	}
	_, err := db.pool.Exec(ctx, "DELETE FROM identity_sessions WHERE expires_ms < $1", cutoffMs)
	return err
}
