package compose

import (
	"cmp"
	"context"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/catalystcommunity/magi/lib/rpc"
	magiapi "github.com/catalystcommunity/magi/magi/api"
	conversationapi "github.com/catalystcommunity/magi/primitives/conversation/api"
)

// adminConversation serves the chats of the admin tests and records each
// request with the subject of the caller.
type adminConversation struct {
	conversationapi.ConversationService
	mu      sync.Mutex
	streams []conversationapi.Stream
	utts    []conversationapi.Utterance
	anns    []conversationapi.Annotation
	links   []conversationapi.Link

	listed  []conversationapi.ListStreamsRequest
	got     []conversationapi.GetStreamRequest
	listedU []conversationapi.ListUtterancesRequest
	annFor  []conversationapi.ListAnnotationsRequest
	callers []string
}

func (c *adminConversation) record(ctx context.Context) {
	p, err := rpc.Caller(ctx)
	if err != nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.callers = append(c.callers, p.Subject)
}

func (c *adminConversation) ListStreams(ctx context.Context, r conversationapi.ListStreamsRequest) (conversationapi.ListStreamsResponse, error) {
	c.record(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.listed = append(c.listed, r)
	out := conversationapi.ListStreamsResponse{Streams: []conversationapi.Stream{}}
	for _, st := range c.streams {
		if r.Person != nil && st.Person != *r.Person {
			continue
		}
		out.Streams = append(out.Streams, st)
	}
	// The service returns the streams newest first.
	slices.SortFunc(out.Streams, func(a, b conversationapi.Stream) int {
		return cmp.Or(cmp.Compare(b.UpdatedMs, a.UpdatedMs), strings.Compare(b.Id, a.Id))
	})
	return out, nil
}

func (c *adminConversation) GetStream(ctx context.Context, r conversationapi.GetStreamRequest) (conversationapi.Stream, error) {
	c.record(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.got = append(c.got, r)
	for _, st := range c.streams {
		if st.Id == r.Id {
			return st, nil
		}
	}
	return conversationapi.Stream{}, rpc.NotFound("no stream %s", r.Id)
}

func (c *adminConversation) ListUtterances(ctx context.Context, r conversationapi.ListUtterancesRequest) (conversationapi.ListUtterancesResponse, error) {
	c.record(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.listedU = append(c.listedU, r)
	out := conversationapi.ListUtterancesResponse{Utterances: []conversationapi.Utterance{}, Links: []conversationapi.Link{}}
	for _, u := range c.utts {
		if u.Stream != r.Stream {
			continue
		}
		if r.BeforeSeq != nil && u.Seq >= *r.BeforeSeq {
			continue
		}
		if r.AfterSeq != nil && u.Seq <= *r.AfterSeq {
			continue
		}
		out.Utterances = append(out.Utterances, u)
	}
	// The service returns the utterances in seq order, also with before_seq
	// (it sorts the newest ones back into order).
	for _, l := range c.links {
		for _, u := range out.Utterances {
			if l.Utterance == u.Id {
				out.Links = append(out.Links, l)
				break
			}
		}
	}
	if n := len(out.Utterances); n > 0 {
		out.LastSeq = out.Utterances[n-1].Seq
	}
	return out, nil
}

func (c *adminConversation) ListAnnotations(ctx context.Context, r conversationapi.ListAnnotationsRequest) (conversationapi.ListAnnotationsResponse, error) {
	c.record(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.annFor = append(c.annFor, r)
	out := conversationapi.ListAnnotationsResponse{Annotations: []conversationapi.Annotation{}}
	for _, an := range c.anns {
		if an.Utterance == r.Utterance {
			out.Annotations = append(out.Annotations, an)
		}
	}
	return out, nil
}

// calls returns the subjects that reached the conversation service.
func (c *adminConversation) calls() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string{}, c.callers...)
}

// lastList returns the newest list-streams request that the service saw.
func (c *adminConversation) lastList() conversationapi.ListStreamsRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.listed[len(c.listed)-1]
}

// lastUtterances returns the newest list-utterances request that the service
// saw.
func (c *adminConversation) lastUtterances() conversationapi.ListUtterancesRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.listedU[len(c.listedU)-1]
}

// adminChatsEnv serves MagiService with the conversation fake. It returns
// the fake, the client, and a token maker.
func adminChatsEnv(t *testing.T, c *adminConversation) (*adminConversation, *magiapi.MagiClient, func(string, ...string) context.Context) {
	t.Helper()
	_, mc, as := adminEnv(t, Config{Admins: []string{"user:admin@example.local"}}, map[string]map[string]func(context.Context, []byte) ([]byte, error){
		"ConversationService": conversationapi.ConversationServiceOps(c),
	})
	return c, mc, as
}

// adminChatWorld is the chat data of the admin chat tests: two chats of two
// people, three messages, and two annotations.
func adminChatWorld() *adminConversation {
	const (
		a = "user:a@example.local"
		b = "user:b@example.local"
	)
	return &adminConversation{
		streams: []conversationapi.Stream{
			{Id: "str_a", Person: a, Persona: Persona, LastSeq: 2, CreatedMs: 1000, UpdatedMs: 3000},
			{Id: "str_b", Person: b, Persona: Engineering, LastSeq: 1, CreatedMs: 2000, UpdatedMs: 4000},
		},
		utts: []conversationapi.Utterance{
			{Id: "utt_1", Stream: "str_a", Seq: 1, Author: a, AuthorKind: "human", Modality: "text", Text: "hello", CreatedMs: 1100},
			{Id: "utt_2", Stream: "str_a", Seq: 2, Author: Persona, AuthorKind: "persona", Modality: "text", Text: "hi", CreatedMs: 1200},
			{Id: "utt_3", Stream: "str_b", Seq: 1, Author: b, AuthorKind: "human", Modality: "text", Text: "build it", CreatedMs: 2100},
		},
		links: []conversationapi.Link{
			{Id: "lnk_1", Utterance: "utt_2", Target: conversationapi.Ref{Kind: "utterance", Ref: "utt_1"}, Kind: "reply-to", Author: "service:magi", State: "current"},
		},
		anns: []conversationapi.Annotation{
			{Id: "ann_1", Utterance: "utt_1", Kind: "other", Author: "service:magi", Body: "not a log", CreatedMs: 1150},
			{Id: "ann_2", Utterance: "utt_1", Kind: "interpretation", Author: "service:magi", Body: `{"reply":"hi"}`, CreatedMs: 1250},
		},
	}
}

// A non-admin gets 403 on each admin chat op, and the conversation service
// sees no call: the role check runs before the read.
func TestAdminChatsDenied(t *testing.T) {
	c, mc, as := adminChatsEnv(t, adminChatWorld())
	person := as("user:a@example.local")
	for _, call := range []func() error{
		func() error { _, err := mc.AdminListChats(person, magiapi.AdminListChatsRequest{}); return err },
		func() error {
			_, err := mc.AdminReadChat(person, magiapi.AdminReadChatRequest{Stream: "str_a"})
			return err
		},
		func() error {
			_, err := mc.AdminTurnLog(person, magiapi.AdminTurnLogRequest{Message: "utt_1"})
			return err
		},
	} {
		if err := call(); rpc.CodeOf(err) != http.StatusForbidden {
			t.Fatalf("a non-admin did not get 403: %v", err)
		}
	}
	if got := c.calls(); len(got) != 0 {
		t.Fatalf("the conversation service saw %v, want no call", got)
	}
}

// An admin lists the chats of all people: Magi asks for all streams with its
// service token, and the person and persona filters narrow the list.
func TestAdminListChats(t *testing.T) {
	c, mc, as := adminChatsEnv(t, adminChatWorld())
	admin := as("user:admin@example.local")
	r, err := mc.AdminListChats(admin, magiapi.AdminListChatsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	// The conversation service returns the streams newest first.
	if len(r.Chats) != 2 || r.Chats[0].Stream != "str_b" || r.Chats[1].Stream != "str_a" {
		t.Fatalf("chats: %+v", r.Chats)
	}
	if got := r.Chats[1]; got.Person != "user:a@example.local" || got.Persona != Persona || got.LastSeq != 2 ||
		got.CreatedMs != 1000 || got.UpdatedMs != 3000 {
		t.Fatalf("chat: %+v", got)
	}
	if got := c.calls(); len(got) != 1 || got[0] != "service:magi" {
		t.Fatalf("the conversation service saw %v, want service:magi", got)
	}
	if got := c.lastList(); got.All == nil || !*got.All || got.Person != nil {
		t.Fatalf("request: %+v", got)
	}
	// The person filter sends person, not all.
	r, err = mc.AdminListChats(admin, magiapi.AdminListChatsRequest{Person: ptr("user:b@example.local")})
	if err != nil || len(r.Chats) != 1 || r.Chats[0].Stream != "str_b" {
		t.Fatalf("by person: %+v %v", r, err)
	}
	if got := c.lastList(); got.All != nil || deref(got.Person) != "user:b@example.local" {
		t.Fatalf("person request: %+v", got)
	}
	// The persona filter stays in Magi: the conversation service has no such
	// filter on list-streams.
	r, err = mc.AdminListChats(admin, magiapi.AdminListChatsRequest{Persona: ptr(Engineering)})
	if err != nil || len(r.Chats) != 1 || r.Chats[0].Stream != "str_b" {
		t.Fatalf("by persona: %+v %v", r, err)
	}
	if got := c.lastList(); got.All == nil || !*got.All {
		t.Fatalf("persona request: %+v", got)
	}
}

// admin-read-chat returns the chat and its newest messages. Without
// before_seq it asks for the messages before last_seq+1, and the limit is
// clamped.
func TestAdminReadChat(t *testing.T) {
	c, mc, as := adminChatsEnv(t, adminChatWorld())
	admin := as("user:admin@example.local")
	r, err := mc.AdminReadChat(admin, magiapi.AdminReadChatRequest{Stream: "str_a"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Chat.Stream != "str_a" || r.Chat.LastSeq != 2 {
		t.Fatalf("chat: %+v", r.Chat)
	}
	if len(r.Messages) != 2 || r.Messages[0].Id != "utt_1" || r.Messages[1].Id != "utt_2" {
		t.Fatalf("messages: %+v", r.Messages)
	}
	if m := r.Messages[1]; m.Persona != Persona || len(m.Links) != 1 || m.Links[0].Kind != "reply-to" {
		t.Fatalf("persona message: %+v", m)
	}
	// Every read of the conversation service is with the service token of
	// magi: the stream, the utterances, and the persona of each message.
	for _, got := range c.calls() {
		if got != "service:magi" {
			t.Fatalf("the conversation service saw %s, want service:magi", got)
		}
	}
	if got := c.lastUtterances(); deref(got.BeforeSeq) != 3 || deref(got.Limit) != adminChatDefault {
		t.Fatalf("request: %+v", got)
	}
	// before_seq goes through, and a limit over the maximum is clamped.
	r, err = mc.AdminReadChat(admin, magiapi.AdminReadChatRequest{Stream: "str_a", BeforeSeq: ptr(uint64(2)), Limit: ptr(uint64(9999))})
	if err != nil || len(r.Messages) != 1 || r.Messages[0].Id != "utt_1" {
		t.Fatalf("before: %+v %v", r, err)
	}
	if got := c.lastUtterances(); deref(got.BeforeSeq) != 2 || deref(got.Limit) != adminChatMax {
		t.Fatalf("clamped request: %+v", got)
	}
	// An unknown stream is the error of the conversation service.
	if _, err := mc.AdminReadChat(admin, magiapi.AdminReadChatRequest{Stream: "str_x"}); rpc.CodeOf(err) != http.StatusNotFound {
		t.Fatalf("unknown stream: %v", err)
	}
}

// admin-turn-log returns the body of the newest interpretation annotation of
// the message, and no log when there is none.
func TestAdminTurnLog(t *testing.T) {
	c, mc, as := adminChatsEnv(t, adminChatWorld())
	admin := as("user:admin@example.local")
	r, err := mc.AdminTurnLog(admin, magiapi.AdminTurnLogRequest{Message: "utt_1"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Message != "utt_1" || deref(r.Log) != `{"reply":"hi"}` {
		t.Fatalf("turn log: %+v", r)
	}
	if got := c.calls(); len(got) != 1 || got[0] != "service:magi" {
		t.Fatalf("the conversation service saw %v, want service:magi", got)
	}
	// A message with no interpretation annotation has no log.
	r, err = mc.AdminTurnLog(admin, magiapi.AdminTurnLogRequest{Message: "utt_3"})
	if err != nil || r.Log != nil {
		t.Fatalf("no log: %+v %v", r, err)
	}
}
