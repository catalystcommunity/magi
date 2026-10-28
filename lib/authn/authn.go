// Package authn issues and verifies Magi bearer tokens.
//
// A token is "magi1.<payload>.<signature>". The payload is base64url JSON.
// The signature is Ed25519 over "magi1.<payload>". Authentication only
// establishes who the caller is. Each service decides what the caller can do.
package authn

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

const prefix = "magi1."

var (
	ErrInvalid = errors.New("invalid token")
	ErrExpired = errors.New("token expired")
)

// Kinds of principal.
const (
	KindUser    = "user"
	KindService = "service"
)

// Principal is an authenticated caller.
type Principal struct {
	Subject string `json:"sub"`
	// Name is a display name for a user. It is not an identifier.
	Name     string   `json:"name,omitempty"`
	Kind     string   `json:"kind"`
	Scopes   []string `json:"scopes,omitempty"`
	Issuer   string   `json:"iss"`
	KeyID    string   `json:"kid"`
	IssuedAt int64    `json:"iat"`
	Expires  int64    `json:"exp"`
	// Token is the raw token. It is not part of the signed payload.
	Token string `json:"-"`
}

// Has reports whether the principal holds scope. "*" holds every scope.
// "name:*" holds every scope that starts with "name:".
func (p Principal) Has(scope string) bool {
	for _, s := range p.Scopes {
		if s == "*" || s == scope {
			return true
		}
		if strings.HasSuffix(s, ":*") && strings.HasPrefix(scope, strings.TrimSuffix(s, "*")) {
			return true
		}
	}
	return false
}

// Issuer signs tokens.
type Issuer struct {
	Name  string
	KeyID string
	Key   ed25519.PrivateKey
	Now   func() time.Time
}

// Issue signs a token for p that lasts ttl.
func (i *Issuer) Issue(p Principal, ttl time.Duration) (string, error) {
	if p.Subject == "" || (p.Kind != KindUser && p.Kind != KindService) {
		return "", fmt.Errorf("issue: subject and kind are required")
	}
	now := time.Now
	if i.Now != nil {
		now = i.Now
	}
	t := now()
	p.Issuer, p.KeyID, p.IssuedAt, p.Expires = i.Name, i.KeyID, t.Unix(), t.Add(ttl).Unix()
	body, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	signed := prefix + base64.RawURLEncoding.EncodeToString(body)
	sig := ed25519.Sign(i.Key, []byte(signed))
	return signed + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// Verifier checks tokens against known issuer keys.
type Verifier struct {
	Issuer string
	Keys   map[string]ed25519.PublicKey
	Now    func() time.Time
}

// Verify returns the principal of a valid, unexpired token.
func (v *Verifier) Verify(token string) (Principal, error) {
	var p Principal
	if !strings.HasPrefix(token, prefix) || len(token) > 8192 {
		return p, ErrInvalid
	}
	dot := strings.LastIndexByte(token, '.')
	if dot <= len(prefix) {
		return p, ErrInvalid
	}
	signed, sigText := token[:dot], token[dot+1:]
	sig, err := base64.RawURLEncoding.DecodeString(sigText)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return p, ErrInvalid
	}
	body, err := base64.RawURLEncoding.DecodeString(signed[len(prefix):])
	if err != nil {
		return p, ErrInvalid
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return Principal{}, ErrInvalid
	}
	key, ok := v.Keys[p.KeyID]
	if !ok || p.Issuer != v.Issuer || !ed25519.Verify(key, []byte(signed), sig) {
		return Principal{}, ErrInvalid
	}
	now := time.Now
	if v.Now != nil {
		now = v.Now
	}
	if now().Unix() >= p.Expires {
		return Principal{}, ErrExpired
	}
	p.Token = token
	return p, nil
}

type ctxKey struct{}

// With returns a context that carries p.
func With(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// From returns the principal in ctx.
func From(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(ctxKey{}).(Principal)
	return p, ok
}

// KeyFile is the on-disk form of an issuer key.
type KeyFile struct {
	Issuer     string `json:"issuer"`
	KeyID      string `json:"kid"`
	PrivateKey string `json:"private_key,omitempty"`
	PublicKey  string `json:"public_key"`
}

// GenerateKeyFile creates a new issuer key.
func GenerateKeyFile(issuer string) (KeyFile, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return KeyFile{}, err
	}
	kid := make([]byte, 8)
	if _, err := rand.Read(kid); err != nil {
		return KeyFile{}, err
	}
	return KeyFile{
		Issuer:     issuer,
		KeyID:      base64.RawURLEncoding.EncodeToString(kid),
		PrivateKey: base64.StdEncoding.EncodeToString(priv),
		PublicKey:  base64.StdEncoding.EncodeToString(pub),
	}, nil
}

func readKeyFile(path string) (KeyFile, error) {
	var k KeyFile
	b, err := os.ReadFile(path)
	if err != nil {
		return k, err
	}
	return k, json.Unmarshal(b, &k)
}

// LoadIssuer reads a key file that contains a private key.
func LoadIssuer(path string) (*Issuer, error) {
	k, err := readKeyFile(path)
	if err != nil {
		return nil, err
	}
	priv, err := base64.StdEncoding.DecodeString(k.PrivateKey)
	if err != nil || len(priv) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("issuer key %s: bad private key", path)
	}
	return &Issuer{Name: k.Issuer, KeyID: k.KeyID, Key: ed25519.PrivateKey(priv)}, nil
}

// LoadVerifier reads one or more key files. Only public keys are used.
func LoadVerifier(paths ...string) (*Verifier, error) {
	v := &Verifier{Keys: map[string]ed25519.PublicKey{}}
	for _, path := range paths {
		k, err := readKeyFile(path)
		if err != nil {
			return nil, err
		}
		pub, err := base64.StdEncoding.DecodeString(k.PublicKey)
		if err != nil || len(pub) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("verifier key %s: bad public key", path)
		}
		if v.Issuer != "" && v.Issuer != k.Issuer {
			return nil, fmt.Errorf("verifier key %s: issuer mismatch", path)
		}
		v.Issuer = k.Issuer
		v.Keys[k.KeyID] = ed25519.PublicKey(pub)
	}
	return v, nil
}
