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

// KnownUser is a subject that signed in, with its newest sign-in time and
// the display name of its newest session that has one. Name is nil when no
// session of the subject has a name.
type KnownUser struct {
	Subject    string
	LastSeenMs int64
	Name       *string
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
	return db.setRoles(ctx, "subject", subject, "role", roles, grantedBy, nowMs)
}

// SetRoleSubjects replaces the subjects of role with subjects. Rows that
// stay keep granted_by and granted_ms. grantedBy and nowMs mark the new rows.
func (db *DB) SetRoleSubjects(ctx context.Context, role string, subjects []string, grantedBy string, nowMs int64) ([]RoleAssignment, error) {
	return db.setRoles(ctx, "role", role, "subject", subjects, grantedBy, nowMs)
}

// setRoles replaces the rows of one column value with the values of the
// other column. fixedCol and fixedVal name the subject or the role that the
// editor edits; listCol and list are the other side. The rows of fixedVal
// that are not in list go, the ones in list stay, and the missing ones come
// in with grantedBy and nowMs. The result is the new list of fixedVal, in
// the order of the other column.
func (db *DB) setRoles(ctx context.Context, fixedCol, fixedVal, listCol string, list []string, grantedBy string, nowMs int64) ([]RoleAssignment, error) {
	// The insert lists the fixed column first, then the list column, so the
	// fixed value and each list value land in their columns.
	cols := "subject, role"
	if fixedCol == "role" {
		cols = "role, subject"
	}
	// A nil list goes to SQL as NULL, and ANY of NULL matches nothing. An
	// empty list must stay an empty array: it deletes every row.
	if list == nil {
		list = []string{}
	}
	var out []RoleAssignment
	err := pgx.BeginFunc(ctx, db.pool, func(tx pgx.Tx) error {
		// The rows that stay are the rows of fixedVal whose other column is
		// in list.
		if _, err := tx.Exec(ctx, `DELETE FROM identity_roles WHERE `+fixedCol+` = $1 AND NOT (`+listCol+` = ANY($2))`, fixedVal, list); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO identity_roles (`+cols+`, granted_by, granted_ms)
		  SELECT $1, v, $2, $3 FROM unnest($4::text[]) AS v
		  ON CONFLICT (subject, role) DO NOTHING`, fixedVal, grantedBy, nowMs, list); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT `+roleCols+` FROM identity_roles WHERE `+fixedCol+` = $1 ORDER BY `+listCol, fixedVal)
		if err != nil {
			return err
		}
		out, err = scanRoles(rows)
		return err
	})
	return out, err
}

// KnownUsers lists the subjects that signed in, newest sign-in first. A
// subject with several sessions counts once, at its newest sign-in. name is
// the display name of the newest session of the subject that has one.
func (db *DB) KnownUsers(ctx context.Context, limit int) ([]KnownUser, error) {
	rows, err := db.pool.Query(ctx, `SELECT subject, max(authenticated_ms),
	  (array_agg(name ORDER BY authenticated_ms DESC) FILTER (WHERE name <> ''))[1] FROM identity_sessions
	  GROUP BY subject ORDER BY max(authenticated_ms) DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []KnownUser
	for rows.Next() {
		var u KnownUser
		if err := rows.Scan(&u.Subject, &u.LastSeenMs, &u.Name); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
