package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func newKey(t *testing.T) (dir, key, pub string) {
	t.Helper()
	dir = t.TempDir()
	key, pub = filepath.Join(dir, "key.json"), filepath.Join(dir, "key.pub.json")
	if err := keygen([]string{"--issuer", "magi", "--out", key, "--public", pub}); err != nil {
		t.Fatal(err)
	}
	return
}

func TestMintLimits(t *testing.T) {
	dir, key, _ := newKey(t)
	n := 0
	try := func(args ...string) error {
		n++
		return mint(append([]string{"--key", key, "--out", filepath.Join(dir, "t"+string(rune('a'+n)))}, args...))
	}
	for name, args := range map[string][]string{
		"over the cap":          {"--subject", "service:x", "--ttl", "8761h"},
		"negative":              {"--subject", "service:x", "--ttl", "-1h"},
		"wildcard":              {"--subject", "service:x", "--scopes", "*", "--ttl", "1h"},
		"operator over its cap": {"--subject", "service:x", "--scopes", "magi:operate", "--ttl", "2200h"},
		"fleet over its cap":    {"--role", "fleet", "--name", "ops", "--ttl", "2200h"},
		"role and subject":      {"--role", "runner", "--name", "a", "--subject", "service:x"},
		"role without name":     {"--role", "runner"},
		"bad role name":         {"--role", "runner", "--name", "a:b"},
	} {
		if err := try(args...); err == nil {
			t.Errorf("%s: mint accepted %v", name, args)
		}
	}
	if err := try("--subject", "service:x", "--ttl", "8760h"); err != nil {
		t.Errorf("one year: %v", err)
	}
	if err := try("--subject", "service:x", "--scopes", "magi:operate", "--ttl", "2160h"); err != nil {
		t.Errorf("operator at its cap: %v", err)
	}
}

func TestMintRoles(t *testing.T) {
	dir, key, pub := newKey(t)
	v, _ := authn.LoadVerifier(pub)
	read := func(role, name string) authn.Principal {
		t.Helper()
		out := filepath.Join(dir, role+"-"+name)
		if err := mint([]string{"--key", key, "--role", role, "--name", name, "--out", out}); err != nil {
			t.Fatal(err)
		}
		b, _ := os.ReadFile(out)
		p, err := v.Verify(strings.TrimSpace(string(b)))
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	r := read("runner", "laptop")
	if r.Subject != "runner:laptop" || r.Kind != authn.KindService || !r.Has("magi:runner") || r.Has("magi:operate") || r.Expires-r.IssuedAt != int64(DefaultTTL.Seconds()) {
		t.Fatalf("runner token = %+v", r)
	}
	f := read("fleet", "ops")
	if f.Subject != "service:fleet-ops" || !f.Has("magi:operate") || f.Has("magi:runner") || f.Expires-f.IssuedAt != int64(DefaultFleetTTL.Seconds()) {
		t.Fatalf("fleet token = %+v", f)
	}
}

// Revoking a runner: subject refuses its current token at once (after the
// reload interval) and a token minted afterwards works.
func TestRevokeRunnerAndFleet(t *testing.T) {
	dir, key, pub := newKey(t)
	deny := filepath.Join(dir, "deny.json")
	t.Setenv(authn.DenyListEnv, deny)
	v, err := authn.LoadVerifier(pub)
	if err != nil {
		t.Fatal(err)
	}
	tokens := map[string]string{}
	for _, c := range [][2]string{{"runner", "a"}, {"runner", "b"}, {"fleet", "ops"}} {
		out := filepath.Join(dir, c[0]+c[1])
		if err := mint([]string{"--key", key, "--role", c[0], "--name", c[1], "--out", out}); err != nil {
			t.Fatal(err)
		}
		b, _ := os.ReadFile(out)
		tokens[c[0]+c[1]] = strings.TrimSpace(string(b))
	}
	clock := time.Now()
	v.Deny.Now = func() time.Time { return clock }
	if err := revoke([]string{"--deny-list", deny, "--subject", "runner:a"}); err != nil {
		t.Fatal(err)
	}
	if err := revoke([]string{"--deny-list", deny, "--subject", "service:fleet-ops"}); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(authn.DefaultDenyReload + time.Second)
	if _, err := v.Verify(tokens["runnera"]); err != authn.ErrRevoked {
		t.Errorf("revoked runner: %v", err)
	}
	if _, err := v.Verify(tokens["fleetops"]); err != authn.ErrRevoked {
		t.Errorf("revoked fleet token: %v", err)
	}
	if _, err := v.Verify(tokens["runnerb"]); err != nil {
		t.Errorf("other runner: %v", err)
	}
	if err := revoke([]string{"--deny-list", deny}); err == nil {
		t.Error("revoke without a target")
	}
}
