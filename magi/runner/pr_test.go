package runner

import (
	"context"
	"strings"
	"testing"
)

func TestGithubRepo(t *testing.T) {
	for in, want := range map[string]string{
		"git@github.com:catalystcommunity/magi.git":       "catalystcommunity/magi",
		"https://github.com/catalystcommunity/magi":       "catalystcommunity/magi",
		"https://github.com/catalystcommunity/magi.git/":  "catalystcommunity/magi",
		"ssh://git@github.com/catalystcommunity/magi.git": "catalystcommunity/magi",
		"file:///srv/git/magi":                            "",
		"git@gitlab.com:x/y.git":                          "",
	} {
		if got := githubRepo(in); got != want {
			t.Errorf("%s: %q, want %q", in, got, want)
		}
	}
}

func TestPRCheck(t *testing.T) {
	old := gh
	t.Cleanup(func() { gh = old })
	r := &Runner{}
	for state, want := range map[string]string{
		`{"state":"OPEN","mergeCommit":null}`:               "open",
		`{"state":"MERGED","mergeCommit":{"oid":"abc123"}}`: "merged",
		`{"state":"CLOSED","mergeCommit":null}`:             "closed",
	} {
		var asked []string
		gh = func(_ context.Context, args ...string) ([]byte, error) { asked = args; return []byte(state), nil }
		res, err := r.prCheckState(context.Background(), "https://github.com/o/r/pull/7")
		if err != nil || res.Outcome != want {
			t.Fatalf("%s: %+v %v", state, res, err)
		}
		if strings.Join(asked[:3], " ") != "pr view https://github.com/o/r/pull/7" {
			t.Fatalf("gh %v", asked)
		}
		if want == "merged" && res.Outputs["publish.commit"] != "abc123" {
			t.Fatalf("merge commit %v", res.Outputs)
		}
	}
}

func TestPRBody(t *testing.T) {
	b := prBody(map[string]string{"instructions": "Add a note.", "apprentice.summary": "Added it."}, "run_x", "0123456789abcdef")
	for _, want := range []string{"Add a note.", "Added it.", "`run_x`", "`0123456789ab`"} {
		if !strings.Contains(b, want) {
			t.Errorf("body lacks %q:\n%s", want, b)
		}
	}
	if prTitle(map[string]string{"title": strings.Repeat("x", 150)}) != strings.Repeat("x", 100) {
		t.Error("long title")
	}
}
