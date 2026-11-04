package compose

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	conversationapi "github.com/catalystcommunity/magi/primitives/conversation/api"
)

func TestLibrarianAnswerParses(t *testing.T) {
	for _, in := range []string{
		`{"links": [], "visibility": "private", "visibility_confidence": 0.9}`,
		"```json\n{\"links\": [{\"to\": \"utt_1\", \"kind\": \"answers\", \"confidence\": 0.8}], \"visibility\": \"shared\"}\n```",
		"Here is the answer: {\"links\": [], \"visibility\": \"private\"} I hope it helps.",
	} {
		var a librarianAnswer
		if err := json.Unmarshal([]byte(jsonObject(in)), &a); err != nil || a.Visibility == "" {
			t.Fatalf("%q: %+v, %v", in, a, err)
		}
	}
}

func TestSnippetAndWhen(t *testing.T) {
	if s := snippet("  one\n two   three ", 7); s != "one two…" {
		t.Fatalf("snippet = %q", s)
	}
	now := time.Date(2026, 10, 30, 15, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		t    time.Time
		want string
	}{
		{now.Add(-time.Hour), "today 14:00"},
		{now.Add(-48 * time.Hour), "Wednesday 15:00"},
		{now.Add(-30 * 24 * time.Hour), "30 September 2026"},
	} {
		if got := when(tc.t, now); got != tc.want {
			t.Errorf("when(%v) = %q, want %q", tc.t, got, tc.want)
		}
	}
}

func TestLibrarianLimits(t *testing.T) {
	st := conversationapi.Stream{Id: "str_1", Person: "user:alex@example.local"}
	for _, tc := range []struct {
		u    conversationapi.Utterance
		want bool
	}{
		{conversationapi.Utterance{Author: st.Person, AuthorKind: "human", Stream: "str_2"}, true},
		{conversationapi.Utterance{Author: "persona:magi", AuthorKind: "persona", Stream: st.Id}, true},
		{conversationapi.Utterance{Author: "persona:magi", AuthorKind: "persona", Stream: "str_2"}, false},
		{conversationapi.Utterance{Author: "user:sam@example.local", AuthorKind: "human", Stream: "str_3"}, false},
	} {
		if got := ownText(tc.u, st); got != tc.want {
			t.Errorf("ownText(%+v) = %v", tc.u, got)
		}
	}
	s := librarianSchema(nil, "never")
	props := s["properties"].(map[string]any)
	if props["links"].(map[string]any)["maxItems"] != 0 || !slices.Equal(props["visibility"].(map[string]any)["enum"].([]string), []string{"private"}) {
		t.Fatalf("schema = %+v", s)
	}
	if !slices.Contains(linkKinds, "changes-subject-from") || len(linkKinds) != 27 {
		t.Fatalf("linkKinds = %v", linkKinds)
	}
}
