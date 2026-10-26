// Package question implements the first single-question workflow. It has no
// retrieval, conversation memory, tool execution, or personal-data admission.
package question

import (
	"crypto/sha256"
	w "github.com/catalystcommunity/magi/generated/go"
)

const Capability = "model.answer.v1"

func Definition(owner w.ScopeRef) w.WorkflowDefinition {
	ports := func(name string) []w.WorkflowPort { return []w.WorkflowPort{{Name: name, ValueType: "text"}} }
	capability := Capability
	return w.WorkflowDefinition{Owner: owner, Id: "question-answer", Revision: "v1", Title: "Answer one test question",
		Inputs: ports("question"), Outputs: ports("answer"), Steps: []w.WorkflowStep{
			{Id: "answer", Kind: "call", DependsOn: []string{}, Inputs: ports("question"), Outputs: ports("answer"),
				Bindings: []w.WorkflowBinding{{Input: "question", SourceStep: "$input", Output: "question"}}, Capability: &capability,
				Prompt: &w.WorkflowPrompt{Revision: "v1", Template: "{{question}}"}},
			{Id: "finish", Kind: "finish", DependsOn: []string{"answer"}, Inputs: ports("answer"), Outputs: ports("answer"),
				Bindings: []w.WorkflowBinding{{Input: "answer", SourceStep: "answer", Output: "answer"}}},
		}, Results: []w.WorkflowBinding{{Input: "answer", SourceStep: "finish", Output: "answer"}}}
}

func Reference(owner w.ScopeRef) w.WorkflowDefinitionRef {
	d := Definition(owner)
	sum := sha256.Sum256(w.EncodeWorkflowDefinition(d))
	return w.WorkflowDefinitionRef{Owner: owner, Id: d.Id, Revision: d.Revision, Digest: sum[:]}
}
