package server_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/catalystcommunity/magi/lib/rpc"
	api "github.com/catalystcommunity/magi/primitives/conversation/api"
	"github.com/catalystcommunity/magi/primitives/conversation/internal/testenv"
	"github.com/catalystcommunity/magi/primitives/conversation/server"
	"github.com/catalystcommunity/magi/primitives/conversation/store"
	"github.com/jackc/pgx/v5"
)

type env struct {
	t     *testing.T
	keys  testenv.Keys
	dbURL string
	urls  []string // one URL per replica
}

// setup starts replicas that share one database. before runs on the new
// database before the service applies its schema.
func setup(t *testing.T, replicas int, poll time.Duration, before ...string) env {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	dbURL := testenv.Database(t)
	if len(before) > 0 {
		conn, err := pgx.Connect(ctx, dbURL)
		if err != nil {
			t.Fatal(err)
		}
		for _, q := range before {
			if _, err := conn.Exec(ctx, q); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
		}
		conn.Close(ctx)
	}
	e := env{t: t, keys: testenv.NewKeys(t), dbURL: dbURL}
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

func must[T any](t *testing.T) func(T, error) T {
	return func(v T, err error) T {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
}

const (
	alice  = "user:alice@example.local"
	bob    = "user:bob@example.local"
	carol  = "user:carol@example.local"
	magi   = "persona:magi"
	butler = "persona:butler"
	quiet  = "persona:quiet"
)

// world has three personas: magi (private by default), butler (public,
// audience alice and bob), and quiet (never shares).
type world struct {
	env
	admin, librarian, alice, bob, carol *api.ConversationClient
}

func newWorld(t *testing.T) world {
	e := setup(t, 1, time.Second)
	w := world{env: e, admin: e.client(0, "user:root@example.local", server.ScopeAdmin),
		librarian: e.client(0, "service:magi", server.ScopeActAs),
		alice:     e.client(0, alice), bob: e.client(0, bob), carol: e.client(0, carol)}
	ctx := context.Background()
	for _, p := range []api.PutPersonaRequest{
		{Id: magi, Name: "Magi", Sharing: "private"},
		{Id: butler, Name: "Butler", Sharing: "public", Members: []string{alice, bob}},
		{Id: quiet, Name: "Quiet", Sharing: "never", Members: []string{alice, bob}},
	} {
		must[api.Persona](t)(w.admin.PutPersona(ctx, p))
	}
	return w
}

func (w world) stream(c *api.ConversationClient, persona string) api.Stream {
	return must[api.Stream](w.t)(c.OpenStream(context.Background(), api.OpenStreamRequest{Persona: persona}))
}

func TestPersonasAndStreams(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	_, err := w.alice.PutPersona(ctx, api.PutPersonaRequest{Id: "persona:x", Name: "X", Sharing: "private"})
	wantCode(t, err, http.StatusForbidden)
	_, err = w.admin.PutPersona(ctx, api.PutPersonaRequest{Id: "persona:x", Name: "X", Sharing: "loud"})
	wantCode(t, err, http.StatusBadRequest)
	list := must[api.ListPersonasResponse](t)(w.bob.ListPersonas(ctx, api.ListPersonasRequest{}))
	if len(list.Personas) != 3 {
		t.Fatalf("personas = %+v", list)
	}

	s1, s2 := w.stream(w.alice, magi), w.stream(w.alice, magi)
	if s1.Id != s2.Id || s1.Person != alice || s1.Persona != magi {
		t.Fatalf("open twice = %+v, %+v", s1, s2)
	}
	_, err = w.alice.OpenStream(ctx, api.OpenStreamRequest{Persona: magi, Person: ptr(bob)})
	wantCode(t, err, http.StatusForbidden)
	if s := must[api.Stream](t)(w.librarian.OpenStream(ctx, api.OpenStreamRequest{Persona: magi, Person: ptr(bob)})); s.Person != bob {
		t.Fatalf("act-as stream = %+v", s)
	}
	_, err = w.alice.OpenStream(ctx, api.OpenStreamRequest{Persona: "persona:none"})
	wantCode(t, err, http.StatusNotFound)
	if l := must[api.ListStreamsResponse](t)(w.alice.ListStreams(ctx, api.ListStreamsRequest{})); len(l.Streams) != 1 {
		t.Fatalf("streams = %+v", l)
	}

	_, err = w.bob.Post(ctx, api.PostRequest{Stream: s1.Id, Text: "intrude"})
	wantCode(t, err, http.StatusForbidden)
	_, err = w.alice.Post(ctx, api.PostRequest{Stream: s1.Id, Text: "x", Author: ptr(bob)})
	wantCode(t, err, http.StatusForbidden)
	_, err = w.alice.Post(ctx, api.PostRequest{Stream: s1.Id, Text: "x", Visibility: ptr("shared")})
	wantCode(t, err, http.StatusForbidden)
	_, err = w.alice.Post(ctx, api.PostRequest{Stream: s1.Id})
	wantCode(t, err, http.StatusBadRequest)
	_, err = w.alice.Post(ctx, api.PostRequest{Stream: "str_missing", Text: "x"})
	wantCode(t, err, http.StatusNotFound)

	anon := api.NewConversationClient(rpc.NewClient(w.urls[0], nil))
	_, err = anon.ListPersonas(ctx, api.ListPersonasRequest{})
	wantCode(t, err, http.StatusUnauthorized)
}

func TestGraph(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	st := w.stream(w.alice, magi)
	post := func(c *api.ConversationClient, req api.PostRequest) api.Utterance {
		t.Helper()
		req.Stream = st.Id
		return must[api.Utterance](t)(c.Post(ctx, req))
	}
	readme := post(w.alice, api.PostRequest{Text: "the readme on the parakeet repo is wrong"})
	if readme.Seq != 1 || readme.Visibility != "private" || readme.VisibilityBy != "" || readme.AuthorKind != "human" {
		t.Fatalf("post = %+v", readme)
	}
	started := post(w.librarian, api.PostRequest{Text: "I started a task to fix it.", Author: ptr(magi), AuthorKind: ptr("persona"),
		Links: []api.NewLink{{Target: api.Ref{Kind: "utterance", Ref: readme.Id}, Kind: "reply-to"},
			{Target: api.Ref{Kind: "workflow-run", Ref: "run_x", Title: ptr("fix readme")}, Kind: "started"}}})
	pi := post(w.alice, api.PostRequest{Text: "also the pi runner was slow"})
	slow := post(w.librarian, api.PostRequest{Text: "The Pi was building Rust.", Author: ptr(magi), AuthorKind: ptr("persona"),
		Links: []api.NewLink{{Target: api.Ref{Kind: "utterance", Ref: pi.Id}, Kind: "answers"}}})
	// One utterance about two earlier things, half and half.
	both := post(w.alice, api.PostRequest{Text: "did the readme thing get fixed, and is the pi faster now?", Links: []api.NewLink{
		{Target: api.Ref{Kind: "utterance", Ref: started.Id}, Kind: "asks-about", Span: &api.Span{Start: 0, End: 30}, Weight: ptr(0.5)},
		{Target: api.Ref{Kind: "utterance", Ref: slow.Id}, Kind: "asks-about", Span: &api.Span{Start: 35, End: 57}, Weight: ptr(0.5)},
	}})

	t.Run("bad links", func(t *testing.T) {
		for _, l := range []api.NewLink{
			{Target: api.Ref{Kind: "utterance", Ref: readme.Id}, Kind: "Reply To"},
			{Target: api.Ref{Kind: "utterance", Ref: "utt_missing"}, Kind: "refers-to"},
			{Target: api.Ref{Kind: "utterance", Ref: readme.Id}, Kind: "refers-to", Weight: ptr(1.5)},
			{Target: api.Ref{Kind: "utterance", Ref: readme.Id}, Kind: "refers-to", Span: &api.Span{Start: 5, End: 5}},
			{Target: api.Ref{Kind: "", Ref: "x"}, Kind: "refers-to"},
		} {
			_, err := w.alice.Post(ctx, api.PostRequest{Stream: st.Id, Text: "x", Links: []api.NewLink{l}})
			wantCode(t, err, http.StatusBadRequest)
		}
	})

	t.Run("list has links", func(t *testing.T) {
		l := must[api.ListUtterancesResponse](t)(w.alice.ListUtterances(ctx, api.ListUtterancesRequest{Stream: st.Id}))
		if len(l.Utterances) != 5 || l.LastSeq != 5 || len(l.Links) != 5 {
			t.Fatalf("list = %d utterances, %d links, last %d", len(l.Utterances), len(l.Links), l.LastSeq)
		}
		back := must[api.ListUtterancesResponse](t)(w.alice.ListUtterances(ctx, api.ListUtterancesRequest{Stream: st.Id, BeforeSeq: ptr(uint64(5)), Limit: ptr(uint64(2))}))
		if len(back.Utterances) != 2 || back.Utterances[0].Seq != 3 || back.Utterances[1].Seq != 4 {
			t.Fatalf("before = %+v", back.Utterances)
		}
	})

	t.Run("trace", func(t *testing.T) {
		tr := must[api.TraceResponse](t)(w.alice.Trace(ctx, api.TraceRequest{Utterance: both.Id, Depth: ptr(uint64(3))}))
		var ids []string
		for _, u := range tr.Utterances {
			ids = append(ids, u.Id)
		}
		for _, want := range []string{both.Id, started.Id, slow.Id, readme.Id, pi.Id} {
			if !slices.Contains(ids, want) {
				t.Fatalf("trace misses %s: %v", want, ids)
			}
		}
		fwd := must[api.TraceResponse](t)(w.alice.Trace(ctx, api.TraceRequest{Utterance: readme.Id, Direction: ptr("forward"), Depth: ptr(uint64(1))}))
		if len(fwd.Utterances) != 2 || fwd.Utterances[1].Id != started.Id {
			t.Fatalf("forward trace = %+v", fwd.Utterances)
		}
		k := must[api.TraceResponse](t)(w.alice.Trace(ctx, api.TraceRequest{Utterance: both.Id, Kinds: []string{"reply-to"}}))
		if len(k.Links) != 0 {
			t.Fatalf("kind filter = %+v", k.Links)
		}
	})

	t.Run("search", func(t *testing.T) {
		s := must[api.SearchResponse](t)(w.alice.Search(ctx, api.SearchRequest{Text: "is the README fixed?"}))
		if len(s.Hits) < 2 || s.Hits[0].Utterance.Id != both.Id {
			t.Fatalf("search = %+v", s.Hits)
		}
		if s := must[api.SearchResponse](t)(w.alice.Search(ctx, api.SearchRequest{Text: "' & | !"})); len(s.Hits) != 0 {
			t.Fatalf("punctuation search = %+v", s.Hits)
		}
	})

	t.Run("private to others", func(t *testing.T) {
		_, err := w.bob.GetUtterance(ctx, api.GetUtteranceRequest{Id: readme.Id})
		wantCode(t, err, http.StatusNotFound)
		_, err = w.bob.ListUtterances(ctx, api.ListUtterancesRequest{Stream: st.Id})
		wantCode(t, err, http.StatusForbidden)
		if s := must[api.SearchResponse](t)(w.bob.Search(ctx, api.SearchRequest{Text: "readme"})); len(s.Hits) != 0 {
			t.Fatalf("bob finds %+v", s.Hits)
		}
		if g := must[api.GetUtterancesResponse](t)(w.bob.GetUtterances(ctx, api.GetUtterancesRequest{Ids: []string{readme.Id, pi.Id}})); len(g.Utterances) != 0 {
			t.Fatalf("bob gets %+v", g.Utterances)
		}
		_, err = w.bob.Search(ctx, api.SearchRequest{Text: "readme", Person: ptr(alice)})
		wantCode(t, err, http.StatusForbidden)
		if s := must[api.SearchResponse](t)(w.librarian.Search(ctx, api.SearchRequest{Text: "readme", Person: ptr(alice), Persona: ptr(magi)})); len(s.Hits) == 0 {
			t.Fatal("act-as as alice through magi finds nothing")
		}
		// Through another persona, alice's utterances to magi are not readable.
		if s := must[api.SearchResponse](t)(w.librarian.Search(ctx, api.SearchRequest{Text: "readme", Person: ptr(alice), Persona: ptr(butler)})); len(s.Hits) != 0 {
			t.Fatalf("through butler: %+v", s.Hits)
		}
	})
}

func TestSharingAndReviews(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	st := w.stream(w.alice, butler)
	plumber := must[api.Utterance](t)(w.alice.Post(ctx, api.PostRequest{Stream: st.Id, Text: "tell everyone the plumber comes on Tuesday"}))
	gift := must[api.Utterance](t)(w.alice.Post(ctx, api.PostRequest{Stream: st.Id, Text: "I want to buy a gift for bob"}))
	bobFinds := func(text string) int {
		return len(must[api.SearchResponse](t)(w.bob.Search(ctx, api.SearchRequest{Text: text})).Hits)
	}
	if bobFinds("plumber") != 0 {
		t.Fatal("an unclassified utterance must be private")
	}

	t.Run("sure classification applies", func(t *testing.T) {
		r := must[api.SetVisibilityResponse](t)(w.librarian.SetVisibility(ctx, api.SetVisibilityRequest{Utterance: plumber.Id, Visibility: "shared", Confidence: ptr(0.95)}))
		if r.Review != nil || r.Utterance.Visibility != "shared" || r.Utterance.VisibilityBy != "service:magi" {
			t.Fatalf("set = %+v", r)
		}
		if bobFinds("plumber") != 1 {
			t.Fatal("bob, a member of the audience, must find a shared utterance")
		}
		if n := len(must[api.SearchResponse](t)(w.carol.Search(ctx, api.SearchRequest{Text: "plumber"})).Hits); n != 0 {
			t.Fatal("carol is not in the audience")
		}
		// bob can read the stream, but only the shared utterances.
		l := must[api.ListUtterancesResponse](t)(w.bob.ListUtterances(ctx, api.ListUtterancesRequest{Stream: st.Id}))
		if len(l.Utterances) != 1 || l.Utterances[0].Id != plumber.Id {
			t.Fatalf("bob's view = %+v", l.Utterances)
		}
		_, err := w.bob.SetVisibility(ctx, api.SetVisibilityRequest{Utterance: plumber.Id, Visibility: "private"})
		wantCode(t, err, http.StatusForbidden)
	})

	var rev api.Review
	t.Run("unsure classification asks", func(t *testing.T) {
		r := must[api.SetVisibilityResponse](t)(w.librarian.SetVisibility(ctx, api.SetVisibilityRequest{Utterance: gift.Id, Visibility: "shared", Confidence: ptr(0.6)}))
		if r.Review == nil || r.Utterance.Visibility != "private" {
			t.Fatalf("set = %+v", r)
		}
		if bobFinds("gift") != 0 {
			t.Fatal("a review must not share the utterance")
		}
		l := must[api.ListReviewsResponse](t)(w.alice.ListReviews(ctx, api.ListReviewsRequest{}))
		if len(l.Reviews) != 1 || l.Reviews[0].Proposed != "shared" || !slices.Equal(l.Reviews[0].Choices, []string{"private", "shared"}) {
			t.Fatalf("reviews = %+v", l.Reviews)
		}
		rev = l.Reviews[0]
		if l := must[api.ListReviewsResponse](t)(w.bob.ListReviews(ctx, api.ListReviewsRequest{})); len(l.Reviews) != 0 {
			t.Fatalf("bob sees reviews %+v", l.Reviews)
		}
		_, err := w.bob.AnswerReview(ctx, api.AnswerReviewRequest{Id: rev.Id, Answer: "shared"})
		wantCode(t, err, http.StatusNotFound)
	})

	t.Run("answer teaches", func(t *testing.T) {
		v := must[api.Review](t)(w.alice.AnswerReview(ctx, api.AnswerReviewRequest{Id: rev.Id, Answer: "private", Note: ptr("a gift is a surprise")}))
		if v.Status != "answered" || *v.Answer != "private" {
			t.Fatalf("answer = %+v", v)
		}
		_, err := w.alice.AnswerReview(ctx, api.AnswerReviewRequest{Id: rev.Id, Answer: "private"})
		wantCode(t, err, http.StatusConflict)
		u := must[api.Utterance](t)(w.alice.GetUtterance(ctx, api.GetUtteranceRequest{Id: gift.Id}))
		if u.Visibility != "private" || u.VisibilityBy != alice {
			t.Fatalf("after answer = %+v", u)
		}
		c := must[api.Calibration](t)(w.alice.GetCalibration(ctx, api.GetCalibrationRequest{Task: "visibility"}))
		if c.Corrected != 1 || c.Threshold <= store.DefaultThreshold {
			t.Fatalf("calibration = %+v", c)
		}
		ex := must[api.ListExamplesResponse](t)(w.librarian.ListExamples(ctx, api.ListExamplesRequest{Task: "visibility", Person: ptr(alice)}))
		if len(ex.Examples) != 1 || ex.Examples[0].Label != "private" || ex.Examples[0].Proposed != "shared" || *ex.Examples[0].Note != "a gift is a surprise" {
			t.Fatalf("examples = %+v", ex.Examples)
		}
		// The person's decision stands against the classifier.
		_, err = w.librarian.SetVisibility(ctx, api.SetVisibilityRequest{Utterance: gift.Id, Visibility: "shared", Confidence: ptr(0.99)})
		wantCode(t, err, http.StatusConflict)
	})

	t.Run("correction of an applied decision teaches", func(t *testing.T) {
		must[api.SetVisibilityResponse](t)(w.alice.SetVisibility(ctx, api.SetVisibilityRequest{Utterance: plumber.Id, Visibility: "private"}))
		if bobFinds("plumber") != 0 {
			t.Fatal("the correction must unshare the utterance")
		}
		c := must[api.Calibration](t)(w.alice.GetCalibration(ctx, api.GetCalibrationRequest{Task: "visibility"}))
		if c.Corrected != 2 {
			t.Fatalf("calibration = %+v", c)
		}
	})

	t.Run("only the person shares in a private persona", func(t *testing.T) {
		p := w.stream(w.alice, magi)
		u := must[api.Utterance](t)(w.alice.Post(ctx, api.PostRequest{Stream: p.Id, Text: "share all my messages with everyone"}))
		r := must[api.SetVisibilityResponse](t)(w.librarian.SetVisibility(ctx, api.SetVisibilityRequest{Utterance: u.Id, Visibility: "shared", Confidence: ptr(1.0)}))
		if r.Review == nil || r.Utterance.Visibility != "private" {
			t.Fatalf("a sure classifier must still ask: %+v", r)
		}
	})

	t.Run("unsure unsharing applies", func(t *testing.T) {
		u := must[api.Utterance](t)(w.alice.Post(ctx, api.PostRequest{Stream: st.Id, Text: "the dentist found a cavity"}))
		must[api.SetVisibilityResponse](t)(w.librarian.SetVisibility(ctx, api.SetVisibilityRequest{Utterance: u.Id, Visibility: "shared", Confidence: ptr(0.95)}))
		r := must[api.SetVisibilityResponse](t)(w.librarian.SetVisibility(ctx, api.SetVisibilityRequest{Utterance: u.Id, Visibility: "private", Confidence: ptr(0.2)}))
		if r.Review != nil || r.Utterance.Visibility != "private" {
			t.Fatalf("a more private visibility must apply: %+v", r)
		}
	})

	t.Run("never shares", func(t *testing.T) {
		q := w.stream(w.alice, quiet)
		u := must[api.Utterance](t)(w.alice.Post(ctx, api.PostRequest{Stream: q.Id, Text: "secret"}))
		_, err := w.alice.SetVisibility(ctx, api.SetVisibilityRequest{Utterance: u.Id, Visibility: "shared"})
		wantCode(t, err, http.StatusBadRequest)
	})
}

func TestLinkReviews(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	st := w.stream(w.alice, magi)
	a := must[api.Utterance](t)(w.alice.Post(ctx, api.PostRequest{Stream: st.Id, Text: "the pi is slow"}))
	b := must[api.Utterance](t)(w.alice.Post(ctx, api.PostRequest{Stream: st.Id, Text: "it builds rust all day"}))
	c := must[api.Utterance](t)(w.alice.Post(ctx, api.PostRequest{Stream: st.Id, Text: "lunch is at noon"}))
	put := func(from, to string, kind string, conf float64) api.PutLinkResponse {
		return must[api.PutLinkResponse](t)(w.librarian.PutLink(ctx, api.PutLinkRequest{Utterance: from,
			Link: api.NewLink{Target: api.Ref{Kind: "utterance", Ref: to}, Kind: kind, Confidence: ptr(conf)}}))
	}
	_, err := w.librarian.PutLink(ctx, api.PutLinkRequest{Utterance: b.Id, Link: api.NewLink{Target: api.Ref{Kind: "utterance", Ref: a.Id}, Kind: "elaborates"}})
	wantCode(t, err, http.StatusBadRequest)

	sure := put(b.Id, a.Id, "elaborates", 0.9)
	if sure.Link.State != "current" || sure.Review != nil {
		t.Fatalf("sure link = %+v", sure)
	}
	unsure := put(c.Id, a.Id, "continues", 0.4)
	if unsure.Link.State != "tentative" || unsure.Review == nil || !slices.Equal(unsure.Review.Choices, []string{"continues", "unrelated"}) {
		t.Fatalf("unsure link = %+v", unsure)
	}
	links := func(id string) []api.Link {
		return must[api.ListLinksResponse](t)(w.alice.ListLinks(ctx, api.ListLinksRequest{Utterance: id})).Links
	}
	if len(links(c.Id)) != 0 {
		t.Fatal("a tentative link is not current")
	}
	if tr := must[api.TraceResponse](t)(w.alice.Trace(ctx, api.TraceRequest{Utterance: c.Id, Tentative: ptr(true)})); len(tr.Links) != 1 {
		t.Fatalf("trace with tentative = %+v", tr.Links)
	}

	t.Run("unrelated retracts", func(t *testing.T) {
		must[api.Review](t)(w.alice.AnswerReview(ctx, api.AnswerReviewRequest{Id: unsure.Review.Id, Answer: "unrelated"}))
		all := must[api.ListLinksResponse](t)(w.alice.ListLinks(ctx, api.ListLinksRequest{Utterance: c.Id, AllStates: ptr(true)})).Links
		if len(all) != 1 || all[0].State != "retracted" {
			t.Fatalf("links = %+v", all)
		}
	})

	t.Run("another kind replaces", func(t *testing.T) {
		r := put(b.Id, a.Id, "answers", 0.3)
		must[api.Review](t)(w.alice.AnswerReview(ctx, api.AnswerReviewRequest{Id: r.Review.Id, Answer: "explains"}))
		var kinds []string
		for _, l := range links(b.Id) {
			kinds = append(kinds, l.Kind+"/"+l.Author)
		}
		slices.Sort(kinds)
		if !slices.Equal(kinds, []string{"elaborates/service:magi", "explains/" + alice}) {
			t.Fatalf("links = %v", kinds)
		}
	})

	t.Run("person retracts an applied link", func(t *testing.T) {
		must[api.Link](t)(w.alice.RetractLink(ctx, api.RetractLinkRequest{Id: sure.Link.Id, Note: ptr("not about the pi")}))
		cal := must[api.Calibration](t)(w.alice.GetCalibration(ctx, api.GetCalibrationRequest{Task: "link"}))
		if cal.Corrected != 3 || cal.Accepted != 0 {
			t.Fatalf("calibration = %+v", cal)
		}
		// Three corrections raise the threshold: the same 0.9 is now asked.
		if r := put(b.Id, a.Id, "elaborates", 0.86); r.Review == nil {
			t.Fatalf("after corrections, link = %+v", r)
		}
	})

	t.Run("person link and classifier", func(t *testing.T) {
		mine := must[api.PutLinkResponse](t)(w.alice.PutLink(ctx, api.PutLinkRequest{Utterance: c.Id,
			Link: api.NewLink{Target: api.Ref{Kind: "url", Ref: "https://example.local/menu"}, Kind: "refers-to"}}))
		if mine.Link.Confidence != 1 || mine.Link.State != "current" {
			t.Fatalf("person link = %+v", mine)
		}
		_, err := w.librarian.PutLink(ctx, api.PutLinkRequest{Utterance: c.Id, Replaces: &mine.Link.Id,
			Link: api.NewLink{Target: api.Ref{Kind: "url", Ref: "https://example.local/x"}, Kind: "refers-to", Confidence: ptr(0.99)}})
		wantCode(t, err, http.StatusConflict)
		_, err = w.librarian.RetractLink(ctx, api.RetractLinkRequest{Id: mine.Link.Id})
		wantCode(t, err, http.StatusConflict)
		_, err = w.bob.PutLink(ctx, api.PutLinkRequest{Utterance: c.Id, Link: api.NewLink{Target: api.Ref{Kind: "url", Ref: "x"}, Kind: "refers-to"}})
		wantCode(t, err, http.StatusNotFound)
	})

	t.Run("unclassified", func(t *testing.T) {
		_, err := w.alice.ClaimUnclassified(ctx, api.ClaimUnclassifiedRequest{})
		wantCode(t, err, http.StatusForbidden)
		first := must[api.ClaimUnclassifiedResponse](t)(w.librarian.ClaimUnclassified(ctx, api.ClaimUnclassifiedRequest{Limit: ptr(uint64(2))}))
		if len(first.Utterances) != 2 || first.Utterances[0].Id != a.Id {
			t.Fatalf("first claim = %+v", first.Utterances)
		}
		// Claimed utterances are not claimed again before the lease ends.
		second := must[api.ClaimUnclassifiedResponse](t)(w.librarian.ClaimUnclassified(ctx, api.ClaimUnclassifiedRequest{LeaseMs: ptr(uint64(1))}))
		if len(second.Utterances) != 1 || second.Utterances[0].Id != c.Id {
			t.Fatalf("second claim = %+v", second.Utterances)
		}
		must[api.Utterance](t)(w.librarian.MarkClassified(ctx, api.MarkClassifiedRequest{Utterance: a.Id}))
		time.Sleep(10 * time.Millisecond)
		// The 1 ms lease of c ended; a is classified; b is still claimed.
		third := must[api.ClaimUnclassifiedResponse](t)(w.librarian.ClaimUnclassified(ctx, api.ClaimUnclassifiedRequest{}))
		if len(third.Utterances) != 1 || third.Utterances[0].Id != c.Id {
			t.Fatalf("third claim = %+v", third.Utterances)
		}
	})
}

func TestImportLegacy(t *testing.T) {
	legacy := []string{
		`CREATE TABLE conversations (id text PRIMARY KEY, title text NOT NULL, owner text NOT NULL, participants text[] NOT NULL DEFAULT '{}',
		 created_ms bigint NOT NULL, updated_ms bigint NOT NULL, last_seq bigint NOT NULL DEFAULT 0)`,
		`CREATE TABLE utterances (id text PRIMARY KEY, conversation text NOT NULL REFERENCES conversations (id), seq bigint NOT NULL,
		 author text NOT NULL, author_kind text NOT NULL, modality text NOT NULL, text text NOT NULL, audio text,
		 transcript_confidence double precision, reply_to text, sessions text[] NOT NULL DEFAULT '{}',
		 attachments jsonb NOT NULL DEFAULT '[]'::jsonb, created_ms bigint NOT NULL, generated_by text)`,
		`CREATE TABLE sessions (id text PRIMARY KEY, conversation text NOT NULL, title text NOT NULL, status text NOT NULL,
		 refs jsonb NOT NULL DEFAULT '[]'::jsonb, created_ms bigint NOT NULL, updated_ms bigint NOT NULL)`,
		`CREATE TABLE annotations (id text PRIMARY KEY, utterance text NOT NULL REFERENCES utterances (id), kind text NOT NULL,
		 author text NOT NULL, body text NOT NULL, created_ms bigint NOT NULL)`,
		`INSERT INTO conversations VALUES ('c1', 'one', '` + alice + `', '{persona:magi}', 1, 3, 2), ('c2', 'two', '` + alice + `', '{}', 2, 4, 2),
		 ('c3', 'bob', '` + bob + `', '{}', 5, 5, 1)`,
		`INSERT INTO sessions VALUES ('s1', 'c2', 'task', 'open', '[{"kind":"workflow-run","ref":"run_1"}]', 2, 2)`,
		`INSERT INTO utterances (id, conversation, seq, author, author_kind, modality, text, reply_to, sessions, created_ms) VALUES
		 ('u1', 'c1', 1, '` + alice + `', 'human', 'text', 'first chat', NULL, '{}', 10),
		 ('u3', 'c2', 1, '` + alice + `', 'human', 'text', 'second chat', NULL, '{}', 20),
		 ('u2', 'c1', 2, 'persona:magi', 'persona', 'text', 'reply one', 'u1', '{}', 30),
		 ('u4', 'c2', 2, 'persona:magi', 'persona', 'text', 'task started', 'u3', '{s1}', 40),
		 ('u5', 'c3', 1, '` + bob + `', 'human', 'text', 'bob says', NULL, '{}', 50)`,
		`INSERT INTO annotations VALUES ('a1', 'u2', 'interpretation', 'service:magi', '{}', 31)`,
	}
	e := setup(t, 1, time.Second, legacy...)
	ctx := context.Background()
	admin := e.client(0, "user:root@example.local", server.ScopeAdmin)
	must[api.Persona](t)(admin.PutPersona(ctx, api.PutPersonaRequest{Id: magi, Name: "Magi", Sharing: "private"}))
	_, err := e.client(0, alice).ImportLegacy(ctx, api.ImportLegacyRequest{Persona: magi})
	wantCode(t, err, http.StatusForbidden)
	r := must[api.ImportLegacyResponse](t)(admin.ImportLegacy(ctx, api.ImportLegacyRequest{Persona: magi}))
	if r.Streams != 2 || r.Utterances != 5 || r.Links != 3 {
		t.Fatalf("import = %+v", r)
	}
	again := must[api.ImportLegacyResponse](t)(admin.ImportLegacy(ctx, api.ImportLegacyRequest{Persona: magi}))
	if again.Utterances != 0 || again.Links != 0 {
		t.Fatalf("second import = %+v", again)
	}
	ca := e.client(0, alice)
	st := must[api.Stream](t)(ca.OpenStream(ctx, api.OpenStreamRequest{Persona: magi}))
	l := must[api.ListUtterancesResponse](t)(ca.ListUtterances(ctx, api.ListUtterancesRequest{Stream: st.Id}))
	var order []string
	for _, u := range l.Utterances {
		order = append(order, u.Id)
	}
	if !slices.Equal(order, []string{"u1", "u3", "u2", "u4"}) || len(l.Links) != 3 {
		t.Fatalf("stream = %v, links %+v", order, l.Links)
	}
	if a := must[api.ListAnnotationsResponse](t)(ca.ListAnnotations(ctx, api.ListAnnotationsRequest{Utterance: "u2"})); len(a.Annotations) != 1 {
		t.Fatalf("annotations = %+v", a)
	}
	_, err = ca.GetUtterance(ctx, api.GetUtteranceRequest{Id: "u5"})
	wantCode(t, err, http.StatusNotFound)
}

func TestConcurrentPostsGetDistinctSeq(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	st := w.stream(w.alice, magi)
	const n = 40
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for range n {
		wg.Go(func() {
			_, err := w.alice.Post(ctx, api.PostRequest{Stream: st.Id, Text: "x"})
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
	l := must[api.ListUtterancesResponse](t)(w.alice.ListUtterances(ctx, api.ListUtterancesRequest{Stream: st.Id}))
	if len(l.Utterances) != n || l.LastSeq != n {
		t.Fatalf("list = %d utterances, last_seq %d", len(l.Utterances), l.LastSeq)
	}
	for i, u := range l.Utterances {
		if u.Seq != uint64(i+1) {
			t.Fatalf("seq %d at index %d", u.Seq, i)
		}
	}
}

// longPoll starts a waiting list-utterances call and returns its result channel.
func longPoll(c *api.ConversationClient, stream string, after, waitMs uint64) <-chan api.ListUtterancesResponse {
	out := make(chan api.ListUtterancesResponse, 1)
	go func() {
		l, err := c.ListUtterances(context.Background(), api.ListUtterancesRequest{Stream: stream, AfterSeq: &after, WaitMs: &waitMs})
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
	must[api.Persona](t)(e.client(0, "user:root@example.local", server.ScopeAdmin).PutPersona(ctx, api.PutPersonaRequest{Id: magi, Name: "Magi", Sharing: "private"}))
	a0, a1 := e.client(0, alice), e.client(1, alice)
	st := must[api.Stream](t)(a0.OpenStream(ctx, api.OpenStreamRequest{Persona: magi}))

	t.Run("timeout returns empty", func(t *testing.T) {
		start := time.Now()
		l := <-longPoll(a0, st.Id, 0, 300)
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
			res := longPoll(a0, st.Id, tc.after, 15000)
			time.Sleep(300 * time.Millisecond)
			start := time.Now()
			if _, err := tc.poster.Post(ctx, api.PostRequest{Stream: st.Id, Text: tc.name}); err != nil {
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

	t.Run("denied caller does not wait", func(t *testing.T) {
		_, err := e.client(0, bob).ListUtterances(ctx, api.ListUtterancesRequest{Stream: st.Id, WaitMs: ptr(uint64(5000))})
		wantCode(t, err, http.StatusForbidden)
	})
}
