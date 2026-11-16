package store

// Trust of records (risk-remediation task 2.4, requirement 8).
//
// An entity or a claim says where it came from. Untrusted records hold text
// of others, of the web, or of a task. They are kept apart (the column and
// its partial indexes), they never change a trusted record, and only the
// person moves them to another trust (promote). The rules here read the
// stored values only; no model is involved.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	api "github.com/catalystcommunity/magi/primitives/knowledge/api"
)

const (
	TrustPerson    = "person"    // the person said it, or promoted it
	TrustNamed     = "named"     // a configured source
	TrustOwn       = "own"       // Magi wrote it from the person's own inputs; the default of old records
	TrustUntrusted = "untrusted" // text of others, of the web, or of a task
)

// TrustRank orders the values: a higher rank is more trusted.
func TrustRank(t string) int {
	switch t {
	case TrustPerson:
		return 3
	case TrustNamed:
		return 2
	case TrustOwn:
		return 1
	}
	return 0
}

// NormalizeTrust returns the trust of a request: own when none is given.
func NormalizeTrust(t *string) (string, error) {
	if t == nil || *t == "" {
		return TrustOwn, nil
	}
	switch *t {
	case TrustPerson, TrustNamed, TrustOwn, TrustUntrusted:
		return *t, nil
	}
	return "", invalid("trust must be person, named, own, or untrusted")
}

// NormalizeTrustValue returns v when it is a trust value, else "".
func NormalizeTrustValue(v string) string {
	if t, err := NormalizeTrust(&v); err == nil && v != "" {
		return t
	}
	return ""
}

// TrustOf returns the trust of a record of a type that has one (entity or
// claim); a value that is not stored reads as own.
func TrustOf(t *string) string {
	if t == nil || *t == "" {
		return TrustOwn
	}
	return *t
}

// promote moves an untrusted entity or claim to person. Who may do it is the
// business of the caller (the server checks that the caller is the person).
func (s *Store) promote(ctx context.Context, tx pgx.Tx, w Who, t string, owners []string, in ChangeInput) (string, error) {
	if t != "entity" && t != "claim" {
		return "", invalid("promote is for entities and claims")
	}
	if len(in.Set) > 0 {
		return "", invalid("promote does not use set")
	}
	table := columnOf(t)
	var cur string
	if err := tx.QueryRow(ctx, fmt.Sprintf(`SELECT trust FROM %s WHERE id = $1 FOR UPDATE`, table), in.ID).Scan(&cur); err != nil {
		return "", err
	}
	if cur != TrustUntrusted {
		return "", invalid("only an untrusted record can be promoted; this one is %s", cur)
	}
	if _, err := tx.Exec(ctx, fmt.Sprintf(`UPDATE %s SET trust = $2, updated_ms = $3 WHERE id = $1`, table), in.ID, TrustPerson, s.now()); err != nil {
		return "", err
	}
	id, _, err := s.change(ctx, tx, in.ID, "promote", w, owners, in.Reason, in.Utterance,
		map[string]any{"trust": cur}, map[string]any{"trust": TrustPerson})
	return id, err
}

// keepsTrusted says that an incoming write must not touch an existing
// record: the write is less trusted than the record.
func keepsTrusted(existing, incoming string) bool {
	return TrustRank(incoming) < TrustRank(existing)
}

var _ = api.Entity{}
