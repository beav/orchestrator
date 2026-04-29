# Design: Replace Claude Code with Goose

**Date:** 2026-04-29
**Status:** Proposed

## Summary

Replace Block's Claude Code CLI agent with Block's Goose (`github.com/aaif-goose/goose`) as the AI agent binary running inside orchestrator sandboxes. This involves changing installation, run script generation, MCP registration, transcript parsing, profile system integration, and configuration.

## Decisions

- **Container username stays `claude`** — avoids disruption to gjoll VMs, terraform configs, and hardcoded paths. The UNIX username is not tied to the agent identity.
- **Transcript parser is fully replaced** — goose's `stream-json` format differs from Claude Code's. No backward compatibility layer.
- **Generic provider/model config fields** — replace Vertex-specific and Anthropic-specific config with `agent_provider`, `agent_model`, `agent_api_key_file`.
- **Profile system adapted to goose equivalents** — `.goosehints` replaces `CLAUDE.md`, MCP servers registered via CLI flags instead of `claude mcp add`.
- **Task name as goose session name** — `--resume -n <taskName>` for session continuation.
- **Post-run artifacts copy `~/.config/goose/`** — replaces `~/.claude/` archival.

## 1. Configuration (`internal/config/config.go`)

### Fields removed

```go
APIProvider      string `yaml:"api_provider"`
VertexProjectID  string `yaml:"vertex_project_id"`
VertexRegion     string `yaml:"vertex_region"`
VertexModel      string `yaml:"vertex_model"`
GCPCredentialsFile string `yaml:"gcp_credentials_file"`
AnthropicKeyFile string `yaml:"anthropic_key_file"`
```

### Fields added

```go
AgentProvider   string `yaml:"agent_provider"`     // goose --provider (e.g., "anthropic", "google", "openai")
AgentModel      string `yaml:"agent_model"`         // goose --model (e.g., "claude-sonnet-4-5")
AgentAPIKeyFile string `yaml:"agent_api_key_file"`  // path to API key file on host
```

### Defaults

```go
AgentProvider:  "anthropic"
AgentModel:     "claude-sonnet-4-5"
AgentAPIKeyFile: "~/.anthropic/api_key"
```

### PodmanConfig update

```go
type PodmanConfig struct {
    Image          string
    AgentProvider  string
    AgentModel     string
    AgentAPIKeyFile string
    MCPPort        int
}
```

The `apiProviderConfig` struct in `task.go` is replaced by:

```go
type agentConfig struct {
    Provider string
    Model    string
}
```

## 2. Installation (`internal/sandbox/podman.go`)

### Install command

```bash
# Old
su - claude -c 'curl -fsSL https://claude.ai/install.sh | bash'

# New
su - claude -c 'curl -fsSL https://github.com/aaif-goose/goose/releases/download/stable/download_cli.sh | CONFIGURE=false bash'
```

### Directory setup

```bash
# Old
mkdir -p ~/workspace ~/.config/claude-code && cd ~/workspace && git init

# New
mkdir -p ~/workspace ~/.config/goose && cd ~/workspace && git init
```

### Credential setup

Replace `setupAnthropicCredentials` and `setupVertexCredentials` with a single `setupCredentials` method:

1. If `AgentAPIKeyFile` is set, copy it to `/home/claude/.agent/api_key` in the container.
2. Set permissions to `claude:claude`, mode 600.
3. The run script exports the appropriate env var based on provider.

Provider-to-env-var mapping in the run script:

| Provider | Env var |
|----------|---------|
| `anthropic` | `ANTHROPIC_API_KEY` |
| `google` | `GOOGLE_API_KEY` |
| `openai` | `OPENAI_API_KEY` |
| `openrouter` | `OPENROUTER_API_KEY` |
| (fallback) | `ANTHROPIC_API_KEY` |

Additional providers can be added to this mapping as needed. The env var name follows the pattern `<PROVIDER_UPPERCASE>_API_KEY`.

## 3. Run Script (`internal/cmd/task.go` — `buildRunScript`)

### New session

```bash
#!/bin/bash
source ~/.bashrc
export PATH="/home/claude/.local/bin:$PATH"
export <ENV_VAR>="$(cat ~/.agent/api_key)"
stdbuf -oL goose run \
  --provider <provider> --model <model> \
  --output-format stream-json \
  --system "$(cat ~/system-prompt.md)" \
  --with-streamable-http-extension http://localhost:19090/mcp \
  <PROFILE_MCP_FLAGS> \
  -n <task-name> \
  -t '<escaped task description>' \
  </dev/null | stdbuf -oL tee ~/transcript.jsonl
```

### Continue session

```bash
#!/bin/bash
source ~/.bashrc
export PATH="/home/claude/.local/bin:$PATH"
export <ENV_VAR>="$(cat ~/.agent/api_key)"
stdbuf -oL goose run \
  --provider <provider> --model <model> \
  --output-format stream-json \
  --system "$(cat ~/system-prompt.md)" \
  --with-streamable-http-extension http://localhost:19090/mcp \
  <PROFILE_MCP_FLAGS> \
  --resume -n <task-name> \
  -t '<escaped task description>' \
  </dev/null | stdbuf -oL tee -a ~/transcript.jsonl
```

### Key differences from Claude Code script

| Aspect | Claude Code | Goose |
|--------|------------|-------|
| Binary | `claude` | `goose run` |
| Permission bypass | `--dangerously-skip-permissions` | Not needed (headless by default) |
| Print mode | `-p` | Not needed |
| Verbose | `--verbose` | Not needed |
| Effort | `--effort max` | No equivalent |
| System prompt | `--append-system-prompt-file ~/system-prompt.md` | `--system "$(cat ~/system-prompt.md)"` |
| Output format | `--output-format stream-json` | `--output-format stream-json` |
| Continue | `--continue` | `--resume -n <taskName>` |
| Session name | Implicit | `-n <taskName>` (always) |
| Task text | Positional arg `'<text>'` | `-t '<text>'` |
| MCP servers | Pre-registered via `claude mcp add` | `--with-streamable-http-extension <url>` at runtime |

### `buildRunScript` signature change

```go
// Old
func buildRunScript(taskDescription string, continueSession bool, apiCfg *apiProviderConfig) string

// New
func buildRunScript(taskName, taskDescription string, continueSession bool, agentCfg *agentConfig, mcpExtensions []mcpExtension) string
```

Where `mcpExtension` is:

```go
type mcpExtension struct {
    Transport string // "http" or "stdio"
    Value     string // URL for http, command for stdio
}
```

The orchestrator's own MCP server is always included. Profile MCP extensions are appended.

## 4. MCP Registration

### Old approach (pre-registration)

```go
// task.go
mcpCmd := fmt.Sprintf("claude mcp add --transport http orchestrator %s --scope user", mcpURL)
runner.SSH(ctx, taskName, "bash", "-c", fmt.Sprintf("su - claude -c '%s'", mcpCmd))

// profile/apply.go
args = []string{"claude", "mcp", "add", "--transport", "stdio", name, command...}
args = []string{"claude", "mcp", "add", "--transport", "http", name, url}
```

### New approach (runtime flags)

MCP servers are passed as flags to `goose run`:

- HTTP/Streamable: `--with-streamable-http-extension <url>`
- Stdio: `--with-extension "<command>"`

This means:
1. The `setupSandbox` function no longer runs `claude mcp add`.
2. Profile application returns a list of MCP extensions instead of registering them.
3. `buildRunScript` accepts the extension list and generates the flags.

### Profile MCP changes (`profile/apply.go`)

The `Apply` function signature changes to return MCP extension info:

```go
// Old
func Apply(ctx context.Context, runner sandbox.Runner, sbx string, prof *Profile, basePrompt string) error

// New
func Apply(ctx context.Context, runner sandbox.Runner, sbx string, prof *Profile, basePrompt string) ([]mcpExtension, error)
```

Instead of running `claude mcp add`, it collects extensions and returns them to the caller.

## 5. Profile System (`internal/profile/apply.go`)

### System prompt placement

```
# Old: ~/.claude/CLAUDE.md (auto-read by Claude Code)
# New: ~/workspace/.goosehints (auto-read by goose from CWD)
```

The profile's CLAUDE.md content + `prompts.Base` is written to `~/workspace/.goosehints`.

### Settings

`~/.claude/settings.json` is dropped — no goose equivalent needed.

### MCP servers

As described in Section 4, MCP registrations are returned as data instead of executed as commands.

### setup.sh

Unchanged — runs arbitrary setup commands in the sandbox.

## 6. Transcript Parser (`internal/cmd/log.go`)

### Goose stream-json format

Each JSONL line is a `StreamEvent` (top-level `type` field, snake_case):

```json
{"type": "message", "message": {"role": "assistant", "created": 1234567890, "content": [...]}}
{"type": "notification", "extension_id": "...", ...}
{"type": "error", "error": "something went wrong"}
{"type": "complete", "total_tokens": 12345}
```

Message content items use `camelCase` type tags:

| Content type | Fields |
|-------------|--------|
| `text` | `text` |
| `toolRequest` | `id`, `toolCall.value.name`, `toolCall.value.arguments` |
| `toolResponse` | `id`, `toolResult.value.content`, `toolResult.value.isError` |
| `thinking` | `thinking`, `signature` |
| `redactedThinking` | `data` |
| `systemNotification` | `notificationType`, `msg` |

### Parser struct changes

```go
// Old
struct {
    Type    string `json:"type"`
    Subtype string `json:"subtype"`
    Message struct {
        Content []struct { Type, Text, Name string; Input, Content json.RawMessage; Thinking string }
    }
    DurationMS, NumTurns int
    TotalCostUSD float64
}

// New
type streamEvent struct {
    Type        string   `json:"type"`         // "message", "error", "complete", "notification"
    Message     *message `json:"message"`
    Error       string   `json:"error"`
    TotalTokens *int     `json:"total_tokens"`
}

type message struct {
    Role    string           `json:"role"`
    Content []messageContent `json:"content"`
}

type messageContent struct {
    Type       string          `json:"type"` // "text", "toolRequest", "toolResponse", "thinking"
    Text       string          `json:"text"`
    Thinking   string          `json:"thinking"`
    ID         string          `json:"id"`
    ToolCall   json.RawMessage `json:"toolCall"`
    ToolResult json.RawMessage `json:"toolResult"`
}
```

### `formatTranscriptLine` rewrite

```
StreamEvent type "message":
  role "assistant":
    content "text"        → print text
    content "toolRequest" → print tool name + args summary
    content "thinking"    → print thinking (verbose only)
  role "user":
    content "toolResponse" → print first line of result (200 char max)
    
StreamEvent type "complete":
  → print total_tokens

StreamEvent type "error":
  → print error message
  
StreamEvent type "notification":
  → skip (or print in verbose mode)
```

### `toolInputSummary` adaptation

Tool name is at `toolCall.value.name`, arguments at `toolCall.value.arguments`. The summary logic for known tools (file paths, commands, patterns) stays similar but adapts to goose's tool naming conventions.

## 7. Post-run Artifacts (`internal/cmd/task.go`)

### Changes

```go
// Old
runner.Cp(ctx, taskName, taskName+":/home/claude/.claude/", filepath.Join(taskDir.Root(), "conversations"))

// New
runner.Cp(ctx, taskName, taskName+":/home/claude/.config/goose/", filepath.Join(taskDir.Root(), "conversations"))
```

`transcript.jsonl` copy is unchanged.

## 8. Container Environment

### Directory structure changes

| Old | New | Purpose |
|-----|-----|---------|
| `~/.config/claude-code/` | `~/.config/goose/` | Agent config |
| `~/.anthropic/api_key` | `~/.agent/api_key` | API key |
| `~/.config/gcloud/application_default_credentials.json` | Removed | GCP ADC (no longer needed) |
| `~/.claude/CLAUDE.md` | `~/workspace/.goosehints` | System prompt for profiles |
| `~/.claude/settings.json` | Removed | Agent settings |
| `~/workspace/` | `~/workspace/` | Unchanged |
| `~/system-prompt.md` | `~/system-prompt.md` | Unchanged |
| `~/transcript.jsonl` | `~/transcript.jsonl` | Unchanged |
| `~/run-claude.sh` | `~/run-goose.sh` | Run script (renamed) |

## 9. Files Modified

| File | Change type |
|------|------------|
| `internal/config/config.go` | Replace API provider fields with agent fields |
| `internal/config/config_test.go` | Update test expectations |
| `internal/sandbox/sandbox.go` | Update `PodmanConfig` struct |
| `internal/sandbox/podman.go` | Change install, credentials, directories |
| `internal/cmd/task.go` | Rewrite `buildRunScript`, update `setupSandbox`, update MCP registration, update artifact copy |
| `internal/cmd/task_test.go` | Rewrite tests for new `buildRunScript` |
| `internal/cmd/log.go` | Rewrite `formatTranscriptLine` for goose format |
| `internal/profile/apply.go` | Return MCP extensions instead of registering, write `.goosehints` instead of `CLAUDE.md` |

## 10. Not Changed

- `internal/sandbox/gjoll_adapter.go` — interface unchanged
- `internal/gjoll/gjoll.go` — no agent-specific code
- `internal/task/task.go` — path helpers unchanged
- `internal/prompts/*.md` — prompt content unchanged (may need minor adjustments to references)
- `internal/mcp/server.go` — MCP server unchanged
- Container user `claude` — kept for backward compatibility
