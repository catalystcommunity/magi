package compose

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/catalystcommunity/magi/lib/rpc"
	magiapi "github.com/catalystcommunity/magi/magi/api"
	artifactsapi "github.com/catalystcommunity/magi/primitives/artifacts/api"
	conversationapi "github.com/catalystcommunity/magi/primitives/conversation/api"
	speechapi "github.com/catalystcommunity/magi/primitives/speech/api"
)

// Spoken replies in parts (docs/speech-interaction.md, 2.2 and 2.3.1).
// Magi posts the text of a reply at once, then makes the audio one part at
// a time: the first part is one clause, so the person hears the reply soon;
// the next parts are whole sentences, so the intonation stays good. Each
// part is an annotation of the message ("speech-part"), with its audio
// artifact and the time of each word. "speech-done" ends the parts. A
// person can stop the parts ("speech-stop") and report what the client
// played ("delivery").
const (
	annSpeechPart = "speech-part"
	annSpeechDone = "speech-done"
	annSpeechStop = "speech-stop"
	annDelivery   = "delivery"

	firstPartMin = 12  // characters before the first part can end at a clause
	firstPartMax = 100 // characters in the first part at most
	minPart      = 40  // characters before a later part can end at a sentence
	partMax      = 300 // characters in a later part at most
	listenPoll   = 150 * time.Millisecond
	listenMax    = 25 * time.Second
)

// span is a part of a text as code point offsets [start, end).
type span struct{ start, end int }

// speechSpans divides text into the parts that Magi speaks. The first
// part ends at the first end of a clause; each later part ends at the first
// end of a sentence after minPart characters. A part with no such end ends
// at a space before its limit. The parts cover all words of text, in order;
// white space between parts is left out.
func speechSpans(text string) []span {
	rs := []rune(text)
	ends := func(i int, clause bool) bool {
		if i+1 < len(rs) && !unicode.IsSpace(rs[i+1]) {
			return false // "3.5", "e.g."
		}
		switch rs[i] {
		case '.', '!', '?', ';', ':', '\n':
			return true
		case ',', '—':
			return clause
		}
		return false
	}
	var out []span
	for start := 0; start < len(rs); {
		for start < len(rs) && unicode.IsSpace(rs[start]) {
			start++
		}
		if start == len(rs) {
			break
		}
		first := len(out) == 0
		limit, least := partMax, minPart
		if first {
			limit, least = firstPartMax, firstPartMin
		}
		end := -1
		for i := start; i < len(rs) && i-start < limit; i++ {
			if i+1 == len(rs) || rs[i] == '\n' || (i-start >= least && ends(i, first)) {
				end = i + 1
				break
			}
		}
		if end < 0 { // no end in the limit: the last space
			end = start + limit
			for j := end - 1; j > start+limit/2; j-- {
				if unicode.IsSpace(rs[j]) {
					end = j
					break
				}
			}
		}
		e := end
		for e > start && unicode.IsSpace(rs[e-1]) {
			e--
		}
		if e > start {
			out = append(out, span{start, e})
		}
		start = end
	}
	return out
}

// speakParts makes the audio of a posted persona reply, part by part. It
// stops early when a person stops the speech. At the end the message gets
// the whole audio, for clients that do not listen.
func (s *System) speakParts(ctx context.Context, owner string, u conversationapi.Utterance, voice string) error {
	var wavs [][]byte
	parts, stopped := 0, false
	var failure error
	for _, sp := range speechSpans(u.Text) {
		if s.speechStopped(ctx, u.Id) {
			stopped = true
			break
		}
		part := string([]rune(u.Text)[sp.start:sp.end])
		req := speechapi.SynthesizeRequest{Text: part, Timings: ptr(true)}
		if voice != "" {
			req.Voice = ptr(voice)
		}
		a, err := s.Speech.Synthesize(ctx, req)
		if err != nil {
			if rpc.CodeOf(err) == 400 { // nothing to say in this part (only symbols)
				continue
			}
			failure = err
			break
		}
		art, err := s.Artifacts.Put(ctx, artifactsapi.PutRequest{MediaType: a.MediaType, Body: a.Body, Owner: ptr(owner),
			Labels: map[string]string{"kind": "speech", "voice": a.Voice, "utterance": u.Id}})
		if err != nil {
			failure = err
			break
		}
		p := magiapi.SpeechPart{Index: uint64(parts), Audio: art.Digest, MediaType: a.MediaType, Start: uint64(sp.start),
			End: uint64(sp.end), AudioMs: a.AudioMs, Words: []magiapi.WordTime{}}
		for _, w := range a.Words {
			p.Words = append(p.Words, magiapi.WordTime{Word: w.Word, StartMs: w.StartMs, EndMs: w.EndMs,
				Start: w.Start + uint64(sp.start), End: w.End + uint64(sp.start)})
		}
		b, _ := json.Marshal(p)
		if _, err := s.Conversation.Annotate(ctx, conversationapi.AnnotateRequest{Utterance: u.Id, Kind: annSpeechPart, Body: string(b)}); err != nil {
			failure = err
			break
		}
		wavs = append(wavs, a.Body)
		parts++
	}
	// The whole audio first, so that "done" means that all is there.
	if failure == nil && !stopped && len(wavs) > 0 {
		failure = s.setWholeAudio(ctx, owner, u.Id, wavs)
	}
	done := map[string]any{"parts": parts, "stopped": stopped}
	if failure != nil {
		done["error"] = failure.Error()
	}
	b, _ := json.Marshal(done)
	if _, err := s.Conversation.Annotate(ctx, conversationapi.AnnotateRequest{Utterance: u.Id, Kind: annSpeechDone, Body: string(b)}); err != nil {
		return err
	}
	return failure
}

func (s *System) setWholeAudio(ctx context.Context, owner, utterance string, wavs [][]byte) error {
	whole, err := joinWAV(wavs)
	if err != nil {
		return err
	}
	art, err := s.Artifacts.Put(ctx, artifactsapi.PutRequest{MediaType: "audio/wav", Body: whole, Owner: ptr(owner),
		Labels: map[string]string{"kind": "speech", "utterance": utterance}})
	if err != nil {
		return err
	}
	_, err = s.Conversation.SetAudio(ctx, conversationapi.SetAudioRequest{Utterance: utterance, Audio: art.Digest})
	return err
}

func (s *System) speechStopped(ctx context.Context, utterance string) bool {
	res, err := s.Conversation.ListAnnotations(ctx, conversationapi.ListAnnotationsRequest{Utterance: utterance})
	if err != nil {
		return false
	}
	for _, a := range res.Annotations {
		if a.Kind == annSpeechStop {
			return true
		}
	}
	return false
}

// joinWAV joins PCM WAV files of one format into one file.
func joinWAV(files [][]byte) ([]byte, error) {
	var format, data []byte
	for _, f := range files {
		fmtChunk, d, err := wavChunks(f)
		if err != nil {
			return nil, err
		}
		if format == nil {
			format = fmtChunk
		} else if !bytes.Equal(format, fmtChunk) {
			return nil, errors.New("the parts have different audio formats")
		}
		data = append(data, d...)
	}
	var b bytes.Buffer
	b.WriteString("RIFF")
	binary.Write(&b, binary.LittleEndian, uint32(4+8+len(format)+8+len(data)))
	b.WriteString("WAVEfmt ")
	binary.Write(&b, binary.LittleEndian, uint32(len(format)))
	b.Write(format)
	b.WriteString("data")
	binary.Write(&b, binary.LittleEndian, uint32(len(data)))
	b.Write(data)
	return b.Bytes(), nil
}

// wavChunks returns the "fmt " and "data" chunks of a WAV file.
func wavChunks(f []byte) (format, data []byte, err error) {
	if len(f) < 12 || string(f[0:4]) != "RIFF" || string(f[8:12]) != "WAVE" {
		return nil, nil, errors.New("not a WAV file")
	}
	for i := 12; i+8 <= len(f); {
		id, n := string(f[i:i+4]), int(binary.LittleEndian.Uint32(f[i+4:i+8]))
		if i+8+n > len(f) {
			return nil, nil, errors.New("short WAV chunk")
		}
		switch id {
		case "fmt ":
			format = f[i+8 : i+8+n]
		case "data":
			data = f[i+8 : i+8+n]
		}
		i += 8 + n + n%2
	}
	if format == nil || data == nil {
		return nil, nil, errors.New("WAV file without fmt or data")
	}
	return format, data, nil
}

// speechState reads the parts of a message from its annotations. Only
// annotations of services count: a person can annotate a message, but
// cannot add audio to the speech of a persona.
func speechState(anns []conversationapi.Annotation) (parts []magiapi.SpeechPart, done, stopped bool) {
	for _, a := range anns {
		if a.Kind == annSpeechStop {
			stopped = true
		}
		if !strings.HasPrefix(a.Author, "service:") {
			continue
		}
		switch a.Kind {
		case annSpeechPart:
			var p magiapi.SpeechPart
			if json.Unmarshal([]byte(a.Body), &p) == nil && p.Index == uint64(len(parts)) {
				if p.Words == nil {
					p.Words = []magiapi.WordTime{}
				}
				parts = append(parts, p)
			}
		case annSpeechDone:
			done = true
		}
	}
	return parts, done, stopped
}

// Listen gives the speech parts of a message after the first req.After.
func (m *Service) Listen(ctx context.Context, req magiapi.ListenRequest) (magiapi.ListenResponse, error) {
	uctx := AsCaller(ctx)
	wait := min(time.Duration(deref(req.WaitMs))*time.Millisecond, listenMax)
	deadline := time.Now().Add(wait)
	for {
		res, err := m.S.Conversation.ListAnnotations(uctx, conversationapi.ListAnnotationsRequest{Utterance: req.Message})
		if err != nil {
			return magiapi.ListenResponse{}, err
		}
		parts, done, stopped := speechState(res.Annotations)
		after := min(int(deref(req.After)), len(parts))
		if len(parts) > after || done || !time.Now().Before(deadline) {
			return magiapi.ListenResponse{Parts: parts[after:], Done: done, Stopped: stopped}, nil
		}
		select {
		case <-ctx.Done():
			return magiapi.ListenResponse{}, ctx.Err()
		case <-time.After(listenPoll):
		}
	}
}

// StopSpeech asks Magi to make no more parts of a message.
func (m *Service) StopSpeech(ctx context.Context, req magiapi.StopSpeechRequest) (magiapi.StopSpeechResponse, error) {
	reason := deref(req.Reason)
	if utf8.RuneCountInString(reason) > 100 {
		return magiapi.StopSpeechResponse{}, rpc.Invalid("reason is longer than 100 characters")
	}
	b, _ := json.Marshal(map[string]string{"reason": reason})
	if _, err := m.S.Conversation.Annotate(AsCaller(ctx), conversationapi.AnnotateRequest{Utterance: req.Message, Kind: annSpeechStop, Body: string(b)}); err != nil {
		return magiapi.StopSpeechResponse{}, err
	}
	return magiapi.StopSpeechResponse{Stopped: true}, nil
}

// ReportDelivery records what the client did with a spoken message.
func (m *Service) ReportDelivery(ctx context.Context, req magiapi.ReportDeliveryRequest) (magiapi.ReportDeliveryResponse, error) {
	if r := deref(req.Reason); r != "" && !strings.Contains(" interrupted typing user error done ", " "+r+" ") {
		return magiapi.ReportDeliveryResponse{}, rpc.Invalid(`reason must be "interrupted", "typing", "user", "error", or "done"`)
	}
	d := map[string]any{"reason": deref(req.Reason)}
	if req.SpokenUntil != nil {
		d["spoken_until"] = *req.SpokenUntil
	}
	if req.ShownMs != nil {
		d["shown_ms"] = *req.ShownMs
	}
	if req.StoppedMs != nil {
		d["stopped_ms"] = *req.StoppedMs
	}
	b, _ := json.Marshal(d)
	if _, err := m.S.Conversation.Annotate(AsCaller(ctx), conversationapi.AnnotateRequest{Utterance: req.Message, Kind: annDelivery, Body: string(b)}); err != nil {
		return magiapi.ReportDeliveryResponse{}, err
	}
	return magiapi.ReportDeliveryResponse{}, nil
}
