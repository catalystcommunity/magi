package server_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/catalystcommunity/magi/lib/rpc"
	api "github.com/catalystcommunity/magi/primitives/conversation/api"
	"github.com/catalystcommunity/magi/primitives/conversation/internal/testenv"
	"github.com/catalystcommunity/magi/primitives/conversation/server"
	"github.com/catalystcommunity/magi/primitives/conversation/store"
)

type env struct {
	t    *testing.T
	keys testenv.Keys
	urls []string // one URL per replica
}

// setup starts replicas that share one database.
func setup(t *testing.T, replicas int, poll time.Duration) env {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	dbURL := testenv.Database(t)
	e := env{t: t, keys: testenv.NewKeys(t)}
	for range replicas {
		db, err := store.Open(ctx, dbURL)
		if err != nil {
			t.Fatal(err)
		}
		svc := &server.Service{DB: db, PollInterval: poll}
		go svc.Listen(ctx, nil)
		s := &rpc.Server{Verifier: e.keys.Verifier(t)}
		server.Register(s, svc)
		hs := httptest.NewServer(rpc.Mux(s, nil))
		// Cleanups run last-in first-out: close the server, then the pool.
		t.Cleanup(db.Close)
		t.Cleanup(cancel)
		t.Cleanup(hs.Close)
		e.urls = append(e.urls, hs.URL)
	}
	// Let the listeners subscribe before the test posts.
	time.Sleep(200 * time.Millisecond)
	return e
}

func (e env) client(replica int, subject string, scopes ...string) *api.ConversationClient {
	return api.NewConversationClient(rpc.NewClient(e.urls[replica], rpc.StaticToken(e.keys.Token(e.t, subject, scopes...))))
}

func wantCode(t *testing.T, err error, code int) {
	t.Helper()
	if rpc.CodeOf(err) != code {
		t.Fatalf("error = %v, want code %d", err, code)
	}
}

func ptr[T any](v T) *T { return &v }

const (
	alice = "user:alice@example.local"
	bob   = "user:bob@example.local"
	carol = "user:carol@example.local"
)

func TestConversation(t *testing.T) {
	e := setup(t, 1, time.Second)
	ctx := context.Background()
	ca, cb, cc := e.client(0, alice), e.client(0, bob), e.client(0, carol)
	persona := e.client(0, "service:magi", server.ScopeActAs)
	admin := e.client(0, "user:root@example.local", server.ScopeAdmin)

	conv, err := ca.CreateConversation(ctx, api.CreateConversationRequest{Title: ptr("plan"), Participants: []string{bob, bob}})
	if err != nil {
		t.Fatal(err)
	}
	if conv.Owner != alice || len(conv.Participants) != 1 || conv.Title != "plan" || conv.LastSeq != 0 {
		t.Fatalf("create = %+v", conv)
	}

	t.Run("create with owner override", func(t *testing.T) {
		_, err := cb.CreateConversation(ctx, api.CreateConversationRequest{Owner: ptr(alice)})
		wantCode(t, err, http.StatusForbidden)
		c, err := persona.CreateConversation(ctx, api.CreateConversationRequest{Owner: ptr(carol)})
		if err != nil || c.Owner != carol {
			t.Fatalf("act-as create = %+v, %v", c, err)
		}
	})

	t.Run("get visibility", func(t *testing.T) {
		for _, c := range []*api.ConversationClient{ca, cb, persona, admin} {
			if _, err := c.GetConversation(ctx, api.GetConversationRequest{Id: conv.Id}); err != nil {
				t.Fatal(err)
			}
		}
		_, err := cc.GetConversation(ctx, api.GetConversationRequest{Id: conv.Id})
		wantCode(t, err, http.StatusForbidden)
		_, err = ca.GetConversation(ctx, api.GetConversationRequest{Id: "cnv_missing"})
		wantCode(t, err, http.StatusNotFound)
	})

	var first api.Utterance
	t.Run("post", func(t *testing.T) {
		first, err = ca.Post(ctx, api.PostRequest{Conversation: conv.Id, Text: "hello"})
		if err != nil {
			t.Fatal(err)
		}
		if first.Seq != 1 || first.Author != alice || first.AuthorKind != "human" || first.Modality != "text" {
			t.Fatalf("post = %+v", first)
		}
		r, err := cb.Post(ctx, api.PostRequest{Conversation: conv.Id, Text: "hi", ReplyTo: ptr(first.Id),
			Attachments: []api.Attachment{{Kind: "url", Ref: "https://example.local/x", Title: ptr("x")}}})
		if err != nil || r.Seq != 2 || *r.ReplyTo != first.Id || len(r.Attachments) != 1 || *r.Attachments[0].Title != "x" {
			t.Fatalf("reply = %+v, %v", r, err)
		}
		_, err = cc.Post(ctx, api.PostRequest{Conversation: conv.Id, Text: "intrude"})
		wantCode(t, err, http.StatusForbidden)
		_, err = cb.Post(ctx, api.PostRequest{Conversation: conv.Id, Text: "x", Author: ptr(alice)})
		wantCode(t, err, http.StatusForbidden)
		_, err = ca.Post(ctx, api.PostRequest{Conversation: conv.Id, Text: "x", AuthorKind: ptr("persona")})
		wantCode(t, err, http.StatusForbidden)
		_, err = ca.Post(ctx, api.PostRequest{Conversation: conv.Id})
		wantCode(t, err, http.StatusBadRequest)
		_, err = ca.Post(ctx, api.PostRequest{Conversation: conv.Id, Text: "x", Modality: ptr("smoke")})
		wantCode(t, err, http.StatusBadRequest)
		_, err = ca.Post(ctx, api.PostRequest{Conversation: conv.Id, Text: "x", ReplyTo: ptr("utt_missing")})
		wantCode(t, err, http.StatusBadRequest)
		_, err = ca.Post(ctx, api.PostRequest{Conversation: "cnv_missing", Text: "x"})
		wantCode(t, err, http.StatusNotFound)

		p, err := persona.Post(ctx, api.PostRequest{Conversation: conv.Id, Text: "I can help.", Author: ptr("persona:magi"),
			AuthorKind: ptr("persona"), Modality: ptr("voice"), Audio: ptr("sha256:abc"), TranscriptConfidence: ptr(0.75)})
		if err != nil || p.Seq != 3 || p.Author != "persona:magi" || p.AuthorKind != "persona" || *p.TranscriptConfidence != 0.75 || *p.Audio != "sha256:abc" {
			t.Fatalf("persona post = %+v, %v", p, err)
		}
	})

	t.Run("list and get utterances", func(t *testing.T) {
		l, err := cb.ListUtterances(ctx, api.ListUtterancesRequest{Conversation: conv.Id})
		if err != nil || len(l.Utterances) != 3 || l.LastSeq != 3 {
			t.Fatalf("list = %+v, %v", l, err)
		}
		for i, u := range l.Utterances {
			if u.Seq != uint64(i+1) {
				t.Fatalf("seq order: %d at %d", u.Seq, i)
			}
		}
		l, err = ca.ListUtterances(ctx, api.ListUtterancesRequest{Conversation: conv.Id, AfterSeq: ptr(uint64(1)), Limit: ptr(uint64(1))})
		if err != nil || len(l.Utterances) != 1 || l.Utterances[0].Seq != 2 || l.LastSeq != 3 {
			t.Fatalf("paged list = %+v, %v", l, err)
		}
		_, err = cc.ListUtterances(ctx, api.ListUtterancesRequest{Conversation: conv.Id})
		wantCode(t, err, http.StatusForbidden)

		u, err := cb.GetUtterance(ctx, api.GetUtteranceRequest{Id: first.Id})
		if err != nil || u.Text != "hello" {
			t.Fatalf("get utterance = %+v, %v", u, err)
		}
		_, err = cc.GetUtterance(ctx, api.GetUtteranceRequest{Id: first.Id})
		wantCode(t, err, http.StatusForbidden)
		_, err = ca.GetUtterance(ctx, api.GetUtteranceRequest{Id: "utt_missing"})
		wantCode(t, err, http.StatusNotFound)
	})

	t.Run("list conversations", func(t *testing.T) {
		l, err := cb.ListConversations(ctx, api.ListConversationsRequest{})
		if err != nil || len(l.Conversations) != 1 || l.Conversations[0].Id != conv.Id || l.Conversations[0].LastSeq != 3 {
			t.Fatalf("bob list = %+v, %v", l, err)
		}
		if _, err := ca.CreateConversation(ctx, api.CreateConversationRequest{Title: ptr("second")}); err != nil {
			t.Fatal(err)
		}
		l, _ = ca.ListConversations(ctx, api.ListConversationsRequest{})
		if len(l.Conversations) != 2 || l.Conversations[0].Title != "second" {
			t.Fatalf("alice list = %+v", l)
		}
		l, _ = ca.ListConversations(ctx, api.ListConversationsRequest{Limit: ptr(uint64(1)), BeforeMs: ptr(l.Conversations[0].UpdatedMs)})
		if len(l.Conversations) > 1 {
			t.Fatalf("before_ms list = %+v", l)
		}
		l, _ = cc.ListConversations(ctx, api.ListConversationsRequest{})
		if len(l.Conversations) != 1 || l.Conversations[0].Owner != carol {
			t.Fatalf("carol list = %+v", l)
		}
	})

	t.Run("sessions", func(t *testing.T) {
		s, err := ca.OpenSession(ctx, api.OpenSessionRequest{Conversation: conv.Id, Title: "fix bug"})
		if err != nil || s.Status != "open" || len(s.Refs) != 0 {
			t.Fatalf("open = %+v, %v", s, err)
		}
		_, err = cc.OpenSession(ctx, api.OpenSessionRequest{Conversation: conv.Id, Title: "x"})
		wantCode(t, err, http.StatusForbidden)
		_, err = ca.OpenSession(ctx, api.OpenSessionRequest{Conversation: conv.Id})
		wantCode(t, err, http.StatusBadRequest)

		u, err := cb.UpdateSession(ctx, api.UpdateSessionRequest{Id: s.Id, Status: ptr("waiting"),
			AddRefs: []api.Attachment{{Kind: "workflow-run", Ref: "run_1"}}})
		if err != nil || u.Status != "waiting" || u.Title != "fix bug" || len(u.Refs) != 1 {
			t.Fatalf("update = %+v, %v", u, err)
		}
		u, err = ca.UpdateSession(ctx, api.UpdateSessionRequest{Id: s.Id, Title: ptr("fix the bug"), AddRefs: []api.Attachment{{Kind: "artifact", Ref: "sha256:x"}}})
		if err != nil || u.Title != "fix the bug" || u.Status != "waiting" || len(u.Refs) != 2 {
			t.Fatalf("second update = %+v, %v", u, err)
		}
		_, err = cc.UpdateSession(ctx, api.UpdateSessionRequest{Id: s.Id, Status: ptr("closed")})
		wantCode(t, err, http.StatusForbidden)
		_, err = ca.UpdateSession(ctx, api.UpdateSessionRequest{Id: "ses_missing"})
		wantCode(t, err, http.StatusNotFound)

		p, err := ca.Post(ctx, api.PostRequest{Conversation: conv.Id, Text: "progress", Sessions: []string{s.Id}})
		if err != nil || len(p.Sessions) != 1 || p.Sessions[0] != s.Id {
			t.Fatalf("post with session = %+v, %v", p, err)
		}
		_, err = ca.Post(ctx, api.PostRequest{Conversation: conv.Id, Text: "x", Sessions: []string{"ses_missing"}})
		wantCode(t, err, http.StatusBadRequest)

		l, err := cb.ListSessions(ctx, api.ListSessionsRequest{Conversation: conv.Id})
		if err != nil || len(l.Sessions) != 1 || l.Sessions[0].Id != s.Id {
			t.Fatalf("list sessions = %+v, %v", l, err)
		}
		_, err = cc.ListSessions(ctx, api.ListSessionsRequest{Conversation: conv.Id})
		wantCode(t, err, http.StatusForbidden)
	})

	t.Run("annotations", func(t *testing.T) {
		a, err := persona.Annotate(ctx, api.AnnotateRequest{Utterance: first.Id, Kind: "interpretation", Body: `{"intent":"greet"}`})
		if err != nil || a.Author != "service:magi" || a.Utterance != first.Id {
			t.Fatalf("annotate = %+v, %v", a, err)
		}
		_, err = cc.Annotate(ctx, api.AnnotateRequest{Utterance: first.Id, Kind: "x", Body: ""})
		wantCode(t, err, http.StatusForbidden)
		_, err = ca.Annotate(ctx, api.AnnotateRequest{Utterance: first.Id, Body: "x"})
		wantCode(t, err, http.StatusBadRequest)
		_, err = ca.Annotate(ctx, api.AnnotateRequest{Utterance: "utt_missing", Kind: "x"})
		wantCode(t, err, http.StatusNotFound)

		l, err := ca.ListAnnotations(ctx, api.ListAnnotationsRequest{Utterance: first.Id})
		if err != nil || len(l.Annotations) != 1 || l.Annotations[0].Body != `{"intent":"greet"}` {
			t.Fatalf("list annotations = %+v, %v", l, err)
		}
		_, err = cc.ListAnnotations(ctx, api.ListAnnotationsRequest{Utterance: first.Id})
		wantCode(t, err, http.StatusForbidden)
	})

	t.Run("token required", func(t *testing.T) {
		anon := api.NewConversationClient(rpc.NewClient(e.urls[0], nil))
		_, err := anon.ListConversations(ctx, api.ListConversationsRequest{})
		wantCode(t, err, http.StatusUnauthorized)
	})
}

func TestConcurrentPostsGetDistinctSeq(t *testing.T) {
	e := setup(t, 2, time.Second)
	ctx := context.Background()
	c := []*api.ConversationClient{e.client(0, alice), e.client(1, alice)}
	conv, err := c[0].CreateConversation(ctx, api.CreateConversationRequest{})
	if err != nil {
		t.Fatal(err)
	}
	const n = 40
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := range n {
		wg.Go(func() {
			_, err := c[i%2].Post(ctx, api.PostRequest{Conversation: conv.Id, Text: "x"})
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	l, err := c[0].ListUtterances(ctx, api.ListUtterancesRequest{Conversation: conv.Id})
	if err != nil || len(l.Utterances) != n || l.LastSeq != n {
		t.Fatalf("list = %d utterances, last_seq %d, %v", len(l.Utterances), l.LastSeq, err)
	}
	for i, u := range l.Utterances {
		if u.Seq != uint64(i+1) {
			t.Fatalf("seq %d at index %d", u.Seq, i)
		}
	}
}

// longPoll starts a waiting list-utterances call and returns its result channel.
func longPoll(c *api.ConversationClient, conv string, after, waitMs uint64) <-chan api.ListUtterancesResponse {
	out := make(chan api.ListUtterancesResponse, 1)
	go func() {
		l, err := c.ListUtterances(context.Background(), api.ListUtterancesRequest{Conversation: conv, AfterSeq: &after, WaitMs: &waitMs})
		if err != nil {
			l.LastSeq = 1 << 63 // marks an error for the test
		}
		out <- l
	}()
	return out
}

func TestLongPoll(t *testing.T) {
	// A long poll interval shows that the wake comes from a notification,
	// not from polling.
	e := setup(t, 2, 20*time.Second)
	ctx := context.Background()
	a0, a1 := e.client(0, alice), e.client(1, alice)
	conv, err := a0.CreateConversation(ctx, api.CreateConversationRequest{})
	if err != nil {
		t.Fatal(err)
	}

	t.Run("timeout returns empty", func(t *testing.T) {
		start := time.Now()
		l := <-longPoll(a0, conv.Id, 0, 300)
		if d := time.Since(start); d < 300*time.Millisecond || d > 3*time.Second || len(l.Utterances) != 0 || l.LastSeq != 0 {
			t.Fatalf("timeout poll: %v, %+v", d, l)
		}
	})

	for _, tc := range []struct {
		name   string
		poster *api.ConversationClient
		after  uint64
	}{
		{"same replica wakes", a0, 0},
		{"other replica wakes", a1, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := longPoll(a0, conv.Id, tc.after, 15000)
			time.Sleep(300 * time.Millisecond)
			start := time.Now()
			if _, err := tc.poster.Post(ctx, api.PostRequest{Conversation: conv.Id, Text: tc.name}); err != nil {
				t.Fatal(err)
			}
			select {
			case l := <-res:
				if len(l.Utterances) != 1 || l.Utterances[0].Text != tc.name || l.LastSeq != tc.after+1 {
					t.Fatalf("woken poll = %+v", l)
				}
				if d := time.Since(start); d > 2*time.Second {
					t.Fatalf("wake took %v", d)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("long poll did not wake")
			}
		})
	}

	t.Run("existing utterances return at once", func(t *testing.T) {
		start := time.Now()
		l := <-longPoll(a0, conv.Id, 0, 15000)
		if len(l.Utterances) != 2 || time.Since(start) > time.Second {
			t.Fatalf("immediate poll = %+v", l)
		}
	})

	t.Run("denied caller does not wait", func(t *testing.T) {
		_, err := e.client(0, bob).ListUtterances(ctx, api.ListUtterancesRequest{Conversation: conv.Id, WaitMs: ptr(uint64(5000))})
		wantCode(t, err, http.StatusForbidden)
	})
}

func TestLongPollFallsBackToPolling(t *testing.T) {
	// Without a listener, polling alone must still deliver the post.
	ctx := context.Background()
	db, err := store.Open(ctx, testenv.Database(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	keys := testenv.NewKeys(t)
	poster := &server.Service{DB: db}
	waiter := &server.Service{DB: db, PollInterval: 100 * time.Millisecond}
	var urls []string
	for _, svc := range []*server.Service{poster, waiter} {
		s := &rpc.Server{Verifier: keys.Verifier(t)}
		server.Register(s, svc)
		hs := httptest.NewServer(rpc.Mux(s, nil))
		t.Cleanup(hs.Close)
		urls = append(urls, hs.URL)
	}
	tok := rpc.StaticToken(keys.Token(t, alice))
	p, w := api.NewConversationClient(rpc.NewClient(urls[0], tok)), api.NewConversationClient(rpc.NewClient(urls[1], tok))
	conv, err := p.CreateConversation(ctx, api.CreateConversationRequest{})
	if err != nil {
		t.Fatal(err)
	}
	res := longPoll(w, conv.Id, 0, 10000)
	time.Sleep(200 * time.Millisecond)
	if _, err := p.Post(ctx, api.PostRequest{Conversation: conv.Id, Text: "polled"}); err != nil {
		t.Fatal(err)
	}
	select {
	case l := <-res:
		if len(l.Utterances) != 1 {
			t.Fatalf("poll = %+v", l)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("polling did not deliver the post")
	}
}
