package runtime

import (
	"testing"

	"github.com/stretchr/testify/assert"
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
