package config

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/goccy/go-yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/docker/docker-agent/pkg/config/latest"
)

// TestPipelineRoundTrip parses a pipeline fixture, asserts that every
// PipelineStep field survives parsing, marshals the config back to YAML,
// re-parses it, and asserts the second parse produces an equal struct.
//
// TestParseExamplesAfterMarshalling covers parse -> marshal -> parse for the
// broader examples/ tree but does not DeepEqual the two parses, so silent
// field-drop regressions can slip through. This test closes that gap
// specifically for the new Pipeline fields.
func TestPipelineRoundTrip(t *testing.T) {
	t.Parallel()

	cfg, err := Load(t.Context(), NewFileSource(filepath.Join("testdata", "pipeline.yaml")))
	require.NoError(t, err)

	pipe, ok := cfg.Agents.Lookup("pipe")
	require.True(t, ok, "pipe agent should be present")
	require.Len(t, pipe.Pipeline, 3, "pipe should have three steps")

	assertFixtureSteps(t, pipe.Pipeline)

	buf, err := yaml.Marshal(cfg)
	require.NoError(t, err)

	roundTripped, err := Load(t.Context(), NewBytesSource("pipeline.yaml", buf))
	require.NoError(t, err, "re-parsing marshalled config should succeed:\n%s", buf)

	rtPipe, ok := roundTripped.Agents.Lookup("pipe")
	require.True(t, ok)

	assert.True(t,
		reflect.DeepEqual(pipe.Pipeline, rtPipe.Pipeline),
		"pipeline should round-trip unchanged\nbefore: %#v\nafter:  %#v",
		pipe.Pipeline, rtPipe.Pipeline,
	)
}

func assertFixtureSteps(t *testing.T, steps []latest.PipelineStep) {
	t.Helper()

	// Step 0: tool step with templated string arg and a numeric arg.
	assert.Empty(t, steps[0].Agent, "step 0 must be a tool step")
	assert.Equal(t, "search_memories", steps[0].Tool)
	assert.Empty(t, steps[0].Task, "tool step must not carry task")
	assert.Equal(t, "{{input}}", steps[0].Args["query"])
	assert.EqualValues(t, 5, steps[0].Args["limit"])

	// Step 1: agent step with task referencing both template variables.
	assert.Equal(t, "writer", steps[1].Agent)
	assert.Empty(t, steps[1].Tool, "agent step must not carry tool")
	assert.Empty(t, steps[1].Args, "agent step must not carry args")
	assert.Contains(t, steps[1].Task, "{{input}}")
	assert.Contains(t, steps[1].Task, "{{output}}")

	// Step 2: agent step with no task — previous output is forwarded verbatim.
	assert.Equal(t, "editor", steps[2].Agent)
	assert.Empty(t, steps[2].Task, "step 2 should have no task")
	assert.Empty(t, steps[2].Tool)
	assert.Empty(t, steps[2].Args)
}
