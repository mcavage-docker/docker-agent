package runtime

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/docker/docker-agent/pkg/config/latest"
)

func TestRenderPipelineTemplate(t *testing.T) {
	tests := []struct {
		name   string
		tmpl   string
		input  string
		output string
		want   string
	}{
		{
			name:   "empty template returns empty string",
			tmpl:   "",
			input:  "user question",
			output: "previous output",
			want:   "",
		},
		{
			name:   "template with input only",
			tmpl:   "Research: {{input}}",
			input:  "quantum computing",
			output: "some output",
			want:   "Research: quantum computing",
		},
		{
			name:   "template with output only",
			tmpl:   "Edit this:\n{{output}}",
			input:  "original",
			output: "draft article",
			want:   "Edit this:\ndraft article",
		},
		{
			name:   "template with both input and output",
			tmpl:   "Original: {{input}}\nResearch: {{output}}",
			input:  "AI safety",
			output: "key findings...",
			want:   "Original: AI safety\nResearch: key findings...",
		},
		{
			name:   "template with no placeholders",
			tmpl:   "Just a static task",
			input:  "ignored",
			output: "also ignored",
			want:   "Just a static task",
		},
		{
			name:   "multiple occurrences of same placeholder",
			tmpl:   "{{input}} and again {{input}}",
			input:  "hello",
			output: "world",
			want:   "hello and again hello",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := renderPipelineTemplate(tt.tmpl, tt.input, tt.output)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestRenderPipelineArgs(t *testing.T) {
	tests := []struct {
		name   string
		args   map[string]any
		input  string
		output string
		want   map[string]any
	}{
		{
			name:   "nil args",
			args:   nil,
			input:  "hello",
			output: "world",
			want:   nil,
		},
		{
			name:   "empty args",
			args:   map[string]any{},
			input:  "hello",
			output: "world",
			want:   map[string]any{},
		},
		{
			name:   "string values are rendered",
			args:   map[string]any{"url": "https://example.com/search?q={{input}}", "format": "json"},
			input:  "golang",
			output: "ignored",
			want:   map[string]any{"url": "https://example.com/search?q=golang", "format": "json"},
		},
		{
			name:   "output template in args",
			args:   map[string]any{"cmd": "echo '{{output}}' | wc -w"},
			input:  "original",
			output: "three word sentence",
			want:   map[string]any{"cmd": "echo 'three word sentence' | wc -w"},
		},
		{
			name:   "non-string values pass through",
			args:   map[string]any{"timeout": 30, "verbose": true, "query": "{{input}}"},
			input:  "search term",
			output: "",
			want:   map[string]any{"timeout": 30, "verbose": true, "query": "search term"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := renderPipelineArgs(tt.args, tt.input, tt.output)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestPipelineStepLabels(t *testing.T) {
	steps := []latest.PipelineStep{
		{Agent: "researcher"},
		{Tool: "fetch"},
		{Agent: "writer"},
		{Tool: "shell"},
	}
	labels := pipelineStepLabels(steps)
	assert.Equal(t, []string{"researcher", "fetch", "writer", "shell"}, labels)
}
