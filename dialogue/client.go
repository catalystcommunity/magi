package dialogue

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	w "github.com/catalystcommunity/magi/generated/go"
	"github.com/catalystcommunity/magi/question"
	"github.com/catalystcommunity/magi/workflow"
	"time"
)

type Client struct {
	*question.Client
	rpc *w.WorkflowsClient
}

func NewClient(p question.Profile) (*Client, error) {
	base, e := question.NewClient(p)
	if e != nil {
		return nil, e
	}
	transport, e := workflow.NewHTTPTransport(p.Endpoint)
	if e != nil {
		return nil, e
	}
	return &Client{Client: base, rpc: w.NewWorkflowsClient(transport)}, nil
}
func (c *Client) sign(op string, body []byte) (w.SignedRecord, error) {
	p := c.Profile
	return workflow.Sign(op, body, p.Actor, p.Recipient, p.KeyID, ed25519.PrivateKey(p.PrivateKey), time.Now().UTC().Truncate(time.Millisecond))
}
func (c *Client) Submit(ctx context.Context, id string, v Request) (w.WorkflowRun, error) {
	raw, e := json.Marshal(v)
	if e != nil {
		return w.WorkflowRun{}, e
	}
	if _, e = Parse(string(raw)); e != nil {
		return w.WorkflowRun{}, e
	}
	packet, e := c.sign("publish", w.EncodeWorkflowDefinition(Definition(c.Profile.Owner)))
	if e != nil {
		return w.WorkflowRun{}, e
	}
	ref, e := c.rpc.Publish(ctx, packet)
	if e != nil {
		return w.WorkflowRun{}, e
	}
	packet, e = c.sign("start", w.EncodeStartWorkflow(w.StartWorkflow{RunId: id, Definition: ref, Inputs: w.WorkflowValues{Values: []w.WorkflowDatum{{Name: "request", Value: string(raw)}}}, Restrictions: w.Restrictions{Requirements: []w.SourceRestriction{}}, Causes: []w.RecordRef{}}))
	if e != nil {
		return w.WorkflowRun{}, e
	}
	return c.rpc.Start(ctx, packet)
}
func (c *Client) CallEvidence(ctx context.Context, id, step string) (w.WorkflowCallEvidence, error) {
	packet, e := c.sign("read-call", w.EncodeReadWorkflowCall(w.ReadWorkflowCall{Owner: c.Profile.Owner, RunId: id, StepId: step}))
	if e != nil {
		return w.WorkflowCallEvidence{}, e
	}
	return c.rpc.ReadCall(ctx, packet)
}
