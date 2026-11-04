package server

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	api "github.com/catalystcommunity/magi/primitives/speech/api"
)

// Spoken makes the text that the TTS engine gets: Markdown removed,
// number ranges said as ranges, and the lexicon applied. The rules are
// deterministic; an unclear case stays as it was.
func Spoken(text string, lex []api.LexiconEntry) string {
	text = plain(text)
	text = ranges(text)
	var subs []sub
	for _, e := range lex {
		if say := say(e); say != "" {
			subs = append(subs, sub{from: e.Written, to: say})
		}
	}
	return replaceWords(text, subs)
}

// Heard changes the heard_as forms of the lexicon in a transcript to the
// written forms.
func Heard(text string, lex []api.LexiconEntry) string {
	var subs []sub
	for _, e := range lex {
		for _, h := range e.HeardAs {
			subs = append(subs, sub{from: h, to: e.Written})
		}
	}
	return replaceWords(text, subs)
}

// say is what the engine gets for an entry: misaki's inline phoneme form
// "[label](/ipa/)" when ipa is set, else spoken.
func say(e api.LexiconEntry) string {
	label := e.Written
	if e.Spoken != nil && *e.Spoken != "" {
		label = *e.Spoken
	}
	if e.Ipa != nil && *e.Ipa != "" {
		return "[" + label + "](/" + *e.Ipa + "/)"
	}
	if label == e.Written {
		return ""
	}
	return label
}

var (
	fence   = regexp.MustCompile("(?m)^\\s*```.*$")
	heading = regexp.MustCompile(`(?m)^\s{0,3}#{1,6}\s+`)
	bullet  = regexp.MustCompile(`(?m)^\s*(?:[-*+]|\d+[.)])\s+`)
	mdLink  = regexp.MustCompile(`!?\[([^\]]*)\]\([^)]*\)`)
	// A single "_" is not emphasis here: it is often in names (snake_case).
	emphasis  = regexp.MustCompile(`(\*\*|__|\*|~~)([^*~\n]+?)(\*\*|__|\*|~~)`)
	backticks = regexp.MustCompile("`+")
	quote     = regexp.MustCompile(`(?m)^\s*>\s?`)
)

// plain removes the Markdown that a model writes. A link becomes its text.
func plain(s string) string {
	s = fence.ReplaceAllString(s, "")
	s = heading.ReplaceAllString(s, "")
	s = bullet.ReplaceAllString(s, "")
	s = quote.ReplaceAllString(s, "")
	s = mdLink.ReplaceAllString(s, "$1")
	s = emphasis.ReplaceAllStringFunc(s, func(m string) string {
		p := emphasis.FindStringSubmatch(m)
		if p[1] != p[3] {
			return m
		}
		return p[2]
	})
	s = backticks.ReplaceAllString(s, "")
	return strings.TrimSpace(s)
}

var rangeRe = regexp.MustCompile(`(\d+(?:\.\d+)?) ?([-–]) ?(\d+(?:\.\d+)?)`)

// Words before or after "N-M" that make it a score, a result, or a
// version, not a range.
var notRange = map[string]bool{
	"version": true, "v": true, "release": true, "build": true, "model": true, "revision": true, "rev": true,
	"won": true, "lost": true, "beat": true, "beats": true, "score": true, "scored": true, "final": true,
	"tied": true, "lead": true, "leads": true, "led": true, "win": true, "loss": true, "victory": true,
	"defeat": true, "draw": true, "tie": true, "record": true,
}

// ranges says "N-M" as "N to M" when it is a range: N is less than M, it
// is not part of a longer token (a date, a model number, a telephone
// number), and no score or version word is next to it. An en dash is always a range.
func ranges(s string) string {
	var b strings.Builder
	last := 0
	for _, m := range rangeRe.FindAllStringSubmatchIndex(s, -1) {
		a, dash, c := s[m[2]:m[3]], s[m[4]:m[5]], s[m[6]:m[7]]
		before, _ := utf8.DecodeLastRuneInString(s[:m[0]])
		after, _ := utf8.DecodeRuneInString(s[m[1]:])
		ok := !(m[0] > 0 && (unicode.IsLetter(before) || unicode.IsDigit(before) || strings.ContainsRune("-–/:.", before))) &&
			!(m[1] < len(s) && (unicode.IsDigit(after) || strings.ContainsRune("-–/:", after)))
		if ok && dash == "-" {
			x, _ := strconv.ParseFloat(a, 64)
			y, _ := strconv.ParseFloat(c, 64)
			phone := len(a) == 3 && len(c) == 4
			ok = x < y && !phone && !notRange[word(s[:m[0]], true)] && !notRange[word(s[m[1]:], false)]
		}
		if !ok {
			continue
		}
		b.WriteString(s[last:m[0]])
		b.WriteString(a + " to " + c)
		last = m[1]
	}
	b.WriteString(s[last:])
	return b.String()
}

// word returns the word next to a range, in lower case: the last word of
// s when before is true, else the first word.
func word(s string, before bool) string {
	f := strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) })
	if len(f) == 0 {
		return ""
	}
	if before {
		return strings.ToLower(f[len(f)-1])
	}
	t := strings.TrimLeft(s, " ")
	if t == "" || !unicode.IsLetter([]rune(t)[0]) {
		return ""
	}
	return strings.ToLower(f[0])
}

type sub struct{ from, to string }

// replaceWords replaces whole-word, case-insensitive matches of each
// sub.from with sub.to. A longer form wins over a shorter one at the same
// place, and a replacement is not matched again.
func replaceWords(s string, subs []sub) string {
	if len(subs) == 0 || s == "" {
		return s
	}
	sort.SliceStable(subs, func(i, j int) bool { return len(subs[i].from) > len(subs[j].from) })
	type span struct {
		start, end int
		to         string
	}
	var spans []span
	taken := func(a, b int) bool {
		for _, sp := range spans {
			if a < sp.end && sp.start < b {
				return true
			}
		}
		return false
	}
	for _, x := range subs {
		from := strings.TrimSpace(x.from)
		if from == "" {
			continue
		}
		re, err := regexp.Compile(`(?i)` + regexp.QuoteMeta(from))
		if err != nil {
			continue
		}
		for _, m := range re.FindAllStringIndex(s, -1) {
			before, _ := utf8.DecodeLastRuneInString(s[:m[0]])
			after, _ := utf8.DecodeRuneInString(s[m[1]:])
			if m[0] > 0 && isWordRune(before) || m[1] < len(s) && isWordRune(after) || taken(m[0], m[1]) {
				continue
			}
			spans = append(spans, span{m[0], m[1], x.to})
		}
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
	var b strings.Builder
	last := 0
	for _, sp := range spans {
		b.WriteString(s[last:sp.start])
		b.WriteString(sp.to)
		last = sp.end
	}
	b.WriteString(s[last:])
	return b.String()
}

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' }
