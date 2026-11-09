package compose

// History import: a person brings their earlier messages (a journal, an
// old chat) into Magi. Each message goes into the stream with its own time,
// and the note taker records its facts as of that time.

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/catalystcommunity/magi/lib/rpc"
	conversationapi "github.com/catalystcommunity/magi/primitives/conversation/api"
	workflowapi "github.com/catalystcommunity/magi/primitives/workflow/api"

	magiapi "github.com/catalystcommunity/magi/magi/api"
)

type historyKey struct{}

// withHistoryTime makes the knowledge writes of ctx happen at t.
func withHistoryTime(ctx context.Context, t time.Time) context.Context {
	return context.WithValue(ctx, historyKey{}, t)
}

func historyTime(ctx context.Context) (time.Time, bool) {
	t, ok := ctx.Value(historyKey{}).(time.Time)
	return t, ok
}

func (m *Service) ImportHistory(ctx context.Context, req magiapi.ImportHistoryRequest) (magiapi.ImportHistoryResponse, error) {
	caller, err := rpc.Caller(ctx)
	if err != nil {
		return magiapi.ImportHistoryResponse{}, err
	}
	if !strings.HasPrefix(caller.Subject, "user:") {
		return magiapi.ImportHistoryResponse{}, rpc.Invalid("only a person can import their history")
	}
	if len(req.Messages) == 0 || len(req.Messages) > 20 {
		return magiapi.ImportHistoryResponse{}, rpc.Invalid("give 1 to 20 messages")
	}
	now := time.Now()
	last := uint64(0)
	for _, h := range req.Messages {
		if strings.TrimSpace(h.Text) == "" || len(h.Text) > 8000 {
			return magiapi.ImportHistoryResponse{}, rpc.Invalid("each message needs text (at most 8000 characters)")
		}
		if h.AtMs > uint64(now.UnixMilli()) || h.AtMs < last {
			return magiapi.ImportHistoryResponse{}, rpc.Invalid("times must be in the past and in order")
		}
		last = h.AtMs
	}
	s := m.S
	persona := personaOr(req.Persona)
	sctx := s.AsService(ctx)
	st, err := s.Conversation.OpenStream(sctx, conversationapi.OpenStreamRequest{Persona: persona, Person: &caller.Subject})
	if err != nil {
		return magiapi.ImportHistoryResponse{}, err
	}
	prof := profileFor(persona)
	as := conversationLens{person: caller.Subject, persona: persona}
	vars := map[string]string{"stream": st.Id, "persona": persona}
	if caller.Name != "" {
		vars["owner_name"] = caller.Name
	}
	a := workflowapi.Assignment{Run: "history-import", Owner: caller.Subject, Vars: vars}
	var recent []conversationapi.Utterance
	out := magiapi.ImportHistoryResponse{Messages: []magiapi.ImportedMessage{}}
	for _, h := range req.Messages {
		start := time.Now()
		at := time.UnixMilli(int64(h.AtMs))
		u, err := s.Conversation.Post(sctx, conversationapi.PostRequest{Stream: st.Id, Text: h.Text, Author: &caller.Subject,
			AuthorKind: ptr("human"), Modality: ptr("import"), CreatedMs: ptr(h.AtMs)})
		if err != nil {
			return out, err
		}
		item := magiapi.ImportedMessage{Id: u.Id, AtMs: h.AtMs, Notes: []string{}}
		if s.Cfg.NoteTaker && s.LibrarianModel != nil && hasTool(prof.tools, "remember") {
			hctx := withHistoryTime(sctx, at)
			kc := s.knowledgeContext(hctx, caller.Subject, h.Text)
			log := turnLog{}
			var refs []conversationapi.Ref
			item.Notes = append(item.Notes, s.applyNotes(hctx, a, as, u, kc, recent, &log, &refs)...)
			if log.Notes != nil && log.Notes.Error != "" {
				item.Error = ptr(log.Notes.Error)
			}
			if body, err := json.Marshal(log); err == nil {
				s.Conversation.Annotate(sctx, conversationapi.AnnotateRequest{Utterance: u.Id, Kind: "interpretation", Body: string(body)})
			}
		}
		recent = append(recent, u)
		item.Ms = uint64(time.Since(start).Milliseconds())
		out.Messages = append(out.Messages, item)
	}
	return out, nil
}

func hasTool(tools []string, name string) bool {
	for _, t := range tools {
		if t == name {
			return true
		}
	}
	return false
}
