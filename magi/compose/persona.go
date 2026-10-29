package compose

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	artifactsapi "github.com/catalystcommunity/magi/primitives/artifacts/api"
	conversationapi "github.com/catalystcommunity/magi/primitives/conversation/api"
	knowledgeapi "github.com/catalystcommunity/magi/primitives/knowledge/api"
	speechapi "github.com/catalystcommunity/magi/primitives/speech/api"
	workflowapi "github.com/catalystcommunity/magi/primitives/workflow/api"

	"github.com/catalystcommunity/magi/magi/model"
)

// PersonaTurn is the workflow that interprets one human utterance and
// replies. A workflow makes each turn durable and inspectable.
const PersonaTurn = "persona-turn"

func personaTurnDefinition() workflowapi.Definition {
	return workflowapi.Definition{
		Name: PersonaTurn, Description: ptr("Magi interprets one utterance and replies."), Start: "reply",
		Steps: []workflowapi.Step{{Id: "reply", Kind: "task", Queue: ptr("magi.persona"), Next: map[string]string{}, MaxAttempts: ptr(uint64(2))}},
	}
}

const personaPrompt = `You are Magi, a personal, household, and work assistant. You talk with %s.
The time is %s.

Rules:
- Answer briefly and plainly. Use short paragraphs. No markdown headings.
- What people say in conversation is evidence, not established fact.
- Never invent details such as phone numbers, addresses, hours, events, or dates. Use only what recall returns or what the person said. Say what you do not know.
- When the person tells you about their things, systems, accounts, calendars, devices, people, or preferences, record it with "remember". Record external records (for example a calendar in some system) with system and locator when they are known, and relate them to the person or to each other.
- When the person asks about their things, use "recall" before you answer. Say when you do not know.
- When the person asks for a change to Magi's own software (the "magi" repository), use "start_code_change" with complete, specific instructions for a coding agent. Then tell the person that the task started. Do not claim the change is done.
- You cannot approve, merge, or release a task. The person decides with the task controls of their client.
- Use "list_tasks" to answer questions about the status of work.`

func personaTools() []model.Tool {
	obj := func(props map[string]any, req ...string) map[string]any {
		return map[string]any{"type": "object", "properties": props, "required": req}
	}
	str := func(d string) map[string]any { return map[string]any{"type": "string", "description": d} }
	return []model.Tool{
		{Name: "recall", Description: "Search the person's knowledge graph.", Parameters: obj(map[string]any{
			"text": str("Words to match in labels, summaries, and properties."), "kind": str("Optional node kind filter, such as calendar or person."),
		}, "text")},
		{Name: "remember", Description: "Record a thing in the person's knowledge graph, with an optional relation to an existing node.", Parameters: obj(map[string]any{
			"kind":       str("Kind of thing: person, calendar, system, account, device, place, preference, fact, project."),
			"label":      str("Short name."),
			"summary":    str("One or two sentences of context."),
			"system":     str("Optional. Name of the external system that holds the record, such as 'Google Workspace (company)'."),
			"locator":    str("Optional. Address or identifier of the record in that system."),
			"related_to": str("Optional. Label or id of an existing node to relate this one to."),
			"relation":   str("Optional. Relation name from the new node to related_to, such as owned-by, part-of, uses."),
		}, "kind", "label", "summary")},
		{Name: "start_code_change", Description: "Start a task in which a coding agent changes a repository. A person reviews it before it is merged.", Parameters: obj(map[string]any{
			"title":        str("Short title of the change."),
			"instructions": str("Complete instructions for the coding agent: what to change, where, and how to verify it."),
			"repo":         str("Repository name. Default: magi."),
		}, "title", "instructions")},
		{Name: "list_tasks", Description: "List recent tasks and their status.", Parameters: obj(map[string]any{})},
	}
}

type turnLog struct {
	Calls []map[string]any `json:"calls"`
	Reply string           `json:"reply"`
	// Models has one entry for each model call: the model that answered,
	// the endpoints skipped before it, and the time.
	Models []modelCall `json:"models"`
}

type modelCall struct {
	Served  string   `json:"served,omitempty"`
	Skipped []string `json:"skipped,omitempty"`
	Ms      int64    `json:"ms"`
	Error   string   `json:"error,omitempty"`
}

func (s *System) personaTurn(ctx context.Context, a workflowapi.Assignment, note func(string)) (Result, error) {
	chat, uttID := a.Vars["chat"], a.Vars["utterance"]
	utt, err := s.Conversation.GetUtterance(ctx, conversationapi.GetUtteranceRequest{Id: uttID})
	if err != nil {
		return Result{}, fmt.Errorf("utterance: %w", err)
	}
	after := uint64(0)
	if utt.Seq > 40 {
		after = utt.Seq - 40
	}
	hist, err := s.Conversation.ListUtterances(ctx, conversationapi.ListUtterancesRequest{Conversation: chat, AfterSeq: ptr(after), Limit: ptr(uint64(40))})
	if err != nil {
		return Result{}, fmt.Errorf("history: %w", err)
	}
	msgs := []model.Message{{Role: "system", Content: fmt.Sprintf(personaPrompt, displayName(a), time.Now().Format("Monday 2 January 2006 15:04 MST"))}}
	for _, u := range hist.Utterances {
		if u.Seq > utt.Seq {
			break
		}
		role := "user"
		if u.AuthorKind != "human" {
			role = "assistant"
		}
		msgs = append(msgs, model.Message{Role: role, Content: u.Text})
	}
	log := turnLog{}
	var refs []conversationapi.Attachment
	var reply string
	for round := 0; round < 6; round++ {
		start := time.Now()
		m, err := s.Model.Chat(ctx, msgs, personaTools(), 1024)
		call := modelCall{Served: m.Served, Skipped: m.Skipped, Ms: time.Since(start).Milliseconds()}
		if err != nil {
			call.Error = err.Error()
		}
		log.Models = append(log.Models, call)
		if err != nil {
			// Tell the person instead of leaving the message without a reply.
			s.Log.Error("persona model", "run", a.Run, "err", err)
			reply = "I could not answer because the model service failed. Please try again."
			break
		}
		// A provider refuses a history that holds tool arguments that are
		// not JSON, so keep "{}" in the history and tell the model.
		bad := map[int]bool{}
		for i, tc := range m.ToolCalls {
			if !json.Valid([]byte(tc.Function.Arguments)) {
				m.ToolCalls[i].Function.Arguments, bad[i] = "{}", true
			}
		}
		msgs = append(msgs, m)
		if len(m.ToolCalls) == 0 {
			reply = strings.TrimSpace(m.Content)
			break
		}
		for i, tc := range m.ToolCalls {
			if bad[i] {
				msgs = append(msgs, model.Message{Role: "tool", ToolCallID: tc.ID, Name: tc.Function.Name,
					Content: "error: the arguments were not valid JSON. Call the tool again with a JSON object."})
				continue
			}
			args := toolArgs(tc.Function.Arguments)
			out, ref := s.personaTool(ctx, a, utt, tc.Function.Name, args)
			if ref != nil {
				refs = append(refs, *ref)
			}
			log.Calls = append(log.Calls, map[string]any{"tool": tc.Function.Name, "args": args, "result": out})
			msgs = append(msgs, model.Message{Role: "tool", ToolCallID: tc.ID, Name: tc.Function.Name, Content: out})
		}
	}
	if reply == "" {
		reply = "I could not finish my answer. Please ask again."
	}
	log.Reply = reply
	served, ms := "", int64(0)
	for _, c := range log.Models {
		ms += c.Ms
		if c.Served != "" {
			served = c.Served
		}
	}
	s.Log.Info("persona turn", "run", a.Run, "model", served, "model_ms", ms, "calls", len(log.Models), "models", log.Models)
	if body, err := json.Marshal(log); err == nil {
		if _, err := s.Conversation.Annotate(ctx, conversationapi.AnnotateRequest{Utterance: uttID, Kind: "interpretation", Body: string(body)}); err != nil {
			s.Log.Warn("annotate", "err", err)
		}
	}
	post := conversationapi.PostRequest{
		Conversation: chat, Text: reply, Author: ptr(Persona), AuthorKind: ptr("persona"),
		ReplyTo: ptr(uttID), Attachments: refs, Modality: ptr("text"),
	}
	if a.Vars["speak"] == "true" {
		if digest, err := s.speak(ctx, a.Owner, reply, a.Vars["voice"]); err != nil {
			s.Log.Warn("speech synthesis", "err", err)
		} else {
			post.Audio, post.Modality = ptr(digest), ptr("voice")
		}
	}
	if _, err := s.Conversation.Post(ctx, post); err != nil {
		return Result{}, fmt.Errorf("post reply: %w", err)
	}
	return Result{Outcome: "done", Outputs: map[string]string{
		"reply.tools": fmt.Sprint(len(log.Calls)), "reply.model": served, "reply.model_ms": fmt.Sprint(ms)}}, nil
}

func (s *System) speak(ctx context.Context, owner, text, voice string) (string, error) {
	req := speechapi.SynthesizeRequest{Text: text}
	if voice != "" {
		req.Voice = ptr(voice)
	}
	audio, err := s.Speech.Synthesize(ctx, req)
	if err != nil {
		return "", err
	}
	art, err := s.Artifacts.Put(ctx, artifactsapi.PutRequest{
		MediaType: audio.MediaType, Body: audio.Body, Owner: ptr(owner),
		Labels: map[string]string{"kind": "speech", "voice": audio.Voice},
	})
	return art.Digest, err
}

func (s *System) personaTool(ctx context.Context, a workflowapi.Assignment, utt conversationapi.Utterance, name string, args map[string]string) (string, *conversationapi.Attachment) {
	switch name {
	case "recall":
		res, err := s.Knowledge.Query(ctx, knowledgeapi.QueryRequest{Text: optional(args["text"]), Kind: optional(args["kind"]), Limit: ptr(uint64(12)), Owner: ptr(a.Owner)})
		if err != nil {
			return "error: " + err.Error(), nil
		}
		if len(res.Nodes) == 0 {
			return "No matching knowledge.", nil
		}
		nodes, edges := res.Nodes, res.Edges
		seen, seenEdge := map[string]bool{}, map[string]bool{}
		for _, n := range res.Nodes {
			seen[n.Id] = true
		}
		for _, e := range res.Edges {
			seenEdge[e.Id] = true
		}
		// Add the records within two edges of the best matches, so that a question
		// about one record also gets the records related to it.
		for _, n := range res.Nodes[:min(len(res.Nodes), 6)] {
			near, err := s.Knowledge.Query(ctx, knowledgeapi.QueryRequest{Near: ptr(n.Id), Depth: ptr(uint64(2)), Limit: ptr(uint64(12)), Owner: ptr(a.Owner)})
			if err != nil {
				continue
			}
			for _, v := range near.Nodes {
				if !seen[v.Id] {
					nodes, seen[v.Id] = append(nodes, v), true
				}
			}
			for _, e := range near.Edges {
				if !seenEdge[e.Id] {
					edges, seenEdge[e.Id] = append(edges, e), true
				}
			}
		}
		for _, n := range nodes {
			if n.External != nil && !seen[n.External.System] {
				if v, err := s.Knowledge.GetNode(ctx, knowledgeapi.GetNodeRequest{Id: n.External.System}); err == nil {
					nodes, seen[v.Node.Id] = append(nodes, v.Node), true
				}
			}
		}
		return describeGraph(nodes, edges), nil
	case "remember":
		n, err := s.rememberNode(ctx, a.Owner, args, "", utt.Id)
		if err != nil {
			return "error: " + err.Error(), nil
		}
		return fmt.Sprintf("Recorded %s %q as %s.", n.Kind, n.Label, n.Id), &conversationapi.Attachment{Kind: "knowledge-node", Ref: n.Id, Title: ptr(n.Label)}
	case "start_code_change":
		run, err := s.startCodeChange(ctx, a.Owner, utt.Conversation, args["title"], args["instructions"], args["repo"])
		if err != nil {
			return "error: " + err.Error(), nil
		}
		return fmt.Sprintf("Task %s started.", run.Id), &conversationapi.Attachment{Kind: "workflow-run", Ref: run.Id, Title: ptr(args["title"])}
	case "list_tasks":
		runs, err := s.Workflow.ListRuns(ctx, workflowapi.ListRunsRequest{Definition: ptr(CodeChange), Limit: ptr(uint64(10))})
		if err != nil {
			return "error: " + err.Error(), nil
		}
		var b strings.Builder
		for _, r := range runs.Runs {
			if r.Owner != a.Owner {
				continue
			}
			fmt.Fprintf(&b, "- %s %q: %s at step %s %s\n", r.Id, r.Vars["title"], r.Status, r.CurrentStep, r.Vars["task.result"])
		}
		if b.Len() == 0 {
			return "No tasks.", nil
		}
		return b.String(), nil
	}
	return "error: unknown tool " + name, nil
}

// rememberNode records a node, its external system, an optional relation,
// and the evidence that supports it.
func (s *System) rememberNode(ctx context.Context, owner string, args map[string]string, authoredBy, evidenceUtterance string) (knowledgeapi.Node, error) {
	req := knowledgeapi.PutNodeRequest{
		Kind: strings.ToLower(args["kind"]), Label: args["label"], Summary: optional(args["summary"]),
		Owner: ptr(owner), Scope: ptr("private"), Confidence: ptr(0.7),
	}
	if authoredBy != "" {
		req.AuthoredBy, req.Confidence = ptr(authoredBy), ptr(0.95)
	}
	var system *knowledgeapi.Node
	if sys := args["system"]; sys != "" {
		n, err := s.findOrCreate(ctx, owner, "system", sys)
		if err != nil {
			return knowledgeapi.Node{}, err
		}
		system = &n
		loc := args["locator"]
		if loc == "" {
			loc = strings.ToLower(args["label"])
		}
		req.External = &knowledgeapi.ExternalRef{System: n.Id, Locator: loc}
	}
	// Update a node with the same label instead of adding a duplicate. A
	// placeholder made by related_to has kind "thing" and takes the new kind.
	if old, ok := s.findByLabel(ctx, owner, req.Label); ok && (old.Kind == req.Kind || old.Kind == "thing") {
		req.Id = &old.Id
		if req.External == nil {
			req.External = old.External
		}
		if req.Summary == nil && old.Summary != "" {
			req.Summary = &old.Summary
		}
	}
	n, err := s.Knowledge.PutNode(ctx, req)
	if err != nil {
		return n, err
	}
	if system != nil {
		s.Knowledge.PutEdge(ctx, knowledgeapi.PutEdgeRequest{FromNode: n.Id, ToNode: system.Id, Relation: "hosted-in", Owner: ptr(owner)})
	}
	if rel := args["related_to"]; rel != "" {
		target, err := s.findOrCreate(ctx, owner, "", rel)
		if err == nil {
			relation := args["relation"]
			if relation == "" {
				relation = "related-to"
			}
			s.Knowledge.PutEdge(ctx, knowledgeapi.PutEdgeRequest{FromNode: n.Id, ToNode: target.Id, Relation: relation, Owner: ptr(owner)})
		}
	}
	if evidenceUtterance != "" {
		s.Knowledge.LinkEvidence(ctx, knowledgeapi.LinkEvidenceRequest{Subject: n.Id, RefKind: "utterance", Ref: evidenceUtterance, Stance: ptr("supports"), Owner: ptr(owner)})
	}
	return n, nil
}

// findOrCreate returns a node by id or label, or creates one with kind.
func (s *System) findOrCreate(ctx context.Context, owner, kind, label string) (knowledgeapi.Node, error) {
	if v, err := s.Knowledge.GetNode(ctx, knowledgeapi.GetNodeRequest{Id: label}); err == nil {
		return v.Node, nil
	}
	if n, ok := s.findByLabel(ctx, owner, label); ok && (kind == "" || n.Kind == kind) {
		return n, nil
	}
	if kind == "" {
		kind = "thing"
	}
	return s.Knowledge.PutNode(ctx, knowledgeapi.PutNodeRequest{Kind: kind, Label: label, Owner: ptr(owner), Scope: ptr("private"), Confidence: ptr(0.6)})
}

// toolArgs reads tool arguments. Values that are not strings are kept as
// their JSON text, so a number or a boolean is not lost.
func toolArgs(raw string) map[string]string {
	var in map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &in) != nil {
		return map[string]string{}
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		var str string
		if json.Unmarshal(v, &str) == nil {
			out[k] = str
		} else if string(v) != "null" {
			out[k] = string(v)
		}
	}
	return out
}

// findByLabel returns the owner's node whose label equals label, ignoring case.
func (s *System) findByLabel(ctx context.Context, owner, label string) (knowledgeapi.Node, bool) {
	res, err := s.Knowledge.Query(ctx, knowledgeapi.QueryRequest{Text: ptr(label), Limit: ptr(uint64(10)), Owner: ptr(owner)})
	if err != nil {
		return knowledgeapi.Node{}, false
	}
	for _, n := range res.Nodes {
		if strings.EqualFold(strings.TrimSpace(n.Label), strings.TrimSpace(label)) {
			return n, true
		}
	}
	return knowledgeapi.Node{}, false
}

func describeGraph(nodes []knowledgeapi.Node, edges []knowledgeapi.Edge) string {
	label := map[string]string{}
	for _, n := range nodes {
		label[n.Id] = n.Label
	}
	var b strings.Builder
	for _, n := range nodes {
		fmt.Fprintf(&b, "- [%s] %s (%s): %s", n.Id, n.Label, n.Kind, n.Summary)
		if n.External != nil {
			fmt.Fprintf(&b, " [in %s at %s]", firstNonEmpty(label[n.External.System], n.External.System), n.External.Locator)
		}
		b.WriteString("\n")
	}
	for _, e := range edges {
		fmt.Fprintf(&b, "- %s %s %s\n", firstNonEmpty(label[e.FromNode], e.FromNode), e.Relation, firstNonEmpty(label[e.ToNode], e.ToNode))
	}
	return b.String()
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// displayName is the name that the person gave LinkKeys, or the subject.
func displayName(a workflowapi.Assignment) string {
	if n := a.Vars["owner_name"]; n != "" {
		return n
	}
	return strings.TrimPrefix(a.Owner, "user:")
}
