package compose

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/catalystcommunity/magi/lib/rpc"
	magiapi "github.com/catalystcommunity/magi/magi/api"
	artifactsapi "github.com/catalystcommunity/magi/primitives/artifacts/api"
	workflowapi "github.com/catalystcommunity/magi/primitives/workflow/api"
)

// adminWorkflow serves the runs of the admin tests and records each request
// with the subject of the caller.
type adminWorkflow struct {
	workflowapi.WorkflowService
	mu      sync.Mutex
	runs    []workflowapi.Run
	listed  []workflowapi.ListRunsRequest
	callers []string
}

// record notes the subject of the caller of one operation.
func (w *adminWorkflow) record(ctx context.Context) {
	p, err := rpc.Caller(ctx)
	if err != nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.callers = append(w.callers, p.Subject)
}

func (w *adminWorkflow) ListRuns(ctx context.Context, r workflowapi.ListRunsRequest) (workflowapi.ListRunsResponse, error) {
	w.record(ctx)
	w.mu.Lock()
	defer w.mu.Unlock()
	w.listed = append(w.listed, r)
	out := workflowapi.ListRunsResponse{Runs: []workflowapi.Run{}}
	for _, run := range w.runs {
		if r.Definition != nil && *r.Definition != "" && run.Definition != *r.Definition {
			continue
		}
		if r.Status != nil && *r.Status != "" && run.Status != *r.Status {
			continue
		}
		out.Runs = append(out.Runs, run)
	}
	return out, nil
}

func (w *adminWorkflow) GetRun(ctx context.Context, r workflowapi.GetRunRequest) (workflowapi.Run, error) {
	w.record(ctx)
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, run := range w.runs {
		if run.Id == r.Id {
			return run, nil
		}
	}
	return workflowapi.Run{}, rpc.NotFound("no run %s", r.Id)
}

// calls returns the subjects that reached the workflow service.
func (w *adminWorkflow) calls() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string{}, w.callers...)
}

// lastList returns the newest list-runs request that the service saw.
func (w *adminWorkflow) lastList() workflowapi.ListRunsRequest {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.listed[len(w.listed)-1]
}

// adminRunsEnv serves MagiService with the workflow and artifacts fakes. It
// returns the workflow fake, the client, and a token maker.
func adminRunsEnv(t *testing.T, fa *fakeArtifacts, runs []workflowapi.Run) (*adminWorkflow, *magiapi.MagiClient, func(string, ...string) context.Context) {
	t.Helper()
	w := &adminWorkflow{runs: runs}
	_, mc, as := adminEnv(t, Config{Admins: []string{"user:admin@example.local"}}, map[string]map[string]func(context.Context, []byte) ([]byte, error){
		"WorkflowService": workflowapi.WorkflowServiceOps(w),
		"ArtifactService": artifactsapi.ArtifactServiceOps(fa),
	})
	return w, mc, as
}

// adminRun is a code-change run of user a: a step with a note, and a checked
// change (branch, base, head).
func adminRun() workflowapi.Run {
	return workflowapi.Run{Id: "run_1", Definition: CodeChange, Owner: "user:a@example.local", Status: "running", CurrentStep: "work",
		Vars: map[string]string{"title": "Fix the login", "instructions": "Fix the login page", "repo": "magi",
			"branch": "task/run_1", "base_commit": "b1", "head_commit": "h1"},
		History:   []workflowapi.StepState{{Step: "prepare", Status: "done", Progress: []workflowapi.Progress{{Note: "environment made"}}}},
		CreatedMs: 1000, UpdatedMs: 2000}
}

// putArtifact stores body in the artifacts fake and returns its digest.
func putArtifact(t *testing.T, fa *fakeArtifacts, mediaType string, body string) string {
	t.Helper()
	a, err := fa.Put(context.Background(), artifactsapi.PutRequest{MediaType: mediaType, Body: []byte(body)})
	if err != nil {
		t.Fatal(err)
	}
	return a.Digest
}

// A non-admin gets 403 on each admin op, and the workflow service sees no
// call: the role check runs before the read.
func TestAdminRunsDenied(t *testing.T) {
	w, mc, as := adminRunsEnv(t, &fakeArtifacts{body: map[string][]byte{}}, []workflowapi.Run{adminRun()})
	person := as("user:a@example.local")
	for _, call := range []func() error{
		func() error { _, err := mc.AdminListRuns(person, magiapi.AdminListRunsRequest{}); return err },
		func() error { _, err := mc.AdminGetRun(person, magiapi.AdminGetRunRequest{Id: "run_1"}); return err },
		func() error {
			_, err := mc.AdminGetRunOutput(person, magiapi.AdminGetRunOutputRequest{Id: "run_1", Name: "check.output"})
			return err
		},
		func() error {
			_, err := mc.AdminGetRunDiff(person, magiapi.AdminGetRunDiffRequest{Id: "run_1"})
			return err
		},
	} {
		if err := call(); rpc.CodeOf(err) != 403 {
			t.Fatalf("a non-admin did not get 403: %v", err)
		}
	}
	if got := w.calls(); len(got) != 0 {
		t.Fatalf("the workflow service saw %v, want no call", got)
	}
}

// An admin lists the runs of all owners: Magi reads them with its service
// token and sends the filters and the limit.
func TestAdminListRuns(t *testing.T) {
	a := adminRun()
	b := workflowapi.Run{Id: "run_2", Definition: PersonaTurn, Owner: "user:b@example.local", Status: "succeeded",
		Vars: map[string]string{}, CreatedMs: 3000, UpdatedMs: 4000}
	w, mc, as := adminRunsEnv(t, &fakeArtifacts{body: map[string][]byte{}}, []workflowapi.Run{a, b})
	admin := as("user:admin@example.local")
	r, err := mc.AdminListRuns(admin, magiapi.AdminListRunsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Runs) != 2 || r.Runs[0].Id != "run_1" || r.Runs[1].Id != "run_2" {
		t.Fatalf("runs: %+v", r.Runs)
	}
	if s := r.Runs[0]; deref(s.Title) != "Fix the login" || s.Owner != "user:a@example.local" || s.Definition != CodeChange ||
		s.Status != "running" || s.CurrentStep != "work" || s.CreatedMs != 1000 || s.UpdatedMs != 2000 {
		t.Fatalf("summary: %+v", s)
	}
	if s := r.Runs[1]; s.Title != nil {
		t.Fatalf("a run with no title has one: %+v", s)
	}
	if got := w.calls(); len(got) != 1 || got[0] != "service:magi" {
		t.Fatalf("the workflow service saw %v, want service:magi", got)
	}
	if got := w.lastList(); deref(got.Limit) != 50 || got.Definition != nil || got.Status != nil {
		t.Fatalf("request: %+v", got)
	}
	// The filters go through.
	r, err = mc.AdminListRuns(admin, magiapi.AdminListRunsRequest{Definition: ptr(PersonaTurn), Status: ptr("succeeded")})
	if err != nil || len(r.Runs) != 1 || r.Runs[0].Id != "run_2" {
		t.Fatalf("filtered: %+v %v", r, err)
	}
	if got := w.lastList(); deref(got.Definition) != PersonaTurn || deref(got.Status) != "succeeded" {
		t.Fatalf("filtered request: %+v", got)
	}
	// A bad definition and a bad status each name the field.
	for _, bad := range []struct {
		req   magiapi.AdminListRunsRequest
		field string
	}{
		{magiapi.AdminListRunsRequest{Definition: ptr("other")}, "definition"},
		{magiapi.AdminListRunsRequest{Status: ptr("paused")}, "status"},
	} {
		_, err := mc.AdminListRuns(admin, bad.req)
		if rpc.CodeOf(err) != 400 || !strings.Contains(err.Error(), bad.field) {
			t.Fatalf("bad %s: %v", bad.field, err)
		}
	}
	// limit 0 sends 50 (checked above); a limit over 500 sends 500.
	if _, err := mc.AdminListRuns(admin, magiapi.AdminListRunsRequest{Limit: ptr(uint64(9999))}); err != nil {
		t.Fatal(err)
	}
	if got := deref(w.lastList().Limit); got != 500 {
		t.Fatalf("limit 9999 sent %d, want 500", got)
	}
}

// admin-get-run returns the summary, the vars, and the steps with their
// notes.
func TestAdminGetRun(t *testing.T) {
	w, mc, as := adminRunsEnv(t, &fakeArtifacts{body: map[string][]byte{}}, []workflowapi.Run{adminRun()})
	r, err := mc.AdminGetRun(as("user:admin@example.local"), magiapi.AdminGetRunRequest{Id: "run_1"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Summary.Id != "run_1" || r.Summary.Owner != "user:a@example.local" {
		t.Fatalf("summary: %+v", r.Summary)
	}
	if r.Vars["instructions"] != "Fix the login page" || r.Vars["repo"] != "magi" {
		t.Fatalf("vars: %v", r.Vars)
	}
	if len(r.Steps) != 1 || r.Steps[0].Step != "prepare" || len(r.Steps[0].Notes) != 1 || r.Steps[0].Notes[0] != "environment made" {
		t.Fatalf("steps: %+v", r.Steps)
	}
	if got := w.calls(); len(got) != 1 || got[0] != "service:magi" {
		t.Fatalf("the workflow service saw %v", got)
	}
	if _, err := mc.AdminGetRun(as("user:admin@example.local"), magiapi.AdminGetRunRequest{Id: "run_x"}); rpc.CodeOf(err) != 404 {
		t.Fatalf("unknown run: %v", err)
	}
}

// admin-get-run-output reads an artifact of the run: an unknown name is a
// client error, and a run without the digest has no output.
func TestAdminGetRunOutput(t *testing.T) {
	run := adminRun()
	fa := &fakeArtifacts{body: map[string][]byte{}}
	run.Vars["check.output"] = putArtifact(t, fa, "text/plain", "all checks passed")
	w, mc, as := adminRunsEnv(t, fa, []workflowapi.Run{run})
	admin := as("user:admin@example.local")
	if _, err := mc.AdminGetRunOutput(admin, magiapi.AdminGetRunOutputRequest{Id: "run_1", Name: "nope"}); rpc.CodeOf(err) != 400 {
		t.Fatalf("unknown name: %v", err)
	}
	if _, err := mc.AdminGetRunOutput(admin, magiapi.AdminGetRunOutputRequest{Id: "run_1", Name: "apprentice.transcript"}); rpc.CodeOf(err) != 404 {
		t.Fatalf("no transcript: %v", err)
	}
	o, err := mc.AdminGetRunOutput(admin, magiapi.AdminGetRunOutputRequest{Id: "run_1", Name: "check.output"})
	if err != nil || o.Text != "all checks passed" || o.MediaType != "text/plain" || o.Task != "run_1" || o.Name != "check.output" {
		t.Fatalf("output: %+v %v", o, err)
	}
	// Every read of the workflow service is with the service token of magi.
	for _, got := range w.calls() {
		if got != "service:magi" {
			t.Fatalf("the workflow service saw %s, want service:magi", got)
		}
	}
}

// admin-get-run-diff returns the patch and the file list that a runner
// uploaded.
func TestAdminGetRunDiff(t *testing.T) {
	run := adminRun()
	fa := &fakeArtifacts{body: map[string][]byte{}}
	run.Vars["patch"] = putArtifact(t, fa, "text/plain", "--- a/a.go\n+++ b/a.go\n")
	run.Vars["patch.files"] = `[{"path": "a.go", "additions": 2, "deletions": 1}]`
	_, mc, as := adminRunsEnv(t, fa, []workflowapi.Run{run})
	d, err := mc.AdminGetRunDiff(as("user:admin@example.local"), magiapi.AdminGetRunDiffRequest{Id: "run_1"})
	if err != nil {
		t.Fatal(err)
	}
	if d.Task != "run_1" || d.Patch != "--- a/a.go\n+++ b/a.go\n" || len(d.Files) != 1 || d.Files[0].Path != "a.go" ||
		d.Files[0].Additions != 2 || d.Files[0].Deletions != 1 {
		t.Fatalf("diff: %+v", d)
	}
}
