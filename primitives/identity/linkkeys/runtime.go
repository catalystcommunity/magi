package linkkeys

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

// Runtime is the private RP connection file (MAGI_LINKKEYS_RUNTIME):
//
//	{"rp_domain": "example.local", "tcp_address": "rp.example.local:4987",
//	 "fingerprints": ["<64 hex>"], "api_key": "<secret>"}
type Runtime struct {
	RPDomain     string   `json:"rp_domain"`
	TCPAddress   string   `json:"tcp_address"`
	Fingerprints []string `json:"fingerprints"`
	APIKey       string   `json:"api_key"`
}

// LoadRuntime reads and checks a runtime file. Errors never contain the key.
func LoadRuntime(path string) (Runtime, error) {
	var r Runtime
	b, err := os.ReadFile(path)
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return r, fmt.Errorf("linkkeys runtime %s: invalid JSON", path)
	}
	r.RPDomain = strings.ToLower(strings.TrimSpace(r.RPDomain))
	if !strings.Contains(r.TCPAddress, ":") {
		r.TCPAddress += ":4987"
	}
	switch {
	case !ValidDomain(r.RPDomain, false):
		return r, errors.New("linkkeys runtime: rp_domain is not a DNS name")
	case r.TCPAddress == ":4987":
		return r, errors.New("linkkeys runtime: tcp_address is required")
	case len(r.Fingerprints) == 0:
		return r, errors.New("linkkeys runtime: fingerprints are required")
	case r.APIKey == "":
		return r, errors.New("linkkeys runtime: api_key is required")
	}
	for _, f := range r.Fingerprints {
		if len(f) != 64 {
			return r, errors.New("linkkeys runtime: a fingerprint is not 64 hex characters")
		}
	}
	return r, nil
}

// Transport returns a pinned transport for the runtime.
func (r Runtime) Transport() *PinnedTransport {
	return &PinnedTransport{Address: r.TCPAddress, Fingerprints: r.Fingerprints, APIKey: r.APIKey}
}
