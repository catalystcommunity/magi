package protocol

import (
	"bytes"
	"context"
	"errors"
	wire "github.com/catalystcommunity/magi/generated/go"
	"testing"
	"time"
)

func TestSecretReferences(t *testing.T) {
	now := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	binding := wire.SecretBinding{BindingId: "old-binding", SessionId: "old-session", Resolution: "current", Secret: wire.SecretRef{Authority: wire.IdentityRef{Domain: "catalystsquad.local", AccountId: "secrets"}, Path: "project/git", Key: "token"}, Validity: wire.TimeWindow{NotBefore: now.Add(-time.Hour)}}
	literal := []byte("literal ${secret:project/git:token} then ")
	secret := wire.SecretSegment{Start: uint64(len(literal)), End: uint64(len(literal)) + 4, Candidate: wire.SecretCandidate{Binding: binding, Encoding: "raw"}}
	content := wire.CapturedContent{Capture: "complete", Segments: []wire.Segment{wire.LiteralSegment{Start: 0, Value: literal}, secret}}
	displayed, e := DisplayContent(content)
	if e != nil || !bytes.Equal(displayed, append(bytes.Clone(literal), []byte("${secret:project/git:token}")...)) {
		t.Fatal("display", e)
	}
	calls := 0
	resolve := func(ctx context.Context, ref wire.SecretRef) (CurrentResolution, error) {
		calls++
		b := binding
		b.BindingId = "new-binding"
		b.SessionId = "new-session"
		return CurrentResolution{Binding: b, Value: []byte("fresh-test-value")}, nil
	}
	result, bindings, e := PrepareCurrent(context.Background(), content, "new-session", now, resolve)
	if e != nil || calls != 1 || len(bindings) != 1 || !bytes.HasSuffix(result, []byte("fresh-test-value")) {
		t.Fatal("current resolution", e)
	}
	if bytes.Contains(wire.EncodeCapturedContent(content), []byte("fresh-test-value")) {
		t.Fatal("changed archived input")
	}
	_, _, e = PrepareCurrent(context.Background(), content, "new-session", now, func(context.Context, wire.SecretRef) (CurrentResolution, error) {
		return CurrentResolution{}, errors.New("sensitive upstream detail")
	})
	if e == nil || bytes.Contains([]byte(e.Error()), []byte("sensitive")) {
		t.Fatal("resolver error leaked")
	}
	for _, kind := range []string{"gap", "ambiguous", "overlap", "expired", "wrong_session", "same_binding", "unsupported_encoding"} {
		t.Run(kind, func(t *testing.T) {
			c := content
			c.Segments = append([]wire.Segment{}, content.Segments...)
			r := resolve
			switch kind {
			case "gap":
				c.Capture = "incomplete"
				c.Segments[1] = wire.GapSegment{Start: secret.Start, End: secret.End, Reason: "lost"}
			case "ambiguous":
				c.Capture = "incomplete"
				c.Segments[1] = wire.AmbiguousSegment{Start: secret.Start, End: secret.End, Candidates: []wire.SecretCandidate{secret.Candidate, secret.Candidate}, Reason: "shared value"}
			case "overlap":
				s := secret
				s.Start--
				c.Segments[1] = s
			case "unsupported_encoding":
				s := secret
				s.Candidate.Encoding = "base64"
				c.Segments[1] = s
			default:
				r = func(ctx context.Context, ref wire.SecretRef) (CurrentResolution, error) {
					v, _ := resolve(ctx, ref)
					switch kind {
					case "expired":
						v.Binding.Validity.ExpiresAt = &now
					case "wrong_session":
						v.Binding.SessionId = "other"
					case "same_binding":
						v.Binding.BindingId = binding.BindingId
					}
					return v, nil
				}
			}
			if _, _, e := PrepareCurrent(context.Background(), c, "new-session", now, r); e == nil {
				t.Fatal("resolved unsafe content")
			}
		})
	}
}
