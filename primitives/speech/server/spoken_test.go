package server

import (
	"testing"

	api "github.com/catalystcommunity/magi/primitives/speech/api"
)

func TestSpoken(t *testing.T) {
	lex := []api.LexiconEntry{
		{Written: "README", Spoken: ptr("readme")},
		{Written: "Kokoro", Ipa: ptr("kˈOkəɹO")},
		{Written: "NAS", Spoken: ptr("nass")},
		{Written: "Magi", HeardAs: []string{"Magei", "Maggie"}},
		{Written: "lab NAS", Spoken: ptr("lab nass")},
	}
	cases := []struct{ in, want string }{
		// Ranges.
		{"Bring 4-6 chairs.", "Bring 4 to 6 chairs."},
		{"It takes 10 - 15 minutes.", "It takes 10 to 15 minutes."},
		{"Open 9-5 on weekdays.", "Open 9-5 on weekdays."}, // 9 > 5: maybe a score, not changed
		{"Pages 3–2.", "Pages 3 to 2."},                    // an en dash is always a range
		{"Use 1.5-2 cups.", "Use 1.5 to 2 cups."},
		{"From 4-6pm.", "From 4 to 6pm."},
		{"They won 3-2.", "They won 3-2."},
		{"A 1-2 loss.", "A 1-2 loss."},
		{"On 2026-10-31 at noon.", "On 2026-10-31 at noon."},
		{"Call 555-1234 now.", "Call 555-1234 now."},
		{"An RTX-4080 card.", "An RTX-4080 card."},
		{"Version 1.2-3 is out.", "Version 1.2-3 is out."},
		{"Scores 10-20-30.", "Scores 10-20-30."},
		// Markdown.
		{"**Note:** see the `README` file.", "Note: see the readme file."},
		{"# Plan\n- one\n- two", "Plan\none\ntwo"},
		{"Read [the docs](https://example.com/x).", "Read the docs."},
		{"Keep snake_case_name as is.", "Keep snake_case_name as is."},
		// Lexicon.
		{"Kokoro reads the README.", "[Kokoro](/kˈOkəɹO/) reads the readme."},
		{"The readme and READMEs.", "The readme and READMEs."},
		{"The lab NAS and the NAS.", "The lab nass and the nass."},
		{"Magi is here.", "Magi is here."}, // heard_as only: no change on speech
	}
	for _, c := range cases {
		if got := Spoken(c.in, lex); got != c.want {
			t.Errorf("Spoken(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	for in, want := range map[string]string{
		"Hello Magei, hi maggie.": "Hello Magi, hi Magi.",
		"Magein is a word.":       "Magein is a word.",
	} {
		if got := Heard(in, lex); got != want {
			t.Errorf("Heard(%q) = %q, want %q", in, got, want)
		}
	}
}
