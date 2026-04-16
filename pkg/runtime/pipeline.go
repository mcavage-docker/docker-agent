package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/docker/docker-agent/pkg/agent"
	"github.com/docker/docker-agent/pkg/config/latest"
	"github.com/docker/docker-agent/pkg/session"
	"github.com/docker/docker-agent/pkg/tools"
)

// runPipeline executes the deterministic pipeline configured on the current agent.
// Steps are executed sequentially by the runtime; no LLM is involved in routing.
// The output of each step is passed as {{output}} to the next step's task template.
// runPipeline is called from within the RunStream goroutine, so StreamStarted and
// StreamStopped events are handled by the outer RunStream envelope.
func (r *LocalRuntime) runPipeline(ctx context.Context, sess *session.Session, span trace.Span, events chan Event) {
	a := r.CurrentAgent()
	pipeline := a.Pipeline()

	// Build the ordered list of step labels for the TUI sidebar.
	stepLabels := pipelineStepLabels(pipeline)

	// Capture the original user input; it is available as {{input}} in every task template.
	input := sess.GetLastUserMessageContent()
	output := input

	for i, step := range pipeline {
		if ctx.Err() != nil {
			return
		}

		label := stepLabels[i]

		stepCtx, stepSpan := r.startSpan(ctx, "runtime.pipeline_step",
			trace.WithAttributes(
				attribute.String("pipeline.agent", a.Name()),
				attribute.String("step.label", label),
				attribute.Int("step.index", i),
			),
		)

		// Notify the TUI of pipeline progress: step started.
		events <- PipelineProgress(a.Name(), i, len(pipeline), label, stepLabels, "started")

		var stepOutput string
		var stepErr error

		if step.Tool != "" {
			stepOutput, stepErr = r.runPipelineToolStep(stepCtx, sess, step, i, input, output, events)
		} else {
			stepOutput, stepErr = r.runPipelineAgentStep(stepCtx, sess, a, step, i, input, output, stepSpan, events)
		}

		// Notify the TUI of pipeline progress: step completed.
		events <- PipelineProgress(a.Name(), i, len(pipeline), label, stepLabels, "completed")

		stepSpan.End()

		if stepErr != nil {
			return
		}

		output = stepOutput
	}

	// The last step's output has already been displayed by the forwarded sub-session
	// events (for agent steps) or tool call response events (for tool steps),
	// so there is no need to emit a duplicate AgentChoice here.
	span.SetAttributes(attribute.String("pipeline.final_output", output))
}

// runPipelineAgentStep executes a single agent step in the pipeline as a sub-session.
func (r *LocalRuntime) runPipelineAgentStep(
	ctx context.Context, sess *session.Session,
	pipelineAgent *agent.Agent,
	step latest.PipelineStep, index int,
	input, output string,
	span trace.Span,
	events chan Event,
) (string, error) {
	task := renderPipelineTemplate(step.Task, input, output)
	if task == "" {
		task = output
	}

	child, err := r.team.Agent(step.Agent)
	if err != nil {
		events <- Error(fmt.Sprintf("pipeline step %d (%q): agent not found: %v", index+1, step.Agent, err))
		return "", fmt.Errorf("agent not found: %w", err)
	}

	events <- AgentSwitching(true, pipelineAgent.Name(), step.Agent)
	r.setCurrentAgent(step.Agent)
	events <- AgentInfo(child.Name(), getAgentModelID(child), child.Description(), child.WelcomeMessage())

	cfg := SubSessionConfig{
		Task:          task,
		AgentName:     step.Agent,
		Title:         fmt.Sprintf("Pipeline step %d: %s", index+1, step.Agent),
		ToolsApproved: sess.ToolsApproved,
	}
	s := newSubSession(sess, cfg, child)

	result, runErr := r.runSubSessionForwarding(ctx, sess, s, span, events, pipelineAgent.Name())

	r.setCurrentAgent(pipelineAgent.Name())
	events <- AgentSwitching(false, step.Agent, pipelineAgent.Name())
	events <- AgentInfo(pipelineAgent.Name(), "", pipelineAgent.Description(), pipelineAgent.WelcomeMessage())

	if runErr != nil {
		return "", runErr
	}

	return result.Output, nil
}

// runPipelineToolStep executes a single tool step in the pipeline by calling
// the tool handler directly — no LLM is involved.
func (r *LocalRuntime) runPipelineToolStep(
	ctx context.Context, _ *session.Session,
	step latest.PipelineStep, index int,
	input, output string,
	events chan Event,
) (string, error) {
	a := r.CurrentAgent()

	// Resolve tools from the pipeline agent.
	agentTools, err := a.Tools(ctx)
	if err != nil {
		msg := fmt.Sprintf("pipeline step %d: failed to load tools: %v", index+1, err)
		events <- Error(msg)
		return "", fmt.Errorf("%s", msg)
	}

	// Find the tool by name from the pipeline agent's toolset.
	var tool *tools.Tool
	for _, t := range agentTools {
		if t.Name == step.Tool {
			tool = &t
			break
		}
	}
	if tool == nil {
		msg := fmt.Sprintf("pipeline step %d: tool %q not found on agent %q", index+1, step.Tool, a.Name())
		events <- Error(msg)
		return "", fmt.Errorf("%s", msg)
	}

	// Render template variables in args, then marshal to JSON.
	renderedArgs := renderPipelineArgs(step.Args, input, output)
	argsJSON, err := json.Marshal(renderedArgs)
	if err != nil {
		msg := fmt.Sprintf("pipeline step %d: failed to marshal args for tool %q: %v", index+1, step.Tool, err)
		events <- Error(msg)
		return "", fmt.Errorf("%s", msg)
	}

	// Build a synthetic tool call.
	tc := tools.ToolCall{
		ID: fmt.Sprintf("pipeline-%s-%d", step.Tool, time.Now().UnixNano()),
		Function: tools.FunctionCall{
			Name:      step.Tool,
			Arguments: string(argsJSON),
		},
	}

	// Emit TUI events so the tool call is visible in the chat.
	events <- ToolCall(tc, *tool, a.Name())

	result, err := tool.Handler(ctx, tc)
	if err != nil {
		msg := fmt.Sprintf("pipeline step %d: tool %q failed: %v", index+1, step.Tool, err)
		events <- Error(msg)
		return "", fmt.Errorf("%s", msg)
	}

	events <- ToolCallResponse(tc.ID, *tool, result, result.Output, a.Name())

	if result.IsError {
		msg := fmt.Sprintf("pipeline step %d: tool %q returned error: %s", index+1, step.Tool, result.Output)
		events <- Error(msg)
		return "", fmt.Errorf("%s", msg)
	}

	return result.Output, nil
}

// pipelineStepLabels builds human-readable labels for each step in the pipeline.
// Agent steps use the agent name; tool steps use the tool name.
func pipelineStepLabels(steps []latest.PipelineStep) []string {
	labels := make([]string, len(steps))
	for i, step := range steps {
		if step.Tool != "" {
			labels[i] = step.Tool
		} else {
			labels[i] = step.Agent
		}
	}
	return labels
}

// renderPipelineTemplate replaces {{input}} and {{output}} in a task template string.
// If the template is empty the empty string is returned (caller should fall back to
// passing the previous step's output verbatim).
func renderPipelineTemplate(tmpl, input, output string) string {
	if tmpl == "" {
		return ""
	}
	return strings.NewReplacer(
		"{{input}}", input,
		"{{output}}", output,
	).Replace(tmpl)
}

// renderPipelineArgs renders {{input}} and {{output}} template variables in
// tool step arguments. Only string values are rendered; other types are
// passed through unchanged.
func renderPipelineArgs(args map[string]any, input, output string) map[string]any {
	if len(args) == 0 {
		return args
	}
	result := make(map[string]any, len(args))
	for k, v := range args {
		if s, ok := v.(string); ok {
			result[k] = renderPipelineTemplate(s, input, output)
		} else {
			result[k] = v
		}
	}
	return result
}
