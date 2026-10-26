package capture

import (
	"bytes"
	"encoding/base64"
	"errors"
	"reflect"
	"strings"
	"testing"

	wire "opal.local/experiments/knowledge/generated-capture/go"
)

func binding(id string) wire.SecretBinding {
	return wire.SecretBinding{Authority: wire.IdentityRef{Domain: "primary.opal.test", AccountId: "vault"}, BindingId: id, Path: "project/github", Key: "ssh_key", SessionId: "session-one"}
}
func must(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func captureAll(t *testing.T, s *Stream, chunks ...[]byte) []wire.Segment {
	t.Helper()
	out := []wire.Segment{}
	for _, chunk := range chunks {
		segments, err := s.Push(chunk)
		must(t, err)
		out = append(out, segments...)
	}
	last, err := s.Close()
	must(t, err)
	return append(out, last...)
}
func normalize(segments []wire.Segment) []wire.Segment {
	out := []wire.Segment{}
	for _, segment := range segments {
		if v, ok := segment.(wire.LiteralSegment); ok {
			for n, b := range v.Value {
				out = appendLiteral(out, v.Start+uint64(n), b)
			}
		} else {
			out = append(out, segment)
		}
	}
	return out
}

func TestEveryChunkBoundary(t *testing.T) {
	value := []byte("synthetic-token-é-123")
	input := []byte("prefix 🌱 " + string(value) + " suffix " + string(value) + " done")
	var expected []wire.Segment
	for split := 0; split <= len(input); split++ {
		s := new(Stream)
		must(t, s.Register(binding("one"), value))
		segments := normalize(captureAll(t, s, input[:split], input[split:]))
		if split == 0 {
			expected = segments
		} else if !reflect.DeepEqual(expected, segments) {
			t.Fatalf("different output at byte %d", split)
		}
		display, err := Display(segments)
		must(t, err)
		if strings.Contains(display, string(value)) {
			t.Fatal("secret in display")
		}
		if display != "prefix 🌱 ${secret:project/github:ssh_key} suffix ${secret:project/github:ssh_key} done" {
			t.Fatal("wrong substituted display")
		}
	}
	s := new(Stream)
	must(t, s.Register(binding("one"), value))
	chunks := [][]byte{}
	for _, b := range input {
		chunks = append(chunks, []byte{b})
	}
	if !reflect.DeepEqual(expected, normalize(captureAll(t, s, chunks...))) {
		t.Fatal("one-byte streaming changed result")
	}
}

func TestShortEqualAndOverlappingValues(t *testing.T) {
	t.Run("one_byte", func(t *testing.T) {
		s := new(Stream)
		must(t, s.Register(binding("one"), []byte("x")))
		segments := captureAll(t, s, []byte("x!x"))
		if _, ok := segments[0].(wire.SecretSegment); !ok {
			t.Fatal("short secret ignored")
		}
	})
	t.Run("equal_values", func(t *testing.T) {
		s := new(Stream)
		must(t, s.Register(binding("one"), []byte("same")))
		must(t, s.Register(binding("two"), []byte("same")))
		field, err := s.Field(binding("two"), "raw", []byte("same"))
		must(t, err)
		if field.Candidate.Binding.BindingId != "two" {
			t.Fatal("field provenance lost")
		}
		segments := captureAll(t, s, []byte("same"))
		v, ok := segments[0].(wire.AmbiguousSegment)
		if !ok || len(v.Candidates) != 2 || v.Reason != "equal_values" {
			t.Fatal("equal values guessed")
		}
		_, _, err = PrepareCurrent(segments, func(wire.SecretBinding) (Resolution, error) {
			t.Fatal("ambiguous value resolved")
			return Resolution{}, nil
		}, new(Stream))
		if !errors.Is(err, ErrAmbiguous) {
			t.Fatal(err)
		}
	})
	t.Run("longest_complete", func(t *testing.T) {
		s := new(Stream)
		must(t, s.Register(binding("short"), []byte("abc")))
		must(t, s.Register(binding("long"), []byte("abcdef")))
		segments := captureAll(t, s, []byte("abc"), []byte("def!"))
		v, ok := segments[0].(wire.SecretSegment)
		if !ok || v.Candidate.Binding.BindingId != "long" {
			t.Fatal("longer credential not selected")
		}
	})
	t.Run("crossing_overlap", func(t *testing.T) {
		s := new(Stream)
		must(t, s.Register(binding("one"), []byte("abcdef")))
		must(t, s.Register(binding("two"), []byte("defghi")))
		segments := captureAll(t, s, []byte("abcde"), []byte("fghi!"))
		v, ok := segments[0].(wire.AmbiguousSegment)
		if !ok || v.End != 9 || v.Reason != "overlap" {
			t.Fatal("overlap suffix exposed")
		}
		display, err := Display(segments)
		must(t, err)
		if display != "[ambiguous secret binding]!" {
			t.Fatal(display)
		}
	})
}

func TestRotationAndLiteralReferences(t *testing.T) {
	s := new(Stream)
	must(t, s.Register(binding("old"), []byte("old-token")))
	first, err := s.Push([]byte("old-to"))
	must(t, err)
	if len(first) != 0 {
		t.Fatal("prefix published")
	}
	must(t, s.Register(binding("new"), []byte("new-token")))
	segments := captureAll(t, s, []byte("ken new-token ${secret:project/github:ssh_key}"))
	if segments[0].(wire.SecretSegment).Candidate.Binding.BindingId != "old" {
		t.Fatal("old binding replaced")
	}
	requested := []string{}
	target := new(Stream)
	prepared, bindings, err := PrepareCurrent(segments, func(b wire.SecretBinding) (Resolution, error) {
		requested = append(requested, b.BindingId)
		current := b
		current.BindingId = b.BindingId + "-current"
		return Resolution{current, []byte("current-token")}, nil
	}, target)
	must(t, err)
	if string(prepared) != "current-token current-token ${secret:project/github:ssh_key}" || len(bindings) != 2 || !reflect.DeepEqual(requested, []string{"old", "new"}) {
		t.Fatal("literal text resolved or history value reused")
	}
	newDisplay, err := Display(captureAll(t, target, prepared))
	must(t, err)
	if strings.Contains(newDisplay, "current-token") {
		t.Fatal("new values not registered before use")
	}
	if len(s.patterns) != 0 || len(s.pending) != 0 {
		t.Fatal("stream retains active values after close")
	}
	if !errors.Is(s.Register(binding("later"), []byte("later")), ErrClosed) {
		t.Fatal("closed stream reopened")
	}
}

func TestEncodings(t *testing.T) {
	value := []byte("token+\"/\n")
	for _, encoding := range []wire.Encoding{"raw", "base64", "json_string", "url_query"} {
		t.Run(string(encoding), func(t *testing.T) {
			s := new(Stream)
			extra := []wire.Encoding{}
			if encoding != "raw" {
				extra = append(extra, encoding)
			}
			must(t, s.Register(binding("one"), value, extra...))
			encoded, err := transform(value, encoding)
			must(t, err)
			segments := captureAll(t, s, encoded[:2], encoded[2:])
			v, ok := segments[0].(wire.SecretSegment)
			if !ok || v.Candidate.Encoding != encoding {
				t.Fatal("encoding provenance lost")
			}
		})
	}
	// Unknown transformations remain outside this contract; no universal DLP claim.
	s := new(Stream)
	must(t, s.Register(binding("one"), value))
	encoded := base64.StdEncoding.EncodeToString(value)
	display, err := Display(captureAll(t, s, []byte(encoded)))
	must(t, err)
	if display != encoded {
		t.Fatal("undeclared transformation unexpectedly detected")
	}
}

func TestCaptureLimitAndResolverFailure(t *testing.T) {
	s := new(Stream)
	must(t, s.Register(binding("one"), []byte("synthetic")))
	segments, err := s.Push(bytes.Repeat([]byte("x"), maxChunk+1))
	if !errors.Is(err, ErrLimit) || len(segments) != 1 {
		t.Fatal("limit did not produce gap")
	}
	if _, ok := segments[0].(wire.GapSegment); !ok {
		t.Fatal("raw bytes returned on failure")
	}
	if _, err = s.Push([]byte("next")); !errors.Is(err, ErrClosed) {
		t.Fatal("failed stream continued")
	}
	s = new(Stream)
	must(t, s.Register(binding("one"), []byte("synthetic")))
	segments = captureAll(t, s, []byte("synthetic"))
	for _, resolver := range []Resolver{nil, func(b wire.SecretBinding) (Resolution, error) {
		return Resolution{}, errors.New("sensitive error text")
	}, func(b wire.SecretBinding) (Resolution, error) { return Resolution{b, []byte("old-value")}, nil }} {
		value, bindings, err := PrepareCurrent(segments, resolver, new(Stream))
		if !errors.Is(err, ErrUnavailable) || value != nil || bindings != nil || strings.Contains(err.Error(), "sensitive") {
			t.Fatal("resolver failure escaped")
		}
	}
}

func TestIncompletePrefixAndRepeatedResolution(t *testing.T) {
	s := new(Stream)
	must(t, s.Register(binding("one"), []byte("synthetic-token")))
	segments := captureAll(t, s, []byte("prefix synthetic-to"))
	if gap, ok := segments[len(segments)-1].(wire.GapSegment); !ok || gap.Reason != "possible_secret_prefix" {
		t.Fatal("possible credential tail published")
	}
	s = new(Stream)
	must(t, s.Register(binding("one"), []byte("synthetic-token")))
	segments = captureAll(t, s, []byte("synthetic-token synthetic-token"))
	calls := 0
	output, bindings, err := PrepareCurrent(segments, func(b wire.SecretBinding) (Resolution, error) {
		calls++
		b.BindingId = "current"
		return Resolution{b, []byte("rotated-token")}, nil
	}, new(Stream))
	must(t, err)
	if calls != 1 || len(bindings) != 1 || string(output) != "rotated-token rotated-token" {
		t.Fatal("one preparation used inconsistent rotations")
	}
}

func TestSelfOverlapAndBoundedBuffer(t *testing.T) {
	s := new(Stream)
	must(t, s.Register(binding("one"), []byte("aa")))
	segments := captureAll(t, s, []byte("aaaa!"))
	v, ok := segments[0].(wire.AmbiguousSegment)
	if !ok || v.End != 4 || len(v.Candidates) != 1 {
		t.Fatal("self-overlap was not preserved")
	}
	must(t, validateBatch(wire.CaptureBatch{SchemaVersion: 1, AttemptId: "one", StreamId: "out", Sequence: 1, Segments: segments, Final: true, Complete: true}))
	s = new(Stream)
	must(t, s.Register(binding("one"), []byte("aa")))
	for range 2 {
		segments, err := s.Push(bytes.Repeat([]byte("a"), maxChunk))
		must(t, err)
		if len(segments) != 0 {
			t.Fatal("unfinished overlap was published")
		}
	}
	segments, err := s.Push([]byte("a"))
	if !errors.Is(err, ErrLimit) || len(segments) != 1 {
		t.Fatal("overlap buffer was not bounded")
	}
}

func FuzzChunkInvariant(f *testing.F) {
	f.Add([]byte("before abcdef and defghi after"), uint8(7))
	f.Fuzz(func(t *testing.T, input []byte, size uint8) {
		if len(input) > 4096 {
			return
		}
		makeStream := func() *Stream {
			s := new(Stream)
			must(t, s.Register(binding("one"), []byte("abcdef")))
			must(t, s.Register(binding("two"), []byte("defghi")))
			return s
		}
		whole := normalize(captureAll(t, makeStream(), input))
		chunks := [][]byte{}
		step := int(size) + 1
		for start := 0; start < len(input); start += step {
			chunks = append(chunks, input[start:min(start+step, len(input))])
		}
		parts := normalize(captureAll(t, makeStream(), chunks...))
		if !reflect.DeepEqual(whole, parts) {
			t.Fatal("chunking changed capture")
		}
	})
}
