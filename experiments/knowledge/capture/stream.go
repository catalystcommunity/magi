// Package capture is a synthetic reference-only archive experiment. The caller
// must register credentials before exposure. This is not an unknown-secret DLP
// scanner, authorization service, or safe boundary for arbitrary opaque files.
package capture

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	wire "opal.local/experiments/knowledge/generated-capture/go"
)

var (
	ErrInvalid     = errors.New("invalid capture input")
	ErrLimit       = errors.New("capture limit exceeded")
	ErrClosed      = errors.New("capture stream closed")
	ErrConflict    = errors.New("capture sequence or binding conflict")
	ErrAmbiguous   = errors.New("secret binding is ambiguous or incomplete")
	ErrUnavailable = errors.New("current secret unavailable")
)

const maxPending = 65536
const maxChunk = 32768

var namePattern = regexp.MustCompile(`^[a-zA-Z0-9_./@-]{1,256}$`)

type pattern struct {
	value     []byte
	candidate wire.SecretCandidate
}
type Stream struct {
	mu       sync.Mutex
	patterns []pattern
	pending  []byte
	offset   uint64
	closed   bool
}

func bindingKey(b wire.SecretBinding) string {
	return fmt.Sprintf("%d:%s%d:%s%d:%s", len(b.Authority.Domain), b.Authority.Domain, len(b.Authority.AccountId), b.Authority.AccountId, len(b.BindingId), b.BindingId)
}
func validBinding(b wire.SecretBinding) bool {
	for _, v := range []string{b.Authority.Domain, b.Authority.AccountId, b.BindingId, b.Path, b.Key, b.SessionId} {
		if !namePattern.MatchString(v) {
			return false
		}
	}
	return b.IssuanceId == nil || namePattern.MatchString(*b.IssuanceId)
}
func transform(value []byte, encoding wire.Encoding) ([]byte, error) {
	switch encoding {
	case "raw":
		return bytes.Clone(value), nil
	case "base64":
		return []byte(base64.StdEncoding.EncodeToString(value)), nil
	case "url_query":
		return []byte(url.QueryEscape(string(value))), nil
	case "json_string":
		if !utf8.Valid(value) {
			return nil, ErrInvalid
		}
		out, err := json.Marshal(string(value))
		if err != nil {
			return nil, ErrInvalid
		}
		return out[1 : len(out)-1], nil
	default:
		return nil, ErrInvalid
	}
}

// Register retains values only in active process memory. Rotation uses a new
// binding ID. Old patterns remain until Close, so delayed output is substituted.
func (s *Stream) Register(binding wire.SecretBinding, value []byte, encodings ...wire.Encoding) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	if !validBinding(binding) || len(value) == 0 || len(value) > 4096 || len(encodings) > 3 {
		return ErrInvalid
	}
	for _, p := range s.patterns {
		if bindingKey(p.candidate.Binding) == bindingKey(binding) {
			return ErrConflict
		}
	}
	if binding.IssuanceId != nil {
		v := *binding.IssuanceId
		binding.IssuanceId = &v
	}
	encodings = append([]wire.Encoding{"raw"}, encodings...)
	added := []pattern{}
	seen := map[wire.Encoding]bool{}
	for _, encoding := range encodings {
		if seen[encoding] {
			return ErrInvalid
		}
		seen[encoding] = true
		encoded, err := transform(value, encoding)
		if err != nil {
			return err
		}
		added = append(added, pattern{encoded, wire.SecretCandidate{Binding: binding, Encoding: encoding}})
	}
	if len(s.patterns)+len(added) > 128 {
		return ErrLimit
	}
	s.patterns = append(s.patterns, added...)
	sort.Slice(s.patterns, func(i, j int) bool {
		a, b := s.patterns[i], s.patterns[j]
		if len(a.value) != len(b.value) {
			return len(a.value) > len(b.value)
		}
		if bindingKey(a.candidate.Binding) != bindingKey(b.candidate.Binding) {
			return bindingKey(a.candidate.Binding) < bindingKey(b.candidate.Binding)
		}
		return a.candidate.Encoding < b.candidate.Encoding
	})
	return nil
}

func appendLiteral(out []wire.Segment, offset uint64, b byte) []wire.Segment {
	if len(out) > 0 {
		if prior, ok := out[len(out)-1].(wire.LiteralSegment); ok && prior.Start+uint64(len(prior.Value)) == offset {
			prior.Value = append(prior.Value, b)
			out[len(out)-1] = prior
			return out
		}
	}
	return append(out, wire.LiteralSegment{Start: offset, Value: []byte{b}})
}

// drain waits at every possible secret prefix, including overlaps. It never
// publishes a prefix and hopes to replace it in a later chunk.
func (s *Stream) drain(final bool) []wire.Segment {
	out := []wire.Segment{}
	for len(s.pending) > 0 {
		end := 0
		wait := false
		for _, p := range s.patterns {
			if bytes.HasPrefix(s.pending, p.value) {
				end = max(end, len(p.value))
			}
			if len(s.pending) < len(p.value) && bytes.HasPrefix(p.value, s.pending) {
				wait = true
			}
		}
		if wait {
			if final {
				out = append(out, wire.GapSegment{Start: s.offset, End: s.offset + uint64(len(s.pending)), Reason: "possible_secret_prefix"})
				s.offset += uint64(len(s.pending))
				clear(s.pending)
				s.pending = nil
			}
			break
		}
		if end == 0 {
			out = appendLiteral(out, s.offset, s.pending[0])
			s.pending[0] = 0
			s.pending = s.pending[1:]
			s.offset++
			continue
		}
		// Expand a crossing overlap to cover its whole byte range. A contained
		// short match does not override a complete longer binding.
		involved := []wire.SecretCandidate{}
		involvedKeys := map[string]bool{}
		for start := 0; start < end; start++ {
			for _, p := range s.patterns {
				remaining := s.pending[start:]
				if bytes.HasPrefix(remaining, p.value) {
					end = max(end, start+len(p.value))
					key := bindingKey(p.candidate.Binding) + "/" + string(p.candidate.Encoding)
					if !involvedKeys[key] {
						involvedKeys[key] = true
						involved = append(involved, p.candidate)
					}
				}
				if len(remaining) < len(p.value) && bytes.HasPrefix(p.value, remaining) {
					wait = true
				}
			}
		}
		if wait {
			if final {
				out = append(out, wire.GapSegment{Start: s.offset, End: s.offset + uint64(len(s.pending)), Reason: "possible_secret_prefix"})
				s.offset += uint64(len(s.pending))
				clear(s.pending)
				s.pending = nil
			}
			break
		}
		whole := []wire.SecretCandidate{}
		for _, p := range s.patterns {
			if len(p.value) == end && bytes.Equal(s.pending[:end], p.value) {
				whole = append(whole, p.candidate)
			}
		}
		if len(whole) == 1 {
			out = append(out, wire.SecretSegment{Start: s.offset, End: s.offset + uint64(end), Candidate: whole[0]})
		} else {
			reason := "equal_values"
			candidates := whole
			if len(whole) == 0 {
				reason = "overlap"
				candidates = deduplicate(involved)
			}
			out = append(out, wire.AmbiguousSegment{Start: s.offset, End: s.offset + uint64(end), Candidates: candidates, Reason: reason})
		}
		s.offset += uint64(end)
		clear(s.pending[:end])
		s.pending = s.pending[end:]
	}
	return out
}
func deduplicate(in []wire.SecretCandidate) []wire.SecretCandidate {
	out := []wire.SecretCandidate{}
	seen := map[string]bool{}
	for _, c := range in {
		key := bindingKey(c.Binding) + "/" + string(c.Encoding)
		if !seen[key] {
			seen[key] = true
			out = append(out, c)
		}
	}
	return out
}

// Push returns safe segments only. A limit error closes the stream and emits a
// gap for discarded bytes. The caller must keep that gap and stop protected work.
func (s *Stream) Push(chunk []byte) ([]wire.Segment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, ErrClosed
	}
	if len(chunk) > maxChunk || len(s.pending)+len(chunk) > maxPending {
		gap := wire.GapSegment{Start: s.offset, End: s.offset + uint64(len(s.pending)) + uint64(len(chunk)), Reason: "capture_limit"}
		s.clear()
		s.closed = true
		return []wire.Segment{gap}, ErrLimit
	}
	s.pending = append(s.pending, chunk...)
	return s.drain(false), nil
}
func (s *Stream) Close() ([]wire.Segment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, ErrClosed
	}
	out := s.drain(true)
	s.clear()
	s.closed = true
	return out, nil
}
func (s *Stream) clear() {
	clear(s.pending)
	s.pending = nil
	for _, p := range s.patterns {
		clear(p.value)
	}
	s.patterns = nil
}

// Field uses known provenance to resolve equal-value ambiguity. It is a
// separate field, with offsets relative to that field, not the stream offset.
func (s *Stream) Field(bindingID wire.SecretBinding, encoding wire.Encoding, value []byte) (wire.SecretSegment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return wire.SecretSegment{}, ErrClosed
	}
	for _, p := range s.patterns {
		if bindingKey(p.candidate.Binding) == bindingKey(bindingID) && p.candidate.Encoding == encoding && bytes.Equal(p.value, value) {
			return wire.SecretSegment{Start: 0, End: uint64(len(value)), Candidate: p.candidate}, nil
		}
	}
	return wire.SecretSegment{}, ErrInvalid
}

func Display(segments []wire.Segment) (string, error) {
	var text strings.Builder
	for _, segment := range segments {
		switch v := segment.(type) {
		case wire.LiteralSegment:
			text.Write(v.Value)
		case wire.SecretSegment:
			text.WriteString("${secret:" + v.Candidate.Binding.Path + ":" + v.Candidate.Binding.Key + "}")
		case wire.AmbiguousSegment:
			text.WriteString("[ambiguous secret binding]")
		case wire.GapSegment:
			text.WriteString("[capture gap]")
		default:
			return "", ErrInvalid
		}
	}
	return text.String(), nil
}

type Resolution struct {
	Binding wire.SecretBinding
	Value   []byte
}
type Resolver func(wire.SecretBinding) (Resolution, error)

// PrepareCurrent does no model or tool execution. The resolver must authorize
// each current resolution and issue a new binding. Literal syntax stays literal.
// Values enter the new capture registry before return. They are returned only
// for active use, never for an archive or a log. Repeated bindings resolve once.
func PrepareCurrent(segments []wire.Segment, resolver Resolver, target *Stream) ([]byte, []wire.SecretBinding, error) {
	if len(segments) > 4096 {
		return nil, nil, ErrLimit
	}
	encodings := map[string][]wire.Encoding{}
	inputBytes := 0
	for _, segment := range segments {
		switch segment.(type) {
		case wire.LiteralSegment, wire.SecretSegment:
			if _, _, err := bounds(segment); err != nil {
				return nil, nil, err
			}
		case wire.AmbiguousSegment, wire.GapSegment:
			return nil, nil, ErrAmbiguous
		default:
			return nil, nil, ErrInvalid
		}
		switch v := segment.(type) {
		case wire.LiteralSegment:
			inputBytes += len(v.Value)
		case wire.SecretSegment:
			if resolver == nil || target == nil {
				return nil, nil, ErrUnavailable
			}
			key := bindingKey(v.Candidate.Binding)
			found := false
			for _, e := range encodings[key] {
				if e == v.Candidate.Encoding {
					found = true
				}
			}
			if !found && v.Candidate.Encoding != "raw" {
				encodings[key] = append(encodings[key], v.Candidate.Encoding)
			}
		}
		if inputBytes > maxBatch {
			return nil, nil, ErrLimit
		}
	}
	var output []byte
	bindings := []wire.SecretBinding{}
	resolved := map[string]Resolution{}
	defer func() {
		for _, current := range resolved {
			clear(current.Value)
		}
	}()
	for _, segment := range segments {
		switch v := segment.(type) {
		case wire.LiteralSegment:
			output = append(output, v.Value...)
		case wire.SecretSegment:
			key := bindingKey(v.Candidate.Binding)
			current, ok := resolved[key]
			if !ok {
				var err error
				current, err = resolver(v.Candidate.Binding)
				if err != nil || !validBinding(current.Binding) || bindingKey(current.Binding) == key || current.Binding.Authority != v.Candidate.Binding.Authority || current.Binding.Path != v.Candidate.Binding.Path || current.Binding.Key != v.Candidate.Binding.Key || len(current.Value) == 0 || len(current.Value) > 4096 {
					clear(output)
					return nil, nil, ErrUnavailable
				}
				current.Value = bytes.Clone(current.Value)
				resolved[key] = current
				if err := target.Register(current.Binding, current.Value, encodings[key]...); err != nil {
					clear(output)
					return nil, nil, ErrUnavailable
				}
				bindings = append(bindings, current.Binding)
			}
			encoded, err := transform(current.Value, v.Candidate.Encoding)
			if err != nil {
				clear(output)
				return nil, nil, err
			}
			output = append(output, encoded...)
			clear(encoded)
		}
		if len(output) > 1<<20 {
			clear(output)
			return nil, nil, ErrLimit
		}
	}
	return output, bindings, nil
}
