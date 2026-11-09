package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// ErrConflict means a newer revision exists.
var ErrConflict = errors.New("conflict")

// GrantRequest is a pending act-as grant request.
type GrantRequest struct {
	NonceHash, Owner, Domain, UserID, Audience, ReturnURL string
	CreatedMs, ExpiresMs                                  int64
}

// Grant is an act-as grant that Magi holds.
type Grant struct {
	ID, Owner, Domain, Audience, State string
	Scopes                             []string
	Bytes                              []byte
	ExpiresMs, RenewableUntilMs        int64
	CreatedMs, UpdatedMs               int64
}

// CreateGrantRequest stores a pending request.
func (db *DB) CreateGrantRequest(ctx context.Context, r GrantRequest) error {
	_, err := db.pool.Exec(ctx, `INSERT INTO identity_grant_requests
	  (nonce_hash, owner, domain, user_id, audience, return_url, created_ms, expires_ms) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		r.NonceHash, r.Owner, r.Domain, r.UserID, r.Audience, r.ReturnURL, r.CreatedMs, r.ExpiresMs)
	return err
}

// TakeGrantRequest marks the request with nonceHash used by grantID and
// returns it. It succeeds once, and only before expiry.
func (db *DB) TakeGrantRequest(ctx context.Context, nonceHash, grantID string, nowMs int64) (GrantRequest, error) {
	var r GrantRequest
	err := db.pool.QueryRow(ctx, `UPDATE identity_grant_requests SET grant_id = $2
	  WHERE nonce_hash = $1 AND grant_id = '' AND expires_ms > $3
	  RETURNING nonce_hash, owner, domain, user_id, audience, return_url, created_ms, expires_ms`, nonceHash, grantID, nowMs).
		Scan(&r.NonceHash, &r.Owner, &r.Domain, &r.UserID, &r.Audience, &r.ReturnURL, &r.CreatedMs, &r.ExpiresMs)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

// PutGrant inserts or replaces a grant.
func (db *DB) PutGrant(ctx context.Context, g Grant) error {
	_, err := db.pool.Exec(ctx, `INSERT INTO identity_grants
	  (grant_id, owner, domain, audience, scopes, grant_bytes, expires_ms, renewable_until_ms, created_ms, updated_ms, state)
	  VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
	  ON CONFLICT (grant_id) DO UPDATE SET scopes = $5, grant_bytes = $6, expires_ms = $7, renewable_until_ms = $8,
	    updated_ms = $10, state = $11 WHERE identity_grants.owner = $2`,
		g.ID, g.Owner, g.Domain, g.Audience, g.Scopes, g.Bytes, g.ExpiresMs, g.RenewableUntilMs, g.CreatedMs, g.UpdatedMs, g.State)
	return err
}

// SetGrantState changes the state of a grant.
func (db *DB) SetGrantState(ctx context.Context, grantID, state string, nowMs int64) error {
	_, err := db.pool.Exec(ctx, `UPDATE identity_grants SET state = $2, updated_ms = $3 WHERE grant_id = $1`, grantID, state, nowMs)
	return err
}

const grantCols = `grant_id, owner, domain, audience, scopes, grant_bytes, expires_ms, renewable_until_ms, created_ms, updated_ms, state`

func scanGrants(rows pgx.Rows) ([]Grant, error) {
	defer rows.Close()
	var out []Grant
	for rows.Next() {
		var g Grant
		if err := rows.Scan(&g.ID, &g.Owner, &g.Domain, &g.Audience, &g.Scopes, &g.Bytes, &g.ExpiresMs, &g.RenewableUntilMs, &g.CreatedMs, &g.UpdatedMs, &g.State); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// Grants lists the grants of owner (all owners when empty), newest first.
// An empty audience matches all audiences.
func (db *DB) Grants(ctx context.Context, owner, audience string) ([]Grant, error) {
	rows, err := db.pool.Query(ctx, `SELECT `+grantCols+` FROM identity_grants
	  WHERE ($1 = '' OR owner = $1) AND ($2 = '' OR audience = $2) ORDER BY created_ms DESC LIMIT 500`, owner, audience)
	if err != nil {
		return nil, err
	}
	return scanGrants(rows)
}

// RenewableGrants gives the active grants that expire before beforeMs and
// that the home domain can still renew (renewable_until after nowMs).
func (db *DB) RenewableGrants(ctx context.Context, beforeMs, nowMs int64) ([]Grant, error) {
	rows, err := db.pool.Query(ctx, `SELECT `+grantCols+` FROM identity_grants
	  WHERE state = 'active' AND expires_ms < $1 AND expires_ms > $2 AND renewable_until_ms > expires_ms
	  ORDER BY expires_ms LIMIT 500`, beforeMs, nowMs)
	if err != nil {
		return nil, err
	}
	return scanGrants(rows)
}

// ExpiringGrants gives the active grants whose renewable_until is after
// nowMs and before beforeMs.
func (db *DB) ExpiringGrants(ctx context.Context, beforeMs, nowMs int64) ([]Grant, error) {
	rows, err := db.pool.Query(ctx, `SELECT `+grantCols+` FROM identity_grants
	  WHERE state = 'active' AND renewable_until_ms > $2 AND renewable_until_ms < $1
	  ORDER BY renewable_until_ms LIMIT 500`, beforeMs, nowMs)
	if err != nil {
		return nil, err
	}
	return scanGrants(rows)
}

// DeleteGrant removes a grant of owner.
func (db *DB) DeleteGrant(ctx context.Context, grantID, owner string) error {
	tag, err := db.pool.Exec(ctx, `DELETE FROM identity_grants WHERE grant_id = $1 AND owner = $2`, grantID, owner)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// Credential is a stored credential. ValueEnc is encrypted.
type Credential struct {
	Owner, System, Label string
	ValueEnc             []byte
	Revision             int64
	ExpiresMs, UpdatedMs int64
}

// PutCredential stores c. With expected >= 0 the stored revision must be
// expected (0: no stored row), else ErrConflict. It returns the new revision.
func (db *DB) PutCredential(ctx context.Context, c Credential, expected int64) (int64, error) {
	var rev int64
	var err error
	if expected == 0 {
		err = db.pool.QueryRow(ctx, `INSERT INTO identity_credentials (owner, system, label, value_enc, revision, expires_ms, updated_ms)
		  VALUES ($1,$2,$3,$4,1,$5,$6) ON CONFLICT DO NOTHING RETURNING revision`,
			c.Owner, c.System, c.Label, c.ValueEnc, c.ExpiresMs, c.UpdatedMs).Scan(&rev)
	} else {
		err = db.pool.QueryRow(ctx, `INSERT INTO identity_credentials (owner, system, label, value_enc, revision, expires_ms, updated_ms)
		  VALUES ($1,$2,$3,$4,1,$5,$6)
		  ON CONFLICT (owner, system) DO UPDATE SET label = $3, value_enc = $4, revision = identity_credentials.revision + 1,
		    expires_ms = $5, updated_ms = $6 WHERE $7 < 0 OR identity_credentials.revision = $7
		  RETURNING revision`, c.Owner, c.System, c.Label, c.ValueEnc, c.ExpiresMs, c.UpdatedMs, expected).Scan(&rev)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrConflict
	}
	return rev, err
}

// GetCredential returns one credential.
func (db *DB) GetCredential(ctx context.Context, owner, system string) (Credential, error) {
	c := Credential{Owner: owner, System: system}
	err := db.pool.QueryRow(ctx, `SELECT label, value_enc, revision, expires_ms, updated_ms FROM identity_credentials
	  WHERE owner = $1 AND system = $2`, owner, system).Scan(&c.Label, &c.ValueEnc, &c.Revision, &c.ExpiresMs, &c.UpdatedMs)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

// Credentials lists credentials of owner (all when empty) without values.
func (db *DB) Credentials(ctx context.Context, owner string) ([]Credential, error) {
	rows, err := db.pool.Query(ctx, `SELECT owner, system, label, revision, expires_ms, updated_ms FROM identity_credentials
	  WHERE $1 = '' OR owner = $1 ORDER BY owner, system LIMIT 500`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Credential
	for rows.Next() {
		var c Credential
		if err := rows.Scan(&c.Owner, &c.System, &c.Label, &c.Revision, &c.ExpiresMs, &c.UpdatedMs); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// DeleteCredential removes one credential.
func (db *DB) DeleteCredential(ctx context.Context, owner, system string) error {
	tag, err := db.pool.Exec(ctx, `DELETE FROM identity_credentials WHERE owner = $1 AND system = $2`, owner, system)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}
