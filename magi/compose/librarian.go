package compose

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/catalystcommunity/magi/magi/model"
	conversationapi "github.com/catalystcommunity/magi/primitives/conversation/api"
)

// The librarian classifies each utterance after it is said (and each
// imported one): the links to earlier utterances and records, and the
// visibility. The conversation service applies a link when its confidence
// is at or above the threshold of the person, and asks the person
// otherwise. It applies a more private visibility with no question, and
// it asks before a message becomes shared (except in a public persona,
// where shared is the default). The examples of the person's earlier answers go into
// the prompt, so the librarian learns from them.

const librarianPrompt = `You are the librarian of a conversation graph. You classify one new message.

1. Links. Find how the new message relates to the earlier messages listed. Use only ids from the lists. A message can have links to several earlier messages; when only a part of the message relates to a target, give that part as "part" (an exact substring of the new message). Usual kinds:
   reply-to, answers, asks-about, acknowledges, thanks,
   continues, resumes, follows-up, changes-subject-from, same-subject-as,
   elaborates, clarifies, restates, summarizes, quotes, example-of, refers-to,
   agrees-with, disagrees-with, contradicts, confirms, corrects, contrasts-with,
   requests, decides, fulfills, cancels.
   Use only these kinds. Give no link when there is no clear relation; a shared word ("battery", "hello") is not a relation. Do not repeat the existing links.
2. Visibility. %s

Confidence is your probability (0 to 1) that the person would agree with the decision. Be honest: the person is asked only about decisions with a low confidence, and a wrong decision with a high confidence is never checked. When a reasonable person could decide the other way (for example a message about one member that the household also needs, or a household secret such as a password), give at most 0.6.

Answer with JSON only, in this form:
{"links": [{"to": "<id>", "kind": "<kind>", "part": "<substring or empty>", "confidence": 0.0, "why": "<short>"}],
 "visibility": "private" or "shared", "visibility_confidence": 0.0, "visibility_why": "<short>"}`

var sharingRule = map[string]string{
	"private": `The persona is private by default. "shared" means that all members of the household (the audience of the persona) can read the message. Choose "shared" only when the message is clearly meant for the household, for example "tell everyone the plumber comes on Tuesday", or a household fact that the person wants others to know. Otherwise "private".`,
	"public":  `The persona is shared by default: the household (the audience of the persona) can read its messages. Choose "private" when the message is personal: health, money, a surprise or a gift, feelings about another person, work secrets, or anything the person would not want the other members to read. Otherwise "shared".`,
}

// linkKinds are the kinds in librarianPrompt.
var linkKinds = strings.Fields(`reply-to answers asks-about acknowledges thanks
	continues resumes follows-up changes-subject-from same-subject-as
	elaborates clarifies restates summarizes quotes example-of refers-to
	agrees-with disagrees-with contradicts confirms corrects contrasts-with
	requests decides fulfills cancels`)

// librarianSchema limits the answer: link targets only from the candidates
// (ids), the known kinds, and the visibility values of the persona.
func librarianSchema(ids []string, sharing string) map[string]any {
	str := map[string]any{"type": "string"}
	num := map[string]any{"type": "number", "minimum": 0, "maximum": 1}
	link := map[string]any{"type": "object", "additionalProperties": false,
		"required": []string{"to", "kind", "part", "confidence", "why"},
		"properties": map[string]any{"to": map[string]any{"type": "string", "enum": ids}, "kind": map[string]any{"type": "string", "enum": linkKinds},
			"part": str, "confidence": num, "why": str}}
	links := map[string]any{"type": "array", "items": link, "maxItems": 8}
	if len(ids) == 0 {
		links = map[string]any{"type": "array", "maxItems": 0}
	}
	vis := []string{"private", "shared"}
	if sharing == "never" {
		vis = []string{"private"}
	}
	return map[string]any{"type": "object", "additionalProperties": false,
		"required": []string{"links", "visibility", "visibility_confidence", "visibility_why"},
		"properties": map[string]any{"links": links, "visibility": map[string]any{"type": "string", "enum": vis},
			"visibility_confidence": num, "visibility_why": str}}
}

// ownText is true when u can go into the context of the librarian for
// stream st: text of the person, or of the persona in this stream. Text of
// other members (shared messages that search finds) can tell the librarian
// what to decide, so it stays out (docs/prompt-injection.md, 5.1).
func ownText(u conversationapi.Utterance, st conversationapi.Stream) bool {
	return (u.AuthorKind != "persona" && u.Author == st.Person) || (u.AuthorKind == "persona" && u.Stream == st.Id)
}

type librarianAnswer struct {
	Links []struct {
		To         string  `json:"to"`
		Kind       string  `json:"kind"`
		Part       string  `json:"part"`
		Confidence float64 `json:"confidence"`
		Why        string  `json:"why"`
	} `json:"links"`
	Visibility           string  `json:"visibility"`
	VisibilityConfidence float64 `json:"visibility_confidence"`
	VisibilityWhy        string  `json:"visibility_why"`
}

// Librarian classifies utterances until ctx ends. Each Magi replica runs
// it; the claims of the conversation service keep replicas from doing the
// same utterance.
func (s *System) Librarian(ctx context.Context) {
	if s.LibrarianModel == nil {
		return
	}
	for ctx.Err() == nil {
		sctx := s.AsService(ctx)
		res, err := s.Conversation.ClaimUnclassified(sctx, conversationapi.ClaimUnclassifiedRequest{Limit: ptr(uint64(4)), LeaseMs: ptr(uint64(300000))})
		if err != nil {
			s.Log.Warn("librarian claim", "err", err)
			sleep(ctx, 10*time.Second)
			continue
		}
		if len(res.Utterances) == 0 {
			sleep(ctx, 3*time.Second)
			continue
		}
		for _, u := range res.Utterances {
			start := time.Now()
			served, err := s.classify(sctx, u)
			if err != nil {
				// The claim ends after its lease and the utterance is tried
				// again; a busy local model gets time to become free.
				s.Log.Warn("librarian", "utterance", u.Id, "err", err)
				sleep(ctx, 30*time.Second)
				break
			}
			if _, err := s.Conversation.MarkClassified(sctx, conversationapi.MarkClassifiedRequest{Utterance: u.Id}); err != nil {
				s.Log.Warn("librarian mark", "utterance", u.Id, "err", err)
			}
			s.Log.Info("librarian classified", "utterance", u.Id, "model", served, "ms", time.Since(start).Milliseconds())
		}
	}
}

// classify proposes the links and the visibility of u. ctx must carry the
// service token.
func (s *System) classify(ctx context.Context, u conversationapi.Utterance) (string, error) {
	st, err := s.Conversation.GetStream(ctx, conversationapi.GetStreamRequest{Id: u.Stream})
	if err != nil {
		return "", err
	}
	persona, err := s.Conversation.GetPersona(ctx, conversationapi.GetPersonaRequest{Id: st.Persona})
	if err != nil {
		return "", err
	}
	as := conversationLens{person: st.Person, persona: st.Persona}
	prev, err := s.Conversation.ListUtterances(ctx, conversationapi.ListUtterancesRequest{Stream: st.Id, BeforeSeq: ptr(u.Seq), Limit: ptr(uint64(8)),
		Person: as.p(), Persona: as.v()})
	if err != nil {
		return "", err
	}
	known := map[string]conversationapi.Utterance{}
	var recent []conversationapi.Utterance
	for _, p := range prev.Utterances {
		if ownText(p, st) {
			recent = append(recent, p)
			known[p.Id] = p
		}
	}
	var older []conversationapi.Utterance
	if hits, err := s.Conversation.Search(ctx, conversationapi.SearchRequest{Text: u.Text, Limit: ptr(uint64(8)), BeforeMs: ptr(u.CreatedMs),
		Person: as.p(), Persona: as.v()}); err == nil {
		for _, h := range hits.Hits {
			if _, ok := known[h.Utterance.Id]; !ok && h.Utterance.Id != u.Id && ownText(h.Utterance, st) {
				older = append(older, h.Utterance)
				known[h.Utterance.Id] = h.Utterance
			}
		}
	}
	have, err := s.Conversation.ListLinks(ctx, conversationapi.ListLinksRequest{Utterance: u.Id, AllStates: ptr(true)})
	if err != nil {
		return "", err
	}
	exists := map[string]bool{}
	var existing strings.Builder
	for _, l := range have.Links {
		exists[l.Kind+" "+l.Target.Ref] = true
		if l.State == "current" {
			fmt.Fprintf(&existing, "- %s %s %s\n", l.Kind, l.Target.Kind, l.Target.Ref)
		}
	}

	msgs := librarianMessages(librarianInput{persona: persona.Name, sharing: persona.Sharing, person: st.Person,
		prev: recent, older: older, existing: existing.String(), examples: s.examplesText(ctx, as), msg: u}, time.Now())
	var m model.Message
	if sc, ok := s.LibrarianModel.(model.SchemaChatter); ok {
		ids := make([]string, 0, len(known))
		for id := range known {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		m, err = sc.ChatSchema(ctx, msgs, librarianSchema(ids, persona.Sharing), 4096)
	} else {
		m, err = s.LibrarianModel.Chat(ctx, msgs, nil, 800)
	}
	if err != nil {
		return "", err
	}
	var ans librarianAnswer
	if err := json.Unmarshal([]byte(jsonObject(m.Content)), &ans); err != nil {
		return m.Served, fmt.Errorf("librarian answer is not JSON: %w", err)
	}

	for _, l := range ans.Links {
		t, ok := known[l.To]
		if !ok || exists[l.Kind+" "+l.To] || l.Confidence <= 0 || !slices.Contains(linkKinds, strings.ToLower(strings.TrimSpace(l.Kind))) {
			continue
		}
		nl := conversationapi.NewLink{Target: conversationapi.Ref{Kind: "utterance", Ref: t.Id}, Kind: strings.ToLower(strings.TrimSpace(l.Kind)),
			Confidence: ptr(min(l.Confidence, 1)), Note: optional(l.Why)}
		if part := strings.TrimSpace(l.Part); part != "" && part != strings.TrimSpace(u.Text) {
			if i := strings.Index(u.Text, part); i >= 0 {
				nl.Span = &conversationapi.Span{Start: uint64(len([]rune(u.Text[:i]))), End: uint64(len([]rune(u.Text[:i+len(part)])))}
				nl.Weight = ptr(max(0.05, float64(len(part))/float64(len(u.Text))))
			}
		}
		q := fmt.Sprintf("This message %s “%s”. Is that right?", strings.ReplaceAll(nl.Kind, "-", " "), snippet(t.Text, 80))
		if _, err := s.Conversation.PutLink(ctx, conversationapi.PutLinkRequest{Utterance: u.Id, Link: nl, Question: &q}); err != nil {
			s.Log.Warn("librarian link", "utterance", u.Id, "kind", nl.Kind, "err", err)
		}
		exists[l.Kind+" "+l.To] = true
	}

	// The default of the persona needs no decision; a change from it does.
	if persona.Sharing != "never" && (ans.Visibility == "shared" || ans.Visibility == "private") &&
		!(persona.Sharing == "private" && ans.Visibility == "private") && u.VisibilityBy == "" {
		q := fmt.Sprintf("Can the household read this message? I think %s: %s", ans.Visibility, ans.VisibilityWhy)
		if _, err := s.Conversation.SetVisibility(ctx, conversationapi.SetVisibilityRequest{Utterance: u.Id, Visibility: ans.Visibility,
			Confidence: ptr(min(ans.VisibilityConfidence, 1)), Question: &q, Note: optional(ans.VisibilityWhy)}); err != nil {
			s.Log.Warn("librarian visibility", "utterance", u.Id, "err", err)
		}
	}
	if body, err := json.Marshal(map[string]any{"model": m.Served, "answer": ans}); err == nil {
		s.Conversation.Annotate(ctx, conversationapi.AnnotateRequest{Utterance: u.Id, Kind: "librarian", Body: string(body)})
	}
	return m.Served, nil
}

// librarianInput is what the librarian sees of one new utterance.
type librarianInput struct {
	persona, sharing, person string
	prev, older              []conversationapi.Utterance
	existing, examples       string
	msg                      conversationapi.Utterance
}

// librarianMessages is the prompt for one classification.
func librarianMessages(in librarianInput, now time.Time) []model.Message {
	rule, ok := sharingRule[in.sharing]
	if !ok {
		rule = `The persona never shares. Answer "private".`
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Persona: %s. Person: %s.\n\n", in.persona, strings.TrimPrefix(in.person, "user:"))
	b.WriteString("Earlier messages, oldest first:\n" + describeUtterances(in.prev, in.person, now))
	if len(in.older) > 0 {
		b.WriteString("\nOlder messages with similar words:\n" + describeUtterances(in.older, in.person, now))
	}
	if in.existing != "" {
		b.WriteString("\nExisting links of the new message:\n" + in.existing)
	}
	b.WriteString(in.examples)
	who := "the person"
	if in.msg.AuthorKind == "persona" {
		who = "the persona"
	}
	fmt.Fprintf(&b, "\nThe new message [%s], by %s:\n%s\n", in.msg.Id, who, in.msg.Text)
	return []model.Message{{Role: "system", Content: fmt.Sprintf(librarianPrompt, rule)}, {Role: "user", Content: b.String()}}
}

// examplesText lists the recent decisions of the person, so that the
// librarian follows them.
func (s *System) examplesText(ctx context.Context, as conversationLens) string {
	var b strings.Builder
	for _, task := range []string{"visibility", "link"} {
		ex, err := s.Conversation.ListExamples(ctx, conversationapi.ListExamplesRequest{Task: task, Limit: ptr(uint64(8)), Person: as.p()})
		if err != nil || len(ex.Examples) == 0 {
			continue
		}
		var ids []string
		for _, e := range ex.Examples {
			ids = append(ids, e.Utterance)
		}
		got, err := s.Conversation.GetUtterances(ctx, conversationapi.GetUtterancesRequest{Ids: ids})
		if err != nil {
			continue
		}
		text := msgIndex(got.Utterances)
		fmt.Fprintf(&b, "\nEarlier %s decisions of the person (follow them):\n", task)
		for _, e := range ex.Examples {
			proposed := ""
			if e.Proposed != "" && e.Proposed != e.Label {
				proposed = " (not " + e.Proposed + ")"
			}
			fmt.Fprintf(&b, "- %q: %s%s", snippet(text[e.Utterance].Text, 120), e.Label, proposed)
			if e.Note != nil && *e.Note != "" {
				fmt.Fprintf(&b, ", because %s", *e.Note)
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}

// jsonObject returns the outermost JSON object in s, without code fences
// or other text around it.
func jsonObject(s string) string {
	i, j := strings.Index(s, "{"), strings.LastIndex(s, "}")
	if i < 0 || j < i {
		return s
	}
	return s[i : j+1]
}
