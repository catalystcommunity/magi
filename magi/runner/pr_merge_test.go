package runner

import "testing"

func TestPRURL(t *testing.T) {
	m := prURL.FindStringSubmatch("https://github.com/catalystcommunity/magi/pull/12")
	if m == nil || m[1] != "catalystcommunity" || m[2] != "magi" || m[3] != "12" {
		t.Fatalf("%v", m)
	}
	for _, bad := range []string{"https://github.com/a/b/pull/1; rm -rf /", "https://evil.example/a/b/pull/1", "https://github.com/a/b/pull/x"} {
		if prURL.MatchString(bad) {
			t.Errorf("accepted %q", bad)
		}
	}
}

// GitHub refuses a merge method that the repository does not permit. A
// different method can merge then. Another refusal ends the try.
func TestMergeAnswer(t *testing.T) {
	for msg, want := range map[string]bool{
		"Merge commits are not allowed on this repository.": true,
		"Rebase merges are not allowed on this repository.": true,
		"Pull Request is not mergeable":                     false,
		"At least 1 approving review is required.":          false,
		"": false,
	} {
		if got := (mergeAnswer{Message: msg}).otherMethod(); got != want {
			t.Errorf("otherMethod(%q) = %v, want %v", msg, got, want)
		}
	}
	if (mergeAnswer{Merged: true, Message: "not allowed on this repository"}).otherMethod() {
		t.Error("a merge that passed needs no other method")
	}
}
