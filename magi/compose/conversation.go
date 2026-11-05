package compose

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/catalystcommunity/magi/lib/rpc"
	magiapi "github.com/catalystcommunity/magi/magi/api"
	artifactsapi "github.com/catalystcommunity/magi/primitives/artifacts/api"
	conversationapi "github.com/catalystcommunity/magi/primitives/conversation/api"
	speechapi "github.com/catalystcommunity/magi/primitives/speech/api"
	workflowapi "github.com/catalystcommunity/magi/primitives/workflow/api"
)

// EnsurePersonas makes the Magi persona when it is not there. Magi is
// private by default. ctx must carry the service token.
func (s *System) EnsurePersonas(ctx context.Context) error {
	for _, id := range []string{Persona, Engineering} {
		if _, err := s.Conversation.GetPersona(ctx, conversationapi.GetPersonaRequest{Id: id}); rpc.CodeOf(err) != 404 {
			if err != nil {
				return err
			}
			continue
		}
		if _, err := s.Conversation.PutPersona(ctx, conversationapi.PutPersonaRequest{Id: id, Name: profiles[id].name, Sharing: "private"}); err != nil {
			return err
		}
	}
	return nil
}

func personaOr(p *string) string {
	if v := deref(p); v != "" {
		return v
	}
	return Persona
}

func (m *Service) Say(ctx context.Context, req magiapi.SayRequest) (magiapi.SayResponse, error) {
	caller, err := rpc.Caller(ctx)
	if err != nil {
		return magiapi.SayResponse{}, err
	}
	s, uctx := m.S, AsCaller(ctx)
	text, modality := strings.TrimSpace(deref(req.Text)), "text"
	var audioDigest *string
	var confidence *float64
	var transcriber *string
	if req.Audio != nil && len(*req.Audio) > 0 {
		audio := *req.Audio
		mt := deref(req.AudioType)
		if mt == "" {
			mt = "audio/wav"
		}
		tr, err := s.Speech.Transcribe(uctx, speechapi.TranscribeRequest{Audio: audio, MediaType: mt})
		if err != nil {
			return magiapi.SayResponse{}, fmt.Errorf("transcribe: %w", err)
		}
		confidence, modality = tr.Confidence, "voice"
		if text == "" {
			text = strings.TrimSpace(tr.Text)
			transcriber = &tr.Engine
		}
	}
	if text == "" {
		return magiapi.SayResponse{}, rpc.Invalid("text or audio is required")
	}
	persona := personaOr(req.Persona)
	// "Computer, ..." and "Engineering, ...": the person names the persona
	// that they talk to, as on the bridge of a starship.
	p := s.addressed(uctx, text)
	if p != "" {
		persona = p
	} else if deref(req.WakeWord) {
		// Not for Magi: keep nothing of it.
		return magiapi.SayResponse{Ignored: ptr(true), Message: magiapi.Message{Refs: []magiapi.Ref{}, Links: []magiapi.MessageLink{}}}, nil
	}
	if req.Audio != nil && len(*req.Audio) > 0 {
		mt := firstNonEmpty(deref(req.AudioType), "audio/wav")
		art, err := s.Artifacts.Put(uctx, artifactsapi.PutRequest{MediaType: mt, Body: *req.Audio, Labels: map[string]string{"kind": "voice-input"}})
		if err != nil {
			return magiapi.SayResponse{}, err
		}
		audioDigest = &art.Digest
	}
	st, err := s.Conversation.OpenStream(uctx, conversationapi.OpenStreamRequest{Persona: persona})
	if err != nil {
		return magiapi.SayResponse{}, err
	}
	u, err := s.Conversation.Post(uctx, conversationapi.PostRequest{
		Stream: st.Id, Text: text, Modality: ptr(modality), Audio: audioDigest, TranscriptConfidence: confidence, GeneratedBy: transcriber,
	})
	if err != nil {
		return magiapi.SayResponse{}, err
	}
	vars := map[string]string{"stream": st.Id, "utterance": u.Id, "persona": persona}
	if caller.Name != "" {
		vars["owner_name"] = caller.Name
	}
	if deref(req.SpeakReply) {
		vars["speak"] = "true"
	}
	if v := deref(req.Voice); v != "" {
		vars["voice"] = v
	}
	if _, err := s.Workflow.Start(s.AsService(ctx), workflowapi.StartRequest{Definition: PersonaTurn, Vars: vars, Owner: ptr(caller.Subject), Key: ptr(u.Id)}); err != nil {
		return magiapi.SayResponse{}, fmt.Errorf("start reply: %w", err)
	}
	return magiapi.SayResponse{Message: message(u, st.Persona, nil)}, nil
}

func (m *Service) Read(ctx context.Context, req magiapi.ReadRequest) (magiapi.ReadResponse, error) {
	uctx := AsCaller(ctx)
	st, err := m.S.Conversation.OpenStream(uctx, conversationapi.OpenStreamRequest{Persona: personaOr(req.Persona)})
	if err != nil {
		return magiapi.ReadResponse{}, err
	}
	res, err := m.S.Conversation.ListUtterances(uctx, conversationapi.ListUtterancesRequest{
		Stream: st.Id, AfterSeq: req.AfterSeq, BeforeSeq: req.BeforeSeq, Limit: req.Limit, WaitMs: req.WaitMs,
	})
	if err != nil {
		return magiapi.ReadResponse{}, err
	}
	msgs, err := m.S.messages(uctx, res.Utterances, res.Links, false)
	return magiapi.ReadResponse{Messages: msgs, LastSeq: res.LastSeq, Stream: st.Id}, err
}

func (m *Service) Search(ctx context.Context, req magiapi.SearchRequest) (magiapi.SearchResponse, error) {
	uctx := AsCaller(ctx)
	res, err := m.S.Conversation.Search(uctx, conversationapi.SearchRequest{Text: req.Text, Limit: req.Limit})
	if err != nil {
		return magiapi.SearchResponse{}, err
	}
	var us []conversationapi.Utterance
	for _, h := range res.Hits {
		us = append(us, h.Utterance)
	}
	msgs, err := m.S.messages(uctx, us, nil, true)
	return magiapi.SearchResponse{Messages: msgs}, err
}

func (m *Service) Trace(ctx context.Context, req magiapi.TraceRequest) (magiapi.TraceResponse, error) {
	uctx := AsCaller(ctx)
	res, err := m.S.Conversation.Trace(uctx, conversationapi.TraceRequest{Utterance: req.Message, Direction: req.Direction, Depth: req.Depth, Tentative: req.Tentative})
	if err != nil {
		return magiapi.TraceResponse{}, err
	}
	msgs, err := m.S.messages(uctx, res.Utterances, res.Links, false)
	if err != nil {
		return magiapi.TraceResponse{}, err
	}
	return magiapi.TraceResponse{Messages: msgs, Links: m.S.messageLinks(uctx, res.Links, msgIndex(res.Utterances))}, nil
}

func (m *Service) ListLinks(ctx context.Context, req magiapi.ListLinksRequest) (magiapi.ListLinksResponse, error) {
	uctx := AsCaller(ctx)
	res, err := m.S.Conversation.ListLinks(uctx, conversationapi.ListLinksRequest{Utterance: req.Message, AllStates: req.All})
	if err != nil {
		return magiapi.ListLinksResponse{}, err
	}
	return magiapi.ListLinksResponse{Links: m.S.messageLinks(uctx, res.Links, nil)}, nil
}

func (m *Service) Link(ctx context.Context, req magiapi.LinkRequest) (magiapi.MessageLink, error) {
	uctx := AsCaller(ctx)
	nl := conversationapi.NewLink{Target: conversationapi.Ref{Kind: req.Target.Kind, Ref: req.Target.Ref, Title: req.Target.Title},
		Kind: req.Kind, Weight: req.Weight, Note: req.Note}
	if req.SpanStart != nil && req.SpanEnd != nil {
		nl.Span = &conversationapi.Span{Start: *req.SpanStart, End: *req.SpanEnd}
	}
	res, err := m.S.Conversation.PutLink(uctx, conversationapi.PutLinkRequest{Utterance: req.Message, Link: nl, Replaces: req.Replaces})
	if err != nil {
		return magiapi.MessageLink{}, err
	}
	return m.S.messageLinks(uctx, []conversationapi.Link{res.Link}, nil)[0], nil
}

func (m *Service) Unlink(ctx context.Context, req magiapi.UnlinkRequest) (magiapi.MessageLink, error) {
	uctx := AsCaller(ctx)
	l, err := m.S.Conversation.RetractLink(uctx, conversationapi.RetractLinkRequest{Id: req.Link, Note: req.Note})
	if err != nil {
		return magiapi.MessageLink{}, err
	}
	return m.S.messageLinks(uctx, []conversationapi.Link{l}, nil)[0], nil
}

func (m *Service) SetVisibility(ctx context.Context, req magiapi.SetVisibilityRequest) (magiapi.Message, error) {
	uctx := AsCaller(ctx)
	res, err := m.S.Conversation.SetVisibility(uctx, conversationapi.SetVisibilityRequest{Utterance: req.Message, Visibility: req.Visibility, Note: req.Note})
	if err != nil {
		return magiapi.Message{}, err
	}
	msgs, err := m.S.messages(uctx, []conversationapi.Utterance{res.Utterance}, nil, true)
	if err != nil || len(msgs) == 0 {
		return magiapi.Message{}, err
	}
	return msgs[0], nil
}

func (m *Service) ListReviews(ctx context.Context, req magiapi.ListReviewsRequest) (magiapi.ListReviewsResponse, error) {
	uctx := AsCaller(ctx)
	res, err := m.S.Conversation.ListReviews(uctx, conversationapi.ListReviewsRequest{Limit: req.Limit})
	if err != nil {
		return magiapi.ListReviewsResponse{}, err
	}
	out := magiapi.ListReviewsResponse{Reviews: []magiapi.ReviewItem{}}
	for _, v := range res.Reviews {
		msgs, err := m.S.messages(uctx, []conversationapi.Utterance{{Id: v.Utterance}}, nil, true)
		if err != nil || len(msgs) == 0 {
			continue
		}
		it := magiapi.ReviewItem{Id: v.Id, Task: v.Task, Question: v.Question, Proposed: v.Proposed, Choices: v.Choices,
			Confidence: v.Confidence, Message: msgs[0], CreatedMs: v.CreatedMs}
		if v.Link != nil {
			links, _ := m.S.Conversation.ListLinks(uctx, conversationapi.ListLinksRequest{Utterance: v.Utterance, AllStates: ptr(true)})
			for _, l := range links.Links {
				if l.Id == *v.Link && l.Target.Kind == "utterance" {
					if t, err := m.S.messages(uctx, []conversationapi.Utterance{{Id: l.Target.Ref}}, nil, true); err == nil && len(t) > 0 {
						it.Target = &t[0]
					}
				}
			}
		}
		out.Reviews = append(out.Reviews, it)
	}
	return out, nil
}

func (m *Service) AnswerReview(ctx context.Context, req magiapi.AnswerReviewRequest) (magiapi.AnswerReviewResponse, error) {
	v, err := m.S.Conversation.AnswerReview(AsCaller(ctx), conversationapi.AnswerReviewRequest{Id: req.Id, Answer: req.Answer, Note: req.Note})
	return magiapi.AnswerReviewResponse{Status: v.Status}, err
}

func (m *Service) ListPersonas(ctx context.Context, req magiapi.ListPersonasRequest) (magiapi.ListPersonasResponse, error) {
	res, err := m.S.Conversation.ListPersonas(AsCaller(ctx), conversationapi.ListPersonasRequest{})
	if err != nil {
		return magiapi.ListPersonasResponse{}, err
	}
	out := magiapi.ListPersonasResponse{Personas: []magiapi.PersonaInfo{}}
	for _, p := range res.Personas {
		out.Personas = append(out.Personas, magiapi.PersonaInfo{Id: p.Id, Name: p.Name, Sharing: p.Sharing, Members: p.Members})
	}
	return out, nil
}

func (m *Service) PutPersona(ctx context.Context, req magiapi.PutPersonaRequest) (magiapi.PersonaInfo, error) {
	if err := m.S.isOperator(ctx); err != nil {
		return magiapi.PersonaInfo{}, err
	}
	if !strings.HasPrefix(req.Id, "persona:") {
		return magiapi.PersonaInfo{}, rpc.Invalid("a persona id starts with persona:")
	}
	p, err := m.S.Conversation.PutPersona(m.S.AsService(ctx), conversationapi.PutPersonaRequest{Id: req.Id, Name: req.Name, Sharing: req.Sharing, Members: req.Members})
	if err != nil {
		return magiapi.PersonaInfo{}, err
	}
	return magiapi.PersonaInfo{Id: p.Id, Name: p.Name, Sharing: p.Sharing, Members: p.Members}, nil
}

func msgIndex(us []conversationapi.Utterance) map[string]conversationapi.Utterance {
	m := map[string]conversationapi.Utterance{}
	for _, u := range us {
		m[u.Id] = u
	}
	return m
}

// messages converts utterances to messages, with their links. Utterances
// with only an id are read first. With fetchLinks, the links of each
// utterance are read; else links has them. Unreadable ones are left out.
func (s *System) messages(ctx context.Context, us []conversationapi.Utterance, links []conversationapi.Link, fetchLinks bool) ([]magiapi.Message, error) {
	var ids []string
	for _, u := range us {
		if u.Stream == "" {
			ids = append(ids, u.Id)
		}
	}
	if len(ids) > 0 {
		got, err := s.Conversation.GetUtterances(ctx, conversationapi.GetUtterancesRequest{Ids: ids})
		if err != nil {
			return nil, err
		}
		idx := msgIndex(got.Utterances)
		var full []conversationapi.Utterance
		for _, u := range us {
			if u.Stream == "" {
				if g, ok := idx[u.Id]; ok {
					full = append(full, g)
				}
				continue
			}
			full = append(full, u)
		}
		us = full
	}
	if fetchLinks {
		links = nil
		for _, u := range us {
			if l, err := s.Conversation.ListLinks(ctx, conversationapi.ListLinksRequest{Utterance: u.Id}); err == nil {
				links = append(links, l.Links...)
			}
		}
	}
	known := msgIndex(us)
	ml := s.messageLinks(ctx, links, known)
	byFrom := map[string][]magiapi.MessageLink{}
	for i, l := range links {
		byFrom[l.Utterance] = append(byFrom[l.Utterance], ml[i])
	}
	personas := map[string]string{}
	out := []magiapi.Message{}
	for _, u := range us {
		p, ok := personas[u.Stream]
		if !ok {
			if st, err := s.Conversation.GetStream(ctx, conversationapi.GetStreamRequest{Id: u.Stream}); err == nil {
				p = st.Persona
			}
			personas[u.Stream] = p
		}
		out = append(out, message(u, p, byFrom[u.Id]))
	}
	return out, nil
}

// messageLinks converts links, with the snippet and time of utterance
// targets. known has utterances that are already read.
func (s *System) messageLinks(ctx context.Context, links []conversationapi.Link, known map[string]conversationapi.Utterance) []magiapi.MessageLink {
	if known == nil {
		known = map[string]conversationapi.Utterance{}
	}
	var missing []string
	for _, l := range links {
		if _, ok := known[l.Target.Ref]; l.Target.Kind == "utterance" && !ok {
			missing = append(missing, l.Target.Ref)
		}
	}
	if len(missing) > 0 {
		if got, err := s.Conversation.GetUtterances(ctx, conversationapi.GetUtterancesRequest{Ids: missing}); err == nil {
			for _, u := range got.Utterances {
				known[u.Id] = u
			}
		}
	}
	out := make([]magiapi.MessageLink, len(links))
	for i, l := range links {
		ml := magiapi.MessageLink{Id: l.Id, Kind: l.Kind, Target: magiapi.Ref{Kind: l.Target.Kind, Ref: l.Target.Ref, Title: l.Target.Title},
			Weight: l.Weight, Confidence: l.Confidence, Author: l.Author, State: l.State}
		if l.Span != nil {
			ml.SpanStart, ml.SpanEnd = ptr(l.Span.Start), ptr(l.Span.End)
		}
		if t, ok := known[l.Target.Ref]; ok && l.Target.Kind == "utterance" {
			ml.Snippet, ml.TargetMs = ptr(snippet(t.Text, 80)), ptr(t.CreatedMs)
			if ts := l.TargetSpan; ts != nil {
				ml.TargetStart, ml.TargetEnd = ptr(ts.Start), ptr(ts.End)
				if rs := []rune(t.Text); ts.End <= uint64(len(rs)) {
					ml.Snippet = ptr(snippet(string(rs[ts.Start:ts.End]), 80))
				}
			}
		}
		out[i] = ml
	}
	return out
}

func message(u conversationapi.Utterance, persona string, links []magiapi.MessageLink) magiapi.Message {
	m := magiapi.Message{Id: u.Id, Stream: u.Stream, Persona: persona, Seq: u.Seq, Author: u.Author, AuthorKind: u.AuthorKind,
		Modality: u.Modality, Text: u.Text, Audio: u.Audio, GeneratedBy: u.GeneratedBy, CreatedMs: u.CreatedMs,
		Visibility: u.Visibility, VisibilityBy: u.VisibilityBy, Refs: []magiapi.Ref{}, Links: links}
	if m.Links == nil {
		m.Links = []magiapi.MessageLink{}
	}
	for _, a := range u.Attachments {
		m.Refs = append(m.Refs, magiapi.Ref{Kind: a.Kind, Ref: a.Ref, Title: a.Title})
	}
	return m
}

func snippet(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// aliases are more names that a person can use to call a persona.
var aliases = map[string][]string{
	Persona:     {"computer", "magi"},
	Engineering: {"engineering", "engineering manager"},
}

// addressed returns the persona that text calls by name at its start
// ("Computer, what time is it?"), or "". The name must come first and a
// comma, colon, or exclamation mark must follow it, so that "computer
// science is hard" does not call a persona.
func (s *System) addressed(ctx context.Context, text string) string {
	t := strings.ToLower(strings.TrimSpace(text))
	for _, hey := range []string{"hey ", "ok ", "okay "} {
		t = strings.TrimPrefix(t, hey)
	}
	end := strings.IndexAny(t, ",:!")
	if end <= 0 || end > 40 {
		return ""
	}
	name := strings.TrimSpace(t[:end])
	for id, names := range aliases {
		if slices.Contains(names, name) {
			return id
		}
	}
	res, err := s.Conversation.ListPersonas(ctx, conversationapi.ListPersonasRequest{})
	if err != nil {
		return ""
	}
	for _, p := range res.Personas {
		if strings.ToLower(p.Name) == name {
			return p.Id
		}
	}
	return ""
}
