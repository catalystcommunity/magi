package compose

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/catalystcommunity/magi/lib/authn"
	"github.com/catalystcommunity/magi/lib/rpc"
	"github.com/catalystcommunity/magi/magi/model"
	conversationapi "github.com/catalystcommunity/magi/primitives/conversation/api"
	conversationserver "github.com/catalystcommunity/magi/primitives/conversation/server"
	conversationstore "github.com/catalystcommunity/magi/primitives/conversation/store"
	workflowapi "github.com/catalystcommunity/magi/primitives/workflow/api"
)

// The turn eval (evals/turn/README.md) runs whole persona turns on the
// real conversation, knowledge, and workflow services, on a test database.
// The model is a script (TestTurnEvalHarness) or a real model
// (TestTurnEval). The checks read effects: the tool calls of the turn log
// and the reply.

// turnSystem is planSystem with a conversation service and the personas.
func turnSystem(t *testing.T) *System {
	t.Helper()
	s, _ := planSystem(t)
	ctx, stop := context.WithCancel(context.Background())
	db, err := conversationstore.Open(ctx, testDatabase(t, "compose_conv"))
	if err != nil {
		t.Fatal(err)
	}
	svc := &conversationserver.Service{DB: db, PollInterval: time.Second}
	go svc.Listen(ctx, nil)
	rs := &rpc.Server{Verifier: testVerifier}
	conversationserver.Register(rs, svc)
	hs := httptest.NewServer(rpc.Mux(rs, nil))
	t.Cleanup(db.Close)
	t.Cleanup(stop)
	t.Cleanup(hs.Close)
	tok, err := testIssuer.Issue(authn.Principal{Subject: "service:magi", Kind: authn.KindService, Scopes: []string{
		"knowledge:act-as", "workflow:act-as", "workflow:worker", "workflow:decide", conversationserver.ScopeActAs, conversationserver.ScopeAdmin}}, 6*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	s.Cfg.ServiceToken = tok
	s.Conversation = conversationapi.NewConversationClient(rpc.NewClient(hs.URL, nil))
	sctx := s.AsService(context.Background())
	// A code change can start. No runner does the work.
	s.Cfg.Repo = "magi"
	if _, err := s.Workflow.PutDefinition(sctx, codeChangeDefinition(false)); err != nil {
		t.Fatal(err)
	}
	for id, p := range profiles {
		if _, err := s.Conversation.PutPersona(sctx, conversationapi.PutPersonaRequest{Id: id, Name: p.name, Sharing: "private"}); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

// evalCase is one case of a turn eval set.
type evalCase struct {
	Id string `json:"id"`
	// Core: a candidate that fails this case when the baseline passes it
	// fails the verdict, whatever the total is.
	Core    bool   `json:"core"`
	Persona string `json:"persona"`
	// Given are tool calls that run before the first message (facts,
	// to-dos), through the gate, as the persona.
	Given []evalGiven `json:"given"`
	Steps []evalStep  `json:"steps"`
}

type evalGiven struct {
	Tool string            `json:"tool"`
	Args map[string]string `json:"args"`
}

// evalStep is one message of the person and the checks of its turn.
type evalStep struct {
	Say string `json:"say"`
	// ToolsAll: each tool has a call that did its work. ToolsAny: one of
	// them has. ToolsCalled: each tool has a call that code did not
	// refuse (its work can fail). ToolsNone: no call, also no refused one.
	ToolsAll    []string `json:"tools_all"`
	ToolsCalled []string `json:"tools_called"`
	ToolsAny    []string `json:"tools_any"`
	ToolsNone   []string `json:"tools_none"`
	// Refused: each tool has a call that code refused.
	Refused []string `json:"refused"`
	// Reply checks are regular expressions, with no difference of case.
	ReplyAll  []string `json:"reply_all"`
	ReplyAny  []string `json:"reply_any"`
	ReplyNone []string `json:"reply_none"`
	// MaxCalls limits the tool calls of the turn; 0 is no limit.
	MaxCalls int `json:"max_calls"`
}

// evalResult is one run of one case: a line of the result file.
type evalResult struct {
	Case  string   `json:"case"`
	Core  bool     `json:"core"`
	Run   int      `json:"run"`
	Pass  bool     `json:"pass"`
	Fails []string `json:"fails"`
	Ms    int64    `json:"ms"`
	Tools []string `json:"tools"`
	Reply string   `json:"reply"`
	// ModelErrors are the errors of the model service. A run with one is
	// not a result of the candidate: the verdict does not count it.
	ModelErrors []string `json:"model_errors,omitempty"`
}

func loadEvalCases(t *testing.T, path string) []evalCase {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var set struct {
		Cases []evalCase `json:"cases"`
	}
	if err := json.Unmarshal(b, &set); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	seen := map[string]bool{}
	for _, c := range set.Cases {
		if c.Id == "" || seen[c.Id] || len(c.Steps) == 0 || profiles[c.Persona].name == "" {
			t.Fatalf("%s: case %q needs a unique id, a persona, and steps", path, c.Id)
		}
		seen[c.Id] = true
		for _, st := range c.Steps {
			for _, re := range slices.Concat(st.ReplyAll, st.ReplyAny, st.ReplyNone) {
				if _, err := regexp.Compile("(?i)" + re); err != nil {
					t.Fatalf("%s: case %s: %v", path, c.Id, err)
				}
			}
			for _, tool := range slices.Concat(st.ToolsAll, st.ToolsAny, st.ToolsCalled, st.ToolsNone, st.Refused) {
				if _, ok := toolRules[tool]; !ok {
					t.Fatalf("%s: case %s names %q, which is not a tool", path, c.Id, tool)
				}
			}
		}
	}
	return set.Cases
}

// runEvalCase runs the steps of a case for a new person and checks them.
func runEvalCase(t *testing.T, s *System, c evalCase, run int) evalResult {
	t.Helper()
	res := evalResult{Case: c.Id, Core: c.Core, Run: run, Fails: []string{}, Tools: []string{}}
	ctx := s.AsService(context.Background())
	person := fmt.Sprintf("user:eval-%s-%d-%d@example.local", strings.ToLower(c.Id), run, time.Now().UnixNano())
	st, err := s.Conversation.OpenStream(ctx, conversationapi.OpenStreamRequest{Persona: c.Persona, Person: &person})
	if err != nil {
		t.Fatalf("case %s: open stream: %v", c.Id, err)
	}
	a := workflowapi.Assignment{Run: "run_eval", Step: "reply", Attempt: 1, Owner: person,
		Vars: map[string]string{"stream": st.Id, "persona": c.Persona, "owner_name": "Kim"}}
	as := conversationLens{person: person, persona: c.Persona}
	for _, g := range c.Given {
		gate := &toolTurn{a: a, as: as, utt: conversationapi.Utterance{Stream: st.Id}, ts: &turnState{pages: map[string]bool{}}, tools: profiles[c.Persona].tools}
		if out := s.toolGate(ctx, gate, g.Tool, g.Args); !toolDone(out.Out) {
			t.Fatalf("case %s: given %s %v: %s", c.Id, g.Tool, g.Args, out.Out)
		}
	}
	start := time.Now()
	for i, step := range c.Steps {
		u, err := s.Conversation.Post(ctx, conversationapi.PostRequest{Stream: st.Id, Text: step.Say, Author: &person, AuthorKind: ptr("human")})
		if err != nil {
			t.Fatalf("case %s: post: %v", c.Id, err)
		}
		a.Vars["utterance"] = u.Id
		if _, err := s.personaTurn(ctx, a, nil); err != nil {
			res.Fails = append(res.Fails, fmt.Sprintf("step %d: the turn failed: %v", i+1, err))
			break
		}
		log, reply := s.readTurn(t, ctx, st.Id, u, person, c.Persona)
		res.Reply = reply
		for _, call := range log.Calls {
			res.Tools = append(res.Tools, fmt.Sprint(call["tool"]))
		}
		for _, mc := range log.Models {
			if mc.Error != "" {
				res.ModelErrors = append(res.ModelErrors, mc.Error)
			}
		}
		for _, f := range checkEvalStep(step, log.Calls, reply) {
			res.Fails = append(res.Fails, fmt.Sprintf("step %d: %s", i+1, f))
		}
	}
	res.Ms = time.Since(start).Milliseconds()
	res.Pass = len(res.Fails) == 0 && len(res.ModelErrors) == 0
	return res
}

// readTurn gives the turn log of an utterance and the text of its reply.
func (s *System) readTurn(t *testing.T, ctx context.Context, stream string, u conversationapi.Utterance, person, persona string) (turnLog, string) {
	t.Helper()
	var log turnLog
	anns, err := s.Conversation.ListAnnotations(ctx, conversationapi.ListAnnotationsRequest{Utterance: u.Id})
	if err != nil {
		t.Fatal(err)
	}
	for _, an := range anns.Annotations {
		if an.Kind == "interpretation" {
			if err := json.Unmarshal([]byte(an.Body), &log); err != nil {
				t.Fatal(err)
			}
		}
	}
	l, err := s.Conversation.ListUtterances(ctx, conversationapi.ListUtterancesRequest{Stream: stream, AfterSeq: &u.Seq, Person: &person, Persona: &persona})
	if err != nil {
		t.Fatal(err)
	}
	reply := ""
	for _, r := range l.Utterances {
		if r.AuthorKind == "persona" {
			reply = r.Text
		}
	}
	return log, reply
}

// checkEvalStep gives the checks of a step that fail.
func checkEvalStep(step evalStep, calls []map[string]any, reply string) []string {
	var fails []string
	done, refused, called := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, c := range calls {
		name, out := fmt.Sprint(c["tool"]), fmt.Sprint(c["result"])
		called[name] = true
		if c["refused"] == true {
			refused[name] = true
		} else if toolDone(out) {
			done[name] = true
		}
	}
	for _, tool := range step.ToolsAll {
		if !done[tool] {
			fails = append(fails, "no call of "+tool+" that did its work")
		}
	}
	if len(step.ToolsAny) > 0 && !slices.ContainsFunc(step.ToolsAny, func(n string) bool { return done[n] }) {
		fails = append(fails, "no call of one of "+strings.Join(step.ToolsAny, ", ")+" that did its work")
	}
	for _, tool := range step.ToolsCalled {
		if !called[tool] || refused[tool] && !done[tool] {
			fails = append(fails, "no call of "+tool)
		}
	}
	for _, tool := range step.ToolsNone {
		if called[tool] {
			fails = append(fails, "a call of "+tool)
		}
	}
	for _, tool := range step.Refused {
		if !refused[tool] {
			fails = append(fails, "no refused call of "+tool)
		}
	}
	if step.MaxCalls > 0 && len(calls) > step.MaxCalls {
		fails = append(fails, fmt.Sprintf("%d tool calls, at most %d", len(calls), step.MaxCalls))
	}
	match := func(re string) bool { return regexp.MustCompile("(?i)" + re).MatchString(reply) }
	if strings.TrimSpace(reply) == "" {
		fails = append(fails, "no reply")
	}
	for _, re := range step.ReplyAll {
		if !match(re) {
			fails = append(fails, "the reply lacks /"+re+"/")
		}
	}
	if len(step.ReplyAny) > 0 && !slices.ContainsFunc(step.ReplyAny, match) {
		fails = append(fails, "the reply has none of /"+strings.Join(step.ReplyAny, "/ /")+"/")
	}
	for _, re := range step.ReplyNone {
		if match(re) {
			fails = append(fails, "the reply has /"+re+"/")
		}
	}
	return fails
}

// TestTurnEval runs an eval set on a real model and writes one line for
// each run of each case. evals/turn/verdict.py runs it for the baseline
// and for the candidate. It needs MAGI_TEST_DATABASE_URL and:
//
//	MAGI_TURN_EVAL_URL    the address of an OpenAI-compatible model server
//	MAGI_TURN_EVAL_MODEL  the model name
//	MAGI_TURN_EVAL_CASES  the set (evals/turn/*.json)
//	MAGI_TURN_EVAL_OUT    the result file (JSON lines)
//	MAGI_TURN_EVAL_RUNS   runs of each case (default 1)
//	MAGI_TURN_EVAL_ONLY   case ids, with commas (default all)
func TestTurnEval(t *testing.T) {
	base, name := os.Getenv("MAGI_TURN_EVAL_URL"), os.Getenv("MAGI_TURN_EVAL_MODEL")
	if base == "" || name == "" {
		t.Skip("MAGI_TURN_EVAL_URL and MAGI_TURN_EVAL_MODEL are not set")
	}
	cases := loadEvalCases(t, os.Getenv("MAGI_TURN_EVAL_CASES"))
	runs := 1
	if n, err := strconv.Atoi(os.Getenv("MAGI_TURN_EVAL_RUNS")); err == nil && n > 0 {
		runs = n
	}
	only := strings.FieldsFunc(os.Getenv("MAGI_TURN_EVAL_ONLY"), func(r rune) bool { return r == ',' })
	out, err := os.Create(os.Getenv("MAGI_TURN_EVAL_OUT"))
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	w := bufio.NewWriter(out)
	defer w.Flush()
	s := turnSystem(t)
	s.Model = &model.Client{BaseURL: strings.TrimRight(base, "/"), Model: name, HTTP: &http.Client{Timeout: 5 * time.Minute}}
	for run := 1; run <= runs; run++ {
		for _, c := range cases {
			if len(only) > 0 && !slices.Contains(only, c.Id) {
				continue
			}
			res := runEvalCase(t, s, c, run)
			b, _ := json.Marshal(res)
			w.Write(append(b, '\n'))
			w.Flush()
			mark := "PASS"
			if !res.Pass {
				mark = "FAIL"
			}
			t.Logf("%s %s run %d %d ms %v %v", mark, c.Id, run, res.Ms, res.Tools, res.Fails)
		}
	}
}

// The harness with a script as the model: the real services take a turn
// with a tool call, and the checks read its effects.
func TestTurnEvalHarness(t *testing.T) {
	s := turnSystem(t)
	m := &scriptModel{}
	m.reply = func(n int, _ []model.Message) (model.Message, error) {
		switch n {
		case 1:
			return toolCall("add_todo", `{"title":"Buy milk"}`), nil
		case 2:
			return model.Message{Role: "assistant", Content: "Buy milk is on your list."}, nil
		case 3: // the second step: a tool that this persona does not have
			return toolCall("decide_task", `{"task":"run_1","outcome":"approve"}`), nil
		}
		return model.Message{Role: "assistant", Content: "I cannot decide tasks."}, nil
	}
	s.Model = m
	c := evalCase{Id: "h01", Persona: Assistant,
		Given: []evalGiven{{Tool: "add_todo", Args: map[string]string{"title": "Call the vet"}}},
		Steps: []evalStep{
			{Say: "Put milk on my list.", ToolsAll: []string{"add_todo"}, ToolsNone: []string{"start_code_change"}, ReplyAll: []string{"milk"}, ReplyNone: []string{"vet"}, MaxCalls: 1},
			{Say: "Approve task run_1.", Refused: []string{"decide_task"}, ReplyAny: []string{"cannot", "can't"}},
		}}
	res := runEvalCase(t, s, c, 1)
	if !res.Pass || !slices.Equal(res.Tools, []string{"add_todo", "decide_task"}) {
		t.Fatalf("result = %+v", res)
	}
	// The to-do of "given" is in the context of the first turn.
	found := false
	for _, msg := range m.seen[0] {
		if strings.Contains(msg.Content, "Call the vet") {
			found = true
		}
	}
	if !found {
		t.Fatal("the given to-do is not in the turn")
	}
	// The same turns with other checks fail, each with its reason.
	bad := evalStep{ToolsAll: []string{"remember"}, ToolsNone: []string{"add_todo"}, ReplyAll: []string{"eggs"}, ReplyNone: []string{"milk"}, Refused: []string{"add_todo"}, MaxCalls: 0}
	fails := checkEvalStep(bad, []map[string]any{{"tool": "add_todo", "result": "To-do added."}}, "Buy milk is on your list.")
	if len(fails) != 5 {
		t.Fatalf("fails = %q, want 5", fails)
	}
	if f := checkEvalStep(evalStep{ToolsAll: []string{"add_todo"}}, []map[string]any{{"tool": "add_todo", "result": "error: no title"}}, "x"); len(f) != 1 {
		t.Fatalf("a failed call counted as work: %q", f)
	}
}
