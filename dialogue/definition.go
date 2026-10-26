// Package dialogue composes conversation evidence, model work, and speech.
// Corndogs and the existing workflow service retain task ownership.
package dialogue

import (
	"crypto/sha256"
	w "github.com/catalystcommunity/magi/generated/go"
)

const Prepare = "conversation.prepare.v1"
const Present = "conversation.present.v1"

func ports(names ...string) []w.WorkflowPort {
	out := []w.WorkflowPort{}
	for _, name := range names {
		out = append(out, w.WorkflowPort{Name: name, ValueType: "text"})
	}
	return out
}
func str(v string) *string { return &v }
func binding(input, step, output string) w.WorkflowBinding {
	return w.WorkflowBinding{Input: input, SourceStep: step, Output: output}
}
func Definition(owner w.ScopeRef) w.WorkflowDefinition {
	return w.WorkflowDefinition{Owner: owner, Id: "conversation-reply", Revision: "v1", Title: "Reply with scoped conversation context and optional speech", Inputs: ports("request"), Outputs: ports("answer", "speech_id", "speech_error"), Steps: []w.WorkflowStep{
		{Id: "prepare", Kind: "call", DependsOn: []string{}, Inputs: ports("request"), Outputs: ports("question"), Bindings: []w.WorkflowBinding{binding("request", "$input", "request")}, Capability: str(Prepare), Prompt: &w.WorkflowPrompt{Revision: "v1", Template: "{{request}}"}},
		{Id: "answer", Kind: "call", DependsOn: []string{"prepare"}, Inputs: ports("question"), Outputs: ports("answer"), Bindings: []w.WorkflowBinding{binding("question", "prepare", "question")}, Capability: str("model.answer.v1"), Prompt: &w.WorkflowPrompt{Revision: "v1", Template: "{{question}}"}},
		{Id: "present", Kind: "call", DependsOn: []string{"answer"}, Inputs: ports("request", "answer"), Outputs: ports("answer", "speech_id", "speech_error"), Bindings: []w.WorkflowBinding{binding("request", "$input", "request"), binding("answer", "answer", "answer")}, Capability: str(Present), Prompt: &w.WorkflowPrompt{Revision: "v1", Template: "{{answer}}"}},
		{Id: "finish", Kind: "finish", DependsOn: []string{"present"}, Inputs: ports("answer", "speech_id", "speech_error"), Outputs: ports("answer", "speech_id", "speech_error"), Bindings: []w.WorkflowBinding{binding("answer", "present", "answer"), binding("speech_id", "present", "speech_id"), binding("speech_error", "present", "speech_error")}},
	}, Results: []w.WorkflowBinding{binding("answer", "finish", "answer"), binding("speech_id", "finish", "speech_id"), binding("speech_error", "finish", "speech_error")}}
}
func Reference(owner w.ScopeRef) w.WorkflowDefinitionRef {
	definition := Definition(owner)
	hash := sha256.Sum256(w.EncodeWorkflowDefinition(definition))
	return w.WorkflowDefinitionRef{Owner: owner, Id: definition.Id, Revision: definition.Revision, Digest: hash[:]}
}
