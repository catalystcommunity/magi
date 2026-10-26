//go:build integration

package conversation

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	w "github.com/catalystcommunity/magi/generated/go"
	"github.com/catalystcommunity/magi/identity"
	p "github.com/catalystcommunity/magi/protocol"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func must(t testing.TB, e error) {
	t.Helper()
	if e != nil {
		t.Fatal(e)
	}
}
func docker(t testing.TB, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	out, e := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if e != nil {
		t.Fatalf("docker %s: %v: %s", args[0], e, out)
	}
	return strings.TrimSpace(string(out))
}

type fixtureKeys struct{ public ed25519.PublicKey }

func (k fixtureKeys) Resolve(_ context.Context, a w.ApplicationRef, id string) (identity.VerifiedKey, error) {
	if a != actor || id != "test-key" {
		return identity.VerifiedKey{}, identity.ErrDenied
	}
	return identity.VerifiedKey{Public: k.public, CreatedAt: time.Now().Add(-time.Hour), ExpiresAt: time.Now().Add(time.Hour)}, nil
}
func TestConversationPersistence(t *testing.T) {
	ctx := context.Background()
	var random [8]byte
	_, e := rand.Read(random[:])
	must(t, e)
	id := docker(t, "run", "--pull=never", "-d", "--name", "opal-graph-test-"+hex.EncodeToString(random[:]), "--label", "opal.fixture=conversation", "--memory", "512m", "--cpus", "2", "-p", "127.0.0.1::5432", "-e", "POSTGRES_PASSWORD=synthetic-only", "postgres@sha256:0b657ff48d7f76a1e907f381b1693eb4f2bf54c1d2df4feb6743d7dc601768dd")
	if len(id) != 64 {
		t.Fatal("invalid container ID")
	}
	t.Cleanup(func() {
		if docker(t, "inspect", "-f", `{{index .Config.Labels "opal.fixture"}}`, id) != "conversation" {
			t.Error("ownership changed")
			return
		}
		docker(t, "rm", "-f", "-v", id)
	})
	dsn := "postgres://postgres:synthetic-only@" + docker(t, "port", id, "5432/tcp") + "/postgres?sslmode=disable"
	pool, e := pgxpool.New(ctx, dsn)
	must(t, e)
	defer pool.Close()
	until := time.Now().Add(30 * time.Second)
	for pool.Ping(ctx) != nil {
		if time.Now().After(until) {
			t.Fatal("database readiness")
		}
		time.Sleep(50 * time.Millisecond)
	}
	receiver := actor
	receiver.ApplicationId = "conversations"
	store := &Store{Pool: pool, Application: receiver}
	must(t, store.Initialize(ctx))
	for _, action := range []string{"read", "write"} {
		must(t, store.SetGrant(ctx, Grant{Actor: actor, Owner: owner, Action: action, Allowed: true}))
	}
	pub, key, e := ed25519.GenerateKey(rand.Reader)
	must(t, e)
	service := &Service{Store: store, Keys: fixtureKeys{pub}}
	server := httptest.NewUnstartedServer(nil)
	handler, e := p.NewRPCHandler("http://"+server.Listener.Addr().String(), service.Dispatch, store.Check)
	must(t, e)
	server.Config.Handler = handler
	server.Start()
	defer server.Close()
	client, e := NewClient(Profile{Endpoint: server.URL + "/csil/v1/rpc", Actor: actor, Recipient: receiver, Owner: owner, KeyID: "test-key", PrivateKey: key})
	must(t, e)
	source := interaction("source")
	entry := w.ConversationAppend{Node: source, Sessions: []string{"calendar", "stars"}}
	receipt, e := client.Append(ctx, entry)
	must(t, e)
	again, e := client.Append(ctx, entry)
	must(t, e)
	if receipt.Sequence != again.Sequence {
		t.Fatal("duplicate inserted")
	}
	changed := source
	changed.Content = literal("Changed statement")
	if _, e = client.Append(ctx, w.ConversationAppend{Node: changed, Sessions: entry.Sessions}); e == nil {
		t.Fatal("rewrote source")
	}
	interpretation := w.InterpretationRecord{Header: header("interpretation"), Sources: []w.RecordRef{source.Header.Ref}, Context: []w.RecordRef{}, Destinations: []w.DestinationMeaning{{SessionId: "calendar", Prompts: []w.RecordRef{}, Content: literal("Move it to tomorrow.")}, {SessionId: "stars", Prompts: []w.RecordRef{}, Content: literal("What is the mass limit?")}}, Method: "explicit fixture split", Uncertainty: "test"}
	_, e = client.Append(ctx, w.ConversationAppend{Node: interpretation, Sessions: entry.Sessions})
	must(t, e)
	for i, session := range entry.Sessions {
		input := w.SessionInput{Header: header("input-" + session), SessionId: session, Sources: []w.RecordRef{source.Header.Ref}, Interpretations: []w.RecordRef{interpretation.Header.Ref}, Content: interpretation.Destinations[i].Content, CommandId: "command-" + session, Admission: w.RecordRef{Authority: actor.Identity, Kind: "admission", RecordId: session}}
		_, e = client.Append(ctx, w.ConversationAppend{Node: input, Sessions: []string{session}})
		must(t, e)
	}
	for _, session := range entry.Sessions {
		page, e := client.History(ctx, session, 0)
		must(t, e)
		if len(page.Records) != 3 {
			t.Fatal("lost or mixed records", len(page.Records))
		}
		last := page.Records[2].Entry.Node.(w.SessionInput)
		if last.SessionId != session {
			t.Fatal("crossed input contexts")
		}
		next, e := client.History(ctx, session, page.NextSequence)
		must(t, e)
		if len(next.Records) != 0 {
			t.Fatal("cursor replay")
		}
		previous, e := client.Before(ctx, session, page.NextSequence)
		must(t, e)
		if len(previous.Records) != 2 || previous.Records[0].Receipt.Sequence >= page.NextSequence || previous.Records[1].Receipt.Sequence >= previous.Records[0].Receipt.Sequence {
			t.Fatal("reverse boundary or ordering")
		}
	}
	otherOwner := owner
	otherOwner.ScopeId = "other"
	for _, action := range []string{"read", "write"} {
		must(t, store.SetGrant(ctx, Grant{Actor: actor, Owner: otherOwner, Action: action, Allowed: true}))
	}
	hidden := interaction("hidden")
	hidden.Header.Access.ControlScope = otherOwner
	hidden.Header.Access.ResultAudience = []w.ScopeRef{otherOwner}
	hidden.Header.Causes = []w.RecordRef{source.Header.Ref}
	if _, e = client.Append(ctx, w.ConversationAppend{Node: hidden, Sessions: []string{"other"}}); e == nil {
		t.Fatal("cross-scope edge accepted")
	}
	replacement := &Store{Pool: pool, Application: receiver}
	must(t, replacement.Check(ctx))
	stored, e := replacement.Read(ctx, actor, w.ConversationRead{Owner: owner, Ref: source.Header.Ref})
	must(t, e)
	if stored.Receipt.Sequence != receipt.Sequence {
		t.Fatal("record not durable")
	}
	must(t, store.SetGrant(ctx, Grant{Actor: actor, Owner: owner, Action: "read", Allowed: false}))
	if _, e = replacement.Read(ctx, actor, w.ConversationRead{Owner: owner, Ref: source.Header.Ref}); !errors.Is(e, ErrDenied) {
		t.Fatal(e)
	}
}
