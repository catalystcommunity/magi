package workshop

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/catalystcommunity/magi/execution"
	w "github.com/catalystcommunity/magi/generated/go"
	"github.com/catalystcommunity/magi/harness"
	"github.com/catalystcommunity/magi/question"
	"github.com/catalystcommunity/magi/workflow"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

type Adapter struct {
	Workflow *workflow.Store
	Store    *Store
	Config   question.WorkerConfig
}

func (a *Adapter) model(ctx context.Context, payload json.RawMessage) (json.RawMessage, json.RawMessage, error) {
	var input struct {
		Messages  []map[string]string `json:"messages"`
		Allowed   []string            `json:"allowed_tools"`
		CanFinish bool                `json:"can_finish"`
	}
	if json.Unmarshal(payload, &input) != nil || len(input.Messages) < 2 || len(input.Messages) > 14 || len(input.Allowed) == 0 || len(input.Allowed) > 3 {
		return nil, nil, workflow.ErrInvalid
	}
	// This installed adapter requires schema-constrained JSON support. The
	// pinned local server ignores an empty JSON-object schema in this path.
	object := func(properties map[string]any, required ...string) map[string]any {
		return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
	}
	tool := func(name string, arguments any) map[string]any {
		// Emit the action discriminator before its arguments. The local grammar
		// follows property order; arguments-first makes a small model select an
		// argument shape before it can choose the intended action.
		properties := struct {
			Tool      any `json:"tool"`
			Arguments any `json:"arguments"`
		}{map[string]any{"const": name}, arguments}
		return map[string]any{"type": "object", "properties": properties, "required": []string{"tool", "arguments"}, "additionalProperties": false}
	}
	path := map[string]any{"const": "solution.py"}
	read := tool("read_file", object(map[string]any{"path": path}, "path"))
	write := tool("write_file", object(map[string]any{"path": path, "text": map[string]any{"type": "string"}}, "path", "text"))
	check := tool("check", object(map[string]any{}))
	answer := object(map[string]any{"answer": map[string]any{"type": "string"}}, "answer")
	choices := []any{}
	for _, name := range input.Allowed {
		switch name {
		case "read_file":
			choices = append(choices, read)
		case "write_file":
			choices = append(choices, write)
		case "check":
			choices = append(choices, check)
		default:
			return nil, nil, workflow.ErrInvalid
		}
	}
	if input.CanFinish {
		choices = append(choices, answer)
	}
	format := map[string]any{"type": "json_schema", "json_schema": map[string]any{"name": "opal_offline_action_v1", "strict": true, "schema": map[string]any{"oneOf": choices}}}
	body, e := json.Marshal(map[string]any{"model": a.Config.Model.Model, "messages": input.Messages, "max_tokens": 512, "temperature": 0, "stream": false, "response_format": format})
	return body, nil, e
}
func (a *Adapter) invoke(ctx context.Context, body []byte) (json.RawMessage, json.RawMessage, error) {
	transport := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 90 * time.Second}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request, e := http.NewRequestWithContext(ctx, http.MethodPost, a.Config.Model.Endpoint, bytes.NewReader(body))
	if e != nil {
		return nil, nil, e
	}
	request.Header.Set("Content-Type", "application/json")
	response, e := client.Do(request)
	if e != nil {
		return nil, json.RawMessage(`{"error":"provider_unavailable"}`), e
	}
	defer response.Body.Close()
	raw, e := io.ReadAll(io.LimitReader(response.Body, 60001))
	if e != nil || len(raw) > 60000 {
		return nil, json.RawMessage(`{"error":"provider_response_limit"}`), workflow.ErrInvalid
	}
	evidence, _ := json.Marshal(map[string]any{"status": response.StatusCode, "body": string(raw)})
	var parsed struct {
		Choices []struct {
			Finish  string `json:"finish_reason"`
			Message struct {
				Role    string          `json:"role"`
				Content string          `json:"content"`
				Tools   json.RawMessage `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if response.StatusCode != 200 || !utf8.Valid(raw) || json.Unmarshal(raw, &parsed) != nil || len(parsed.Choices) != 1 || parsed.Choices[0].Finish != "stop" || parsed.Choices[0].Message.Role != "assistant" || len(parsed.Choices[0].Message.Content) == 0 || len(parsed.Choices[0].Message.Content) > 12000 || len(parsed.Choices[0].Message.Tools) > 0 && string(parsed.Choices[0].Message.Tools) != "null" && string(parsed.Choices[0].Message.Tools) != "[]" {
		return nil, evidence, workflow.ErrInvalid
	}
	reply, _ := json.Marshal(map[string]string{"text": parsed.Choices[0].Message.Content})
	return reply, evidence, nil
}
func (a *Adapter) Call(ctx context.Context, r workflow.CallRequest) (w.WorkflowValues, w.RecordRef, error) {
	var empty w.WorkflowValues
	var zero w.RecordRef
	if a.Workflow == nil || a.Store == nil || a.Config.Model.Validate() != nil || r.Actor != a.Config.Actor || !bytes.Equal(w.EncodeWorkflowDefinitionRef(r.Definition), w.EncodeWorkflowDefinitionRef(Reference(a.Config.Owner))) || r.StepID != "develop" || len(r.Inputs.Values) != 1 || r.Inputs.Values[0].Name != "task" {
		return empty, zero, workflow.ErrDenied
	}
	task, ok := r.Inputs.Values[0].Value.(string)
	if !ok || strings.TrimSpace(task) == "" || len(task) > 3000 || !utf8.ValidString(task) || r.Prompt != task {
		return empty, zero, workflow.ErrInvalid
	}
	hash := sha256.Sum256(append(w.EncodeScopeRef(a.Config.Owner), []byte(r.RunID)...))
	environmentID := "dev-" + hex.EncodeToString(hash[:20])
	request, _ := json.Marshal(map[string]any{"task": task, "profile": execution.Profile, "image": execution.PythonImage, "policy": execution.PolicyRevision, "harness": harness.Revision, "source": harness.InitialSource, "model": a.Config.Model.Model, "endpoint": a.Config.Model.Endpoint})
	call := w.ReadWorkflowCall{Owner: a.Config.Owner, RunId: r.RunID, StepId: r.StepID}
	if e := a.Workflow.BeginCallEvidence(ctx, w.WorkflowCallEvidence{Call: call, Capability: Capability, AdapterRevision: "offline-development-v1", Destination: environmentID, MediaType: "application/json", Request: request, Outcome: "prepared"}); e != nil {
		return empty, zero, e
	}
	if e := a.Store.Admit(ctx, a.Config.Owner, r.RunID, environmentID, request); e != nil {
		return empty, zero, e
	}
	output, callErr := a.execute(ctx, r, environmentID, task)
	capture, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	code := ""
	if callErr != nil {
		code = "development_incomplete"
	}
	response, _ := json.Marshal(output)
	if e := a.Workflow.FinishCallEvidence(capture, call, response, code); e != nil {
		return empty, zero, e
	}
	if callErr != nil {
		a.Store.Unknown(capture, a.Config.Owner, r.RunID)
		return empty, zero, workflow.ErrReconcile
	}
	return output, w.RecordRef{Authority: a.Workflow.Application.Identity, Kind: "development-evidence", RecordId: r.RunID}, nil
}
func (a *Adapter) execute(ctx context.Context, r workflow.CallRequest, id, task string) (w.WorkflowValues, error) {
	var empty w.WorkflowValues
	creation, _ := json.Marshal(map[string]any{"environment": id, "profile": execution.Profile, "image": execution.PythonImage})
	if e := a.Store.Begin(ctx, a.Config.Owner, r.RunID, "environment-create", "environment", creation); e != nil {
		return empty, e
	}
	c, e := execution.Create(ctx, id)
	if e != nil {
		return empty, e
	}
	if e = a.Store.Bind(ctx, a.Config.Owner, r.RunID, c); e != nil {
		return empty, e
	}
	created, _ := json.Marshal(c)
	if e = a.Store.Finish(ctx, a.Config.Owner, r.RunID, "environment-create", created, "returned"); e != nil {
		return empty, e
	}
	tool := func(operation string, p execution.Proposal) (json.RawMessage, error) {
		decision := "allowed"
		if execution.Evaluate(p) != nil {
			decision = "denied"
		}
		request, _ := json.Marshal(map[string]any{"proposal": p, "decision": decision, "policy": execution.PolicyRevision, "environment": id, "image": execution.PythonImage})
		if e := a.Store.Begin(ctx, a.Config.Owner, r.RunID, operation, "tool", request); e != nil {
			return nil, e
		}
		result, err := c.Call(ctx, id, p)
		raw, _ := json.Marshal(result)
		capture, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if e := a.Store.Finish(capture, a.Config.Owner, r.RunID, operation, raw, result.Outcome); e != nil {
			return nil, e
		}
		if err != nil && result.Outcome != "unknown" {
			return nil, err
		}
		return raw, nil
	}
	seed, e := tool("seed", execution.Proposal{Tool: "write_file", Arguments: map[string]string{"path": "solution.py", "text": harness.InitialSource}})
	if e != nil {
		return empty, e
	}
	var seeded execution.Result
	if json.Unmarshal(seed, &seeded) != nil || seeded.Outcome != "returned" || seeded.ExitCode != 0 {
		return empty, workflow.ErrReconcile
	}
	final, e := harness.Run(ctx, a.Config.Model.Python, task, 6, func(call context.Context, f harness.Frame) (json.RawMessage, error) {
		if f.Kind == "tool" {
			var proposal execution.Proposal
			decoder := json.NewDecoder(bytes.NewReader(f.Payload))
			decoder.DisallowUnknownFields()
			if decoder.Decode(&proposal) != nil {
				return nil, workflow.ErrInvalid
			}
			return tool(f.Operation, proposal)
		}
		body, _, e := a.model(call, f.Payload)
		if e != nil {
			return nil, e
		}
		if e = a.Store.Begin(call, a.Config.Owner, r.RunID, f.Operation, "model", body); e != nil {
			return nil, e
		}
		reply, evidence, invokeErr := a.invoke(call, body)
		capture, cancel := context.WithTimeout(context.WithoutCancel(call), 5*time.Second)
		defer cancel()
		outcome := "returned"
		if invokeErr != nil {
			outcome = "unknown"
		}
		if evidence == nil {
			evidence = json.RawMessage(`{"error":"provider_response_missing"}`)
		}
		if e = a.Store.Finish(capture, a.Config.Owner, r.RunID, f.Operation, evidence, outcome); e != nil {
			return nil, e
		}
		return reply, invokeErr
	})
	if e != nil {
		return empty, e
	}
	if e = a.Store.Begin(ctx, a.Config.Owner, r.RunID, "harness-finish", "harness", final); e != nil {
		return empty, e
	}
	if e = a.Store.Finish(ctx, a.Config.Owner, r.RunID, "harness-finish", []byte(`{"recorded":true}`), "returned"); e != nil {
		return empty, e
	}
	var report struct {
		Answer    string `json:"answer"`
		Reconcile bool   `json:"reconcile_required"`
	}
	if json.Unmarshal(final, &report) != nil || report.Reconcile || report.Answer == "" {
		return empty, workflow.ErrReconcile
	}
	// Independent final observation. The model's success statement is not a check.
	checkRaw, e := tool("final-check", execution.Proposal{Tool: "check", Arguments: map[string]string{}})
	if e != nil {
		return empty, e
	}
	var check execution.Result
	if json.Unmarshal(checkRaw, &check) != nil || check.Outcome != "returned" {
		return empty, workflow.ErrReconcile
	}
	candidateRaw, e := tool("export", execution.Proposal{Tool: "read_file", Arguments: map[string]string{"path": "solution.py"}})
	if e != nil {
		return empty, e
	}
	var candidate execution.Result
	if json.Unmarshal(candidateRaw, &candidate) != nil || candidate.Outcome != "returned" || candidate.ExitCode != 0 {
		return empty, workflow.ErrReconcile
	}
	digest := sha256.Sum256([]byte(candidate.Text))
	digestText := hex.EncodeToString(digest[:])
	state := "candidate_ready"
	if check.CheckedDigest != digestText {
		state = "candidate_changed"
		report.Answer = "The exported candidate differs from the checked bytes. Inspect the live changes before another check. " + report.Answer
	} else if check.ExitCode != 0 {
		state = "checks_failed"
		report.Answer = "The candidate did not pass the final checks. " + report.Answer
	}
	if len(report.Answer) > 3000 {
		report.Answer = "Inspect the retained candidate and checks before review."
	}
	if e = a.Store.Complete(ctx, a.Config.Owner, r.RunID, candidate.Text, digestText, state); e != nil {
		return empty, e
	}
	return w.WorkflowValues{Values: []w.WorkflowDatum{{Name: "answer", Value: report.Answer}, {Name: "candidate_digest", Value: digestText}}}, nil
}
func NewWorker(store *workflow.Store, c question.WorkerConfig) (*workflow.Worker, *Store, error) {
	if store == nil || store.Journal == nil || store.Journal.Pool == nil || store.Journal.Transport == nil || c.Model.Validate() != nil || c.CallSeconds < 8 || c.CallSeconds > 120 || !workflow.ValidateDefinition(Definition(c.Owner)).Valid {
		return nil, nil, workflow.ErrInvalid
	}
	evidence := &Store{Pool: store.Journal.Pool}
	adapter := &Adapter{Workflow: store, Store: evidence, Config: c}
	engine := &workflow.Engine{Store: store, CallTimeout: time.Duration(c.CallSeconds) * time.Second, Adapters: map[string]workflow.CallAdapter{Capability: adapter}}
	return &workflow.Worker{Engine: engine, Definition: Reference(c.Owner), Actor: &c.Actor}, evidence, nil
}
