package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/docker/docker-agent/pkg/agent"
	"github.com/docker/docker-agent/pkg/config/latest"
	"github.com/docker/docker-agent/pkg/session"
	"github.com/docker/docker-agent/pkg/team"
	"github.com/docker/docker-agent/pkg/tools"
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
			vars := map[string]any{"input": tt.input, "output": tt.output}
			got := renderPipelineTemplate(tt.tmpl, vars)
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
			vars := map[string]any{"input": tt.input, "output": tt.output}
			got := renderPipelineArgs(tt.args, vars)
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

// newEchoTool returns a tool whose handler JSON-encodes its input args and
// returns them as output, optionally marked as error. The prefix is prepended
// to the encoded args so each step can be attributed in assertions.
func newEchoTool(name, prefix string, isError bool) tools.Tool {
	return tools.Tool{
		Name:       name,
		Parameters: map[string]any{},
		Handler: func(_ context.Context, tc tools.ToolCall) (*tools.ToolCallResult, error) {
			out := prefix + "|" + tc.Function.Arguments
			if isError {
				return tools.ResultError(out), nil
			}
			return tools.ResultSuccess(out), nil
		},
	}
}

// drainEvents reads all events from ch until it is closed, returning them in order.
func drainEvents(ch <-chan Event) []Event {
	var out []Event
	for ev := range ch {
		out = append(out, ev)
	}
	return out
}

// filterPipelineProgress extracts all PipelineProgressEvents from an event slice.
func filterPipelineProgress(events []Event) []*PipelineProgressEvent {
	var out []*PipelineProgressEvent
	for _, ev := range events {
		if p, ok := ev.(*PipelineProgressEvent); ok {
			out = append(out, p)
		}
	}
	return out
}

// filterToolCallResponses extracts all ToolCallResponseEvents from an event slice.
func filterToolCallResponses(events []Event) []*ToolCallResponseEvent {
	var out []*ToolCallResponseEvent
	for _, ev := range events {
		if r, ok := ev.(*ToolCallResponseEvent); ok {
			out = append(out, r)
		}
	}
	return out
}

// TestPipelineExecutionE2E_AllToolSteps runs a three-tool-step pipeline end to
// end and verifies:
//   - PipelineProgressEvent is emitted with the correct (started/completed,
//     step index, total) for every step in declared order
//   - step N's output flows into step N+1 via the {{output}} template in args
//   - the tool call arguments rendered for each step contain the expected
//     substituted values
func TestPipelineExecutionE2E_AllToolSteps(t *testing.T) {
	toolA := newEchoTool("step_a", "A", false)
	toolB := newEchoTool("step_b", "B", false)
	toolC := newEchoTool("step_c", "C", false)

	pipe := agent.New("pipe", "pipeline agent",
		agent.WithToolSets(newStubToolSet(nil, []tools.Tool{toolA, toolB, toolC}, nil)),
		agent.WithPipeline([]latest.PipelineStep{
			{Tool: "step_a", Args: map[string]any{"q": "{{input}}"}},
			{Tool: "step_b", Args: map[string]any{"q": "{{output}}"}},
			{Tool: "step_c", Args: map[string]any{"q": "{{output}}"}},
		}),
	)

	tm := team.New(team.WithAgents(pipe))
	rt, err := NewLocalRuntime(tm, WithSessionCompaction(false), WithModelStore(mockModelStore{}))
	require.NoError(t, err)

	sess := session.New(session.WithUserMessage("HELLO"))
	sess.Title = "Unit Test"

	events := drainEvents(rt.RunStream(t.Context(), sess))

	// PipelineProgress: 3 steps × (started + completed) = 6 events, ordered.
	progress := filterPipelineProgress(events)
	require.Len(t, progress, 6, "expected 6 pipeline-progress events")

	expected := []struct {
		step   int
		status string
		label  string
	}{
		{0, "started", "step_a"},
		{0, "completed", "step_a"},
		{1, "started", "step_b"},
		{1, "completed", "step_b"},
		{2, "started", "step_c"},
		{2, "completed", "step_c"},
	}
	for i, want := range expected {
		assert.Equal(t, want.step, progress[i].StepIndex, "progress[%d] step index", i)
		assert.Equal(t, want.status, progress[i].Status, "progress[%d] status", i)
		assert.Equal(t, want.label, progress[i].StepAgent, "progress[%d] label", i)
		assert.Equal(t, 3, progress[i].TotalSteps, "progress[%d] total", i)
		assert.Equal(t, "pipe", progress[i].PipelineAgent, "progress[%d] pipeline agent", i)
	}

	// Tool responses — one per step, in order. Verify output chaining: step_b's
	// args should contain step_a's output; step_c's args should contain step_b's.
	responses := filterToolCallResponses(events)
	require.Len(t, responses, 3, "expected one tool response per step")

	// Step 0: {{input}} was "HELLO"
	assert.Contains(t, responses[0].Response, `"q":"HELLO"`, "step 0 should receive the raw user input")

	// Step 1: {{output}} was step 0's full output string ("A|{\"q\":\"HELLO\"}")
	step0Out := responses[0].Response
	step1Args := extractArgsFromResponse(t, responses[1].Response)
	assert.Equal(t, step0Out, step1Args["q"], "step 1 should see step 0's output as {{output}}")

	// Step 2: {{output}} was step 1's full output string
	step1Out := responses[1].Response
	step2Args := extractArgsFromResponse(t, responses[2].Response)
	assert.Equal(t, step1Out, step2Args["q"], "step 2 should see step 1's output as {{output}}")
}

// TestPipelineExecutionE2E_ToolError_Aborts verifies that a tool step returning
// IsError aborts the pipeline — no subsequent steps run, and no
// "completed"/"started" progress events are emitted beyond the failing step.
func TestPipelineExecutionE2E_ToolError_Aborts(t *testing.T) {
	ok1 := newEchoTool("ok_1", "OK1", false)
	bad := newEchoTool("bad", "BAD", true) // tool returns IsError
	never := newEchoTool("never", "NEVER", false)

	pipe := agent.New("pipe", "pipeline agent",
		agent.WithToolSets(newStubToolSet(nil, []tools.Tool{ok1, bad, never}, nil)),
		agent.WithPipeline([]latest.PipelineStep{
			{Tool: "ok_1", Args: map[string]any{"q": "{{input}}"}},
			{Tool: "bad", Args: map[string]any{"q": "{{output}}"}},
			{Tool: "never", Args: map[string]any{"q": "{{output}}"}},
		}),
	)

	tm := team.New(team.WithAgents(pipe))
	rt, err := NewLocalRuntime(tm, WithSessionCompaction(false), WithModelStore(mockModelStore{}))
	require.NoError(t, err)

	sess := session.New(session.WithUserMessage("HELLO"))
	sess.Title = "Unit Test"

	events := drainEvents(rt.RunStream(t.Context(), sess))

	progress := filterPipelineProgress(events)

	// Expect start+complete for step 0, start+complete for step 1 (we still emit
	// "completed" for the failing step after the error is recorded), and
	// NOTHING for step 2.
	stepIndices := make([]int, 0, len(progress))
	for _, p := range progress {
		stepIndices = append(stepIndices, p.StepIndex)
	}
	assert.NotContains(t, stepIndices, 2, "step 2 must not execute after step 1 error")

	// Verify the "never" tool was never invoked.
	responses := filterToolCallResponses(events)
	for _, r := range responses {
		assert.NotContains(t, r.Response, "NEVER|", "tool %q must not have been invoked", "never")
	}

	// Verify the error surfaced as an ErrorEvent mentioning the failing step.
	sawError := false
	for _, ev := range events {
		if e, ok := ev.(*ErrorEvent); ok && strings.Contains(e.Error, "pipeline step") {
			sawError = true
		}
	}
	assert.True(t, sawError, "expected an ErrorEvent mentioning the failing pipeline step")
}

// extractArgsFromResponse parses the JSON-echoed args back out of an echo tool
// response string. Echo tools produce "PREFIX|{json args}".
func extractArgsFromResponse(t *testing.T, response string) map[string]any {
	t.Helper()
	idx := strings.IndexByte(response, '|')
	require.GreaterOrEqual(t, idx, 0, "response should contain |: %q", response)
	var args map[string]any
	require.NoError(t, json.Unmarshal([]byte(response[idx+1:]), &args), "args json: %q", response)
	return args
}

// TestRenderPipelineTemplate_NamedAndDotted verifies {{name}} and
// {{name.path}} resolution against the vars map, with map values JSON-rendered
// when used in string context and unknown names left as literal.
func TestRenderPipelineTemplate_NamedAndDotted(t *testing.T) {
	vars := map[string]any{
		"input":          "original",
		"output":         "prev",
		"intent":         "refund",
		"classification": map[string]any{"intent": "technical", "confidence": 0.9},
	}

	tests := []struct {
		name string
		tmpl string
		want string
	}{
		{
			name: "named string var",
			tmpl: "Intent was {{intent}}.",
			want: "Intent was refund.",
		},
		{
			name: "dotted path into map",
			tmpl: "Classified as {{classification.intent}}.",
			want: "Classified as technical.",
		},
		{
			name: "whole map renders as JSON",
			tmpl: "Full: {{classification}}",
			want: `Full: {"confidence":0.9,"intent":"technical"}`,
		},
		{
			name: "unknown name left as literal",
			tmpl: "Unknown: {{missing}}",
			want: "Unknown: {{missing}}",
		},
		{
			name: "unknown path segment left as literal",
			tmpl: "Bad path: {{classification.nope}}",
			want: "Bad path: {{classification.nope}}",
		},
		{
			name: "input and output still work",
			tmpl: "In={{input}}, out={{output}}",
			want: "In=original, out=prev",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := renderPipelineTemplate(tt.tmpl, vars)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestPipelineExecutionE2E_AsAndWhen exercises `as:` capture + CEL-gated
// `when:` end to end. Three tool steps: step 0 produces a JSON object,
// step 1 gates on a field of that object (runs), step 2 gates on a different
// value of that field (skipped).
func TestPipelineExecutionE2E_AsAndWhen(t *testing.T) {
	// Emitter tool whose output is a fixed JSON object — simulates a
	// structured-output tool (A+ rule: tool outputs try-parse as JSON).
	emit := tools.Tool{
		Name:       "emit_classification",
		Parameters: map[string]any{},
		Handler: func(_ context.Context, _ tools.ToolCall) (*tools.ToolCallResult, error) {
			return tools.ResultSuccess(`{"intent":"refund","confidence":0.92}`), nil
		},
	}
	handleRefund := newEchoTool("handle_refund", "REFUND_HANDLED", false)
	handleTechnical := newEchoTool("handle_technical", "TECH_HANDLED", false)

	// Go through yaml.Unmarshal so validatePipeline compiles the CEL
	// programs onto the steps (WhenProgram is runtime-only and only populated
	// by the validator).
	const yamlSrc = `version: "8"
agents:
  pipe:
    description: pipe
    instruction: noop
    pipeline:
      - tool: emit_classification
        as: classification
      - tool: handle_refund
        when: "classification.intent == 'refund'"
        args:
          q: "{{input}}"
      - tool: handle_technical
        when: "classification.intent == 'technical'"
        args:
          q: "{{input}}"
`
	var parsed latest.Config
	require.NoError(t, yaml.Unmarshal([]byte(yamlSrc), &parsed))
	pipeCfg, ok := parsed.Agents.Lookup("pipe")
	require.True(t, ok)

	pipe := agent.New("pipe", "pipeline agent",
		agent.WithToolSets(newStubToolSet(nil, []tools.Tool{emit, handleRefund, handleTechnical}, nil)),
		agent.WithPipeline(pipeCfg.Pipeline), // now carries compiled WhenPrograms
	)

	tm := team.New(team.WithAgents(pipe))
	rt, err := NewLocalRuntime(tm, WithSessionCompaction(false), WithModelStore(mockModelStore{}))
	require.NoError(t, err)

	sess := session.New(session.WithUserMessage("I want a refund"))
	sess.Title = "Unit Test"

	events := drainEvents(rt.RunStream(t.Context(), sess))

	progress := filterPipelineProgress(events)

	// Expected: step 0 started+completed; step 1 started+completed; step 2 skipped.
	var got []string
	for _, p := range progress {
		got = append(got, fmt.Sprintf("%d:%s", p.StepIndex, p.Status))
	}
	assert.Equal(t, []string{
		"0:started", "0:completed",
		"1:started", "1:completed",
		"2:skipped",
	}, got)

	// handle_refund ran (echoed args back); handle_technical did not.
	responses := filterToolCallResponses(events)
	var sawRefund, sawTechnical bool
	for _, r := range responses {
		if strings.HasPrefix(r.Response, "REFUND_HANDLED|") {
			sawRefund = true
		}
		if strings.HasPrefix(r.Response, "TECH_HANDLED|") {
			sawTechnical = true
		}
	}
	assert.True(t, sawRefund, "expected handle_refund to have run")
	assert.False(t, sawTechnical, "handle_technical must have been skipped")
}
