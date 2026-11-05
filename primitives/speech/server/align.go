package server

import (
	"strings"
	"unicode"

	api "github.com/catalystcommunity/magi/primitives/speech/api"
)

// A word of a text: its key (lower case letters and digits) and its
// character (code point) offsets.
type textWord struct {
	key        string
	start, end int
}

// textWords returns the words of s. A word is a run of letters and digits;
// an apostrophe between letters stays in the word ("don't").
func textWords(s string) []textWord {
	rs := []rune(s)
	var out []textWord
	for i := 0; i < len(rs); {
		if !isAlnum(rs[i]) {
			i++
			continue
		}
		j := i
		for j < len(rs) && (isAlnum(rs[j]) || (rs[j] == '\'' || rs[j] == '’') && j+1 < len(rs) && isAlnum(rs[j+1]) && j > i) {
			j++
		}
		out = append(out, textWord{key: wordKey(string(rs[i:j])), start: i, end: j})
		i = j
	}
	return out
}

func isAlnum(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

func wordKey(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if isAlnum(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// engineWord is one word of the engine: [word, start_ms, end_ms].
type engineWord struct {
	word           string
	startMs, endMs uint64
}

// alignWords gives each engine word (a word of the spoken text) the place
// of a word in text, the text before the spoken form. Equal words match in
// order (longest common subsequence). A spoken word with no match (the
// "four" of "4", the "to" of "4-6") gets a word of text between its matched
// neighbours, in proportion, or the nearest matched word. Engine tokens
// with no letter or digit (pauses for punctuation) are left out.
func alignWords(text string, eng []engineWord) []api.WordTime {
	var spoken []engineWord
	for _, w := range eng {
		if wordKey(w.word) != "" {
			spoken = append(spoken, w)
		}
	}
	orig := textWords(text)
	out := make([]api.WordTime, len(spoken))
	if len(spoken) == 0 {
		return out
	}
	n, m := len(spoken), len(orig)
	// lcs[i][j] is the length of the common subsequence of spoken[i:] and orig[j:].
	lcs := make([][]int32, n+1)
	for i := range lcs {
		lcs[i] = make([]int32, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		k := wordKey(spoken[i].word)
		for j := m - 1; j >= 0; j-- {
			switch {
			case k == orig[j].key:
				lcs[i][j] = lcs[i+1][j+1] + 1
			case lcs[i+1][j] >= lcs[i][j+1]:
				lcs[i][j] = lcs[i+1][j]
			default:
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}
	match := make([]int, n) // the orig word of each spoken word, or -1
	for i, j := 0, 0; i < n; {
		switch {
		case j < m && wordKey(spoken[i].word) == orig[j].key:
			match[i] = j
			i, j = i+1, j+1
		case j < m && lcs[i][j+1] > lcs[i+1][j]:
			j++
		default:
			match[i] = -1
			i++
		}
	}
	for i := 0; i < n; {
		if match[i] >= 0 {
			i++
			continue
		}
		// spoken[i:k] have no match; they lie between the orig words
		// prev and next.
		k := i
		for k < n && match[k] < 0 {
			k++
		}
		prev, next := -1, m
		if i > 0 {
			prev = match[i-1]
		}
		if k < n {
			next = match[k]
		}
		for x := i; x < k; x++ {
			switch free := next - prev - 1; {
			case free > 0:
				match[x] = prev + 1 + (x-i)*free/(k-i)
			case prev >= 0:
				match[x] = prev
			case next < m:
				match[x] = next
			}
		}
		i = k
	}
	for i, w := range spoken {
		out[i] = api.WordTime{Word: w.word, StartMs: w.startMs, EndMs: w.endMs}
		if j := match[i]; j >= 0 && j < m {
			out[i].Start, out[i].End = uint64(orig[j].start), uint64(orig[j].end)
		}
	}
	return out
}
