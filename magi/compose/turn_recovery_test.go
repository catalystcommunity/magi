package compose

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/catalystcommunity/magi/lib/rpc"
	"github.com/catalystcommunity/magi/magi/model"
	conversationapi "github.com/catalystcommunity/magi/primitives/conversation/api"
	workflowapi "github.com/catalystcommunity/magi/primitives/workflow/api"
)

// turnWorld is a conversation service and a workflow service in memory,
// for tests that run a whole persona turn. Operations that it does not
// have give an error, as a service that is down.
type turnWorld struct {
	conversationapi.ConversationService
	mu          sync.Mutex
	utts        []conversationapi.Utterance
	keys        map[string]string // post key -> utterance id
	annotations []conversationapi.Annotation
	starts      []workflowapi.StartRequest
	runs        map[string]workflowapi.Run // by key
	failHistory bool
}

func (w *turnWorld) GetUtterance(_ context.Context, r conversationapi.GetUtteranceRequest) (conversationapi.Utterance, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, u := range w.utts {
		if u.Id == r.Id {
			return u, nil
		}
	}
	return conversationapi.Utterance{}, rpc.NotFound("no utterance %s", r.Id)
}

func (w *turnWorld) ListUtterances(_ context.Context, r conversationapi.ListUtterancesRequest) (conversationapi.ListUtterancesResponse, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.failHistory {
		return conversationapi.ListUtterancesResponse{}, rpc.Unavailable("the history is not there")
	}
	out := conversationapi.ListUtterancesResponse{Utterances: []conversationapi.Utterance{}}
	for _, u := range w.utts {
		if r.BeforeSeq == nil || u.Seq < *r.BeforeSeq {
			out.Utterances = append(out.Utterances, u)
		}
	}
	return out, nil
}

func (w *turnWorld) Post(_ context.Context, r conversationapi.PostRequest) (conversationapi.Utterance, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if r.Key != nil {
		if id, ok := w.keys[*r.Key]; ok {
			for _, u := range w.utts {
				if u.Id == id {
					return u, nil
				}
			}
		}
	}
	u := conversationapi.Utterance{Id: fmt.Sprintf("utt_%d", len(w.utts)+1), Stream: r.Stream, Seq: uint64(len(w.utts) + 1),
		Author: deref(r.Author), AuthorKind: deref(r.AuthorKind), Modality: deref(r.Modality), Text: r.Text, Attachments: []conversationapi.Ref{}}
	w.utts = append(w.utts, u)
	if r.Key != nil {
		w.keys[*r.Key] = u.Id
	}
	return u, nil
}

func (w *turnWorld) Annotate(_ context.Context, r conversationapi.AnnotateRequest) (conversationapi.Annotation, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	a := conversationapi.Annotation{Id: fmt.Sprintf("ann_%d", len(w.annotations)+1), Utterance: r.Utterance, Kind: r.Kind, Body: r.Body}
	w.annotations = append(w.annotations, a)
	return a, nil
}

func (w *turnWorld) ListAnnotations(_ context.Context, r conversationapi.ListAnnotationsRequest) (conversationapi.ListAnnotationsResponse, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := conversationapi.ListAnnotationsResponse{Annotations: []conversationapi.Annotation{}}
	for _, a := range w.annotations {
		if a.Utterance == r.Utterance {
			out.Annotations = append(out.Annotations, a)
		}
	}
	return out, nil
}

// turnWorkflow is the workflow side of turnWorld.
type turnWorkflow struct {
	workflowapi.WorkflowService
	w *turnWorld
}

func (f turnWorkflow) Start(_ context.Context, r workflowapi.StartRequest) (workflowapi.Run, error) {
	f.w.mu.Lock()
	defer f.w.mu.Unlock()
	f.w.starts = append(f.w.starts, r)
	key := deref(r.Key)
	if run, ok := f.w.runs[key]; ok && key != "" {
		return run, nil
	}
	run := workflowapi.Run{Id: fmt.Sprintf("run_%d", len(f.w.runs)+1), Definition: r.Definition, Owner: deref(r.Owner), Status: "running", Vars: r.Vars}
	f.w.runs[key] = run
	return run, nil
}

func (f turnWorkflow) ListRuns(context.Context, workflowapi.ListRunsRequest) (workflowapi.ListRunsResponse, error) {
	return workflowapi.ListRunsResponse{Runs: []workflowapi.Run{}}, nil
}

// safeOps makes an operation that the fake does not have an error, not a
// crash of the test server.
func safeOps(ops map[string]func(context.Context, []byte) ([]byte, error)) map[string]func(context.Context, []byte) ([]byte, error) {
	for name, op := range ops {
		ops[name] = func(ctx context.Context, b []byte) (out []byte, err error) {
			defer func() {
				if recover() != nil {
					out, err = nil, rpc.Unavailable("the test service has no %s", name)
				}
			}()
			return op(ctx, b)
		}
	}
	return ops
}

// scriptModel answers from a function of the round and the messages.
type scriptModel struct {
	mu    sync.Mutex
	calls int
	seen  [][]model.Message
	reply func(n int, msgs []model.Message) (model.Message, error)
}

func (m *scriptModel) Chat(_ context.Context, msgs []model.Message, _ []model.Tool, _ int) (model.Message, error) {
	m.mu.Lock()
	m.calls++
	n := m.calls
	m.seen = append(m.seen, msgs)
	m.mu.Unlock()
	return m.reply(n, msgs)
}

func toolCall(name, args string) model.Message {
	var tc model.ToolCall
	tc.ID, tc.Type = "call_1", "function"
	tc.Function.Name, tc.Function.Arguments = name, args
	return model.Message{Role: "assistant", ToolCalls: []model.ToolCall{tc}}
}

const turnOwner = "user:a@example.local"

// turnEnv gives a system on a turnWorld with one message of the person,
// and the assignment of its turn.
func turnEnv(t *testing.T, text string, m model.Chatter) (*System, *turnWorld, workflowapi.Assignment) {
	t.Helper()
	w := &turnWorld{keys: map[string]string{}, runs: map[string]workflowapi.Run{}}
	w.utts = []conversationapi.Utterance{{Id: "utt_1", Stream: "str_1", Seq: 1, Author: turnOwner, AuthorKind: "human", Modality: "text",
		Text: text, Attachments: []conversationapi.Ref{}}}
	sys, _, _ := adminEnv(t, Config{}, map[string]map[string]func(context.Context, []byte) ([]byte, error){
		"ConversationService": safeOps(conversationapi.ConversationServiceOps(w)),
		"WorkflowService":     safeOps(workflowapi.WorkflowServiceOps(turnWorkflow{w: w})),
	})
	sys.Model = m
	a := workflowapi.Assignment{Run: "run_turn", Step: "reply", Attempt: 1, Owner: turnOwner,
		Vars: map[string]string{"stream": "str_1", "utterance": "utt_1", "persona": Assistant, dispatchVar: testDispatch(turnOwner, Assistant)}}
	return sys, w, a
}

func (w *turnWorld) replies() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []string
	for _, u := range w.utts {
		if u.AuthorKind == "persona" {
			out = append(out, u.Text)
		}
	}
	return out
}

func (w *turnWorld) todoStarts() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := 0
	for _, s := range w.starts {
		if s.Definition == Todo {
			n++
		}
	}
	return n
}

func run(sys *System, ctx context.Context, a workflowapi.Assignment) (Result, error) {
	return sys.personaTurn(sys.AsService(ctx), a, nil)
}

const addMilk = `{"title":"Buy milk"}`

// Magi stops after an effect of a turn. The turn starts again: the effect
// is not made a second time, the model learns of it, and one reply goes
// to the person (docs/decisions/0004).
func TestTurnThatStartsAgainDoesNotRepeatAnEffect(t *testing.T) {
	ctx1, stop := context.WithCancel(context.Background())
	m := &scriptModel{}
	m.reply = func(n int, msgs []model.Message) (model.Message, error) {
		switch n {
		case 1:
			return toolCall("add_todo", addMilk), nil
		case 2: // Magi stops here, after the to-do and before the reply.
			stop()
			return model.Message{}, context.Canceled
		case 3: // The second start. The model gives the same call again.
			return toolCall("add_todo", addMilk), nil
		}
		return model.Message{Role: "assistant", Content: "Buy milk is on your list."}, nil
	}
	sys, w, a := turnEnv(t, "Put milk on my list.", m)
	if _, err := run(sys, ctx1, a); err == nil {
		t.Fatal("the first start did not fail")
	}
	if got := w.replies(); len(got) != 0 {
		t.Fatalf("the stopped start posted %q", got)
	}
	if n := w.todoStarts(); n != 1 {
		t.Fatalf("the first start made %d to-dos, want 1", n)
	}
	a.Attempt = 2
	res, err := run(sys, context.Background(), a)
	if err != nil || res.Outcome != "done" {
		t.Fatalf("second start: %+v, %v", res, err)
	}
	if n := w.todoStarts(); n != 1 {
		t.Fatalf("after the second start there are %d to-do starts, want 1", n)
	}
	if got := w.replies(); len(got) != 1 || got[0] != "Buy milk is on your list." {
		t.Fatalf("replies = %q", got)
	}
	// The second start told the model of the action of the first start,
	// and the repeated call got the first result.
	second := m.seen[2]
	told, repeated := false, false
	for _, msg := range second {
		if msg.Role == "user" && strings.Contains(msg.Content, "Magi already did this") && strings.Contains(msg.Content, "add_todo") {
			told = true
		}
	}
	for _, msg := range m.seen[3] {
		if msg.Role == "tool" && strings.Contains(msg.Content, repeatMark) {
			repeated = true
		}
	}
	if !told || !repeated {
		t.Fatalf("told=%v repeated=%v", told, repeated)
	}
}

// Magi stops after the reply and before the report to the workflow. The
// turn starts again: no model call, no second reply.
func TestTurnThatStartsAgainAfterTheReplyDoesNothing(t *testing.T) {
	m := &scriptModel{}
	m.reply = func(n int, _ []model.Message) (model.Message, error) {
		if n == 1 {
			return toolCall("add_todo", addMilk), nil
		}
		return model.Message{Role: "assistant", Content: fmt.Sprintf("Answer %d.", n)}, nil
	}
	sys, w, a := turnEnv(t, "Put milk on my list.", m)
	if _, err := run(sys, context.Background(), a); err != nil {
		t.Fatal(err)
	}
	calls := m.calls
	a.Attempt = 2
	res, err := run(sys, context.Background(), a)
	if err != nil || res.Outcome != "done" {
		t.Fatalf("second start: %+v, %v", res, err)
	}
	if m.calls != calls {
		t.Fatalf("the second start called the model %d times", m.calls-calls)
	}
	if got := w.replies(); len(got) != 1 || got[0] != "Answer 2." {
		t.Fatalf("replies = %q", got)
	}
	if n := w.todoStarts(); n != 1 {
		t.Fatalf("%d to-do starts, want 1", n)
	}
}

// The record that the reply was posted is lost (Magi stopped between the
// post and the record). The key of the post keeps the reply single.
func TestReplyKeyKeepsOneReply(t *testing.T) {
	m := &scriptModel{reply: func(n int, _ []model.Message) (model.Message, error) {
		return model.Message{Role: "assistant", Content: fmt.Sprintf("Answer %d.", n)}, nil
	}}
	sys, w, a := turnEnv(t, "Hello.", m)
	if _, err := run(sys, context.Background(), a); err != nil {
		t.Fatal(err)
	}
	w.mu.Lock()
	kept := w.annotations[:0]
	for _, an := range w.annotations {
		if an.Kind != annReplied {
			kept = append(kept, an)
		}
	}
	w.annotations = kept
	w.mu.Unlock()
	a.Attempt = 2
	if _, err := run(sys, context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if got := w.replies(); len(got) != 1 || got[0] != "Answer 1." {
		t.Fatalf("replies = %q", got)
	}
}

// A turn that fails two times tells the person one time. The first
// failure tells nothing: the turn starts again.
func TestTurnThatFailsTwoTimesTellsThePerson(t *testing.T) {
	m := &scriptModel{reply: func(int, []model.Message) (model.Message, error) {
		return model.Message{}, errors.New("not used")
	}}
	sys, w, a := turnEnv(t, "Hello.", m)
	w.failHistory = true
	if _, err := run(sys, context.Background(), a); err == nil {
		t.Fatal("no error")
	}
	if got := w.replies(); len(got) != 0 {
		t.Fatalf("the first failure posted %q", got)
	}
	a.Attempt = 2
	for range 2 {
		if _, err := run(sys, context.Background(), a); err == nil {
			t.Fatal("no error")
		}
	}
	got := w.replies()
	if len(got) != 1 || !strings.Contains(got[0], "could not complete") {
		t.Fatalf("replies = %q", got)
	}
}

// The same call two times in one start runs one time.
func TestSameEffectTwoTimesInOneTurnRunsOneTime(t *testing.T) {
	m := &scriptModel{}
	m.reply = func(n int, _ []model.Message) (model.Message, error) {
		if n <= 2 {
			return toolCall("add_todo", addMilk), nil
		}
		return model.Message{Role: "assistant", Content: "Done."}, nil
	}
	sys, w, a := turnEnv(t, "Put milk on my list.", m)
	if _, err := run(sys, context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if n := w.todoStarts(); n != 1 {
		t.Fatalf("%d to-do starts, want 1", n)
	}
}

func TestEffectKey(t *testing.T) {
	a := effectKey("add_todo", map[string]string{"title": "x", "due": "y"})
	b := effectKey("add_todo", map[string]string{"due": "y", "title": "x"})
	if a != b {
		t.Fatal("the order of the arguments changed the key")
	}
	for _, other := range []string{
		effectKey("add_todo", map[string]string{"title": "x"}),
		effectKey("set_reminder", map[string]string{"title": "x", "due": "y"}),
		effectKey("add_todo", map[string]string{"title": "xdue", "": "y"}),
	} {
		if other == a {
			t.Fatal("two different calls have one key")
		}
	}
}

// A turn acts for one principal: the author of the message. A run with a
// different owner does not act for that owner with the words of the
// author: no model call, no tool call (docs/decisions/0008).
func TestTurnForADifferentOwnerDoesNotAct(t *testing.T) {
	m := &scriptModel{reply: func(int, []model.Message) (model.Message, error) {
		return toolCall("add_todo", addMilk), nil
	}}
	sys, w, a := turnEnv(t, "Put milk on my list.", m)
	a.Owner = "user:b@example.local"
	if _, err := run(sys, context.Background(), a); err == nil || !strings.Contains(err.Error(), "one principal") {
		t.Fatalf("err = %v", err)
	}
	if m.calls != 0 || w.todoStarts() != 0 || len(w.replies()) != 0 {
		t.Fatalf("the turn acted: %d model calls, %d to-dos, replies %q", m.calls, w.todoStarts(), w.replies())
	}
}

// A turn with no person (a device with no identity) runs, and code
// refuses each tool that acts for a person.
func TestTurnWithNoPersonGetsNoPersonTools(t *testing.T) {
	m := &scriptModel{}
	m.reply = func(n int, _ []model.Message) (model.Message, error) {
		if n == 1 {
			return toolCall("add_todo", addMilk), nil
		}
		return model.Message{Role: "assistant", Content: "Sign in first, please."}, nil
	}
	sys, w, a := turnEnv(t, "Put milk on my list.", m)
	a.Owner = "device:kitchen"
	a.Vars[dispatchVar] = testDispatch("device:kitchen", Assistant)
	w.utts[0].Author = "device:kitchen"
	if _, err := run(sys, context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if w.todoStarts() != 0 {
		t.Fatal("a turn with no person made a to-do")
	}
	refused := false
	for _, msg := range m.seen[1] {
		if msg.Role == "tool" && strings.Contains(msg.Content, "no verified person") {
			refused = true
		}
	}
	if got := w.replies(); !refused || len(got) != 1 {
		t.Fatalf("refused=%v replies=%q", refused, got)
	}
}
