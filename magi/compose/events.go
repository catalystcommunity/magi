package compose

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	conversationapi "github.com/catalystcommunity/magi/primitives/conversation/api"
	workflowapi "github.com/catalystcommunity/magi/primitives/workflow/api"
)

// PersonaEvent is the workflow of a persona turn that an event starts
// (docs/decisions/0009): a gate with the loop limit, then the turn.
const PersonaEvent = "persona-event"

// eventRecheck is the time between two looks of the gate at an event that
// waits.
const eventRecheck = time.Minute

// eventMark starts an event in the messages of a turn. The event is not a
// message of the person.
const eventMark = "Event from Magi (not a message of the person): "

// eventRules are added to the prompt of a turn that an event starts.
const eventRules = `

This turn starts from an event, not from a message of the person. The newest message is the event.
- First read the result with a tool. For a task, call "task_status" with the task id of the event.
- Then do one thing: do the next step of what the person asked for before, or tell the person in one or two sentences what happened and what they must decide.
- Do not start the same work again when nothing changed. Do not ask a question that the result answers.`

func personaEventDefinition() workflowapi.Definition {
	return workflowapi.Definition{
		Name: PersonaEvent, Description: ptr("An event starts a persona turn, when the loop limit permits."), Start: "gate",
		Steps: []workflowapi.Step{
			{Id: "gate", Kind: "task", Queue: ptr("magi.event.gate"), Next: map[string]string{"go": "reply", "wait": "hold"}, MaxAttempts: ptr(uint64(3))},
			// An event above the limit waits here. It is not dropped, and
			// it does not wait for a person.
			{Id: "hold", Kind: "human", Prompt: ptr("The event waits for the loop limit."), Outcomes: []string{"check"},
				WaitMs: ptr(uint64(eventRecheck.Milliseconds())), TimeoutOutcome: ptr("check"), Next: map[string]string{"check": "gate"}},
			{Id: "reply", Kind: "task", Queue: ptr("magi.persona"), Next: map[string]string{}, MaxAttempts: ptr(uint64(personaAttempts))},
		},
	}
}

// eventLimits are the numbers of the loop limit.
type eventLimits struct {
	// Window and Rate: no more than Rate event turns of one persona in one
	// conversation start in Window. More events wait.
	Window time.Duration
	Rate   int
	// Chain: no more than Chain events in a row with no message of a
	// person between them. The next event starts no turn.
	Chain int
}

func (s *System) eventLimits() eventLimits {
	l := s.Cfg.Events.eventLimits
	if l.Window <= 0 {
		l.Window = 10 * time.Minute
	}
	if l.Rate <= 0 {
		l.Rate = 6
	}
	if l.Chain <= 0 {
		l.Chain = 4
	}
	return l
}

// eventTitles are the reports of a task that an event can name. The text
// of a report can have text that a task made, so an event does not have
// it: the persona reads the result with a tool, as data.
var eventTitles = map[string]string{
	"Ready for review": "is ready for review",
	"Task stopped":     "stopped",
	"Task blocked":     "is blocked: the apprentice stopped before the work was complete",
	"Merge failed":     "could not be merged",
	"Release failed":   "failed in the release",
	"Push failed":      "could not be pushed",
	"Not pushed":       "was not pushed",
	"Pull request":     "has an open pull request",
	"Closed":           "has a closed pull request",
}

// eventRuns lists the event runs of owner.
func (s *System) eventRuns(ctx context.Context, owner string) ([]workflowapi.Run, error) {
	var out []workflowapi.Run
	seen := map[string]bool{}
	for _, st := range []string{"running", "waiting", "running", "succeeded", "failed", "cancelled"} {
		res, err := s.Workflow.ListRuns(ctx, workflowapi.ListRunsRequest{Definition: ptr(PersonaEvent), Status: ptr(st), Limit: ptr(uint64(200))})
		if err != nil {
			return nil, err
		}
		for _, r := range res.Runs {
			if r.Owner == owner && !seen[r.Id] {
				seen[r.Id] = true
				out = append(out, r)
			}
		}
	}
	return out, nil
}

// wake starts a turn of the persona that started the work of a, for an
// event of that work. report is the title of the report of the task. The
// turn acts with the dispatch of the work. A run with no persona, or of no
// person, starts no turn.
func (s *System) wake(ctx context.Context, a workflowapi.Assignment, report string) {
	if what, ok := eventTitles[report]; ok {
		s.wakeFor(ctx, a, report, what)
	}
}

// wakeFor is wake for an event that is not a report of a step: what is the
// end of the sentence "Task ... ...", and report is the name of the event
// (one turn for each name of one run).
func (s *System) wakeFor(ctx context.Context, a workflowapi.Assignment, report, what string) {
	if s.Cfg.Events.Off {
		return
	}
	au, err := s.restore(a.Owner, a.Vars)
	stream := a.Vars["stream"]
	if err != nil || au.Persona == "" || stream == "" || !(principal{subject: au.Principal}).person() {
		return
	}
	ctx = s.AsService(ctx)
	runs, err := s.eventRuns(ctx, a.Owner)
	if err != nil {
		s.Log.Warn("event: list", "run", a.Run, "err", err)
		return
	}
	// Each event of one piece of work makes the chain longer, and so does
	// each piece of work that an event turn started.
	earlier := 0
	for _, r := range runs {
		if r.Vars["event.source"] == a.Run {
			earlier++
		}
	}
	d := au.child(stream, "")
	d.Chain = au.Chain + 1 + earlier
	if d.Chain > s.eventLimits().Chain+1 {
		// The gate stopped this chain already, with its notice.
		return
	}
	key := fmt.Sprintf("event:%s/%s/%d", a.Run, report, earlier)
	if a.Claim != "" {
		// One event for one report of one claim of a step, also when the
		// step sends the report again.
		key = fmt.Sprintf("event:%s/%s/%s", a.Run, report, a.Claim)
	}
	text := fmt.Sprintf("Task %s %q %s.", a.Run, clip(neutralize(a.Vars["title"]), 120), what)
	run := conversationapi.Ref{Kind: "workflow-run", Ref: a.Run, Title: optional(a.Vars["title"])}
	u, err := s.Conversation.Post(ctx, conversationapi.PostRequest{
		Stream: stream, Text: text, Author: ptr("magi"), AuthorKind: ptr("system"), Modality: ptr("text"),
		Attachments: []conversationapi.Ref{run}, Links: []conversationapi.NewLink{{Target: run, Kind: "reports-on"}}, Key: &key,
	})
	if err != nil {
		s.Log.Warn("event: post", "run", a.Run, "err", err)
		return
	}
	d.Cause = u.Id
	vars := map[string]string{"stream": stream, "utterance": u.Id, "persona": au.Persona, dispatchVar: d.encode(),
		"event.source": a.Run, "event.report": report, "event.title": a.Vars["title"]}
	if n := a.Vars["owner_name"]; n != "" {
		vars["owner_name"] = n
	}
	if _, err := s.Workflow.Start(ctx, workflowapi.StartRequest{Definition: PersonaEvent, Vars: vars, Owner: ptr(a.Owner), Key: &key}); err != nil {
		s.Log.Warn("event: start", "run", a.Run, "err", err)
	}
}

// eventGate is the loop limit (docs/decisions/0009). "go" starts the turn.
// "wait" holds the event until the window has room. A chain that is too
// long ends the run with one notice: the next step needs a person.
func (s *System) eventGate(ctx context.Context, a workflowapi.Assignment, _ func(string)) (Result, error) {
	au, err := s.restore(a.Owner, a.Vars)
	if err != nil {
		return Result{}, err
	}
	lim := s.eventLimits()
	if au.Chain > lim.Chain {
		_, err := s.sendNotice(ctx, noticeReq{owner: a.Owner, persona: au.Persona, stream: a.Vars["stream"], urgency: "normal",
			title: "I stopped here",
			text:  fmt.Sprintf("I followed up on this work %d times with no message from you. I stopped, so that I do not go around in a circle. Tell me what to do next.", lim.Chain),
			key:   "event-chain:" + a.Run})
		if err != nil {
			s.Log.Warn("event: notice of the chain limit", "run", a.Run, "err", err)
		}
		return Result{Outcome: "stopped", Outputs: map[string]string{"event.stopped": "chain"}}, nil
	}
	runs, err := s.eventRuns(ctx, a.Owner)
	if err != nil {
		return Result{}, err
	}
	now := time.Now()
	from := now.Add(-lim.Window).UnixMilli()
	started, noticed := 0, false
	for _, r := range runs {
		if r.Id == a.Run || r.Vars["stream"] != a.Vars["stream"] || r.Vars["persona"] != a.Vars["persona"] {
			continue
		}
		if ms, _ := strconv.ParseInt(r.Vars["event.go_ms"], 10, 64); ms >= from {
			started++
		}
		if ms, _ := strconv.ParseInt(r.Vars["event.noticed_ms"], 10, 64); ms >= from {
			noticed = true
		}
	}
	if started < lim.Rate {
		return Result{Outcome: "go", Outputs: map[string]string{"event.go_ms": strconv.FormatInt(now.UnixMilli(), 10)}}, nil
	}
	out := map[string]string{"event.held": "true"}
	// One notice in a window, for all the events that wait.
	if !noticed && a.Vars["event.noticed_ms"] == "" {
		_, err := s.sendNotice(ctx, noticeReq{owner: a.Owner, persona: au.Persona, stream: a.Vars["stream"], urgency: "low",
			title: "Many events",
			text: fmt.Sprintf("%d events started work for me in %s. The next ones wait, so that I do not go around in a circle. They continue by themselves.",
				started, strings.TrimSuffix(lim.Window.Round(time.Minute).String(), "0s"))})
		if err != nil {
			s.Log.Warn("event: notice of the rate limit", "run", a.Run, "err", err)
		}
		out["event.noticed_ms"] = strconv.FormatInt(now.UnixMilli(), 10)
	}
	return Result{Outcome: "wait", Outputs: out}, nil
}
