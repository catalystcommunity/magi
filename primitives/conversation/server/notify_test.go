package server

import (
	"testing"

	"github.com/catalystcommunity/magi/lib/authn"
	api "github.com/catalystcommunity/magi/primitives/conversation/api"
)

func TestWaitSignal(t *testing.T) {
	var s Service
	a, a2, b := s.waitChan("a"), s.waitChan("a"), s.waitChan("b")
	if a != a2 {
		t.Fatal("waiters of one conversation must share a channel")
	}
	s.signal("a")
	select {
	case <-a:
	default:
		t.Fatal("signal did not close the channel")
	}
	select {
	case <-b:
		t.Fatal("signal closed the channel of another conversation")
	default:
	}
	if s.waitChan("a") == a {
		t.Fatal("after a signal, a new channel is expected")
	}
	s.signal("none") // no waiters: no panic
}

func TestCanSee(t *testing.T) {
	c := api.Conversation{Owner: "user:a", Participants: []string{"user:b"}}
	for _, tc := range []struct {
		p    authn.Principal
		want bool
	}{
		{authn.Principal{Subject: "user:a"}, true},
		{authn.Principal{Subject: "user:b"}, true},
		{authn.Principal{Subject: "user:c"}, false},
		{authn.Principal{Subject: "service:x", Scopes: []string{ScopeActAs}}, true},
		{authn.Principal{Subject: "user:root", Scopes: []string{"conversation:*"}}, true},
		{authn.Principal{Subject: "user:c", Scopes: []string{"artifacts:admin"}}, false},
	} {
		if canSee(tc.p, c) != tc.want {
			t.Errorf("canSee(%v) = %v", tc.p, !tc.want)
		}
	}
}
