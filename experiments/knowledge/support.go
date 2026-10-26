package knowledge

// This internal experiment checks citation integrity and current access. It does
// not establish that a model's interpretation of the quoted text is correct.
import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	wire "opal.local/experiments/knowledge/generated/go"
)

type supportSource struct {
	ID   string `json:"id"`
	At   string `json:"observed_at"`
	Text string `json:"text"`
}
type supportRequest struct {
	Question string          `json:"question"`
	Now      string          `json:"now"`
	Sources  []supportSource `json:"sources"`
}
type supportQuote struct {
	Source string `json:"source"`
	Quote  string `json:"quote"`
}
type supportAssessment struct {
	Mode     string         `json:"mode"`
	Evidence []supportQuote `json:"evidence"`
	Gap      string         `json:"gap"`
}
type supportBinding struct {
	ref    wire.RecordRef
	digest [32]byte
}
type supportPacket struct {
	request  supportRequest
	bindings []supportBinding
	access   [32]byte
}

func accessDigest(a wire.ContextAccess) [32]byte {
	b, _ := json.Marshal(a)
	return sha256.Sum256(b)
}
func recordDigest(r wire.EvidenceRecord) [32]byte {
	return sha256.Sum256(wire.EncodeHistoryPage(wire.HistoryPage{Records: []wire.EvidenceRecord{r}}))
}

// Policy checks use a single database snapshot per boundary. No transaction is
// held during inference. Revocation cannot withdraw text already sent to a model.
func (s *store) prepareSupport(ctx context.Context, a wire.ContextAccess, question string, refs []wire.RecordRef) (supportPacket, error) {
	p := supportPacket{access: accessDigest(a)}
	if !utf8.ValidString(question) || len(question) == 0 || len(question) > 2048 || len(refs) > 10 || a.Purpose != "recall" {
		return p, ErrInvalid
	}
	p.request = supportRequest{Question: question, Now: s.now().UTC().Format(time.RFC3339Nano), Sources: []supportSource{}}
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return p, ErrUnavailable
	}
	defer tx.Rollback(ctx)
	// The question itself belongs to the query control scope. An empty search
	// result must not bypass caller or processor checks for that input.
	if err := s.allowed(ctx, tx, a, []string{scopeKey(a.ControlScope)}, false); err != nil {
		if errors.Is(err, ErrDenied) {
			return supportPacket{}, ErrDenied
		}
		return supportPacket{}, ErrUnavailable
	}
	seen := map[string]bool{}
	total := 0
	for _, ref := range refs {
		if seen[refKey(ref)] {
			return supportPacket{}, ErrInvalid
		}
		seen[refKey(ref)] = true
		r, err := s.read(ctx, tx, a, ref, map[string]bool{})
		if errors.Is(err, ErrDenied) {
			continue
		} // Do not disclose denied or missing IDs.
		if err != nil {
			return supportPacket{}, ErrUnavailable
		}
		source, ok := r.(wire.SourceContext)
		if !ok {
			return supportPacket{}, ErrInvalid
		}
		total += len(source.Content)
		if !utf8.ValidString(source.Content) || len(source.Content) > 4096 || total > 16000 {
			return supportPacket{}, ErrLimit
		}
		at := ""
		if source.Header.ObservedAt != nil {
			at = source.Header.ObservedAt.UTC().Format(time.RFC3339Nano)
		}
		p.request.Sources = append(p.request.Sources, supportSource{fmt.Sprintf("e%02d", len(p.bindings)+1), at, source.Content})
		p.bindings = append(p.bindings, supportBinding{ref, recordDigest(r)})
	}
	if err := tx.Commit(ctx); err != nil {
		return supportPacket{}, ErrUnavailable
	}
	return p, nil
}

// Recheck every input, not only cited inputs. Uncited text can affect a model.
// A caller must run this immediately before releasing a retained assessment.
// This is not an atomic network send or a general policy-revocation protocol.
func (s *store) recheckSupport(ctx context.Context, a wire.ContextAccess, p supportPacket) error {
	if accessDigest(a) != p.access {
		return ErrDenied
	}
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return ErrUnavailable
	}
	defer tx.Rollback(ctx)
	if err := s.allowed(ctx, tx, a, []string{scopeKey(a.ControlScope)}, false); err != nil {
		if errors.Is(err, ErrDenied) {
			return ErrDenied
		}
		return ErrUnavailable
	}
	for _, binding := range p.bindings {
		r, err := s.read(ctx, tx, a, binding.ref, map[string]bool{})
		if errors.Is(err, ErrDenied) {
			return ErrDenied
		}
		if err != nil {
			return ErrUnavailable
		}
		if recordDigest(r) != binding.digest {
			return ErrConflict
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return ErrUnavailable
	}
	return nil
}

func validateSupport(raw []byte, request supportRequest) (supportAssessment, error) {
	var a supportAssessment
	if len(raw) > 8192 || !utf8.Valid(raw) || uniqueJSON(raw) != nil {
		return a, ErrInvalid
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || len(fields) != 3 || fields["mode"] == nil || fields["evidence"] == nil || fields["gap"] == nil || bytes.Equal(fields["gap"], []byte("null")) {
		return a, ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&a) != nil {
		return a, ErrInvalid
	}
	if d.Decode(new(any)) != io.EOF {
		return a, ErrInvalid
	}
	switch a.Mode {
	case "answer", "partial", "clarify", "unavailable", "no_evidence":
	default:
		return a, ErrInvalid
	}
	if a.Evidence == nil || len(a.Evidence) > 6 || len(a.Gap) > 800 {
		return a, ErrInvalid
	}
	if (a.Mode == "no_evidence" && len(a.Evidence) != 0) || (a.Mode != "no_evidence" && len(a.Evidence) == 0) {
		return a, ErrInvalid
	}
	if (a.Mode == "answer") != (strings.TrimSpace(a.Gap) == "") {
		return a, ErrInvalid
	}
	sources := map[string]string{}
	for _, source := range request.Sources {
		sources[source.ID] = source.Text
	}
	seen := map[string]bool{}
	for _, evidence := range a.Evidence {
		text, ok := sources[evidence.Source]
		if !ok || seen[evidence.Source] || len(evidence.Quote) < 12 || len(evidence.Quote) > 700 || !strings.Contains(text, evidence.Quote) {
			return a, ErrInvalid
		}
		seen[evidence.Source] = true
	}
	return a, nil
}

// Reject duplicate object keys at any depth. encoding/json otherwise keeps the
// last value, which can give an auditor and a consumer different interpretations.
func uniqueJSON(raw []byte) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	var value func(int) error
	value = func(depth int) error {
		if depth > 8 {
			return ErrLimit
		}
		token, err := d.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				name, err := d.Token()
				if err != nil {
					return err
				}
				key, ok := name.(string)
				if !ok || seen[key] {
					return ErrInvalid
				}
				seen[key] = true
				if err := value(depth + 1); err != nil {
					return err
				}
			}
		case '[':
			for d.More() {
				if err := value(depth + 1); err != nil {
					return err
				}
			}
		default:
			return ErrInvalid
		}
		_, err = d.Token()
		return err
	}
	if err := value(0); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return ErrInvalid
	}
	return nil
}
