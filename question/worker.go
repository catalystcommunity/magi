package question

import (
	w "github.com/catalystcommunity/magi/generated/go"
	"github.com/catalystcommunity/magi/workflow"
	"time"
)

// WorkerConfig is an operator-installed capability, not a self-registration
// request. The service API still authenticates the actor through LinkKeys.
type WorkerConfig struct {
	Actor       w.ApplicationRef `json:"actor"`
	Owner       w.ScopeRef       `json:"owner"`
	Model       ModelConfig      `json:"model"`
	CallSeconds int              `json:"call_seconds"`
}

func NewWorker(store *workflow.Store, c WorkerConfig) (*workflow.Worker, error) {
	if store == nil || store.Journal == nil || store.Journal.Pool == nil || store.Journal.Transport == nil ||
		c.Model.Validate() != nil || !workflow.ValidateDefinition(Definition(c.Owner)).Valid || c.CallSeconds < 8 || c.CallSeconds > 120 {
		return nil, workflow.ErrInvalid
	}
	adapter := &Adapter{Store: store, Config: c.Model, Actor: c.Actor, Owner: c.Owner}
	engine := &workflow.Engine{Store: store, CallTimeout: time.Duration(c.CallSeconds) * time.Second, Adapters: map[string]workflow.CallAdapter{Capability: adapter}}
	return &workflow.Worker{Engine: engine, Definition: Reference(c.Owner), Actor: &c.Actor}, nil
}
