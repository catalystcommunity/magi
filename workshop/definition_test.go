package workshop

import (
	w "github.com/catalystcommunity/magi/generated/go"
	"github.com/catalystcommunity/magi/workflow"
	"testing"
)

func TestReviewBeforeCompletion(t *testing.T) {
	owner := w.ScopeRef{Authority: w.IdentityRef{Domain: "local.opal.test", AccountId: "tester"}, ScopeId: "public"}
	definition := Definition(owner)
	if v := workflow.ValidateDefinition(definition); !v.Valid {
		t.Fatalf("definition: %+v", v)
	}
	if definition.Steps[1].Kind != "human" || definition.Steps[1].Bindings[0].Output != "candidate_digest" {
		t.Fatal("missing exact candidate review")
	}
	for _, s := range definition.Steps {
		if s.Capability != nil && *s.Capability != Capability {
			t.Fatal("unexpected effect authority")
		}
	}
}
