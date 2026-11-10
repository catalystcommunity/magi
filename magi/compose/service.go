package compose

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/catalystcommunity/magi/lib/rpc"
	magiapi "github.com/catalystcommunity/magi/magi/api"
	artifactsapi "github.com/catalystcommunity/magi/primitives/artifacts/api"
	knowledgeapi "github.com/catalystcommunity/magi/primitives/knowledge/api"
	sourceapi "github.com/catalystcommunity/magi/primitives/source/api"
	speechapi "github.com/catalystcommunity/magi/primitives/speech/api"
	workflowapi "github.com/catalystcommunity/magi/primitives/workflow/api"
)

// Service implements the human-facing MagiService.
//
// Reads and writes of a person's own records use the person's token, so
// each primitive authorizes the person. Background work uses the service
// token with act-as scopes.
type Service struct{ S *System }

var _ magiapi.MagiService = (*Service)(nil)

func (m *Service) GetAudio(ctx context.Context, req magiapi.GetAudioRequest) (magiapi.AudioResponse, error) {
	a, err := m.S.Artifacts.Get(AsCaller(ctx), artifactsapi.GetRequest{Digest: req.Digest})
	if err != nil {
		return magiapi.AudioResponse{}, err
	}
	if !strings.HasPrefix(a.Artifact.MediaType, "audio/") {
		return magiapi.AudioResponse{}, rpc.Invalid("artifact is not audio")
	}
	return magiapi.AudioResponse{Body: a.Body, MediaType: a.Artifact.MediaType}, nil
}

func (m *Service) StartTask(ctx context.Context, req magiapi.StartTaskRequest) (magiapi.Task, error) {
	caller, err := rpc.Caller(ctx)
	if err != nil {
		return magiapi.Task{}, err
	}
	if req.Kind != CodeChange {
		return magiapi.Task{}, rpc.Invalid("unknown task kind %q", req.Kind)
	}
	title := deref(req.Title)
	if title == "" {
		title = firstLine(req.Instructions, 60)
	}
	run, err := m.S.startCodeChange(m.S.AsService(ctx), caller.Subject, origin{}, title, req.Instructions, deref(req.Repo), deref(req.Runner))
	if err != nil {
		return magiapi.Task{}, err
	}
	return task(run), nil
}

// origin is the message that asked for a task, if any.
type origin struct{ stream, utterance, persona string }

// startCodeChange starts a code-change run owned by owner. ctx must carry
// the service token. The run reports to the stream of from.
func (s *System) startCodeChange(ctx context.Context, owner string, from origin, title, instructions, repo, runner string) (workflowapi.Run, error) {
	if strings.TrimSpace(instructions) == "" {
		return workflowapi.Run{}, rpc.Invalid("instructions are required")
	}
	if repo == "" {
		repo = s.Cfg.Repo
	}
	runner, publisher, err := s.chooseRunners(runner, repo)
	if err != nil {
		return workflowapi.Run{}, err
	}
	vars := map[string]string{"title": title, "instructions": instructions, "repo": repo, "check_command": s.Cfg.CheckCommand,
		"runner": runner, "publisher": publisher, "stream": from.stream, "utterance": from.utterance, "persona": from.persona}
	return s.Workflow.Start(ctx, workflowapi.StartRequest{Definition: CodeChange, Vars: vars, Owner: ptr(owner)})
}

func (m *Service) GetTask(ctx context.Context, req magiapi.GetTaskRequest) (magiapi.Task, error) {
	run, err := m.S.Workflow.GetRun(AsCaller(ctx), workflowapi.GetRunRequest{Id: req.Id})
	if err != nil {
		return magiapi.Task{}, err
	}
	if run.Definition != CodeChange {
		return magiapi.Task{}, rpc.NotFound("no task %s", req.Id)
	}
	return task(run), nil
}

func (m *Service) ListTasks(ctx context.Context, req magiapi.ListTasksRequest) (magiapi.ListTasksResponse, error) {
	res, err := m.S.Workflow.ListRuns(AsCaller(ctx), workflowapi.ListRunsRequest{Definition: ptr(CodeChange), Status: req.Status, Limit: req.Limit})
	if err != nil {
		return magiapi.ListTasksResponse{}, err
	}
	out := magiapi.ListTasksResponse{Tasks: []magiapi.Task{}}
	for _, r := range res.Runs {
		out.Tasks = append(out.Tasks, task(r))
	}
	return out, nil
}

func (m *Service) GetTaskDiff(ctx context.Context, req magiapi.GetTaskDiffRequest) (magiapi.TaskDiff, error) {
	// Reading the run with the caller's token is the access check.
	run, err := m.S.Workflow.GetRun(AsCaller(ctx), workflowapi.GetRunRequest{Id: req.Id})
	if err != nil {
		return magiapi.TaskDiff{}, err
	}
	branch, base := run.Vars["branch"], run.Vars["base_commit"]
	if branch == "" || base == "" || run.Vars["head_commit"] == "" {
		return magiapi.TaskDiff{}, rpc.Conflict("the task has no checked change yet")
	}
	if digest := run.Vars["patch"]; digest != "" {
		// A runner uploaded the patch and its file list.
		got, err := m.S.Artifacts.Get(m.S.AsService(ctx), artifactsapi.GetRequest{Digest: digest})
		if err != nil {
			return magiapi.TaskDiff{}, err
		}
		out := magiapi.TaskDiff{Task: run.Id, Patch: string(got.Body), Files: []magiapi.FileChange{}}
		if len(out.Patch) > 1<<20 {
			out.Patch, out.Truncated = out.Patch[:1<<20], true
		}
		json.Unmarshal([]byte(run.Vars["patch.files"]), &out.Files)
		return out, nil
	}
	d, err := m.S.Source.Diff(m.S.AsService(ctx), sourceapi.DiffRequest{Repo: run.Vars["repo"], Base: base, Head: branch, MaxBytes: ptr(uint64(1 << 20))})
	if err != nil {
		return magiapi.TaskDiff{}, err
	}
	out := magiapi.TaskDiff{Task: run.Id, Patch: d.Patch, Truncated: d.Truncated, Files: []magiapi.FileChange{}}
	for _, f := range d.Files {
		out.Files = append(out.Files, magiapi.FileChange{Path: f.Path, Additions: f.Additions, Deletions: f.Deletions})
	}
	return out, nil
}

func (m *Service) Decide(ctx context.Context, req magiapi.DecideRequest) (magiapi.Task, error) {
	uctx := AsCaller(ctx)
	run, err := m.S.Workflow.GetRun(uctx, workflowapi.GetRunRequest{Id: req.Task})
	if err != nil {
		return magiapi.Task{}, err
	}
	if run.Status != "waiting" {
		return magiapi.Task{}, rpc.Conflict("the task is not waiting for a decision (status %s)", run.Status)
	}
	run, err = m.S.Workflow.Decide(uctx, workflowapi.DecideRequest{Run: run.Id, Step: run.CurrentStep, Outcome: req.Outcome, Comment: req.Comment})
	if err != nil {
		return magiapi.Task{}, err
	}
	return task(run), nil
}

func (m *Service) CancelTask(ctx context.Context, req magiapi.CancelTaskRequest) (magiapi.Task, error) {
	caller, err := rpc.Caller(ctx)
	if err != nil {
		return magiapi.Task{}, err
	}
	// The caller's own token reads the run, so a person can stop only
	// their own tasks.
	run, err := m.S.Workflow.GetRun(AsCaller(ctx), workflowapi.GetRunRequest{Id: req.Task})
	if err != nil {
		return magiapi.Task{}, err
	}
	run, err = m.S.cancelTask(m.S.AsService(ctx), run, caller.Subject, deref(req.Reason), deref(req.Confirm))
	if err != nil {
		return magiapi.Task{}, err
	}
	return task(run), nil
}

func (m *Service) QueryKnowledge(ctx context.Context, req magiapi.QueryKnowledgeRequest) (magiapi.QueryKnowledgeResponse, error) {
	caller, err := rpc.Caller(ctx)
	if err != nil {
		return magiapi.QueryKnowledgeResponse{}, err
	}
	limit := 50
	if req.Limit != nil && *req.Limit > 0 {
		limit = int(min(*req.Limit, 200))
	}
	items, records, err := m.S.knowledgeView(m.S.AsService(ctx), caller.Subject, deref(req.Text), deref(req.Kind), limit, req.IncludeEnded != nil && *req.IncludeEnded)
	return magiapi.QueryKnowledgeResponse{Items: items, Records: records}, err
}

func (m *Service) Remember(ctx context.Context, req magiapi.RememberRequest) (magiapi.RememberResponse, error) {
	caller, err := rpc.Caller(ctx)
	if err != nil {
		return magiapi.RememberResponse{}, err
	}
	sctx := m.S.AsService(ctx)
	f := fact{subject: req.Subject, subjectKind: deref(req.SubjectKind), relation: req.Relation, object: deref(req.Object), objectKind: deref(req.ObjectKind),
		value: deref(req.Value), summary: deref(req.Summary), also: req.Also != nil && *req.Also}
	c, note, err := m.S.remember(sctx, caller.Subject, caller.Name, f, true, nil)
	if err != nil {
		return magiapi.RememberResponse{}, rpc.Invalid("%s", err.Error())
	}
	out := magiapi.RememberResponse{Recorded: c.Id != "", Note: optional(note)}
	subject := c.Subject
	if subject == "" {
		e, err := m.S.entityFor(sctx, caller.Subject, caller.Name, req.Subject, deref(req.SubjectKind), nil)
		if err != nil {
			return out, err
		}
		subject = e.Id
	}
	a, err := m.S.Knowledge.About(sctx, knowledgeapi.AboutRequest{Id: subject, ActFor: &caller.Subject})
	if err != nil {
		return out, err
	}
	out.Item = knowledgeItem(a, caller.Subject, map[string]knowledgeapi.Claim{c.Id: c}, "")
	return out, nil
}

func (m *Service) ChangeKnowledge(ctx context.Context, req magiapi.ChangeKnowledgeRequest) (magiapi.ChangeKnowledgeResponse, error) {
	caller, err := rpc.Caller(ctx)
	if err != nil {
		return magiapi.ChangeKnowledgeResponse{}, err
	}
	sctx := m.S.AsService(ctx)
	r, err := m.S.changeRecord(sctx, caller.Subject, caller.Name, req.Id, req.Operation, deref(req.Field), deref(req.Value), deref(req.Reason),
		req.Erase != nil && *req.Erase, nil)
	if err != nil {
		if rpc.CodeOf(err) == 0 {
			return magiapi.ChangeKnowledgeResponse{}, rpc.Invalid("%s", err.Error())
		}
		return magiapi.ChangeKnowledgeResponse{}, err
	}
	return magiapi.ChangeKnowledgeResponse{Change: knowledgeChange(r.Change), Item: m.S.changedItem(sctx, caller.Subject, r)}, nil
}

func (m *Service) MergeKnowledge(ctx context.Context, req magiapi.MergeKnowledgeRequest) (magiapi.ChangeKnowledgeResponse, error) {
	caller, err := rpc.Caller(ctx)
	if err != nil {
		return magiapi.ChangeKnowledgeResponse{}, err
	}
	sctx := m.S.AsService(ctx)
	c, err := m.S.Knowledge.Merge(sctx, knowledgeapi.MergeRequest{Keep: req.Keep, Merge: req.Merge, Reason: req.Reason, ActFor: &caller.Subject})
	if err != nil {
		return magiapi.ChangeKnowledgeResponse{}, err
	}
	keep := knowledgeapi.Entity{Id: req.Keep}
	return magiapi.ChangeKnowledgeResponse{Change: knowledgeChange(c), Item: m.S.changedItem(sctx, caller.Subject, knowledgeapi.ChangeResponse{Entity: &keep})}, nil
}

func (m *Service) UnmergeKnowledge(ctx context.Context, req magiapi.UnmergeKnowledgeRequest) (magiapi.ChangeKnowledgeResponse, error) {
	caller, err := rpc.Caller(ctx)
	if err != nil {
		return magiapi.ChangeKnowledgeResponse{}, err
	}
	c, err := m.S.Knowledge.Unmerge(m.S.AsService(ctx), knowledgeapi.UnmergeRequest{Change: req.Change, ActFor: &caller.Subject})
	if err != nil {
		return magiapi.ChangeKnowledgeResponse{}, err
	}
	return magiapi.ChangeKnowledgeResponse{Change: knowledgeChange(c)}, nil
}

func (m *Service) KnowledgeHistory(ctx context.Context, req magiapi.KnowledgeHistoryRequest) (magiapi.KnowledgeHistoryResponse, error) {
	caller, err := rpc.Caller(ctx)
	if err != nil {
		return magiapi.KnowledgeHistoryResponse{}, err
	}
	h, err := m.S.Knowledge.History(m.S.AsService(ctx), knowledgeapi.HistoryRequest{Id: req.Id, ActFor: &caller.Subject})
	if err != nil {
		return magiapi.KnowledgeHistoryResponse{}, err
	}
	out := magiapi.KnowledgeHistoryResponse{Changes: []magiapi.KnowledgeChange{}}
	for _, c := range h.Changes {
		out.Changes = append(out.Changes, knowledgeChange(c))
	}
	return out, nil
}

func (m *Service) ListVoices(ctx context.Context, req magiapi.ListVoicesRequest) (magiapi.ListVoicesResponse, error) {
	res, err := m.S.Speech.ListVoices(AsCaller(ctx), speechapi.ListVoicesRequest{Language: req.Language})
	if err != nil {
		return magiapi.ListVoicesResponse{}, err
	}
	out := magiapi.ListVoicesResponse{Voices: []magiapi.VoiceInfo{}}
	for _, v := range res.Voices {
		out.Voices = append(out.Voices, magiapi.VoiceInfo{Id: v.Id, Name: v.Name, Language: v.Language})
	}
	return out, nil
}

func task(r workflowapi.Run) magiapi.Task {
	t := magiapi.Task{Id: r.Id, Kind: r.Definition, Title: r.Vars["title"], Status: r.Status, CurrentStep: r.CurrentStep,
		Branch: optional(r.Vars["branch"]), Repo: optional(r.Vars["repo"]),
		Error: r.Error, CreatedMs: r.CreatedMs, UpdatedMs: r.UpdatedMs, Steps: []magiapi.TaskStep{}, Refs: []magiapi.Ref{}}
	if res := r.Vars["task.result"]; res != "" && r.Status == "succeeded" {
		t.Status = res
	}
	if terminalOpen(r) {
		t.Terminal = ptr(true)
	}
	if r.Status == "waiting" {
		t.WaitingFor = codeChangeDefinition(false).Steps[3].Prompt
		t.Decisions = codeChangeDefinition(false).Steps[3].Outcomes
	}
	for _, st := range r.History {
		ts := magiapi.TaskStep{Step: st.Step, Status: st.Status, Outcome: st.Outcome, StartedMs: st.StartedMs, EndedMs: st.EndedMs, Notes: []string{}}
		for _, p := range st.Progress {
			ts.Notes = append(ts.Notes, p.Note)
		}
		if st.Comment != nil {
			ts.Notes = append(ts.Notes, "Comment: "+*st.Comment)
		}
		t.Steps = append(t.Steps, ts)
	}
	for _, k := range []string{"check.output", "apprentice.transcript"} {
		if v := r.Vars[k]; v != "" {
			t.Refs = append(t.Refs, magiapi.Ref{Kind: "artifact", Ref: v, Title: ptr(k)})
		}
	}
	if c := r.Vars["merge.commit"]; c != "" {
		t.Refs = append(t.Refs, magiapi.Ref{Kind: "commit", Ref: c, Title: ptr("merge")})
	}
	return t
}

func firstLine(s string, n int) string {
	s = strings.TrimSpace(strings.SplitN(s, "\n", 2)[0])
	if len(s) > n {
		s = s[:n] + "..."
	}
	return s
}

// taskOutputs are the outputs of a run that a person can read.
var taskOutputs = []string{"check.output", "apprentice.transcript"}

func (m *Service) GetTaskOutput(ctx context.Context, req magiapi.GetTaskOutputRequest) (magiapi.TaskOutput, error) {
	// Reading the run with the caller's token is the access check.
	run, err := m.S.Workflow.GetRun(AsCaller(ctx), workflowapi.GetRunRequest{Id: req.Id})
	if err != nil {
		return magiapi.TaskOutput{}, err
	}
	return m.S.taskOutput(ctx, run, req.Name, 1<<20)
}

// taskOutput reads output name of run, at most max bytes (the end).
func (s *System) taskOutput(ctx context.Context, run workflowapi.Run, name string, max int) (magiapi.TaskOutput, error) {
	if !slices.Contains(taskOutputs, name) {
		return magiapi.TaskOutput{}, rpc.Invalid("name must be one of %v", taskOutputs)
	}
	digest := run.Vars[name]
	if digest == "" {
		return magiapi.TaskOutput{}, rpc.NotFound("task %s has no %s yet", run.Id, name)
	}
	a, err := s.Artifacts.Get(s.AsService(ctx), artifactsapi.GetRequest{Digest: digest})
	if err != nil {
		return magiapi.TaskOutput{}, err
	}
	out := magiapi.TaskOutput{Task: run.Id, Name: name, MediaType: a.Artifact.MediaType, Text: string(a.Body)}
	if len(out.Text) > max {
		out.Text, out.Truncated = out.Text[len(out.Text)-max:], true
	}
	return out, nil
}
