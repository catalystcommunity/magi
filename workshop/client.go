package workshop

import (
	"context"
	"crypto/ed25519"
	w "github.com/catalystcommunity/magi/generated/go"
	"github.com/catalystcommunity/magi/question"
	"github.com/catalystcommunity/magi/workflow"
	"strings"
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
func (c *Client) Submit(ctx context.Context, id, task string) (w.WorkflowRun, error) {
	if strings.TrimSpace(task) == "" || len(task) > 3000 {
		return w.WorkflowRun{}, workflow.ErrInvalid
	}
	packet, e := c.sign("publish", w.EncodeWorkflowDefinition(Definition(c.Profile.Owner)))
	if e != nil {
		return w.WorkflowRun{}, e
	}
	ref, e := c.rpc.Publish(ctx, packet)
	if e != nil {
		return w.WorkflowRun{}, e
	}
	packet, e = c.sign("start", w.EncodeStartWorkflow(w.StartWorkflow{RunId: id, Definition: ref, Inputs: w.WorkflowValues{Values: []w.WorkflowDatum{{Name: "task", Value: task}}}, Restrictions: w.Restrictions{Requirements: []w.SourceRestriction{}}, Causes: []w.RecordRef{}}))
	if e != nil {
		return w.WorkflowRun{}, e
	}
	return c.rpc.Start(ctx, packet)
}
func (c *Client) Review(ctx context.Context, run w.WorkflowRun, text string, evidence w.RecordRef) (w.WorkflowHumanReceipt, error) {
	if strings.TrimSpace(text) == "" || len(text) > 1000 {
		return w.WorkflowHumanReceipt{}, workflow.ErrInvalid
	}
	for _, step := range run.Steps {
		if step.StepId == "review" && step.TaskId != nil && step.TaskRevision != nil {
			packet, e := c.sign("complete-human", w.EncodeCompleteWorkflowHuman(w.CompleteWorkflowHuman{Owner: c.Profile.Owner, RunId: run.Request.RunId, StepId: "review", TaskId: *step.TaskId, TaskRevision: *step.TaskRevision, Result: w.WorkflowValues{Values: []w.WorkflowDatum{{Name: "review", Value: text}}}, Evidence: evidence}))
			if e != nil {
				return w.WorkflowHumanReceipt{}, e
			}
			return c.rpc.CompleteHuman(ctx, packet)
		}
	}
	return w.WorkflowHumanReceipt{}, workflow.ErrConflict
}
