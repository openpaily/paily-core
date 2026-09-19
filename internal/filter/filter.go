// Package filter provides the expr-lang expression evaluation engine used by
// the filtering and naming subsystems.
package filter

import (
	"fmt"
	"reflect"
	"sync"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"
)

// caches holds compiled expr programs keyed by "<expr>:<type>".
var caches sync.Map // map[string]*vm.Program

// compiled returns a cached *vm.Program for the given expression and env type,
// compiling it on first access. envType must be a zero-value struct pointer used
// for type-checking (e.g. (*NodeFilterEnv)(nil)).
func compiled(expression string, cacheKey string, opts ...expr.Option) (*vm.Program, error) {
	if v, ok := caches.Load(cacheKey); ok {
		return v.(*vm.Program), nil
	}
	prog, err := expr.Compile(expression, opts...)
	if err != nil {
		return nil, fmt.Errorf("filter: compile %q: %w", expression, err)
	}
	caches.Store(cacheKey, prog)
	return prog, nil
}

// EvalBool evaluates expression against env, expecting a bool result.
// env must be a struct compatible with the expression.
func EvalBool(expression string, env any) (bool, error) {
	cacheKey := expression + "\x00bool"
	prog, err := compiled(expression, cacheKey, expr.Env(env), expr.AsBool())
	if err != nil {
		return false, err
	}
	result, err := vm.Run(prog, env)
	if err != nil {
		return false, fmt.Errorf("filter: eval %q: %w", expression, err)
	}
	b, ok := result.(bool)
	if !ok {
		return false, fmt.Errorf("filter: expression %q returned %T, expected bool", expression, result)
	}
	return b, nil
}

// EvalString evaluates expression against env, expecting a string result.
func EvalString(expression string, env any) (string, error) {
	// Try compiling with AsKind(reflect.String) first; fall back to AsAny for
	// complex expressions (e.g. ternary chains) the type-checker can't infer.
	cacheKey := expression + "\x00string"
	prog, err := compiled(expression, cacheKey, expr.Env(env), expr.AsKind(reflect.String))
	if err != nil {
		cacheKey2 := expression + "\x00any"
		prog2, err2 := compiled(expression, cacheKey2, expr.Env(env), expr.AsAny())
		if err2 != nil {
			return "", fmt.Errorf("filter: compile %q: %w", expression, err)
		}
		result, err2 := vm.Run(prog2, env)
		if err2 != nil {
			return "", fmt.Errorf("filter: eval %q: %w", expression, err2)
		}
		if s, ok := result.(string); ok {
			return s, nil
		}
		return fmt.Sprintf("%v", result), nil
	}
	result, err := vm.Run(prog, env)
	if err != nil {
		return "", fmt.Errorf("filter: eval %q: %w", expression, err)
	}
	if s, ok := result.(string); ok {
		return s, nil
	}
	return fmt.Sprintf("%v", result), nil
}
