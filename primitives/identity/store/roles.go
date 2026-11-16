package store

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// RoleAssignment is one role of one subject. The primitive does not know
// what the role permits.
type RoleAssignment struct {
	Subject, Role, GrantedBy string
	GrantedMs                int64
}

// KnownUser is a subject that signed in, with its newest sign-in time.
type KnownUser struct {
	Subject    string
	LastSeenMs int64
}

const roleCols = `subject, role, granted_by, granted_ms`

func scanRoles(rows pgx.Rows) ([]RoleAssignment, error) {
	defer rows.Close()
	var out []RoleAssignment
	for rows.Next() {
		var r RoleAssignment
		if err := rows.Scan(&r.Subject, &r.Role, &r.GrantedBy, &r.GrantedMs); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Roles lists the assignments of subject, or the assignments of role. One
// of the two must be set. The order is by subject, then role.
func (db *DB) Roles(ctx context.Context, subject, role string) ([]RoleAssignment, error) {
	rows, err := db.pool.Query(ctx, `SELECT `+roleCols+` FROM identity_roles
	  WHERE ($1 <> '' AND subject = $1) OR ($2 <> '' AND role = $2) ORDER BY subject, role`, subject, role)
	if err != nil {
		return nil, err
	}
	return scanRoles(rows)
}

// SetSubjectRoles replaces the roles of subject with roles. Rows that stay
// keep granted_by and granted_ms. grantedBy and nowMs mark the new rows.
func (db *DB) SetSubjectRoles(ctx context.Context, subject string, roles []string, grantedBy string, nowMs int64) ([]RoleAssignment, error) {
	var out []RoleAssignment
	err := pgx.BeginFunc(ctx, db.pool, func(tx pgx.Tx) error {
		if err := clearRoles(ctx, tx, "subject", subject, roles); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO identity_roles (subject, role, granted_by, granted_ms)
		  SELECT $1, r, $2, $3 FROM unnest($4::text[]) AS r
		  ON CONFLICT (subject, role) DO NOTHING`, subject, grantedBy, nowMs, roles); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT `+roleCols+` FROM identity_roles WHERE subject = $1 ORDER BY role`, subject)
		if err != nil {
			return err
		}
		out, err = scanRoles(rows)
		return err
	})
	return out, err
}

// SetRoleSubjects replaces the subjects of role with subjects. Rows that
// stay keep granted_by and granted_ms. grantedBy and nowMs mark the new rows.
func (db *DB) SetRoleSubjects(ctx context.Context, role string, subjects []string, grantedBy string, nowMs int64) ([]RoleAssignment, error) {
	var out []RoleAssignment
	err := pgx.BeginFunc(ctx, db.pool, func(tx pgx.Tx) error {
		if err := clearRoles(ctx, tx, "role", role, subjects); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO identity_roles (subject, role, granted_by, granted_ms)
		  SELECT s, $1, $2, $3 FROM unnest($4::text[]) AS s
		  ON CONFLICT (subject, role) DO NOTHING`, role, grantedBy, nowMs, subjects); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT `+roleCols+` FROM identity_roles WHERE role = $1 ORDER BY subject`, role)
		if err != nil {
			return err
		}
		out, err = scanRoles(rows)
		return err
	})
	return out, err
}

// clearRoles deletes the rows of one subject or one role that are not in
// keep. An empty keep deletes them all: ANY of an empty list matches
// nothing, and ANY of NULL matches nothing too.
func clearRoles(ctx context.Context, tx pgx.Tx, col, value string, keep []string) error {
	if len(keep) == 0 {
		_, err := tx.Exec(ctx, `DELETE FROM identity_roles WHERE `+col+` = $1`, value)
		return err
	}
	_, err := tx.Exec(ctx, `DELETE FROM identity_roles WHERE `+col+` = $1 AND NOT (`+col+` = ANY($2))`, value, keep)
	return err
}

// KnownUsers lists the subjects that signed in, newest sign-in first. A
// subject with several sessions counts once, at its newest sign-in.
func (db *DB) KnownUsers(ctx context.Context, limit int) ([]KnownUser, error) {
	rows, err := db.pool.Query(ctx, `SELECT subject, max(authenticated_ms) FROM identity_sessions
	  GROUP BY subject ORDER BY max(authenticated_ms) DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []KnownUser
	for rows.Next() {
		var u KnownUser
		if err := rows.Scan(&u.Subject, &u.LastSeenMs); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
