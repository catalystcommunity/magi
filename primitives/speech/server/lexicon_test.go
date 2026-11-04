package server

import (
	"context"
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/catalystcommunity/magi/lib/authn"
	"github.com/catalystcommunity/magi/lib/rpc"
	api "github.com/catalystcommunity/magi/primitives/speech/api"
	"github.com/catalystcommunity/magi/primitives/speech/internal/testenv"
	"github.com/catalystcommunity/magi/primitives/speech/store"
)

func TestLexicon(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, testenv.Database(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	eng := fakeEngine(t)
	svc := &Service{STT: NewEngine(eng.URL, engineToken), TTS: NewEngine(eng.URL, engineToken), Lexicon: db}

	pub, priv, _ := ed25519.GenerateKey(nil)
	iss := &authn.Issuer{Name: "test", KeyID: "k1", Key: priv}
	ver := &authn.Verifier{Issuer: "test", Keys: map[string]ed25519.PublicKey{"k1": pub}}
	s := &rpc.Server{Verifier: ver, MaxBody: MaxRequest}
	Register(s, svc)
	hs := httptest.NewServer(rpc.Mux(s, nil))
	t.Cleanup(hs.Close)
	client := func(p authn.Principal) *api.SpeechClient {
		tok, err := iss.Issue(p, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		return api.NewSpeechClient(rpc.NewClient(hs.URL, rpc.StaticToken(tok)))
	}
	user := client(authn.Principal{Subject: "user:ada@example.local", Kind: authn.KindUser})
	magi := client(authn.Principal{Subject: "service:magi", Kind: authn.KindService, Scopes: []string{ScopeLexicon}})

	// Only a caller with the scope changes the lexicon.
	if _, err := user.PutLexiconEntry(ctx, api.PutLexiconEntryRequest{Written: "README", Spoken: ptr("readme")}); rpc.CodeOf(err) != http.StatusForbidden {
		t.Fatalf("user put = %v, want denied", err)
	}
	e, err := magi.PutLexiconEntry(ctx, api.PutLexiconEntryRequest{Written: " README ", Spoken: ptr("readme"), By: ptr("user:tod@example.local")})
	if err != nil || e.Written != "README" || e.UpdatedBy != "user:tod@example.local" || e.UpdatedMs == 0 {
		t.Fatalf("put = %+v, %v", e, err)
	}
	if _, err := magi.PutLexiconEntry(ctx, api.PutLexiconEntryRequest{Written: "Magi", HeardAs: []string{"Magei", " ", "magi"}}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []api.PutLexiconEntryRequest{
		{Written: ""}, {Written: "x"}, {Written: "--", Spoken: ptr("dash")}, {Written: "a\nb", Spoken: ptr("x")},
		{Written: "x", Ipa: ptr("a/b")}, {Written: "x", Spoken: ptr("[x]")},
	} {
		if _, err := magi.PutLexiconEntry(ctx, bad); rpc.CodeOf(err) != http.StatusBadRequest {
			t.Errorf("put %+v = %v, want invalid", bad, err)
		}
	}
	// A put with the same word in another case replaces the entry.
	if _, err := magi.PutLexiconEntry(ctx, api.PutLexiconEntryRequest{Written: "readme", Spoken: ptr("read me")}); err != nil {
		t.Fatal(err)
	}
	l, err := user.ListLexicon(ctx, api.ListLexiconRequest{})
	if err != nil || len(l.Entries) != 2 || l.Entries[0].Written != "Magi" || len(l.Entries[0].HeardAs) != 1 ||
		l.Entries[1].Written != "readme" || *l.Entries[1].Spoken != "read me" {
		t.Fatalf("list = %+v, %v", l, err)
	}

	// Synthesize and transcribe use it.
	a, err := user.Synthesize(ctx, api.SynthesizeRequest{Text: "See the **README**, 4-6 lines."})
	if err != nil || a.Spoken != "See the read me, 4 to 6 lines." || string(a.Body) != "RIFF"+a.Spoken {
		t.Fatalf("synthesize = %q %q, %v", a.Spoken, a.Body, err)
	}
	a, err = user.Synthesize(ctx, api.SynthesizeRequest{Text: "README 4-6", Raw: ptr(true)})
	if err != nil || a.Spoken != "README 4-6" {
		t.Fatalf("raw synthesize = %q, %v", a.Spoken, err)
	}
	p, err := user.PrepareText(ctx, api.PrepareTextRequest{Text: "readme"})
	if err != nil || p.Spoken != "read me" {
		t.Fatalf("prepare = %+v, %v", p, err)
	}
	if _, err := magi.PutLexiconEntry(ctx, api.PutLexiconEntryRequest{Written: "Ada", HeardAs: []string{"heard audio/wav"}}); err != nil {
		t.Fatal(err)
	}
	tr, err := user.Transcribe(ctx, api.TranscribeRequest{Audio: []byte("abc"), MediaType: "audio/wav"})
	if err != nil || tr.Text != "Ada" || tr.Heard == nil || *tr.Heard != "heard audio/wav" {
		t.Fatalf("transcribe = %+v, %v", tr, err)
	}

	if _, err := user.DeleteLexiconEntry(ctx, api.DeleteLexiconEntryRequest{Written: "Ada"}); rpc.CodeOf(err) != http.StatusForbidden {
		t.Fatalf("user delete = %v, want denied", err)
	}
	d, err := magi.DeleteLexiconEntry(ctx, api.DeleteLexiconEntryRequest{Written: "ADA"})
	if err != nil || !d.Deleted {
		t.Fatalf("delete = %+v, %v", d, err)
	}
	if d, _ := magi.DeleteLexiconEntry(ctx, api.DeleteLexiconEntryRequest{Written: "ADA"}); d.Deleted {
		t.Fatal("second delete deleted")
	}
}
