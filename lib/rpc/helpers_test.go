package rpc

import (
	"encoding/json"
	"os"
	"testing"
)

func writeJSON(t *testing.T, path string, v any) {
	b, _ := json.Marshal(v)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}
