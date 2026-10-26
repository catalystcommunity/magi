package main

import (
	"strings"
	"testing"
)

func TestStrictInput(t *testing.T) {
	for _, raw := range []string{`{"operation":"start","unexpected":true}`, `{"operation":"start"} {}`, `[]`, `{"binding":{"controller":{"bogus":true}}}`} {
		var r request
		if decode([]byte(raw), &r) == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	var r request
	if decode([]byte(`{"operation":"start"}`), &r) != nil || r.Operation != "start" {
		t.Fatal("valid shape failed")
	}
}

func TestProfileNeverComesFromRequest(t *testing.T) {
	for _, args := range [][]string{{}, {"--shell", "/tmp/ignored"}, {"--request"}, {"--request", "/tmp/ignored", "--migrate"}} {
		if run(t.Context(), args, strings.NewReader(""), &strings.Builder{}) == nil {
			t.Fatal("accepted invalid arguments")
		}
	}
}
