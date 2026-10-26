package protocol

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"time"

	wire "github.com/catalystcommunity/magi/generated/go"
)

// ValidateContent checks offsets and binding structure. Capture must replace
// known secret values before this boundary. This function is not a DLP filter.
func ValidateContent(content wire.CapturedContent) error {
	if len(content.Segments) > MaxCollection {
		return ErrWire
	}
	var next uint64
	for _, segment := range content.Segments {
		var start, end uint64
		switch s := segment.(type) {
		case wire.LiteralSegment:
			start = s.Start
			end = start + uint64(len(s.Value))
			if len(s.Value) > MaxBlob || end < start {
				return ErrWire
			}
		case wire.SecretSegment:
			start = s.Start
			end = s.End
			if err := validateBinding(s.Candidate.Binding); err != nil {
				return err
			}
		case wire.AmbiguousSegment:
			start = s.Start
			end = s.End
			if len(s.Candidates) < 2 || content.Capture == "complete" {
				return ErrWire
			}
			for _, c := range s.Candidates {
				if err := validateBinding(c.Binding); err != nil {
					return err
				}
			}
		case wire.GapSegment:
			start = s.Start
			end = s.End
			if content.Capture == "complete" {
				return ErrWire
			}
		default:
			return ErrWire
		}
		if start != next || end <= start {
			return ErrWire
		}
		next = end
	}
	return nil
}
func validateBinding(b wire.SecretBinding) error {
	if b.BindingId == "" || b.SessionId == "" || b.Resolution != "current" || b.Secret.Authority.Domain == "" || b.Secret.Authority.AccountId == "" || b.Secret.Path == "" || b.Secret.Key == "" {
		return ErrWire
	}
	if b.Validity.ExpiresAt != nil && !b.Validity.ExpiresAt.After(b.Validity.NotBefore) {
		return ErrWire
	}
	return nil
}

// DisplayContent never resolves credentials. Literal reference text stays literal.
func DisplayContent(content wire.CapturedContent) ([]byte, error) {
	if e := ValidateContent(content); e != nil {
		return nil, e
	}
	var result bytes.Buffer
	for _, segment := range content.Segments {
		switch s := segment.(type) {
		case wire.LiteralSegment:
			result.Write(s.Value)
		case wire.SecretSegment:
			fmt.Fprintf(&result, "${secret:%s:%s}", s.Candidate.Binding.Secret.Path, s.Candidate.Binding.Secret.Key)
		case wire.AmbiguousSegment:
			result.WriteString("[ambiguous secret reference]")
		case wire.GapSegment:
			result.WriteString("[capture gap]")
		}
	}
	return result.Bytes(), nil
}

// CurrentResolution is transient. Never serialize Value into evidence or logs.
type CurrentResolution struct {
	Binding wire.SecretBinding
	Value   []byte
}
type CurrentResolver func(context.Context, wire.SecretRef) (CurrentResolution, error)

// PrepareCurrent uses an independently authorized resolver. The caller selects
// its current authority and session. It does not resolve old credential values.
// The returned bindings must enter capture before the returned bytes are used.
func PrepareCurrent(ctx context.Context, content wire.CapturedContent, session string, now time.Time, resolve CurrentResolver) ([]byte, []wire.SecretBinding, error) {
	if session == "" || content.Capture != "complete" || ValidateContent(content) != nil {
		return nil, nil, ErrWire
	}
	var result bytes.Buffer
	var bindings []wire.SecretBinding
	for _, segment := range content.Segments {
		switch s := segment.(type) {
		case wire.LiteralSegment:
			result.Write(s.Value)
		case wire.SecretSegment:
			if s.Candidate.Encoding != "raw" || resolve == nil {
				return nil, nil, ErrWire
			}
			r, e := resolve(ctx, s.Candidate.Binding.Secret)
			if e != nil {
				return nil, nil, fmt.Errorf("current credential is unavailable")
			}
			b := r.Binding
			if validateBinding(b) != nil || b.BindingId == s.Candidate.Binding.BindingId || b.SessionId != session || !reflect.DeepEqual(b.Secret, s.Candidate.Binding.Secret) || now.Before(b.Validity.NotBefore) || (b.Validity.ExpiresAt != nil && !now.Before(*b.Validity.ExpiresAt)) || len(r.Value) == 0 || len(r.Value) > MaxBlob {
				return nil, nil, ErrWire
			}
			for _, old := range bindings {
				if old.BindingId == b.BindingId && !reflect.DeepEqual(old, b) {
					return nil, nil, ErrWire
				}
			}
			bindings = append(bindings, b)
			result.Write(r.Value)
		default:
			return nil, nil, ErrWire
		}
		if result.Len() > MaxBlob {
			return nil, nil, ErrWire
		}
	}
	return result.Bytes(), bindings, nil
}
