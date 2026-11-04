// Package server implements ConversationService.
package server

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sync"
	"time"

	"github.com/catalystcommunity/magi/lib/authn"
	"github.com/catalystcommunity/magi/lib/rpc"
	api "github.com/catalystcommunity/magi/primitives/conversation/api"
	"github.com/catalystcommunity/magi/primitives/conversation/store"
)

const (
	ScopeActAs = "conversation:act-as"
	ScopeAdmin = "conversation:admin"

	// MaxWait caps wait_ms of list-utterances.
	MaxWait = 30 * time.Second

	defaultLimit = 100
	maxLimit     = 1000
	maxText      = 1 << 20
	maxRefs      = 100
	maxDepth     = 4
	maxMembers   = 100

	TaskVisibility = "visibility"
	TaskLink       = "link"
)

var kindRE = regexp.MustCompile(`^[a-z][a-z-]{1,39}$`)

type Service struct {
	DB  *store.DB
	Now func() time.Time
	// PollInterval is how often a waiting list-utterances reads the
	// database when no notification arrives. The default is one second.
	PollInterval time.Duration

	mu      sync.Mutex
	waiters map[string]chan struct{}
}

var _ api.ConversationService = (*Service)(nil)

// Register adds the service operations to s.
func Register(s *rpc.Server, svc *Service) {
	s.Register("ConversationService", api.ConversationServiceOps(svc))
}

// Listen receives posts from all replicas through PostgreSQL NOTIFY and wakes
// waiting list-utterances calls. Run it for the life of the process.
func (s *Service) Listen(ctx context.Context, onError func(error)) {
	s.DB.Listen(ctx, s.signal, onError)
}

// waitChan returns a channel that closes at the next post to stream.
func (s *Service) waitChan(stream string) <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.waiters == nil {
		s.waiters = map[string]chan struct{}{}
	}
	ch, ok := s.waiters[stream]
	if !ok {
		ch = make(chan struct{})
		s.waiters[stream] = ch
	}
	return ch
}

func (s *Service) signal(stream string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ch, ok := s.waiters[stream]; ok {
		close(ch)
		delete(s.waiters, stream)
	}
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) nowMs() uint64 { return uint64(s.now().UnixMilli()) }

func newID(prefix string) string {
	b := make([]byte, 12)
	rand.Read(b)
	return prefix + base64.RawURLEncoding.EncodeToString(b)
}

func privileged(p authn.Principal) bool { return p.Has(ScopeActAs) || p.Has(ScopeAdmin) }

func limit(v *uint64) int {
	if v == nil || *v == 0 {
		return defaultLimit
	}
	return int(min(*v, maxLimit))
}

func deref[T any](p *T) T {
	var z T
	if p == nil {
		return z
	}
	return *p
}

func ptr[T any](v T) *T { return &v }

func notFound(err error, format string, args ...any) error {
	if errors.Is(err, store.ErrNotFound) {
		return rpc.NotFound(format, args...)
	}
	return err
}

func checkRefs(refs []api.Ref) error {
	if len(refs) > maxRefs {
		return rpc.Invalid("at most %d references", maxRefs)
	}
	for _, r := range refs {
		if r.Kind == "" || r.Ref == "" {
			return rpc.Invalid("reference kind and ref are required")
		}
	}
	return nil
}

// access returns the caller and the identity that a call reads as. A
// privileged caller reads all, or as person through persona. Others read
// as themselves, optionally through persona.
func (s *Service) access(ctx context.Context, person, persona *string) (authn.Principal, store.Access, error) {
	p, err := rpc.Caller(ctx)
	if err != nil {
		return p, store.Access{}, err
	}
	who := deref(person)
	if who != "" && who != p.Subject && !privileged(p) {
		return p, store.Access{}, rpc.Denied("reading as %s needs %s", who, ScopeActAs)
	}
	acc := store.Access{Via: deref(persona)}
	if who == "" {
		if privileged(p) {
			acc.Principals = []string{"*"}
			return p, acc, nil
		}
		who = p.Subject
	}
	groups, err := s.DB.Groups(ctx, who)
	if err != nil {
		return p, acc, err
	}
	acc.Principals = append([]string{who}, groups...)
	return p, acc, nil
}

// readers returns the readers of an utterance in stream st with visibility.
func readers(st api.Stream, visibility string) []string {
	r := []string{st.Person, st.Persona}
	if visibility == "shared" {
		r = append(r, store.AudienceGroup(st.Persona))
	}
	return r
}

// Personas

func (s *Service) PutPersona(ctx context.Context, req api.PutPersonaRequest) (api.Persona, error) {
	p, err := rpc.Caller(ctx)
	if err != nil {
		return api.Persona{}, err
	}
	if !p.Has(ScopeAdmin) {
		return api.Persona{}, rpc.Denied("put-persona needs %s", ScopeAdmin)
	}
	switch {
	case req.Id == "" || req.Name == "":
		return api.Persona{}, rpc.Invalid("id and name are required")
	case !slices.Contains([]string{"never", "private", "public"}, req.Sharing):
		return api.Persona{}, rpc.Invalid("sharing must be never, private, or public")
	case len(req.Members) > maxMembers:
		return api.Persona{}, rpc.Invalid("at most %d members", maxMembers)
	}
	return s.DB.PutPersona(ctx, api.Persona{Id: req.Id, Name: req.Name, Sharing: req.Sharing, Members: req.Members, UpdatedMs: s.nowMs()})
}

func (s *Service) GetPersona(ctx context.Context, req api.GetPersonaRequest) (api.Persona, error) {
	if _, err := rpc.Caller(ctx); err != nil {
		return api.Persona{}, err
	}
	v, err := s.DB.GetPersona(ctx, req.Id)
	return v, notFound(err, "persona %s", req.Id)
}

func (s *Service) ListPersonas(ctx context.Context, req api.ListPersonasRequest) (api.ListPersonasResponse, error) {
	if _, err := rpc.Caller(ctx); err != nil {
		return api.ListPersonasResponse{}, err
	}
	list, err := s.DB.ListPersonas(ctx)
	return api.ListPersonasResponse{Personas: list}, err
}

// Streams

func (s *Service) OpenStream(ctx context.Context, req api.OpenStreamRequest) (api.Stream, error) {
	p, err := rpc.Caller(ctx)
	if err != nil {
		return api.Stream{}, err
	}
	person := deref(req.Person)
	if person == "" {
		person = p.Subject
	}
	if person != p.Subject && !privileged(p) {
		return api.Stream{}, rpc.Denied("a stream of %s needs %s", person, ScopeActAs)
	}
	if _, err := s.DB.GetPersona(ctx, req.Persona); err != nil {
		return api.Stream{}, notFound(err, "persona %s", req.Persona)
	}
	return s.DB.OpenStream(ctx, newID("str_"), person, req.Persona, s.now())
}

func (s *Service) GetStream(ctx context.Context, req api.GetStreamRequest) (api.Stream, error) {
	p, acc, err := s.access(ctx, nil, nil)
	if err != nil {
		return api.Stream{}, err
	}
	return s.stream(ctx, p, acc, req.Id)
}

func (s *Service) ListStreams(ctx context.Context, req api.ListStreamsRequest) (api.ListStreamsResponse, error) {
	p, err := rpc.Caller(ctx)
	if err != nil {
		return api.ListStreamsResponse{}, err
	}
	person := deref(req.Person)
	if person == "" {
		person = p.Subject
	}
	if person != p.Subject && !privileged(p) {
		return api.ListStreamsResponse{}, rpc.Denied("streams of %s need %s", person, ScopeActAs)
	}
	list, err := s.DB.ListStreams(ctx, person)
	return api.ListStreamsResponse{Streams: list}, err
}

// stream returns stream id if the caller can use it: its person, a
// privileged caller, or a member of the audience of its persona.
func (s *Service) stream(ctx context.Context, p authn.Principal, acc store.Access, id string) (api.Stream, error) {
	st, err := s.DB.GetStream(ctx, id)
	if err != nil {
		return st, notFound(err, "stream %s", id)
	}
	if st.Person == p.Subject || slices.Contains(acc.Principals, "*") || slices.Contains(acc.Principals, st.Person) ||
		slices.Contains(acc.Principals, store.AudienceGroup(st.Persona)) {
		return st, nil
	}
	return st, rpc.Denied("stream %s is not visible to %s", id, p.Subject)
}

func defaultKind(p authn.Principal) string {
	if p.Kind == authn.KindUser {
		return "human"
	}
	return "system"
}

// newLink checks l and makes the link record from utterance.
func (s *Service) newLink(ctx context.Context, acc store.Access, utterance string, l api.NewLink, author, state string) (api.Link, error) {
	if !kindRE.MatchString(l.Kind) {
		return api.Link{}, rpc.Invalid("link kind %q must match %s", l.Kind, kindRE)
	}
	if l.Target.Kind == "" || l.Target.Ref == "" {
		return api.Link{}, rpc.Invalid("link target kind and ref are required")
	}
	if l.Target.Kind == "utterance" {
		if l.Target.Ref == utterance {
			return api.Link{}, rpc.Invalid("a link cannot go to its own utterance")
		}
		if _, err := s.DB.GetUtterance(ctx, acc, l.Target.Ref); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return api.Link{}, rpc.Invalid("link target %s is not a readable utterance", l.Target.Ref)
			}
			return api.Link{}, err
		}
	}
	w := 1.0
	if l.Weight != nil {
		w = *l.Weight
	}
	conf := 1.0
	if l.Confidence != nil {
		conf = *l.Confidence
	}
	if w <= 0 || w > 1 || conf < 0 || conf > 1 {
		return api.Link{}, rpc.Invalid("weight must be in (0, 1] and confidence in [0, 1]")
	}
	if l.Span != nil && l.Span.End <= l.Span.Start {
		return api.Link{}, rpc.Invalid("span end must be after start")
	}
	return api.Link{Id: newID("lnk_"), Utterance: utterance, Span: l.Span, Weight: w, Target: l.Target, Kind: l.Kind,
		Confidence: conf, Author: author, Note: l.Note, State: state, CreatedMs: s.nowMs()}, nil
}

func (s *Service) Post(ctx context.Context, req api.PostRequest) (api.Utterance, error) {
	p, acc, err := s.access(ctx, nil, nil)
	if err != nil {
		return api.Utterance{}, err
	}
	st, err := s.stream(ctx, p, acc, req.Stream)
	if err != nil {
		return api.Utterance{}, err
	}
	if st.Person != p.Subject && !privileged(p) {
		return api.Utterance{}, rpc.Denied("only %s can post in stream %s", st.Person, st.Id)
	}
	u := api.Utterance{
		Id: newID("utt_"), Stream: st.Id, Author: p.Subject, AuthorKind: defaultKind(p), Modality: "text",
		Text: req.Text, Audio: req.Audio, TranscriptConfidence: req.TranscriptConfidence, Attachments: req.Attachments,
		GeneratedBy: req.GeneratedBy, Visibility: "private", CreatedMs: s.nowMs(),
	}
	if u.Attachments == nil {
		u.Attachments = []api.Ref{}
	}
	if req.Author != nil && *req.Author != "" {
		u.Author = *req.Author
	}
	if req.AuthorKind != nil && *req.AuthorKind != "" {
		u.AuthorKind = *req.AuthorKind
	}
	if req.Modality != nil && *req.Modality != "" {
		u.Modality = *req.Modality
	}
	if (u.Author != p.Subject || u.AuthorKind != defaultKind(p)) && !privileged(p) {
		return api.Utterance{}, rpc.Denied("author or author_kind override needs %s", ScopeActAs)
	}
	classified := false
	if v := deref(req.Visibility); v != "" {
		if !privileged(p) {
			return api.Utterance{}, rpc.Denied("visibility on post needs %s", ScopeActAs)
		}
		if err := s.checkVisibility(ctx, st, v); err != nil {
			return api.Utterance{}, err
		}
		u.Visibility, u.VisibilityBy, classified = v, p.Subject, true
	}
	switch {
	case !slices.Contains([]string{"human", "persona", "system"}, u.AuthorKind):
		return api.Utterance{}, rpc.Invalid("author_kind must be human, persona, or system")
	case u.Modality != "text" && u.Modality != "voice":
		return api.Utterance{}, rpc.Invalid("modality must be text or voice")
	case u.Text == "" && (u.Audio == nil || *u.Audio == ""):
		return api.Utterance{}, rpc.Invalid("text or audio is required")
	case len(u.Text) > maxText:
		return api.Utterance{}, rpc.Invalid("text is larger than %d bytes", maxText)
	case len(req.Links) > maxRefs:
		return api.Utterance{}, rpc.Invalid("at most %d links", maxRefs)
	}
	if err := checkRefs(u.Attachments); err != nil {
		return api.Utterance{}, err
	}
	links := []api.Link{}
	for _, nl := range req.Links {
		l, err := s.newLink(ctx, acc, u.Id, nl, p.Subject, "current")
		if err != nil {
			return api.Utterance{}, err
		}
		links = append(links, l)
	}
	u, err = s.DB.Post(ctx, u, readers(st, u.Visibility), classified, links)
	if err != nil {
		return u, notFound(err, "stream %s", st.Id)
	}
	s.signal(st.Id)
	return u, nil
}

func (s *Service) checkVisibility(ctx context.Context, st api.Stream, v string) error {
	if v != "private" && v != "shared" {
		return rpc.Invalid("visibility must be private or shared")
	}
	if v == "shared" {
		per, err := s.DB.GetPersona(ctx, st.Persona)
		if err != nil {
			return err
		}
		if per.Sharing == "never" {
			return rpc.Invalid("persona %s never shares", per.Id)
		}
	}
	return nil
}

func (s *Service) ListUtterances(ctx context.Context, req api.ListUtterancesRequest) (api.ListUtterancesResponse, error) {
	p, acc, err := s.access(ctx, req.Person, req.Persona)
	if err != nil {
		return api.ListUtterancesResponse{}, err
	}
	after, before := deref(req.AfterSeq), deref(req.BeforeSeq)
	var wait time.Duration
	if req.WaitMs != nil && before == 0 {
		wait = min(time.Duration(*req.WaitMs)*time.Millisecond, MaxWait)
	}
	poll := s.PollInterval
	if poll <= 0 {
		poll = time.Second
	}
	deadline := time.Now().Add(wait)
	for {
		// Take the wait channel before the read, so a post between the read
		// and the wait is not missed.
		ch := s.waitChan(req.Stream)
		st, err := s.stream(ctx, p, acc, req.Stream)
		if err != nil {
			return api.ListUtterancesResponse{}, err
		}
		list, err := s.DB.ListUtterances(ctx, acc, st.Id, after, before, limit(req.Limit))
		if err != nil {
			return api.ListUtterancesResponse{}, err
		}
		left := time.Until(deadline)
		if len(list) > 0 || left <= 0 {
			ids := make([]string, len(list))
			for i, u := range list {
				ids[i] = u.Id
			}
			links, err := s.DB.Links(ctx, acc, ids, store.LinkFilter{Back: true})
			if err != nil {
				return api.ListUtterancesResponse{}, err
			}
			last := st.LastSeq
			if n := len(list); n > 0 {
				last = max(last, list[n-1].Seq)
			}
			return api.ListUtterancesResponse{Utterances: list, Links: links, LastSeq: last}, nil
		}
		t := time.NewTimer(min(left, poll))
		select {
		case <-ch:
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return api.ListUtterancesResponse{}, ctx.Err()
		}
		t.Stop()
	}
}

func (s *Service) GetUtterance(ctx context.Context, req api.GetUtteranceRequest) (api.Utterance, error) {
	_, acc, err := s.access(ctx, req.Person, req.Persona)
	if err != nil {
		return api.Utterance{}, err
	}
	u, err := s.DB.GetUtterance(ctx, acc, req.Id)
	return u, notFound(err, "utterance %s", req.Id)
}

func (s *Service) GetUtterances(ctx context.Context, req api.GetUtterancesRequest) (api.GetUtterancesResponse, error) {
	_, acc, err := s.access(ctx, req.Person, req.Persona)
	if err != nil {
		return api.GetUtterancesResponse{}, err
	}
	if len(req.Ids) > maxLimit {
		return api.GetUtterancesResponse{}, rpc.Invalid("at most %d ids", maxLimit)
	}
	list, err := s.DB.GetUtterances(ctx, acc, req.Ids)
	return api.GetUtterancesResponse{Utterances: list}, err
}

func (s *Service) Search(ctx context.Context, req api.SearchRequest) (api.SearchResponse, error) {
	_, acc, err := s.access(ctx, req.Person, req.Persona)
	if err != nil {
		return api.SearchResponse{}, err
	}
	hits, err := s.DB.Search(ctx, acc, req.Text, deref(req.BeforeMs), limit(req.Limit))
	return api.SearchResponse{Hits: hits}, err
}

func (s *Service) Trace(ctx context.Context, req api.TraceRequest) (api.TraceResponse, error) {
	_, acc, err := s.access(ctx, req.Person, req.Persona)
	if err != nil {
		return api.TraceResponse{}, err
	}
	start, err := s.DB.GetUtterance(ctx, acc, req.Utterance)
	if err != nil {
		return api.TraceResponse{}, notFound(err, "utterance %s", req.Utterance)
	}
	f := store.LinkFilter{Kinds: req.Kinds}
	switch deref(req.Direction) {
	case "", "back":
		f.Back = true
	case "forward":
		f.Forward = true
	case "both":
		f.Back, f.Forward = true, true
	default:
		return api.TraceResponse{}, rpc.Invalid("direction must be back, forward, or both")
	}
	if deref(req.Tentative) {
		f.States = []string{"current", "tentative"}
	}
	depth := int(min(deref(req.Depth), maxDepth))
	if depth == 0 {
		depth = 2
	}
	seen := map[string]bool{start.Id: true}
	seenLink := map[string]bool{}
	out := api.TraceResponse{Utterances: []api.Utterance{start}, Links: []api.Link{}}
	frontier := []string{start.Id}
	for range depth {
		links, err := s.DB.Links(ctx, acc, frontier, f)
		if err != nil {
			return api.TraceResponse{}, err
		}
		var next []string
		for _, l := range links {
			if seenLink[l.Id] {
				continue
			}
			seenLink[l.Id] = true
			out.Links = append(out.Links, l)
			for _, id := range []string{l.Utterance, l.Target.Ref} {
				if (id == l.Utterance || l.Target.Kind == "utterance") && !seen[id] {
					seen[id] = true
					next = append(next, id)
				}
			}
		}
		if len(next) == 0 {
			break
		}
		us, err := s.DB.GetUtterances(ctx, acc, next)
		if err != nil {
			return api.TraceResponse{}, err
		}
		out.Utterances = append(out.Utterances, us...)
		frontier = next
	}
	return out, nil
}

// subject returns the readable utterance id, its stream, and whether the
// caller is the person of that stream.
func (s *Service) subject(ctx context.Context, id string) (authn.Principal, store.Access, api.Utterance, api.Stream, bool, error) {
	p, acc, err := s.access(ctx, nil, nil)
	if err != nil {
		return p, acc, api.Utterance{}, api.Stream{}, false, err
	}
	u, err := s.DB.GetUtterance(ctx, acc, id)
	if err != nil {
		return p, acc, u, api.Stream{}, false, notFound(err, "utterance %s", id)
	}
	st, err := s.DB.GetStream(ctx, u.Stream)
	if err != nil {
		return p, acc, u, st, false, err
	}
	own := st.Person == p.Subject
	if !own && !privileged(p) {
		return p, acc, u, st, false, rpc.Denied("only %s or a classifier can change utterance %s", st.Person, id)
	}
	return p, acc, u, st, own, nil
}

func (s *Service) PutLink(ctx context.Context, req api.PutLinkRequest) (api.PutLinkResponse, error) {
	p, acc, _, st, own, err := s.subject(ctx, req.Utterance)
	if err != nil {
		return api.PutLinkResponse{}, err
	}
	var old *api.Link
	if req.Replaces != nil {
		o, err := s.DB.GetLink(ctx, acc, *req.Replaces)
		if err != nil || o.Utterance != req.Utterance {
			return api.PutLinkResponse{}, rpc.Invalid("link %s is not a link of utterance %s", *req.Replaces, req.Utterance)
		}
		old = &o
	}
	if own {
		// A link by the person is a correction with full confidence.
		req.Link.Confidence = ptr(1.0)
		l, err := s.newLink(ctx, acc, req.Utterance, req.Link, p.Subject, "current")
		if err != nil {
			return api.PutLinkResponse{}, err
		}
		l.Replaces = req.Replaces
		if err := s.DB.PutLink(ctx, l); err != nil {
			return api.PutLinkResponse{}, err
		}
		if old != nil && old.Author != p.Subject {
			s.learn(ctx, st.Person, TaskLink, req.Utterance, old.Kind, l.Kind, req.Link.Note, false)
			s.DB.CloseOpenReviews(ctx, TaskLink, req.Utterance, &old.Id, s.now())
		}
		return api.PutLinkResponse{Link: l}, nil
	}
	if req.Link.Confidence == nil {
		return api.PutLinkResponse{}, rpc.Invalid("a classifier link needs a confidence")
	}
	cal, err := s.DB.Calibration(ctx, st.Person, TaskLink)
	if err != nil {
		return api.PutLinkResponse{}, err
	}
	state := "current"
	if *req.Link.Confidence < cal.Threshold {
		state = "tentative"
	}
	l, err := s.newLink(ctx, acc, req.Utterance, req.Link, p.Subject, state)
	if err != nil {
		return api.PutLinkResponse{}, err
	}
	if old != nil && old.Author == st.Person {
		return api.PutLinkResponse{}, rpc.Conflict("link %s is a decision of %s; a classifier cannot replace it", old.Id, st.Person)
	}
	if state == "current" {
		l.Replaces = req.Replaces
	}
	if err := s.DB.PutLink(ctx, l); err != nil {
		return api.PutLinkResponse{}, err
	}
	out := api.PutLinkResponse{Link: l}
	if state == "tentative" {
		q := deref(req.Question)
		if q == "" {
			q = fmt.Sprintf("Is this message %s %s %s?", l.Kind, l.Target.Kind, firstNonEmpty(deref(l.Target.Title), l.Target.Ref))
		}
		v := api.Review{Id: newID("rev_"), Reviewer: st.Person, Task: TaskLink, Utterance: req.Utterance, Link: &l.Id, Question: q,
			Proposed: l.Kind, Choices: []string{l.Kind, "unrelated"}, Confidence: l.Confidence, Status: "open", CreatedMs: s.nowMs()}
		if err := s.DB.CreateReview(ctx, v); err != nil {
			return out, err
		}
		out.Review = &v
	}
	return out, nil
}

func (s *Service) RetractLink(ctx context.Context, req api.RetractLinkRequest) (api.Link, error) {
	p, acc, err := s.access(ctx, nil, nil)
	if err != nil {
		return api.Link{}, err
	}
	l, err := s.DB.GetLink(ctx, acc, req.Id)
	if err != nil {
		return l, notFound(err, "link %s", req.Id)
	}
	_, _, _, st, own, err := s.subject(ctx, l.Utterance)
	if err != nil {
		return api.Link{}, err
	}
	if !own && l.Author == st.Person {
		return api.Link{}, rpc.Conflict("link %s is a decision of %s", l.Id, st.Person)
	}
	if own && l.Author != p.Subject {
		s.learn(ctx, st.Person, TaskLink, l.Utterance, l.Kind, "unrelated", req.Note, false)
		s.DB.CloseOpenReviews(ctx, TaskLink, l.Utterance, &l.Id, s.now())
	}
	out, err := s.DB.SetLinkState(ctx, l.Id, "retracted", req.Note)
	return out, notFound(err, "link %s", l.Id)
}

func (s *Service) ListLinks(ctx context.Context, req api.ListLinksRequest) (api.ListLinksResponse, error) {
	_, acc, err := s.access(ctx, req.Person, req.Persona)
	if err != nil {
		return api.ListLinksResponse{}, err
	}
	f := store.LinkFilter{}
	switch deref(req.Direction) {
	case "", "back":
		f.Back = true
	case "forward":
		f.Forward = true
	case "both":
		f.Back, f.Forward = true, true
	default:
		return api.ListLinksResponse{}, rpc.Invalid("direction must be back, forward, or both")
	}
	if deref(req.AllStates) {
		f.States = []string{"current", "tentative", "replaced", "retracted"}
	}
	if _, err := s.DB.GetUtterance(ctx, acc, req.Utterance); err != nil {
		return api.ListLinksResponse{}, notFound(err, "utterance %s", req.Utterance)
	}
	list, err := s.DB.Links(ctx, acc, []string{req.Utterance}, f)
	return api.ListLinksResponse{Links: list}, err
}

func (s *Service) SetVisibility(ctx context.Context, req api.SetVisibilityRequest) (api.SetVisibilityResponse, error) {
	p, _, u, st, own, err := s.subject(ctx, req.Utterance)
	if err != nil {
		return api.SetVisibilityResponse{}, err
	}
	if err := s.checkVisibility(ctx, st, req.Visibility); err != nil {
		return api.SetVisibilityResponse{}, err
	}
	if own {
		if u.VisibilityBy != "" && u.VisibilityBy != p.Subject {
			// A change of a classifier decision is a correction; the same
			// value confirms it.
			s.learn(ctx, st.Person, TaskVisibility, u.Id, u.Visibility, req.Visibility, req.Note, u.Visibility == req.Visibility)
		}
		s.DB.CloseOpenReviews(ctx, TaskVisibility, u.Id, nil, s.now())
		u, err := s.DB.SetVisibility(ctx, u.Id, req.Visibility, readers(st, req.Visibility), p.Subject, ptr(1.0), req.Note, newID("vis_"), s.now())
		return api.SetVisibilityResponse{Utterance: u}, err
	}
	if req.Confidence == nil {
		return api.SetVisibilityResponse{}, rpc.Invalid("a classifier visibility needs a confidence")
	}
	if u.VisibilityBy == st.Person {
		return api.SetVisibilityResponse{}, rpc.Conflict("the visibility of %s is a decision of %s", u.Id, st.Person)
	}
	cal, err := s.DB.Calibration(ctx, st.Person, TaskVisibility)
	if err != nil {
		return api.SetVisibilityResponse{}, err
	}
	// A classifier can make a message more private with no question, but
	// only the person shares it outside the default of the persona: the
	// classifier reads text that an attacker can write, and it gives its own
	// confidence (docs/prompt-injection.md, 5.1).
	persona, err := s.DB.GetPersona(ctx, st.Persona)
	if err != nil {
		return api.SetVisibilityResponse{}, err
	}
	sure := *req.Confidence >= cal.Threshold && persona.Sharing == "public"
	if req.Visibility == "private" || sure {
		u, err := s.DB.SetVisibility(ctx, u.Id, req.Visibility, readers(st, req.Visibility), p.Subject, req.Confidence, req.Note, newID("vis_"), s.now())
		return api.SetVisibilityResponse{Utterance: u}, err
	}
	q := deref(req.Question)
	if q == "" {
		q = "Who can see this message: only you and the persona (private), or the audience of the persona (shared)?"
	}
	v := api.Review{Id: newID("rev_"), Reviewer: st.Person, Task: TaskVisibility, Utterance: u.Id, Question: q, Proposed: req.Visibility,
		Choices: []string{"private", "shared"}, Confidence: *req.Confidence, Status: "open", Note: req.Note, CreatedMs: s.nowMs()}
	if err := s.DB.CreateReview(ctx, v); err != nil {
		return api.SetVisibilityResponse{}, err
	}
	return api.SetVisibilityResponse{Utterance: u, Review: &v}, nil
}

// learn records a decision of the person as an example and moves the
// threshold. Errors are not fatal: the decision itself is stored.
func (s *Service) learn(ctx context.Context, reviewer, task, utterance, proposed, label string, note *string, accepted bool) {
	s.DB.AddExample(ctx, reviewer, api.Example{Task: task, Utterance: utterance, Proposed: proposed, Label: label, Note: note, CreatedMs: s.nowMs()})
	s.DB.Calibrate(ctx, reviewer, task, accepted)
}

func (s *Service) ListReviews(ctx context.Context, req api.ListReviewsRequest) (api.ListReviewsResponse, error) {
	p, acc, err := s.access(ctx, req.Person, nil)
	if err != nil {
		return api.ListReviewsResponse{}, err
	}
	who := deref(req.Person)
	if who == "" {
		who = p.Subject
	}
	status := deref(req.Status)
	if status == "" {
		status = "open"
	}
	if status == "all" {
		status = ""
	}
	list, err := s.DB.ListReviews(ctx, acc, who, status, limit(req.Limit))
	return api.ListReviewsResponse{Reviews: list}, err
}

func (s *Service) AnswerReview(ctx context.Context, req api.AnswerReviewRequest) (api.Review, error) {
	p, acc, err := s.access(ctx, nil, nil)
	if err != nil {
		return api.Review{}, err
	}
	v, err := s.DB.GetReview(ctx, acc, req.Id)
	if err != nil {
		return v, notFound(err, "review %s", req.Id)
	}
	if v.Reviewer != p.Subject {
		return api.Review{}, rpc.Denied("review %s is for %s", v.Id, v.Reviewer)
	}
	if v.Status != "open" {
		return api.Review{}, rpc.Conflict("review %s is %s", v.Id, v.Status)
	}
	if req.Answer == "dismiss" {
		return s.DB.CloseReview(ctx, v.Id, "dismissed", req.Answer, req.Note, s.now())
	}
	switch v.Task {
	case TaskVisibility:
		st, u, err := s.reviewSubject(ctx, v)
		if err != nil {
			return api.Review{}, err
		}
		if err := s.checkVisibility(ctx, st, req.Answer); err != nil {
			return api.Review{}, err
		}
		if _, err := s.DB.SetVisibility(ctx, u.Id, req.Answer, readers(st, req.Answer), p.Subject, ptr(1.0), req.Note, newID("vis_"), s.now()); err != nil {
			return api.Review{}, err
		}
	case TaskLink:
		l, err := s.DB.GetLink(ctx, store.All, deref(v.Link))
		if err != nil {
			return api.Review{}, err
		}
		switch {
		case req.Answer == "unrelated":
			_, err = s.DB.SetLinkState(ctx, l.Id, "retracted", req.Note)
		case req.Answer == l.Kind:
			_, err = s.DB.SetLinkState(ctx, l.Id, "current", nil)
		case kindRE.MatchString(req.Answer):
			// Another kind: the person's link replaces the proposal.
			c := l
			c.Id, c.Kind, c.Author, c.Confidence, c.State, c.Replaces, c.Note, c.CreatedMs = newID("lnk_"), req.Answer, p.Subject, 1, "current", &l.Id, req.Note, s.nowMs()
			err = s.DB.PutLink(ctx, c)
		default:
			return api.Review{}, rpc.Invalid("answer must be a link kind, unrelated, or dismiss")
		}
		if err != nil {
			return api.Review{}, err
		}
	default:
		return api.Review{}, rpc.Invalid("unknown review task %q", v.Task)
	}
	s.learn(ctx, v.Reviewer, v.Task, v.Utterance, v.Proposed, req.Answer, req.Note, req.Answer == v.Proposed)
	return s.DB.CloseReview(ctx, v.Id, "answered", req.Answer, req.Note, s.now())
}

func (s *Service) reviewSubject(ctx context.Context, v api.Review) (api.Stream, api.Utterance, error) {
	u, err := s.DB.GetUtterance(ctx, store.All, v.Utterance)
	if err != nil {
		return api.Stream{}, u, err
	}
	st, err := s.DB.GetStream(ctx, u.Stream)
	return st, u, err
}

func (s *Service) ListExamples(ctx context.Context, req api.ListExamplesRequest) (api.ListExamplesResponse, error) {
	p, acc, err := s.access(ctx, req.Person, nil)
	if err != nil {
		return api.ListExamplesResponse{}, err
	}
	who := deref(req.Person)
	if who == "" {
		who = p.Subject
	}
	n := 20
	if req.Limit != nil && *req.Limit > 0 {
		n = int(min(*req.Limit, 100))
	}
	list, err := s.DB.ListExamples(ctx, acc, who, req.Task, n)
	return api.ListExamplesResponse{Examples: list}, err
}

func (s *Service) GetCalibration(ctx context.Context, req api.GetCalibrationRequest) (api.Calibration, error) {
	p, _, err := s.access(ctx, req.Person, nil)
	if err != nil {
		return api.Calibration{}, err
	}
	who := deref(req.Person)
	if who == "" {
		who = p.Subject
	}
	return s.DB.Calibration(ctx, who, req.Task)
}

func (s *Service) ClaimUnclassified(ctx context.Context, req api.ClaimUnclassifiedRequest) (api.ClaimUnclassifiedResponse, error) {
	p, err := rpc.Caller(ctx)
	if err != nil {
		return api.ClaimUnclassifiedResponse{}, err
	}
	if !privileged(p) {
		return api.ClaimUnclassifiedResponse{}, rpc.Denied("claim-unclassified needs %s", ScopeActAs)
	}
	lease := 2 * time.Minute
	if req.LeaseMs != nil && *req.LeaseMs > 0 {
		lease = min(time.Duration(*req.LeaseMs)*time.Millisecond, time.Hour)
	}
	list, err := s.DB.ClaimUnclassified(ctx, limit(req.Limit), s.now(), lease)
	return api.ClaimUnclassifiedResponse{Utterances: list}, err
}

func (s *Service) MarkClassified(ctx context.Context, req api.MarkClassifiedRequest) (api.Utterance, error) {
	p, err := rpc.Caller(ctx)
	if err != nil {
		return api.Utterance{}, err
	}
	if !privileged(p) {
		return api.Utterance{}, rpc.Denied("mark-classified needs %s", ScopeActAs)
	}
	u, err := s.DB.MarkClassified(ctx, req.Utterance)
	return u, notFound(err, "utterance %s", req.Utterance)
}

func (s *Service) Annotate(ctx context.Context, req api.AnnotateRequest) (api.Annotation, error) {
	p, acc, err := s.access(ctx, nil, nil)
	if err != nil {
		return api.Annotation{}, err
	}
	if _, err := s.DB.GetUtterance(ctx, acc, req.Utterance); err != nil {
		return api.Annotation{}, notFound(err, "utterance %s", req.Utterance)
	}
	if req.Kind == "" {
		return api.Annotation{}, rpc.Invalid("kind is required")
	}
	if len(req.Body) > maxText {
		return api.Annotation{}, rpc.Invalid("body is larger than %d bytes", maxText)
	}
	a := api.Annotation{Id: newID("ann_"), Utterance: req.Utterance, Kind: req.Kind, Author: p.Subject, Body: req.Body, CreatedMs: s.nowMs()}
	return a, s.DB.Annotate(ctx, a)
}

func (s *Service) ListAnnotations(ctx context.Context, req api.ListAnnotationsRequest) (api.ListAnnotationsResponse, error) {
	_, acc, err := s.access(ctx, nil, nil)
	if err != nil {
		return api.ListAnnotationsResponse{}, err
	}
	if _, err := s.DB.GetUtterance(ctx, acc, req.Utterance); err != nil {
		return api.ListAnnotationsResponse{}, notFound(err, "utterance %s", req.Utterance)
	}
	list, err := s.DB.ListAnnotations(ctx, req.Utterance)
	return api.ListAnnotationsResponse{Annotations: list}, err
}

func (s *Service) ImportLegacy(ctx context.Context, req api.ImportLegacyRequest) (api.ImportLegacyResponse, error) {
	p, err := rpc.Caller(ctx)
	if err != nil {
		return api.ImportLegacyResponse{}, err
	}
	if !p.Has(ScopeAdmin) {
		return api.ImportLegacyResponse{}, rpc.Denied("import-legacy needs %s", ScopeAdmin)
	}
	if _, err := s.DB.GetPersona(ctx, req.Persona); err != nil {
		return api.ImportLegacyResponse{}, notFound(err, "persona %s", req.Persona)
	}
	res, err := s.DB.ImportLegacy(ctx, req.Persona, func(person string) []string {
		return readers(api.Stream{Person: person, Persona: req.Persona}, "private")
	}, newID, s.now())
	return api.ImportLegacyResponse{Streams: uint64(res.Streams), Utterances: uint64(res.Utterances), Links: uint64(res.Links)}, err
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}
