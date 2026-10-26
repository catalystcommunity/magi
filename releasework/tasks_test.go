package releasework

import (
	w "github.com/catalystcommunity/magi/generated/go"
	"testing"
)

func TestTaskKeyExcludesMutableApprovalBytes(t *testing.T) {
	b := Binding{CandidateID: "00000000-0000-4000-8000-000000000001", Target: "canary", Controller: w.ApplicationRef{ApplicationId: "controller"}}
	first := key(b)
	b.CandidateDigest = "changed"
	b.ApprovalDigest = "changed"
	b.ApprovalID = "changed"
	if key(b) != first {
		t.Fatal("changed proposal bypasses the submission conflict")
	}
	b.CandidateID = "00000000-0000-4000-8000-000000000002"
	if key(b) == first {
		t.Fatal("candidate IDs collide")
	}
}

func TestNoRuntimeIsNotAnAuthorizedRequest(t *testing.T) {
	tasks := &Tasks{}
	if _, e := tasks.Start(t.Context(), Binding{}); e == nil {
		t.Fatal("missing runtime accepted")
	}
	if _, e := tasks.Finish(t.Context(), Binding{}, Outcome{}); e == nil {
		t.Fatal("missing outcome accepted")
	}
}

func TestTaskKeyMatchesPublicJavaScriptFixture(t *testing.T) {
	b := Binding{CandidateID: "00000000-0000-4000-8000-000000000002", Target: "opal-stateless-canary-v1", Controller: w.ApplicationRef{Identity: w.IdentityRef{Domain: "catalystsquad.local", AccountId: "00000000-0000-4000-8000-000000000001"}, ApplicationId: "opal-release-controller", InstanceId: "example-1"}}
	if key(b) != "release/a323cd04bac1aafdd62a76730df22d760bac93a115caec96903e2e31b04a9c5e" {
		t.Fatal("JavaScript and Go task keys differ")
	}
}
