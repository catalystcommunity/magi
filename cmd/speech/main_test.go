package main

import (
	w "github.com/catalystcommunity/magi/generated/go"
	"testing"
)

func TestLocalKeyBoundary(t *testing.T) {
	a := w.ApplicationRef{Identity: w.IdentityRef{Domain: "local.opal.test", AccountId: "fixture"}, ApplicationId: "speech", InstanceId: "one"}
	c := configuration{Application: a, LocalKeys: []localKey{{Actor: a, Public: make([]byte, 32)}}}
	if !validLocal(c, "127.0.0.1:8094", "") {
		t.Fatal("local setup rejected")
	}
	for _, address := range []string{"0.0.0.0:8094", "192.0.2.20:8094", "localhost:8094"} {
		if validLocal(c, address, "") {
			t.Fatal("nonliteral loopback accepted")
		}
	}
	if validLocal(c, "127.0.0.1:8094", "https://magi.catalystsquad.local") {
		t.Fatal("public origin accepted")
	}
	c.Application.Identity.Domain = "catalystsquad.local"
	if validLocal(c, "127.0.0.1:8094", "") {
		t.Fatal("real identity accepted")
	}
}
