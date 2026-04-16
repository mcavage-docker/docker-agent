package runtime

import (
	"context"
	"fmt"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/docker/docker-agent/pkg/session"
)

// runPipeline executes the deterministic pipeline configured on the current agent.
// Steps are executed sequentially by the runtime; no LLM is involved in routing.
// The output of each step is passed as {{output}} to the next step's task template.
// runPipeline is called from within the RunStream goroutine, so StreamStarted and
// StreamStopped events are handled by the outer RunStream envelope.
func (r *LocalRuntime) runPipeline(ctx context.Context, sess *session.Session, span trace.Span, events chan Event) {
	a := r.CurrentAgent()

	// Capture the original user input; it is available as {{input}} in every task template.
	input := sess.GetLastUserMessageContent()
	output := input

	for i, step := range a.Pipeline() {
		if ctx.Err() != nil {
			return
		}

		task := renderPipelineTemplate(step.Task, input, output)
		if task == "" {
			// No task template: forward the previous step's output as-is.
			task = output
		}

		child, err := r.team.Agent(step.Agent)
		if err != nil {
			events <- Error(fmt.Sprintf("pipeline step %d (%q): agent not found: %v", i+1, step.Agent, err))
			return
		}

		stepCtx, stepSpan := r.startSpan(ctx, "runtime.pipeline_step",
			trace.WithAttributes(
				attribute.String("pipeline.agent", a.Name()),
				attribute.String("step.agent", step.Agent),
				attribute.Int("step.index", i),
			),
		)

		// Notify the TUI that we are switching into the child agent.
		events <- AgentSwitching(true, a.Name(), step.Agent)
		r.setCurrentAgent(step.Agent)
		events <- AgentInfo(child.Name(), getAgentModelID(child), child.Description(), child.WelcomeMessage())

		cfg := SubSessionConfig{
			Task:          task,
			AgentName:     step.Agent,
			Title:         fmt.Sprintf("Pipeline step %d: %s", i+1, step.Agent),
			ToolsApproved: sess.ToolsApproved,
		}
		s := newSubSession(sess, cfg, child)

		result, runErr := r.runSubSessionForwarding(stepCtx, sess, s, stepSpan, events, a.Name())

		// Restore the pipeline agent as current before emitting switch-back events.
		r.setCurrentAgent(a.Name())
		events <- AgentSwitching(false, step.Agent, a.Name())
		events <- AgentInfo(a.Name(), "", a.Description(), a.WelcomeMessage())

		stepSpan.End()

		if runErr != nil {
			// runSubSessionForwarding has already emitted an Error event.
			return
		}

		output = result.Output
	}

	// The last step's output has already been displayed by the forwarded sub-session
	// events, so there is no need to emit a duplicate AgentChoice here.
	// We store it on the span so it is captured in traces.
	span.SetAttributes(attribute.String("pipeline.final_output", output))
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
