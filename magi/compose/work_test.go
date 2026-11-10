package compose

import (
	"slices"
	"testing"
	"time"

	cal "github.com/catalystcommunity/catalystcal/clients/go"
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

func TestAllDayText(t *testing.T) {
	one := cal.Occurrence{Event: cal.Event{AllDay: ptr(true)}, Start: "2026-09-27T00:00:00Z", End: "2026-09-28T00:00:00Z"}
	if got := allDayText(one); got != "Sun 2026-09-27 all day" {
		t.Errorf("one day: %q", got)
	}
	trip := cal.Occurrence{Event: cal.Event{AllDay: ptr(true)}, Start: "2026-11-16T00:00:00Z", End: "2026-11-19T00:00:00Z"}
	if got := allDayText(trip); got != "Mon 2026-11-16 to Wed 2026-11-18 all day" {
		t.Errorf("trip: %q", got)
	}
}

func TestDaysIn(t *testing.T) {
	loc := time.UTC
	now := time.Date(2026, 11, 9, 10, 0, 0, 0, loc) // a Monday
	day := func(m time.Month, d int) time.Time { return time.Date(2026, m, d, 0, 0, 0, 0, loc) }
	for text, want := range map[string][]time.Time{
		"Is anything on Alex's calendar on Friday November 13?": {day(11, 13)},
		"What's on 2026-11-20?":                                 {day(11, 20)},
		"What do I have tomorrow?":                              {day(11, 10)},
		"What's on Saturday?":                                   {day(11, 14)},
		"Lunch on 3 December":                                   {day(12, 3)},
		"Who's Ruby's coach?":                                   nil,
	} {
		got := daysIn(text, now, loc)
		if len(got) != len(want) {
			t.Errorf("%q: %v", text, got)
			continue
		}
		for i := range got {
			if !got[i].Equal(want[i]) {
				t.Errorf("%q: %v", text, got)
			}
		}
	}
}
