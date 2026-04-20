package teamloader

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/docker/docker-agent/pkg/config"
)

func TestLoad_PipelineUnknownAgent(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "dummy")

	src, err := config.Resolve("testdata/pipeline_unknown_agent.yaml", nil)
	require.NoError(t, err)

	_, err = Load(t.Context(), src, &config.RuntimeConfig{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `agent "pipe"`)
	assert.Contains(t, err.Error(), "pipeline[0]")
	assert.Contains(t, err.Error(), `agent "ghost" not found`)
}

func TestLoad_PipelineUnknownTool(t *testing.T) {
	t.Parallel()

	src, err := config.Resolve("testdata/pipeline_unknown_tool.yaml", nil)
	require.NoError(t, err)

	_, err = Load(t.Context(), src, &config.RuntimeConfig{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `agent "pipe"`)
	assert.Contains(t, err.Error(), "pipeline[0]")
	assert.Contains(t, err.Error(), `tool "not_a_real_tool"`)
}
