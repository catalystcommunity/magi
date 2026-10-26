// Package knowledge is a disposable storage experiment, not an authenticated
// Magi service. Access contexts and grants are supplied by synthetic fixtures.
package knowledge

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	wire "opal.local/experiments/knowledge/generated/go"
)

//go:embed storage.sql
var migration string

var (
	ErrDenied      = errors.New("denied or absent")
	ErrConflict    = errors.New("conflict")
	ErrInvalid     = errors.New("invalid")
	ErrLimit       = errors.New("limit exceeded")
	ErrUnavailable = errors.New("unavailable")
)

const maxPayload = 1 << 20
const maxGraph = 128

type store struct {
	db        *pgxpool.Pool
	authority wire.IdentityRef
	now       func() time.Time
}

// NUL is not accepted in keys. Length framing prevents ambiguous separators.
func key(parts ...string) string {
	var b strings.Builder
	for _, p := range parts {
		fmt.Fprintf(&b, "%d:%s", len(p), p)
	}
	return b.String()
}
func identity(i wire.IdentityRef) string    { return key(i.Domain, i.AccountId) }
func scopeKey(s wire.ScopeRef) string       { return key(identity(s.Authority), s.ScopeId) }
func refKey(r wire.RecordRef) string        { return key(identity(r.Authority), r.RecordId) }
func validText(s string) bool               { return s != "" && len(s) <= 256 && !strings.ContainsRune(s, 0) }
func validIdentity(i wire.IdentityRef) bool { return validText(i.Domain) && validText(i.AccountId) }

func describe(r wire.EvidenceRecord) (wire.EvidenceHeader, string, string, []wire.RecordRef, error) {
	var h wire.EvidenceHeader
	var kind, session string
	var sources []wire.RecordRef
	switch v := r.(type) {
	case wire.InteractionRecord:
		h, kind = v.Header, "interaction"
	case wire.InterpretationRecord:
		h, kind = v.Header, "interpretation"
		sources = append(sources, v.Sources...)
		sources = append(sources, v.ContextRefs...)
		for _, d := range v.Destinations {
			sources = append(sources, d.PromptRefs...)
		}
		if v.Prior != nil {
			sources = append(sources, *v.Prior)
		}
	case wire.SessionInput:
		h, kind, session = v.Header, "session_input", v.SessionId
		sources = append(sources, v.Interpretation)
		sources = append(sources, v.Sources...)
	case wire.PresentationRecord:
		h, kind = v.Header, "presentation"
		sources = append(sources, v.Output)
	case wire.LearningAssessment:
		h, kind = v.Header, "assessment"
		sources = append(sources, v.Inputs...)
		sources = append(sources, v.Supporting...)
		sources = append(sources, v.Contrary...)
		if v.Prior != nil {
			sources = append(sources, *v.Prior)
		}
	case wire.SourceContext:
		h, kind = v.Header, "source"
	default:
		return h, "", "", nil, ErrInvalid
	}
	sources = append(sources, h.Causes...)
	return h, kind, session, sources, nil
}

func decode(payload []byte) (wire.EvidenceRecord, error) {
	if len(payload) > maxPayload {
		return nil, ErrLimit
	}
	p, err := wire.DecodeHistoryPage(payload)
	if err != nil || len(p.Records) != 1 {
		return nil, ErrInvalid
	}
	return p.Records[0], nil
}

func dbError(err error) error {
	var p *pgconn.PgError
	if errors.As(err, &p) && p.Code == "23505" {
		return ErrConflict
	}
	return err
}

func (s *store) allowed(ctx context.Context, tx pgx.Tx, a wire.ContextAccess, scopes []string, write bool) error {
	if !validIdentity(a.Actor) || !validIdentity(a.Processor) || !validText(a.Purpose) {
		return ErrDenied
	}
	subject := a.Actor
	if a.RepresentedIdentity != nil {
		subject = *a.RepresentedIdentity
	}
	for _, scope := range scopes {
		var ok bool
		err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM scope_readers
            WHERE subject=$1 AND scope=$2 AND (NOT $3 OR can_write))
            AND EXISTS(SELECT 1 FROM processor_grants WHERE processor=$4 AND scope=$2 AND purpose=$5)`,
			identity(subject), scope, write, identity(a.Processor), a.Purpose).Scan(&ok)
		if err != nil {
			return err
		}
		if !ok {
			return ErrDenied
		}
		if a.RepresentedIdentity != nil && subject != a.Actor {
			err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM representations
                WHERE actor=$1 AND subject=$2 AND scope=$3 AND purpose=$4)`,
				identity(a.Actor), identity(subject), scope, a.Purpose).Scan(&ok)
			if err != nil {
				return err
			}
			if !ok {
				return ErrDenied
			}
		}
	}
	return nil
}

// Scope grants are checked again for each read. A retained payload's old label
// never overrides current source or session restrictions.
func (s *store) read(ctx context.Context, tx pgx.Tx, a wire.ContextAccess, ref wire.RecordRef, seen map[string]bool) (wire.EvidenceRecord, error) {
	k := refKey(ref)
	if seen[k] {
		return nil, nil
	}
	if len(seen) >= maxGraph {
		return nil, ErrLimit
	}
	seen[k] = true
	var payload []byte
	var scopes, purposes []string
	var kind string
	var deleted, enrichment bool
	err := tx.QueryRow(ctx, `SELECT r.payload, r.scopes || COALESCE(s.scopes, ARRAY[]::text[]),
            r.purposes, r.kind, r.deleted, r.enrichment_allowed
        FROM records r LEFT JOIN sessions s ON s.authority=r.authority AND s.id=r.session_id
        WHERE r.authority=$1 AND r.id=$2`, identity(ref.Authority), ref.RecordId).
		Scan(&payload, &scopes, &purposes, &kind, &deleted, &enrichment)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrDenied
	}
	if err != nil {
		return nil, err
	}
	if deleted || kind != ref.Kind || !slices.Contains(purposes, a.Purpose) || (a.Purpose == "enrich" && !enrichment) {
		return nil, ErrDenied
	}
	if err = s.allowed(ctx, tx, a, scopes, false); err != nil {
		return nil, err
	}
	r, err := decode(payload)
	if err != nil {
		return nil, err
	}
	_, _, _, dependencies, err := describe(r)
	if err != nil {
		return nil, err
	}
	for _, source := range dependencies {
		if _, err = s.read(ctx, tx, a, source, seen); err != nil {
			return nil, err
		}
	}
	return r, nil
}

func (s *store) Read(ctx context.Context, a wire.ContextAccess, ref wire.RecordRef) (wire.EvidenceRecord, error) {
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if err = s.allowed(ctx, tx, a, []string{scopeKey(a.ControlScope)}, false); err != nil {
		return nil, err
	}
	return s.read(ctx, tx, a, ref, map[string]bool{})
}

// put is an internal, trusted capture/admission seam, not a public append API.
// It accepts only synthetic, reference-form content in this experiment.
func (s *store) put(ctx context.Context, a wire.ContextAccess, command string, record wire.EvidenceRecord, expected *uint64) (wire.Receipt, error) {
	var receipt wire.Receipt
	h, kind, session, sources, err := describe(record)
	if err != nil {
		return receipt, err
	}
	if !validText(command) || !validText(h.Ref.RecordId) || !validText(h.StreamId) || h.Ref.Authority != s.authority || h.Ref.Kind != kind || h.Producer != a.Actor || h.SourceSequence > math.MaxInt64 || len(h.Restrictions.RequiredScopes) == 0 || h.ReceivedAt.IsZero() {
		return receipt, ErrInvalid
	}
	if (h.RepresentedIdentity == nil) != (a.RepresentedIdentity == nil) || (h.RepresentedIdentity != nil && *h.RepresentedIdentity != *a.RepresentedIdentity) {
		return receipt, ErrDenied
	}
	payload := wire.EncodeHistoryPage(wire.HistoryPage{Records: []wire.EvidenceRecord{record}})
	if _, err = decode(payload); err != nil {
		return receipt, err
	}
	request := append(append([]byte{}, payload...), wire.EncodeContextAccess(a)...)
	if expected != nil {
		request = append(request, []byte(fmt.Sprintf("/expected:%d", *expected))...)
	}
	digest := sha256.Sum256(request)
	authority := identity(s.authority)
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return receipt, err
	}
	defer tx.Rollback(ctx)
	// Serialize this key before checking its receipt. Different keys can proceed.
	_, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, key(authority, identity(a.Actor), kind, command))
	if err != nil {
		return receipt, err
	}
	if err = s.allowed(ctx, tx, a, []string{scopeKey(a.ControlScope)}, true); err != nil {
		return receipt, err
	}
	var oldDigest, oldReceipt []byte
	err = tx.QueryRow(ctx, `SELECT digest,receipt FROM command_receipts
        WHERE authority=$1 AND actor=$2 AND operation=$3 AND command_key=$4`, authority, identity(a.Actor), kind, command).Scan(&oldDigest, &oldReceipt)
	if err == nil {
		if !bytes.Equal(oldDigest, digest[:]) {
			return receipt, ErrConflict
		}
		// A retry cannot reveal a now-restricted receipt or resurrect deletion.
		if _, err = s.read(ctx, tx, a, h.Ref, map[string]bool{}); err != nil {
			return receipt, err
		}
		return wire.DecodeReceipt(oldReceipt)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return receipt, err
	}
	scopes := make([]string, 0, len(h.Restrictions.RequiredScopes))
	for _, scope := range h.Restrictions.RequiredScopes {
		scopes = append(scopes, scopeKey(scope))
	}
	if !slices.Contains(scopes, scopeKey(a.ControlScope)) || !slices.Contains(h.Restrictions.PermittedPurposes, a.Purpose) {
		return receipt, ErrDenied
	}
	if err = s.allowed(ctx, tx, a, scopes, false); err != nil {
		return receipt, err
	}
	// Derivation cannot drop any source restriction, including its current session.
	for _, source := range sources {
		if source == h.Ref {
			return receipt, ErrInvalid
		}
		if _, err = s.read(ctx, tx, a, source, map[string]bool{}); err != nil {
			return receipt, err
		}
		var required, purposes []string
		err = tx.QueryRow(ctx, `SELECT r.scopes || COALESCE(s.scopes,ARRAY[]::text[]),r.purposes
            FROM records r LEFT JOIN sessions s ON s.authority=r.authority AND s.id=r.session_id
            WHERE r.authority=$1 AND r.id=$2`, identity(source.Authority), source.RecordId).Scan(&required, &purposes)
		if err != nil {
			return receipt, err
		}
		for _, need := range required {
			if !slices.Contains(scopes, need) {
				return receipt, ErrDenied
			}
		}
		for _, purpose := range h.Restrictions.PermittedPurposes {
			if !slices.Contains(purposes, purpose) {
				return receipt, ErrDenied
			}
		}
	}
	if v, ok := record.(wire.InterpretationRecord); ok {
		if len(v.Sources) == 0 || len(v.Destinations) == 0 {
			return receipt, ErrInvalid
		}
		if v.Prior == nil {
			if expected != nil {
				return receipt, ErrInvalid
			}
			_, err = tx.Exec(ctx, `INSERT INTO interpretation_heads VALUES($1,$2,$2,1)`, authority, h.Ref.RecordId)
		} else {
			if v.Prior.Authority != s.authority {
				return receipt, ErrDenied
			}
			if expected == nil || *expected > math.MaxInt64 {
				return receipt, ErrInvalid
			}
			result, e := tx.Exec(ctx, `UPDATE interpretation_heads SET current_id=$1,revision=revision+1
                WHERE authority=$2 AND current_id=$3 AND revision=$4`, h.Ref.RecordId, authority, v.Prior.RecordId, int64(*expected))
			err = e
			if err == nil && result.RowsAffected() != 1 {
				return receipt, ErrConflict
			}
		}
		if err != nil {
			return receipt, dbError(err)
		}
	}
	if v, ok := record.(wire.SessionInput); ok {
		if v.CommandKey != command {
			return receipt, ErrInvalid
		}
		r, e := s.read(ctx, tx, a, v.Interpretation, map[string]bool{})
		if e != nil {
			return receipt, e
		}
		meaning, ok := r.(wire.InterpretationRecord)
		if !ok {
			return receipt, ErrInvalid
		}
		matched := false
		for _, d := range meaning.Destinations {
			if d.SessionId == session && d.Content == v.Content {
				matched = true
			}
		}
		if !matched || !slices.Equal(v.Sources, meaning.Sources) {
			return receipt, ErrInvalid
		}
		// Hold the current interpretation until admission commits. A concurrent
		// correction must not replace it between this check and the new route.
		var current string
		err = tx.QueryRow(ctx, `SELECT current_id FROM interpretation_heads WHERE authority=$1 AND current_id=$2 FOR SHARE`, identity(v.Interpretation.Authority), v.Interpretation.RecordId).Scan(&current)
		if errors.Is(err, pgx.ErrNoRows) {
			return receipt, ErrConflict
		}
		if err != nil {
			return receipt, err
		}
		var sessionScopes []string
		var revision uint64
		err = tx.QueryRow(ctx, `SELECT scopes,revision FROM sessions WHERE authority=$1 AND id=$2 FOR UPDATE`, authority, session).Scan(&sessionScopes, &revision)
		if errors.Is(err, pgx.ErrNoRows) {
			return receipt, ErrDenied
		}
		if err != nil {
			return receipt, err
		}
		if revision != v.PolicyRevision {
			return receipt, ErrConflict
		}
		// No implicit declassification or silent privacy change in this first slice.
		// The coordinator must explicitly restrict the session before admission.
		for _, need := range scopes {
			if !slices.Contains(sessionScopes, need) {
				return receipt, ErrDenied
			}
		}
		if err = s.allowed(ctx, tx, a, sessionScopes, true); err != nil {
			return receipt, err
		}
	}
	observed := h.ReceivedAt
	if h.ObservedAt != nil {
		observed = *h.ObservedAt
	}
	var sessionArg any
	if session != "" {
		sessionArg = session
	}
	enrichment := true
	if v, ok := record.(wire.SourceContext); ok {
		enrichment = v.EnrichmentAllowed
	}
	_, err = tx.Exec(ctx, `INSERT INTO records(authority,id,kind,producer,stream,source_sequence,payload,scopes,purposes,session_id,observed_seconds,observed_nanos,enrichment_allowed)
        VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, authority, h.Ref.RecordId, kind, identity(h.Producer), h.StreamId, int64(h.SourceSequence), payload, scopes, h.Restrictions.PermittedPurposes, sessionArg, observed.Unix(), observed.Nanosecond(), enrichment)
	if err != nil {
		return receipt, dbError(err)
	}
	for _, source := range sources {
		_, err = tx.Exec(ctx, `INSERT INTO dependencies VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, authority, h.Ref.RecordId, identity(source.Authority), source.RecordId)
		if err != nil {
			return receipt, err
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO outbox(authority,id) VALUES($1,$2)`, authority, h.Ref.RecordId)
	if err != nil {
		return receipt, err
	}
	if session != "" {
		_, err = tx.Exec(ctx, `INSERT INTO deliveries(authority,id,session_id) VALUES($1,$2,$3)`, authority, h.Ref.RecordId, session)
		if err != nil {
			return receipt, err
		}
		v := record.(wire.SessionInput)
		_, err = tx.Exec(ctx, `INSERT INTO session_input_routes VALUES($1,$2,$3,$4,$5)`, authority, identity(v.Interpretation.Authority), v.Interpretation.RecordId, session, h.Ref.RecordId)
		if err != nil {
			return receipt, dbError(err)
		}
	}
	receipt = wire.Receipt{Record: h.Ref, CommandKey: command, AcceptedAt: s.now().UTC()}
	_, err = tx.Exec(ctx, `INSERT INTO command_receipts VALUES($1,$2,$3,$4,$5,$6,$7)`, authority, identity(a.Actor), kind, command, digest[:], h.Ref.RecordId, wire.EncodeReceipt(receipt))
	if err != nil {
		return receipt, err
	}
	return receipt, tx.Commit(ctx)
}

func (s *store) Deliver(ctx context.Context, a wire.ContextAccess, ref wire.RecordRef, useContext string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	r, err := s.read(ctx, tx, a, ref, map[string]bool{})
	if err != nil {
		return err
	}
	input, ok := r.(wire.SessionInput)
	if !ok {
		return ErrInvalid
	}
	if err = s.allowed(ctx, tx, a, []string{scopeKey(a.ControlScope)}, true); err != nil {
		return err
	}
	var sessionScopes []string
	if err = tx.QueryRow(ctx, `SELECT scopes FROM sessions WHERE authority=$1 AND id=$2`, identity(ref.Authority), input.SessionId).Scan(&sessionScopes); err != nil {
		return err
	}
	if !slices.Contains(sessionScopes, scopeKey(a.ControlScope)) {
		return ErrDenied
	}
	if err = s.allowed(ctx, tx, a, sessionScopes, true); err != nil {
		return err
	}
	// Receipt and actual context use are separate. A blank useContext is delivery only.
	if useContext == "" {
		_, err = tx.Exec(ctx, `UPDATE deliveries SET received=true WHERE authority=$1 AND id=$2`, identity(ref.Authority), ref.RecordId)
	} else {
		if !validText(useContext) {
			return ErrInvalid
		}
		var received bool
		if err = tx.QueryRow(ctx, `SELECT received FROM deliveries WHERE authority=$1 AND id=$2 FOR UPDATE`, identity(ref.Authority), ref.RecordId).Scan(&received); err != nil {
			return err
		}
		if !received {
			return ErrConflict
		}
		_, err = tx.Exec(ctx, `INSERT INTO context_uses VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, identity(ref.Authority), ref.RecordId, useContext)
	}
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *store) History(ctx context.Context, a wire.ContextAccess, root wire.RecordRef) (wire.HistoryPage, error) {
	page := wire.HistoryPage{}
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return page, err
	}
	defer tx.Rollback(ctx)
	if err = s.allowed(ctx, tx, a, []string{scopeKey(a.ControlScope)}, false); err != nil {
		return page, err
	}
	if _, err = s.read(ctx, tx, a, root, map[string]bool{}); err != nil {
		return page, err
	}
	queue := []wire.RecordRef{root}
	visited := map[string]bool{}
	for len(queue) > 0 {
		ref := queue[0]
		queue = queue[1:]
		if visited[refKey(ref)] {
			continue
		}
		if len(visited) >= maxGraph {
			return page, ErrLimit
		}
		visited[refKey(ref)] = true
		record, err := s.read(ctx, tx, a, ref, map[string]bool{})
		if errors.Is(err, ErrDenied) {
			continue
		}
		if err != nil {
			return page, err
		}
		page.Records = append(page.Records, record)
		_, _, _, sources, _ := describe(record)
		queue = append(queue, sources...)
		rows, err := tx.Query(ctx, `SELECT r.payload FROM dependencies d JOIN records r
            ON r.authority=d.authority AND r.id=d.id WHERE d.source_authority=$1 AND d.source_id=$2
            ORDER BY r.authority,r.id LIMIT 129`, identity(ref.Authority), ref.RecordId)
		if err != nil {
			return page, err
		}
		payloads, err := pgx.CollectRows(rows, pgx.RowTo[[]byte])
		if err != nil {
			return page, err
		}
		if len(payloads) > maxGraph {
			return page, ErrLimit
		}
		for _, p := range payloads {
			r, err := decode(p)
			if err != nil {
				return page, err
			}
			h, _, _, _, _ := describe(r)
			queue = append(queue, h.Ref)
		}
	}
	return page, nil
}

func searchFailure(ctx context.Context, err error) (wire.QueryResult, error) {
	if errors.Is(ctx.Err(), context.Canceled) {
		return wire.QueryResult{Status: "cancelled"}, context.Canceled
	}
	if errors.Is(err, ErrDenied) {
		return wire.QueryResult{Status: "denied"}, ErrDenied
	}
	return wire.QueryResult{Status: "unavailable"}, ErrUnavailable
}

func (s *store) Search(ctx context.Context, a wire.ContextAccess, q wire.KnowledgeQuery) (wire.QueryResult, error) {
	return s.search(ctx, a, q, false)
}

// anyTerm is an internal comparison option, not a caller-selected policy.
func (s *store) search(ctx context.Context, a wire.ContextAccess, q wire.KnowledgeQuery, anyTerm bool) (wire.QueryResult, error) {
	result := wire.QueryResult{Status: "no_match", Matches: []wire.QueryMatch{}, Gaps: []string{}}
	if q.Limit < 1 || q.Limit > 50 || len(q.Question) == 0 || len(q.Question) > 512 || q.Purpose != a.Purpose {
		return result, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return searchFailure(ctx, err)
	}
	defer tx.Rollback(ctx)
	if err = s.allowed(ctx, tx, a, []string{scopeKey(a.ControlScope)}, false); err != nil {
		return searchFailure(ctx, err)
	}
	subject := a.Actor
	if a.RepresentedIdentity != nil {
		subject = *a.RepresentedIdentity
	}
	rows, err := tx.Query(ctx, `SELECT r.scope FROM scope_readers r JOIN processor_grants p ON p.scope=r.scope
        WHERE r.subject=$1 AND p.processor=$2 AND p.purpose=$3
        AND ($4=$1 OR EXISTS(SELECT 1 FROM representations d WHERE d.actor=$4 AND d.subject=$1 AND d.scope=r.scope AND d.purpose=$3))`, identity(subject), identity(a.Processor), a.Purpose, identity(a.Actor))
	if err != nil {
		return searchFailure(ctx, err)
	}
	allowed, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return searchFailure(ctx, err)
	}
	if allowed == nil {
		allowed = []string{}
	}
	afterSec, beforeSec := int64(math.MinInt64), int64(math.MaxInt64)
	afterNano, beforeNano := int32(0), int32(999999999)
	if q.After != nil {
		afterSec, afterNano = q.After.Unix(), int32(q.After.Nanosecond())
	}
	if q.Before != nil {
		beforeSec, beforeNano = q.Before.Unix(), int32(q.Before.Nanosecond())
	}
	if q.After != nil && q.Before != nil && q.Before.Before(*q.After) {
		return result, ErrInvalid
	}
	queryExpression := "plainto_tsquery('english',$4)"
	if anyTerm {
		queryExpression = `(SELECT string_agg(quote_literal(term), ' | ')::tsquery
            FROM unnest(tsvector_to_array(to_tsvector('english',$4))) AS term)`
	}
	rows, err = tx.Query(ctx, `SELECT r.payload FROM segments i JOIN records r USING(authority,id)
        LEFT JOIN sessions s ON s.authority=r.authority AND s.id=r.session_id
        WHERE r.authority=$1 AND NOT r.deleted AND r.scopes <@ $2::text[]
        AND COALESCE(s.scopes,ARRAY[]::text[]) <@ $2::text[] AND $3=ANY(r.purposes)
        AND ($3 <> 'enrich' OR r.enrichment_allowed)
        AND (r.observed_seconds,r.observed_nanos) >= ($5::bigint,$6::int)
        AND (r.observed_seconds,r.observed_nanos) <= ($7::bigint,$8::int)
        AND i.search @@ `+queryExpression+`
        ORDER BY ts_rank_cd(i.search,`+queryExpression+`) DESC, r.id LIMIT 101`, identity(s.authority), allowed, a.Purpose, q.Question, afterSec, afterNano, beforeSec, beforeNano)
	if err != nil {
		return searchFailure(ctx, err)
	}
	payloads, err := pgx.CollectRows(rows, pgx.RowTo[[]byte])
	if err != nil {
		return searchFailure(ctx, err)
	}
	for n, payload := range payloads {
		if n == 100 {
			result.Status = "partial"
			result.Gaps = []string{"search budget exhausted"}
			break
		}
		record, err := decode(payload)
		if err != nil {
			return searchFailure(ctx, err)
		}
		h, _, _, _, _ := describe(record)
		record, err = s.read(ctx, tx, a, h.Ref, map[string]bool{})
		if errors.Is(err, ErrDenied) {
			continue
		}
		if err != nil {
			return searchFailure(ctx, err)
		}
		content := ""
		switch v := record.(type) {
		case wire.SourceContext:
			content = v.Content
		case wire.InteractionRecord:
			content = v.Content
		case wire.SessionInput:
			content = v.Content
		default:
			continue
		}
		observed := h.ReceivedAt
		if h.ObservedAt != nil {
			observed = *h.ObservedAt
		}
		result.Matches = append(result.Matches, wire.QueryMatch{Source: h.Ref, Content: content, ObservedAt: observed})
		result.Status = "complete"
		if len(result.Matches) == int(q.Limit) {
			break
		}
	}
	return result, nil
}

// Publish serializes only one authority's feed. Sequence allocation and visible
// feed rows commit together, so late commits cannot fall behind a returned cursor.
func (s *store) Publish(ctx context.Context) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	authority := identity(s.authority)
	if _, err = tx.Exec(ctx, `INSERT INTO feed_counters(authority) VALUES($1) ON CONFLICT DO NOTHING`, authority); err != nil {
		return err
	}
	var position int64
	if err = tx.QueryRow(ctx, `SELECT last_position FROM feed_counters WHERE authority=$1 FOR UPDATE`, authority).Scan(&position); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT id FROM outbox WHERE authority=$1 AND NOT published ORDER BY id LIMIT 1000`, authority)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for _, id := range ids {
		position++
		if _, err = tx.Exec(ctx, `INSERT INTO feed VALUES($1,$2,$3)`, authority, position, id); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE outbox SET published=true WHERE authority=$1 AND id=$2`, authority, id); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE feed_counters SET last_position=$2 WHERE authority=$1`, authority, position); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
