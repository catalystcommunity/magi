package authn

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
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
	tok, err := iss.Issue(Principal{Subject: "service:apprentice", Kind: KindService, Scopes: []string{"workflow:*"}}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	p, err := v.Verify(tok)
	if err != nil || p.Subject != "service:apprentice" || !p.Has("workflow:decide") || p.Has("artifacts:read") {
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

func denyEnv(t *testing.T) (iss *Issuer, v *Verifier, now *time.Time, path string) {
	t.Helper()
	kf, err := GenerateKeyFile("magi-test")
	if err != nil {
		t.Fatal(err)
	}
	priv, _ := base64.StdEncoding.DecodeString(kf.PrivateKey)
	pub, _ := base64.StdEncoding.DecodeString(kf.PublicKey)
	clock := time.Unix(10_000, 0)
	now = &clock
	get := func() time.Time { return *now }
	iss = &Issuer{Name: kf.Issuer, KeyID: kf.KeyID, Key: priv, Now: get}
	path = filepath.Join(t.TempDir(), "deny.json")
	d, err := LoadDenyList(path) // the file does not exist yet: empty list
	if err != nil {
		t.Fatal(err)
	}
	d.Now, d.checked = get, get()
	v = &Verifier{Issuer: kf.Issuer, Keys: map[string]ed25519.PublicKey{kf.KeyID: pub}, Now: get, Deny: d}
	return
}

func TestRevokeSubjectAppliesWithoutRestart(t *testing.T) {
	iss, v, now, path := denyEnv(t)
	old, _ := iss.Issue(Principal{Subject: "runner:a", Kind: KindService, Scopes: []string{"magi:runner"}}, time.Hour)
	other, _ := iss.Issue(Principal{Subject: "runner:b", Kind: KindService, Scopes: []string{"magi:runner"}}, time.Hour)
	if _, err := v.Verify(old); err != nil {
		t.Fatal(err)
	}
	if err := RevokeSubject(path, "runner:a", *now); err != nil {
		t.Fatal(err)
	}
	// Within the reload interval the verifier may still use the old list.
	*now = now.Add(DefaultDenyReload + time.Second)
	if _, err := v.Verify(old); err != ErrRevoked {
		t.Fatalf("revoked subject: %v", err)
	}
	if _, err := v.Verify(other); err != nil {
		t.Fatalf("other subject: %v", err)
	}
	// A token minted after the revocation works.
	fresh, _ := iss.Issue(Principal{Subject: "runner:a", Kind: KindService}, time.Hour)
	if _, err := v.Verify(fresh); err != nil {
		t.Fatalf("token minted after revocation: %v", err)
	}
}

func TestRevokeTokenByID(t *testing.T) {
	iss, v, now, path := denyEnv(t)
	a, _ := iss.Issue(Principal{Subject: "service:fleet-ops", Kind: KindService, Scopes: []string{"magi:operate"}}, time.Hour)
	b, _ := iss.Issue(Principal{Subject: "service:fleet-ops", Kind: KindService, Scopes: []string{"magi:operate"}}, time.Hour)
	pa, err := v.Verify(a)
	if err != nil || pa.ID == "" {
		t.Fatalf("token has no id: %+v %v", pa, err)
	}
	if err := RevokeToken(path, pa.ID); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(DefaultDenyReload)
	if _, err := v.Verify(a); err != ErrRevoked {
		t.Fatalf("revoked token: %v", err)
	}
	if _, err := v.Verify(b); err != nil {
		t.Fatalf("second token of the subject: %v", err)
	}
}

func TestDenyListKeepsLastGoodOnBadFile(t *testing.T) {
	iss, v, now, path := denyEnv(t)
	tok, _ := iss.Issue(Principal{Subject: "runner:a", Kind: KindService}, time.Hour)
	if err := RevokeSubject(path, "runner:a", *now); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(DefaultDenyReload)
	if _, err := v.Verify(tok); err != ErrRevoked {
		t.Fatal(err)
	}
	os.WriteFile(path, []byte("{not json"), 0o644)
	*now = now.Add(DefaultDenyReload)
	if _, err := v.Verify(tok); err != ErrRevoked {
		t.Fatalf("bad file lifted a revocation: %v", err)
	}
	if v.Deny.Err() == nil {
		t.Fatal("bad file not reported")
	}
	if _, err := LoadDenyList(path); err == nil {
		t.Fatal("start-up accepted a bad deny list")
	}
}

func TestLoadVerifierReadsDenyListEnv(t *testing.T) {
	dir := t.TempDir()
	kf, _ := GenerateKeyFile("magi-test")
	kf.PrivateKey = ""
	b, _ := json.Marshal(kf)
	key := filepath.Join(dir, "pub.json")
	os.WriteFile(key, b, 0o600)
	t.Setenv(DenyListEnv, filepath.Join(dir, "deny.json"))
	v, err := LoadVerifier(key)
	if err != nil || v.Deny == nil {
		t.Fatalf("verifier = %+v, %v", v, err)
	}
	os.WriteFile(filepath.Join(dir, "deny.json"), []byte("["), 0o644)
	if _, err := LoadVerifier(key); err == nil {
		t.Fatal("a bad deny list was accepted")
	}
}
