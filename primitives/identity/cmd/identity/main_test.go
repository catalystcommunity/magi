package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/catalystcommunity/magi/lib/authn"
)

func TestKeygenAndMint(t *testing.T) {
	dir := t.TempDir()
	key, pub, tok := filepath.Join(dir, "key.json"), filepath.Join(dir, "key.pub.json"), filepath.Join(dir, "token.txt")
	if err := keygen([]string{"--issuer", "magi", "--out", key, "--public", pub}); err != nil {
		t.Fatal(err)
	}
	if err := keygen([]string{"--out", key}); err == nil {
		t.Fatal("keygen overwrote an existing key")
	}
	if err := mint([]string{"--key", key, "--subject", "service:x", "--scopes", "a, b", "--ttl", "720h", "--out", tok}); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{key, tok} {
		if st, _ := os.Stat(f); st.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode %v", f, st.Mode())
		}
	}
	if b, _ := os.ReadFile(pub); strings.Contains(string(b), "private_key") {
		t.Fatal("public key file has the private key")
	}
	v, err := authn.LoadVerifier(pub)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(tok)
	p, err := v.Verify(strings.TrimSpace(string(b)))
	if err != nil || p.Subject != "service:x" || p.Kind != authn.KindService || len(p.Scopes) != 2 || p.Expires-p.IssuedAt != 720*3600 {
		t.Fatalf("token = %+v, %v", p, err)
	}
	if err := mint([]string{"--key", key, "--subject", "x", "--out", filepath.Join(dir, "t2")}); err == nil {
		t.Fatal("mint accepted a subject without a kind")
	}
}
