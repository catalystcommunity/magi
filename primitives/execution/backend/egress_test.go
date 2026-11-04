package backend

import "testing"

// The same vector is in primitives/egress/src/proxy.rs (credential_vector).
func TestEgressCredential(t *testing.T) {
	e := &EgressProxy{Host: "egress.magi.svc", Port: 3128, Key: []byte("0123456789abcdef0123456789abcdef")}
	if got := e.Credential("env_a"); got != "06863b8c6d38ee30f3b0490b804c5eee1a8f7280cd7852d35b347ec84cf94a2c" {
		t.Fatalf("credential %s", got)
	}
	if u := e.URL("env_a"); u != "http://env_a:06863b8c6d38ee30f3b0490b804c5eee1a8f7280cd7852d35b347ec84cf94a2c@egress.magi.svc:3128" {
		t.Fatalf("url %s", u)
	}
}
