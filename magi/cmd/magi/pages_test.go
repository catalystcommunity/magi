package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/catalystcommunity/magi/lib/authn"
	"github.com/catalystcommunity/magi/lib/rpc"
	magiapi "github.com/catalystcommunity/magi/magi/api"
)

// fakePages answers the two page calls of the Magi service, so the CLI
// commands can be tested against the real RPC transport and codec.
type fakePages struct {
	magiapi.MagiService
	pages []magiapi.Page
	got   []string // the ids that get-page asked for
}

func (f *fakePages) ListPages(context.Context, magiapi.ListPagesRequest) (magiapi.ListPagesResponse, error) {
	return magiapi.ListPagesResponse{Pages: f.pages}, nil
}

func (f *fakePages) GetPage(_ context.Context, r magiapi.GetPageRequest) (magiapi.Page, error) {
	f.got = append(f.got, r.Id)
	for _, p := range f.pages {
		if p.Id == r.Id {
			return p, nil
		}
	}
	return magiapi.Page{}, rpc.NotFound("no such page")
}

// pageServer serves the fake page store; it returns the server URL and the
// token that the CLI must send.
func pageServer(t *testing.T, f *fakePages) (string, string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ver := &authn.Verifier{Issuer: "test", Keys: map[string]ed25519.PublicKey{"k1": pub}}
	iss := &authn.Issuer{Name: "test", KeyID: "k1", Key: priv}
	tok, err := iss.Issue(authn.Principal{Subject: "user:a@example.local", Kind: authn.KindUser}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	srv := &rpc.Server{Verifier: ver, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	srv.Register("MagiService", magiapi.MagiServiceOps(f))
	h := httptest.NewServer(rpc.Mux(srv, nil))
	t.Cleanup(h.Close)
	return h.URL, tok
}

// pageCLI returns a CLI that talks to a server with the given pages.
func pageCLI(t *testing.T, pages []magiapi.Page) *cli {
	t.Helper()
	f := &fakePages{pages: pages}
	url, tok := pageServer(t, f)
	c := &cli{p: profile{Server: url, Token: tok}, ctx: context.Background()}
	c.magi = magiapi.NewMagiClient(rpc.NewClient(url, rpc.StaticToken(tok)))
	return c
}

// capture collects what a command prints.
func capture(t *testing.T, run func() error) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stdout
	os.Stdout = w
	runErr := run()
	w.Close()
	os.Stdout = saved
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if runErr != nil {
		t.Fatal(runErr)
	}
	return string(b)
}

// pages lists the pages that personas made: the title, the persona, the
// date, and the link that opens the page.
func TestPagesCommand(t *testing.T) {
	pages := []magiapi.Page{
		{Id: "sha256:1", Title: "Trip plan", Text: "# Trip plan", Persona: "magi", CreatedMs: 1770000000000},
		{Id: "sha256:2", Title: "Heating comparison", Text: "table", Persona: "beatrice", CreatedMs: 1770100000000},
	}
	c := pageCLI(t, pages)
	out := capture(t, func() error { return c.run("pages", nil) })
	want := []string{"Trip plan", "magi", "beatrice", c.p.Server + "/#/page/sha256:1", c.p.Server + "/#/page/sha256:2"}
	for _, p := range pages {
		want = append(want, time.UnixMilli(int64(p.CreatedMs)).Format("2006-01-02"))
	}
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Fatalf("pages output %q misses %q", out, w)
		}
	}
	// With no pages the command says so instead of printing nothing.
	empty := pageCLI(t, nil)
	if got := capture(t, func() error { return empty.run("pages", nil) }); got != "no pages\n" {
		t.Fatalf("empty pages output %q", got)
	}
}

// pages show prints the text of one page, and an unknown id is an error.
func TestPagesShowCommand(t *testing.T) {
	c := pageCLI(t, []magiapi.Page{
		{Id: "sha256:1", Title: "Trip plan", Persona: "magi", CreatedMs: 1770000000000,
			Text: "# Trip plan\n\n```mermaid\nflowchart LR\n  a --> b\n```"},
	})
	out := capture(t, func() error { return c.run("pages", []string{"show", "sha256:1"}) })
	for _, want := range []string{"Trip plan", "magi", "# Trip plan", "flowchart LR"} {
		if !strings.Contains(out, want) {
			t.Fatalf("pages show output %q misses %q", out, want)
		}
	}
	// An unknown page is an error, and the subcommand needs an id.
	if err := c.run("pages", []string{"show", "sha256:9"}); err == nil || !strings.Contains(err.Error(), "no such page") {
		t.Fatalf("unknown page error %v", err)
	}
	if err := c.run("pages", []string{"show"}); err == nil || !strings.Contains(err.Error(), "pages show ID") {
		t.Fatalf("missing id error %v", err)
	}
}
