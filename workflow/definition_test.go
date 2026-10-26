package workflow

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	w "github.com/catalystcommunity/magi/generated/go"
	"github.com/catalystcommunity/magi/identity"
)

var definitionOwner = w.ScopeRef{Authority: w.IdentityRef{Domain: "catalystsquad.local", AccountId: "household"}, ScopeId: "private-test"}

func textPorts(names ...string) []w.WorkflowPort {
	out := []w.WorkflowPort{}
	for _, name := range names {
		out = append(out, w.WorkflowPort{Name: name, ValueType: "text"})
	}
	return out
}
func textValues(name, text string) w.WorkflowValues {
	return w.WorkflowValues{Values: []w.WorkflowDatum{{Name: name, Value: text}}}
}
func sampleDefinition() w.WorkflowDefinition {
	capability := "fixture.draft"
	return w.WorkflowDefinition{Owner: definitionOwner, Id: "meeting-brief", Revision: "v1", Title: "Prepare a meeting brief", Inputs: textPorts("subject"), Outputs: textPorts("brief"), Steps: []w.WorkflowStep{
		{Id: "draft", Kind: "call", Inputs: textPorts("subject"), Outputs: textPorts("brief"), Bindings: []w.WorkflowBinding{{Input: "subject", SourceStep: "$input", Output: "subject"}}, Capability: &capability, Prompt: &w.WorkflowPrompt{Revision: "v1", Template: "Prepare a brief about {{subject}}."}},
		{Id: "review", Kind: "human", DependsOn: []string{"draft"}, Inputs: textPorts("brief"), Outputs: textPorts("approved"), Bindings: []w.WorkflowBinding{{Input: "brief", SourceStep: "draft", Output: "brief"}}},
		{Id: "finish", Kind: "finish", DependsOn: []string{"review"}, Inputs: textPorts("approved"), Outputs: textPorts("approved"), Bindings: []w.WorkflowBinding{{Input: "approved", SourceStep: "review", Output: "approved"}}},
	}, Results: []w.WorkflowBinding{{Input: "brief", SourceStep: "finish", Output: "approved"}}}
}
func TestDefinitionValidation(t *testing.T) {
	if v := ValidateDefinition(sampleDefinition()); !v.Valid {
		t.Fatal(v)
	}
	cases := map[string]func(*w.WorkflowDefinition){
		"cycle":                 func(d *w.WorkflowDefinition) { d.Steps[0].DependsOn = []string{"finish"} },
		"duplicate-step":        func(d *w.WorkflowDefinition) { d.Steps[1].Id = "draft" },
		"missing-dependency":    func(d *w.WorkflowDefinition) { d.Steps[1].DependsOn = []string{"absent"} },
		"duplicate-dependency":  func(d *w.WorkflowDefinition) { d.Steps[1].DependsOn = []string{"draft", "draft"} },
		"data-without-order":    func(d *w.WorkflowDefinition) { d.Steps[1].DependsOn = nil },
		"mismatched-port-type":  func(d *w.WorkflowDefinition) { d.Steps[1].Inputs[0].ValueType = "integer" },
		"missing-port":          func(d *w.WorkflowDefinition) { d.Steps[0].Bindings = nil },
		"duplicate-port":        func(d *w.WorkflowDefinition) { d.Inputs = append(d.Inputs, d.Inputs[0]) },
		"unsupported-kind":      func(d *w.WorkflowDefinition) { d.Steps[0].Kind = "shell" },
		"missing-capability":    func(d *w.WorkflowDefinition) { d.Steps[0].Capability = nil },
		"human-with-capability": func(d *w.WorkflowDefinition) { d.Steps[1].Capability = d.Steps[0].Capability },
		"missing-finish":        func(d *w.WorkflowDefinition) { d.Steps = d.Steps[:2] },
		"duplicate-finish":      func(d *w.WorkflowDefinition) { d.Steps[1].Kind = "finish" },
		"finish-transformation": func(d *w.WorkflowDefinition) { d.Steps[2].Outputs[0].Name = "other" },
		"orphan-step":           func(d *w.WorkflowDefinition) { d.Steps = append(d.Steps, w.WorkflowStep{Id: "orphan", Kind: "human"}) },
		"bad-result":            func(d *w.WorkflowDefinition) { d.Results[0].SourceStep = "missing" },
		"unknown-placeholder":   func(d *w.WorkflowDefinition) { d.Steps[0].Prompt.Template = "{{unknown}}" },
		"expression-template":   func(d *w.WorkflowDefinition) { d.Steps[0].Prompt.Template = "{{ subject + secret }}" },
		"non-text-placeholder":  func(d *w.WorkflowDefinition) { d.Steps[0].Inputs[0].ValueType = "integer" },
		"large-definition":      func(d *w.WorkflowDefinition) { d.Title = strings.Repeat("x", MaxDefinitionBytes) },
		"large-step-count": func(d *w.WorkflowDefinition) {
			for len(d.Steps) < 17 {
				d.Steps = append(d.Steps, d.Steps[0])
			}
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			d := sampleDefinition()
			change(&d)
			if v := ValidateDefinition(d); v.Valid || len(v.Issues) == 0 {
				t.Fatal("invalid definition accepted", v)
			}
		})
	}
}
func TestValuesAndPrompt(t *testing.T) {
	step := sampleDefinition().Steps[0]
	prompt, e := RenderPrompt(step, textValues("subject", "{{secret}}"))
	if e != nil || prompt != "Prepare a brief about {{secret}}." {
		t.Fatal(prompt, e)
	}
	for _, values := range []w.WorkflowValues{textValues("wrong", "text"), {Values: []w.WorkflowDatum{{Name: "subject", Value: int64(4)}}}, textValues("subject", strings.Repeat("x", MaxValuesBytes)), {Values: []w.WorkflowDatum{{Name: "subject", Value: 1.5}}}} {
		if ValidateValues(step.Inputs, values) == nil {
			t.Fatal("invalid values accepted")
		}
	}
	types := []w.WorkflowValueType{"text", "integer", "boolean", "decimal", "timestamp", "bytes"}
	values := []any{"text", int64(9007199254740991), true, w.CsilDecimal{Mantissa: big.NewInt(900719925474099312), Exponent: -2}, w.WorkflowTimestampValue{At: time.Date(2026, 10, 14, 0, 0, 0, 123000000, time.UTC)}, []byte{1, 2}}
	for i, value := range values {
		t.Run(string(types[i]), func(t *testing.T) {
			v := w.WorkflowValues{Values: []w.WorkflowDatum{{Name: "value", Value: value}}}
			if e := ValidateValues([]w.WorkflowPort{{Name: "value", ValueType: types[i]}}, v); e != nil {
				t.Fatal(e)
			}
			raw := w.EncodeWorkflowValues(v)
			copy, e := w.DecodeWorkflowValues(raw)
			if e != nil || !bytes.Equal(raw, w.EncodeWorkflowValues(copy)) {
				t.Fatal("value changed", e)
			}
		})
	}
}

type workflowTestKeys struct {
	public ed25519.PublicKey
	actor  w.ApplicationRef
}

func (k workflowTestKeys) Resolve(_ context.Context, a w.ApplicationRef, id string) (identity.VerifiedKey, error) {
	if a != k.actor || id != "test-key" {
		return identity.VerifiedKey{}, identity.ErrDenied
	}
	return identity.VerifiedKey{Public: k.public, CreatedAt: time.Now().Add(-time.Hour), ExpiresAt: time.Now().Add(time.Hour)}, nil
}
func TestWorkflowAuthentication(t *testing.T) {
	caller := w.ApplicationRef{Identity: w.IdentityRef{Domain: "partner.local", AccountId: "external-client"}, ApplicationId: "standalone", InstanceId: "one"}
	receiver := w.ApplicationRef{Identity: definitionOwner.Authority, ApplicationId: "workflows", InstanceId: "one"}
	public, private, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	service := &Service{Store: &Store{Application: receiver}, Keys: workflowTestKeys{public, caller}}
	packet, e := Sign("publish", w.EncodeWorkflowDefinition(sampleDefinition()), caller, receiver, "test-key", private, time.Now().UTC().Truncate(time.Millisecond))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = service.authenticate(context.Background(), "publish", packet); e != nil {
		t.Fatal(e)
	}
	for _, mode := range []string{"operation", "recipient", "schema", "old", "future", "key", "signature"} {
		t.Run(mode, func(t *testing.T) {
			v, e := w.DecodeSignatureInput(packet.Input)
			if e != nil {
				t.Fatal(e)
			}
			switch mode {
			case "operation":
				v.Context = "catalyst.events/publish"
			case "recipient":
				v.Recipient = caller
			case "schema":
				v.Schema.Digest = make([]byte, 32)
			case "old":
				v.SignedAt = v.SignedAt.Add(-2 * time.Minute)
			case "future":
				v.SignedAt = v.SignedAt.Add(time.Minute)
			case "key":
				v.KeyId = "other"
			}
			q := w.SignedRecord{Input: w.EncodeSignatureInput(v)}
			q.Signature = ed25519.Sign(private, q.Input)
			if mode == "signature" {
				q.Signature[0] ^= 1
			}
			if _, e = service.authenticate(context.Background(), "publish", q); e == nil {
				t.Fatal("invalid proof accepted")
			}
		})
	}
	service.Keys = nil
	if _, e = service.authenticate(context.Background(), "publish", packet); !errors.Is(e, ErrDenied) {
		t.Fatal(e)
	}
}
