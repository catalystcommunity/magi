package compose

import (
	"context"
	"slices"

	"github.com/catalystcommunity/magi/lib/rpc"
	magiapi "github.com/catalystcommunity/magi/magi/api"
	workflowapi "github.com/catalystcommunity/magi/primitives/workflow/api"
)

// RoleAdminTasks is the role that reads the runs of all people. A later task
// adds the other roles of the admin views.
const RoleAdminTasks = "admin.tasks"

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
var adminDefinitions = []string{CodeChange, PersonaTurn, Todo}

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

func (m *Service) AdminGetRun(ctx context.Context, req magiapi.AdminGetRunRequest) (magiapi.AdminRun, error) {
	if err := m.S.adminCall(ctx, RoleAdminTasks, "admin-get-run", req.Id); err != nil {
		return magiapi.AdminRun{}, err
	}
	run, err := m.S.Workflow.GetRun(m.S.AsService(ctx), workflowapi.GetRunRequest{Id: req.Id})
	if err != nil {
		return magiapi.AdminRun{}, err
	}
	// vars is never nil: the contract has it, and a run can start with no data.
	vars := run.Vars
	if vars == nil {
		vars = map[string]string{}
	}
	return magiapi.AdminRun{Summary: adminRunSummary(run), Vars: vars, Steps: taskSteps(run)}, nil
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

func (m *Service) AdminGetRunDiff(ctx context.Context, req magiapi.AdminGetRunDiffRequest) (magiapi.TaskDiff, error) {
	if err := m.S.adminCall(ctx, RoleAdminTasks, "admin-get-run-diff", req.Id); err != nil {
		return magiapi.TaskDiff{}, err
	}
	run, err := m.S.Workflow.GetRun(m.S.AsService(ctx), workflowapi.GetRunRequest{Id: req.Id})
	if err != nil {
		return magiapi.TaskDiff{}, err
	}
	return m.S.taskDiff(ctx, run)
}
