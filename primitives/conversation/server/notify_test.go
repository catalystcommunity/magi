package server

import (
	"slices"
	"testing"

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

func TestReaders(t *testing.T) {
	st := api.Stream{Person: "user:a", Persona: "persona:p"}
	if r := readers(st, "private"); !slices.Equal(r, []string{"user:a", "persona:p"}) {
		t.Fatalf("private readers = %v", r)
	}
	if r := readers(st, "shared"); !slices.Equal(r, []string{"user:a", "persona:p", "audience:persona:p"}) {
		t.Fatalf("shared readers = %v", r)
	}
}
