package dialogue

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"github.com/catalystcommunity/magi/conversation"
	w "github.com/catalystcommunity/magi/generated/go"
	"github.com/catalystcommunity/magi/speech"
	"github.com/catalystcommunity/magi/workshop"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

//go:embed web/index.html web/app.js web/style.css
var assets embed.FS

// Web is a loopback test interaction point. It is not a public identity proxy.
// Signing keys stay in this process. Every API request needs the random token.
type Web struct {
	Client       *Client
	Graph        *conversation.Client
	Speech       *speech.Client
	Tasks        *workshop.Client
	TaskEvidence *workshop.Store
	Host, Token  string
}

func (s *Web) ServeHTTP(wr http.ResponseWriter, r *http.Request) {
	wr.Header().Set("Cache-Control", "no-store")
	wr.Header().Set("X-Content-Type-Options", "nosniff")
	wr.Header().Set("Referrer-Policy", "no-referrer")
	wr.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; media-src 'self' blob:; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
	host, _, e := net.SplitHostPort(s.Host)
	if e != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() || r.Host != s.Host || r.URL.RawQuery != "" || (r.Header.Get("Origin") != "" && r.Header.Get("Origin") != "http://"+s.Host) {
		http.Error(wr, "local origin required", 403)
		return
	}
	if r.Method == "GET" && (r.URL.Path == "/" || r.URL.Path == "/app.js" || r.URL.Path == "/style.css") {
		path := r.URL.Path
		mime := "text/html; charset=utf-8"
		if path == "/" {
			path = "/index.html"
		}
		if path == "/app.js" {
			mime = "text/javascript; charset=utf-8"
		}
		if path == "/style.css" {
			mime = "text/css; charset=utf-8"
		}
		raw, _ := assets.ReadFile("web" + path)
		wr.Header().Set("Content-Type", mime)
		wr.Write(raw)
		return
	}
	if len(s.Token) < 32 || subtle.ConstantTimeCompare([]byte(s.Token), []byte(r.Header.Get("X-Opal-Demo-Token"))) != 1 {
		http.Error(wr, "demo token required", 403)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
	defer cancel()
	if strings.HasPrefix(r.URL.Path, "/api/develop") {
		s.development(ctx, wr, r)
		return
	}
	fail := func(code int, message string) { http.Error(wr, message, code) }
	reply := func(v any) { wr.Header().Set("Content-Type", "application/json"); json.NewEncoder(wr).Encode(v) }
	decode := func(v any) bool {
		if r.Header.Get("Content-Type") != "application/json" {
			fail(415, "JSON required")
			return false
		}
		r.Body = http.MaxBytesReader(wr, r.Body, 8192)
		d := json.NewDecoder(r.Body)
		d.DisallowUnknownFields()
		if d.Decode(v) != nil || d.Decode(new(any)) != io.EOF {
			fail(400, "invalid request")
			return false
		}
		return true
	}
	switch {
	case r.Method == "POST" && r.URL.Path == "/api/turn":
		var v Request
		if !decode(&v) {
			return
		}
		raw, _ := json.Marshal(v)
		if _, e := Parse(string(raw)); e != nil {
			fail(400, "invalid interaction")
			return
		}
		results := []map[string]any{}
		for i := range v.Destinations {
			v.Destination = i
			id := v.InteractionID + "-" + string(rune('0'+i))
			run, e := s.Client.Submit(ctx, id, v)
			result := map[string]any{"run": id, "session": v.Destinations[i].Session}
			if e != nil {
				result["error"] = "Submission result is uncertain. Read this run ID before any resubmission."
			} else {
				result["state"] = run
			}
			results = append(results, result)
		}
		reply(results)
	case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/api/run/"):
		id := strings.TrimPrefix(r.URL.Path, "/api/run/")
		if !name.MatchString(id) {
			fail(400, "invalid run ID")
			return
		}
		run, e := s.Client.Read(ctx, id)
		if e != nil {
			fail(503, "Run is unavailable. Do not submit it again with changed input.")
			return
		}
		reply(run)
	case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/api/evidence/"):
		id := strings.TrimPrefix(r.URL.Path, "/api/evidence/")
		if !name.MatchString(id) {
			fail(400, "invalid run ID")
			return
		}
		out := map[string]any{}
		for _, step := range []string{"prepare", "answer", "present"} {
			v, e := s.Client.CallEvidence(ctx, id, step)
			if e != nil {
				out[step] = "not available"
				continue
			}
			item := map[string]any{"metadata": v}
			if json.Valid(v.Request) {
				item["request"] = json.RawMessage(v.Request)
			}
			if v.Response != nil && json.Valid(*v.Response) {
				item["response"] = json.RawMessage(*v.Response)
			}
			out[step] = item
		}
		reply(out)
	case r.Method == "POST" && r.URL.Path == "/api/history":
		var v struct {
			Session string `json:"session"`
			After   uint64 `json:"after"`
		}
		if !decode(&v) {
			return
		}
		if !name.MatchString(v.Session) {
			fail(400, "invalid session")
			return
		}
		page, e := s.Graph.History(ctx, v.Session, v.After)
		if e != nil {
			fail(503, "Conversation history is unavailable.")
			return
		}
		reply(page)
	case r.Method == "GET" && r.URL.Path == "/api/voices":
		v, e := s.Speech.Capabilities(ctx)
		if e != nil {
			fail(503, "Speech service is unavailable. Text is still available.")
			return
		}
		reply(v)
	case r.Method == "POST" && strings.HasPrefix(r.URL.Path, "/api/transcribe/"):
		id := strings.TrimPrefix(r.URL.Path, "/api/transcribe/")
		if !name.MatchString(id) || r.Header.Get("Content-Type") != "audio/wav" {
			fail(400, "a WAV file and request ID are required")
			return
		}
		raw, e := io.ReadAll(http.MaxBytesReader(wr, r.Body, speech.MaxInputSamples*2+4096))
		if e != nil {
			fail(413, "Audio exceeds 60 seconds.")
			return
		}
		pcm, e := speech.ParseWAV(raw)
		if e != nil {
			fail(400, "Use mono 16-bit PCM WAV at 16000 Hz, up to 60 seconds.")
			return
		}
		hash := sha256.Sum256(pcm)
		digest := hash[:]
		samples := uint64(len(pcm) / 2)
		status, e := s.Speech.Submit(ctx, w.SpeechSubmit{Owner: s.Speech.Profile.Owner, RequestId: id, Kind: "stt", Generation: 1, Deadline: time.Now().UTC().Truncate(time.Millisecond).Add(119 * time.Second), RetentionSeconds: 3600, Sources: []w.RecordRef{}, InputDigest: &digest, InputSamples: &samples})
		if e != nil {
			fail(503, "Transcription submission is uncertain. Check the request ID.")
			return
		}
		if e = s.Speech.Upload(ctx, status.Ref, pcm); e != nil {
			fail(503, "Audio upload is incomplete. Check the request ID.")
			return
		}
		reply(status)
	case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/api/speech/"):
		id := strings.TrimPrefix(r.URL.Path, "/api/speech/")
		if !name.MatchString(id) {
			fail(400, "invalid speech ID")
			return
		}
		status, e := s.Speech.Read(ctx, w.SpeechRef{Owner: s.Speech.Profile.Owner, RequestId: id, Generation: 1})
		if e != nil {
			fail(503, "Speech status is unavailable.")
			return
		}
		reply(status)
	case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/api/audio/"):
		id := strings.TrimPrefix(r.URL.Path, "/api/audio/")
		if !name.MatchString(id) {
			fail(400, "invalid speech ID")
			return
		}
		status, e := s.Speech.Read(ctx, w.SpeechRef{Owner: s.Speech.Profile.Owner, RequestId: id, Generation: 1})
		if e != nil {
			fail(503, "Speech status is unavailable.")
			return
		}
		pcm, e := s.Speech.Audio(ctx, status)
		if e != nil {
			fail(503, "Audio is unavailable or its retention period has ended.")
			return
		}
		raw, e := speech.WAV(pcm, uint32(status.OutputSampleRate))
		if e != nil {
			fail(503, "Audio format is invalid.")
			return
		}
		wr.Header().Set("Content-Type", "audio/wav")
		wr.Write(raw)
	case r.Method == "POST" && r.URL.Path == "/api/presentation":
		var v struct {
			ID         string              `json:"id"`
			Run        string              `json:"run"`
			Session    string              `json:"session"`
			State      w.PresentationState `json:"state"`
			ObservedAt time.Time           `json:"observed_at"`
		}
		if !decode(&v) {
			return
		}
		if !name.MatchString(v.ID) || !name.MatchString(v.Run) || !name.MatchString(v.Session) || v.ObservedAt.IsZero() || v.ObservedAt.Nanosecond()%1000000 != 0 || (v.State != "queued" && v.State != "started" && v.State != "completed" && v.State != "interrupted") {
			fail(400, "invalid presentation")
			return
		}
		source := w.RecordRef{Authority: s.Graph.Profile.Actor.Identity, Kind: "assistant-output", RecordId: v.Run + "-answer"}
		original, e := s.Graph.Read(ctx, source)
		if e != nil {
			fail(503, "Output is unavailable.")
			return
		}
		found := false
		for _, session := range original.Entry.Sessions {
			if session == v.Session {
				found = true
			}
		}
		if !found {
			fail(400, "Output does not belong to this session.")
			return
		}
		a := Adapter{GraphActor: s.Graph.Profile.Actor, GraphOwner: s.Graph.Profile.Owner}
		record := w.PresentationRecord{Header: a.header(v.ID, "presentation", v.ObservedAt, []w.RecordRef{source}), PointId: "demo-browser", Outputs: []w.RecordRef{source}, Prompts: []w.RecordRef{}, Sequence: 1, State: v.State, Uncertainty: "Browser-reported playback state; this does not prove that a human heard it."}
		receipt, e := s.Graph.Append(ctx, w.ConversationAppend{Node: record, Sessions: []string{v.Session}})
		if e != nil {
			fail(503, "Presentation receipt is unavailable.")
			return
		}
		reply(receipt)
	default:
		fail(404, "not found")
	}
}
