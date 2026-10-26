package protocol

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"reflect"
	"time"

	wire "github.com/catalystcommunity/magi/generated/go"
)

var ErrSignature = errors.New("signature verification failed")

// SignatureScope is selected by the receiver, not copied from an incoming packet.
type SignatureScope struct {
	Context   string
	Schema    wire.SchemaRef
	Signer    wire.ApplicationRef
	Recipient wire.ApplicationRef
}

func Sign(scope SignatureScope, body []byte, keyID string, at time.Time, key ed25519.PrivateKey) (wire.SignedRecord, error) {
	input := wire.SignatureInput{Context: scope.Context, Schema: scope.Schema, Signer: scope.Signer, Recipient: scope.Recipient, KeyId: keyID, SignedAt: at, Body: body}
	raw := wire.EncodeSignatureInput(input)
	if len(key) != ed25519.PrivateKeySize || len(scope.Schema.Digest) != 32 || keyID == "" {
		return wire.SignedRecord{}, ErrSignature
	}
	if _, err := Decode(raw, wire.DecodeSignatureInput, wire.EncodeSignatureInput); err != nil {
		return wire.SignedRecord{}, err
	}
	return wire.SignedRecord{Input: raw, Signature: ed25519.Sign(key, raw)}, nil
}

// Verify checks proof and binding only. The caller must resolve a trusted key,
// check its status, validate the body, and apply freshness and resource policy.
// It returns a copy of the verified bytes for retention. Do not re-encode them.
func Verify(packet wire.SignedRecord, key ed25519.PublicKey, expected SignatureScope) (wire.SignatureInput, []byte, error) {
	var zero wire.SignatureInput
	if len(key) != ed25519.PublicKeySize || len(packet.Signature) != ed25519.SignatureSize {
		return zero, nil, ErrSignature
	}
	// Copy first: verification and decoding must consume the same bytes.
	raw := bytes.Clone(packet.Input)
	input, err := Decode(raw, wire.DecodeSignatureInput, wire.EncodeSignatureInput)
	if err != nil || len(input.Schema.Digest) != 32 || input.KeyId == "" || input.Context != expected.Context || !reflect.DeepEqual(input.Schema, expected.Schema) || !reflect.DeepEqual(input.Signer, expected.Signer) || !reflect.DeepEqual(input.Recipient, expected.Recipient) || !ed25519.Verify(key, raw, packet.Signature) {
		return zero, nil, ErrSignature
	}
	return input, raw, nil
}
