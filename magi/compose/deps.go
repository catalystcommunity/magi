package compose

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	artifactsapi "github.com/catalystcommunity/magi/primitives/artifacts/api"
	conversationapi "github.com/catalystcommunity/magi/primitives/conversation/api"
	executionapi "github.com/catalystcommunity/magi/primitives/execution/api"
	knowledgeapi "github.com/catalystcommunity/magi/primitives/knowledge/api"
	releaseapi "github.com/catalystcommunity/magi/primitives/release/goapi"
	sourceapi "github.com/catalystcommunity/magi/primitives/source/api"
	speechapi "github.com/catalystcommunity/magi/primitives/speech/api"
	workflowapi "github.com/catalystcommunity/magi/primitives/workflow/api"
)

// DepCheck is the result of one dependency check.
type DepCheck struct {
	Name string
	Ms   int64
	Err  error
}

// depTimeout is the time limit of one dependency check.
const depTimeout = 5 * time.Second

// CheckDeps calls one read operation of each service that Magi uses, with
// the clients of this build. A call that the server does not answer, or an
// answer that this build cannot decode (a contract that does not agree),
// is an error. The operations return records when the installation has
// records, so that the decoder reads real responses, not only empty lists.
func (s *System) CheckDeps(ctx context.Context) []DepCheck {
	ctx = s.AsService(ctx)
	one := uint64(1)
	checks := []struct {
		name string
		call func(context.Context) error
	}{
		{"workflow", func(ctx context.Context) error {
			if _, err := s.Workflow.GetDefinition(ctx, workflowapi.GetDefinitionRequest{Name: CodeChange}); err != nil {
				return err
			}
			_, err := s.Workflow.ListRuns(ctx, workflowapi.ListRunsRequest{Limit: &one})
			return err
		}},
		{"conversation", func(ctx context.Context) error {
			_, err := s.Conversation.ListPersonas(ctx, conversationapi.ListPersonasRequest{})
			return err
		}},
		{"knowledge", func(ctx context.Context) error {
			_, err := s.Knowledge.Search(ctx, knowledgeapi.SearchRequest{Limit: &one})
			return err
		}},
		{"artifacts", func(ctx context.Context) error {
			_, err := s.Artifacts.List(ctx, artifactsapi.ListRequest{Limit: &one})
			return err
		}},
		{"execution", func(ctx context.Context) error {
			_, err := s.Execution.List(ctx, executionapi.ListRequest{Limit: &one})
			return err
		}},
		{"source", func(ctx context.Context) error {
			_, err := s.Source.ListRepos(ctx, sourceapi.ListReposRequest{Limit: &one})
			return err
		}},
		{"speech", func(ctx context.Context) error {
			_, err := s.Speech.ListVoices(ctx, speechapi.ListVoicesRequest{})
			return err
		}},
	}
	if s.Release != nil {
		checks = append(checks, struct {
			name string
			call func(context.Context) error
		}{"release", func(ctx context.Context) error {
			_, err := s.Release.ListTargets(ctx, releaseapi.ListTargetsRequest{})
			return err
		}})
	}
	out := make([]DepCheck, len(checks))
	var wg sync.WaitGroup
	for i, c := range checks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, depTimeout)
			defer cancel()
			start := time.Now()
			err := c.call(cctx)
			out[i] = DepCheck{Name: c.name, Ms: time.Since(start).Milliseconds(), Err: err}
		}()
	}
	wg.Wait()
	return out
}

// DepsFailed returns a line for each failed check, or nil.
func DepsFailed(cs []DepCheck) []string {
	var bad []string
	for _, c := range cs {
		if c.Err != nil {
			bad = append(bad, fmt.Sprintf("%s: %v", c.Name, c.Err))
		}
	}
	return bad
}

// DepsHandler serves the dependency checks: 200 with "name ok" for each
// service, or 503 when a check fails. The release controller uses it during
// the stabilization window. Do not use it as the readiness probe: then one
// service that stops makes all Magi pods unready.
//
// The path can be public, so the answer has no error text (the log has it),
// and the result is kept for 2 seconds, so that callers cannot multiply the
// calls to the services.
func (s *System) DepsHandler() http.Handler {
	var mu sync.Mutex
	var last []DepCheck
	var at time.Time
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if time.Since(at) > 2*time.Second {
			last, at = s.CheckDeps(context.WithoutCancel(r.Context())), time.Now()
			if bad := DepsFailed(last); bad != nil {
				s.Log.Warn("dependency check", "failed", bad)
			}
		}
		cs := last
		mu.Unlock()
		var b strings.Builder
		for _, c := range cs {
			state := "ok"
			if c.Err != nil {
				state = "fail"
			}
			fmt.Fprintf(&b, "%s %s %dms\n", c.Name, state, c.Ms)
		}
		if DepsFailed(cs) != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		w.Write([]byte(b.String()))
	})
}
