package compose

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	conversationapi "github.com/catalystcommunity/magi/primitives/conversation/api"
)

// conversationLens reads the conversation graph as one person through one
// persona.
type conversationLens struct{ person, persona string }

func (l conversationLens) p() *string { return &l.person }
func (l conversationLens) v() *string { return &l.persona }

// relatedContext finds earlier utterances that probably relate to utt and
// are not in recent: matches of its words, with the utterances that they
// link to and from, so that a match brings its answer or its question.
func (s *System) relatedContext(ctx context.Context, as conversationLens, utt conversationapi.Utterance, recent []conversationapi.Utterance, now time.Time) string {
	skip := map[string]bool{}
	before := utt.CreatedMs
	for _, u := range recent {
		skip[u.Id] = true
		before = min(before, u.CreatedMs)
	}
	res, err := s.Conversation.Search(ctx, conversationapi.SearchRequest{Text: utt.Text, Limit: ptr(uint64(6)), BeforeMs: ptr(before),
		Person: as.p(), Persona: as.v()})
	if err != nil {
		s.Log.Warn("related search", "err", err)
		return ""
	}
	found := map[string]conversationapi.Utterance{}
	for _, h := range res.Hits {
		found[h.Utterance.Id] = h.Utterance
		tr, err := s.Conversation.Trace(ctx, conversationapi.TraceRequest{Utterance: h.Utterance.Id, Direction: ptr("both"), Depth: ptr(uint64(1)),
			Person: as.p(), Persona: as.v()})
		if err != nil {
			continue
		}
		for _, u := range tr.Utterances {
			found[u.Id] = u
		}
	}
	var list []conversationapi.Utterance
	for id, u := range found {
		if !skip[id] {
			list = append(list, u)
		}
	}
	slices.SortFunc(list, func(a, b conversationapi.Utterance) int { return cmp.Compare(a.CreatedMs, b.CreatedMs) })
	if len(list) > 14 {
		list = list[len(list)-14:]
	}
	return describeUtterances(list, as.person, now)
}

// describeUtterances writes one line for each utterance: id, time, who,
// and text.
func describeUtterances(us []conversationapi.Utterance, person string, now time.Time) string {
	var b strings.Builder
	for _, u := range us {
		who := "the person"
		switch {
		case u.AuthorKind == "persona":
			who = "you"
		case u.Author != person:
			who = strings.TrimPrefix(u.Author, "user:") + " (shared with you)"
		}
		fmt.Fprintf(&b, "- [%s] %s, %s: %s\n", u.Id, when(time.UnixMilli(int64(u.CreatedMs)), now), who, snippet(u.Text, 400))
	}
	return b.String()
}

// when is a short, relative time.
// zone is the time zone of the household (MAGI_TIME_ZONE) for the times
// that the models read; New sets it.
var zone = time.UTC

func when(t, now time.Time) string {
	t, now = t.In(zone), now.In(zone)
	switch d := now.Sub(t); {
	case d < 24*time.Hour && t.Day() == now.Day():
		return "today " + t.Format("15:04")
	case d < 7*24*time.Hour:
		return t.Format("Monday 15:04")
	default:
		return t.Format("2 January 2006")
	}
}
