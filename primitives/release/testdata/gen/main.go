// Command gen writes testdata/authn-vector.json: tokens issued by lib/authn
// with a fixed test key. The Rust tests verify them.
//
//	cd primitives/release/testdata/gen && GOWORK=off go run . > ../authn-vector.json
package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"time"

	"github.com/catalystcommunity/magi/lib/authn"
)

func main() {
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i + 1)
	}
	priv := ed25519.NewKeyFromSeed(seed)
	key := authn.KeyFile{
		Issuer:    "magi.example.local",
		KeyID:     "test-kid-1",
		PublicKey: base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey)),
	}
	at := func(t time.Time) func() time.Time { return func() time.Time { return t } }
	iss := &authn.Issuer{Name: key.Issuer, KeyID: key.KeyID, Key: priv, Now: at(time.Unix(1790000000, 0))}
	issue := func(p authn.Principal, ttl time.Duration) string {
		t, err := iss.Issue(p, ttl)
		if err != nil {
			panic(err)
		}
		return t
	}
	long := 100 * 365 * 24 * time.Hour
	out := map[string]any{
		"key":      key,
		"now":      1790000100,
		"operator": issue(authn.Principal{Subject: "service:magi", Kind: authn.KindService, Scopes: []string{"release:operate"}}, long),
		"reader":   issue(authn.Principal{Subject: "user:ana@example.local", Kind: authn.KindUser, Scopes: []string{"release:read"}}, long),
		"wildcard": issue(authn.Principal{Subject: "user:root@example.local", Kind: authn.KindUser, Scopes: []string{"*"}}, long),
		"noscope":  issue(authn.Principal{Subject: "user:bob@example.local", Kind: authn.KindUser}, long),
		"expired":  issue(authn.Principal{Subject: "service:magi", Kind: authn.KindService, Scopes: []string{"release:operate"}}, time.Minute),
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(out)
}
