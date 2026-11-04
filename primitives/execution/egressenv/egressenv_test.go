package egressenv

import "testing"

// The same vector is in primitives/egress/src/proxy.rs.
func TestCredential(t *testing.T) {
	c := Credential([]byte("0123456789abcdef0123456789abcdef"), "env_a")
	if c != "06863b8c6d38ee30f3b0490b804c5eee1a8f7280cd7852d35b347ec84cf94a2c" {
		t.Fatalf("credential %s", c)
	}
	if u := URL("127.0.0.1:3128", "env_a", c); u != "http://env_a:"+c+"@127.0.0.1:3128" {
		t.Fatalf("url %s", u)
	}
	v := Vars("http://p", "/ca.pem")
	if v["HTTPS_PROXY"] != "http://p" || v["SSL_CERT_FILE"] != "/ca.pem" || v["NO_PROXY"] == "" {
		t.Fatalf("vars %v", v)
	}
}
