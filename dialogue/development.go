package dialogue

import (
	"bytes"
	"context"
	"encoding/json"
	w "github.com/catalystcommunity/magi/generated/go"
	"github.com/catalystcommunity/magi/workshop"
	"io"
	"net/http"
	"strings"
	"time"
)

func (s *Web) development(ctx context.Context, wr http.ResponseWriter, r *http.Request) {
	fail := func(code int, message string) { http.Error(wr, message, code) }
	reply := func(v any) { wr.Header().Set("Content-Type", "application/json"); json.NewEncoder(wr).Encode(v) }
	if s.Tasks == nil || s.TaskEvidence == nil {
		fail(503, "Task runtime is not configured.")
		return
	}
	decode := func(v any) bool {
		if r.Header.Get("Content-Type") != "application/json" {
			fail(415, "JSON required")
			return false
		}
		d := json.NewDecoder(http.MaxBytesReader(wr, r.Body, 8192))
		d.DisallowUnknownFields()
		if d.Decode(v) != nil || d.Decode(new(any)) != io.EOF {
			fail(400, "invalid request")
			return false
		}
		return true
	}
	adapter := Adapter{GraphActor: s.Graph.Profile.Actor, GraphOwner: s.Graph.Profile.Owner}
	if r.Method == "POST" && r.URL.Path == "/api/develop" {
		var v struct {
			ID         string    `json:"id"`
			Task       string    `json:"task"`
			Session    string    `json:"session"`
			ObservedAt time.Time `json:"observed_at"`
		}
		if !decode(&v) {
			return
		}
		if !name.MatchString(v.ID) || !name.MatchString(v.Session) || strings.TrimSpace(v.Task) == "" || len(v.Task) > 3000 || v.ObservedAt.IsZero() || v.ObservedAt.Nanosecond()%1000000 != 0 {
			fail(400, "invalid task")
			return
		}
		record := w.InteractionRecord{Header: adapter.header(v.ID, "interaction", v.ObservedAt, []w.RecordRef{}), PointId: "demo-browser", Channel: "text", Sequence: 1, Content: literal(v.Task), Conversations: []string{v.Session}, ReplyHints: []w.RecordRef{}, IdentityEvidence: []w.RecordRef{}}
		receipt, e := s.Graph.Append(ctx, w.ConversationAppend{Node: record, Sessions: []string{v.Session}})
		if e != nil {
			fail(503, "Task source was not confirmed. No workflow was submitted.")
			return
		}
		run, e := s.Tasks.Submit(ctx, v.ID, v.Task)
		if e != nil {
			fail(503, "Task admission is uncertain. Read its run ID before any retry.")
			return
		}
		input := w.SessionInput{Header: adapter.header(v.ID+"-input", "session-input", v.ObservedAt, []w.RecordRef{receipt.Ref}), SessionId: v.Session, Sources: []w.RecordRef{receipt.Ref}, Interpretations: []w.RecordRef{}, Content: literal(v.Task), CommandId: v.ID, Admission: w.RecordRef{Authority: s.Tasks.Profile.Recipient.Identity, Kind: "workflow-run", RecordId: v.ID}}
		if _, e = s.Graph.Append(ctx, w.ConversationAppend{Node: input, Sessions: []string{v.Session}}); e != nil {
			fail(503, "Task was admitted, but its graph link was not confirmed. Inspect its run ID.")
			return
		}
		reply(run)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/development/")
	sourceBundle := strings.HasSuffix(id, "/source")
	if sourceBundle {
		id = strings.TrimSuffix(id, "/source")
	}
	if r.Method == "GET" && name.MatchString(id) {
		run, e := s.Tasks.Read(ctx, id)
		if e != nil || !bytes.Equal(w.EncodeWorkflowDefinitionRef(run.Request.Definition), w.EncodeWorkflowDefinitionRef(workshop.Reference(s.Tasks.Profile.Owner))) {
			fail(403, "Task is unavailable.")
			return
		}
		record, e := s.TaskEvidence.Read(ctx, s.Tasks.Profile.Owner, id)
		if e != nil {
			if sourceBundle {
				fail(409, "Source evidence is unavailable.")
				return
			}
			reply(map[string]any{"workflow": run, "evidence": "not available yet"})
			return
		}
		if sourceBundle {
			bundle, err := workshop.PrepareSource(s.Tasks.Profile.Owner, s.Tasks.Profile.Recipient.Identity, record)
			if err != nil {
				fail(409, "A changed candidate with matching successful checks is required.")
				return
			}
			reply(bundle)
			return
		}
		reply(map[string]any{"workflow": run, "evidence": record})
		return
	}
	if r.Method != "POST" || (r.URL.Path != "/api/development/control" && r.URL.Path != "/api/development/review") {
		fail(404, "not found")
		return
	}
	var v struct {
		ID     string `json:"id"`
		Run    string `json:"run"`
		Action string `json:"action"`
		Digest string `json:"digest"`
		Note   string `json:"note"`
	}
	if !decode(&v) {
		return
	}
	if !name.MatchString(v.ID) || !name.MatchString(v.Run) {
		fail(400, "invalid control ID")
		return
	}
	run, e := s.Tasks.Read(ctx, v.Run)
	if e != nil || !bytes.Equal(w.EncodeWorkflowDefinitionRef(run.Request.Definition), w.EncodeWorkflowDefinitionRef(workshop.Reference(s.Tasks.Profile.Owner))) {
		fail(403, "Task is unavailable.")
		return
	}
	record, e := s.TaskEvidence.Read(ctx, s.Tasks.Profile.Owner, v.Run)
	if e != nil {
		fail(503, "Task evidence is unavailable.")
		return
	}
	if r.URL.Path == "/api/development/review" {
		if v.Digest == "" || v.Digest != record.Digest || strings.TrimSpace(v.Note) == "" || len(v.Note) > 1000 {
			fail(409, "Review must name the retained candidate digest and include a note.")
			return
		}
		source := w.RecordRef{Authority: s.Graph.Profile.Actor.Identity, Kind: "interaction", RecordId: v.Run}
		original, e := s.Graph.Read(ctx, source)
		if e != nil {
			fail(503, "Original task context is unavailable.")
			return
		}
		at := time.Now().UTC().Truncate(time.Millisecond)
		review := w.InteractionRecord{Header: adapter.header(v.ID, "interaction", at, []w.RecordRef{source}), PointId: "demo-browser", Channel: "text", Sequence: 1, Content: literal("Review of candidate " + v.Digest + ": " + v.Note), Conversations: original.Entry.Sessions, ReplyHints: []w.RecordRef{source}, IdentityEvidence: []w.RecordRef{}}
		receipt, e := s.Graph.Append(ctx, w.ConversationAppend{Node: review, Sessions: original.Entry.Sessions})
		if e != nil {
			fail(503, "Review record was not confirmed.")
			return
		}
		result, e := s.Tasks.Review(ctx, run, v.Note, receipt.Ref)
		if e != nil {
			fail(409, "Review result is uncertain or the task revision changed. Read the workflow before another action.")
			return
		}
		reply(result)
		return
	}
	if v.Action != "hold" && v.Action != "release" && v.Action != "cleanup" {
		fail(400, "Unknown control. Hold is separate from pause and command cancellation.")
		return
	}
	raw, _ := json.Marshal(v)
	if e = s.TaskEvidence.Begin(ctx, s.Tasks.Profile.Owner, v.Run, v.ID, "human-control", raw); e != nil {
		fail(409, "Control already exists. Inspect its evidence before another action.")
		return
	}
	if v.Action == "cleanup" {
		e = s.TaskEvidence.Cleanup(ctx, s.Tasks.Profile.Owner, v.Run)
	} else {
		e = s.TaskEvidence.Hold(ctx, s.Tasks.Profile.Owner, v.Run, v.Action == "hold")
	}
	outcome := "returned"
	result := map[string]any{"action": v.Action}
	if e != nil {
		outcome = "unknown"
		result["error"] = "Control was not confirmed. A hold or active task can prevent cleanup."
	} else if v.Action == "hold" && record.Container.ID != "" {
		result["shell"] = record.Container.ShellHint()
		result["note"] = "Joining is live. The agent is not paused. The hard environment deadline still applies."
	}
	encoded, _ := json.Marshal(result)
	if e = s.TaskEvidence.Finish(ctx, s.Tasks.Profile.Owner, v.Run, v.ID, encoded, outcome); e != nil {
		fail(503, "Control receipt could not be saved.")
		return
	}
	reply(result)
}
