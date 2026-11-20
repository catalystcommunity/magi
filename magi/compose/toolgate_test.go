package compose

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"unicode/utf8"

	workflowapi "github.com/catalystcommunity/magi/primitives/workflow/api"
)

// Each tool that a persona can get has a rule, and no rule is left of a
// tool that is gone.
func TestEachToolHasAGateRule(t *testing.T) {
	defs := toolDefs()
	for name := range defs {
		if _, ok := toolRules[name]; !ok {
			t.Errorf("tool %q has no rule in toolRules", name)
		}
	}
	for name := range toolRules {
		if _, ok := defs[name]; !ok {
			t.Errorf("toolRules has %q, which is not a tool", name)
		}
	}
	for id, p := range profiles {
		for _, name := range p.tools {
			if _, ok := defs[name]; !ok {
				t.Errorf("persona %s has the tool %q, which has no definition", id, name)
			}
		}
	}
}

// Only the gate calls the tool handlers (docs/decisions/0003).
func TestOnlyTheGateCallsToolHandlers(t *testing.T) {
	// handler -> the functions that can call it.
	callers := map[string][]string{
		"webTool":     {"gated"},
		"extraTool":   {"gated"},
		"personaTool": {"gated"},
		"planTool":    {"extraTool"},
		"workTool":    {"extraTool"},
		"makePage":    {"extraTool"},
		"gated":       {"toolGate"},
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range file.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if allowed, ok := callers[sel.Sel.Name]; ok && !contains(allowed, fn.Name.Name) {
					t.Errorf("%s: %s uses %s; only %v can (tool calls go through toolGate)",
						fset.Position(sel.Pos()), fn.Name.Name, sel.Sel.Name, allowed)
				}
				return true
			})
		}
	}
}

func contains(l []string, s string) bool {
	i := sort.SearchStrings(l, s)
	return i < len(l) && l[i] == s
}

func gateTurn(owner string, tools ...string) *toolTurn {
	return &toolTurn{a: workflowapi.Assignment{Owner: owner}, ts: &turnState{pages: map[string]bool{}}, tools: tools}
}

// A refusal has three parts, and the handler does not run.
func assertRefusal(t *testing.T, res toolResult, rule string) {
	t.Helper()
	lines := strings.Split(res.Out, "\n")
	if !res.Refused || len(lines) != 3 || !strings.HasPrefix(lines[0], refusalMark) ||
		!strings.HasPrefix(lines[1], "Rule: ") || !strings.HasPrefix(lines[2], "Next: ") {
		t.Fatalf("not a refusal with three parts: %+v", res)
	}
	if !strings.Contains(lines[1], rule) {
		t.Fatalf("rule = %q, want it to contain %q", lines[1], rule)
	}
	if res.Call["refused"] != true || !toolFailed(res.Out) {
		t.Fatalf("the refusal is not recorded as one: %+v", res.Call)
	}
}

func TestGateRefusals(t *testing.T) {
	s := &System{} // a refusal comes before any service is used
	ctx := context.Background()
	// A tool that the persona does not have, also when Magi has it.
	assertRefusal(t, s.toolGate(ctx, gateTurn("user:a@example.local", "recall"), "decide_task", map[string]string{}), "only the tools that it has")
	assertRefusal(t, s.toolGate(ctx, gateTurn("user:a@example.local", "recall"), "no_such_tool", map[string]string{}), "only the tools that it has")
	// Arguments that are not a JSON object.
	for _, raw := range []string{"{", "[1]", `"text"`, "null"} {
		args, ok := gateArgs(raw)
		if ok {
			t.Fatalf("gateArgs(%q) passed", raw)
		}
		assertRefusal(t, s.toolGate(ctx, gateTurn("user:a@example.local", "recall"), "recall", args), "one JSON object")
	}
	if args, ok := gateArgs(`{"query":"x","n":3}`); !ok || args["query"] != "x" || args["n"] != "3" {
		t.Fatalf("gateArgs = %v, %v", args, ok)
	}
	if args, ok := gateArgs(""); !ok || args == nil {
		t.Fatal("empty arguments are an empty object")
	}
	// A turn with no person: tools for a person are refused, and the
	// refusal names the tools that need no person.
	for _, owner := range []string{"", "service:magi", "device:kitchen"} {
		res := s.toolGate(ctx, gateTurn(owner, "recall", "web_search"), "recall", map[string]string{"query": "x"})
		assertRefusal(t, res, "no verified person")
		if !strings.Contains(res.Out, "web_search") {
			t.Fatalf("the refusal does not name what is possible: %s", res.Out)
		}
	}
}

// A result above the limit is cut between two characters, with a note.
func TestGateLimitsTheResult(t *testing.T) {
	if got := limitResult("short"); got != "short" {
		t.Fatalf("a short result changed: %q", got)
	}
	// "é" has two bytes; with one byte before, the limit is in the middle
	// of a character.
	out := limitResult("x" + strings.Repeat("é", maxToolResult))
	body, note, ok := strings.Cut(out, "\n[Magi cut this result")
	if !ok || !strings.Contains(note, "Ask for a smaller part") {
		t.Fatalf("no cut note: %q", out[len(out)-80:])
	}
	if len(body) != maxToolResult-1 || !utf8.ValidString(body) {
		t.Fatalf("cut at %d bytes, valid=%v", len(body), utf8.ValidString(body))
	}
	// A tool that needs no person runs in a turn with no person, and its
	// result goes through the limit and the record.
	res := (&System{}).toolGate(context.Background(), gateTurn("", "read_page"), "read_page", map[string]string{"url": "https://example.local/"})
	if res.Refused || res.Call["tool"] != "read_page" || res.Call["result"] != res.Out {
		t.Fatalf("read_page: %+v", res)
	}
}
