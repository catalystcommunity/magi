package hostsetup

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/catalystcommunity/magi/magi/model"
)

// Env is where the setup tools act: the real host, or a scripted host in
// the live check.
type Env interface {
	// Exec runs a shell command as the setup user and returns its output.
	Exec(ctx context.Context, cmd string) string
	// Download gets a pinned item and checks its SHA-256. The output starts
	// with "ok" on success.
	Download(ctx context.Context, item string) string
	// Start starts the engine with a model and checks its health. The
	// output starts with "ok" when the engine is healthy.
	Start(ctx context.Context, engine, model string) string
	// Ask asks the person and returns the answer.
	Ask(ctx context.Context, question string) string
}

// Call is one tool call of the model.
type Call struct{ Tool, Arg, Out string }

// Result is the record of one setup run.
type Result struct {
	Calls  []Call
	Finish map[string]string
	// Engine and Model are of the last start that was healthy.
	Engine, Model string
	// Downloaded has the items that downloaded correctly.
	Downloaded map[string]bool
}

// MaxSteps is the maximum number of model calls in one run.
const MaxSteps = 24

// Tools are the tools of the setup model.
func Tools() []model.Tool {
	obj := func(props map[string]any, req ...string) map[string]any {
		return map[string]any{"type": "object", "properties": props, "required": req}
	}
	str := func(d string) map[string]any { return map[string]any{"type": "string", "description": d} }
	return []model.Tool{
		{Name: "run_command", Description: "Run a shell command on the host as the setup user (no sudo). Returns its output.",
			Parameters: obj(map[string]any{"command": str("the command")}, "command")},
		{Name: "download", Description: "Download one engine build or model from the lists and check its SHA-256.",
			Parameters: obj(map[string]any{"item": str("the name from a list")}, "item")},
		{Name: "start_engine", Description: "Start the engine with a downloaded engine build and model, and check its health.",
			Parameters: obj(map[string]any{"engine": str("engine build name"), "model": str("model name; for speech, both names separated by a comma")}, "engine", "model")},
		{Name: "ask_person", Description: "Ask the person to do something or to answer a question. Waits for the answer.",
			Parameters: obj(map[string]any{"question": str("the request, with the exact command when there is one")}, "question")},
		{Name: "finish", Description: "End the setup.",
			Parameters: obj(map[string]any{
				"status":  map[string]any{"type": "string", "enum": []string{"ok", "failed", "needs-person"}},
				"engine":  str("the engine build that runs, or empty"),
				"model":   str("the model that runs, or empty"),
				"summary": str("what was done and why")}, "status", "summary")},
	}
}

var sudoWord = regexp.MustCompile(`(^|[\s;&|(])(sudo|doas|pkexec|su)(\s|$)`)

// Run is the agent loop. It enforces the plan: a download or start of an
// item that the checks removed is refused. log gets each call when it is
// done (nil for none).
func Run(ctx context.Context, c model.Chatter, env Env, pl *Plan, log func(Call)) (*Result, error) {
	msgs := []model.Message{
		{Role: "system", Content: pl.Prompt()},
		{Role: "user", Content: "Set up this host."},
	}
	r := &Result{Downloaded: map[string]bool{}}
	ran := map[string]bool{}
	record := func(cl Call) {
		r.Calls = append(r.Calls, cl)
		if log != nil {
			log(cl)
		}
	}
	for step := 0; step < MaxSteps; step++ {
		m, err := c.Chat(ctx, msgs, Tools(), 2048)
		if err != nil {
			return r, err
		}
		m.Served, m.Skipped = "", nil
		msgs = append(msgs, m)
		if len(m.ToolCalls) == 0 {
			msgs = append(msgs, model.Message{Role: "user", Content: "Continue. Use the tools, and end with the finish tool."})
			continue
		}
		for _, tc := range m.ToolCalls {
			raw := map[string]any{}
			json.Unmarshal([]byte(tc.Function.Arguments), &raw)
			a := map[string]string{}
			for k, v := range raw {
				a[k] = fmt.Sprint(v)
			}
			var arg, out string
			switch tc.Function.Name {
			case "run_command":
				arg = a["command"]
				switch {
				case sudoWord.MatchString(arg):
					out = "error: commands that need sudo or a password are not permitted. Ask the person with ask_person, and give the exact command."
				case ran[arg]:
					out = "You already ran this command, and its output is the same. Use the facts and the outputs that you have."
				default:
					out = env.Exec(ctx, arg)
				}
				ran[arg] = true
			case "download":
				arg = strings.TrimSpace(a["item"])
				if !pl.Allowed(arg) {
					out = "error: " + arg + " is not in the lists for this host. Use only the listed items."
					break
				}
				out = env.Download(ctx, arg)
				if strings.HasPrefix(out, "ok") {
					r.Downloaded[arg] = true
				}
			case "start_engine":
				arg = a["engine"] + " " + a["model"]
				for _, item := range append([]string{a["engine"]}, strings.Split(a["model"], ",")...) {
					item = strings.TrimSpace(item)
					if !pl.Allowed(item) {
						out = "error: " + item + " is not in the lists for this host"
						break
					}
					if !r.Downloaded[item] {
						out = "error: " + item + " is not downloaded"
						break
					}
				}
				if out == "" {
					out = env.Start(ctx, strings.TrimSpace(a["engine"]), strings.TrimSpace(a["model"]))
				}
				if strings.HasPrefix(out, "ok") {
					r.Engine, r.Model = strings.TrimSpace(a["engine"]), strings.TrimSpace(a["model"])
				}
			case "ask_person":
				arg, out = a["question"], env.Ask(ctx, a["question"])
			case "finish":
				record(Call{Tool: "finish", Arg: a["status"] + " " + a["engine"] + " " + a["model"], Out: a["summary"]})
				r.Finish = a
				return r, nil
			default:
				arg, out = tc.Function.Name, "error: unknown tool "+tc.Function.Name
			}
			record(Call{Tool: tc.Function.Name, Arg: arg, Out: out})
			msgs = append(msgs, model.Message{Role: "tool", ToolCallID: tc.ID, Content: out})
		}
	}
	return r, fmt.Errorf("no finish after %d model calls", MaxSteps)
}
