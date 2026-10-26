package conversation

import (
	w "github.com/catalystcommunity/magi/generated/go"
	"testing"
	"time"
)

var actor = w.ApplicationRef{Identity: w.IdentityRef{Domain: "local.opal.test", AccountId: "person"}, ApplicationId: "interface", InstanceId: "one"}
var owner = w.ScopeRef{Authority: actor.Identity, ScopeId: "public-fixtures"}

func header(id string) w.EvidenceHeader {
	return w.EvidenceHeader{Ref: w.RecordRef{Authority: actor.Identity, Kind: "interaction", RecordId: id}, Producer: actor.Identity, Access: w.ContextAccess{Actor: actor.Identity, ControlScope: owner, Purpose: "demo", ResultAudience: []w.ScopeRef{owner}, Processor: actor.Identity}, RecordedAt: time.Now().UTC().Truncate(time.Millisecond), Restrictions: w.Restrictions{Requirements: []w.SourceRestriction{}}, Causes: []w.RecordRef{}, Capture: "complete"}
}
func literal(text string) w.CapturedContent {
	return w.CapturedContent{Capture: "complete", Segments: []w.Segment{w.LiteralSegment{Start: 0, Value: []byte(text)}}}
}
func interaction(id string) w.InteractionRecord {
	return w.InteractionRecord{Header: header(id), PointId: "web", Channel: "text", Sequence: 1, Content: literal("Two replies in one interaction."), Conversations: []string{}, ReplyHints: []w.RecordRef{}, IdentityEvidence: []w.RecordRef{}}
}
func TestGraphNodeBoundaries(t *testing.T) {
	n := interaction("source")
	v := w.ConversationAppend{Node: n, Sessions: []string{"calendar", "stars"}}
	if _, _, e := validate(actor, v); e != nil {
		t.Fatal(e)
	}
	n.Header.Restrictions.Requirements = []w.SourceRestriction{{Owner: owner}}
	v.Node = n
	if _, _, e := validate(actor, v); e == nil {
		t.Fatal("private source accepted before policy integration")
	}
	n = interaction("source")
	n.Header.Producer.AccountId = "wife"
	v.Node = n
	if _, _, e := validate(actor, v); e == nil {
		t.Fatal("forged producer")
	}
	n = interaction("source")
	n.Header.Causes = []w.RecordRef{n.Header.Ref}
	v.Node = n
	if _, _, e := validate(actor, v); e == nil {
		t.Fatal("self reference")
	}
	n = interaction("source")
	v.Node = n
	v.Sessions = []string{"one", "one"}
	if _, _, e := validate(actor, v); e == nil {
		t.Fatal("duplicate session")
	}
}
func TestInterpretationsAndInputsStaySeparate(t *testing.T) {
	source := interaction("source").Header.Ref
	interpretation := w.InterpretationRecord{Header: header("interpretation"), Sources: []w.RecordRef{source}, Context: []w.RecordRef{}, Destinations: []w.DestinationMeaning{{SessionId: "calendar", Prompts: []w.RecordRef{}, Content: literal("Move it to tomorrow.")}, {SessionId: "stars", Prompts: []w.RecordRef{}, Content: literal("What is the upper mass limit?")}}, Method: "explicit routing fixture", Uncertainty: "test only"}
	_, refs, e := validate(actor, w.ConversationAppend{Node: interpretation, Sessions: []string{"calendar", "stars"}})
	if e != nil || len(refs) != 1 {
		t.Fatal(e, refs)
	}
	input := w.SessionInput{Header: header("input"), SessionId: "calendar", Sources: []w.RecordRef{source}, Interpretations: []w.RecordRef{interpretation.Header.Ref}, Content: interpretation.Destinations[0].Content, CommandId: "command", Admission: w.RecordRef{Authority: actor.Identity, Kind: "admission", RecordId: "demo"}}
	if _, _, e = validate(actor, w.ConversationAppend{Node: input, Sessions: []string{"calendar"}}); e != nil {
		t.Fatal(e)
	}
	if _, _, e = validate(actor, w.ConversationAppend{Node: input, Sessions: []string{"calendar", "stars"}}); e == nil {
		t.Fatal("input crossed session boundary")
	}
}
