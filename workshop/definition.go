package workshop

import (
	"crypto/sha256"
	w "github.com/catalystcommunity/magi/generated/go"
)

const Capability = "agent.offline-development.v1"

func ports(names ...string) []w.WorkflowPort {
	v := []w.WorkflowPort{}
	for _, name := range names {
		v = append(v, w.WorkflowPort{Name: name, ValueType: "text"})
	}
	return v
}
func bind(input, step, output string) w.WorkflowBinding {
	return w.WorkflowBinding{Input: input, SourceStep: step, Output: output}
}
func Definition(owner w.ScopeRef) w.WorkflowDefinition {
	capability := Capability
	return w.WorkflowDefinition{Owner: owner, Id: "offline-development", Revision: "v1", Title: "Develop an isolated candidate and request human review", Inputs: ports("task"), Outputs: ports("answer", "candidate_digest", "review"), Steps: []w.WorkflowStep{
		{Id: "develop", Kind: "call", DependsOn: []string{}, Inputs: ports("task"), Outputs: ports("answer", "candidate_digest"), Bindings: []w.WorkflowBinding{bind("task", "$input", "task")}, Capability: &capability, Prompt: &w.WorkflowPrompt{Revision: "v1", Template: "{{task}}"}},
		{Id: "review", Kind: "human", DependsOn: []string{"develop"}, Inputs: ports("candidate_digest"), Outputs: ports("review"), Bindings: []w.WorkflowBinding{bind("candidate_digest", "develop", "candidate_digest")}, Prompt: &w.WorkflowPrompt{Revision: "v1", Template: "Inspect the candidate and test evidence for digest {{candidate_digest}}. Record your review. This does not authorize a deployment."}},
		{Id: "finish", Kind: "finish", DependsOn: []string{"develop", "review"}, Inputs: ports("answer", "candidate_digest", "review"), Outputs: ports("answer", "candidate_digest", "review"), Bindings: []w.WorkflowBinding{bind("answer", "develop", "answer"), bind("candidate_digest", "develop", "candidate_digest"), bind("review", "review", "review")}},
	}, Results: []w.WorkflowBinding{bind("answer", "finish", "answer"), bind("candidate_digest", "finish", "candidate_digest"), bind("review", "finish", "review")}}
}
func Reference(owner w.ScopeRef) w.WorkflowDefinitionRef {
	v := Definition(owner)
	hash := sha256.Sum256(w.EncodeWorkflowDefinition(v))
	return w.WorkflowDefinitionRef{Owner: owner, Id: v.Id, Revision: v.Revision, Digest: hash[:]}
}
