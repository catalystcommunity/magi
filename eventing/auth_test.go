package eventing

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	w "github.com/catalystcommunity/magi/generated/events/go"
	p "github.com/catalystcommunity/magi/protocol"
	"testing"
	"time"
)

var producer = w.ApplicationRef{Identity: w.IdentityRef{Domain: "primary.opal.test", AccountId: "producer"}, ApplicationId: "calendar", InstanceId: "one"}
var consumer = w.ApplicationRef{Identity: w.IdentityRef{Domain: "secondary.opal.test", AccountId: "consumer"}, ApplicationId: "report", InstanceId: "one"}
var broker = w.ApplicationRef{Identity: w.IdentityRef{Domain: "primary.opal.test", AccountId: "events"}, ApplicationId: "events", InstanceId: "stable"}

type testKeys struct{ key ed25519.PublicKey }

func (k testKeys) Resolve(_ context.Context, a w.ApplicationRef, id string) (VerifiedKey, error) {
	if a != producer && a != consumer || id != "key" {
		return VerifiedKey{}, ErrDenied
	}
	return VerifiedKey{Public: k.key, CreatedAt: time.Now().Add(-time.Hour), ExpiresAt: time.Now().Add(time.Hour)}, nil
}
func must(t testing.TB, e error) {
	t.Helper()
	if e != nil {
		t.Fatal(e)
	}
}
func sample(id string) []byte {
	return w.EncodeEvent(w.Event{Ref: w.RecordRef{Authority: producer.Identity, Kind: "event", RecordId: id}, Producer: producer.Identity, Recipients: []w.IdentityRef{consumer.Identity}, EventType: "calendar.changed", OccurredAt: now(), RecordedAt: now(), Payload: w.TypedPayload{Schema: w.SchemaRef{Name: "synthetic-calendar", Version: "1", Digest: make([]byte, 32)}, Data: []byte("synthetic")}, Causes: []w.RecordRef{}, Restrictions: w.Restrictions{Requirements: []w.SourceRestriction{}}})
}
func TestAuthenticationBindings(t *testing.T) {
	pub, private, e := ed25519.GenerateKey(rand.Reader)
	must(t, e)
	keys := testKeys{pub}
	raw := sample("one")
	packet, e := Sign("publish", raw, producer, broker, "key", private, now())
	must(t, e)
	v, e := authenticate(context.Background(), keys, broker, "publish", packet)
	must(t, e)
	if string(v.Body) != string(raw) {
		t.Fatal("body changed")
	}
	for _, name := range []string{"signature", "recipient", "operation", "schema", "stale", "future", "identity", "key", "unknown-field"} {
		t.Run(name, func(t *testing.T) {
			v, e := w.DecodeSignatureInput(packet.Input)
			must(t, e)
			q := packet
			switch name {
			case "signature":
				q.Signature = make([]byte, 64)
			case "recipient":
				v.Recipient = consumer
			case "operation":
				v.Context = "catalyst.events/pull"
			case "schema":
				v.Schema.Digest = make([]byte, 32)
			case "stale":
				v.SignedAt = now().Add(-2 * time.Minute)
			case "future":
				v.SignedAt = now().Add(time.Minute)
			case "identity":
				v.Signer.Identity.AccountId = "other"
			case "key":
				v.KeyId = "other"
			case "unknown-field":
				q.Input = append(append([]byte{}, packet.Input...), 0)
				q.Signature = ed25519.Sign(private, q.Input)
			}
			if name != "signature" && name != "unknown-field" {
				q.Input = w.EncodeSignatureInput(v)
				q.Signature = ed25519.Sign(private, q.Input)
			}
			if _, e := authenticate(context.Background(), keys, broker, "publish", q); e == nil {
				t.Fatal("accepted invalid proof")
			}
		})
	}
	if _, e := authenticate(context.Background(), nil, broker, "publish", packet); e == nil {
		t.Fatal("nil resolver accepted")
	}
}
func TestEventLimitsAndNoSession(t *testing.T) {
	v, e := event(sample("one"))
	must(t, e)
	if v.SessionId != nil {
		t.Fatal("requires session")
	}
	for _, change := range []func(*w.Event){func(e *w.Event) { e.Recipients = nil }, func(e *w.Event) { e.Ref.Authority = consumer.Identity }, func(e *w.Event) { e.Payload.Data = make([]byte, MaxEventBytes) }, func(e *w.Event) { e.Payload.Schema.Digest = nil }} {
		bad := v
		change(&bad)
		if _, e := event(w.EncodeEvent(bad)); e == nil {
			t.Fatal("invalid event accepted")
		}
	}
	if _, e := p.NewRPCHandler("http://public.example", nil, nil); e == nil {
		t.Fatal("unsafe handler")
	}
}

type resolverFunc func(context.Context, w.ApplicationRef, string) (VerifiedKey, error)

func (f resolverFunc) Resolve(ctx context.Context, a w.ApplicationRef, id string) (VerifiedKey, error) {
	return f(ctx, a, id)
}
func TestAuthenticationUnavailableIsRetryable(t *testing.T) {
	_, private, e := ed25519.GenerateKey(rand.Reader)
	must(t, e)
	packet, e := Sign("publish", sample("unavailable"), producer, broker, "key", private, now())
	must(t, e)
	keys := resolverFunc(func(context.Context, w.ApplicationRef, string) (VerifiedKey, error) {
		return VerifiedKey{}, ErrUnavailable
	})
	_, e = authenticate(context.Background(), keys, broker, "publish", packet)
	if !errors.Is(e, ErrUnavailable) {
		t.Fatal("transient authentication failure became permanent", e)
	}
	response := failure(e)
	detail, e := w.DecodeServiceError(response.Payload)
	must(t, e)
	if detail.Code != 1500 || !detail.Retryable {
		t.Fatal("unavailable identity is not retryable")
	}
}
func TestKeyExpiresDuringResolution(t *testing.T) {
	public, private, e := ed25519.GenerateKey(rand.Reader)
	must(t, e)
	packet, e := Sign("publish", sample("expires"), producer, broker, "key", private, now())
	must(t, e)
	keys := resolverFunc(func(context.Context, w.ApplicationRef, string) (VerifiedKey, error) {
		expires := time.Now().Add(time.Millisecond)
		time.Sleep(5 * time.Millisecond)
		return VerifiedKey{Public: public, CreatedAt: time.Now().Add(-time.Hour), ExpiresAt: expires}, nil
	})
	if _, e := authenticate(context.Background(), keys, broker, "publish", packet); !errors.Is(e, ErrDenied) {
		t.Fatal("expired key accepted after lookup", e)
	}
}
