package authn

import (
	"crypto/ed25519"
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func TestIssueVerify(t *testing.T) {
	kf, err := GenerateKeyFile("magi-test")
	if err != nil {
		t.Fatal(err)
	}
	priv, _ := base64.StdEncoding.DecodeString(kf.PrivateKey)
	pub, _ := base64.StdEncoding.DecodeString(kf.PublicKey)
	now := time.Unix(1000, 0)
	iss := &Issuer{Name: kf.Issuer, KeyID: kf.KeyID, Key: priv, Now: func() time.Time { return now }}
	v := &Verifier{Issuer: kf.Issuer, Keys: map[string]ed25519.PublicKey{kf.KeyID: pub}, Now: func() time.Time { return now }}
	tok, err := iss.Issue(Principal{Subject: "service:agent", Kind: KindService, Scopes: []string{"workflow:*"}}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	p, err := v.Verify(tok)
	if err != nil || p.Subject != "service:agent" || !p.Has("workflow:decide") || p.Has("artifacts:read") {
		t.Fatalf("verify: %+v %v", p, err)
	}
	// Change one character in the middle of the signature. (A change of
	// the last characters can keep the same bytes.)
	i := strings.LastIndexByte(tok, '.') + 20
	c := byte('A')
	if tok[i] == 'A' {
		c = 'B'
	}
	if _, err := v.Verify(tok[:i] + string(c) + tok[i+1:]); err == nil {
		t.Fatal("tampered token accepted")
	}
	// Other spellings of the same signature (the unused low bits of the
	// last character) are refused.
	last := tok[len(tok)-1]
	for _, alt := range "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_" {
		if byte(alt) == last {
			continue
		}
		if _, err := v.Verify(tok[:len(tok)-1] + string(alt)); err == nil {
			t.Fatalf("token with last character %q accepted", alt)
		}
	}
	v.Now = func() time.Time { return now.Add(2 * time.Minute) }
	if _, err := v.Verify(tok); err != ErrExpired {
		t.Fatalf("expiry: %v", err)
	}
}
