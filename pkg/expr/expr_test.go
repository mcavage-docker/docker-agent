package expr

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCompile_SyntaxError(t *testing.T) {
	env, err := NewEnv([]string{"x"})
	require.NoError(t, err)

	_, err = env.Compile("x ==")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "compiling CEL")
}

func TestCompile_UndeclaredVariable(t *testing.T) {
	env, err := NewEnv([]string{"input"})
	require.NoError(t, err)

	_, err = env.Compile("output == 'x'")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "output")
}

func TestCompile_NonBoolResult(t *testing.T) {
	env, err := NewEnv([]string{"x"})
	require.NoError(t, err)

	_, err = env.Compile("x + 1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must return bool")
}

func TestEval_StringEquality(t *testing.T) {
	env, err := NewEnv([]string{"intent"})
	require.NoError(t, err)

	prog, err := env.Compile("intent == 'refund'")
	require.NoError(t, err)

	ok, err := prog.EvalBool(map[string]any{"intent": "refund"})
	require.NoError(t, err)
	assert.True(t, ok)

	ok, err = prog.EvalBool(map[string]any{"intent": "technical"})
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestEval_MapFieldAccess(t *testing.T) {
	env, err := NewEnv([]string{"classification"})
	require.NoError(t, err)

	prog, err := env.Compile("classification.intent == 'refund'")
	require.NoError(t, err)

	ok, err := prog.EvalBool(map[string]any{
		"classification": map[string]any{"intent": "refund", "confidence": 0.9},
	})
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestEval_HasMacro_OptionalField(t *testing.T) {
	env, err := NewEnv([]string{"x"})
	require.NoError(t, err)

	prog, err := env.Compile("has(x.confidence) && x.confidence > 0.5")
	require.NoError(t, err)

	// field missing -> short-circuits to false
	ok, err := prog.EvalBool(map[string]any{"x": map[string]any{"intent": "refund"}})
	require.NoError(t, err)
	assert.False(t, ok)

	// field present and high enough
	ok, err = prog.EvalBool(map[string]any{"x": map[string]any{"confidence": 0.9}})
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestEval_InOperator(t *testing.T) {
	env, err := NewEnv([]string{"intent"})
	require.NoError(t, err)

	prog, err := env.Compile("intent in ['refund', 'billing']")
	require.NoError(t, err)

	ok, err := prog.EvalBool(map[string]any{"intent": "refund"})
	require.NoError(t, err)
	assert.True(t, ok)

	ok, err = prog.EvalBool(map[string]any{"intent": "other"})
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestEval_ExistsMacro(t *testing.T) {
	env, err := NewEnv([]string{"analysis"})
	require.NoError(t, err)

	prog, err := env.Compile("analysis.issues.exists(i, i.severity == 'error')")
	require.NoError(t, err)

	ok, err := prog.EvalBool(map[string]any{
		"analysis": map[string]any{
			"issues": []any{
				map[string]any{"severity": "warning"},
				map[string]any{"severity": "error"},
			},
		},
	})
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestEval_FieldTypoSurfacesAtRuntime(t *testing.T) {
	env, err := NewEnv([]string{"classification"})
	require.NoError(t, err)

	// DynType means we don't catch the typo at compile time.
	prog, err := env.Compile("classification.itnent == 'refund'")
	require.NoError(t, err)

	_, err = prog.EvalBool(map[string]any{
		"classification": map[string]any{"intent": "refund"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "evaluating CEL")
}
