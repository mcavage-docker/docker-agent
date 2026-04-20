package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
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
//
// Variables available to each step:
//   - input  — the original user input (constant across the pipeline)
//   - output — the output of the last *executed* step (falls through to input
//              if every preceding step was skipped)
//   - <name> — any `as:`-bound value declared by an earlier step
//
// `when:` expressions evaluate in CEL against these variables; false-returning
// expressions cause the step to be skipped.
func (r *LocalRuntime) runPipeline(ctx context.Context, sess *session.Session, span trace.Span, events chan Event) {
	a := r.CurrentAgent()
	pipeline := a.Pipeline()

	// Build the ordered list of step labels for the TUI sidebar.
	stepLabels := pipelineStepLabels(pipeline)

	input := sess.GetLastUserMessageContent()
	output := input

	// vars is the shared name table for templates and CEL. `input` is constant;
	// `output` is rebound after each executed step.
	vars := map[string]any{
		"input":  input,
		"output": output,
	}

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

		// Evaluate `when:` before doing anything else — a skipped step should
		// produce exactly one "skipped" progress event and advance immediately.
		if step.WhenProgram() != nil {
			run, err := step.WhenProgram().EvalBool(vars)
			if err != nil {
				msg := fmt.Sprintf("pipeline step %d: %v", i+1, err)
				events <- Error(msg)
				stepSpan.SetAttributes(attribute.String("when.error", err.Error()))
				stepSpan.End()
				return
			}
			stepSpan.SetAttributes(
				attribute.String("when.expr", step.When),
				attribute.Bool("when.result", run),
			)
			if !run {
				slog.Info("pipeline step skipped",
					"pipeline_agent", a.Name(),
					"step_index", i,
					"label", label,
					"when", step.When,
				)
				events <- PipelineProgress(a.Name(), i, len(pipeline), label, stepLabels, "skipped")
				stepSpan.End()
				continue
			}
		}

		// Notify the TUI of pipeline progress: step started.
		events <- PipelineProgress(a.Name(), i, len(pipeline), label, stepLabels, "started")

		var (
			stepOutput  string
			stepErr     error
			parseAsJSON bool
		)

		if step.Tool != "" {
			stepOutput, stepErr = r.runPipelineToolStep(stepCtx, sess, step, i, vars, events)
			// Tool outputs: best-effort JSON parse (A+ rule, tool half).
			parseAsJSON = true
		} else {
			var childHasStructuredOutput bool
			stepOutput, childHasStructuredOutput, stepErr = r.runPipelineAgentStep(stepCtx, sess, a, step, i, vars, stepSpan, events)
			// Agent outputs: parse only if the child agent declared `structured_output:`.
			parseAsJSON = childHasStructuredOutput
		}

		// Notify the TUI of pipeline progress: step completed.
		events <- PipelineProgress(a.Name(), i, len(pipeline), label, stepLabels, "completed")

		stepSpan.End()

		if stepErr != nil {
			return
		}

		// Rebind `output` for the next step.
		output = stepOutput
		vars["output"] = output

		// Capture the output under `as:` (if declared) using the A+ parsing rule.
		if step.As != "" {
			vars[step.As] = normalizeOutput(stepOutput, parseAsJSON)
		}
	}

	// The last step's output has already been displayed by the forwarded sub-session
	// events (for agent steps) or tool call response events (for tool steps),
	// so there is no need to emit a duplicate AgentChoice here.
	span.SetAttributes(attribute.String("pipeline.final_output", output))
}

// normalizeOutput applies the A+ JSON-parsing rule: JSON-decode the step output
// if the caller says we should, keep it as a string otherwise. On decode
// failure we also fall back to string — tools returning plain text are
// first-class citizens.
func normalizeOutput(raw string, tryJSON bool) any {
	if !tryJSON {
		return raw
	}
	var parsed any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return raw
	}
	return parsed
}

// runPipelineAgentStep executes a single agent step in the pipeline as a sub-session.
//
// Returns the step's output string, a flag indicating whether the child agent
// declared structured output (the caller uses this for A+ JSON parsing), and
// any execution error.
func (r *LocalRuntime) runPipelineAgentStep(
	ctx context.Context, sess *session.Session,
	pipelineAgent *agent.Agent,
	step latest.PipelineStep, index int,
	vars map[string]any,
	span trace.Span,
	events chan Event,
) (string, bool, error) {
	task := renderPipelineTemplate(step.Task, vars)
	if task == "" {
		task = asString(vars["output"])
	}

	child, err := r.team.Agent(step.Agent)
	if err != nil {
		events <- Error(fmt.Sprintf("pipeline step %d (%q): agent not found: %v", index+1, step.Agent, err))
		return "", false, fmt.Errorf("agent not found: %w", err)
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
		return "", false, runErr
	}

	return result.Output, child.HasStructuredOutput(), nil
}

// runPipelineToolStep executes a single tool step in the pipeline by calling
// the tool handler directly — no LLM is involved.
func (r *LocalRuntime) runPipelineToolStep(
	ctx context.Context, _ *session.Session,
	step latest.PipelineStep, index int,
	vars map[string]any,
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
	renderedArgs := renderPipelineArgs(step.Args, vars)
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

// templateVarPattern matches `{{name}}` and `{{name.path.with.dots}}`. Names
// and path segments must be standard identifiers.
var templateVarPattern = regexp.MustCompile(`\{\{\s*([a-zA-Z_][a-zA-Z0-9_]*(?:\.[a-zA-Z_][a-zA-Z0-9_]*)*)\s*\}\}`)

// renderPipelineTemplate resolves `{{name}}` and `{{name.path}}` references in
// a task/args template string against the vars map. Non-string terminal values
// are rendered as JSON. Unknown names or invalid paths are left as-is (the
// original `{{...}}` literal) so failures are visible in the rendered output
// rather than silently swallowed.
//
// Returns the empty string for an empty template (caller falls back to passing
// the previous step's output verbatim).
func renderPipelineTemplate(tmpl string, vars map[string]any) string {
	if tmpl == "" {
		return ""
	}
	return templateVarPattern.ReplaceAllStringFunc(tmpl, func(match string) string {
		groups := templateVarPattern.FindStringSubmatch(match)
		if len(groups) < 2 {
			return match
		}
		path := strings.Split(groups[1], ".")
		val, ok := lookupVar(vars, path)
		if !ok {
			return match
		}
		return asString(val)
	})
}

// renderPipelineArgs renders template variables in tool step arguments. String
// values go through the template renderer; non-string values pass through.
func renderPipelineArgs(args map[string]any, vars map[string]any) map[string]any {
	if len(args) == 0 {
		return args
	}
	result := make(map[string]any, len(args))
	for k, v := range args {
		if s, ok := v.(string); ok {
			result[k] = renderPipelineTemplate(s, vars)
		} else {
			result[k] = v
		}
	}
	return result
}

// lookupVar walks a dotted path into a value retrieved from vars. The first
// segment is a key in vars; subsequent segments navigate into map[string]any
// values. Returns the resolved value and whether the lookup succeeded.
func lookupVar(vars map[string]any, path []string) (any, bool) {
	if len(path) == 0 {
		return nil, false
	}
	cur, ok := vars[path[0]]
	if !ok {
		return nil, false
	}
	for _, seg := range path[1:] {
		m, isMap := cur.(map[string]any)
		if !isMap {
			return nil, false
		}
		cur, ok = m[seg]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

// asString renders a value for use inside a string template. Strings pass
// through; everything else is JSON-encoded (consistent with how CEL-facing
// structured values would be serialised back to text).
func asString(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}
