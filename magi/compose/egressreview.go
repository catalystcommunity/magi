package compose

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/catalystcommunity/magi/lib/rpc"
	magiapi "github.com/catalystcommunity/magi/magi/api"
	artifactsapi "github.com/catalystcommunity/magi/primitives/artifacts/api"
)

// Egress reviews. The proxy blocks a request at once and reports it; an
// admin decides later what the policy does with the next such requests.
// The store is append-only, in the artifacts service (owner service:magi):
//
//   - a report (label magi.kind=egress-report) for each blocked request,
//     at most one each minute for each key;
//   - a decision (label magi.kind=egress-decision) for each admin decision.
//
// A key is the kind, the host (with the port for a destination), the
// method, and the path. The open review of a key has the reports that are
// newer than the last decision of the key. A decision other than
// "dismiss" is also a rule for the next requests.

const (
	labelKind      = "magi.kind"
	kindReport     = "egress-report"
	kindDecision   = "egress-decision"
	reviewScan     = 1000 // the newest records that a list reads
	reportInterval = time.Minute
	decisionsTTL   = 30 * time.Second
)

var decisions = []string{"allow-host", "allow-path", "allow-layer4", "block", "dismiss"}

// egressReport is the body of a report artifact.
type egressReport struct {
	Key         string                       `json:"key"`
	Kind        string                       `json:"kind"`
	Owner       string                       `json:"owner"`
	Environment string                       `json:"environment"`
	Destination string                       `json:"destination"`
	Method      string                       `json:"method"`
	URL         string                       `json:"url"`
	Headers     []magiapi.EgressReviewHeader `json:"headers"`
	Body        []byte                       `json:"body"`
	Reason      string                       `json:"reason"`
	By          string                       `json:"by"`
	Ms          uint64                       `json:"ms"`
}

// egressDecision is the body of a decision artifact.
type egressDecision struct {
	Key      string `json:"key"`
	Kind     string `json:"kind"`
	Decision string `json:"decision"`
	Host     string `json:"host"` // host, or host:port for a destination
	Method   string `json:"method"`
	Path     string `json:"path"`
	By       string `json:"by"`
	Note     string `json:"note,omitempty"`
	Ms       uint64 `json:"ms"`
}

// verdict is the Evaluate verdict of a rule decision.
func (d *egressDecision) verdict() string {
	if d.Decision == "block" {
		return "block"
	}
	return "pass"
}

// matches reports whether the rule of the decision covers the key parts.
func (d *egressDecision) matches(kind, host, method, path string) bool {
	if d.Kind != kind || d.Decision == "dismiss" || !strings.EqualFold(d.Host, host) {
		return false
	}
	if kind == "destination" {
		return true
	}
	if !strings.EqualFold(d.Method, method) {
		return false
	}
	return d.Decision != "allow-path" || d.Path == path
}

type reviewParts struct{ kind, host, method, path string }

func (p reviewParts) key() string {
	h := sha256.Sum256([]byte(p.kind + "\x00" + strings.ToLower(p.host) + "\x00" + strings.ToUpper(p.method) + "\x00" + p.path))
	return "egr_" + hex.EncodeToString(h[:8])
}

// reviewKey returns the parts of a flow. The path has no query.
func reviewKey(kind string, f magiapi.EgressFlow) reviewParts {
	p := urlPath(f.Url)
	if i := strings.IndexAny(p, "?#"); i >= 0 {
		p = p[:i]
	}
	host := flowHost(f)
	if kind == "destination" {
		host = f.Destination
	}
	return reviewParts{kind: kind, host: host, method: strings.ToUpper(f.Method), path: p}
}

// egressReviews caches the decisions and limits the reports.
type egressReviews struct {
	mu       sync.Mutex
	loaded   time.Time
	rules    []egressDecision // newest first
	reported map[string]time.Time
}

func (s *System) reviews() *egressReviews { return s.egressReviews }

func (r *egressReviews) load(ctx context.Context, s *System, force bool) []egressDecision {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !force && time.Since(r.loaded) < decisionsTTL {
		return r.rules
	}
	var out []egressDecision
	err := listRecords(ctx, s, kindDecision, func(_ artifactsapi.Artifact, b []byte) {
		var d egressDecision
		if json.Unmarshal(b, &d) == nil {
			out = append(out, d)
		}
	})
	if err != nil {
		s.Log.Warn("egress decisions", "err", err)
		return r.rules // keep the last good list
	}
	r.rules, r.loaded = out, time.Now()
	return out
}

// decided returns the newest rule decision that covers p, or nil. A
// dismiss cancels the older decisions of its own review only.
func (r *egressReviews) decided(ctx context.Context, s *System, p reviewParts) *egressDecision {
	dismissed := map[string]bool{}
	for _, d := range r.load(ctx, s, false) {
		if dismissed[d.Key] {
			continue
		}
		if d.Decision == "dismiss" {
			dismissed[d.Key] = true
			continue
		}
		if d.matches(p.kind, p.host, p.method, p.path) {
			return &d
		}
	}
	return nil
}

// listRecords calls f for each record of a kind, newest first.
func listRecords(ctx context.Context, s *System, kind string, f func(artifactsapi.Artifact, []byte)) error {
	sctx := s.AsService(ctx)
	res, err := s.Artifacts.List(sctx, artifactsapi.ListRequest{LabelKey: ptr(labelKind), LabelValue: ptr(kind), Limit: ptr(uint64(reviewScan))})
	if err != nil {
		return err
	}
	for _, a := range res.Artifacts {
		g, err := s.Artifacts.Get(sctx, artifactsapi.GetRequest{Digest: a.Digest})
		if err != nil {
			return err
		}
		f(a, g.Body)
	}
	return nil
}

func putRecord(ctx context.Context, s *System, kind, key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = s.Artifacts.Put(s.AsService(ctx), artifactsapi.PutRequest{
		MediaType: "application/json", Body: b, Labels: map[string]string{labelKind: kind, "magi.review": key},
	})
	return err
}

func nowMs() uint64 { return uint64(time.Now().UnixMilli()) }

// Destination tells the proxy whether a decided review permits a
// destination that its policy does not permit.
func (e *Egress) Destination(ctx context.Context, req magiapi.EgressDestinationRequest) (magiapi.EgressDestinationResponse, error) {
	if err := egressCaller(ctx); err != nil {
		return magiapi.EgressDestinationResponse{}, err
	}
	d := e.S.reviews().decided(ctx, e.S, reviewParts{kind: "destination", host: req.Destination})
	out := magiapi.EgressDestinationResponse{Allowed: d != nil && d.Decision != "block"}
	if d != nil && d.Decision == "allow-layer4" {
		out.Layer4 = ptr(true)
	}
	return out, nil
}

// Report records a blocked flow for review.
func (e *Egress) Report(ctx context.Context, req magiapi.EgressReportRequest) (magiapi.EgressReportResponse, error) {
	if err := egressCaller(ctx); err != nil {
		return magiapi.EgressReportResponse{}, err
	}
	if req.Kind != "request" && req.Kind != "destination" {
		return magiapi.EgressReportResponse{}, rpc.Invalid(`kind must be "request" or "destination"`)
	}
	p := reviewKey(req.Kind, req.Flow)
	key := p.key()
	r := e.S.reviews()
	if d := r.decided(ctx, e.S, p); d != nil && d.Decision == "block" {
		return magiapi.EgressReportResponse{Review: key}, nil // decided: no new review
	}
	r.mu.Lock()
	last := r.reported[key]
	skip := time.Since(last) < reportInterval
	if !skip {
		r.reported[key] = time.Now()
	}
	r.mu.Unlock()
	if skip {
		return magiapi.EgressReportResponse{Review: key}, nil
	}
	f := req.Flow
	hs := make([]magiapi.EgressReviewHeader, 0, len(f.Headers))
	for _, h := range f.Headers {
		hs = append(hs, magiapi.EgressReviewHeader{Name: h.Name, Value: trunc(h.Value, 500)})
	}
	body := f.Body
	if len(body) > 8<<10 {
		body = body[:8<<10]
	}
	rep := egressReport{Key: key, Kind: req.Kind, Owner: f.Owner, Environment: f.Environment, Destination: f.Destination,
		Method: p.method, URL: f.Url, Headers: hs, Body: body, Reason: trunc(req.Reason, 500), By: trunc(req.By, 200), Ms: nowMs()}
	if err := putRecord(ctx, e.S, kindReport, key, rep); err != nil {
		return magiapi.EgressReportResponse{}, err
	}
	e.S.Log.Info("egress review", "review", key, "kind", req.Kind, "destination", f.Destination, "method", p.method, "by", req.By)
	return magiapi.EgressReportResponse{Review: key}, nil
}

// egressReviewList builds the reviews from the newest records.
func (s *System) egressReviewList(ctx context.Context) ([]magiapi.EgressReview, error) {
	decided := map[string]egressDecision{} // the newest decision of each key
	if err := listRecords(ctx, s, kindDecision, func(_ artifactsapi.Artifact, b []byte) {
		var d egressDecision
		if json.Unmarshal(b, &d) == nil {
			if _, ok := decided[d.Key]; !ok {
				decided[d.Key] = d
			}
		}
	}); err != nil {
		return nil, err
	}
	byKey := map[string]*magiapi.EgressReview{}
	var order []string
	err := listRecords(ctx, s, kindReport, func(_ artifactsapi.Artifact, b []byte) {
		var r egressReport
		if json.Unmarshal(b, &r) != nil {
			return
		}
		d, hasD := decided[r.Key]
		state := "open"
		if hasD && d.Ms >= r.Ms {
			state = "decided"
		}
		id := r.Key + ":" + state
		if v := byKey[id]; v != nil {
			v.Count++
			v.FirstMs = min(v.FirstMs, r.Ms)
			return
		}
		v := &magiapi.EgressReview{Id: r.Key, Kind: r.Kind, State: state, Owner: r.Owner, Environment: r.Environment,
			Destination: r.Destination, Method: r.Method, Url: r.URL, Headers: r.Headers, Body: r.Body,
			Reason: r.Reason, By: r.By, Count: 1, FirstMs: r.Ms, LastMs: r.Ms}
		if v.Headers == nil {
			v.Headers = []magiapi.EgressReviewHeader{}
		}
		if v.Body == nil {
			v.Body = []byte{}
		}
		if state == "decided" {
			v.Decision, v.DecidedBy = ptr(d.Decision), ptr(d.By)
			if d.Note != "" {
				v.Note = ptr(d.Note)
			}
		}
		byKey[id] = v
		order = append(order, id)
	})
	if err != nil {
		return nil, err
	}
	out := make([]magiapi.EgressReview, 0, len(order))
	for _, id := range order {
		out = append(out, *byKey[id])
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].LastMs > out[j].LastMs })
	return out, nil
}

// ListEgressReviews lists the reviews for admins, newest first.
func (m *Service) ListEgressReviews(ctx context.Context, req magiapi.ListEgressReviewsRequest) (magiapi.ListEgressReviewsResponse, error) {
	if _, admin := m.S.isAdmin(ctx); !admin {
		return magiapi.ListEgressReviewsResponse{}, rpc.Denied("only an admin reviews egress requests")
	}
	state := "open"
	if req.State != nil {
		state = *req.State
	}
	if state != "open" && state != "decided" {
		return magiapi.ListEgressReviewsResponse{}, rpc.Invalid(`state must be "open" or "decided"`)
	}
	all, err := m.S.egressReviewList(ctx)
	if err != nil {
		return magiapi.ListEgressReviewsResponse{}, err
	}
	limit := 50
	if req.Limit != nil && *req.Limit > 0 {
		limit = int(min(*req.Limit, 500))
	}
	out := magiapi.ListEgressReviewsResponse{Reviews: []magiapi.EgressReview{}}
	for _, v := range all {
		if v.State == state && len(out.Reviews) < limit {
			out.Reviews = append(out.Reviews, v)
		}
	}
	return out, nil
}

// DecideEgressReview records an admin decision; it is a rule for the next
// requests unless it is "dismiss". A new decision of a decided review
// replaces its rule.
func (m *Service) DecideEgressReview(ctx context.Context, req magiapi.DecideEgressReviewRequest) (magiapi.DecideEgressReviewResponse, error) {
	sub, admin := m.S.isAdmin(ctx)
	if !admin {
		return magiapi.DecideEgressReviewResponse{}, rpc.Denied("only an admin reviews egress requests")
	}
	if !slices.Contains(decisions, req.Decision) {
		return magiapi.DecideEgressReviewResponse{}, rpc.Invalid("decision must be one of %s", strings.Join(decisions, ", "))
	}
	all, err := m.S.egressReviewList(ctx)
	if err != nil {
		return magiapi.DecideEgressReviewResponse{}, err
	}
	// The open review first; a decided review can take a new decision,
	// which replaces its rule ("dismiss" removes it).
	i := slices.IndexFunc(all, func(v magiapi.EgressReview) bool { return v.Id == req.Id && v.State == "open" })
	if i < 0 {
		i = slices.IndexFunc(all, func(v magiapi.EgressReview) bool { return v.Id == req.Id })
	}
	if i < 0 {
		return magiapi.DecideEgressReviewResponse{}, rpc.NotFound("no egress review %s", req.Id)
	}
	v := all[i]
	p := reviewKey(v.Kind, magiapi.EgressFlow{Destination: v.Destination, Method: v.Method, Url: v.Url})
	if v.Kind == "destination" && req.Decision == "allow-path" {
		return magiapi.DecideEgressReviewResponse{}, rpc.Invalid(`a destination review takes "allow-host", "allow-layer4", "block", or "dismiss"`)
	}
	if v.Kind != "destination" && req.Decision == "allow-layer4" {
		return magiapi.DecideEgressReviewResponse{}, rpc.Invalid(`"allow-layer4" is only for a destination review`)
	}
	d := egressDecision{Key: v.Id, Kind: v.Kind, Decision: req.Decision, Host: p.host, Method: p.method, Path: p.path, By: sub, Ms: nowMs()}
	if req.Note != nil {
		d.Note = trunc(*req.Note, 500)
	}
	if err := putRecord(ctx, m.S, kindDecision, v.Id, d); err != nil {
		return magiapi.DecideEgressReviewResponse{}, err
	}
	m.S.reviews().load(ctx, m.S, true)
	m.S.Log.Info("egress review decided", "review", v.Id, "decision", req.Decision, "by", sub)
	out := magiapi.DecideEgressReviewResponse{Id: v.Id, Decision: req.Decision}
	if r := d.describe(); r != "" {
		out.Rule = ptr(r)
	}
	return out, nil
}

// describe is the rule in words, or "" for dismiss.
func (d *egressDecision) describe() string {
	what := map[string]string{"allow-host": "pass", "allow-path": "pass", "allow-layer4": "pass", "block": "block"}[d.Decision]
	switch {
	case what == "":
		return ""
	case d.Decision == "allow-layer4":
		return "permit the destination " + d.Host + " at layer 4 (not inspected)"
	case d.Kind == "destination" && what == "pass":
		return "permit the destination " + d.Host
	case d.Kind == "destination":
		return "block the destination " + d.Host + " with no new review"
	case d.Decision == "allow-path":
		return fmt.Sprintf("%s %s requests to %s%s", what, d.Method, d.Host, d.Path)
	default:
		return fmt.Sprintf("%s %s requests to %s", what, d.Method, d.Host)
	}
}
