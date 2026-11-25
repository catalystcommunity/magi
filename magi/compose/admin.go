package compose

import (
	"context"
	"slices"
	"unicode/utf8"

	"github.com/catalystcommunity/magi/lib/rpc"
	magiapi "github.com/catalystcommunity/magi/magi/api"
	conversationapi "github.com/catalystcommunity/magi/primitives/conversation/api"
	workflowapi "github.com/catalystcommunity/magi/primitives/workflow/api"
)

// RoleAdminTasks is the role that reads the runs of all people. A later task
// adds the other roles of the admin views.
const RoleAdminTasks = "admin.tasks"

// RoleAdminChats is the role that reads the chats and turn logs of all
// people.
const RoleAdminChats = "admin.chats"

// adminCall checks that the caller holds role for op on target, and logs the
// call. It runs before any primitive call, so a caller that fails it reads
// nothing. For now the operator check stands in for the role check; a later
// task reads the roles of the caller from the database and replaces it.
func (s *System) adminCall(ctx context.Context, role, op, target string) error {
	if err := s.isOperator(ctx); err != nil {
		return err
	}
	p, err := rpc.Caller(ctx)
	if err != nil {
		return err
	}
	s.Log.Info("admin call", "role", role, "op", op, "target", target, "by", p.Subject)
	return nil
}

// adminDefinitions are the run definitions that an admin can list.
var adminDefinitions = []string{CodeChange, Investigation, PersonaTurn, Todo}

// adminStatuses are the run statuses that an admin can filter by.
var adminStatuses = []string{"running", "waiting", "succeeded", "failed", "cancelled"}

// adminRunSummary is the summary of a run for the admin views.
func adminRunSummary(r workflowapi.Run) magiapi.AdminRunSummary {
	return magiapi.AdminRunSummary{Id: r.Id, Definition: r.Definition, Owner: r.Owner, Status: r.Status, CurrentStep: r.CurrentStep,
		Title: optional(r.Vars["title"]), Error: r.Error, CreatedMs: r.CreatedMs, UpdatedMs: r.UpdatedMs}
}

func (m *Service) AdminListRuns(ctx context.Context, req magiapi.AdminListRunsRequest) (magiapi.AdminListRunsResponse, error) {
	if err := m.S.adminCall(ctx, RoleAdminTasks, "admin-list-runs", deref(req.Definition)); err != nil {
		return magiapi.AdminListRunsResponse{}, err
	}
	definition := deref(req.Definition)
	if definition != "" && !slices.Contains(adminDefinitions, definition) {
		return magiapi.AdminListRunsResponse{}, rpc.Invalid("definition must be one of %v", adminDefinitions)
	}
	status := deref(req.Status)
	if status != "" && !slices.Contains(adminStatuses, status) {
		return magiapi.AdminListRunsResponse{}, rpc.Invalid("status must be one of %v", adminStatuses)
	}
	limit := uint64(50)
	if l := deref(req.Limit); l > 0 {
		limit = min(l, 500)
	}
	res, err := m.S.Workflow.ListRuns(m.S.AsService(ctx), workflowapi.ListRunsRequest{Definition: req.Definition, Status: req.Status, Limit: ptr(limit)})
	if err != nil {
		return magiapi.AdminListRunsResponse{}, err
	}
	out := magiapi.AdminListRunsResponse{Runs: []magiapi.AdminRunSummary{}}
	for _, r := range res.Runs {
		out.Runs = append(out.Runs, adminRunSummary(r))
	}
	return out, nil
}

// adminVarMax is the most bytes of one run var that the admin view sends.
// The run data holds large texts (instructions, plan, lessons), and the view
// has no use for more than a screen of each.
const adminVarMax = 4096

// adminVars copies the vars of run and cuts each value at adminVarMax bytes.
// A cut value ends with a mark, so the reader knows that it is not whole.
func adminVars(r workflowapi.Run) map[string]string {
	out := make(map[string]string, len(r.Vars))
	for k, v := range r.Vars {
		if len(v) > adminVarMax {
			// Cut on a rune boundary: the codec sends valid UTF-8 only.
			v = v[:adminVarMax]
			for len(v) > 0 && !utf8.RuneStart(v[len(v)-1]) {
				v = v[:len(v)-1]
			}
			v += "… (cut at 4 KiB)"
		}
		out[k] = v
	}
	return out
}

func (m *Service) AdminGetRun(ctx context.Context, req magiapi.AdminGetRunRequest) (magiapi.AdminRun, error) {
	if err := m.S.adminCall(ctx, RoleAdminTasks, "admin-get-run", req.Id); err != nil {
		return magiapi.AdminRun{}, err
	}
	run, err := m.S.Workflow.GetRun(m.S.AsService(ctx), workflowapi.GetRunRequest{Id: req.Id})
	if err != nil {
		return magiapi.AdminRun{}, err
	}
	return magiapi.AdminRun{Summary: adminRunSummary(run), Vars: adminVars(run), Steps: taskSteps(run)}, nil
}

func (m *Service) AdminGetRunOutput(ctx context.Context, req magiapi.AdminGetRunOutputRequest) (magiapi.TaskOutput, error) {
	if err := m.S.adminCall(ctx, RoleAdminTasks, "admin-get-run-output", req.Id); err != nil {
		return magiapi.TaskOutput{}, err
	}
	run, err := m.S.Workflow.GetRun(m.S.AsService(ctx), workflowapi.GetRunRequest{Id: req.Id})
	if err != nil {
		return magiapi.TaskOutput{}, err
	}
	return m.S.taskOutput(ctx, run, req.Name, 1<<20)
}

func (m *Service) AdminGetRunDiff(ctx context.Context, req magiapi.AdminGetRunRequest) (magiapi.TaskDiff, error) {
	if err := m.S.adminCall(ctx, RoleAdminTasks, "admin-get-run-diff", req.Id); err != nil {
		return magiapi.TaskDiff{}, err
	}
	run, err := m.S.Workflow.GetRun(m.S.AsService(ctx), workflowapi.GetRunRequest{Id: req.Id})
	if err != nil {
		return magiapi.TaskDiff{}, err
	}
	return m.S.taskDiff(ctx, run)
}

// adminChat is the chat row of a stream for the admin views.
func adminChat(st conversationapi.Stream) magiapi.AdminChat {
	return magiapi.AdminChat{Stream: st.Id, Person: st.Person, Persona: st.Persona, LastSeq: st.LastSeq,
		CreatedMs: st.CreatedMs, UpdatedMs: st.UpdatedMs}
}

// The default and the maximum number of messages that admin-read-chat
// returns.
const (
	adminChatDefault = 50
	adminChatMax     = 200
)

// AdminListChats lists the chats of all people, newest first. With person or
// persona, only those chats.
func (m *Service) AdminListChats(ctx context.Context, req magiapi.AdminListChatsRequest) (magiapi.AdminListChatsResponse, error) {
	if err := m.S.adminCall(ctx, RoleAdminChats, "admin-list-chats", deref(req.Person)); err != nil {
		return magiapi.AdminListChatsResponse{}, err
	}
	sctx := m.S.AsService(ctx)
	list := conversationapi.ListStreamsRequest{All: ptr(true)}
	if person := deref(req.Person); person != "" {
		list = conversationapi.ListStreamsRequest{Person: req.Person}
	}
	res, err := m.S.Conversation.ListStreams(sctx, list)
	if err != nil {
		return magiapi.AdminListChatsResponse{}, err
	}
	out := magiapi.AdminListChatsResponse{Chats: []magiapi.AdminChat{}}
	for _, st := range res.Streams {
		if persona := deref(req.Persona); persona != "" && st.Persona != persona {
			continue
		}
		out.Chats = append(out.Chats, adminChat(st))
	}
	return out, nil
}

// AdminReadChat reads the newest messages of a chat, or the ones before
// before_seq (for scrolling back).
func (m *Service) AdminReadChat(ctx context.Context, req magiapi.AdminReadChatRequest) (magiapi.AdminReadChatResponse, error) {
	if err := m.S.adminCall(ctx, RoleAdminChats, "admin-read-chat", req.Stream); err != nil {
		return magiapi.AdminReadChatResponse{}, err
	}
	sctx := m.S.AsService(ctx)
	st, err := m.S.Conversation.GetStream(sctx, conversationapi.GetStreamRequest{Id: req.Stream})
	if err != nil {
		return magiapi.AdminReadChatResponse{}, err
	}
	limit := uint64(adminChatDefault)
	if l := deref(req.Limit); l > 0 {
		limit = min(l, adminChatMax)
	}
	// Without before_seq, the newest messages: the ones before last_seq+1.
	before := deref(req.BeforeSeq)
	if before == 0 {
		before = st.LastSeq + 1
	}
	res, err := m.S.Conversation.ListUtterances(sctx, conversationapi.ListUtterancesRequest{
		Stream: st.Id, BeforeSeq: ptr(before), Limit: ptr(limit)})
	if err != nil {
		return magiapi.AdminReadChatResponse{}, err
	}
	msgs, err := m.S.messages(sctx, res.Utterances, res.Links, false)
	if err != nil {
		return magiapi.AdminReadChatResponse{}, err
	}
	return magiapi.AdminReadChatResponse{Chat: adminChat(st), Messages: msgs}, nil
}

// AdminTurnLog returns the turn log of the persona reply to message: the
// body of the newest interpretation annotation of that message.
func (m *Service) AdminTurnLog(ctx context.Context, req magiapi.AdminTurnLogRequest) (magiapi.AdminTurnLogResponse, error) {
	if err := m.S.adminCall(ctx, RoleAdminChats, "admin-turn-log", req.Message); err != nil {
		return magiapi.AdminTurnLogResponse{}, err
	}
	res, err := m.S.Conversation.ListAnnotations(m.S.AsService(ctx), conversationapi.ListAnnotationsRequest{Utterance: req.Message})
	if err != nil {
		return magiapi.AdminTurnLogResponse{}, err
	}
	out := magiapi.AdminTurnLogResponse{Message: req.Message}
	// The store returns the annotations oldest first, so the last match is
	// the newest one.
	for _, an := range res.Annotations {
		if an.Kind == "interpretation" {
			out.Log = ptr(an.Body)
		}
	}
	return out, nil
}
