package latest

import (
	"testing"

	"github.com/goccy/go-yaml"
	"github.com/stretchr/testify/require"
)

func TestToolset_Validate_LSP(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		config  string
		wantErr string
	}{
		{
			name: "valid lsp with command",
			config: `
version: "3"
agents:
  root:
    model: "openai/gpt-4"
    toolsets:
      - type: lsp
        command: gopls
`,
			wantErr: "",
		},
		{
			name: "lsp missing command",
			config: `
version: "3"
agents:
  root:
    model: "openai/gpt-4"
    toolsets:
      - type: lsp
`,
			wantErr: "lsp toolset requires a command to be set",
		},
		{
			name: "lsp with args",
			config: `
version: "3"
agents:
  root:
    model: "openai/gpt-4"
    toolsets:
      - type: lsp
        command: gopls
        args:
          - -remote=auto
`,
			wantErr: "",
		},
		{
			name: "lsp with env",
			config: `
version: "3"
agents:
  root:
    model: "openai/gpt-4"
    toolsets:
      - type: lsp
        command: gopls
        env:
          GOFLAGS: "-mod=vendor"
`,
			wantErr: "",
		},
		{
			name: "lsp with file_types",
			config: `
version: "5"
agents:
  root:
    model: "openai/gpt-4"
    toolsets:
      - type: lsp
        command: gopls
        file_types: [".go", ".mod"]
`,
			wantErr: "",
		},
		{
			name: "file_types on non-lsp toolset",
			config: `
version: "5"
agents:
  root:
    model: "openai/gpt-4"
    toolsets:
      - type: shell
        file_types: [".go"]
`,
			wantErr: "file_types can only be used with type 'lsp'",
		},
		{
			name: "lsp with working_dir",
			config: `
version: "8"
agents:
  root:
    model: "openai/gpt-4"
    toolsets:
      - type: lsp
        command: gopls
        working_dir: ./backend
`,
			wantErr: "",
		},
		{
			name: "working_dir on non-mcp-lsp toolset is rejected",
			config: `
version: "8"
agents:
  root:
    model: "openai/gpt-4"
    toolsets:
      - type: shell
        working_dir: ./backend
`,
			wantErr: "working_dir can only be used with type 'mcp' or 'lsp'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var cfg Config
			err := yaml.Unmarshal([]byte(tt.config), &cfg)

			if tt.wantErr != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.wantErr)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestToolset_Validate_MCP_WorkingDir(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		config    string
		wantErr   string
		wantValue string
	}{
		{
			name: "mcp with working_dir",
			config: `
version: "8"
agents:
  root:
    model: "openai/gpt-4"
    toolsets:
      - type: mcp
        command: my-mcp-server
        working_dir: ./tools/mcp
`,
			wantErr:   "",
			wantValue: "./tools/mcp",
		},
		{
			name: "mcp without working_dir defaults to empty",
			config: `
version: "8"
agents:
  root:
    model: "openai/gpt-4"
    toolsets:
      - type: mcp
        command: my-mcp-server
`,
			wantErr:   "",
			wantValue: "",
		},
		{
			name: "working_dir on remote mcp is rejected",
			config: `
version: "8"
agents:
  root:
    model: "openai/gpt-4"
    toolsets:
      - type: mcp
        remote:
          url: https://mcp.example.com/sse
        working_dir: ./tools
`,
			wantErr:   "working_dir is not valid for remote MCP toolsets",
			wantValue: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var cfg Config
			err := yaml.Unmarshal([]byte(tt.config), &cfg)

			if tt.wantErr != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.wantErr)
			} else {
				require.NoError(t, err)
				require.Equal(t, tt.wantValue, cfg.Agents.First().Toolsets[0].WorkingDir)
			}
		})
	}
}

func TestValidatePipeline(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		config  string
		wantErr string
	}{
		{
			name: "valid mixed agent and tool steps",
			config: `
version: "8"
agents:
  pipe:
    description: pipe
    instruction: noop
    pipeline:
      - tool: search_memories
        args:
          query: "{{input}}"
      - agent: writer
        task: "Write: {{output}}"
      - agent: editor
  writer:
    model: openai/gpt-4o-mini
    description: writer
    instruction: noop
  editor:
    model: openai/gpt-4o-mini
    description: editor
    instruction: noop
`,
			wantErr: "",
		},
		{
			name: "pipeline and sub_agents are mutually exclusive",
			config: `
version: "8"
agents:
  root:
    model: openai/gpt-4o-mini
    description: root
    instruction: noop
    sub_agents: [worker]
    pipeline:
      - agent: worker
  worker:
    model: openai/gpt-4o-mini
    description: worker
    instruction: noop
`,
			wantErr: `agent "root": pipeline and sub_agents are mutually exclusive`,
		},
		{
			name: "pipeline and handoffs are mutually exclusive",
			config: `
version: "8"
agents:
  root:
    model: openai/gpt-4o-mini
    description: root
    instruction: noop
    handoffs: [worker]
    pipeline:
      - agent: worker
  worker:
    model: openai/gpt-4o-mini
    description: worker
    instruction: noop
`,
			wantErr: `agent "root": pipeline and handoffs are mutually exclusive`,
		},
		{
			name: "step missing both agent and tool",
			config: `
version: "8"
agents:
  pipe:
    description: pipe
    instruction: noop
    pipeline:
      - task: "orphan"
`,
			wantErr: `agent "pipe": pipeline[0]: must specify either agent or tool`,
		},
		{
			name: "step sets both agent and tool",
			config: `
version: "8"
agents:
  pipe:
    description: pipe
    instruction: noop
    pipeline:
      - agent: writer
        tool: search_memories
  writer:
    model: openai/gpt-4o-mini
    description: writer
    instruction: noop
`,
			wantErr: `agent "pipe": pipeline[0]: agent and tool are mutually exclusive`,
		},
		{
			name: "tool step carrying task",
			config: `
version: "8"
agents:
  pipe:
    description: pipe
    instruction: noop
    pipeline:
      - tool: search_memories
        task: "this is wrong"
`,
			wantErr: `agent "pipe": pipeline[0]: task is not valid for tool steps (use args)`,
		},
		{
			name: "agent step carrying args",
			config: `
version: "8"
agents:
  pipe:
    description: pipe
    instruction: noop
    pipeline:
      - agent: writer
        args:
          foo: bar
  writer:
    model: openai/gpt-4o-mini
    description: writer
    instruction: noop
`,
			wantErr: `agent "pipe": pipeline[0]: args is not valid for agent steps (use task)`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var cfg Config
			err := yaml.Unmarshal([]byte(tt.config), &cfg)

			if tt.wantErr != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.wantErr)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
