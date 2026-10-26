package eventing

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"time"

	lk "github.com/catalystcommunity/linkkeys/sdks/regular-rp/go/generated"
	schema "github.com/catalystcommunity/magi/generated/events"
	w "github.com/catalystcommunity/magi/generated/events/go"
	all "github.com/catalystcommunity/magi/generated/go"
	authidentity "github.com/catalystcommunity/magi/identity"
	p "github.com/catalystcommunity/magi/protocol"
)

func Schema() w.SchemaRef {
	d, _ := hex.DecodeString(schema.Digest)
	return w.SchemaRef{Name: "catalyst.events", Version: "0alpha1", Digest: d}
}

type VerifiedKey = authidentity.VerifiedKey
type DomainTrust = authidentity.DomainTrust
type KeyResolver interface {
	Resolve(context.Context, w.ApplicationRef, string) (VerifiedKey, error)
}
type LinkKeysResolver struct {
	RP      *lk.RpClient
	Domains []DomainTrust
}

func (r *LinkKeysResolver) Resolve(ctx context.Context, a w.ApplicationRef, keyID string) (VerifiedKey, error) {
	canonical, e := all.DecodeApplicationRef(w.EncodeApplicationRef(a))
	if e != nil {
		return VerifiedKey{}, ErrDenied
	}
	result, e := (&authidentity.LinkKeysResolver{RP: r.RP, Domains: r.Domains}).Resolve(ctx, canonical, keyID)
	if errors.Is(e, authidentity.ErrDenied) {
		e = ErrDenied
	}
	if errors.Is(e, authidentity.ErrUnavailable) {
		e = ErrUnavailable
	}
	return result, e
}

func Sign(op string, body []byte, signer, recipient w.ApplicationRef, keyID string, private ed25519.PrivateKey, at time.Time) (w.SignedRecord, error) {
	if !validApplication(signer) || !validApplication(recipient) || len(private) != ed25519.PrivateKeySize || !namePattern.MatchString(keyID) {
		return w.SignedRecord{}, ErrInvalid
	}
	raw := w.EncodeSignatureInput(w.SignatureInput{Context: "catalyst.events/" + op, Schema: Schema(), Signer: signer, Recipient: recipient, KeyId: keyID, SignedAt: at, Body: body})
	if _, e := p.Decode(raw, w.DecodeSignatureInput, w.EncodeSignatureInput); e != nil {
		return w.SignedRecord{}, e
	}
	return w.SignedRecord{Input: raw, Signature: ed25519.Sign(private, raw)}, nil
}
func authenticate(ctx context.Context, resolver KeyResolver, recipient w.ApplicationRef, op string, packet w.SignedRecord) (w.SignatureInput, error) {
	raw := bytes.Clone(packet.Input)
	v, e := p.Decode(raw, w.DecodeSignatureInput, w.EncodeSignatureInput)
	at := time.Now()
	schema := Schema()
	if e != nil || resolver == nil || v.Context != "catalyst.events/"+op || v.Schema.Name != schema.Name || v.Schema.Version != schema.Version || !bytes.Equal(v.Schema.Digest, schema.Digest) || v.Recipient != recipient || !validApplication(v.Signer) || v.SignedAt.After(at.Add(5*time.Second)) || at.Sub(v.SignedAt) > time.Minute || len(packet.Signature) != 64 {
		return v, ErrDenied
	}
	k, e := resolver.Resolve(ctx, v.Signer, v.KeyId)
	if e != nil {
		return v, e
	}
	at = time.Now()
	if len(k.Public) != 32 || k.CreatedAt.After(at) || v.SignedAt.Before(k.CreatedAt) || !v.SignedAt.Before(k.ExpiresAt) || !at.Before(k.ExpiresAt) || at.Sub(v.SignedAt) > time.Minute || !ed25519.Verify(k.Public, raw, packet.Signature) {
		return v, ErrDenied
	}
	// Retrying an authenticated packet is allowed. All mutations have a durable
	// identity-bound key. Fresh signatures can carry an old retained event.
	return v, nil
}
