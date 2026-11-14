package compose

import (
	"context"
	"fmt"
	"time"

	magiapi "github.com/catalystcommunity/magi/magi/api"
)

// The note gate asks a decision model (task "decide") whether the newest
// message has something for the notes. When the probability of yes is less
// than Config.NoteGate, the note taker does not run. The decision model is
// small (a small GPU), and the note taker is large, so the gate keeps the
// large GPU free for messages with no facts: questions, requests, small
// talk.
//
// The question and the state are the same as the "record" question of
// evals/decide/eval_notes.py, because the catalog calibrates the question
// by its id on that eval. Change them together.
const recordQuestion = "Does the message tell something new that the notes must keep: a new or changed lasting fact, " +
	"a correction, a fact that is no longer true, or a notable happening? Questions, requests, plans, " +
	"wishes, small talk, and facts that the notes already have do not count."

// noteGateTimeout is the most time for the gate. With no answer in time,
// the note taker keeps its result.
const noteGateTimeout = 5 * time.Second

// gateResult is the answer of the gate, for the turn log.
type gateResult struct {
	Yes   float64 `json:"yes"`
	Model string  `json:"model,omitempty"`
	Ms    int64   `json:"ms"`
	Skip  bool    `json:"skip"`
	Error string  `json:"error,omitempty"`
}

func gateState(self, notes, text string) string {
	if notes == "" {
		notes = "(no notes yet)"
	}
	if self == "" {
		self = "the person"
	}
	return fmt.Sprintf("Notes of the assistant about %s:\n%s\n\nMessage from %s to the assistant:\n%s", self, notes, self, text)
}

// noteGate asks the gate question. ok is false when the gate is off or did
// not answer; then the note taker decides alone.
func (s *System) noteGate(ctx context.Context, self, notes, text string) (gateResult, bool) {
	if s.Cfg.NoteGate <= 0 || s.Router == nil {
		return gateResult{}, false
	}
	ctx, cancel := context.WithTimeout(ctx, noteGateTimeout)
	defer cancel()
	start := time.Now()
	res, err := s.Decide(ctx, DecisionTask, nil, false, gateState(self, notes, text),
		[]magiapi.DecisionQuestion{{Id: "record", Type: "noul", Question: recordQuestion}})
	g := gateResult{Ms: time.Since(start).Milliseconds()}
	if err != nil {
		g.Error = err.Error()
		return g, false
	}
	if len(res.Answers) != 1 || len(res.Answers[0].Probs) != 2 {
		g.Error = "no answer"
		return g, false
	}
	g.Model, g.Yes = res.Model, res.Answers[0].Probs[0]
	g.Skip = g.Yes < s.Cfg.NoteGate
	return g, true
}
