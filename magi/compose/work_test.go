package compose

import (
	"slices"
	"testing"
	"time"
)

func TestContentWords(t *testing.T) {
	for in, want := range map[string][]string{
		"When's my next dentist appointment?":                   {"dentist"},
		"Who did I use to have one-on-ones with on Wednesdays?": {"use", "1:1", "wednesday"},
		"Is the kitchen remodel still on budget?":               {"kitchen", "remodel", "budget"},
		"What's on Alex's calendar while Alex is in Chicago?":   {"alex", "chicago"},
		"When is the final kitchen inspection?":                 {"final", "kitchen", "inspection"},
	} {
		if got := contentWords(in); !slices.Equal(got, want) {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

func TestRepeatRule(t *testing.T) {
	loc, _ := time.LoadLocation("America/Denver")
	for in, want := range map[[2]string]string{
		{"weekly", ""}:                  "FREQ=WEEKLY",
		{"every 2 weeks", "2026-12-31"}: "FREQ=WEEKLY;INTERVAL=2;UNTIL=20270101T065959Z",
		{"FREQ=MONTHLY;BYDAY=1SU", ""}:  "FREQ=MONTHLY;BYDAY=1SU",
		{"RRULE:FREQ=YEARLY", ""}:       "FREQ=YEARLY",
	} {
		got, err := repeatRule(in[0], in[1], loc)
		if err != nil || got != want {
			t.Errorf("%q: %q %v, want %q", in, got, err, want)
		}
	}
	if _, err := repeatRule("whenever", "", loc); err == nil {
		t.Error("whenever: no error")
	}
}
