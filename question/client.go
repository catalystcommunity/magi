package question

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	w "github.com/catalystcommunity/magi/generated/go"
	"github.com/catalystcommunity/magi/protocol"
	"github.com/catalystcommunity/magi/workflow"
	"os"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type Profile struct {
	Endpoint   string           `json:"endpoint"`
	Actor      w.ApplicationRef `json:"actor"`
	Recipient  w.ApplicationRef `json:"recipient"`
	Owner      w.ScopeRef       `json:"owner"`
	KeyID      string           `json:"key_id"`
	PrivateKey []byte           `json:"private_key"`
}
type Client struct {
	Profile Profile
	rpc     *w.WorkflowsClient
}

func NewClient(profile Profile) (*Client, error) {
	if len(profile.PrivateKey) != ed25519.PrivateKeySize || len(profile.KeyID) == 0 {
		return nil, workflow.ErrInvalid
	}
	transport, e := workflow.NewHTTPTransport(profile.Endpoint)
	if e != nil {
		return nil, e
	}
	return &Client{Profile: profile, rpc: w.NewWorkflowsClient(transport)}, nil
}
func LoadProfile(path string) (Profile, error) {
	var p Profile
	st, e := os.Lstat(path)
	if e != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 || st.Size() > 16384 {
		return p, errors.New("profile must be a private regular file")
	}
	raw, e := os.ReadFile(path)
	if e != nil {
		return p, e
	}
	if e = decodeJSON(raw, &p); e != nil {
		return p, e
	}
	return p, nil
}
func (c *Client) sign(op string, body []byte) (w.SignedRecord, error) {
	return workflow.Sign(op, body, c.Profile.Actor, c.Profile.Recipient, c.Profile.KeyID, ed25519.PrivateKey(c.Profile.PrivateKey), time.Now().UTC().Truncate(time.Millisecond))
}
func (c *Client) Submit(ctx context.Context, id, question string) (w.WorkflowRun, error) {
	var empty w.WorkflowRun
	if strings.TrimSpace(question) == "" || len(question) > 3000 || !utf8.ValidString(question) {
		return empty, errors.New("question must contain 1 to 3000 UTF-8 bytes")
	}
	packet, e := c.sign("publish", w.EncodeWorkflowDefinition(Definition(c.Profile.Owner)))
	if e != nil {
		return empty, e
	}
	ref, e := c.rpc.Publish(ctx, packet)
	if e != nil {
		return empty, e
	}
	packet, e = c.sign("start", w.EncodeStartWorkflow(w.StartWorkflow{RunId: id, Definition: ref, Inputs: w.WorkflowValues{Values: []w.WorkflowDatum{{Name: "question", Value: question}}}, Restrictions: w.Restrictions{Requirements: []w.SourceRestriction{}}, Causes: []w.RecordRef{}}))
	if e != nil {
		return empty, e
	}
	return c.rpc.Start(ctx, packet)
}
func (c *Client) Read(ctx context.Context, id string) (w.WorkflowRun, error) {
	packet, e := c.sign("read-run", w.EncodeReadWorkflowRun(w.ReadWorkflowRun{Owner: c.Profile.Owner, RunId: id}))
	if e != nil {
		return w.WorkflowRun{}, e
	}
	return c.rpc.ReadRun(ctx, packet)
}
func (c *Client) Evidence(ctx context.Context, id string) (w.WorkflowCallEvidence, error) {
	packet, e := c.sign("read-call", w.EncodeReadWorkflowCall(w.ReadWorkflowCall{Owner: c.Profile.Owner, RunId: id, StepId: "answer"}))
	if e != nil {
		return w.WorkflowCallEvidence{}, e
	}
	return c.rpc.ReadCall(ctx, packet)
}
func (c *Client) Wait(ctx context.Context, id string) (string, error) {
	interval := pollInterval(c.Profile.Endpoint)
	evidenceInterval := interval
	if strings.HasPrefix(c.Profile.Endpoint, "https://") {
		evidenceInterval = 15 * time.Second
	}
	var nextEvidence time.Time
	for {
		run, e := c.Read(ctx, id)
		if e != nil {
			var remote *protocol.RemoteError
			if !errors.As(e, &remote) || !remote.Detail.Retryable || remote.Detail.Code != 1500 {
				return "", e
			}
			// Only a read is retried. Admission and model calls are not repeated.
		}
		if run.Result != nil {
			if len(run.Result.Values) != 1 || run.Result.Values[0].Name != "answer" {
				return "", workflow.ErrInvalid
			}
			answer, ok := run.Result.Values[0].Value.(string)
			if !ok {
				return "", workflow.ErrInvalid
			}
			return answer, nil
		}
		for _, step := range run.Steps {
			if step.Phase == "reconcile_required" || step.Phase == "rejected" {
				return "", workflow.ErrReconcile
			}
			if step.StepId == "answer" && step.Phase == "outcome_unknown" && !time.Now().Before(nextEvidence) {
				// A dispatch marker also exists while the worker is still active.
				// Only a retained failure permits an early failure response.
				ev, err := c.Evidence(ctx, id)
				nextEvidence = time.Now().Add(evidenceInterval)
				if err == nil && ev.Outcome == "failed" {
					return "", fmt.Errorf("model call failed (%s); inspect evidence; no automatic retry", *ev.ErrorCode)
				}
			}
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", ctx.Err()
		case <-timer.C:
		}
	}
}

func pollInterval(endpoint string) time.Duration {
	// Each current resolver call opens three LinkKeys TLS connections. Stay
	// below the default one-handshake-per-second budget during a long wait.
	if strings.HasPrefix(endpoint, "https://") {
		return 5 * time.Second
	}
	return 250 * time.Millisecond
}

// DisplayEvidence expands JSON payloads for inspection. It never executes them.
func DisplayEvidence(v w.WorkflowCallEvidence) ([]byte, error) {
	request := any(string(v.Request))
	response := any(nil)
	if json.Valid(v.Request) {
		request = json.RawMessage(v.Request)
	}
	if v.Response != nil {
		response = string(*v.Response)
		if json.Valid(*v.Response) {
			response = json.RawMessage(*v.Response)
		}
	}
	return json.MarshalIndent(struct {
		Metadata w.WorkflowCallEvidence `json:"metadata"`
		Request  any                    `json:"request"`
		Response any                    `json:"response"`
	}{Metadata: v, Request: request, Response: response}, "", "  ")
}

// TerminalText keeps model-provided control sequences out of the terminal.
// The workflow result and evidence retain the original bytes.
func TerminalText(text string) string {
	var out strings.Builder
	for _, r := range text {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			fmt.Fprintf(&out, "\\u%04x", r)
		} else {
			out.WriteRune(r)
		}
	}
	return out.String()
}
