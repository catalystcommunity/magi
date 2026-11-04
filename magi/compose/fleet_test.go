package compose

import (
	"strings"
	"testing"
	"time"

	magiapi "github.com/catalystcommunity/magi/magi/api"
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
		"publish": "runner.${publisher}.publish", "release": "magi.release", "rollout": "magi.rollout", "cleanup": "runner.${runner}.cleanup"}
	for _, st := range d.Steps {
		if q, ok := want[st.Id]; ok && deref(st.Queue) != q {
			t.Errorf("%s queue = %s, want %s", st.Id, deref(st.Queue), q)
		}
	}
}
