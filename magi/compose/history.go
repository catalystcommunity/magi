package compose

// History import: a person brings their earlier messages (a journal, an
// old chat) into Magi. Each message goes into the stream with its own time,
// and the note taker records its facts as of that time.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
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

var importLocks sync.Map // stream id -> *sync.Mutex

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
	// The batch finishes when the client goes away (a gateway timeout), and
	// one import of a stream runs at a time: a retry waits, then skips what
	// the first call did.
	sctx := s.AsService(context.WithoutCancel(ctx))
	st, err := s.Conversation.OpenStream(sctx, conversationapi.OpenStreamRequest{Persona: persona, Person: &caller.Subject})
	if err != nil {
		return magiapi.ImportHistoryResponse{}, err
	}
	lock, _ := importLocks.LoadOrStore(st.Id, &sync.Mutex{})
	lock.(*sync.Mutex).Lock()
	defer lock.(*sync.Mutex).Unlock()
	if st, err = s.Conversation.GetStream(sctx, conversationapi.GetStreamRequest{Id: st.Id}); err != nil {
		return magiapi.ImportHistoryResponse{}, err
	}
	prof := profileFor(persona)
	as := conversationLens{person: caller.Subject, persona: persona}
	vars := map[string]string{"stream": st.Id, "persona": persona}
	if caller.Name != "" {
		vars["owner_name"] = caller.Name
	}
	a := workflowapi.Assignment{Run: "history-import", Owner: caller.Subject, Vars: vars}
	out := magiapi.ImportHistoryResponse{Messages: []magiapi.ImportedMessage{}}
	// A gateway can end a long call while Magi still imports; the client
	// then sends the batch again. A message that the stream already has
	// (same time and text, among the newest ones) is not imported twice.
	have := map[string]string{}
	after := uint64(0)
	if st.LastSeq > 60 {
		after = st.LastSeq - 60
	}
	if l, err := s.Conversation.ListUtterances(sctx, conversationapi.ListUtterancesRequest{Stream: st.Id, AfterSeq: &after, Limit: ptr(uint64(60))}); err == nil {
		for _, u := range l.Utterances {
			if u.Author == caller.Subject {
				have[fmt.Sprintf("%d\n%s", u.CreatedMs, u.Text)] = u.Id
			}
		}
	}
	for _, h := range req.Messages {
		if id, ok := have[fmt.Sprintf("%d\n%s", h.AtMs, h.Text)]; ok {
			out.Messages = append(out.Messages, magiapi.ImportedMessage{Id: id, AtMs: h.AtMs, Notes: []string{"already imported"}})
			continue
		}
		start := time.Now()
		at := time.UnixMilli(int64(h.AtMs))
		u, err := s.Conversation.Post(sctx, conversationapi.PostRequest{Stream: st.Id, Text: h.Text, Author: &caller.Subject,
			AuthorKind: ptr("human"), Modality: ptr("text"), CreatedMs: ptr(h.AtMs)})
		if err != nil {
			return out, err
		}
		item := magiapi.ImportedMessage{Id: u.Id, AtMs: h.AtMs, Notes: []string{}}
		if s.Cfg.NoteTaker && s.LibrarianModel != nil && hasTool(prof.tools, "remember") {
			hctx := withHistoryTime(sctx, at)
			kc := s.knowledgeContext(hctx, caller.Subject, h.Text)
			var log turnLog
			// A model host can be away for a moment (a restart); an import
			// must not lose the facts of a message, so try again.
			for try := 0; try < 3; try++ {
				log = turnLog{}
				var refs []conversationapi.Ref
				// No "conversation before": imported messages stand alone, and
				// a small model repeats the facts of earlier messages.
				item.Notes = append(item.Notes, s.applyNotes(hctx, a, as, u, kc, nil, &log, &refs)...)
				if log.Notes == nil || log.Notes.Error == "" {
					break
				}
				time.Sleep(5 * time.Second)
			}
			if log.Notes != nil && log.Notes.Error != "" {
				item.Error = ptr(log.Notes.Error)
			}
			if body, err := json.Marshal(log); err == nil {
				s.Conversation.Annotate(sctx, conversationapi.AnnotateRequest{Utterance: u.Id, Kind: "interpretation", Body: string(body)})
			}
		}
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
