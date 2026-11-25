package compose

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	conversationapi "github.com/catalystcommunity/magi/primitives/conversation/api"
	workflowapi "github.com/catalystcommunity/magi/primitives/workflow/api"
)

// toolRule is what toolGate knows of a tool. Each tool of toolDefs has a
// rule (a test checks this), so a new tool cannot leave the gate out.
type toolRule struct {
	// person: the tool acts for a person, so the turn needs one.
	person bool
	// effect: the tool changes something that stays after the turn.
	effect bool
}

var (
	readRule   = toolRule{person: true}
	writeRule  = toolRule{person: true, effect: true}
	publicRule = toolRule{}
)

// toolRules has the rule of each tool.
var toolRules = map[string]toolRule{
	"recall": readRule, "query_knowledge": readRule, "search_conversation": readRule, "remember": writeRule, "change_knowledge": writeRule,
	"start_code_change": writeRule, "investigate": writeRule, "decide_task": writeRule, "cancel_task": writeRule,
	"magi_system": readRule,
	"list_tasks":  readRule, "task_status": readRule, "task_diff": readRule, "terminal_link": readRule, "list_runners": readRule, "list_models": readRule,
	"send_notice": writeRule, "list_devices": readRule,
	"web_search": publicRule, "read_page": publicRule,
	"add_todo": writeRule, "set_reminder": writeRule, "list_todos": readRule, "finish_todo": writeRule, "change_todo": writeRule,
	"make_page": writeRule, "connect_account": writeRule,
	"calendar_events": readRule, "find_events": readRule, "free_busy": readRule,
	"create_event": writeRule, "change_event": writeRule, "cancel_event": writeRule,
	"longhouse_projects": readRule, "longhouse_tasks": readRule,
	"add_longhouse_task": writeRule, "update_longhouse_task": writeRule, "add_longhouse_project": writeRule,
}

// maxToolResult is the most bytes of a tool result that a model gets. The
// largest results that the tools make (a page, a patch with its file list)
// are below it; a result above it is a fault or an attack on the context.
const maxToolResult = 16000

// refusalMark starts each refusal of the gate.
const refusalMark = "Refused: "

// refusal is the text for the model when code does not let an action
// occur (docs/decisions/0005): what did not occur, the rule in one
// sentence, and what the model can do next.
func refusal(what, rule, next string) string {
	return refusalMark + what + "\nRule: " + rule + "\nNext: " + next
}

// toolFailed is true for a result that says that the tool did not do its
// work.
func toolFailed(out string) bool {
	return strings.HasPrefix(out, "error") || strings.HasPrefix(out, "Not recorded") || strings.HasPrefix(out, refusalMark)
}

// toolDone is true for a result that says that the tool did its work. A
// request for a confirmation is not a failure, and it is not the work.
func toolDone(out string) bool {
	return !toolFailed(out) && !strings.HasPrefix(out, "confirmation needed")
}

// annEffect is the annotation kind of an effect of a turn. The body is an
// effect.
const annEffect = "effect"

// effect is one action of a turn that changed something
// (docs/decisions/0004). The turn keeps its effects on its utterance. A
// turn that starts again, or a model that gives the same call two times,
// gets the result of the first time, and the action does not run again.
type effect struct {
	Key    string               `json:"key"`
	Tool   string               `json:"tool"`
	Args   map[string]string    `json:"args"`
	Result string               `json:"result"`
	Ref    *conversationapi.Ref `json:"ref,omitempty"`
	By     string               `json:"by,omitempty"`
}

// effectKey names an effect: the tool and its arguments. The same call
// has the same key in each start of the turn.
func effectKey(tool string, args map[string]string) string {
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	h.Write([]byte(tool))
	for _, k := range keys {
		fmt.Fprintf(h, "\x00%s\x00%s", k, args[k])
	}
	return hex.EncodeToString(h.Sum(nil))[:24]
}

// repeatMark starts the result of an effect that did not run again.
const repeatMark = "This action is complete in this turn. It did not run again. Its result: "

// annReplied marks an utterance whose turn posted its reply. The body is
// the id of the reply.
const annReplied = "replied"

// loadEffects reads what an earlier start of the turn did: its effects,
// and if it posted the reply.
func (s *System) loadEffects(ctx context.Context, utterance string) (effects map[string]effect, replied bool) {
	effects = map[string]effect{}
	if s.Conversation == nil {
		return effects, false
	}
	res, err := s.Conversation.ListAnnotations(ctx, conversationapi.ListAnnotationsRequest{Utterance: utterance})
	if err != nil {
		s.Log.Warn("read the effects of the turn", "utterance", utterance, "err", err)
		return effects, false
	}
	for _, a := range res.Annotations {
		var e effect
		switch {
		case a.Kind == annReplied:
			replied = true
		case a.Kind == annEffect && json.Unmarshal([]byte(a.Body), &e) == nil && e.Key != "":
			effects[e.Key] = e
		}
	}
	return effects, replied
}

// effectsOf reads only the effects.
func (s *System) effectsOf(ctx context.Context, utterance string) map[string]effect {
	effects, _ := s.loadEffects(ctx, utterance)
	return effects
}

// sortedEffects gives the effects in a stable order.
func sortedEffects(m map[string]effect) []effect {
	out := make([]effect, 0, len(m))
	for _, e := range m {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Tool+out[i].Key < out[j].Tool+out[j].Key })
	return out
}

// keepEffect stores an effect on the utterance of the turn.
func (s *System) keepEffect(ctx context.Context, t *toolTurn, e effect) {
	if t.ts.effects == nil {
		t.ts.effects = map[string]effect{}
	}
	t.ts.effects[e.Key] = e
	if s.Conversation == nil || t.utt.Id == "" {
		return
	}
	body, err := json.Marshal(e)
	if err != nil {
		return
	}
	if _, err := s.Conversation.Annotate(ctx, conversationapi.AnnotateRequest{Utterance: t.utt.Id, Kind: annEffect, Body: string(body)}); err != nil {
		s.Log.Warn("keep the effect of the turn", "utterance", t.utt.Id, "tool", e.Tool, "err", err)
	}
}

// principal is the one identity that a turn acts for
// (docs/decisions/0008). Code selects it from verified facts: the owner of
// the run, which the workflow service set from the token of the request,
// and the author of the message. Text never changes it.
type principal struct{ subject string }

// person is true when the turn acts for a person. A turn of a device with
// no identity, or of a service, has no person: it gets only the tools
// that need none.
func (p principal) person() bool { return strings.HasPrefix(p.subject, "user:") }

// turnPrincipal gives the principal of the turn of a message. The message
// of a person starts a turn only for that person: a run whose owner is
// not the author of the message does not act. There is no fallback
// identity.
func turnPrincipal(a workflowapi.Assignment, utt conversationapi.Utterance) (principal, error) {
	if utt.AuthorKind == "human" && utt.Author != a.Owner {
		return principal{}, fmt.Errorf("the turn is for %s and the message is of %s: one turn acts for one principal", a.Owner, utt.Author)
	}
	return principal{subject: a.Owner}, nil
}

// toolTurn is the part of a persona turn that the gate needs.
type toolTurn struct {
	a   workflowapi.Assignment
	as  conversationLens
	utt conversationapi.Utterance
	ts  *turnState
	// tools are the tools of the persona in this turn.
	tools []string
	// by names the caller when it is not the persona model ("note-taker").
	by string
}

// toolResult is one tool call that went through the gate.
type toolResult struct {
	Out     string
	Ref     *conversationapi.Ref
	Refused bool
	// Call is the record of the call for the turn log.
	Call map[string]any
}

// gateArgs reads the arguments that a model gave. They must be one JSON
// object.
func gateArgs(raw string) (map[string]string, bool) {
	if strings.TrimSpace(raw) == "" {
		return map[string]string{}, true
	}
	var in map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &in) != nil || in == nil {
		return nil, false
	}
	return toolArgs(raw), true
}

// toolGate runs one tool call of a persona turn (docs/decisions/0003). It
// is the only caller of the tool handlers. Steps: find the tool, check
// the arguments, check the authority, run, limit the result, record. The
// caller puts Out in the fence of the turn. args is nil when the
// arguments were not valid.
func (s *System) toolGate(ctx context.Context, t *toolTurn, name string, args map[string]string) toolResult {
	start := time.Now()
	out, ref, refused := s.gated(ctx, t, name, args)
	// A handler refuses with the same form as the gate.
	refused = refused || strings.HasPrefix(out, refusalMark)
	out = limitResult(out)
	call := map[string]any{"tool": name, "args": args, "result": out, "ms": time.Since(start).Milliseconds()}
	if refused {
		call["refused"] = true
	}
	if t.by != "" {
		call["by"] = t.by
	}
	return toolResult{Out: out, Ref: ref, Refused: refused, Call: call}
}

// limitResult cuts a result that is above maxToolResult, between two
// characters, and says so.
func limitResult(out string) string {
	n := len(out)
	if n <= maxToolResult {
		return out
	}
	cut := maxToolResult
	for cut > 0 && !utf8.RuneStart(out[cut]) {
		cut--
	}
	return out[:cut] + fmt.Sprintf("\n[Magi cut this result: it had %d bytes and the limit is %d. Ask for a smaller part.]", n, maxToolResult)
}

func (s *System) gated(ctx context.Context, t *toolTurn, name string, args map[string]string) (out string, ref *conversationapi.Ref, refused bool) {
	rule, known := toolRules[name]
	if !known || !slices.Contains(t.tools, name) {
		return refusal(fmt.Sprintf("the tool %q did not run.", name),
			"A persona can call only the tools that it has in this turn.",
			"Use one of your tools: "+strings.Join(t.tools, ", ")+". If none fits, tell the person what you cannot do."), nil, true
	}
	if args == nil {
		return refusal(fmt.Sprintf("the tool %q did not run.", name),
			"The arguments of a tool call are one JSON object.",
			"Call "+name+" again with a JSON object that has the arguments of its description."), nil, true
	}
	if rule.person && !(principal{subject: t.a.Owner}).person() {
		return refusal(fmt.Sprintf("the tool %q did not run.", name),
			"This tool acts for a person, and this turn has no verified person.",
			"Tell the person that they must sign in for this. "+strings.Join(t.publicTools(), ", ")+" need no person."), nil, true
	}
	key := ""
	if rule.effect {
		key = effectKey(name, args)
		if e, ok := t.ts.effects[key]; ok {
			return repeatMark + e.Result, e.Ref, false
		}
		defer func() {
			if !refused && toolDone(out) {
				s.keepEffect(ctx, t, effect{Key: key, Tool: name, Args: args, Result: out, Ref: ref, By: t.by})
			}
		}()
	}
	var ok bool
	if out, ok = s.webTool(ctx, t.utt.Text, t.ts, name, args); ok {
		return out, nil, false
	}
	if out, ref, ok = s.extraTool(ctx, t.a, t.as, t.utt, t.ts, name, args); ok {
		return out, ref, false
	}
	out, ref = s.personaTool(ctx, t.a, t.as, t.utt, name, args)
	return out, ref, false
}

// publicTools are the tools of the turn that need no person.
func (t *toolTurn) publicTools() []string {
	var out []string
	for _, n := range t.tools {
		if r, ok := toolRules[n]; ok && !r.person {
			out = append(out, n)
		}
	}
	if len(out) == 0 {
		return []string{"No tools"}
	}
	return out
}
