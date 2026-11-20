package compose

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
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
	"recall": readRule, "search_conversation": readRule, "remember": writeRule, "change_knowledge": writeRule,
	"start_code_change": writeRule, "decide_task": writeRule, "cancel_task": writeRule,
	"list_tasks": readRule, "task_status": readRule, "task_diff": readRule, "terminal_link": readRule, "list_runners": readRule,
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
	if rule.person && !strings.HasPrefix(t.a.Owner, "user:") {
		return refusal(fmt.Sprintf("the tool %q did not run.", name),
			"This tool acts for a person, and this turn has no verified person.",
			"Tell the person that they must sign in for this. "+strings.Join(t.publicTools(), ", ")+" need no person."), nil, true
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
