package compose

import (
	"testing"
	"time"
)

func TestAgeNote(t *testing.T) {
	old := zone
	zone = time.UTC
	defer func() { zone = old }()
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct{ rel, v, want string }{
		{"birthday", "February 21, 2019", "7 years ago; the next one is Sunday 21 February 2027, 8 years"},
		{"birthday", "2016-09-03", "10 years ago; the next one is Friday 3 September 2027, 11 years"},
		{"married-on", "September 27, 2014", "12 years ago; the next one is Monday 27 September 2027, 13 years"},
		{"birthday", "2016-11-09", "10 years ago; the next one is Monday 9 November 2026, 10 years"},
		{"birthday", "April 2", ""},
		{"vet", "2016-09-03", ""},
	} {
		if got := ageNote(c.rel, c.v, now); got != c.want {
			t.Errorf("%s %s: %q", c.rel, c.v, got)
		}
	}
}
