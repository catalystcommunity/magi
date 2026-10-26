package knowledge

import (
	"bytes"
	"encoding/hex"
	"os"
	"os/exec"
	"strings"
	"testing"

	wire "opal.local/experiments/knowledge/generated/go"
)

func TestCodec(t *testing.T) {
	i := interaction("I1", "Tuesday works for both.")
	ir := i.Header.Ref
	records := []wire.EvidenceRecord{
		i,
		wire.InterpretationRecord{Header: header("interpretation", "R1"), Sources: []wire.RecordRef{ir}, Method: "synthetic-fixture", Uncertainty: "ambiguous"},
		wire.SessionInput{Header: header("session_input", "C1"), SessionId: "C", Interpretation: ir, Content: "Tuesday", CommandKey: "C1", PolicyRevision: 1},
		wire.PresentationRecord{Header: header("presentation", "P1"), Output: ir, PointId: "desk", State: "interrupted"},
		wire.LearningAssessment{Header: header("assessment", "A1"), Inputs: []wire.RecordRef{ir}, Method: "review"},
		wire.SourceContext{Header: header("source", "S1"), ResourceId: "document", Adapter: "fixture", EnrichmentAllowed: false},
	}
	rust := os.Getenv("OPAL_RUST_CODEC")
	roundtrip := func(t *testing.T, mode string, data []byte) {
		t.Helper()
		if rust == "" {
			t.Skip("set OPAL_RUST_CODEC for the cross-language check")
		}
		cmd := exec.Command(rust, mode)
		cmd.Stdin = strings.NewReader(hex.EncodeToString(data))
		out, err := cmd.CombinedOutput()
		must(t, err)
		got, err := hex.DecodeString(strings.TrimSpace(string(out)))
		must(t, err)
		if !bytes.Equal(got, data) {
			t.Fatalf("Go/Rust byte mismatch\nGo: %x\nRust: %x", data, got)
		}
	}
	for n, record := range records {
		h, _, _, _, _ := describe(record)
		t.Run(h.Ref.Kind, func(t *testing.T) {
			page := wire.HistoryPage{Records: []wire.EvidenceRecord{record}}
			data := wire.EncodeHistoryPage(page)
			got, err := wire.DecodeHistoryPage(data)
			must(t, err)
			gh, kind, _, _, err := describe(got.Records[0])
			must(t, err)
			if kind != h.Ref.Kind || gh.ReceivedAt.Nanosecond() != 123456789 {
				t.Fatalf("wrong variant or timestamp at %d", n)
			}
			if !bytes.Equal(data, wire.EncodeHistoryPage(got)) {
				t.Fatal("Go round-trip drift")
			}
			t.Run("rust", func(t *testing.T) { roundtrip(t, "history", data) })
		})
	}
	for _, dims := range [][]wire.TypedDimension{nil, {}, {{Schema: wire.SchemaRef{Name: "opal.test/hypothesis", Version: 2, Digest: []byte{1, 2}}, Payload: []byte{0xa0}}}} {
		name := "absent"
		if dims != nil {
			name = "present"
			if len(dims) > 0 {
				name = "extension"
			}
		}
		t.Run(name, func(t *testing.T) {
			record := wire.LearningAssessment{Header: header("assessment", name), Dimensions: dims}
			emptyCursor := []byte{}
			data := wire.EncodeHistoryPage(wire.HistoryPage{Records: []wire.EvidenceRecord{record}, NextCursor: &emptyCursor})
			got, err := wire.DecodeHistoryPage(data)
			must(t, err)
			d := got.Records[0].(wire.LearningAssessment).Dimensions
			if (d == nil) != (dims == nil) || got.NextCursor == nil {
				t.Fatal("optional presence lost")
			}
			t.Run("rust", func(t *testing.T) { roundtrip(t, "history", data) })
		})
	}
	t.Run("exact_decimal", func(t *testing.T) {
		d, err := wire.ParseCsilDecimal("9007199254740993.12345")
		must(t, err)
		result := wire.QueryResult{Status: "partial", Quantities: []wire.Quantity{{Name: "spend", Value: d, Unit: "USD"}}}
		data := wire.EncodeQueryResult(result)
		got, err := wire.DecodeQueryResult(data)
		must(t, err)
		if got.Quantities[0].Value.String() != "9007199254740993.12345" {
			t.Fatal("decimal precision lost")
		}
		t.Run("rust", func(t *testing.T) { roundtrip(t, "result", data) })
	})
	t.Run("query_timestamp", func(t *testing.T) {
		q := wire.KnowledgeQuery{Question: "white dwarf", Purpose: "recall", Limit: 10, After: &fixedTime}
		data := wire.EncodeKnowledgeQuery(q)
		got, err := wire.DecodeKnowledgeQuery(data)
		must(t, err)
		if !got.After.Equal(fixedTime) {
			t.Fatal("timestamp changed")
		}
		t.Run("rust", func(t *testing.T) { roundtrip(t, "query", data) })
	})
	t.Run("malformed", func(t *testing.T) {
		for _, data := range [][]byte{{}, {0xa1}, {0x9b, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}} {
			if _, err := wire.DecodeHistoryPage(data); err == nil {
				t.Fatal("accepted malformed data")
			}
		}
		data := wire.EncodeServiceError(wire.ServiceError{Code: "not-a-member", Message: "invalid"})
		if _, err := wire.DecodeServiceError(data); err == nil {
			t.Fatal("accepted unknown enum")
		}
		if rust != "" {
			cmd := exec.Command(rust, "error")
			cmd.Stdin = strings.NewReader(hex.EncodeToString(data))
			if err := cmd.Run(); err == nil {
				t.Fatal("Rust accepted unknown enum")
			}
		}
	})
	t.Run("descriptor", func(t *testing.T) {
		goSchema, err := os.ReadFile("generated/go/knowledge.csil-schema.cbor")
		must(t, err)
		rustSchema, err := os.ReadFile("generated/rust/knowledge.csil-schema.cbor")
		must(t, err)
		if !bytes.Equal(goSchema, rustSchema) {
			t.Fatal("descriptor mismatch")
		}
	})
}
