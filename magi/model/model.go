// Package model calls an OpenAI-compatible Chat Completions endpoint.
// It works with llama.cpp llama-server and hosted providers.
package model

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
}

type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// Tool describes a function the model can call. Parameters is a JSON Schema.
type Tool struct {
	Name        string
	Description string
	Parameters  map[string]any
}

type Client struct {
	BaseURL string
	Model   string
	APIKey  string
	HTTP    *http.Client
	// Attempts is the number of tries. 0 means 4.
	Attempts int
}

// Chatter is one model endpoint or a chain of endpoints.
type Chatter interface {
	Chat(ctx context.Context, msgs []Message, tools []Tool, maxTokens int) (Message, error)
}

// Chain tries each endpoint in order. It moves to the next endpoint when one
// fails, for example when an inference host is busy, starting, or down.
type Chain []*Client

func (c Chain) Chat(ctx context.Context, msgs []Message, tools []Tool, maxTokens int) (Message, error) {
	var errs []string
	for _, m := range c {
		out, err := m.Chat(ctx, msgs, tools, maxTokens)
		if err == nil || ctx.Err() != nil {
			return out, err
		}
		errs = append(errs, m.Model+": "+err.Error())
	}
	return Message{}, fmt.Errorf("no model answered: %s", strings.Join(errs, "; "))
}

// Endpoint is one entry of MAGI_PERSONA_MODELS.
type Endpoint struct {
	BaseURL  string `json:"base_url"`
	Model    string `json:"model"`
	KeyFile  string `json:"key_file"`
	Attempts int    `json:"attempts"`
	// TimeoutS limits one request. 0 means 10 minutes.
	TimeoutS int `json:"timeout_s"`
}

// ChainFromEnv reads the JSON list in the variable name. When it is empty,
// the chain is only primary.
func ChainFromEnv(name string, primary *Client) (Chain, error) {
	v := os.Getenv(name)
	if v == "" {
		return Chain{primary}, nil
	}
	var eps []Endpoint
	if err := json.Unmarshal([]byte(v), &eps); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	var out Chain
	for _, e := range eps {
		if e.BaseURL == "" || e.Model == "" {
			return nil, fmt.Errorf("%s: base_url and model are required", name)
		}
		t := 10 * time.Minute
		if e.TimeoutS > 0 {
			t = time.Duration(e.TimeoutS) * time.Second
		}
		c := &Client{BaseURL: strings.TrimRight(e.BaseURL, "/"), Model: e.Model, Attempts: e.Attempts, HTTP: &http.Client{Timeout: t}}
		if e.KeyFile != "" {
			b, err := os.ReadFile(e.KeyFile)
			if err != nil {
				return nil, err
			}
			c.APIKey = strings.TrimSpace(string(b))
		}
		out = append(out, c)
	}
	return out, nil
}

// FromEnv reads MAGI_MODEL_BASE_URL, MAGI_MODEL_NAME, and
// MAGI_MODEL_API_KEY_FILE.
func FromEnv() (*Client, error) {
	c := &Client{
		BaseURL: strings.TrimRight(os.Getenv("MAGI_MODEL_BASE_URL"), "/"),
		Model:   os.Getenv("MAGI_MODEL_NAME"),
		HTTP:    &http.Client{Timeout: 10 * time.Minute},
	}
	if c.BaseURL == "" || c.Model == "" {
		return nil, fmt.Errorf("MAGI_MODEL_BASE_URL and MAGI_MODEL_NAME are required")
	}
	if f := os.Getenv("MAGI_MODEL_API_KEY_FILE"); f != "" {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		c.APIKey = strings.TrimSpace(string(b))
	}
	return c, nil
}

type request struct {
	Model       string           `json:"model"`
	Messages    []Message        `json:"messages"`
	Tools       []map[string]any `json:"tools,omitempty"`
	MaxTokens   int              `json:"max_tokens,omitempty"`
	Temperature float64          `json:"temperature"`
}

type response struct {
	Choices []struct {
		Message      Message `json:"message"`
		FinishReason string  `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Chat sends one request and returns the assistant message.
// It retries on connection errors, 429, and 5xx responses.
func (c *Client) Chat(ctx context.Context, msgs []Message, tools []Tool, maxTokens int) (Message, error) {
	req := request{Model: c.Model, Messages: msgs, MaxTokens: maxTokens, Temperature: 0.3}
	for _, t := range tools {
		req.Tools = append(req.Tools, map[string]any{"type": "function", "function": map[string]any{
			"name": t.Name, "description": t.Description, "parameters": t.Parameters,
		}})
	}
	body, err := json.Marshal(req)
	if err != nil {
		return Message{}, err
	}
	attempts := c.Attempts
	if attempts <= 0 {
		attempts = 4
	}
	var last error
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return Message{}, ctx.Err()
			case <-time.After(time.Duration(attempt*attempt) * 2 * time.Second):
			}
		}
		m, retry, err := c.once(ctx, body)
		if err == nil {
			return m, nil
		}
		last = err
		if !retry {
			break
		}
	}
	return Message{}, last
}

func (c *Client) once(ctx context.Context, body []byte) (Message, bool, error) {
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return Message{}, false, err
	}
	r.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		r.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	resp, err := c.HTTP.Do(r)
	if err != nil {
		return Message{}, ctx.Err() == nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return Message{}, true, err
	}
	if resp.StatusCode == 429 || resp.StatusCode >= 500 {
		return Message{}, true, fmt.Errorf("model: status %d: %.200s", resp.StatusCode, b)
	}
	var out response
	if err := json.Unmarshal(b, &out); err != nil {
		return Message{}, false, fmt.Errorf("model: bad response: %w", err)
	}
	if out.Error != nil {
		return Message{}, false, fmt.Errorf("model: %s", out.Error.Message)
	}
	if resp.StatusCode != 200 || len(out.Choices) == 0 {
		return Message{}, false, fmt.Errorf("model: status %d: %.200s", resp.StatusCode, b)
	}
	m := out.Choices[0].Message
	m.Role = "assistant"
	return m, false, nil
}
