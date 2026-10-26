package workflow

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"slices"
	"strings"

	w "github.com/catalystcommunity/magi/generated/go"
	p "github.com/catalystcommunity/magi/protocol"
)

const MaxDefinitionBytes = 32768
const MaxValuesBytes = 4096

var portName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)
var promptToken = regexp.MustCompile(`\{\{([A-Za-z][A-Za-z0-9_-]{0,63})\}\}`)

func validScope(v w.ScopeRef) bool {
	return validIdentity(v.Authority) && identifier.MatchString(v.ScopeId)
}

// ValidateDefinition checks the initial acyclic, all-dependencies-required subset.
// Capability names select administrator-installed adapters; they are not URLs.
func ValidateDefinition(d w.WorkflowDefinition) w.WorkflowValidation {
	v := w.WorkflowValidation{Valid: true, Issues: []w.WorkflowIssue{}}
	add := func(path, code string) {
		v.Valid = false
		if len(v.Issues) < 128 {
			v.Issues = append(v.Issues, w.WorkflowIssue{Path: path, Code: code})
		}
	}
	if len(w.EncodeWorkflowDefinition(d)) > MaxDefinitionBytes {
		add("definition", "too_large")
		return v
	}
	if !validScope(d.Owner) || !portName.MatchString(d.Id) || !portName.MatchString(d.Revision) || len(d.Title) == 0 || len(d.Title) > 256 {
		add("definition", "invalid_identity_or_title")
	}
	ports := func(path string, pp []w.WorkflowPort) map[string]w.WorkflowValueType {
		m := map[string]w.WorkflowValueType{}
		if len(pp) > 16 {
			add(path, "too_many_ports")
		}
		for _, q := range pp {
			if !portName.MatchString(q.Name) || m[q.Name] != "" || !slices.Contains([]w.WorkflowValueType{"text", "integer", "boolean", "decimal", "timestamp", "bytes"}, q.ValueType) {
				add(path+"/"+q.Name, "invalid_or_duplicate_port")
			}
			m[q.Name] = q.ValueType
		}
		return m
	}
	in := ports("inputs", d.Inputs)
	out := ports("outputs", d.Outputs)
	if len(d.Steps) == 0 || len(d.Steps) > 16 {
		add("steps", "step_count")
		return v
	}
	steps := map[string]w.WorkflowStep{}
	inputs := map[string]map[string]w.WorkflowValueType{}
	outputs := map[string]map[string]w.WorkflowValueType{"$input": in}
	finish := ""
	for _, s := range d.Steps {
		path := "steps/" + s.Id
		if _, ok := steps[s.Id]; ok || !portName.MatchString(s.Id) {
			add(path, "invalid_or_duplicate_step")
		}
		steps[s.Id] = s
		inputs[s.Id] = ports(path+"/inputs", s.Inputs)
		outputs[s.Id] = ports(path+"/outputs", s.Outputs)
		switch s.Kind {
		case "call":
			if s.Capability == nil || !identifier.MatchString(*s.Capability) {
				add(path, "capability_required")
			}
		case "human":
			if s.Capability != nil {
				add(path, "human_has_no_automatic_capability")
			}
		case "finish":
			if finish != "" {
				add(path, "one_finish_required")
			}
			finish = s.Id
			if s.Capability != nil || s.Prompt != nil || len(s.Inputs) != len(s.Outputs) {
				add(path, "finish_must_pass_inputs")
			}
			for name, typ := range inputs[s.Id] {
				if outputs[s.Id][name] != typ {
					add(path, "finish_must_pass_inputs")
				}
			}
		default:
			add(path, "unsupported_step_kind")
		}
		if s.Prompt != nil {
			if !portName.MatchString(s.Prompt.Revision) || len(s.Prompt.Template) > 4096 {
				add(path+"/prompt", "invalid_prompt")
			}
			rest := promptToken.ReplaceAllString(s.Prompt.Template, "")
			if strings.Contains(rest, "{{") || strings.Contains(rest, "}}") {
				add(path+"/prompt", "unsupported_template_syntax")
			}
			for _, m := range promptToken.FindAllStringSubmatch(s.Prompt.Template, -1) {
				if inputs[s.Id][m[1]] != "text" {
					add(path+"/prompt", "text_input_required")
				}
			}
		}
	}
	if finish == "" {
		add("steps", "one_finish_required")
	}
	ancestors := map[string]map[string]bool{}
	visiting := map[string]bool{}
	var visit func(string) map[string]bool
	visit = func(id string) map[string]bool {
		if a, ok := ancestors[id]; ok {
			return a
		}
		a := map[string]bool{}
		if visiting[id] {
			add("steps/"+id, "cycle")
			return a
		}
		visiting[id] = true
		seen := map[string]bool{}
		for _, dep := range steps[id].DependsOn {
			if _, ok := steps[dep]; !ok || dep == id || seen[dep] {
				add("steps/"+id, "invalid_dependency")
				continue
			}
			seen[dep] = true
			a[dep] = true
			for x := range visit(dep) {
				a[x] = true
			}
		}
		visiting[id] = false
		ancestors[id] = a
		return a
	}
	for _, s := range d.Steps {
		visit(s.Id)
	}
	bindings := func(path string, pp map[string]w.WorkflowValueType, bb []w.WorkflowBinding, allowed map[string]bool) {
		seen := map[string]bool{}
		if len(bb) != len(pp) {
			add(path, "bind_every_input")
		}
		for _, b := range bb {
			typ, ok := pp[b.Input]
			if !ok || seen[b.Input] {
				add(path, "invalid_or_duplicate_binding")
			}
			seen[b.Input] = true
			if b.SourceStep != "$input" && !allowed[b.SourceStep] {
				add(path, "source_not_dependency")
			}
			if typ == "" || outputs[b.SourceStep][b.Output] != typ {
				add(path, "binding_type_mismatch")
			}
		}
	}
	for _, s := range d.Steps {
		bindings("steps/"+s.Id+"/bindings", inputs[s.Id], s.Bindings, ancestors[s.Id])
		if s.Id != finish && !ancestors[finish][s.Id] {
			add("steps/"+s.Id, "not_joined_by_finish")
		}
	}
	allowed := map[string]bool{}
	for id := range steps {
		allowed[id] = true
	}
	bindings("results", out, d.Results, allowed)
	return v
}

func definitionRef(d w.WorkflowDefinition) w.WorkflowDefinitionRef {
	digest := sha256.Sum256(w.EncodeWorkflowDefinition(d))
	return w.WorkflowDefinitionRef{Owner: d.Owner, Id: d.Id, Revision: d.Revision, Digest: digest[:]}
}
func scalarType(v w.WorkflowScalar) w.WorkflowValueType {
	switch v.(type) {
	case string:
		return "text"
	case int64:
		return "integer"
	case bool:
		return "boolean"
	case w.CsilDecimal:
		return "decimal"
	case w.WorkflowTimestampValue:
		return "timestamp"
	case []byte:
		return "bytes"
	}
	return ""
}
func ValidateValues(ports []w.WorkflowPort, v w.WorkflowValues) error {
	raw := w.EncodeWorkflowValues(v)
	if len(raw) > MaxValuesBytes || len(ports) != len(v.Values) {
		return ErrInvalid
	}
	if _, e := p.Decode(raw, w.DecodeWorkflowValues, w.EncodeWorkflowValues); e != nil {
		return ErrInvalid
	}
	types := map[string]w.WorkflowValueType{}
	for _, q := range ports {
		types[q.Name] = q.ValueType
	}
	seen := map[string]bool{}
	for _, q := range v.Values {
		if seen[q.Name] || types[q.Name] == "" || types[q.Name] != scalarType(q.Value) {
			return ErrInvalid
		}
		seen[q.Name] = true
	}
	return nil
}
func bindValues(bb []w.WorkflowBinding, sources map[string]w.WorkflowValues) (w.WorkflowValues, error) {
	v := w.WorkflowValues{Values: []w.WorkflowDatum{}}
	for _, b := range bb {
		found := false
		for _, x := range sources[b.SourceStep].Values {
			if x.Name == b.Output {
				v.Values = append(v.Values, w.WorkflowDatum{Name: b.Input, Value: x.Value})
				found = true
				break
			}
		}
		if !found {
			return v, ErrConflict
		}
	}
	return v, nil
}

// RenderPrompt substitutes declared text inputs once. It cannot execute code,
// read a secret, fetch a URL, or expand placeholders inside supplied values.
func RenderPrompt(s w.WorkflowStep, values w.WorkflowValues) (string, error) {
	if e := ValidateValues(s.Inputs, values); e != nil {
		return "", e
	}
	if s.Prompt == nil {
		return "", nil
	}
	text := map[string]string{}
	for _, v := range values.Values {
		if s, ok := v.Value.(string); ok {
			text[v.Name] = s
		}
	}
	var failure error
	result := promptToken.ReplaceAllStringFunc(s.Prompt.Template, func(token string) string {
		name := token[2 : len(token)-2]
		v, ok := text[name]
		if !ok {
			failure = fmt.Errorf("%w: prompt input", ErrInvalid)
		}
		return v
	})
	if len(result) > MaxValuesBytes {
		return "", ErrInvalid
	}
	return result, failure
}
