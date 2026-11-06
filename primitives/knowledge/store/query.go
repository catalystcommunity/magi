package store

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"

	api "github.com/catalystcommunity/magi/primitives/knowledge/api"
)

// SearchInput is a validated search request.
type SearchInput struct {
	Text           string
	Types          []string
	Kind, Relation string
	At             *uint64
	IncludeEnded   bool
	Limit          int
}

var word = regexp.MustCompile(`[\p{L}\p{N}]+`)

// orQuery is a tsquery text that matches any of the words of text. A word
// of three or more letters also matches as a prefix ("vet" finds
// "veterinarian").
func orQuery(text string) string {
	ws := word.FindAllString(strings.ToLower(text), 32)
	for i, w := range ws {
		if len([]rune(w)) >= 3 {
			ws[i] = w + ":*"
		}
	}
	return strings.Join(ws, " | ")
}

// claimTime is the SQL condition on the time and status of claims c.
func (s *Store) claimTime(at *uint64, includeEnded bool, args *[]any) string {
	switch {
	case at != nil:
		*args = append(*args, *at)
		n := len(*args)
		return fmt.Sprintf(`c.status IN ('current', 'ended') AND (c.valid_from_ms IS NULL OR c.valid_from_ms <= $%d) AND (c.valid_to_ms IS NULL OR c.valid_to_ms > $%d)`, n, n)
	case includeEnded:
		return `c.status IN ('current', 'ended')`
	}
	*args = append(*args, s.now())
	return fmt.Sprintf(`c.status = 'current' AND (c.valid_to_ms IS NULL OR c.valid_to_ms > $%d)`, len(*args))
}

const latestProcedure = `NOT r.retracted AND NOT EXISTS (SELECT 1 FROM procedures n WHERE n.name = r.name AND n.owners = r.owners AND n.revision > r.revision AND NOT n.retracted)`

// Search finds records with any of the words of the text (full-text
// search), and entities whose label is similar to a part of the text.
// Without text it gives the records that changed last.
func (s *Store) Search(ctx context.Context, w Who, in SearchInput) (api.SearchResponse, error) {
	out := api.SearchResponse{Hits: []api.Hit{}, Entities: []api.Entity{}, Claims: []api.Claim{}, Episodes: []api.Episode{}, Procedures: []api.Procedure{}}
	want := func(t string) bool { return len(in.Types) == 0 || slices.Contains(in.Types, t) }
	q := orQuery(in.Text)
	type part struct{ typ, table, alias, cond, score string }
	var parts []part
	if want("entity") {
		cond := `e.status = 'current'`
		if in.Kind != "" {
			cond += ` AND e.kind = $KIND`
		}
		parts = append(parts, part{"entity", "entities", "e", cond,
			`ts_rank(e.search, to_tsquery('english', $Q)) + CASE WHEN length(e.label) >= 3 THEN 2 * word_similarity(lower(e.label), lower($TEXT)) ELSE 0 END`})
	}
	if want("claim") {
		cond := "CLAIMTIME"
		if in.Relation != "" {
			cond += ` AND c.relation = $REL`
		}
		parts = append(parts, part{"claim", "claims", "c", cond, `ts_rank(c.search, to_tsquery('english', $Q))`})
	}
	if want("episode") {
		cond := `p.status = 'current'`
		if in.Kind != "" {
			cond += ` AND p.kind = $KIND`
		}
		parts = append(parts, part{"episode", "episodes", "p", cond, `ts_rank(p.search, to_tsquery('english', $Q))`})
	}
	if want("procedure") {
		parts = append(parts, part{"procedure", "procedures", "r", latestProcedure, `ts_rank(r.search, to_tsquery('english', $Q))`})
	}
	for _, p := range parts {
		args := []any{}
		named := func(v any) string { args = append(args, v); return fmt.Sprintf("$%d", len(args)) }
		cond := p.cond
		if strings.Contains(cond, "CLAIMTIME") {
			cond = strings.Replace(cond, "CLAIMTIME", s.claimTime(in.At, in.IncludeEnded, &args), 1)
		}
		if strings.Contains(cond, "$KIND") {
			cond = strings.ReplaceAll(cond, "$KIND", named(in.Kind))
		}
		if strings.Contains(cond, "$REL") {
			cond = strings.ReplaceAll(cond, "$REL", named(in.Relation))
		}
		cond += " AND " + access(w, p.alias, false, &args)
		var sql string
		if q == "" {
			sql = fmt.Sprintf(`SELECT %s.id, 0::float8 FROM %s %s WHERE %s ORDER BY %s.updated_ms DESC LIMIT %d`, p.alias, p.table, p.alias, cond, p.alias, in.Limit)
		} else {
			score := strings.ReplaceAll(p.score, "$Q", named(q))
			if strings.Contains(score, "$TEXT") {
				score = strings.ReplaceAll(score, "$TEXT", named(in.Text))
			}
			match := fmt.Sprintf(`%s.search @@ to_tsquery('english', %s)`, p.alias, named(q))
			if p.typ == "entity" {
				match = fmt.Sprintf(`(%s OR (length(e.label) >= 3 AND word_similarity(lower(e.label), lower(%s)) >= 0.6))`, match, named(in.Text))
			}
			sql = fmt.Sprintf(`SELECT id, score FROM (SELECT %s.id, (%s)::float8 AS score FROM %s %s WHERE %s AND %s) x ORDER BY score DESC LIMIT %d`,
				p.alias, score, p.table, p.alias, cond, match, in.Limit)
		}
		rows, err := s.DB.Query(ctx, sql, args...)
		if err != nil {
			return out, err
		}
		for rows.Next() {
			h := api.Hit{Type: p.typ}
			if err := rows.Scan(&h.Id, &h.Score); err != nil {
				rows.Close()
				return out, err
			}
			out.Hits = append(out.Hits, h)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return out, err
		}
	}
	slices.SortStableFunc(out.Hits, func(a, b api.Hit) int {
		switch {
		case a.Score > b.Score:
			return -1
		case a.Score < b.Score:
			return 1
		}
		return 0
	})
	if len(out.Hits) > in.Limit {
		out.Hits = out.Hits[:in.Limit]
	}
	byType := map[string][]string{}
	for _, h := range out.Hits {
		byType[h.Type] = append(byType[h.Type], h.Id)
	}
	var err error
	all := Who{All: true}
	if out.Claims, err = s.claims(ctx, all, `c.id = ANY($1)`, byType["claim"]); err != nil {
		return out, err
	}
	if out.Episodes, err = collect(ctx, s.DB, scanEpisode, `SELECT `+episodeCols+` FROM episodes p WHERE p.id = ANY($1)`, nonNil(byType["episode"])); err != nil {
		return out, err
	}
	if out.Procedures, err = collect(ctx, s.DB, scanProcedure, `SELECT `+procedureCols+` FROM procedures r WHERE r.id = ANY($1)`, nonNil(byType["procedure"])); err != nil {
		return out, err
	}
	ents := byType["entity"]
	for _, c := range out.Claims {
		ents = append(ents, c.Subject)
		if c.Object != nil {
			ents = append(ents, *c.Object)
		}
	}
	for _, e := range out.Episodes {
		ents = append(ents, e.Entities...)
	}
	out.Entities, err = s.Entities(ctx, s.DB, w, ents)
	return out, err
}

func (s *Store) claims(ctx context.Context, w Who, cond string, args ...any) ([]api.Claim, error) {
	if len(args) == 1 {
		if v, ok := args[0].([]string); ok {
			args[0] = nonNil(v)
		}
	}
	cond += " AND " + access(w, "c", false, &args)
	cs, err := collect(ctx, s.DB, scanClaim, `SELECT `+claimCols+` FROM claims c WHERE `+cond+` ORDER BY c.created_ms`, args...)
	return s.ended(cs), err
}

// About gives an entity with its claims, the entities that they name, its
// episodes and procedures, the evidence, and the open disputes.
func (s *Store) About(ctx context.Context, w Who, id string, at *uint64, includeEnded bool) (api.AboutResponse, error) {
	var out api.AboutResponse
	e, err := s.Entity(ctx, s.DB, w, id)
	if err != nil {
		return out, err
	}
	out.Entity = e
	args := []any{e.Id}
	cond := `(c.subject = $1 OR c.object = $1) AND ` + s.claimTime(at, includeEnded, &args)
	if out.Claims, err = s.claims(ctx, w, cond, args...); err != nil {
		return out, err
	}
	var other, subjects []string
	subjects = append(subjects, e.Id)
	for _, c := range out.Claims {
		subjects = append(subjects, c.Id)
		other = append(other, c.Subject)
		if c.Object != nil {
			other = append(other, *c.Object)
		}
	}
	other = slices.DeleteFunc(Set(other), func(x string) bool { return x == e.Id })
	if out.Related, err = s.Entities(ctx, s.DB, w, other); err != nil {
		return out, err
	}
	args = []any{e.Id}
	if out.Episodes, err = collect(ctx, s.DB, scanEpisode, `SELECT `+episodeCols+` FROM episodes p WHERE $1 = ANY(p.entities) AND p.status = 'current' AND `+
		access(w, "p", false, &args)+` ORDER BY p.started_ms DESC LIMIT 20`, args...); err != nil {
		return out, err
	}
	args = []any{e.Id}
	if out.Procedures, err = collect(ctx, s.DB, scanProcedure, `SELECT `+procedureCols+` FROM procedures r WHERE $1 = ANY(r.entities) AND `+latestProcedure+` AND `+
		access(w, "r", false, &args)+` ORDER BY r.updated_ms DESC LIMIT 20`, args...); err != nil {
		return out, err
	}
	if out.Evidence, err = collect(ctx, s.DB, scanEvidence, `SELECT `+evidenceCols+` FROM evidence v WHERE v.subject = ANY($1) AND NOT v.retracted ORDER BY v.created_ms`, subjects); err != nil {
		return out, err
	}
	args = []any{subjects, w.Subject}
	out.Disputes, err = collect(ctx, s.DB, scanDispute, `SELECT `+disputeCols+` FROM disputes d WHERE d.record = ANY($1) AND d.state = 'open'
		AND (d.by_subject = $2 OR `+access(w, "d", true, &args)+`) ORDER BY d.created_ms`, args...)
	return out, err
}

// Neighbors gives the entities at most depth current claims from an entity,
// and the claims between them.
func (s *Store) Neighbors(ctx context.Context, w Who, id, relation string, depth, limit int) (api.NeighborsResponse, error) {
	out := api.NeighborsResponse{Entities: []api.Entity{}, Claims: []api.Claim{}}
	e, err := s.Entity(ctx, s.DB, w, id)
	if err != nil {
		return out, err
	}
	args := []any{e.Id, depth, relation}
	vis := access(w, "c", false, &args)
	t := s.claimTime(nil, false, &args)
	args = append(args, limit)
	rows, err := s.DB.Query(ctx, fmt.Sprintf(`WITH RECURSIVE hood(id, d) AS (
			SELECT $1::text, 0
			UNION
			SELECT CASE WHEN c.subject = h.id THEN c.object ELSE c.subject END, h.d + 1
			FROM hood h JOIN claims c ON (c.subject = h.id OR c.object = h.id) AND c.object IS NOT NULL
				AND ($3 = '' OR c.relation = $3) AND %s AND %s
			WHERE h.d < $2
		) SELECT id FROM hood GROUP BY id ORDER BY min(d) LIMIT $%d`, vis, t, len(args)), args...)
	if err != nil {
		return out, err
	}
	var ids []string
	for rows.Next() {
		var x string
		if err := rows.Scan(&x); err != nil {
			rows.Close()
			return out, err
		}
		ids = append(ids, x)
	}
	rows.Close()
	if out.Entities, err = s.Entities(ctx, s.DB, w, ids); err != nil {
		return out, err
	}
	args = []any{nonNil(ids), relation}
	out.Claims, err = s.claims(ctx, w, `c.subject = ANY($1) AND c.object = ANY($1) AND ($2 = '' OR c.relation = $2) AND `+s.claimTime(nil, false, &args), args...)
	return out, err
}

// Timeline gives the episodes, claims, and changes in a period, of one
// entity or of all.
func (s *Store) Timeline(ctx context.Context, w Who, entity string, from, to uint64, limit int) (api.TimelineResponse, error) {
	out := api.TimelineResponse{}
	var err error
	args := []any{from, to, entity}
	if out.Episodes, err = collect(ctx, s.DB, scanEpisode, `SELECT `+episodeCols+` FROM episodes p WHERE p.status = 'current'
		AND p.started_ms < $2 AND coalesce(p.ended_ms, p.started_ms) >= $1 AND ($3 = '' OR $3 = ANY(p.entities)) AND `+access(w, "p", false, &args)+
		fmt.Sprintf(` ORDER BY p.started_ms LIMIT %d`, limit), args...); err != nil {
		return out, err
	}
	args = []any{from, to, entity}
	if out.Claims, err = s.claims(ctx, w, `c.status <> 'retracted' AND ($3 = '' OR c.subject = $3 OR c.object = $3)
		AND (c.created_ms >= $1::bigint AND c.created_ms < $2::bigint OR c.valid_from_ms >= $1 AND c.valid_from_ms < $2 OR c.valid_to_ms >= $1 AND c.valid_to_ms < $2)`, args...); err != nil {
		return out, err
	}
	if len(out.Claims) > limit {
		out.Claims = out.Claims[:limit]
	}
	args = []any{from, to, entity}
	out.Changes, err = collect(ctx, s.DB, scanChange, `SELECT `+changeCols+` FROM changes h WHERE h.at_ms >= $1 AND h.at_ms < $2
		AND ($3 = '' OR h.record = $3 OR h.record IN (SELECT id FROM claims WHERE subject = $3 OR object = $3)) AND `+access(w, "h", true, &args)+
		fmt.Sprintf(` ORDER BY h.at_ms LIMIT %d`, limit), args...)
	return out, err
}

// History gives the changes of a record. The caller must own it.
func (s *Store) History(ctx context.Context, w Who, id string) ([]api.Change, error) {
	args := []any{id}
	return collect(ctx, s.DB, scanChange, `SELECT `+changeCols+` FROM changes h WHERE h.record = $1 AND `+access(w, "h", true, &args)+` ORDER BY h.at_ms, h.id`, args...)
}

// Get gives one visible record of any type.
func (s *Store) Get(ctx context.Context, w Who, id string) (api.GetResponse, error) {
	var out api.GetResponse
	t, err := s.TypeOf(ctx, s.DB, id)
	if err != nil {
		return out, err
	}
	switch t {
	case "entity":
		e, err := s.Entity(ctx, s.DB, w, id)
		out.Entity = &e
		return out, err
	case "claim":
		c, err := s.Claim(ctx, s.DB, w, id)
		out.Claim = &c
		return out, err
	case "episode":
		e, err := s.Episode(ctx, s.DB, w, id)
		out.Episode = &e
		return out, err
	case "procedure":
		p, err := s.Procedure(ctx, s.DB, w, id)
		out.Procedure = &p
		return out, err
	}
	v, err := s.EvidenceRecord(ctx, s.DB, w, id)
	out.Evidence = &v
	return out, err
}
