package store

import (
	"strings"
	"testing"
)

func TestSearchText(t *testing.T) {
	got := searchText("Work", "calendar", map[string]string{"b": "two", "a": "one"})
	if got != "Work calendar one two" {
		t.Fatalf("searchText: %q", got)
	}
}

func TestEscapeLike(t *testing.T) {
	if got := escapeLike(`50%_a\b`); got != `50\%\_a\\b` {
		t.Fatalf("escapeLike: %q", got)
	}
}

func TestNewID(t *testing.T) {
	a, b := NewID("nod_"), NewID("nod_")
	if a == b || !strings.HasPrefix(a, "nod_") || len(a) != 24 {
		t.Fatalf("ids: %q %q", a, b)
	}
}
