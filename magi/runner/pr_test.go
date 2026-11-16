package runner

import (
	"context"
	magiapi "github.com/catalystcommunity/magi/magi/api"
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

func TestSanitizePullRequestText(t *testing.T) {
	for in, want := range map[string]string{
		"hi @octocat and @org/team":        "hi @\u200boctocat and @\u200borg/team",
		"![a](https://e.example/x.png)":    "[image removed]",
		"[go](https://e.example/a)":        "go (hxxps://e.example/a)",
		"see HTTP://e.example and www.e.x": "see hxxp://e.example and www[.]e.x",
		"<img src=x onerror=1>":            "&lt;img src=x onerror=1&gt;",
		"plain text stays":                 "plain text stays",
	} {
		if got := sanitize(in); got != want {
			t.Errorf("sanitize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPRTitleIsOneLine(t *testing.T) {
	got := prTitle(map[string]string{"title": "Fix\n@everyone\n![x](https://e.example/i.png)"})
	if strings.ContainsAny(got, "\n") || strings.Contains(got, "@everyone") || strings.Contains(got, "https://") {
		t.Errorf("title %q", got)
	}
}

func TestGatedCheckUsesBaseAndApproval(t *testing.T) {
	a := magiapi.RunnerAssignment{Vars: map[string]string{"base_commit": "abc123"}}
	g := gatedCheck(a) // the default command is a script of the repository
	if !strings.Contains(g, "'abc123'") || !strings.Contains(g, "'tools/check.sh'") || !strings.HasSuffix(g, "./tools/check.sh") {
		t.Errorf("guard missing: %s", g)
	}
	a.Vars["check_script_approved"] = "true"
	if g := gatedCheck(a); g != "./tools/check.sh" {
		t.Errorf("approved: %s", g)
	}
	a.Vars["check_command"] = "go test ./..."
	delete(a.Vars, "check_script_approved")
	if g := gatedCheck(a); g != "go test ./..." {
		t.Errorf("no script: %s", g)
	}
}
