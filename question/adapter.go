package question

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	w "github.com/catalystcommunity/magi/generated/go"
	"github.com/catalystcommunity/magi/workflow"
)

//go:embed model_worker.py
var workerSource string

const AdapterRevision = "python-chat-completions-v1"

// ModelConfig is operator configuration. A question cannot select a URL,
// executable, model, credential, or system instruction.
type ModelConfig struct {
	Endpoint          string `json:"endpoint"`
	Model             string `json:"model"`
	Python            string `json:"python"`
	MaxTokens         int    `json:"max_tokens"`
	SystemInstruction string `json:"system_instruction,omitempty"`
}

func (c ModelConfig) Validate() error {
	u, e := url.Parse(c.Endpoint)
	if e != nil || len(c.Endpoint) > 2048 || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(c.Endpoint, " \t\r\n") ||
		(u.Scheme != "https" && (u.Scheme != "http" || (u.Hostname() != "127.0.0.1" && u.Hostname() != "::1"))) ||
		!filepath.IsAbs(c.Python) || len(c.Model) == 0 || len(c.Model) > 128 || c.MaxTokens < 1 || c.MaxTokens > 512 || len(c.SystemInstruction) > 3000 || !utf8.ValidString(c.SystemInstruction) {
		return workflow.ErrInvalid
	}
	return nil
}

type Adapter struct {
	Store             *workflow.Store
	Config            ModelConfig
	Actor             w.ApplicationRef
	Owner             w.ScopeRef
	AllowedDefinition *w.WorkflowDefinitionRef
}

type workerInput struct {
	Version  int     `json:"version"`
	Endpoint string  `json:"endpoint"`
	Request  []byte  `json:"request"`
	Timeout  float64 `json:"timeout_seconds"`
}
type workerOutput struct {
	Version  int    `json:"version"`
	Status   int    `json:"status_code"`
	Response []byte `json:"response"`
	Error    string `json:"error_code"`
}
type limitedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.Len() {
		return 0, errors.New("worker output limit")
	}
	return b.Buffer.Write(p)
}

func modelRequest(c ModelConfig, question string) ([]byte, error) {
	system := c.SystemInstruction
	if system == "" {
		system = "Answer the question briefly. You have no tools, personal records, or conversation history. State uncertainty. Do not claim to have checked live sources. Keep the answer under 3000 UTF-8 bytes."
	}
	return json.Marshal(struct {
		Model       string              `json:"model"`
		Messages    []map[string]string `json:"messages"`
		MaxTokens   int                 `json:"max_tokens"`
		Temperature int                 `json:"temperature"`
		Stream      bool                `json:"stream"`
	}{Model: c.Model, MaxTokens: c.MaxTokens, Messages: []map[string]string{
		{"role": "system", "content": system},
		{"role": "user", "content": question},
	}})
}

func parseAnswer(raw []byte) (string, error) {
	if len(raw) == 0 || len(raw) > 65536 || !utf8.Valid(raw) {
		return "", workflow.ErrInvalid
	}
	var v struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Role         string          `json:"role"`
				Content      *string         `json:"content"`
				ToolCalls    json.RawMessage `json:"tool_calls"`
				FunctionCall json.RawMessage `json:"function_call"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal(raw, &v) != nil || len(v.Choices) != 1 {
		return "", workflow.ErrInvalid
	}
	c := v.Choices[0]
	if c.FinishReason != "stop" || c.Message.Role != "assistant" || c.Message.Content == nil || strings.TrimSpace(*c.Message.Content) == "" || len(*c.Message.Content) > 3500 ||
		(len(c.Message.ToolCalls) > 0 && string(c.Message.ToolCalls) != "[]" && string(c.Message.ToolCalls) != "null") || (len(c.Message.FunctionCall) > 0 && string(c.Message.FunctionCall) != "null") {
		return "", workflow.ErrInvalid
	}
	return *c.Message.Content, nil
}

func (a *Adapter) Call(ctx context.Context, r workflow.CallRequest) (w.WorkflowValues, w.RecordRef, error) {
	var empty w.WorkflowValues
	var ref w.RecordRef
	expected := Reference(a.Owner)
	if a.AllowedDefinition != nil {
		expected = *a.AllowedDefinition
	}
	if a.Store == nil || a.Config.Validate() != nil || r.Actor != a.Actor || !bytes.Equal(w.EncodeWorkflowDefinitionRef(r.Definition), w.EncodeWorkflowDefinitionRef(expected)) ||
		r.StepID != "answer" || len(r.Inputs.Values) != 1 || r.Inputs.Values[0].Name != "question" {
		return empty, ref, workflow.ErrDenied
	}
	question, ok := r.Inputs.Values[0].Value.(string)
	if !ok || strings.TrimSpace(question) == "" || len(question) > 3000 || !utf8.ValidString(question) || r.Prompt != question {
		return empty, ref, workflow.ErrInvalid
	}
	deadline, ok := ctx.Deadline()
	remaining := time.Until(deadline) - 3*time.Second
	if !ok || remaining <= 0 || remaining > 120*time.Second {
		return empty, ref, workflow.ErrInvalid
	}
	body, e := modelRequest(a.Config, question)
	if e != nil {
		return empty, ref, e
	}
	call := w.ReadWorkflowCall{Owner: a.Owner, RunId: r.RunID, StepId: r.StepID}
	e = a.Store.BeginCallEvidence(ctx, w.WorkflowCallEvidence{Call: call, Capability: Capability, AdapterRevision: AdapterRevision, Destination: a.Config.Endpoint, MediaType: "application/json", Request: body, Outcome: "prepared"})
	if e != nil {
		return empty, ref, e
	}
	input, _ := json.Marshal(workerInput{Version: 1, Endpoint: a.Config.Endpoint, Request: body, Timeout: remaining.Seconds()})
	command := exec.CommandContext(ctx, a.Config.Python, "-I", "-B", "-c", workerSource)
	// An empty environment excludes workstation secrets, proxies, and Python
	// startup injection. This fixed program is not an OS security sandbox.
	command.Env = []string{"LANG=C.UTF-8"}
	command.Stdin = bytes.NewReader(input)
	output := &limitedBuffer{limit: 96 * 1024}
	command.Stdout = output
	command.Stderr = io.Discard
	command.WaitDelay = time.Second
	code := ""
	var result workerOutput
	if command.Run() != nil {
		code = "worker_process_failed"
	} else {
		decoder := json.NewDecoder(bytes.NewReader(output.Bytes()))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&result) != nil || decoder.Decode(new(any)) != io.EOF || result.Version != 1 || len(result.Response) > 65536 {
			code = "worker_output_invalid"
		} else {
			code = result.Error
			if code == "" && result.Status != 200 {
				code = "provider_http_error"
			}
		}
	}
	answer := ""
	if code == "" {
		answer, e = parseAnswer(result.Response)
		if e != nil {
			code = "model_response_invalid"
		}
	}
	// Preserve an available response even after the execution deadline. This
	// short capture grace grants no permission to repeat the model request.
	captureCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if e = a.Store.FinishCallEvidence(captureCtx, call, result.Response, code); e != nil {
		return empty, ref, e
	}
	if code != "" {
		return empty, ref, workflow.ErrReconcile
	}
	return w.WorkflowValues{Values: []w.WorkflowDatum{{Name: "answer", Value: answer}}}, w.RecordRef{Authority: a.Store.Application.Identity, Kind: "workflow-call", RecordId: r.Key}, nil
}
