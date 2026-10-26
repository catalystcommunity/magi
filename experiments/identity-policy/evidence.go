// Package policy is an isolated W02 experiment, not a production protocol.
package policy

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"time"

	lk "github.com/catalystcommunity/linkkeys/sdks/regular-rp/go"
	api "github.com/catalystcommunity/linkkeys/sdks/regular-rp/go/generated"
)

const signatureContext = "opal-w02-experiment-statement-v1\x00"
const schema = "opal.w02.fixture/1"

// Statement is a test-only signed shape. W03 must replace it with CSIL.
type Statement struct {
	Schema    string
	Kind      string
	ID        string
	Issuer    lk.InstanceRef
	Subject   lk.InstanceRef
	Audience  string
	Resource  string
	Purpose   string
	Action    string
	IssuedAt  string
	NotBefore string
	ExpiresAt *string
	Amount    string
	Limit     string
	Attribute string
	Value     *bool
}

type Signed struct {
	Body, Signature []byte
	KeyID           string
}

type HumanIdentity struct{ AccountID, Domain string }

// Approval is receiver-owned test state. Production must commit it from an
// authenticated approval operation, not accept it as a caller-supplied flag.
type Approval struct {
	Subject         HumanIdentity
	StatementDigest string
	ApprovedAt      time.Time
	Allowance       Statement
}

// KeyEvidence is delivered by a trusted resolver, never supplied by a caller.
// Anchors are independent receiver configuration, not keys from the message.
type KeyEvidence struct {
	Attestations         []api.SignedApplicationKeyAttestation
	Revocations          []api.ApplicationKeyRevocation
	LocalFreshness       string
	UpstreamFreshness    string
	FetchedAt            time.Time
	RevocationsCheckedAt time.Time
}

type Receiver struct {
	Audience       string
	Now            time.Time
	MaxEvidenceAge time.Duration
	Anchors        map[string][]api.DomainPublicKey
	Evidence       map[lk.InstanceRef]KeyEvidence
	Owners         map[string]lk.InstanceRef
	// Test records stand in for a future authenticated approval transaction.
	ApprovedStatements      map[string]Approval
	Withdrawn               map[string]bool
	TrustedAttributeIssuers map[lk.InstanceRef]bool
	Blocked                 bool
}

func identity(v lk.InstanceRef) string  { b, _ := json.Marshal(v); return string(b) }
func digest(b []byte) string            { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func signatureInput(body []byte) []byte { return append([]byte(signatureContext), body...) }

// recordApproval models only the binding after authentication. It does not
// perform login, session validation, CSRF checks, or durable storage.
func (r *Receiver) recordApproval(authenticated HumanIdentity, allowance Signed) error {
	a, err := r.authenticate(allowance, "allowance")
	if err != nil {
		return err
	}
	if authenticated.AccountID != a.Issuer.SubjectUserID || authenticated.Domain != a.Issuer.SubjectDomain {
		return errors.New("approval subject does not match issuer")
	}
	h := digest(allowance.Body)
	if existing, ok := r.ApprovedStatements[h]; ok {
		if existing.Subject != authenticated {
			return errors.New("approval identity conflict")
		}
		return nil
	}
	r.ApprovedStatements[h] = Approval{Subject: authenticated, StatementDigest: h, ApprovedAt: r.Now, Allowance: a}
	return nil
}

func (r Receiver) approved(a Statement, signed Signed) bool {
	h := digest(signed.Body)
	p, ok := r.ApprovedStatements[h]
	return ok && p.StatementDigest == h && p.Subject.AccountID == a.Issuer.SubjectUserID && p.Subject.Domain == a.Issuer.SubjectDomain && !p.ApprovedAt.After(r.Now)
}

func fresh(status string) bool { return status == "fresh" || status == "refreshed" }

func (r Receiver) authenticate(s Signed, kind string) (Statement, error) {
	var body Statement
	if len(s.Body) == 0 || len(s.Body) > 16384 {
		return body, errors.New("body size")
	}
	d := json.NewDecoder(bytes.NewReader(s.Body))
	d.DisallowUnknownFields()
	if err := d.Decode(&body); err != nil {
		return body, fmt.Errorf("decode: %w", err)
	}
	// This fixture accepts only its own deterministic JSON encoding. This also
	// rejects duplicate fields, trailing values, and ambiguous encodings.
	encoded, _ := json.Marshal(body)
	if !bytes.Equal(encoded, s.Body) {
		return body, errors.New("noncanonical fixture body")
	}
	if body.Schema != schema || body.Kind != kind || body.ID == "" || body.Audience != r.Audience {
		return body, errors.New("schema, kind, id, or recipient binding")
	}
	if body.Issuer.SubjectUserID == "" || body.Issuer.SubjectDomain == "" || body.Issuer.ApplicationID == "" || body.Issuer.InstanceID == "" {
		return body, errors.New("incomplete issuer")
	}
	e, ok := r.Evidence[body.Issuer]
	if !ok || !fresh(e.LocalFreshness) || !fresh(e.UpstreamFreshness) || r.MaxEvidenceAge <= 0 {
		return body, errors.New("missing or stale key evidence")
	}
	for _, at := range []time.Time{e.FetchedAt, e.RevocationsCheckedAt} {
		if at.After(r.Now) || r.Now.Sub(at) > r.MaxEvidenceAge {
			return body, errors.New("key evidence age")
		}
	}
	anchors := r.Anchors[body.Issuer.SubjectDomain]
	if len(anchors) == 0 {
		return body, errors.New("untrusted identity domain")
	}
	keys := lk.VerifyApplicationKeySet(e.Attestations, e.Revocations, anchors, body.Issuer, r.Now, 0)
	if len(keys.Rejected) != 0 {
		return body, errors.New("invalid key evidence record")
	}
	key, err := keys.KeyForUse(s.KeyID, lk.KeyUsageSign, "ed25519")
	if err != nil {
		return body, fmt.Errorf("key use: %w", err)
	}
	created, err := time.Parse(time.RFC3339Nano, key.KeyCreatedAt)
	if err != nil || created.After(r.Now) {
		return body, errors.New("key creation time")
	}
	attested, err := time.Parse(time.RFC3339Nano, key.AttestedAt)
	if err != nil || attested.After(r.Now) {
		return body, errors.New("attestation issue time")
	}
	issued, err := time.Parse(time.RFC3339Nano, body.IssuedAt)
	if err != nil || issued.After(r.Now) || issued.Before(created) {
		return body, errors.New("statement issue time")
	}
	if !ed25519.Verify(ed25519.PublicKey(key.PublicKey), signatureInput(s.Body), s.Signature) {
		return body, errors.New("statement signature")
	}
	return body, nil
}

var decimalPattern = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?$`)

// Exact decimal comparison is a trusted conversion, not a float conversion.
func decimalAtMost(amount, limit string) (bool, error) {
	if len(amount) > 128 || len(limit) > 128 || !decimalPattern.MatchString(amount) || !decimalPattern.MatchString(limit) {
		return false, errors.New("invalid decimal")
	}
	a, ok := new(big.Rat).SetString(amount)
	if !ok {
		return false, errors.New("invalid amount")
	}
	b, ok := new(big.Rat).SetString(limit)
	if !ok {
		return false, errors.New("invalid limit")
	}
	if a.Sign() < 0 || b.Sign() < 0 {
		return false, errors.New("negative fixture amount")
	}
	return a.Cmp(b) <= 0, nil
}

func validWindow(s Statement, now time.Time) (bool, error) {
	start, err := time.Parse(time.RFC3339Nano, s.NotBefore)
	if err != nil {
		return false, err
	}
	if s.ExpiresAt == nil {
		return !now.Before(start), nil
	}
	end, err := time.Parse(time.RFC3339Nano, *s.ExpiresAt)
	if err != nil || !end.After(start) {
		return false, errors.New("invalid time window")
	}
	return !now.Before(start) && now.Before(end), nil
}

// Facts are created only after signature verification. Policy engines receive
// verified facts, never caller-supplied claims of ownership or approval.
func (r Receiver) facts(allowance, request Signed, attribute *Signed) (map[string]any, error) {
	var a Statement
	if p, ok := r.ApprovedStatements[digest(allowance.Body)]; ok {
		// The receiver retains evidence it authenticated at admission. Current
		// request authentication is separate from an accepted allowance's life.
		a = p.Allowance
		if !r.approved(a, allowance) || a.Audience != r.Audience || a.Schema != schema || a.Kind != "allowance" {
			return nil, errors.New("invalid retained approval")
		}
	} else {
		var err error
		a, err = r.authenticate(allowance, "allowance")
		if err != nil {
			return nil, err
		}
	}
	q, err := r.authenticate(request, "request")
	if err != nil {
		return nil, err
	}
	window, err := validWindow(a, r.Now)
	if err != nil {
		return nil, err
	}
	requestWindow, err := validWindow(q, r.Now)
	if err != nil {
		return nil, err
	}
	if !requestWindow || q.ExpiresAt == nil {
		return nil, errors.New("request deadline")
	}
	amountOK, err := decimalAtMost(q.Amount, a.Limit)
	if err != nil {
		return nil, err
	}
	owner, known := r.Owners[q.Resource]
	if !known {
		return nil, errors.New("unknown local resource")
	}
	ageTrusted := false
	if attribute != nil {
		claim, err := r.authenticate(*attribute, "attribute")
		if err != nil {
			return nil, err
		}
		current, err := validWindow(claim, r.Now)
		if err != nil {
			return nil, err
		}
		ageTrusted = current && claim.Subject == q.Issuer && claim.Attribute == "age.over21" && claim.Value != nil && *claim.Value && r.TrustedAttributeIssuers[claim.Issuer]
	}
	return map[string]any{
		"issuer": identity(a.Issuer), "owner": identity(owner),
		"delegate": identity(a.Subject), "actor": identity(q.Issuer),
		"resource": a.Resource, "target": q.Resource,
		"purpose": a.Purpose, "action": q.Action,
		"window": window, "approved": r.approved(a, allowance),
		"withdrawn": r.Withdrawn[identity(a.Issuer)+"/"+a.ID], "blocked": r.Blocked,
		"amount_ok": amountOK, "age_trusted": ageTrusted,
	}, nil
}
