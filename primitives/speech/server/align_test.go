package server

import (
	"fmt"
	"testing"
)

func TestAlignWords(t *testing.T) {
	// The spoken text of "Read the **README**, it takes 4-6 minutes." is
	// "Read the readme, it takes 4 to 6 minutes."; the engine gives
	// punctuation as pauses.
	text := "Read the **README**, it takes 4-6 minutes. Don’t wait."
	eng := []engineWord{{"Read", 0, 100}, {"the", 100, 200}, {"readme", 200, 300}, {",", 300, 320}, {"it", 320, 400},
		{"takes", 400, 500}, {"four", 500, 600}, {"to", 600, 700}, {"six", 700, 800}, {"minutes", 800, 900}, {".", 900, 950},
		{"Don't", 950, 1000}, {"wait", 1000, 1100}}
	got := alignWords(text, eng)
	rs := []rune(text)
	var words []string
	for _, w := range got {
		words = append(words, fmt.Sprintf("%s=%s@%d", w.Word, string(rs[w.Start:w.End]), w.StartMs))
	}
	want := []string{"Read=Read@0", "the=the@100", "readme=README@200", "it=it@320", "takes=takes@400",
		"four=4@500", "to=4@600", "six=6@700", "minutes=minutes@800", "Don't=Don’t@950", "wait=wait@1000"}
	if fmt.Sprint(words) != fmt.Sprint(want) {
		t.Fatalf("got  %v\nwant %v", words, want)
	}
	// Offsets count code points, not bytes.
	got = alignWords("Café ok", []engineWord{{"ok", 0, 10}})
	if got[0].Start != 5 || got[0].End != 7 {
		t.Fatalf("code points: %+v", got[0])
	}
	if got := alignWords("", []engineWord{{"hi", 0, 10}}); len(got) != 1 || got[0].End != 0 {
		t.Fatalf("no text: %+v", got)
	}
}
