package capture

import (
	"bytes"
	"encoding/hex"
	"os"
	"os/exec"
	"strings"
	"testing"

	wire "opal.local/experiments/knowledge/generated-capture/go"
)

func TestCaptureCodec(t *testing.T) {
	issuance := "issued-one"
	b := binding("one")
	b.IssuanceId = &issuance
	variants := []wire.Segment{
		wire.LiteralSegment{Start: 0, Value: []byte("${secret:literal:only}")},
		wire.SecretSegment{Start: 0, End: 12, Candidate: wire.SecretCandidate{Binding: b, Encoding: "base64"}},
		wire.AmbiguousSegment{Start: 0, End: 12, Candidates: []wire.SecretCandidate{{Binding: b, Encoding: "raw"}, {Binding: binding("two"), Encoding: "raw"}}, Reason: "equal_values"},
		wire.GapSegment{Start: 0, End: 12, Reason: "producer_lost"},
	}
	for _, segment := range variants {
		batch := wire.CaptureBatch{SchemaVersion: 1, AttemptId: "attempt", StreamId: "stream", Sequence: 1, Segments: []wire.Segment{segment}, Final: true}
		data := wire.EncodeCaptureBatch(batch)
		decoded, err := wire.DecodeCaptureBatch(data)
		must(t, err)
		if !bytes.Equal(data, wire.EncodeCaptureBatch(decoded)) {
			t.Fatal("Go codec changed bytes")
		}
		t.Run("rust", func(t *testing.T) {
			binary := os.Getenv("OPAL_RUST_CODEC")
			if binary == "" {
				t.Skip("use node run.mjs for Rust checks")
			}
			cmd := exec.Command(binary, "capture")
			cmd.Stdin = strings.NewReader(hex.EncodeToString(data))
			out, err := cmd.CombinedOutput()
			must(t, err)
			got, err := hex.DecodeString(strings.TrimSpace(string(out)))
			must(t, err)
			if !bytes.Equal(data, got) {
				t.Fatal("Go and Rust disagree")
			}
		})
	}
	goSchema, err := os.ReadFile("../generated-capture/go/capture.csil-schema.cbor")
	must(t, err)
	rustSchema, err := os.ReadFile("../generated-capture/rust/capture.csil-schema.cbor")
	must(t, err)
	if !bytes.Equal(goSchema, rustSchema) {
		t.Fatal("capture descriptor mismatch")
	}
	for _, data := range [][]byte{{}, {0xa1}, {0x9b, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}} {
		if _, err := wire.DecodeCaptureBatch(data); err == nil {
			t.Fatal("malformed CBOR accepted")
		}
	}
}
