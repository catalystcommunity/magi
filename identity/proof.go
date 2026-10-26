package identity

import (
	"bytes"
	"context"
	w "github.com/catalystcommunity/magi/generated/go"
	p "github.com/catalystcommunity/magi/protocol"
	"time"
)

// Authenticate verifies application possession and current authentication evidence.
// The receiving service must separately authorize the decoded operation.
func Authenticate(ctx context.Context, resolver KeyResolver, recipient w.ApplicationRef, contextName string, schema w.SchemaRef, packet w.SignedRecord) (w.SignatureInput, error) {
	packet.Input = bytes.Clone(packet.Input)
	packet.Signature = bytes.Clone(packet.Signature)
	v, e := p.Decode(packet.Input, w.DecodeSignatureInput, w.EncodeSignatureInput)
	now := time.Now()
	if e != nil || resolver == nil || !validApplication(recipient) || !validApplication(v.Signer) || v.Recipient != recipient || v.Context != contextName || v.Schema.Name != schema.Name || v.Schema.Version != schema.Version || !bytes.Equal(v.Schema.Digest, schema.Digest) || len(packet.Signature) != 64 || v.SignedAt.After(now.Add(5*time.Second)) || now.Sub(v.SignedAt) > time.Minute {
		return v, ErrDenied
	}
	key, e := resolver.Resolve(ctx, v.Signer, v.KeyId)
	if e != nil {
		return v, e
	}
	now = time.Now()
	if key.CreatedAt.After(now) || v.SignedAt.Before(key.CreatedAt) || !v.SignedAt.Before(key.ExpiresAt) || !now.Before(key.ExpiresAt) || now.Sub(v.SignedAt) > time.Minute {
		return v, ErrDenied
	}
	v, _, e = p.Verify(packet, key.Public, p.SignatureScope{Context: contextName, Schema: schema, Signer: v.Signer, Recipient: recipient})
	if e != nil {
		return v, ErrDenied
	}
	return v, nil
}
