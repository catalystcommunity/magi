package compose

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/catalystcommunity/magi/magi/model"
	conversationapi "github.com/catalystcommunity/magi/primitives/conversation/api"
)

// A librarian case: earlier utterances, a new one, and the right answer.
// links maps an earlier id to the kinds that are correct for it; an id
// that is not in links must not get a link. visibility is the right
// visibility ("" when the case does not test it). need lists groups of
// ids: a correct link to one id of each group is required (default: each
// id of links is its own group).
type libCase struct {
	name       string
	sharing    string
	prev       []libUtt
	older      []libUtt
	msg        libUtt
	links      map[string][]string
	need       [][]string
	visibility string
	// unclear: a person could decide either way, so the right answer is a
	// question: a visibility confidence below the threshold.
	unclear bool
}

type libUtt struct {
	id, who, text string // who: "p" person, "m" persona, or a subject
	ago           time.Duration
}

var turnKinds = []string{"reply-to", "answers", "acknowledges", "thanks", "follows-up", "continues", "agrees-with", "confirms"}

func kinds(k ...string) []string { return append(k, turnKinds...) }

var libCases = []libCase{
	{name: "answer to a question", sharing: "private",
		prev:  []libUtt{{"u1", "p", "What time is the dentist on Friday?", 3 * time.Minute}},
		msg:   libUtt{"u2", "m", "Your dentist appointment is at 9:30 on Friday at Maple Street Dental.", 0},
		links: map[string][]string{"u1": kinds("answers", "reply-to")}},
	{name: "two topics in one message", sharing: "private",
		prev: []libUtt{
			{"u1", "p", "The README on the parakeet repo is wrong about the install step.", 3 * 24 * time.Hour},
			{"u2", "m", "I started a task to fix the parakeet README.", 3 * 24 * time.Hour},
			{"u3", "p", "Also the Pi runner was slow yesterday.", 2 * 24 * time.Hour},
			{"u4", "m", "The Pi was building Rust for the release controller; that takes 20 minutes there.", 2 * 24 * time.Hour},
		},
		msg: libUtt{"u5", "p", "Did the readme thing get fixed, and is the Pi faster now?", 0},
		links: map[string][]string{"u1": kinds("asks-about", "refers-to", "resumes", "same-subject-as"), "u2": kinds("asks-about", "refers-to", "resumes", "same-subject-as"),
			"u3": kinds("asks-about", "refers-to", "resumes", "same-subject-as"), "u4": kinds("asks-about", "refers-to", "resumes", "same-subject-as")},
		need: [][]string{{"u1", "u2"}, {"u3", "u4"}}},
	{name: "change of subject", sharing: "private",
		prev: []libUtt{
			{"u1", "p", "Can you check the runner logs for errors?", 10 * time.Minute},
			{"u2", "m", "There are no errors in the runner logs today.", 9 * time.Minute},
		},
		msg:   libUtt{"u3", "p", "Different question: what's a good recipe for banana bread?", 0},
		links: map[string][]string{"u2": {"changes-subject-from"}}, need: [][]string{}},
	{name: "correction", sharing: "private",
		prev: []libUtt{
			{"u1", "p", "My mother's birthday is March 3.", 20 * time.Minute},
			{"u2", "m", "I recorded that your mother's birthday is March 3.", 19 * time.Minute},
		},
		msg:   libUtt{"u3", "p", "Sorry, I was wrong, it's March 13, not March 3.", 0},
		links: map[string][]string{"u1": {"corrects", "contradicts", "clarifies"}, "u2": {"corrects", "contradicts", "reply-to", "clarifies"}},
		need:  [][]string{{"u1", "u2"}}},
	{name: "resumes an old thought", sharing: "private",
		prev: []libUtt{{"u5", "p", "Thanks, that is all for now.", 2 * time.Hour}},
		older: []libUtt{
			{"u1", "p", "I want to plan a trip to Lisbon in the spring.", 20 * 24 * time.Hour},
			{"u2", "m", "Spring is a good time for Lisbon. Do you want to fly or take the train from Madrid?", 20 * 24 * time.Hour},
		},
		msg:   libUtt{"u6", "p", "Back to the Lisbon trip: let's take the train from Madrid.", 0},
		links: map[string][]string{"u1": {"resumes", "continues", "follows-up", "refers-to", "same-subject-as"}, "u2": kinds("answers", "resumes", "decides", "refers-to", "same-subject-as")},
		need:  [][]string{{"u1", "u2"}}},
	{name: "similar words, unrelated", sharing: "private",
		prev: []libUtt{{"u3", "p", "Set a timer for the oven.", time.Hour}},
		older: []libUtt{
			{"u1", "p", "The car battery is dead again.", 30 * 24 * time.Hour},
			{"u2", "m", "I noted that the car battery is dead.", 30 * 24 * time.Hour},
		},
		msg:   libUtt{"u4", "p", "My phone battery drains fast since the update.", 0},
		links: map[string][]string{}},
	{name: "request then fulfilment", sharing: "private",
		prev:  []libUtt{{"u1", "p", "Please add a health endpoint section to the Magi README.", 30 * time.Minute}},
		msg:   libUtt{"u2", "m", "Done: the task merged and the README now has a Health endpoint section.", 0},
		links: map[string][]string{"u1": kinds("fulfills", "reports-on")}},
	{name: "disagreement", sharing: "private",
		prev:  []libUtt{{"u1", "m", "I suggest that you run the runner on the NAS, because it is always on.", 5 * time.Minute}},
		msg:   libUtt{"u2", "p", "No, the NAS is too slow for builds; keep it on the Pi.", 0},
		links: map[string][]string{"u1": kinds("disagrees-with", "decides", "contradicts")}},

	{name: "private persona, household note", sharing: "private",
		prev: []libUtt{}, msg: libUtt{"u1", "p", "Tell everyone that the plumber comes on Tuesday at 10.", 0},
		links: map[string][]string{}, visibility: "shared"},
	{name: "private persona, health", sharing: "private",
		prev: []libUtt{}, msg: libUtt{"u1", "p", "My blood pressure was 150 over 95 this morning, I'm worried.", 0},
		links: map[string][]string{}, visibility: "private"},
	{name: "private persona, plain work", sharing: "private",
		prev: []libUtt{}, msg: libUtt{"u1", "p", "Start a code change to rename the agent package.", 0},
		links: map[string][]string{}, visibility: "private"},
	{name: "public persona, groceries", sharing: "public",
		prev: []libUtt{}, msg: libUtt{"u1", "p", "We are out of milk and eggs, put them on the shopping list.", 0},
		links: map[string][]string{}, visibility: "shared"},
	{name: "public persona, gift", sharing: "public",
		prev: []libUtt{}, msg: libUtt{"u1", "p", "Help me pick a birthday gift for Lorna, it's a surprise.", 0},
		links: map[string][]string{}, visibility: "private"},
	{name: "public persona, lights", sharing: "public",
		prev: []libUtt{}, msg: libUtt{"u1", "p", "Turn off the porch lights at 11 every night.", 0},
		links: map[string][]string{}, visibility: "shared"},
	{name: "public persona, money worry", sharing: "public",
		prev: []libUtt{}, msg: libUtt{"u1", "p", "I overdrew my personal account again and I don't want anyone to know.", 0},
		links: map[string][]string{}, visibility: "private"},
	{name: "public persona, reply follows the question", sharing: "public",
		prev:  []libUtt{{"u1", "p", "When is trash pickup this week?", time.Minute}},
		msg:   libUtt{"u2", "m", "Trash pickup is on Thursday this week because of the holiday.", 0},
		links: map[string][]string{"u1": kinds("answers", "reply-to")}, visibility: "shared"},

	// Harder cases, from the errors of the first import.
	{name: "unclear reference, two candidates", sharing: "private",
		prev: []libUtt{
			{"u1", "p", "Start a task to update the deploy README.", 2 * time.Hour},
			{"u2", "m", "Task run_a started for the deploy README.", 2 * time.Hour},
			{"u3", "p", "And start one to fix the Android sign-in text.", time.Hour},
			{"u4", "m", "Task run_b started for the Android sign-in text.", time.Hour},
		},
		msg: libUtt{"u5", "p", "Is it done yet?", 0},
		links: map[string][]string{"u3": kinds("asks-about", "refers-to"), "u4": kinds("asks-about", "refers-to"),
			"u1": {"asks-about", "refers-to"}, "u2": {"asks-about", "refers-to"}},
		need: [][]string{{"u1", "u2", "u3", "u4"}}},
	{name: "three topics, one is new", sharing: "private",
		prev: []libUtt{
			{"u1", "p", "The kitchen faucet drips.", 5 * 24 * time.Hour},
			{"u2", "m", "I noted the dripping kitchen faucet.", 5 * 24 * time.Hour},
			{"u3", "p", "Remind me to renew the car registration in November.", 4 * 24 * time.Hour},
			{"u4", "m", "I will remind you about the car registration in November.", 4 * 24 * time.Hour},
		},
		msg: libUtt{"u5", "p", "The plumber fixed the faucet, I renewed the registration early, and can you find me a piano teacher?", 0},
		links: map[string][]string{"u1": {"follows-up", "reports-on", "resumes", "refers-to", "fulfills", "same-subject-as", "continues"},
			"u2": {"follows-up", "reports-on", "resumes", "refers-to", "fulfills", "same-subject-as", "continues"},
			"u3": {"follows-up", "reports-on", "resumes", "refers-to", "fulfills", "same-subject-as", "continues"},
			"u4": {"follows-up", "reports-on", "resumes", "refers-to", "fulfills", "same-subject-as", "continues"}},
		need: [][]string{{"u1", "u2"}, {"u3", "u4"}}},
	{name: "repeated test greeting, far apart", sharing: "private",
		prev:  []libUtt{{"u3", "p", "What is on my calendar today?", 3 * 24 * time.Hour}},
		older: []libUtt{{"u1", "p", "Hello Magi. Reply with one short sentence.", 10 * 24 * time.Hour}, {"u2", "m", "Hello! How can I help?", 10 * 24 * time.Hour}},
		msg:   libUtt{"u4", "p", "Hello Magi. Reply with one short sentence.", 0},
		links: map[string][]string{"u1": {"restates", "same-subject-as"}}, need: [][]string{}},
	{name: "public persona, another person's health for the house", sharing: "public",
		prev: []libUtt{}, msg: libUtt{"u1", "p", "Lorna has a migraine, keep the house quiet this afternoon.", 0},
		links: map[string][]string{}, visibility: "shared", unclear: true},
	{name: "public persona, opinion about a member", sharing: "public",
		prev: []libUtt{}, msg: libUtt{"u1", "p", "Honestly Sam never does the dishes, it drives me crazy.", 0},
		links: map[string][]string{}, visibility: "private"},
	{name: "public persona, wifi password", sharing: "public",
		prev: []libUtt{}, msg: libUtt{"u1", "p", "The new wifi password is maple-river-42.", 0},
		links: map[string][]string{}, visibility: "shared", unclear: true},
	{name: "private persona, family plan", sharing: "private",
		prev: []libUtt{}, msg: libUtt{"u1", "p", "We're going to my parents' place for Thanksgiving.", 0},
		links: map[string][]string{}, visibility: "private"},
	{name: "private persona, shared calendar fact", sharing: "private",
		prev: []libUtt{}, msg: libUtt{"u1", "p", "Let the family know dinner is at 6 tonight.", 0},
		links: map[string][]string{}, visibility: "shared"},
}

type libDecision struct {
	conf    float64
	correct bool
}

// TestLiveLibrarian checks a model as the librarian: its links and
// visibility on scripted conversations, and whether its confidence is
// honest. An error with a confidence at or above the start threshold
// (0.75) is never reviewed, so "confident errors" is the most important
// number. Set MAGI_TEST_LIBRARIAN_URL and MAGI_TEST_LIBRARIAN_MODEL
// (default: the MAGI_TEST_PERSONA_ ones), and _KEY_FILE for a key.
// MAGI_TEST_LIBRARIAN_RUNS (default 2) repeats each case.
func TestLiveLibrarian(t *testing.T) {
	env := func(k string) string {
		if v := os.Getenv("MAGI_TEST_LIBRARIAN_" + k); v != "" {
			return v
		}
		return os.Getenv("MAGI_TEST_PERSONA_" + k)
	}
	base, name := env("URL"), env("MODEL")
	if base == "" || name == "" {
		t.Skip("MAGI_TEST_LIBRARIAN_URL and MAGI_TEST_LIBRARIAN_MODEL are not set")
	}
	c := &model.Client{BaseURL: strings.TrimRight(base, "/"), Model: name, HTTP: &http.Client{Timeout: 5 * time.Minute}, Attempts: 1}
	if f := env("KEY_FILE"); f != "" {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		c.APIKey = strings.TrimSpace(string(b))
	}
	runs := 2
	if n, err := strconv.Atoi(os.Getenv("MAGI_TEST_LIBRARIAN_RUNS")); err == nil && n > 0 {
		runs = n
	}
	const threshold = 0.75 // the start threshold of the conversation service
	now := time.Now()
	var all []libDecision
	var calls, badJSON, missed, expected int
	var ms int64
	for _, tc := range libCases {
		for range runs {
			in := librarianInput{persona: "Magi", sharing: tc.sharing, person: "user:alex@example.local", msg: tc.msg.utt(now)}
			if tc.sharing == "public" {
				in.persona = "Butler"
			}
			// As in classify: only own text, and only its ids as targets.
			st := conversationapi.Stream{Id: "str_live", Person: in.person}
			var ids []string
			for _, u := range tc.prev {
				if v := u.utt(now); ownText(v, st) {
					in.prev, ids = append(in.prev, v), append(ids, v.Id)
				}
			}
			for _, u := range tc.older {
				if v := u.utt(now); ownText(v, st) {
					in.older, ids = append(in.older, v), append(ids, v.Id)
				}
			}
			start := time.Now()
			m, err := c.ChatSchema(context.Background(), librarianMessages(in, now), librarianSchema(ids, tc.sharing), 800)
			ms += time.Since(start).Milliseconds()
			calls++
			var ans librarianAnswer
			if err == nil {
				err = json.Unmarshal([]byte(jsonObject(m.Content)), &ans)
			}
			if err != nil {
				badJSON++
				t.Logf("%-40s error: %v %q", tc.name, err, snippet(m.Content, 120))
				continue
			}
			var got []string
			linked := map[string]bool{}
			for _, l := range ans.Links {
				ok := slices.Contains(tc.links[l.To], strings.ToLower(l.Kind))
				all = append(all, libDecision{l.Confidence, ok})
				linked[l.To] = linked[l.To] || ok
				got = append(got, fmt.Sprintf("%s %s %.2f%s", l.Kind, l.To, l.Confidence, map[bool]string{true: "", false: " WRONG"}[ok]))
			}
			need := tc.need
			if need == nil {
				for id := range tc.links {
					need = append(need, []string{id})
				}
			}
			for _, group := range need {
				expected++
				if !slices.ContainsFunc(group, func(id string) bool { return linked[id] }) {
					missed++
				}
			}
			if tc.visibility != "" {
				ok := ans.Visibility == tc.visibility
				if tc.unclear {
					// Either answer is fine; a question is right.
					ok = ans.VisibilityConfidence < threshold
				}
				all = append(all, libDecision{ans.VisibilityConfidence, ok})
				got = append(got, fmt.Sprintf("visibility %s %.2f%s", ans.Visibility, ans.VisibilityConfidence, map[bool]string{true: "", false: " WRONG"}[ok]))
			}
			t.Logf("%-40s %s", tc.name, strings.Join(got, "; "))
		}
	}
	var correct, confidentWrong, askedRight int
	var brier float64
	for _, d := range all {
		y := 0.0
		if d.correct {
			y, correct = 1, correct+1
		}
		brier += math.Pow(d.conf-y, 2)
		switch {
		case !d.correct && d.conf >= threshold:
			confidentWrong++
		case d.correct && d.conf < threshold:
			askedRight++
		}
	}
	n := max(len(all), 1)
	t.Logf("%s: %d calls, %d ms mean, %d not JSON", name, calls, ms/int64(max(calls, 1)), badJSON)
	t.Logf("decisions %d: %.0f%% correct; confident errors %d (%.0f%%); questions about correct decisions %d (%.0f%%); Brier %.3f",
		len(all), 100*float64(correct)/float64(n), confidentWrong, 100*float64(confidentWrong)/float64(n), askedRight,
		100*float64(askedRight)/float64(n), brier/float64(n))
	t.Logf("expected links missed: %d of %d", missed, expected)
	if badJSON*10 > calls {
		t.Errorf("%d of %d answers were not JSON", badJSON, calls)
	}
}

func (u libUtt) utt(now time.Time) conversationapi.Utterance {
	out := conversationapi.Utterance{Id: u.id, Stream: "str_live", Text: u.text, AuthorKind: "human", Author: "user:alex@example.local",
		CreatedMs: uint64(now.Add(-u.ago).UnixMilli())}
	switch u.who {
	case "m":
		out.AuthorKind, out.Author = "persona", "persona:magi"
	case "p":
	default:
		out.Author = u.who
	}
	return out
}
