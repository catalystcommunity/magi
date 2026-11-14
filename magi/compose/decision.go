package compose

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/catalystcommunity/magi/lib/rpc"
	magiapi "github.com/catalystcommunity/magi/magi/api"
	inferenceapi "github.com/catalystcommunity/magi/primitives/inference/api"
	modelsapi "github.com/catalystcommunity/magi/primitives/models/api"
)

// DecisionTask is the default task of decisions (evals/decide/README.md).
const DecisionTask = "decide"

// Decision answers typed questions about a state with a decision model of
// the catalog: an inference host with decide (InferenceService/decide).
func (m *Service) Decision(ctx context.Context, req magiapi.DecisionRequest) (magiapi.DecisionResponse, error) {
	if _, err := rpc.Caller(ctx); err != nil {
		return magiapi.DecisionResponse{}, err
	}
	task := DecisionTask
	if t := deref(req.Task); t != "" {
		task = t
	}
	var sel *modelsapi.Selector
	strict := false
	if req.Model != nil {
		s := modelsapi.Selector(*req.Model)
		if m.S.isOperator(ctx) != nil {
			s.MaxInputCost, s.MaxOutputCost = nil, nil
			strict = true
		}
		sel = &s
	}
	return m.S.Decide(ctx, task, sel, strict, req.State, req.Questions)
}

// Decide asks the models of task in order; the first that answers wins.
func (s *System) Decide(ctx context.Context, task string, sel *modelsapi.Selector, strict bool, state string, qs []magiapi.DecisionQuestion) (magiapi.DecisionResponse, error) {
	if s.Router == nil {
		return magiapi.DecisionResponse{}, rpc.Unavailable("this installation has no model catalog")
	}
	if len(qs) == 0 || len(qs) > 64 {
		return magiapi.DecisionResponse{}, rpc.Invalid("1 to 64 questions")
	}
	ms, err := s.Router.Models(ctx, task, sel, strict)
	if err != nil {
		return magiapi.DecisionResponse{}, err
	}
	var errs []string
	for _, mdl := range ms {
		res, err := s.decideWith(ctx, mdl, state, qs)
		if err == nil {
			return res, nil
		}
		if c := rpc.CodeOf(err); c == 400 {
			return magiapi.DecisionResponse{}, err
		}
		errs = append(errs, mdl.Name+": "+err.Error())
	}
	if len(ms) == 0 {
		return magiapi.DecisionResponse{}, rpc.Unavailable("no decision model for task %s", task)
	}
	return magiapi.DecisionResponse{}, rpc.Unavailable("no decision model answered: %s", strings.Join(errs, "; "))
}

func (s *System) decideWith(ctx context.Context, mdl modelsapi.Model, state string, qs []magiapi.DecisionQuestion) (magiapi.DecisionResponse, error) {
	key := ""
	if r := mdl.Endpoint.KeyRef; r != nil && *r != "" {
		k, ok := s.Router.Keys[*r]
		if !ok {
			return magiapi.DecisionResponse{}, fmt.Errorf("no key %s", *r)
		}
		key = k
	}
	// The host root: the base URL without /v1.
	root := strings.TrimSuffix(strings.TrimRight(mdl.Endpoint.BaseUrl, "/"), "/v1")
	c := rpc.NewClient(root, rpc.StaticToken(key))
	c.HTTP = &http.Client{Timeout: 60 * time.Second, Transport: s.Router.Transport}
	iq := make([]inferenceapi.Question, len(qs))
	cal := make([]bool, len(qs))
	for i, q := range qs {
		iq[i] = inferenceapi.Question{Id: q.Id, Type: q.Type, Question: q.Question, Choices: q.Choices, Max: q.Max, Scale: q.Scale}
		if t, ok := mdl.Calibration[q.Id]; ok && t > 0 {
			iq[i].Temperature = &t
			cal[i] = true
		}
	}
	var model *string
	if mdl.Endpoint.Model != "" {
		model = &mdl.Endpoint.Model
	}
	// The key of the model, never a token of the context: a turn has the
	// service token of Magi in its context, and a model host must not get it.
	res, err := inferenceapi.NewInferenceClient(c).Decide(rpc.WithToken(ctx, key), inferenceapi.DecideRequest{State: state, Questions: iq, Model: model})
	if err != nil {
		return magiapi.DecisionResponse{}, err
	}
	out := magiapi.DecisionResponse{Model: mdl.Name, Ms: res.Ms}
	for i, a := range res.Answers {
		out.Answers = append(out.Answers, magiapi.DecisionAnswer{Id: a.Id, Probs: a.Probs, Answer: a.Answer,
			Confidence: a.Confidence, Expected: a.Expected, Calibrated: i < len(cal) && cal[i]})
	}
	return out, nil
}
