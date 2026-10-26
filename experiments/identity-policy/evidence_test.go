package policy

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"testing"
	"time"

	lk "github.com/catalystcommunity/linkkeys/sdks/regular-rp/go"
	api "github.com/catalystcommunity/linkkeys/sdks/regular-rp/go/generated"
)

type fixture struct {
	r                       Receiver
	a, q, attr              Statement
	private                 map[lk.InstanceRef][]ed25519.PrivateKey
	domainPrivate           map[string]ed25519.PrivateKey
	owner, guest, attester  lk.InstanceRef
	approved, withAttribute bool
}

func newFixture(t testing.TB, reverse bool) *fixture {
	t.Helper()
	now := time.Date(2026, 9, 30, 12, 0, 0, 123456789, time.UTC)
	primary, secondary := "primary.opal.test", "secondary.opal.test"
	if reverse {
		primary, secondary = secondary, primary
	}
	f := &fixture{r: Receiver{Audience: "music@" + primary, Now: now, MaxEvidenceAge: time.Minute,
		Anchors: map[string][]api.DomainPublicKey{}, Evidence: map[lk.InstanceRef]KeyEvidence{},
		Owners: map[string]lk.InstanceRef{}, ApprovedStatements: map[string]Approval{}, Withdrawn: map[string]bool{}, TrustedAttributeIssuers: map[lk.InstanceRef]bool{}},
		private: map[lk.InstanceRef][]ed25519.PrivateKey{}, domainPrivate: map[string]ed25519.PrivateKey{}, approved: true}
	for _, domain := range []string{primary, secondary} {
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		f.domainPrivate[domain] = priv
		f.r.Anchors[domain] = []api.DomainPublicKey{{KeyId: "domain-1", Algorithm: "ed25519", KeyUsage: "sign", PublicKey: pub, Fingerprint: lk.Fingerprint(pub), CreatedAt: "2020-01-01T00:00:00Z", ExpiresAt: "2126-01-01T00:00:00Z"}}
	}
	f.owner = lk.InstanceRef{SubjectUserID: "018f0000-0000-7000-8000-000000000001", SubjectDomain: primary, ApplicationID: "opal", InstanceID: "owner-device"}
	f.guest = lk.InstanceRef{SubjectUserID: "018f0000-0000-7000-8000-000000000002", SubjectDomain: secondary, ApplicationID: "opal", InstanceID: "guest-device"}
	f.attester = lk.InstanceRef{SubjectUserID: "018f0000-0000-7000-8000-000000000003", SubjectDomain: secondary, ApplicationID: "attribute-service", InstanceID: "attester"}
	for _, instance := range []lk.InstanceRef{f.owner, f.guest, f.attester} {
		f.enroll(t, instance)
	}
	expires := now.Add(time.Hour).Format(time.RFC3339Nano)
	f.a = Statement{Schema: schema, Kind: "allowance", ID: "allowance-1", Issuer: f.owner, Subject: f.guest, Audience: f.r.Audience, Resource: "kitchen", Purpose: "music", IssuedAt: now.Format(time.RFC3339Nano), NotBefore: now.Add(-time.Hour).Format(time.RFC3339Nano), ExpiresAt: &expires, Limit: "100.00"}
	f.q = Statement{Schema: schema, Kind: "request", ID: "request-1", Issuer: f.guest, Audience: f.r.Audience, Resource: "kitchen", Action: "queue", IssuedAt: now.Format(time.RFC3339Nano), NotBefore: now.Add(-time.Minute).Format(time.RFC3339Nano), ExpiresAt: &expires, Amount: "0"}
	truth := true
	f.attr = Statement{Schema: schema, Kind: "attribute", ID: "attribute-1", Issuer: f.attester, Subject: f.guest, Audience: f.r.Audience, IssuedAt: now.Format(time.RFC3339Nano), NotBefore: now.Add(-time.Hour).Format(time.RFC3339Nano), ExpiresAt: &expires, Attribute: "age.over21", Value: &truth}
	f.r.Owners["kitchen"] = f.owner
	f.r.Owners["bedroom"] = f.owner
	f.r.TrustedAttributeIssuers[f.attester] = true
	return f
}

func (f *fixture) enroll(t testing.TB, instance lk.InstanceRef) {
	t.Helper()
	e := KeyEvidence{LocalFreshness: "refreshed", UpstreamFreshness: "refreshed", FetchedAt: f.r.Now, RevocationsCheckedAt: f.r.Now}
	for i := 0; i < 3; i++ {
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		f.private[instance] = append(f.private[instance], priv)
		a := api.ApplicationKeyAttestation{SubjectUserId: instance.SubjectUserID, SubjectDomain: instance.SubjectDomain, ApplicationId: instance.ApplicationID, InstanceId: instance.InstanceID, KeyId: fmt.Sprintf("key-%d", i), KeyUsage: "sign", Algorithm: "ed25519", PublicKey: pub, Fingerprint: lk.Fingerprint(pub), KeyCreatedAt: f.r.Now.Add(-24 * time.Hour).Format(time.RFC3339Nano), KeyExpiresAt: f.r.Now.Add(24 * time.Hour).Format(time.RFC3339Nano), AttestedAt: f.r.Now.Add(-time.Hour).Format(time.RFC3339Nano), AttestationExpiresAt: f.r.Now.Add(2 * time.Hour).Format(time.RFC3339Nano)}
		e.Attestations = append(e.Attestations, f.attest(a))
	}
	f.r.Evidence[instance] = e
}

func (f *fixture) attest(a api.ApplicationKeyAttestation) api.SignedApplicationKeyAttestation {
	b := api.EncodeApplicationKeyAttestation(a)
	return api.SignedApplicationKeyAttestation{Attestation: b, Signatures: []api.ClaimSignature{{Domain: a.SubjectDomain, SignedByKeyId: "domain-1", Signature: ed25519.Sign(f.domainPrivate[a.SubjectDomain], lk.AttestationSignatureInput(b))}}}
}

func (f *fixture) sign(s Statement) Signed {
	b, _ := json.Marshal(s)
	return Signed{Body: b, Signature: ed25519.Sign(f.private[s.Issuer][0], signatureInput(b)), KeyID: "key-0"}
}

func (f *fixture) messages() (Signed, Signed, *Signed) {
	a, q := f.sign(f.a), f.sign(f.q)
	if f.approved {
		// Fixture admission models an already authenticated approval. The
		// dedicated approval tests exercise recordApproval itself.
		f.r.ApprovedStatements[digest(a.Body)] = Approval{Subject: HumanIdentity{AccountID: f.a.Issuer.SubjectUserID, Domain: f.a.Issuer.SubjectDomain}, StatementDigest: digest(a.Body), ApprovedAt: f.r.Now, Allowance: f.a}
	}
	if !f.withAttribute {
		return a, q, nil
	}
	attr := f.sign(f.attr)
	return a, q, &attr
}

func (f *fixture) alterKey(t testing.TB, edit func(*api.ApplicationKeyAttestation)) {
	t.Helper()
	e := f.r.Evidence[f.guest]
	a, err := api.DecodeApplicationKeyAttestation(e.Attestations[0].Attestation)
	if err != nil {
		t.Fatal(err)
	}
	edit(&a)
	e.Attestations[0] = f.attest(a)
	f.r.Evidence[f.guest] = e
}

type scenario struct {
	name  string
	allow bool
	edit  func(*fixture)
}

func scenarios() []scenario {
	return []scenario{
		{"guest_kitchen", true, func(f *fixture) {}},
		{"other_room", false, func(f *fixture) { f.q.Resource = "bedroom" }},
		{"issuer_lacks_authority_T01", false, func(f *fixture) { f.a.Issuer = f.guest }},
		{"wrong_delegate", false, func(f *fixture) { f.a.Subject = f.owner }},
		{"app_key_without_human_approval", false, func(f *fixture) { f.approved = false }},
		{"calendar_propose", true, func(f *fixture) { f.a.Purpose = "calendar"; f.q.Action = "propose" }},
		{"calendar_delete", false, func(f *fixture) { f.a.Purpose = "calendar"; f.q.Action = "delete" }},
		{"indefinite_allowance", true, func(f *fixture) { f.a.ExpiresAt = nil }},
		{"withdrawn_indefinite", false, func(f *fixture) { f.a.ExpiresAt = nil; f.r.Withdrawn[identity(f.a.Issuer)+"/"+f.a.ID] = true }},
		{"expiry_equal_now", false, func(f *fixture) { s := f.r.Now.Format(time.RFC3339Nano); f.a.ExpiresAt = &s }},
		{"expiry_one_nanosecond_later", true, func(f *fixture) { s := f.r.Now.Add(time.Nanosecond).Format(time.RFC3339Nano); f.a.ExpiresAt = &s }},
		{"not_yet_valid", false, func(f *fixture) { f.a.NotBefore = f.r.Now.Add(time.Nanosecond).Format(time.RFC3339Nano) }},
		{"deny_overrides_permit", false, func(f *fixture) { f.r.Blocked = true }},
		{"exact_large_decimal_equal", true, func(f *fixture) { f.q.Amount = "9007199254740993.00001"; f.a.Limit = f.q.Amount }},
		{"exact_large_decimal_above", false, func(f *fixture) { f.q.Amount = "9007199254740993.00002"; f.a.Limit = "9007199254740993.00001" }},
		{"trusted_age_attribute", true, func(f *fixture) { f.withAttribute = true; f.q.Action = "queue_age_restricted" }},
		{"untrusted_age_signer", false, func(f *fixture) { f.withAttribute = true; f.q.Action = "queue_age_restricted"; f.attr.Issuer = f.owner }},
		{"age_wrong_subject", false, func(f *fixture) {
			f.withAttribute = true
			f.q.Action = "queue_age_restricted"
			f.attr.Subject = f.owner
		}},
		{"age_missing", false, func(f *fixture) { f.q.Action = "queue_age_restricted" }},
		{"unknown_action", false, func(f *fixture) { f.q.Action = "admin" }},
	}
}

func TestVerifiedPolicy(t *testing.T) {
	e, err := newEvaluator(permitExpression, denyExpression)
	if err != nil {
		t.Fatal(err)
	}
	for _, reverse := range []bool{false, true} {
		for _, s := range scenarios() {
			t.Run(fmt.Sprintf("reverse_%t/%s", reverse, s.name), func(t *testing.T) {
				f := newFixture(t, reverse)
				s.edit(f)
				a, q, attr := f.messages()
				facts, err := f.r.facts(a, q, attr)
				if err != nil {
					t.Fatal(err)
				}
				got, err := e.decide(facts)
				if err != nil || got != s.allow {
					t.Fatalf("allow=%t want=%t error=%v", got, s.allow, err)
				}
			})
		}
	}
}

func TestEvidenceRejection(t *testing.T) {
	for _, name := range []string{"recipient_T23", "schema", "body_tamper", "context_tamper", "wrong_key", "unknown_domain", "local_stale_T24", "upstream_stale_T24", "old_revocations_T24", "future_fetch", "expired_attestation", "expired_key", "future_key", "future_attestation", "bad_attestation_signature", "revoked_key", "malformed_decimal", "malformed_timestamp", "unknown_instance", "same_uuid_other_domain", "request_no_expiry", "unknown_resource"} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, false)
			switch name {
			case "schema":
				f.q.Schema = "other/1"
			case "unknown_domain":
				delete(f.r.Anchors, f.guest.SubjectDomain)
			case "local_stale_T24":
				e := f.r.Evidence[f.guest]
				e.LocalFreshness = "stale"
				f.r.Evidence[f.guest] = e
			case "upstream_stale_T24":
				e := f.r.Evidence[f.guest]
				e.LocalFreshness = "fresh"
				e.UpstreamFreshness = "stale"
				f.r.Evidence[f.guest] = e
			case "old_revocations_T24":
				e := f.r.Evidence[f.guest]
				e.RevocationsCheckedAt = f.r.Now.Add(-2 * time.Minute)
				f.r.Evidence[f.guest] = e
			case "future_fetch":
				e := f.r.Evidence[f.guest]
				e.FetchedAt = f.r.Now.Add(time.Minute)
				f.r.Evidence[f.guest] = e
			case "expired_attestation":
				f.alterKey(t, func(a *api.ApplicationKeyAttestation) { a.AttestationExpiresAt = f.r.Now.Format(time.RFC3339Nano) })
			case "expired_key":
				f.alterKey(t, func(a *api.ApplicationKeyAttestation) { a.KeyExpiresAt = f.r.Now.Format(time.RFC3339Nano) })
			case "future_key":
				f.alterKey(t, func(a *api.ApplicationKeyAttestation) {
					a.KeyCreatedAt = f.r.Now.Add(time.Hour).Format(time.RFC3339Nano)
				})
			case "future_attestation":
				f.alterKey(t, func(a *api.ApplicationKeyAttestation) { a.AttestedAt = f.r.Now.Add(time.Hour).Format(time.RFC3339Nano) })
			case "bad_attestation_signature":
				e := f.r.Evidence[f.guest]
				e.Attestations[0].Signatures[0].Signature[0] ^= 1
				f.r.Evidence[f.guest] = e
			case "revoked_key":
				e := f.r.Evidence[f.guest]
				a, err := api.DecodeApplicationKeyAttestation(e.Attestations[0].Attestation)
				if err != nil {
					t.Fatal(err)
				}
				signers := []lk.ApplicationSigner{}
				for i := 1; i < 3; i++ {
					signers = append(signers, lk.ApplicationSigner{KeyID: fmt.Sprintf("key-%d", i), Algorithm: "ed25519", PrivateKeyBytes: f.private[f.guest][i].Seed()})
				}
				rev, err := lk.SignRevocation(f.guest, "key-0", a.Fingerprint, f.r.Now.Format(time.RFC3339Nano), signers)
				if err != nil {
					t.Fatal(err)
				}
				e.Revocations = []api.ApplicationKeyRevocation{rev}
				f.r.Evidence[f.guest] = e
			case "malformed_decimal":
				f.q.Amount = "NaN"
			case "malformed_timestamp":
				f.a.NotBefore = "tomorrow"
			case "unknown_instance":
				old := f.guest
				f.guest.InstanceID = "other"
				f.private[f.guest] = f.private[old]
				f.q.Issuer = f.guest
			case "same_uuid_other_domain":
				old := f.guest
				f.guest.SubjectDomain = f.owner.SubjectDomain
				f.private[f.guest] = f.private[old]
				f.q.Issuer = f.guest
			case "request_no_expiry":
				f.q.ExpiresAt = nil
			case "unknown_resource":
				f.q.Resource = "unknown"
			}
			a, q, attr := f.messages()
			switch name {
			case "recipient_T23":
				f.r.Audience = "calendar@primary.opal.test"
			case "body_tamper":
				q.Body[10] ^= 1
			case "context_tamper":
				q.Signature = ed25519.Sign(f.private[f.guest][0], append([]byte("wrong-context"), q.Body...))
			case "wrong_key":
				q.KeyID = "key-1"
			}
			if _, err := f.r.facts(a, q, attr); err == nil {
				t.Fatal("invalid evidence accepted")
			}
		})
	}
}

func TestPolicyValidationAndErrors(t *testing.T) {
	for _, expression := range []string{`unknown == true`, `issuer + true`, `42`, `true &&`} {
		if _, err := newEvaluator(expression, denyExpression); err == nil {
			t.Errorf("invalid policy accepted: %s", expression)
		}
	}
	f := newFixture(t, false)
	a, q, attr := f.messages()
	facts, err := f.r.facts(a, q, attr)
	if err != nil {
		t.Fatal(err)
	}
	e, err := newEvaluator(permitExpression, denyExpression)
	if err != nil {
		t.Fatal(err)
	}
	delete(facts, "blocked")
	if allow, err := e.decide(facts); allow || err == nil {
		t.Fatalf("missing deny fact did not fail closed: %t %v", allow, err)
	}
	facts["blocked"] = "false"
	if allow, err := e.decide(facts); allow || err == nil {
		t.Fatalf("invalid deny fact did not fail closed: %t %v", allow, err)
	}
}

func BenchmarkCELDecision(b *testing.B) {
	f := newFixture(b, false)
	a, q, attr := f.messages()
	facts, err := f.r.facts(a, q, attr)
	if err != nil {
		b.Fatal(err)
	}
	e, err := newEvaluator(permitExpression, denyExpression)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		allow, err := e.decide(facts)
		if err != nil || !allow {
			b.Fatal(allow, err)
		}
	}
}

func BenchmarkVerifyAndDecide(b *testing.B) {
	f := newFixture(b, false)
	a, q, attr := f.messages()
	e, err := newEvaluator(permitExpression, denyExpression)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		facts, err := f.r.facts(a, q, attr)
		if err != nil {
			b.Fatal(err)
		}
		allow, err := e.decide(facts)
		if err != nil || !allow {
			b.Fatal(allow, err)
		}
	}
}

func BenchmarkCELCompile(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := newEvaluator(permitExpression, denyExpression); err != nil {
			b.Fatal(err)
		}
	}
}

func TestCELFootprint(t *testing.T) {
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()
	e, err := newEvaluator(permitExpression, denyExpression)
	if err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	runtime.ReadMemStats(&after)
	runtime.KeepAlive(e)
	t.Logf("compile_us=%d allocated_bytes=%d process_rss=%s", elapsed.Microseconds(), after.TotalAlloc-before.TotalAlloc, residentMemory(os.Getpid()))
}

func TestAuthenticatedApprovalBinding(t *testing.T) {
	for _, name := range []string{"same_bytes_idempotent", "wrong_account", "wrong_domain", "changed_scope", "changed_delegate", "changed_recipient", "changed_expiry"} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, false)
			f.approved = false
			a, q, attr := f.messages()
			person := HumanIdentity{AccountID: f.owner.SubjectUserID, Domain: f.owner.SubjectDomain}
			if name == "wrong_account" {
				person.AccountID = f.guest.SubjectUserID
			}
			if name == "wrong_domain" {
				person.Domain = f.guest.SubjectDomain
			}
			err := f.r.recordApproval(person, a)
			if name == "wrong_account" || name == "wrong_domain" {
				if err == nil {
					t.Fatal("wrong human approved allowance")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			original := f.r.ApprovedStatements[digest(a.Body)]
			if name == "same_bytes_idempotent" {
				f.r.Now = f.r.Now.Add(time.Second)
				if err := f.r.recordApproval(person, a); err != nil {
					t.Fatal(err)
				}
				if f.r.ApprovedStatements[digest(a.Body)] != original {
					t.Fatal("repeat approval replaced its first record")
				}
				facts, err := f.r.facts(a, q, attr)
				if err != nil || facts["approved"] != true {
					t.Fatal("approved bytes not recognized", err)
				}
				return
			}
			switch name {
			case "changed_scope":
				f.a.Purpose = "calendar"
			case "changed_delegate":
				f.a.Subject = f.owner
			case "changed_recipient":
				f.a.Audience = "other-service"
			case "changed_expiry":
				f.a.ExpiresAt = nil
			}
			changed := f.sign(f.a)
			if f.r.approved(f.a, changed) {
				t.Fatal("old approval authorized changed bytes")
			}
		})
	}
}

func TestRetainedApprovalAfterIssuerKeyExpiry(t *testing.T) {
	f := newFixture(t, false)
	f.approved = false
	f.a.ExpiresAt = nil
	a, _, _ := f.messages()
	if err := f.r.recordApproval(HumanIdentity{AccountID: f.owner.SubjectUserID, Domain: f.owner.SubjectDomain}, a); err != nil {
		t.Fatal(err)
	}
	f.r.Now = f.r.Now.Add(48 * time.Hour)
	// The request uses renewed, current evidence. The allowance issuer is not
	// online and its old signing keys and attestations have expired.
	f.private[f.guest] = nil
	f.enroll(t, f.guest)
	f.q.IssuedAt = f.r.Now.Format(time.RFC3339Nano)
	f.q.NotBefore = f.q.IssuedAt
	end := f.r.Now.Add(time.Minute).Format(time.RFC3339Nano)
	f.q.ExpiresAt = &end
	q := f.sign(f.q)
	facts, err := f.r.facts(a, q, nil)
	if err != nil {
		t.Fatal(err)
	}
	e, err := newEvaluator(permitExpression, denyExpression)
	if err != nil {
		t.Fatal(err)
	}
	if allow, err := e.decide(facts); err != nil || !allow {
		t.Fatal("accepted indefinite allowance did not survive key expiry", err)
	}
	f.r.Withdrawn[identity(f.a.Issuer)+"/"+f.a.ID] = true
	facts, err = f.r.facts(a, q, nil)
	if err != nil {
		t.Fatal(err)
	}
	if allow, err := e.decide(facts); err != nil || allow {
		t.Fatal("withdrawal did not stop accepted allowance", err)
	}
}

func TestApprovalAuthenticatesNewStatement(t *testing.T) {
	for _, name := range []string{"bad_signature", "wrong_recipient", "expired_issuer_key", "untrusted_issuer_domain"} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, false)
			f.approved = false
			a, _, _ := f.messages()
			switch name {
			case "bad_signature":
				a.Signature[0] ^= 1
			case "wrong_recipient":
				f.r.Audience = "different-service"
			case "untrusted_issuer_domain":
				delete(f.r.Anchors, f.owner.SubjectDomain)
			case "expired_issuer_key":
				e := f.r.Evidence[f.owner]
				k, err := api.DecodeApplicationKeyAttestation(e.Attestations[0].Attestation)
				if err != nil {
					t.Fatal(err)
				}
				k.KeyExpiresAt = f.r.Now.Format(time.RFC3339Nano)
				e.Attestations[0] = f.attest(k)
				f.r.Evidence[f.owner] = e
			}
			if err := f.r.recordApproval(HumanIdentity{AccountID: f.owner.SubjectUserID, Domain: f.owner.SubjectDomain}, a); err == nil {
				t.Fatal("invalid statement approved")
			}
			if len(f.r.ApprovedStatements) != 0 {
				t.Fatal("failed approval changed records")
			}
		})
	}
}
