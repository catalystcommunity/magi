package compose

import (
	"testing"

	speechapi "github.com/catalystcommunity/magi/primitives/speech/api"
)

func TestDecideOverlap(t *testing.T) {
	for _, tc := range []struct {
		ack, turn float64
		words     int
		final     bool
		want      string
	}{
		{0.97, 0.02, 1, false, "continue"}, // "mm-hm"
		{0.05, 0.93, 1, false, "stop"},     // "wait"
		{0.20, 0.55, 2, false, "stop"},
		{0.30, 0.40, 1, false, "wait"}, // "so"
		{0.30, 0.40, 1, true, "stop"},  // not clear at the end: the person takes the turn
		{0.55, 0.30, 2, true, "continue"},
		{0.60, 0.30, 9, false, "stop"}, // many words are a turn
		{0.95, 0.03, 9, false, "continue"},
	} {
		got := decideOverlap(speechapi.TurnClass{Ack: tc.ack, Turn: tc.turn, Unclear: 1 - tc.ack - tc.turn}, tc.words, tc.final)
		if got != tc.want {
			t.Errorf("%+v: got %s", tc, got)
		}
	}
}

func TestLastWords(t *testing.T) {
	text := "The meeting is at three. Bring the notes."
	if got := lastWords(text, 24, 3); got != "is at three." {
		t.Fatalf("got %q", got)
	}
	if got := lastWords(text, 0, 2); got != "the notes." {
		t.Fatalf("got %q", got)
	}
}

func TestIsEcho(t *testing.T) {
	text := "The meeting is at three. Bring the notes from Tuesday. After that we can plan the trip."
	at := len([]rune("The meeting is at three. Bring the notes"))
	for words, want := range map[string]bool{
		"bring the notes":           true,
		"from tuesday":              true, // just after the cursor: the playback is ahead of the cursor
		"wait what notes":           false,
		"mm-hm":                     false,
		"no not three it's at four": false,
	} {
		if got := isEcho(words, text, at); got != want {
			t.Errorf("isEcho(%q) = %v", words, got)
		}
	}
}
