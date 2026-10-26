package policy

import (
	"errors"
	"fmt"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
)

const permitExpression = `issuer == owner && delegate == actor && resource == target && window && approved && !withdrawn && amount_ok && ((purpose == "music" && action == "queue") || (purpose == "calendar" && action == "propose") || (purpose == "music" && action == "queue_age_restricted" && age_trusted))`
const denyExpression = `blocked`

var stringFacts = []string{"issuer", "owner", "delegate", "actor", "resource", "target", "purpose", "action"}
var boolFacts = []string{"window", "approved", "withdrawn", "blocked", "amount_ok", "age_trusted"}

type Evaluator struct{ permit, deny cel.Program }

func newEvaluator(permit, deny string) (*Evaluator, error) {
	options := []cel.EnvOption{}
	for _, key := range stringFacts {
		options = append(options, cel.Variable(key, cel.StringType))
	}
	for _, key := range boolFacts {
		options = append(options, cel.Variable(key, cel.BoolType))
	}
	env, err := cel.NewEnv(options...)
	if err != nil {
		return nil, err
	}
	compile := func(expression string) (cel.Program, error) {
		ast, issues := env.Compile(expression)
		if issues.Err() != nil {
			return nil, issues.Err()
		}
		if ast.OutputType() != cel.BoolType {
			return nil, errors.New("policy must return bool")
		}
		return env.Program(ast, cel.CostLimit(1000))
	}
	a, err := compile(permit)
	if err != nil {
		return nil, err
	}
	b, err := compile(deny)
	if err != nil {
		return nil, err
	}
	return &Evaluator{permit: a, deny: b}, nil
}

func (e *Evaluator) decide(facts map[string]any) (bool, error) {
	// Evaluate both policies. A true permit must not hide a failing deny rule.
	p, _, pe := e.permit.Eval(facts)
	d, _, de := e.deny.Eval(facts)
	if pe != nil || de != nil {
		return false, fmt.Errorf("policy evaluation: permit=%v deny=%v", pe, de)
	}
	if types.IsUnknown(p) || types.IsUnknown(d) || types.IsError(p) || types.IsError(d) {
		return false, errors.New("indeterminate policy result")
	}
	if p.Type() != types.BoolType || d.Type() != types.BoolType {
		return false, errors.New("policy result is not boolean")
	}
	return p == types.True && d == types.False, nil
}
