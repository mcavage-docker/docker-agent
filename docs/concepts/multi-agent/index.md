---
title: "Multi-Agent Systems"
description: "Build teams of specialized agents that collaborate and delegate tasks to each other."
permalink: /concepts/multi-agent/
---

# Multi-Agent Systems

_Build teams of specialized agents that collaborate and delegate tasks to each other._

## Why Multi-Agent?

Complex tasks benefit from specialization. Instead of one monolithic agent trying to do everything, you can create a **team** of focused agents:

- A **coordinator** that understands the overall goal and delegates
- A **developer** that writes code with filesystem and shell access
- A **reviewer** that checks code quality
- A **researcher** that searches the web for information

Each agent has its own model, tools, and instructions — optimized for its specific role.

## Three Patterns: Delegation, Handoffs, Pipelines

docker-agent supports three multi-agent patterns:

| | **Delegation** (`sub_agents`) | **Handoffs** (`handoffs`) | **Pipeline** (`pipeline`) |
|---|---|---|---|
| **Topology** | Hierarchical (parent → child → parent) | Peer-to-peer graph (A → B → C → A) | Fixed linear sequence (step 1 → step 2 → step 3) |
| **Routing** | LLM chooses which child to call | LLM chooses when to hand off | **Runtime drives execution — no LLM involved in routing** |
| **Session** | Child runs in a **sub-session** | Conversation stays in the **same session** | Each step runs in its own **sub-session** |
| **Context** | Child gets a clean task description | Next agent sees the **full conversation history** | Next step sees previous step's output via `{{output}}` |
| **Control flow** | Parent blocks until child finishes | Active agent switches | Steps execute in declared order — none skipped, none reordered |
| **Tool** | `transfer_task` | `handoff` | — (no tool; execution is programmatic) |
| **Best for** | Task delegation to specialists | Conversational routing, agent graphs | Deterministic workflows where order matters |

`sub_agents` and `handoffs` can be combined on the same agent. `pipeline` is mutually exclusive with both — a pipeline agent is a pure sequencer.

<div class="callout callout-tip" markdown="1">
<div class="callout-title">💡 When to use which
</div>
  <p><strong><code>sub_agents</code></strong> — Use when a coordinator needs to send tasks to specialists and synthesize their results.</p>
  <p><strong><code>handoffs</code></strong> — Use when agents should take turns processing the same conversation (conversational routing, agent graphs).</p>
  <p><strong><code>pipeline</code></strong> — Use when the order of steps is fixed and you don't want an LLM deciding what happens next. Ideal for research → write → edit flows, ETL-style data processing, or any workflow that must run the same way every time.</p>
  <p><strong><code>background_agents</code></strong> — Use when multiple independent tasks can run simultaneously.</p>

</div>

## Delegation with `sub_agents`

Agents delegate tasks using the built-in `transfer_task` tool, which is automatically available to any agent with `sub_agents`. The parent agent sends a task to a child agent, waits for the result, and then continues.

1. **User** sends a message to the root agent
2. **Root agent** analyzes the request and decides which sub-agent should handle it
3. **Root agent** calls `transfer_task` with the target agent, task description, and expected output
4. **Sub-agent** processes the task in its own agentic loop using its tools
5. **Results** flow back to the root agent, which responds to the user

```bash
# The transfer_task tool call looks like:
transfer_task(
  agent="developer",
  task="Create a REST API endpoint for user authentication",
  expected_output="Working Go code with tests"
)
```

<div class="callout callout-info" markdown="1">
<div class="callout-title">ℹ️ Auto-Approved
</div>
  <p>Unlike other tools, <code>transfer_task</code> is always auto-approved — no user confirmation needed. This allows seamless delegation between agents.</p>

</div>

## Handoffs Routing

Handoffs are a peer-to-peer routing pattern where agents **hand off the entire conversation** to another agent. Unlike delegation, there is no sub-session — the conversation stays in a single session and the active agent simply switches.

This pattern is ideal for:

- **Pipeline workflows** — data flows through a chain of specialized agents
- **Conversational routing** — a coordinator routes the user to the right specialist, who can route back when done
- **Graph topologies** — agents can form cycles (A → B → C → A), enabling iterative workflows

### How It Works

1. **User** sends a message to the starting agent
2. **Agent A** processes the message, then calls `handoff` to route to **Agent B**
3. **Agent B** becomes the active agent and sees the **full conversation history**
4. **Agent B** can respond, use its own tools, or hand off to another agent
5. This continues until an agent responds directly without handing off

```bash
# The handoff tool call looks like:
handoff(
  agent="summarizer"
)
```

<div class="callout callout-info" markdown="1">
<div class="callout-title">ℹ️ Scoped Handoff Targets
</div>
  <p>Each agent can only hand off to agents listed in its own <code>handoffs</code> array. The <code>handoff</code> tool is automatically injected — you don't need to add it manually.</p>

</div>

### Example

A coordinator routes to a researcher, who hands off to a summarizer, who returns to the coordinator:

```
Root ──→ Researcher ──→ Summarizer ──→ Root
```

```yaml
agents:
  root:
    model: anthropic/claude-sonnet-4-5
    description: Coordinator that routes queries
    instruction: |
      Route research queries to the researcher.
    handoffs:
      - researcher

  researcher:
    model: openai/gpt-4o
    description: Web researcher
    instruction: |
      Search the web, then hand off to the summarizer.
    toolsets:
      - type: mcp
        ref: docker:duckduckgo
    handoffs:
      - summarizer

  summarizer:
    model: openai/gpt-4o-mini
    description: Summarizes findings
    instruction: |
      Summarize the research results, then hand off
      back to root.
    handoffs:
      - root
```

<div class="callout callout-tip" markdown="1">
<div class="callout-title">💡 Full pipeline example
</div>
  <p>For a more complex handoff graph with branching and multiple processing stages, see <a href="https://github.com/docker/docker-agent/blob/main/examples/handoff.yaml"><code>examples/handoff.yaml</code></a>.</p>

</div>

## Deterministic Pipelines with `pipeline`

Some workflows do not need an LLM to decide what happens next. When you already know the exact sequence of steps — say, *recall memory → research → write → edit* — encoding that sequence in an LLM prompt wastes tokens and introduces non-determinism. The `pipeline` field runs the sequence programmatically instead.

A pipeline agent is a pure sequencer: the runtime walks the `pipeline` list in declared order, no step can be skipped, reordered, or retried by model choice. Each step runs in its own sub-session, and the output of step N is fed into step N+1.

### Step types

A step is either an **agent step** or a **tool step**:

- **Agent step** (`agent:` + `task:`) — runs an LLM agent in a sub-session with the given task.
- **Tool step** (`tool:` + `args:`) — calls a tool directly with no LLM. The tool must be available in the pipeline agent's own toolsets. Useful for fetching URLs, reading files, running shell commands, or looking up memories — any deterministic operation that doesn't need a model.

The two are mutually exclusive: a step has either `agent` or `tool`, never both.

### Template variables

Both `task` (agent steps) and string values in `args` (tool steps) support two template variables:

- `{{input}}` — the original user input to the pipeline. Available in every step.
- `{{output}}` — the output of the previous step. For the first step, this equals `{{input}}`.

If an agent step omits `task`, the previous step's output is forwarded to the agent verbatim.

### Example

```yaml
models:
  gpt:
    provider: openai
    model: gpt-4o-mini

agents:
  # Pipeline agent — no model, just a sequence of steps.
  content_pipeline:
    description: Recall → research → write → edit
    instruction: Runs a four-step content pipeline.
    toolsets:
      - type: memory
        path: ./pipeline_memory.db
    pipeline:
      # Tool step: recall relevant memories (no LLM).
      - tool: search_memories
        args:
          query: "{{input}}"

      # Agent step: research using the recalled context.
      - agent: researcher
        task: "Research the topic. Memory context:\n\n{{output}}\n\nTopic: {{input}}"

      # Agent step: write an article from the research.
      - agent: writer
        task: "Turn this research into an article:\n\n{{output}}"

      # Agent step with no task — previous output forwarded verbatim.
      - agent: editor

  researcher:
    model: gpt
    description: Research specialist
    instruction: Extract key facts and structure them.

  writer:
    model: gpt
    description: Content writer
    instruction: Produce a clear, engaging article.

  editor:
    model: gpt
    description: Copy editor
    instruction: Polish for clarity and grammar. Preserve meaning.
```

### Triggering a pipeline from another agent

A pipeline agent can be listed in another agent's `sub_agents`, which exposes it via `transfer_task`. An LLM coordinator can then dispatch to the pipeline the same way it dispatches to any specialist — but once the pipeline is running, step order is fixed by the runtime, not by the model:

```yaml
agents:
  root:
    model: gpt
    description: Content orchestrator
    sub_agents:
      - content_pipeline
    instruction: |
      When the user asks you to write or research something, call
      transfer_task with agent="content_pipeline" and pass the user's
      request verbatim as the task.
```

<div class="callout callout-info" markdown="1">
<div class="callout-title">ℹ️ Pipeline agents and models
</div>
  <p>A pipeline agent does not require a <code>model</code> field — the pipeline itself never invokes an LLM on behalf of the sequencer. You may still set one if you want the pipeline agent to own a model for session-title generation or similar ancillary use.</p>

</div>

<div class="callout callout-tip" markdown="1">
<div class="callout-title">💡 Full working example
</div>
  <p>See <a href="https://github.com/docker/docker-agent/blob/main/examples/pipeline.yaml"><code>examples/pipeline.yaml</code></a> for the complete runnable configuration.</p>

</div>

## Parallel Delegation with Background Agents

`transfer_task` is **sequential** — the coordinator waits for the sub-agent to finish before continuing. When you need to fan out work to multiple agents at the same time, use the `background_agents` toolset instead.

Add it to your coordinator's toolsets:

```yaml
agents:
  root:
    model: anthropic/claude-sonnet-4-0
    description: Research coordinator
    sub_agents: [researcher, analyst, writer]
    toolsets:
      - type: think
      - type: background_agents
```

The coordinator can then:

1. **Dispatch** several tasks at once with `run_background_agent` — each returns a task ID immediately
2. **Monitor** progress with `list_background_agents` or `view_background_agent`
3. **Collect** results once tasks complete
4. **Cancel** tasks that are no longer needed with `stop_background_agent`

```bash
# Start two tasks in parallel
run_background_agent(agent="researcher", task="Find recent papers on LLM agents")
run_background_agent(agent="analyst", task="Analyze our current architecture")

# Check on all tasks
list_background_agents()

# Read results when ready
view_background_agent(task_id="agent_task_abc123")
```

## External Sub-Agents from Registries

Sub-agents don't have to be defined locally — you can reference agents from OCI registries (such as the [Docker Agent Catalog](https://hub.docker.com/u/agentcatalog)) directly in your `sub_agents` list. This lets you compose teams using pre-built, shared agents without duplicating their configuration.

```yaml
agents:
  root:
    model: openai/gpt-4o
    description: Coordinator that delegates to local and catalog sub-agents
    instruction: |
      Delegate tasks to the most appropriate sub-agent.
    sub_agents:
      - local_helper
      - agentcatalog/pirate # pulled from registry automatically

  local_helper:
    model: openai/gpt-4o
    description: A local helper agent for simple tasks
    instruction: You are a helpful assistant.
```

External sub-agents are automatically named after their last path segment — for example, `agentcatalog/pirate` becomes `pirate`. You can also give them an explicit name using the `name:reference` syntax:

```yaml
    sub_agents:
      - my_pirate:agentcatalog/pirate  # available as "my_pirate"
      - reviewer:docker.io/myorg/review-agent:latest
```

<div class="callout callout-tip" markdown="1">
<div class="callout-title">💡 Tip
</div>
  <p>External sub-agents work with any OCI-compatible registry, not just the Docker Agent Catalog. See <a href="{{ '/concepts/distribution/' | relative_url }}">Agent Distribution</a> for more on registry references.</p>

</div>

## Example: Development Team

```yaml
agents:
  root:
    model: anthropic/claude-sonnet-4-0
    description: Technical lead coordinating development
    instruction: |
      You are a technical lead managing a development team.
      Analyze requests and delegate to the right specialist.
      Ensure quality by reviewing results before responding.
    sub_agents: [developer, reviewer, tester]
    toolsets:
      - type: think

  developer:
    model: anthropic/claude-sonnet-4-0
    description: Expert software developer
    instruction: |
      You are an expert developer. Write clean, efficient code
      and follow best practices.
    toolsets:
      - type: filesystem
      - type: shell
      - type: think

  reviewer:
    model: openai/gpt-4o
    description: Code review specialist
    instruction: |
      You review code for quality, security, and maintainability.
      Provide actionable feedback.
    toolsets:
      - type: filesystem

  tester:
    model: openai/gpt-4o
    description: Quality assurance engineer
    instruction: |
      You write tests and ensure software quality. Run tests
      and report results.
    toolsets:
      - type: shell
      - type: todo
```

## Example: Research Team

```yaml
agents:
  root:
    model: anthropic/claude-sonnet-4-0
    description: Research coordinator
    instruction: |
      Coordinate research tasks. Delegate web searches to
      the researcher and writing to the writer.
    sub_agents: [researcher, writer]
    toolsets:
      - type: think

  researcher:
    model: openai/gpt-4o
    description: Web researcher
    instruction: Search the web and gather information.
    toolsets:
      - type: mcp
        ref: docker:duckduckgo
      - type: memory
        path: ./research.db

  writer:
    model: anthropic/claude-sonnet-4-0
    description: Content writer
    instruction: Write clear, well-structured content.
    toolsets:
      - type: filesystem
```

## Multi-Model Teams

A key advantage of multi-agent systems is using different models for different roles — picking the best model for each job:

```yaml
models:
  fast:
    provider: openai
    model: gpt-5-mini
    temperature: 0.2 # precise

  creative:
    provider: openai
    model: gpt-4o
    temperature: 0.8 # creative

  local:
    provider: dmr
    model: ai/qwen3 # runs locally, no API cost

agents:
  analyst:
    model: fast # cheap and fast for analysis
  writer:
    model: creative # creative for content
  helper:
    model: local # free for simple tasks
```

## Shared Tools

Tools like `todo` can be shared between agents for collaborative task tracking:

```yaml
toolsets:
  - type: todo
    shared: true # all agents see the same todo list
```

## Best Practices

- **Keep agents focused** — Each agent should have a clear, narrow role
- **Write clear descriptions** — The coordinator uses descriptions to decide who to delegate to
- **Give minimal tools** — Only give each agent the tools it needs for its specific role
- **Use the think tool when needed** — For models without native reasoning, give coordinators the think tool so they reason about delegation. Models with built-in thinking (e.g., via `thinking_budget`) don't need it
- **Use the right model** — Use capable models for complex reasoning, cheap models for simple tasks
- **Choose the right pattern** — Use `sub_agents` for hierarchical task delegation, `handoffs` for conversational routing and agent graphs, `pipeline` for deterministic fixed-order workflows

<div class="callout callout-info" markdown="1">
<div class="callout-title">ℹ️ Beyond docker-agent
</div>
  <p>For interoperability with other agent frameworks, docker-agent supports the <a href="{{ '/features/a2a/' | relative_url }}">A2A protocol</a> and can expose agents via <a href="{{ '/features/mcp-mode/' | relative_url }}">MCP Mode</a>.</p>

</div>
