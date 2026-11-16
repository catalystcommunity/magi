package compose

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/catalystcommunity/magi/lib/authn"
	"github.com/catalystcommunity/magi/lib/rpc"
	magiapi "github.com/catalystcommunity/magi/magi/api"
)

func runnerCtx(name string) context.Context {
	return authn.With(context.Background(), authn.Principal{Subject: "runner:" + name, Kind: authn.KindService, Scopes: []string{ScopeRunner}})
}

func TestPublishRunnersFromEnv(t *testing.T) {
	got, err := publishRunnersFromEnv(" studio, eval ,,")
	if err != nil || len(got) != 2 || got[0] != "studio" || got[1] != "eval" {
		t.Fatalf("%v %v", got, err)
	}
	if got, err := publishRunnersFromEnv(""); err != nil || len(got) != 0 {
		t.Fatalf("empty: %v %v", got, err)
	}
	for _, bad := range []string{"*", "a b", "runner:x", "a/b"} {
		if _, err := publishRunnersFromEnv(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	t.Setenv("MAGI_PUBLISH_RUNNERS", "studio,eval")
	if c, err := ConfigFromEnv(); err != nil || len(c.PublishRunners) != 2 {
		t.Fatalf("config: %v %v", c.PublishRunners, err)
	}
	t.Setenv("MAGI_PUBLISH_RUNNERS", "*")
	if _, err := ConfigFromEnv(); err == nil {
		t.Fatal("wildcard accepted")
	}
}

// A runner that is not approved gets no publish step, even when it says
// that it publishes the repository, and a configured publisher must be approved.
func TestChooseRunnersNeedsApproval(t *testing.T) {
	now := uint64(time.Now().UnixMilli())
	s := &System{Cfg: Config{Runner: "mac", PublishRunners: []string{"eval"}}}
	s.fleet.seen(magiapi.RunnerInfo{Name: "mac", Repos: []string{"magi"}, Publishes: []string{"magi"}, SeenMs: now})
	if _, _, err := s.chooseRunners("", "magi"); err == nil || !strings.Contains(err.Error(), "approved") {
		t.Fatalf("unapproved self-declared publisher: %v", err)
	}
	s.fleet.seen(magiapi.RunnerInfo{Name: "eval", Repos: []string{"magi"}, Publishes: []string{"magi"}, SeenMs: now})
	if r, p, err := s.chooseRunners("", "magi"); err != nil || r != "mac" || p != "eval" {
		t.Fatalf("approved publisher: %s %s %v", r, p, err)
	}
	s.Cfg.Publisher = "mac"
	if _, _, err := s.chooseRunners("", "magi"); err == nil || !strings.Contains(err.Error(), "not approved") {
		t.Fatalf("unapproved MAGI_PUBLISHER: %v", err)
	}
	// With no list, nothing publishes.
	s2 := &System{Cfg: Config{Runner: "mac"}}
	s2.fleet.seen(magiapi.RunnerInfo{Name: "mac", Repos: []string{"magi"}, Publishes: []string{"magi"}, SeenMs: now})
	if _, _, err := s2.chooseRunners("", "magi"); err == nil {
		t.Fatal("empty approval list allowed a publisher")
	}
}

// An unapproved runner (for example one with allow_host_network, which
// Magi cannot see) cannot claim publish steps, post a publish result, or
// upload a release build. Its other steps are not affected by the gate.
func TestUnapprovedRunnerRefused(t *testing.T) {
	s := &System{Cfg: Config{PublishRunners: []string{"eval"}}}
	r := &Runners{S: s}
	ctx := runnerCtx("hostnet")
	for _, step := range publishSteps {
		_, err := r.Claim(ctx, magiapi.RunnerClaimRequest{Queue: RunnerQueue("hostnet", step)})
		if rpc.CodeOf(err) != 403 || !strings.Contains(err.Error(), "MAGI_PUBLISH_RUNNERS") {
			t.Errorf("claim %s: %v", step, err)
		}
		_, err = r.Complete(ctx, magiapi.RunnerCompleteRequest{Step: magiapi.RunnerStep{Run: "run_1", Step: step, Claim: "c"}, Outcome: "done"})
		if rpc.CodeOf(err) != 403 {
			t.Errorf("complete %s: %v", step, err)
		}
	}
	_, err := r.Complete(ctx, magiapi.RunnerCompleteRequest{Step: magiapi.RunnerStep{Run: "run_1", Step: "merge", Claim: "c"}, Outcome: "done",
		Outputs: map[string]string{"build.binary": "sha256:x"}})
	if rpc.CodeOf(err) != 403 {
		t.Errorf("complete merge with build: %v", err)
	}
	for _, kind := range releaseArtifactKinds {
		_, err = r.PutArtifact(ctx, magiapi.RunnerPutArtifactRequest{Labels: map[string]string{"kind": kind}})
		if rpc.CodeOf(err) != 403 {
			t.Errorf("put %s: %v", kind, err)
		}
	}
	if !s.publishApproved("eval") || s.publishApproved("hostnet") || s.publishApproved("") {
		t.Fatal("publishApproved")
	}
}
