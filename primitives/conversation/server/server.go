// Package server implements ConversationService.
package server

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
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

	defaultLimit    = 100
	maxLimit        = 1000
	maxParticipants = 100
	maxText         = 1 << 20
	maxRefs         = 100
)

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

// waitChan returns a channel that closes at the next post to conv.
func (s *Service) waitChan(conv string) <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.waiters == nil {
		s.waiters = map[string]chan struct{}{}
	}
	ch, ok := s.waiters[conv]
	if !ok {
		ch = make(chan struct{})
		s.waiters[conv] = ch
	}
	return ch
}

func (s *Service) signal(conv string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ch, ok := s.waiters[conv]; ok {
		close(ch)
		delete(s.waiters, conv)
	}
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func newID(prefix string) string {
	b := make([]byte, 12)
	rand.Read(b)
	return prefix + base64.RawURLEncoding.EncodeToString(b)
}

func privileged(p authn.Principal) bool { return p.Has(ScopeActAs) || p.Has(ScopeAdmin) }

func canSee(p authn.Principal, c api.Conversation) bool {
	return c.Owner == p.Subject || slices.Contains(c.Participants, p.Subject) || privileged(p)
}

func limit(v *uint64) int {
	if v == nil || *v == 0 {
		return defaultLimit
	}
	return int(min(*v, maxLimit))
}

func notFound(err error, format string, args ...any) error {
	if errors.Is(err, store.ErrNotFound) {
		return rpc.NotFound(format, args...)
	}
	return err
}

func checkRefs(refs []api.Attachment) error {
	if len(refs) > maxRefs {
		return rpc.Invalid("at most %d references", maxRefs)
	}
	for _, r := range refs {
		if r.Kind == "" || r.Ref == "" {
			return rpc.Invalid("attachment kind and ref are required")
		}
	}
	return nil
}

// conversation returns conversation id if the caller can see it.
func (s *Service) conversation(ctx context.Context, id string) (authn.Principal, api.Conversation, error) {
	p, err := rpc.Caller(ctx)
	if err != nil {
		return p, api.Conversation{}, err
	}
	c, err := s.DB.GetConversation(ctx, id)
	if err != nil {
		return p, c, notFound(err, "conversation %s", id)
	}
	if !canSee(p, c) {
		return p, c, rpc.Denied("conversation %s is not visible to %s", id, p.Subject)
	}
	return p, c, nil
}

func (s *Service) CreateConversation(ctx context.Context, req api.CreateConversationRequest) (api.Conversation, error) {
	p, err := rpc.Caller(ctx)
	if err != nil {
		return api.Conversation{}, err
	}
	owner := p.Subject
	if req.Owner != nil && *req.Owner != "" && *req.Owner != p.Subject {
		if !privileged(p) {
			return api.Conversation{}, rpc.Denied("owner override needs %s", ScopeActAs)
		}
		owner = *req.Owner
	}
	if len(req.Participants) > maxParticipants {
		return api.Conversation{}, rpc.Invalid("at most %d participants", maxParticipants)
	}
	parts := []string{}
	for _, x := range req.Participants {
		if x == "" {
			return api.Conversation{}, rpc.Invalid("participant must not be empty")
		}
		if !slices.Contains(parts, x) {
			parts = append(parts, x)
		}
	}
	now := uint64(s.now().UnixMilli())
	c := api.Conversation{Id: newID("cnv_"), Owner: owner, Participants: parts, CreatedMs: now, UpdatedMs: now}
	if req.Title != nil {
		c.Title = *req.Title
	}
	return c, s.DB.CreateConversation(ctx, c)
}

func (s *Service) GetConversation(ctx context.Context, req api.GetConversationRequest) (api.Conversation, error) {
	_, c, err := s.conversation(ctx, req.Id)
	return c, err
}

func (s *Service) ListConversations(ctx context.Context, req api.ListConversationsRequest) (api.ListConversationsResponse, error) {
	p, err := rpc.Caller(ctx)
	if err != nil {
		return api.ListConversationsResponse{}, err
	}
	var before uint64
	if req.BeforeMs != nil {
		before = *req.BeforeMs
	}
	list, err := s.DB.ListConversations(ctx, p.Subject, before, limit(req.Limit))
	return api.ListConversationsResponse{Conversations: list}, err
}

func defaultKind(p authn.Principal) string {
	if p.Kind == authn.KindUser {
		return "human"
	}
	return "system"
}

func (s *Service) Post(ctx context.Context, req api.PostRequest) (api.Utterance, error) {
	p, c, err := s.conversation(ctx, req.Conversation)
	if err != nil {
		return api.Utterance{}, err
	}
	u := api.Utterance{
		Id: newID("utt_"), Conversation: c.Id, Author: p.Subject, AuthorKind: defaultKind(p), Modality: "text",
		Text: req.Text, Audio: req.Audio, TranscriptConfidence: req.TranscriptConfidence, ReplyTo: req.ReplyTo,
		Sessions: req.Sessions, Attachments: req.Attachments, GeneratedBy: req.GeneratedBy, CreatedMs: uint64(s.now().UnixMilli()),
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
	switch {
	case !slices.Contains([]string{"human", "persona", "system"}, u.AuthorKind):
		return api.Utterance{}, rpc.Invalid("author_kind must be human, persona, or system")
	case u.Modality != "text" && u.Modality != "voice":
		return api.Utterance{}, rpc.Invalid("modality must be text or voice")
	case u.Text == "" && (u.Audio == nil || *u.Audio == ""):
		return api.Utterance{}, rpc.Invalid("text or audio is required")
	case len(u.Text) > maxText:
		return api.Utterance{}, rpc.Invalid("text is larger than %d bytes", maxText)
	case len(u.Sessions) > maxRefs:
		return api.Utterance{}, rpc.Invalid("at most %d sessions", maxRefs)
	}
	if err := checkRefs(u.Attachments); err != nil {
		return api.Utterance{}, err
	}
	if u.ReplyTo != nil {
		r, err := s.DB.GetUtterance(ctx, *u.ReplyTo)
		if err != nil || r.Conversation != c.Id {
			return api.Utterance{}, invalidRef(err, "reply_to %s is not in this conversation", *u.ReplyTo)
		}
	}
	for _, id := range u.Sessions {
		ses, err := s.DB.GetSession(ctx, id)
		if err != nil || ses.Conversation != c.Id {
			return api.Utterance{}, invalidRef(err, "session %s is not in this conversation", id)
		}
	}
	u, err = s.DB.Post(ctx, u)
	if err != nil {
		return u, notFound(err, "conversation %s", c.Id)
	}
	s.signal(c.Id)
	return u, nil
}

// invalidRef maps a missing or foreign reference to Invalid and keeps
// database errors as they are.
func invalidRef(err error, format string, args ...any) error {
	if err == nil || errors.Is(err, store.ErrNotFound) {
		return rpc.Invalid(format, args...)
	}
	return err
}

func (s *Service) ListUtterances(ctx context.Context, req api.ListUtterancesRequest) (api.ListUtterancesResponse, error) {
	var after uint64
	if req.AfterSeq != nil {
		after = *req.AfterSeq
	}
	var wait time.Duration
	if req.WaitMs != nil {
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
		ch := s.waitChan(req.Conversation)
		_, c, err := s.conversation(ctx, req.Conversation)
		if err != nil {
			return api.ListUtterancesResponse{}, err
		}
		list, err := s.DB.ListUtterances(ctx, c.Id, after, limit(req.Limit))
		if err != nil {
			return api.ListUtterancesResponse{}, err
		}
		last := c.LastSeq
		if n := len(list); n > 0 {
			last = max(last, list[n-1].Seq)
		}
		left := time.Until(deadline)
		if len(list) > 0 || left <= 0 {
			return api.ListUtterancesResponse{Utterances: list, LastSeq: last}, nil
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
	_, u, err := s.utterance(ctx, req.Id)
	return u, err
}

func (s *Service) OpenSession(ctx context.Context, req api.OpenSessionRequest) (api.Session, error) {
	_, c, err := s.conversation(ctx, req.Conversation)
	if err != nil {
		return api.Session{}, err
	}
	if req.Title == "" {
		return api.Session{}, rpc.Invalid("title is required")
	}
	if err := checkRefs(req.Refs); err != nil {
		return api.Session{}, err
	}
	now := uint64(s.now().UnixMilli())
	ses := api.Session{Id: newID("ses_"), Conversation: c.Id, Title: req.Title, Status: "open", Refs: req.Refs, CreatedMs: now, UpdatedMs: now}
	if req.Status != nil && *req.Status != "" {
		ses.Status = *req.Status
	}
	return s.DB.OpenSession(ctx, ses)
}

func (s *Service) UpdateSession(ctx context.Context, req api.UpdateSessionRequest) (api.Session, error) {
	if _, err := rpc.Caller(ctx); err != nil {
		return api.Session{}, err
	}
	ses, err := s.DB.GetSession(ctx, req.Id)
	if err != nil {
		return ses, notFound(err, "session %s", req.Id)
	}
	if _, _, err := s.conversation(ctx, ses.Conversation); err != nil {
		return api.Session{}, err
	}
	if req.Title != nil && *req.Title == "" {
		return api.Session{}, rpc.Invalid("title must not be empty")
	}
	if err := checkRefs(req.AddRefs); err != nil {
		return api.Session{}, err
	}
	ses, err = s.DB.UpdateSession(ctx, req.Id, req.Title, req.Status, req.AddRefs, s.now())
	return ses, notFound(err, "session %s", req.Id)
}

func (s *Service) ListSessions(ctx context.Context, req api.ListSessionsRequest) (api.ListSessionsResponse, error) {
	_, c, err := s.conversation(ctx, req.Conversation)
	if err != nil {
		return api.ListSessionsResponse{}, err
	}
	list, err := s.DB.ListSessions(ctx, c.Id)
	return api.ListSessionsResponse{Sessions: list}, err
}

// utterance returns the caller and the utterance id if the caller can see it.
func (s *Service) utterance(ctx context.Context, id string) (authn.Principal, api.Utterance, error) {
	p, err := rpc.Caller(ctx)
	if err != nil {
		return p, api.Utterance{}, err
	}
	u, err := s.DB.GetUtterance(ctx, id)
	if err != nil {
		return p, u, notFound(err, "utterance %s", id)
	}
	_, _, err = s.conversation(ctx, u.Conversation)
	return p, u, err
}

func (s *Service) Annotate(ctx context.Context, req api.AnnotateRequest) (api.Annotation, error) {
	p, u, err := s.utterance(ctx, req.Utterance)
	if err != nil {
		return api.Annotation{}, err
	}
	if req.Kind == "" {
		return api.Annotation{}, rpc.Invalid("kind is required")
	}
	if len(req.Body) > maxText {
		return api.Annotation{}, rpc.Invalid("body is larger than %d bytes", maxText)
	}
	a := api.Annotation{Id: newID("ann_"), Utterance: u.Id, Kind: req.Kind, Author: p.Subject, Body: req.Body, CreatedMs: uint64(s.now().UnixMilli())}
	return a, s.DB.Annotate(ctx, a)
}

func (s *Service) ListAnnotations(ctx context.Context, req api.ListAnnotationsRequest) (api.ListAnnotationsResponse, error) {
	_, u, err := s.utterance(ctx, req.Utterance)
	if err != nil {
		return api.ListAnnotationsResponse{}, err
	}
	list, err := s.DB.ListAnnotations(ctx, u.Id)
	return api.ListAnnotationsResponse{Annotations: list}, err
}
