package server_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/catalystcommunity/magi/lib/rpc"
	api "github.com/catalystcommunity/magi/primitives/artifacts/api"
	"github.com/catalystcommunity/magi/primitives/artifacts/internal/testenv"
	"github.com/catalystcommunity/magi/primitives/artifacts/server"
	"github.com/catalystcommunity/magi/primitives/artifacts/store"
)

type env struct {
	t    *testing.T
	url  string
	keys testenv.Keys
}

func setup(t *testing.T) env {
	ctx := context.Background()
	db, err := store.Open(ctx, testenv.Database(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	keys := testenv.NewKeys(t)
	// Each call to Now advances one millisecond, so ordering is exact.
	var clock atomic.Int64
	clock.Store(1_700_000_000_000)
	svc := &server.Service{DB: db, Blobs: store.Blobs{Dir: t.TempDir()},
		Now: func() time.Time { return time.UnixMilli(clock.Add(1)) }}
	s := &rpc.Server{Verifier: keys.Verifier(t), MaxBody: server.MaxRequest}
	server.Register(s, svc)
	hs := httptest.NewServer(rpc.Mux(s, nil))
	t.Cleanup(hs.Close)
	return env{t: t, url: hs.URL, keys: keys}
}

func (e env) client(subject string, scopes ...string) *api.ArtifactClient {
	return api.NewArtifactClient(rpc.NewClient(e.url, rpc.StaticToken(e.keys.Token(e.t, subject, scopes...))))
}

func wantCode(t *testing.T, err error, code int) {
	t.Helper()
	if rpc.CodeOf(err) != code {
		t.Fatalf("error = %v, want code %d", err, code)
	}
}

func ptr[T any](v T) *T { return &v }

func TestArtifacts(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	alice := e.client("user:alice@example.local")
	bob := e.client("user:bob@example.local")
	svc := e.client("service:magi", server.ScopeActAs)
	admin := e.client("user:root@example.local", server.ScopeAdmin)

	a, err := alice.Put(ctx, api.PutRequest{MediaType: "text/plain", Body: []byte("hello"), Labels: map[string]string{"kind": "note"}})
	if err != nil {
		t.Fatal(err)
	}
	if a.Digest != store.Digest([]byte("hello")) || a.Size != 5 || a.Owner != "user:alice@example.local" || a.Labels["kind"] != "note" {
		t.Fatalf("put = %+v", a)
	}

	t.Run("put again merges labels", func(t *testing.T) {
		a2, err := alice.Put(ctx, api.PutRequest{MediaType: "text/plain", Body: []byte("hello"), Labels: map[string]string{"x": "1"}})
		if err != nil {
			t.Fatal(err)
		}
		if a2.CreatedMs != a.CreatedMs || a2.Labels["kind"] != "note" || a2.Labels["x"] != "1" {
			t.Fatalf("re-put = %+v", a2)
		}
	})

	t.Run("get and stat by owner", func(t *testing.T) {
		g, err := alice.Get(ctx, api.GetRequest{Digest: a.Digest})
		if err != nil || !bytes.Equal(g.Body, []byte("hello")) || g.Artifact.Owner != a.Owner {
			t.Fatalf("get = %+v, %v", g, err)
		}
		st, err := alice.Stat(ctx, api.StatRequest{Digest: a.Digest})
		if err != nil || st.Size != 5 {
			t.Fatalf("stat = %+v, %v", st, err)
		}
	})

	t.Run("other user cannot read", func(t *testing.T) {
		_, err := bob.Get(ctx, api.GetRequest{Digest: a.Digest})
		wantCode(t, err, http.StatusNotFound)
		_, err = bob.Stat(ctx, api.StatRequest{Digest: a.Digest})
		wantCode(t, err, http.StatusNotFound)
		_, err = bob.Label(ctx, api.LabelRequest{Digest: a.Digest, Labels: map[string]string{"y": "2"}})
		wantCode(t, err, http.StatusNotFound)
	})

	t.Run("same body for another owner is a separate record", func(t *testing.T) {
		b, err := bob.Put(ctx, api.PutRequest{MediaType: "text/markdown", Body: []byte("hello")})
		if err != nil || b.Digest != a.Digest || b.Owner != "user:bob@example.local" || b.MediaType != "text/markdown" {
			t.Fatalf("bob put = %+v, %v", b, err)
		}
		g, err := bob.Get(ctx, api.GetRequest{Digest: a.Digest})
		if err != nil || g.Artifact.Owner != b.Owner {
			t.Fatalf("bob get = %+v, %v", g.Artifact, err)
		}
	})

	t.Run("act-as and admin can read", func(t *testing.T) {
		for _, c := range []*api.ArtifactClient{svc, admin} {
			g, err := c.Get(ctx, api.GetRequest{Digest: a.Digest})
			if err != nil || string(g.Body) != "hello" {
				t.Fatalf("privileged get: %v", err)
			}
			if _, err := c.Stat(ctx, api.StatRequest{Digest: a.Digest}); err != nil {
				t.Fatal(err)
			}
		}
	})

	t.Run("owner override", func(t *testing.T) {
		_, err := bob.Put(ctx, api.PutRequest{MediaType: "text/plain", Body: []byte("x"), Owner: ptr("user:alice@example.local")})
		wantCode(t, err, http.StatusForbidden)
		o, err := svc.Put(ctx, api.PutRequest{MediaType: "audio/ogg", Body: []byte("voice"), Owner: ptr("user:alice@example.local")})
		if err != nil || o.Owner != "user:alice@example.local" {
			t.Fatalf("act-as put = %+v, %v", o, err)
		}
		if _, err := alice.Get(ctx, api.GetRequest{Digest: o.Digest}); err != nil {
			t.Fatalf("owner get of act-as record: %v", err)
		}
	})

	t.Run("label", func(t *testing.T) {
		l, err := alice.Label(ctx, api.LabelRequest{Digest: a.Digest, Labels: map[string]string{"kind": "transcript"}})
		if err != nil || l.Labels["kind"] != "transcript" || l.Labels["x"] != "1" {
			t.Fatalf("label = %+v, %v", l, err)
		}
		_, err = alice.Label(ctx, api.LabelRequest{Digest: "nope", Labels: map[string]string{}})
		wantCode(t, err, http.StatusBadRequest)
	})

	t.Run("list", func(t *testing.T) {
		for i := range 3 {
			if _, err := alice.Put(ctx, api.PutRequest{MediaType: "text/plain", Body: []byte{byte(i)}, Labels: map[string]string{"batch": "b"}}); err != nil {
				t.Fatal(err)
			}
		}
		all, err := alice.List(ctx, api.ListRequest{})
		if err != nil || len(all.Artifacts) != 5 {
			t.Fatalf("list = %d, %v", len(all.Artifacts), err)
		}
		for i := 1; i < len(all.Artifacts); i++ {
			if all.Artifacts[i].CreatedMs > all.Artifacts[i-1].CreatedMs {
				t.Fatal("list is not newest first")
			}
		}
		for _, x := range all.Artifacts {
			if x.Owner != "user:alice@example.local" {
				t.Fatalf("list returned record of %s", x.Owner)
			}
		}
		page, err := alice.List(ctx, api.ListRequest{Limit: ptr(uint64(2))})
		if err != nil || len(page.Artifacts) != 2 {
			t.Fatalf("limit: %d %v", len(page.Artifacts), err)
		}
		next, err := alice.List(ctx, api.ListRequest{BeforeMs: ptr(page.Artifacts[1].CreatedMs)})
		if err != nil || len(next.Artifacts) != 3 {
			t.Fatalf("before_ms: %d %v", len(next.Artifacts), err)
		}
		byKey, _ := alice.List(ctx, api.ListRequest{LabelKey: ptr("batch")})
		byValue, _ := alice.List(ctx, api.ListRequest{LabelKey: ptr("kind"), LabelValue: ptr("transcript")})
		if len(byKey.Artifacts) != 3 || len(byValue.Artifacts) != 1 {
			t.Fatalf("label filters: %d %d", len(byKey.Artifacts), len(byValue.Artifacts))
		}
		_, err = alice.List(ctx, api.ListRequest{LabelValue: ptr("x")})
		wantCode(t, err, http.StatusBadRequest)
		other, _ := bob.List(ctx, api.ListRequest{})
		if len(other.Artifacts) != 1 {
			t.Fatalf("bob list = %d", len(other.Artifacts))
		}
	})

	t.Run("validation", func(t *testing.T) {
		_, err := alice.Put(ctx, api.PutRequest{Body: []byte("x")})
		wantCode(t, err, http.StatusBadRequest)
		_, err = alice.Put(ctx, api.PutRequest{MediaType: "application/octet-stream", Body: make([]byte, server.MaxBody+1)})
		wantCode(t, err, http.StatusBadRequest)
		_, err = alice.Get(ctx, api.GetRequest{Digest: "sha256:../../etc/passwd"})
		wantCode(t, err, http.StatusBadRequest)
		_, err = alice.Stat(ctx, api.StatRequest{Digest: store.Digest([]byte("never stored"))})
		wantCode(t, err, http.StatusNotFound)
	})

	t.Run("max body accepted", func(t *testing.T) {
		big := bytes.Repeat([]byte{7}, server.MaxBody)
		x, err := alice.Put(ctx, api.PutRequest{MediaType: "application/octet-stream", Body: big})
		if err != nil || x.Size != server.MaxBody {
			t.Fatalf("max put: %v", err)
		}
		g, err := alice.Get(ctx, api.GetRequest{Digest: x.Digest})
		if err != nil || !bytes.Equal(g.Body, big) {
			t.Fatalf("max get: %v", err)
		}
	})

	t.Run("token required", func(t *testing.T) {
		anon := api.NewArtifactClient(rpc.NewClient(e.url, nil))
		_, err := anon.List(ctx, api.ListRequest{})
		wantCode(t, err, http.StatusUnauthorized)
	})
}
