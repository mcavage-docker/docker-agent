// Package expr wraps google/cel-go to provide the small slice of CEL used by
// deterministic pipelines for the `when:` conditional step. Keeping the
// dependency isolated behind this package makes it cheap to swap evaluators
// later if the need arises.
//
// Expressions are parsed and type-checked at config load time (see
// validatePipeline in pkg/config/latest), producing a compiled Program that
// the runtime evaluates once per step against a map of variable bindings.
//
// All variables registered with NewEnv are typed as cel.DynType. This trades
// load-time catching of field typos and type mismatches for a simpler
// surface — see .claude/todos/pipeline_branching.md decision D7.
package expr

import (
	"fmt"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
)

// Env is a CEL environment parameterised over the set of variable names a
// caller will bind at evaluation time. It wraps *cel.Env so downstream callers
// never see the cel-go import directly.
type Env struct {
	env *cel.Env
}

// Program is a compiled CEL expression ready for repeated evaluation.
type Program struct {
	prog cel.Program
	expr string
}

// NewEnv builds a CEL environment with the given variable names, all typed as
// cel.DynType. The standard macros (has, size, exists, all, filter, map) are
// enabled.
func NewEnv(names []string) (*Env, error) {
	opts := []cel.EnvOption{
		cel.Macros(cel.StandardMacros...),
	}
	for _, n := range names {
		opts = append(opts, cel.Variable(n, cel.DynType))
	}

	env, err := cel.NewEnv(opts...)
	if err != nil {
		return nil, fmt.Errorf("building CEL environment: %w", err)
	}
	return &Env{env: env}, nil
}

// Compile parses and type-checks expr against the environment's variables and
// returns a Program. The expression must produce a bool.
//
// Compilation errors include CEL's column info for syntax mistakes and
// variable-resolution errors (e.g. a reference to an undeclared name).
func (e *Env) Compile(expr string) (*Program, error) {
	ast, issues := e.env.Compile(expr)
	if issues != nil && issues.Err() != nil {
		return nil, fmt.Errorf("compiling CEL expression %q: %w", expr, issues.Err())
	}
	if out := ast.OutputType(); out != cel.BoolType {
		return nil, fmt.Errorf("CEL expression %q must return bool, got %s", expr, out)
	}

	prog, err := e.env.Program(ast)
	if err != nil {
		return nil, fmt.Errorf("building CEL program for %q: %w", expr, err)
	}
	return &Program{prog: prog, expr: expr}, nil
}

// EvalBool evaluates the program with the supplied variable bindings and
// returns the resulting boolean. Runtime errors surface here — most commonly
// field typos on dynamically-typed variables, or comparisons between
// incompatible types.
func (p *Program) EvalBool(vars map[string]any) (bool, error) {
	out, _, err := p.prog.Eval(vars)
	if err != nil {
		return false, fmt.Errorf("evaluating CEL expression %q: %w", p.expr, err)
	}
	// Result must be a native bool — the compile step already verified the
	// expression's static type, but structured-output paths may coerce.
	b, ok := asBool(out)
	if !ok {
		return false, fmt.Errorf("CEL expression %q evaluated to non-bool %v", p.expr, out.Value())
	}
	return b, nil
}

// Expr returns the original expression string, useful for diagnostics.
func (p *Program) Expr() string {
	return p.expr
}

func asBool(v ref.Val) (bool, bool) {
	if v == nil {
		return false, false
	}
	if b, ok := v.(types.Bool); ok {
		return bool(b), true
	}
	if b, ok := v.Value().(bool); ok {
		return b, true
	}
	return false, false
}
