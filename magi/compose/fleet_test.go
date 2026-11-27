package compose

import (
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	magiapi "github.com/catalystcommunity/magi/magi/api"
	workflowapi "github.com/catalystcommunity/magi/primitives/workflow/api"
)

func TestChooseRunners(t *testing.T) {
	s := &System{Cfg: Config{Runner: "studio"}}
	now := uint64(time.Now().UnixMilli())
	s.fleet.seen(magiapi.RunnerInfo{Name: "studio", Repos: []string{"magi"}, Publishes: []string{"magi"}, SeenMs: now})
	s.fleet.seen(magiapi.RunnerInfo{Name: "pi", Repos: []string{"magi"}, Publishes: []string{}, SeenMs: now})
	s.fleet.seen(magiapi.RunnerInfo{Name: "gone", Repos: []string{"magi"}, SeenMs: now - uint64(time.Hour.Milliseconds())})
	cases := []struct{ runner, repo, wantRunner, wantPub, err string }{
		{"", "magi", "studio", "studio", ""},      // the default runner publishes itself
		{"pi", "magi", "pi", "studio", ""},        // pi cannot push: studio publishes
		{"gone", "magi", "", "", "not connected"}, // not seen recently
		{"pi", "other", "", "", "no repository"},
	}
	for _, c := range cases {
		r, p, err := s.chooseRunners(c.runner, c.repo)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%s/%s: err = %v, want %q", c.runner, c.repo, err, c.err)
			}
			continue
		}
		if err != nil || r != c.wantRunner || p != c.wantPub {
			t.Errorf("%s/%s = %s, %s, %v; want %s, %s", c.runner, c.repo, r, p, err, c.wantRunner, c.wantPub)
		}
	}
	// Without a publisher, the start fails at once, not after the review.
	s2 := &System{Cfg: Config{Runner: "pi"}}
	s2.fleet.seen(magiapi.RunnerInfo{Name: "pi", Repos: []string{"magi"}, SeenMs: now})
	if _, _, err := s2.chooseRunners("", "magi"); err == nil || !strings.Contains(err.Error(), "can publish") {
		t.Fatalf("no publisher: %v", err)
	}
	// With PUBLISHER set, it wins.
	s.Cfg.Publisher = "studio"
	if _, p, _ := s.chooseRunners("pi", "magi"); p != "studio" {
		t.Fatalf("publisher = %s", p)
	}
}

func TestCodeChangeQueues(t *testing.T) {
	d := codeChangeDefinition(true)
	want := map[string]string{"prepare": "runner.${runner}.prepare", "work": "runner.${runner}.work", "merge": "runner.${runner}.merge",
		"publish": "runner.${publisher}.publish", "pr-check": "runner.${publisher}.pr-check", "release": "magi.release", "rollout": "magi.rollout", "cleanup": "runner.${runner}.cleanup",
		"backup": "magi.backup", "check": "runner.${runner}.check"}
	seen := map[string]bool{}
	for _, st := range d.Steps {
		seen[st.Id] = true
	}
	for id := range want {
		if !seen[id] {
			t.Errorf("no step %s", id)
		}
	}
	for _, st := range d.Steps {
		if q, ok := want[st.Id]; ok && deref(st.Queue) != q {
			t.Errorf("%s queue = %s, want %s", st.Id, deref(st.Queue), q)
		}
	}
}

// A blocked work round waits for a person, and each decision leads on: a
// check failure there would start another work round on its own.
func TestCodeChangeBlockedStep(t *testing.T) {
	d := codeChangeDefinition(true)
	steps := map[string]workflowapi.Step{}
	for _, st := range d.Steps {
		steps[st.Id] = st
	}
	// A work that the model could not finish goes to the backup step first:
	// it starts a round with the backup model, or it hands on to the person
	// (blocked) or stops the task (give-up).
	for from, outcome := range map[string]string{"work": "blocked", "check": "give-up"} {
		if got := steps[from].Next[outcome]; got != "backup" {
			t.Fatalf("%s %s -> %q, want backup", from, outcome, got)
		}
	}
	if got := steps["check"].Next["guard-stop"]; got != "backup" {
		t.Fatalf("check guard-stop -> %q, want backup", got)
	}
	if !maps.Equal(steps["backup"].Next, map[string]string{"work": "work", "blocked": "blocked", "give-up": "cleanup"}) {
		t.Fatalf("backup next = %v", steps["backup"].Next)
	}
	// A failure of the environment needs a person, not a different model.
	if steps["work"].Next["environment"] != "blocked" || steps["check"].Next["blocked"] != "blocked" {
		t.Fatalf("environment failures: work %v, check %v", steps["work"].Next, steps["check"].Next)
	}
	b := steps["blocked"]
	if b.Kind != "human" || !slices.Equal(b.Outcomes, []string{"resume", "check", "reject"}) {
		t.Fatalf("blocked step = %+v", b)
	}
	if !maps.Equal(b.Next, map[string]string{"resume": "work", "check": "check", "reject": "cleanup"}) {
		t.Fatalf("blocked next = %v", b.Next)
	}
}

// The person approved the change at the review step: the pull request is
// merged with no second question, and the task waits only when the merge
// fails.
func TestPullRequestMergesAfterApproval(t *testing.T) {
	steps := map[string]workflowapi.Step{}
	for _, st := range codeChangeDefinition(true).Steps {
		steps[st.Id] = st
	}
	if got := steps["publish"].Next["proposed"]; got != "pr-merge" {
		t.Fatalf("publish proposed -> %q, want pr-merge", got)
	}
	if m := steps["pr-merge"]; m.Kind != "task" || m.Next["merged"] != "pr-check" || m.Next["fail"] != "pr-wait" {
		t.Fatalf("pr-merge = %+v", m)
	}
	if w := steps["pr-wait"]; w.Kind != "human" || w.Next["merge"] != "pr-merge" {
		t.Fatalf("pr-wait = %+v", w)
	}
}
