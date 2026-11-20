package compose

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	executionapi "github.com/catalystcommunity/magi/primitives/execution/api"
	workflowapi "github.com/catalystcommunity/magi/primitives/workflow/api"
)

// dispatchVar is the variable of a run that has its dispatch. It is a
// variable that the run starts with, so no step can change it (the
// workflow service refuses that).
const dispatchVar = "dispatch"

// dispatch is the authority of work that continues after the turn or the
// request that started it (docs/decisions/0007): a task, a to-do, a
// reminder, a persona turn. Code writes it when the work starts and reads
// it when the work continues. The thing that wakes the work (an event, a
// runner, a model text) gives no authority.
type dispatch struct {
	// Principal is the subject for whom the work acts.
	Principal string `json:"principal"`
	// Persona started the work ("" when a person started it directly).
	Persona string `json:"persona,omitempty"`
	Stream  string `json:"stream,omitempty"`
	// Cause is the message or the event that started the work.
	Cause string `json:"cause,omitempty"`
	// Tools are the tools that the persona had at the start.
	Tools []string `json:"tools,omitempty"`
	// Grants are the placeholders of the egress secret rules that applied
	// at the start.
	Grants []string `json:"grants,omitempty"`
	// Chain is the number of events between a message of a person and
	// this work (docs/decisions/0009).
	Chain int   `json:"chain,omitempty"`
	AtMs  int64 `json:"at_ms"`
}

func (d dispatch) encode() string {
	b, _ := json.Marshal(d)
	return string(b)
}

// authority is a dispatch narrowed by the policy now. Tools and Grants are
// those that the dispatch had and the policy still gives.
type authority struct {
	dispatch
	// revoked are the grants of the dispatch that the policy took away.
	revoked []string
}

// errNoDispatch: there is no fallback identity for work with no dispatch.
var errNoDispatch = errors.New("the work has no dispatch: it does not run")

// grantsNow gives the placeholders of the secret rules that the policy
// gives now to a task of owner that persona started.
func (s *System) grantsNow(owner, persona string) []string {
	out := []string{}
	if s.EgressRules == nil {
		return out
	}
	for _, r := range s.EgressRules.Secrets {
		if (len(r.Owners) > 0 && !slices.Contains(r.Owners, owner)) ||
			(len(r.Personas) > 0 && !slices.Contains(r.Personas, personaOr(&persona))) {
			continue
		}
		out = append(out, r.Placeholder)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// toolsNow gives the tools that persona has now.
func toolsNow(persona string) []string {
	if persona == "" {
		return nil
	}
	return slices.Clone(profileFor(persona).tools)
}

// newDispatch makes the dispatch of work that principal starts now, with
// the policy now. persona is "" when a person starts the work directly.
func (s *System) newDispatch(principal, persona, stream, cause string) dispatch {
	return dispatch{Principal: principal, Persona: persona, Stream: stream, Cause: cause,
		Tools: toolsNow(persona), Grants: s.grantsNow(principal, persona), AtMs: time.Now().UnixMilli()}
}

// child makes the dispatch of work that work with authority au starts: the
// same principal and persona, and no more than au has now.
func (au authority) child(stream, cause string) dispatch {
	return dispatch{Principal: au.Principal, Persona: au.Persona, Stream: firstNonEmpty(stream, au.Stream), Cause: cause,
		Tools: slices.Clone(au.Tools), Grants: slices.Clone(au.Grants), Chain: au.Chain, AtMs: time.Now().UnixMilli()}
}

func both(a, b []string) (in, out []string) {
	for _, x := range a {
		if slices.Contains(b, x) {
			in = append(in, x)
		} else {
			out = append(out, x)
		}
	}
	return in, out
}

// restore reads the dispatch of a run and narrows it by the policy now.
// owner is the owner of the run, which the workflow service set. An error
// means that the work must not run.
func (s *System) restore(owner string, vars map[string]string) (authority, error) {
	raw := vars[dispatchVar]
	if raw == "" {
		return authority{}, errNoDispatch
	}
	var d dispatch
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		return authority{}, fmt.Errorf("the dispatch of the work cannot be read: %w", err)
	}
	if d.Principal == "" || d.Principal != owner {
		return authority{}, fmt.Errorf("the dispatch is for %q and the work is of %q: it does not run", d.Principal, owner)
	}
	if p := vars["persona"]; p != "" && d.Persona != "" && p != d.Persona {
		return authority{}, fmt.Errorf("the dispatch is of %s and the work names %s: it does not run", d.Persona, p)
	}
	if s.EgressRules == nil && len(d.Grants) > 0 {
		return authority{}, errors.New("the work started with grants and the policy cannot be read now: it does not run")
	}
	au := authority{dispatch: d}
	au.Tools, _ = both(d.Tools, toolsNow(d.Persona))
	au.Grants, au.revoked = both(d.Grants, s.grantsNow(d.Principal, d.Persona))
	if len(au.revoked) > 0 {
		return au, fmt.Errorf("a grant that the work started with was revoked (%s): it does not continue", strings.Join(au.revoked, ", "))
	}
	return au, nil
}

// childDispatch gives the dispatch for work that the work of a starts, as
// the value of dispatchVar.
func (s *System) childDispatch(a workflowapi.Assignment, stream, cause string) (string, error) {
	au, err := s.restore(a.Owner, a.Vars)
	if err != nil {
		return "", err
	}
	return au.child(stream, cause).encode(), nil
}

// has is true when the authority has the grant with placeholder.
func (au authority) has(placeholder string) bool { return slices.Contains(au.Grants, placeholder) }

// permitted is the check of each step that a worker of Magi or a runner
// claims: the dispatch of the run permits the step, or the run stops.
func (s *System) permitted(ctx context.Context, a workflowapi.Assignment) bool {
	if _, err := s.restore(a.Owner, a.Vars); err != nil {
		s.refuseWork(ctx, a, err)
		return false
	}
	return true
}

// refuseWork stops a run whose dispatch does not permit it to continue,
// and tells its owner one time. A refusal is not a failure to try again.
func (s *System) refuseWork(ctx context.Context, a workflowapi.Assignment, why error) {
	s.Log.Warn("work refused", "run", a.Run, "step", a.Step, "err", why)
	reason := "Magi stopped this work: " + why.Error()
	run, err := s.Workflow.Cancel(ctx, workflowapi.CancelRequest{Id: a.Run, Reason: &reason})
	if err != nil {
		s.Log.Error("cancel refused work", "run", a.Run, "err", err)
		return
	}
	// A runner removes its own environments when it sees that the run ended.
	if id := run.Vars["environment"]; id != "" && run.Vars["runner"] == "" && s.Execution != nil {
		if _, err := s.Execution.Destroy(ctx, executionapi.DestroyRequest{Id: id}); err != nil {
			s.Log.Warn("destroy environment", "env", id, "err", err)
		}
	}
	if !(principal{subject: run.Owner}).person() {
		return
	}
	title := "Work stopped"
	if t := run.Vars["title"]; t != "" {
		title += ": " + clip(t, 100)
	}
	// The notice names the owner of the run and nothing from the dispatch:
	// the dispatch is what could not be trusted.
	if _, err := s.sendNotice(ctx, noticeReq{owner: run.Owner, persona: Persona, title: title,
		text: reason + ". Start it again if you still want it.", urgency: "normal"}); err != nil {
		s.Log.Warn("notice of refused work", "run", a.Run, "err", err)
	}
}
