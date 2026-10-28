package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	corndogs "github.com/CatalystCommunity/corndogs/clients/corndogs"
)

// Ticket identifies one task in the queue at one revision.
type Ticket struct {
	UUID     string
	Revision int64
}

// Delivery is a claimed queue task.
type Delivery struct {
	Ticket
	Key string
}

// Queue holds task state and claims for task steps. Postgres stays the
// authority for run state and leases; the queue decides who gets a task.
type Queue interface {
	// Submit creates the task for key once. A repeated call with the same
	// queue and key returns the first task.
	Submit(ctx context.Context, queue, key string) (Ticket, error)
	// Claim takes the next pending task of queue, or returns nil.
	Claim(ctx context.Context, queue string) (*Delivery, error)
	// Finish closes a task. A task that is already closed is not an error.
	Finish(ctx context.Context, queue string, t Ticket) error
	// Release returns a claimed task to pending. ok is false when the task
	// is not claimed at revision t.Revision.
	Release(ctx context.Context, queue string, t Ticket) (Ticket, bool, error)
}

const (
	statePending = "pending"
	stateWorking = "pending-working"
)

// Corndogs is a Queue on a Corndogs 0.8.0+ server. It uses only keyed
// submissions and guarded operations. Queue names get Prefix, so one
// Corndogs installation can serve several workflow stores.
type Corndogs struct {
	Client *corndogs.CorndogsClient
	Prefix string
	// Timeout bounds each call. The default is 10 seconds.
	Timeout time.Duration
}

// NewCorndogs connects to comma-separated seed addresses, such as
// "corndogs:5080" or "tcp://a:5080,tcp://b:5080", and checks features.
func NewCorndogs(ctx context.Context, seeds, prefix string) (*Corndogs, error) {
	c := &Corndogs{Client: corndogs.NewCluster(strings.Split(seeds, ",")...), Prefix: prefix}
	ctx, cancel := c.deadline(ctx)
	defer cancel()
	if _, err := c.Client.RequireFeatures(ctx, corndogs.FeatureSubmissionKeys, corndogs.FeatureTaskGuards); err != nil {
		return nil, fmt.Errorf("corndogs: %w", err)
	}
	return c, nil
}

func (c *Corndogs) deadline(ctx context.Context) (context.Context, context.CancelFunc) {
	d := c.Timeout
	if d == 0 {
		d = 10 * time.Second
	}
	return context.WithTimeout(ctx, d)
}

func (c *Corndogs) Submit(ctx context.Context, queue, key string) (Ticket, error) {
	ctx, cancel := c.deadline(ctx)
	defer cancel()
	// Timeout -1 parks the task with no timeout. Magi owns the lease.
	r, err := c.Client.SubmitKeyedTask(ctx, corndogs.SubmitKeyedTaskRequest{
		SubmissionKey: key, Guarded: true, Queue: c.Prefix + queue,
		CurrentState: statePending, AutoTargetState: stateWorking, Timeout: -1, Payload: []byte(key),
	})
	if err != nil {
		return Ticket{}, err
	}
	t := Ticket{UUID: r.Receipt.TaskUuid}
	if r.Task != nil {
		t.Revision = r.Task.Revision
	}
	return t, nil
}

func (c *Corndogs) Claim(ctx context.Context, queue string) (*Delivery, error) {
	ctx, cancel := c.deadline(ctx)
	defer cancel()
	// Corndogs keeps no receipt for an empty claim, so each poll needs a new ID.
	r, err := c.Client.ClaimGuardedTask(ctx, corndogs.ClaimGuardedTaskRequest{
		OperationId: corndogs.NewOperationID(), Queue: c.Prefix + queue, CurrentState: statePending,
	})
	if err != nil || r.Delivery == nil {
		return nil, err
	}
	t := r.Delivery.Task
	return &Delivery{Ticket: Ticket{UUID: t.Task.Uuid, Revision: t.Revision}, Key: string(r.Delivery.Payload)}, nil
}

func (c *Corndogs) Finish(ctx context.Context, queue string, t Ticket) error {
	ctx, cancel := c.deadline(ctx)
	defer cancel()
	q := c.Prefix + queue
	for range 3 {
		_, err := c.Client.CompleteGuardedTask(ctx, corndogs.CompleteGuardedTaskRequest{
			OperationId: corndogs.NewOperationID(), Uuid: t.UUID, Queue: q, ExpectedRevision: t.Revision,
		})
		code, ok := corndogs.ServiceErrorCode(err)
		switch {
		case err == nil, ok && code == corndogs.CodeTaskNotFound:
			return nil
		case !ok || code != corndogs.CodeRevisionConflict:
			return err
		}
		// Postgres decided that the task is over; close it at its current revision.
		g, err := c.Client.GetGuardedTask(ctx, corndogs.GetGuardedTaskRequest{Uuid: t.UUID, Queue: q})
		if err != nil {
			return err
		}
		if g.Task == nil || g.Task.Terminal {
			return nil
		}
		t.Revision = g.Task.Revision
	}
	return errors.New("corndogs: task revision keeps changing")
}

func (c *Corndogs) Release(ctx context.Context, queue string, t Ticket) (Ticket, bool, error) {
	ctx, cancel := c.deadline(ctx)
	defer cancel()
	q := c.Prefix + queue
	g, err := c.Client.GetGuardedTask(ctx, corndogs.GetGuardedTaskRequest{Uuid: t.UUID, Queue: q})
	if err != nil || g.Task == nil || g.Task.Terminal || g.Task.Task.CurrentState != stateWorking {
		return t, false, err
	}
	state := stateWorking
	r, err := c.Client.UpdateGuardedTask(ctx, corndogs.UpdateGuardedTaskRequest{
		OperationId: corndogs.NewOperationID(), Uuid: t.UUID, Queue: q, ExpectedRevision: g.Task.Revision,
		ExpectedState: &state, NewState: statePending, AutoTargetState: stateWorking, Timeout: -1,
	})
	if code, ok := corndogs.ServiceErrorCode(err); ok && code == corndogs.CodeRevisionConflict {
		return t, false, nil
	}
	if err != nil {
		return t, false, err
	}
	return Ticket{UUID: t.UUID, Revision: r.Task.Revision}, true, nil
}
