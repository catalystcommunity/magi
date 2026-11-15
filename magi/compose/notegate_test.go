package compose

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The calibration of the catalog is for the question of the eval. The
// gate must ask the same question.
func TestRecordQuestionAgreesWithEval(t *testing.T) {
	b, err := os.ReadFile("../../evals/decide/eval_notes.py")
	if err != nil {
		t.Skip(err)
	}
	src := regexp.MustCompile(`"\s*\n\s*"`).ReplaceAllString(string(b), "")
	if !strings.Contains(src, `"question": "`+recordQuestion+`"`) {
		t.Fatal("recordQuestion is not the record question of evals/decide/eval_notes.py")
	}
	if !strings.Contains(src, `f"Notes of the assistant about {c['self']}:\n{notes}\n\nMessage from {c['self']} to the assistant:\n{c['text']}"`) {
		t.Fatal("gateState is not state_of of evals/decide/eval_notes.py")
	}
}
