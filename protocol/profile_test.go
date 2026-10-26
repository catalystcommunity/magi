package protocol

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	wire "github.com/catalystcommunity/magi/generated/go"
	"testing"
	"time"
)

func TestHostileFrames(t *testing.T) {
	for _, h := range []string{"", "9bffffffffffffffff", "bf616101ff", "a2616101616102", "a16161", "a000", "1817", "7bffffffffffffffff", "fb7ff0000000000000", "61ff", "c06178", "1b0020000000000000", "3b001fffffffffffff"} {
		t.Run(h, func(t *testing.T) {
			b, _ := hex.DecodeString(h)
			if CheckFrame(b) == nil {
				t.Fatal("accepted hostile frame")
			}
		})
	}
	b := []byte{0}
	for range MaxDepth + 1 {
		b = append([]byte{0x81}, b...)
	}
	if CheckFrame(b) == nil {
		t.Fatal("accepted depth")
	}
	if CheckFrame(make([]byte, MaxFrame+1)) == nil {
		t.Fatal("accepted frame size")
	}
}
func TestUnknownFieldsAndPreciseTime(t *testing.T) {
	b, _ := hex.DecodeString("a266646f6d61696e61646a6163636f756e745f69646161")
	if _, e := Decode(b, wire.DecodeIdentityRef, wire.EncodeIdentityRef); e != nil {
		t.Fatal(e)
	}
	b[0] = 0xa3
	b = append(b, 0x61, 'x', 0xf5)
	if _, e := Decode(b, wire.DecodeIdentityRef, wire.EncodeIdentityRef); e == nil {
		t.Fatal("unknown field")
	}
	for _, s := range []string{"2026-01-02T03:04:05.123456789Z", "2026-02-31T00:00:00Z"} {
		b := append([]byte{0xc0, 0x78, byte(len(s))}, []byte(s)...)
		if len(s) < 24 {
			b = append([]byte{0xc0, 0x60 + byte(len(s))}, []byte(s)...)
		}
		if CheckFrame(b) == nil {
			t.Fatal("accepted lossy or invalid timestamp")
		}
	}
}
func TestSignatureBindings(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	app := wire.ApplicationRef{Identity: wire.IdentityRef{Domain: "catalystsquad.local", AccountId: "test"}, ApplicationId: "opal", InstanceId: "stable"}
	scope := SignatureScope{Context: "opal.command.0alpha1", Schema: wire.SchemaRef{Name: "Command", Version: "0alpha1", Digest: make([]byte, 32)}, Signer: app, Recipient: app}
	p, e := Sign(scope, []byte{0xa0}, "key-1", time.Date(2026, 10, 13, 0, 0, 0, 120000000, time.UTC), key)
	if e != nil {
		t.Fatal(e)
	}
	input, raw, e := Verify(p, pub, scope)
	if e != nil || !SameBytes(raw, p.Input) {
		t.Fatal("verification or preservation", e)
	}
	for _, field := range []string{"context", "recipient", "signer", "schema", "body", "key", "time"} {
		t.Run(field, func(t *testing.T) {
			v := input
			switch field {
			case "context":
				v.Context += "x"
			case "recipient":
				v.Recipient.InstanceId += "x"
			case "signer":
				v.Signer.InstanceId += "x"
			case "schema":
				v.Schema.Version += "x"
			case "body":
				v.Body = []byte{0xa1}
			case "key":
				v.KeyId += "x"
			case "time":
				v.SignedAt = v.SignedAt.Add(time.Second)
			}
			q := p
			q.Input = wire.EncodeSignatureInput(v)
			if _, _, e := Verify(q, pub, scope); e == nil {
				t.Fatal("accepted changed input")
			}
		})
	}
	scope.Recipient.InstanceId = "other"
	if _, _, e := Verify(p, pub, scope); e == nil {
		t.Fatal("recipient expectation ignored")
	}
}
