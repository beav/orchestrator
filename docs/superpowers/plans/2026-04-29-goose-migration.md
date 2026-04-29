# Goose Migration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace Claude Code with Goose as the AI agent binary in orchestrator sandboxes.

**Architecture:** Six source files are modified to swap Claude Code CLI integration for Goose equivalents. Config fields are generalized (agent_provider/agent_model/agent_api_key_file), the podman sandbox installs goose instead of claude, the run script calls `goose run` with provider/model/MCP flags, the transcript parser handles goose's stream-json format, and the profile system writes `.goosehints` and returns MCP extensions as data instead of running `claude mcp add`.

**Tech Stack:** Go, Cobra CLI, Podman containers, Goose CLI, MCP protocol

**Spec:** `docs/superpowers/specs/2026-04-29-goose-migration-design.md`

---

### Task 1: Update Configuration (`internal/config/config.go` + `internal/config/config_test.go`)

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`

- [ ] **Step 1: Write the failing test for new agent config fields**

In `internal/config/config_test.go`, replace the "vertex config parsed" test case with an "agent config parsed" test case and update all existing test cases to use the new field names. Replace this test case:

```go
		{
			name:      "vertex config parsed",
			writeFile: true,
			yaml:      "api_provider: vertex\nvertex_project_id: my-project\nvertex_region: us-east5\nvertex_model: claude-opus-4-6\ngcp_credentials_file: /path/to/creds.json\n",
			want: Config{
				OutputDir:          "./tasks",
				SandboxBackend:     "gjoll",
				GjollEnv:           "./configs/sandbox.tf",
				PodmanImage:        "fedora:43",
				AnthropicKeyFile:   "~/.anthropic/api_key",
				APIProvider:        "vertex",
				VertexProjectID:    "my-project",
				VertexRegion:       "us-east5",
				VertexModel:        "claude-opus-4-6",
				GCPCredentialsFile: "/path/to/creds.json",
			},
		},
```

With:

```go
		{
			name:      "agent config parsed",
			writeFile: true,
			yaml:      "agent_provider: google\nagent_model: gemini-2.5-pro\nagent_api_key_file: /path/to/key\n",
			want: Config{
				OutputDir:       "./tasks",
				SandboxBackend:  "gjoll",
				GjollEnv:        "./configs/sandbox.tf",
				PodmanImage:     "fedora:43",
				AgentProvider:   "google",
				AgentModel:      "gemini-2.5-pro",
				AgentAPIKeyFile: "/path/to/key",
			},
		},
```

In all other test cases that reference the old fields, replace:
```go
				AnthropicKeyFile:   "~/.anthropic/api_key",
				APIProvider:        "anthropic",
				VertexModel:        "claude-sonnet-4-5",
				GCPCredentialsFile: "~/.config/gcloud/application_default_credentials.json",
```
With:
```go
				AgentProvider:   "anthropic",
				AgentModel:      "claude-sonnet-4-5",
				AgentAPIKeyFile: "~/.anthropic/api_key",
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config/ -v -run TestLoad`
Expected: Compilation failure — `Config` struct doesn't have the new fields yet.

- [ ] **Step 3: Update the Config struct and defaults**

In `internal/config/config.go`, replace the old API provider fields:

```go
	// Podman backend settings  
	PodmanImage      string `yaml:"podman_image"`       // container image (e.g. "fedora:43")
	AnthropicKeyFile string `yaml:"anthropic_key_file"` // path to API key for mounting
	
	// API provider: "anthropic" (direct API) or "vertex" (Google Vertex AI)
	APIProvider      string `yaml:"api_provider"`
	
	// Vertex AI settings (used when api_provider is "vertex")
	VertexProjectID    string `yaml:"vertex_project_id"`
	VertexRegion       string `yaml:"vertex_region"`
	VertexModel        string `yaml:"vertex_model"`          // e.g. "claude-sonnet-4-5"
	GCPCredentialsFile string `yaml:"gcp_credentials_file"`  // path to ADC JSON on host
```

With:

```go
	// Podman backend settings  
	PodmanImage     string `yaml:"podman_image"`        // container image (e.g. "fedora:43")

	// Agent settings (passed to goose CLI)
	AgentProvider   string `yaml:"agent_provider"`      // goose --provider (e.g., "anthropic", "google", "openai")
	AgentModel      string `yaml:"agent_model"`          // goose --model (e.g., "claude-sonnet-4-5")
	AgentAPIKeyFile string `yaml:"agent_api_key_file"`   // path to API key file on host
```

Replace the old defaults block:

```go
	if cfg.AnthropicKeyFile == "" {
		cfg.AnthropicKeyFile = "~/.anthropic/api_key"
	}
	if cfg.APIProvider == "" {
		cfg.APIProvider = "anthropic"
	}
	if cfg.VertexModel == "" {
		cfg.VertexModel = "claude-sonnet-4-5"
	}
	if cfg.GCPCredentialsFile == "" {
		cfg.GCPCredentialsFile = "~/.config/gcloud/application_default_credentials.json"
	}
```

With:

```go
	if cfg.AgentProvider == "" {
		cfg.AgentProvider = "anthropic"
	}
	if cfg.AgentModel == "" {
		cfg.AgentModel = "claude-sonnet-4-5"
	}
	if cfg.AgentAPIKeyFile == "" {
		cfg.AgentAPIKeyFile = "~/.anthropic/api_key"
	}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/config/ -v -run TestLoad`
Expected: All tests PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "config: replace Claude-specific API fields with generic agent fields

Replace api_provider, vertex_*, anthropic_key_file, gcp_credentials_file
with agent_provider, agent_model, agent_api_key_file for goose migration."
```

---

### Task 2: Update PodmanConfig and PodmanRunner (`internal/sandbox/sandbox.go` + `internal/sandbox/podman.go`)

**Files:**
- Modify: `internal/sandbox/sandbox.go`
- Modify: `internal/sandbox/podman.go`

- [ ] **Step 1: Update PodmanConfig struct**

In `internal/sandbox/sandbox.go`, replace:

```go
// PodmanConfig holds configuration for the podman sandbox backend.
type PodmanConfig struct {
	Image              string
	AnthropicKeyFile   string
	APIProvider        string // "anthropic" or "vertex"
	VertexProjectID    string
	VertexRegion       string
	VertexModel        string
	GCPCredentialsFile string
	MCPPort            int
}
```

With:

```go
// PodmanConfig holds configuration for the podman sandbox backend.
type PodmanConfig struct {
	Image           string
	AgentProvider   string // goose --provider (e.g., "anthropic", "google")
	AgentModel      string // goose --model
	AgentAPIKeyFile string // path to API key file on host
	MCPPort         int
}
```

- [ ] **Step 2: Rewrite PodmanRunner struct and NewPodman**

In `internal/sandbox/podman.go`, replace:

```go
// PodmanRunner implements Runner using podman containers as sandboxes.
type PodmanRunner struct {
	image              string
	anthropicKey       string
	apiProvider        string // "anthropic" or "vertex"
	vertexProjectID    string
	vertexRegion       string
	vertexModel        string
	gcpCredentialsFile string
	mcpServerPort      int
}

// NewPodman creates a PodmanRunner from a PodmanConfig.
func NewPodman(cfg *PodmanConfig) *PodmanRunner {
	image := cfg.Image
	if image == "" {
		image = "fedora:43"
	}
	apiProvider := cfg.APIProvider
	if apiProvider == "" {
		apiProvider = "anthropic"
	}
	return &PodmanRunner{
		image:              image,
		anthropicKey:       cfg.AnthropicKeyFile,
		apiProvider:        apiProvider,
		vertexProjectID:    cfg.VertexProjectID,
		vertexRegion:       cfg.VertexRegion,
		vertexModel:        cfg.VertexModel,
		gcpCredentialsFile: cfg.GCPCredentialsFile,
		mcpServerPort:      cfg.MCPPort,
	}
}
```

With:

```go
// PodmanRunner implements Runner using podman containers as sandboxes.
type PodmanRunner struct {
	image           string
	agentProvider   string
	agentModel      string
	agentAPIKeyFile string
	mcpServerPort   int
}

// NewPodman creates a PodmanRunner from a PodmanConfig.
func NewPodman(cfg *PodmanConfig) *PodmanRunner {
	image := cfg.Image
	if image == "" {
		image = "fedora:43"
	}
	return &PodmanRunner{
		image:           image,
		agentProvider:   cfg.AgentProvider,
		agentModel:      cfg.AgentModel,
		agentAPIKeyFile: cfg.AgentAPIKeyFile,
		mcpServerPort:   cfg.MCPPort,
	}
}
```

- [ ] **Step 3: Replace install command and directory setup in Up method**

In `internal/sandbox/podman.go`, in the `Up` method, replace:

```go
	// Install Claude Code as the claude user
	installCmds := []string{
		"bash", "-c",
		"su - claude -c 'curl -fsSL https://claude.ai/install.sh | bash'",
	}
	if err := r.SSH(ctx, name, installCmds...); err != nil {
		_ = r.Down(context.Background(), name)
		return fmt.Errorf("claude install: %w", err)
	}
```

With:

```go
	// Install goose as the claude user
	installCmds := []string{
		"bash", "-c",
		"su - claude -c 'curl -fsSL https://github.com/aaif-goose/goose/releases/download/stable/download_cli.sh | CONFIGURE=false bash'",
	}
	if err := r.SSH(ctx, name, installCmds...); err != nil {
		_ = r.Down(context.Background(), name)
		return fmt.Errorf("goose install: %w", err)
	}
```

Replace the directory setup:

```go
	// Configure environment as claude user
	configCmds := []string{
		"bash", "-c",
		"su - claude -c 'mkdir -p ~/workspace ~/.config/claude-code && cd ~/workspace && git init'",
	}
```

With:

```go
	// Configure environment as claude user
	configCmds := []string{
		"bash", "-c",
		"su - claude -c 'mkdir -p ~/workspace ~/.config/goose && cd ~/workspace && git init'",
	}
```

- [ ] **Step 4: Replace credential setup methods**

In `internal/sandbox/podman.go`, replace the entire `setupCredentials`, `setupAnthropicCredentials`, `setupVertexCredentials`, and the accessor methods (`APIProvider`, `VertexProjectID`, `VertexRegion`, `VertexModel`) with:

```go
// setupCredentials copies the agent API key file into the container.
func (r *PodmanRunner) setupCredentials(ctx context.Context, name string) error {
	if r.agentAPIKeyFile == "" {
		return nil
	}

	keyPath := r.resolveHomePath(r.agentAPIKeyFile)

	// Create .agent directory and copy key
	mkdirCmd := []string{"bash", "-c", "mkdir -p /home/claude/.agent && chown claude:claude /home/claude/.agent"}
	if err := r.SSH(ctx, name, mkdirCmd...); err != nil {
		return fmt.Errorf("creating .agent dir: %w", err)
	}

	if err := r.Cp(ctx, name, keyPath, name+":/home/claude/.agent/api_key"); err != nil {
		return fmt.Errorf("copying API key: %w", err)
	}

	chownCmd := []string{"bash", "-c", "chown claude:claude /home/claude/.agent/api_key && chmod 600 /home/claude/.agent/api_key"}
	if err := r.SSH(ctx, name, chownCmd...); err != nil {
		return fmt.Errorf("fixing API key permissions: %w", err)
	}

	return nil
}
```

Keep the `resolveHomePath` method unchanged.

- [ ] **Step 5: Verify compilation**

Run: `go build ./internal/sandbox/`
Expected: Compilation failure (callers in `cmd/task.go` and `cmd/log.go` still reference old fields). This is expected — those will be fixed in later tasks.

- [ ] **Step 6: Commit**

```bash
git add internal/sandbox/sandbox.go internal/sandbox/podman.go
git commit -m "sandbox: replace Claude Code with goose install and generic credentials

Install goose instead of Claude Code, use ~/.agent/api_key for credential
storage, replace Vertex/Anthropic-specific credential setup with a single
generic method, and create ~/.config/goose/ instead of ~/.config/claude-code/."
```

---

### Task 3: Rewrite Run Script and MCP Registration (`internal/cmd/task.go`)

**Files:**
- Modify: `internal/cmd/task.go`

- [ ] **Step 1: Replace apiProviderConfig with agentConfig and mcpExtension types**

In `internal/cmd/task.go`, replace:

```go
// apiProviderConfig holds the settings needed by buildRunScript to configure
// the correct environment variables for the chosen API provider.
type apiProviderConfig struct {
	Provider        string // "anthropic" or "vertex"
	VertexProjectID string
	VertexRegion    string
	VertexModel     string
}
```

With:

```go
// agentConfig holds the provider/model settings for the goose run script.
type agentConfig struct {
	Provider string // goose --provider (e.g., "anthropic", "google", "openai")
	Model    string // goose --model
}

// mcpExtension represents an MCP server to pass to goose at runtime.
type mcpExtension struct {
	Transport string // "http" or "stdio"
	Value     string // URL for http, command string for stdio
}
```

- [ ] **Step 2: Rewrite buildRunScript**

Replace the entire `buildRunScript` function:

```go
func buildRunScript(taskDescription string, continueSession bool, apiCfg *apiProviderConfig) string {
	escapedDesc := strings.ReplaceAll(taskDescription, "'", "'\\''")

	var claudeFlags string
	var teeFlag string
	if continueSession {
		claudeFlags = "--continue"
		teeFlag = "-a"
	}

	var envBlock string
	if apiCfg != nil && apiCfg.Provider == "vertex" {
		envBlock = fmt.Sprintf(`export CLAUDE_CODE_USE_VERTEX=1
export CLOUD_ML_REGION="%s"
export ANTHROPIC_VERTEX_PROJECT_ID="%s"
export ANTHROPIC_MODEL="%s"
export GOOGLE_APPLICATION_CREDENTIALS="/home/claude/.config/gcloud/application_default_credentials.json"`,
			apiCfg.VertexRegion, apiCfg.VertexProjectID, apiCfg.VertexModel)
	} else {
		envBlock = `export ANTHROPIC_API_KEY="$(cat ~/.anthropic/api_key)"`
	}

	return fmt.Sprintf(`#!/bin/bash
source ~/.bashrc
export PATH="/home/claude/.local/bin:$PATH"
%s
stdbuf -oL claude --dangerously-skip-permissions -p --verbose \
  --effort max \
  --output-format stream-json --append-system-prompt-file ~/system-prompt.md \
  %s '%s' \
  </dev/null | stdbuf -oL tee %s ~/transcript.jsonl
`, envBlock, claudeFlags, escapedDesc, teeFlag)
}
```

With:

```go
// providerEnvVar returns the environment variable name for the given provider's API key.
func providerEnvVar(provider string) string {
	switch provider {
	case "google":
		return "GOOGLE_API_KEY"
	case "openai":
		return "OPENAI_API_KEY"
	case "openrouter":
		return "OPENROUTER_API_KEY"
	default:
		return "ANTHROPIC_API_KEY"
	}
}

func buildRunScript(taskName, taskDescription string, continueSession bool, agentCfg *agentConfig, mcpExts []mcpExtension) string {
	escapedDesc := strings.ReplaceAll(taskDescription, "'", "'\\''")

	var resumeFlag string
	var teeFlag string
	if continueSession {
		resumeFlag = "--resume"
		teeFlag = "-a"
	}

	provider := "anthropic"
	model := "claude-sonnet-4-5"
	if agentCfg != nil {
		if agentCfg.Provider != "" {
			provider = agentCfg.Provider
		}
		if agentCfg.Model != "" {
			model = agentCfg.Model
		}
	}

	envVar := providerEnvVar(provider)

	// Build MCP extension flags
	var mcpFlags string
	for _, ext := range mcpExts {
		switch ext.Transport {
		case "http":
			mcpFlags += fmt.Sprintf(" \\\n  --with-streamable-http-extension '%s'", ext.Value)
		case "stdio":
			mcpFlags += fmt.Sprintf(" \\\n  --with-extension '%s'", ext.Value)
		}
	}

	return fmt.Sprintf(`#!/bin/bash
source ~/.bashrc
export PATH="/home/claude/.local/bin:$PATH"
export %s="$(cat ~/.agent/api_key)"
stdbuf -oL goose run \
  --provider %s --model %s \
  --output-format stream-json \
  --system "$(cat ~/system-prompt.md)"%s \
  %s -n '%s' \
  -t '%s' \
  </dev/null | stdbuf -oL tee %s ~/transcript.jsonl
`, envVar, provider, model, mcpFlags, resumeFlag, taskName, escapedDesc, teeFlag)
}
```

- [ ] **Step 3: Update executeTask to use new types and pass task name**

In `internal/cmd/task.go`, in `executeTask`, replace:

```go
	// Build the Claude run script
	slog.Info("Running Claude", "task", taskName)
	apiCfg := &apiProviderConfig{
		Provider:        cfg.APIProvider,
		VertexProjectID: cfg.VertexProjectID,
		VertexRegion:    cfg.VertexRegion,
		VertexModel:     cfg.VertexModel,
	}
	runScript := buildRunScript(taskDescription, continueSession, apiCfg)
```

With:

```go
	// Build the goose run script
	slog.Info("Running goose", "task", taskName)
	agentCfg := &agentConfig{
		Provider: cfg.AgentProvider,
		Model:    cfg.AgentModel,
	}

	// Collect MCP extensions: orchestrator is always included
	mcpURL := fmt.Sprintf("http://localhost:%d/mcp", mcpserver.MCPRemotePort)
	mcpExts := []mcpExtension{{Transport: "http", Value: mcpURL}}

	// Add profile MCP extensions if available
	if !continueSession {
		profileExts, err := setupSandbox(ctx, runner, taskName, taskDir, cfg, profileName, profileVars)
		if err != nil {
			return fmt.Errorf("setting up sandbox: %w", err)
		}
		slog.Debug("Sandbox setup complete", "task", taskName)
		mcpExts = append(mcpExts, profileExts...)
	}

	runScript := buildRunScript(taskName, taskDescription, continueSession, agentCfg, mcpExts)
```

Also remove the old sandbox setup block that was separate:

```go
	if !continueSession {
		if err := setupSandbox(ctx, runner, taskName, taskDir, cfg, profileName, profileVars); err != nil {
			return fmt.Errorf("setting up sandbox: %w", err)
		}
		slog.Debug("Sandbox setup complete", "task", taskName)
	}
```

This block is now integrated into the MCP extension collection above.

- [ ] **Step 4: Update run script file naming**

Replace:

```go
	tmpRun, err := os.CreateTemp("", "run-claude-*.sh")
```

With:

```go
	tmpRun, err := os.CreateTemp("", "run-goose-*.sh")
```

Replace:

```go
	if err := runner.Cp(ctx, taskName, tmpRun.Name(), taskName+":/home/claude/run-claude.sh"); err != nil {
		return fmt.Errorf("copying run script: %w", err)
	}
	if err := runner.SSH(ctx, taskName, "bash", "-c", "chown claude:claude /home/claude/run-claude.sh && chmod +x /home/claude/run-claude.sh"); err != nil {
		return fmt.Errorf("making run script executable: %w", err)
	}
```

With:

```go
	if err := runner.Cp(ctx, taskName, tmpRun.Name(), taskName+":/home/claude/run-goose.sh"); err != nil {
		return fmt.Errorf("copying run script: %w", err)
	}
	if err := runner.SSH(ctx, taskName, "bash", "-c", "chown claude:claude /home/claude/run-goose.sh && chmod +x /home/claude/run-goose.sh"); err != nil {
		return fmt.Errorf("making run script executable: %w", err)
	}
```

Replace:

```go
	if err := runner.SSHProxyOutput(ctx, taskName, w, sshOpts, "bash", "-c", "su - claude -c /home/claude/run-claude.sh"); err != nil {
		slog.Error("Claude exited with error", "task", taskName, "error", err)
```

With:

```go
	if err := runner.SSHProxyOutput(ctx, taskName, w, sshOpts, "bash", "-c", "su - claude -c /home/claude/run-goose.sh"); err != nil {
		slog.Error("Goose exited with error", "task", taskName, "error", err)
```

Replace:

```go
	slog.Info("Claude finished", "task", taskName)
```

With:

```go
	slog.Info("Goose finished", "task", taskName)
```

- [ ] **Step 5: Update post-run artifact copy**

Replace:

```go
		slog.Debug("Copying conversations", "task", taskName)
		if copyErr := runner.Cp(context.Background(), taskName, taskName+":/home/claude/.claude/", taskDir.ConversationsPath()); copyErr != nil {
			slog.Warn("Failed to copy conversations", "task", taskName, "error", copyErr)
		}
```

With:

```go
		slog.Debug("Copying conversations", "task", taskName)
		if copyErr := runner.Cp(context.Background(), taskName, taskName+":/home/claude/.config/goose/", taskDir.ConversationsPath()); copyErr != nil {
			slog.Warn("Failed to copy conversations", "task", taskName, "error", copyErr)
		}
```

- [ ] **Step 6: Update createSandboxRunner**

Replace:

```go
func createSandboxRunner(cfg *config.Config) sandbox.Runner {
	slog.Info("Creating sandbox runner", "backend", cfg.SandboxBackend)
	podmanCfg := &sandbox.PodmanConfig{
		Image:              cfg.PodmanImage,
		AnthropicKeyFile:   cfg.AnthropicKeyFile,
		APIProvider:        cfg.APIProvider,
		VertexProjectID:    cfg.VertexProjectID,
		VertexRegion:       cfg.VertexRegion,
		VertexModel:        cfg.VertexModel,
		GCPCredentialsFile: cfg.GCPCredentialsFile,
		MCPPort:            mcpserver.MCPRemotePort,
	}
	return sandbox.NewFromConfig(cfg.SandboxBackend, cfg.GjollEnv, podmanCfg)
}
```

With:

```go
func createSandboxRunner(cfg *config.Config) sandbox.Runner {
	slog.Info("Creating sandbox runner", "backend", cfg.SandboxBackend)
	podmanCfg := &sandbox.PodmanConfig{
		Image:           cfg.PodmanImage,
		AgentProvider:   cfg.AgentProvider,
		AgentModel:      cfg.AgentModel,
		AgentAPIKeyFile: cfg.AgentAPIKeyFile,
		MCPPort:         mcpserver.MCPRemotePort,
	}
	return sandbox.NewFromConfig(cfg.SandboxBackend, cfg.GjollEnv, podmanCfg)
}
```

- [ ] **Step 7: Update setupSandbox to return MCP extensions and remove claude mcp add**

Replace:

```go
func setupSandbox(ctx context.Context, runner sandbox.Runner, taskName string, taskDir *task.Dir, cfg *config.Config, profileName string, profileVars []string) error {
	// Always: configure git (run as claude user)
	if err := runner.SSH(ctx, taskName, "bash", "-c", "su - claude -c 'git config --global user.name Drellabot'"); err != nil {
		return fmt.Errorf("git config user.name: %w", err)
	}
	if err := runner.SSH(ctx, taskName, "bash", "-c", "su - claude -c 'git config --global user.email imagebuilder-bots+drella@redhat.com'"); err != nil {
		return fmt.Errorf("git config user.email: %w", err)
	}

	// Always: register orchestrator MCP server
	mcpURL := fmt.Sprintf("http://localhost:%d/mcp", mcpserver.MCPRemotePort)
	mcpCmd := fmt.Sprintf("claude mcp add --transport http orchestrator %s --scope user", mcpURL)
	if err := runner.SSH(ctx, taskName, "bash", "-c", fmt.Sprintf("su - claude -c '%s'", mcpCmd)); err != nil {
		return fmt.Errorf("registering MCP server: %w", err)
	}

	if profileName != "" {
		return setupSandboxWithProfile(ctx, runner, taskName, taskDir, cfg, profileName, profileVars)
	}
	return setupSandboxDefault(ctx, runner, taskName)
}
```

With:

```go
func setupSandbox(ctx context.Context, runner sandbox.Runner, taskName string, taskDir *task.Dir, cfg *config.Config, profileName string, profileVars []string) ([]mcpExtension, error) {
	// Always: configure git (run as claude user)
	if err := runner.SSH(ctx, taskName, "bash", "-c", "su - claude -c 'git config --global user.name Drellabot'"); err != nil {
		return nil, fmt.Errorf("git config user.name: %w", err)
	}
	if err := runner.SSH(ctx, taskName, "bash", "-c", "su - claude -c 'git config --global user.email imagebuilder-bots+drella@redhat.com'"); err != nil {
		return nil, fmt.Errorf("git config user.email: %w", err)
	}

	if profileName != "" {
		return setupSandboxWithProfile(ctx, runner, taskName, taskDir, cfg, profileName, profileVars)
	}
	err := setupSandboxDefault(ctx, runner, taskName)
	return nil, err
}
```

- [ ] **Step 8: Update setupSandboxWithProfile to return MCP extensions and write .goosehints**

Replace:

```go
// setupSandboxWithProfile applies a profile's configuration to the sandbox.
func setupSandboxWithProfile(ctx context.Context, runner sandbox.Runner, taskName string, taskDir *task.Dir, cfg *config.Config, profileName string, profileVars []string) error {
	profileSource, cleanup, err := resolveProfileSource(ctx, cfg)
	if err != nil {
		return err
	}
	if cleanup != nil {
		defer cleanup()
	}

	p, err := profile.Load(profileSource, profileName)
	if err != nil {
		return fmt.Errorf("loading profile: %w", err)
	}

	slog.Info("Applying profile", "profile", profileName, "task", taskName)

	vars := parseVarFlags(profileVars)
	if err := profile.Apply(ctx, p, runner, taskName, taskDir.Path(), prompts.Base, vars); err != nil {
		return fmt.Errorf("applying profile: %w", err)
	}

	// Write the base prompt as system-prompt.md for --append-system-prompt-file
	// (profile CLAUDE.md is in ~/.claude/CLAUDE.md, picked up automatically)
	tmpFile, err := os.CreateTemp("", "prompt-*.md")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.WriteString(prompts.OnInit); err != nil {
		tmpFile.Close()
		return fmt.Errorf("writing prompt: %w", err)
	}
	tmpFile.Close()

	if err := runner.Cp(ctx, taskName, tmpFile.Name(), ":~/system-prompt.md"); err != nil {
		return fmt.Errorf("copying system prompt: %w", err)
	}

	return nil
}
```

With:

```go
// setupSandboxWithProfile applies a profile's configuration to the sandbox and
// returns MCP extensions defined by the profile.
func setupSandboxWithProfile(ctx context.Context, runner sandbox.Runner, taskName string, taskDir *task.Dir, cfg *config.Config, profileName string, profileVars []string) ([]mcpExtension, error) {
	profileSource, cleanup, err := resolveProfileSource(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if cleanup != nil {
		defer cleanup()
	}

	p, err := profile.Load(profileSource, profileName)
	if err != nil {
		return nil, fmt.Errorf("loading profile: %w", err)
	}

	slog.Info("Applying profile", "profile", profileName, "task", taskName)

	vars := parseVarFlags(profileVars)
	profileExts, err := profile.Apply(ctx, p, runner, taskName, taskDir.Path(), prompts.Base, vars)
	if err != nil {
		return nil, fmt.Errorf("applying profile: %w", err)
	}

	// Write the base prompt as system-prompt.md for --system flag
	tmpFile, err := os.CreateTemp("", "prompt-*.md")
	if err != nil {
		return nil, fmt.Errorf("creating temp file: %w", err)
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.WriteString(prompts.OnInit); err != nil {
		tmpFile.Close()
		return nil, fmt.Errorf("writing prompt: %w", err)
	}
	tmpFile.Close()

	if err := runner.Cp(ctx, taskName, tmpFile.Name(), ":~/system-prompt.md"); err != nil {
		return nil, fmt.Errorf("copying system prompt: %w", err)
	}

	// Convert profile MCP extensions to our type
	var exts []mcpExtension
	for _, pe := range profileExts {
		exts = append(exts, mcpExtension{Transport: pe.Transport, Value: pe.Value})
	}

	return exts, nil
}
```

- [ ] **Step 9: Verify compilation (expect failure from profile package)**

Run: `go build ./internal/cmd/`
Expected: Compilation failure — `profile.Apply` signature hasn't been updated yet. This is expected — Task 5 will fix it.

- [ ] **Step 10: Commit**

```bash
git add internal/cmd/task.go
git commit -m "cmd/task: rewrite run script for goose and pass MCP extensions at runtime

Replace buildRunScript to generate goose run commands with --provider,
--model, --system, and --with-streamable-http-extension flags. MCP servers
are now passed as runtime flags instead of pre-registered via claude mcp add.
setupSandbox returns MCP extension list for the run script."
```

---

### Task 4: Rewrite Run Script Tests (`internal/cmd/task_test.go`)

**Files:**
- Modify: `internal/cmd/task_test.go`

- [ ] **Step 1: Rewrite TestBuildRunScript for goose**

Replace the entire `TestBuildRunScript` function:

```go
func TestBuildRunScript(t *testing.T) {
	anthropicCfg := &apiProviderConfig{Provider: "anthropic"}
	vertexCfg := &apiProviderConfig{
		Provider:        "vertex",
		VertexProjectID: "my-project",
		VertexRegion:    "us-east5",
		VertexModel:     "claude-opus-4-6",
	}

	tests := []struct {
		name            string
		taskDescription string
		continueSession bool
		apiCfg          *apiProviderConfig
		wantContains    []string
		wantNotContains []string
	}{
		{
			name:            "new session",
			taskDescription: "Fix the bug in handler.go",
			continueSession: false,
			apiCfg:          anthropicCfg,
			wantContains: []string{
				"#!/bin/bash",
				"source ~/.bashrc",
				"--dangerously-skip-permissions",
				"--effort max",
				"--output-format stream-json",
				"--append-system-prompt-file ~/system-prompt.md",
				"'Fix the bug in handler.go'",
				"tee  ~/transcript.jsonl",
				"ANTHROPIC_API_KEY",
			},
			wantNotContains: []string{
				"--continue",
				"tee -a",
				"cd ~/project",
				"CLAUDE_CODE_USE_VERTEX",
			},
		},
		{
			name:            "continue session",
			taskDescription: "Also fix the tests",
			continueSession: true,
			apiCfg:          anthropicCfg,
			wantContains: []string{
				"--continue",
				"--append-system-prompt-file ~/system-prompt.md",
				"'Also fix the tests'",
				"tee -a ~/transcript.jsonl",
			},
		},
		{
			name:            "description with single quotes",
			taskDescription: "Fix the 'bug' in handler.go",
			continueSession: false,
			apiCfg:          anthropicCfg,
			wantContains: []string{
				`'Fix the '\''bug'\'' in handler.go'`,
			},
		},
		{
			name:            "vertex provider",
			taskDescription: "Fix the bug",
			continueSession: false,
			apiCfg:          vertexCfg,
			wantContains: []string{
				"#!/bin/bash",
				"CLAUDE_CODE_USE_VERTEX=1",
				`CLOUD_ML_REGION="us-east5"`,
				`ANTHROPIC_VERTEX_PROJECT_ID="my-project"`,
				`ANTHROPIC_MODEL="claude-opus-4-6"`,
				"GOOGLE_APPLICATION_CREDENTIALS=",
			},
			wantNotContains: []string{
				"ANTHROPIC_API_KEY",
			},
		},
		{
			name:            "nil apiCfg defaults to anthropic",
			taskDescription: "Fix something",
			continueSession: false,
			apiCfg:          nil,
			wantContains: []string{
				"ANTHROPIC_API_KEY",
			},
			wantNotContains: []string{
				"CLAUDE_CODE_USE_VERTEX",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildRunScript(tt.taskDescription, tt.continueSession, tt.apiCfg)
			for _, want := range tt.wantContains {
				if !strings.Contains(got, want) {
					t.Errorf("buildRunScript() missing %q\ngot:\n%s", want, got)
				}
			}
			for _, notWant := range tt.wantNotContains {
				if strings.Contains(got, notWant) {
					t.Errorf("buildRunScript() should not contain %q\ngot:\n%s", notWant, got)
				}
			}
		})
	}
}
```

With:

```go
func TestBuildRunScript(t *testing.T) {
	defaultCfg := &agentConfig{Provider: "anthropic", Model: "claude-sonnet-4-5"}
	googleCfg := &agentConfig{Provider: "google", Model: "gemini-2.5-pro"}
	orchestratorMCP := mcpExtension{Transport: "http", Value: "http://localhost:19090/mcp"}

	tests := []struct {
		name            string
		taskName        string
		taskDescription string
		continueSession bool
		agentCfg        *agentConfig
		mcpExts         []mcpExtension
		wantContains    []string
		wantNotContains []string
	}{
		{
			name:            "new session",
			taskName:        "fix-handler",
			taskDescription: "Fix the bug in handler.go",
			continueSession: false,
			agentCfg:        defaultCfg,
			mcpExts:         []mcpExtension{orchestratorMCP},
			wantContains: []string{
				"#!/bin/bash",
				"source ~/.bashrc",
				"goose run",
				"--provider anthropic",
				"--model claude-sonnet-4-5",
				"--output-format stream-json",
				`--system "$(cat ~/system-prompt.md)"`,
				"--with-streamable-http-extension 'http://localhost:19090/mcp'",
				"-n 'fix-handler'",
				"-t 'Fix the bug in handler.go'",
				"tee  ~/transcript.jsonl",
				"ANTHROPIC_API_KEY",
			},
			wantNotContains: []string{
				"--resume",
				"tee -a",
				"claude",
			},
		},
		{
			name:            "continue session",
			taskName:        "fix-handler",
			taskDescription: "Also fix the tests",
			continueSession: true,
			agentCfg:        defaultCfg,
			mcpExts:         []mcpExtension{orchestratorMCP},
			wantContains: []string{
				"--resume",
				"-n 'fix-handler'",
				"-t 'Also fix the tests'",
				"tee -a ~/transcript.jsonl",
			},
		},
		{
			name:            "description with single quotes",
			taskName:        "fix-bug",
			taskDescription: "Fix the 'bug' in handler.go",
			continueSession: false,
			agentCfg:        defaultCfg,
			mcpExts:         []mcpExtension{orchestratorMCP},
			wantContains: []string{
				`-t 'Fix the '\''bug'\'' in handler.go'`,
			},
		},
		{
			name:            "google provider",
			taskName:        "fix-bug",
			taskDescription: "Fix the bug",
			continueSession: false,
			agentCfg:        googleCfg,
			mcpExts:         []mcpExtension{orchestratorMCP},
			wantContains: []string{
				"--provider google",
				"--model gemini-2.5-pro",
				"GOOGLE_API_KEY",
			},
			wantNotContains: []string{
				"ANTHROPIC_API_KEY",
			},
		},
		{
			name:            "nil agentCfg defaults to anthropic",
			taskName:        "fix-something",
			taskDescription: "Fix something",
			continueSession: false,
			agentCfg:        nil,
			mcpExts:         []mcpExtension{orchestratorMCP},
			wantContains: []string{
				"--provider anthropic",
				"ANTHROPIC_API_KEY",
			},
		},
		{
			name:            "multiple MCP extensions",
			taskName:        "multi-mcp",
			taskDescription: "Do the thing",
			continueSession: false,
			agentCfg:        defaultCfg,
			mcpExts: []mcpExtension{
				orchestratorMCP,
				{Transport: "http", Value: "http://localhost:8080/other"},
				{Transport: "stdio", Value: "npx some-mcp-server"},
			},
			wantContains: []string{
				"--with-streamable-http-extension 'http://localhost:19090/mcp'",
				"--with-streamable-http-extension 'http://localhost:8080/other'",
				"--with-extension 'npx some-mcp-server'",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildRunScript(tt.taskName, tt.taskDescription, tt.continueSession, tt.agentCfg, tt.mcpExts)
			for _, want := range tt.wantContains {
				if !strings.Contains(got, want) {
					t.Errorf("buildRunScript() missing %q\ngot:\n%s", want, got)
				}
			}
			for _, notWant := range tt.wantNotContains {
				if strings.Contains(got, notWant) {
					t.Errorf("buildRunScript() should not contain %q\ngot:\n%s", notWant, got)
				}
			}
		})
	}
}
```

- [ ] **Step 2: Run tests (expect failure from profile package)**

Run: `go test ./internal/cmd/ -v -run TestBuildRunScript`
Expected: Compilation failure — `profile.Apply` signature change is needed. This is expected — Task 5 fixes it.

- [ ] **Step 3: Commit**

```bash
git add internal/cmd/task_test.go
git commit -m "cmd/task: rewrite buildRunScript tests for goose

Test goose run flags, provider env vars, MCP extension flags, session
resume, and single-quote escaping."
```

---

### Task 5: Update Profile System (`internal/profile/apply.go`)

**Files:**
- Modify: `internal/profile/apply.go`

- [ ] **Step 1: Add MCPExtension type and update Apply signature**

In `internal/profile/apply.go`, add the exported type and update the function:

Replace:

```go
// Apply writes the profile's configuration into a sandbox.
//
// It performs the following steps (skipping optional files that are absent):
//  1. Write base prompt + profile CLAUDE.md → ~/.claude/CLAUDE.md
//  2. Copy settings.json → ~/.claude/settings.json
//  3. Register MCP servers from mcp.yaml via "claude mcp add"
//  4. Run setup.sh on the host with helper scripts and environment variables
func Apply(ctx context.Context, p *Profile, runner sandbox.Runner, sbx string, taskDir string, basePrompt string, vars map[string]string) error {
	// 1. Write combined CLAUDE.md
	claudemd := basePrompt + "\n\n# Profile: " + p.Name + "\n\n" + p.Claudemd
	if err := writeToSandbox(ctx, runner, sbx, claudemd, ":~/.claude/CLAUDE.md"); err != nil {
		return fmt.Errorf("writing CLAUDE.md: %w", err)
	}

	// 2. Copy settings.json if present
	if p.Settings != "" {
		if err := runner.Cp(ctx, sbx, p.Settings, ":~/.claude/settings.json"); err != nil {
			return fmt.Errorf("copying settings.json: %w", err)
		}
		slog.Debug("Copied profile settings.json", "profile", p.Name)
	}

	// 3. Register MCP servers from mcp.yaml
	if p.MCP != nil {
		for _, server := range p.MCP.Servers {
			if err := registerMCPServer(ctx, runner, sbx, server); err != nil {
				return fmt.Errorf("registering MCP server %q: %w", server.Name, err)
			}
			slog.Debug("Registered MCP server", "profile", p.Name, "server", server.Name)
		}
	}

	// 4. Run setup.sh on the host
	if p.Setup != "" {
		if err := runSetup(ctx, runner, sbx, p.Setup, taskDir, vars); err != nil {
			return fmt.Errorf("running setup.sh: %w", err)
		}
		slog.Debug("Ran profile setup.sh", "profile", p.Name)
	}

	return nil
}
```

With:

```go
// MCPExtension represents an MCP server to pass to goose at runtime.
type MCPExtension struct {
	Transport string // "http" or "stdio"
	Value     string // URL for http, command string for stdio
}

// Apply writes the profile's configuration into a sandbox and returns MCP
// extensions that should be passed to the agent at runtime.
//
// It performs the following steps (skipping optional files that are absent):
//  1. Write base prompt + profile hints → ~/workspace/.goosehints
//  2. Collect MCP servers from mcp.yaml as runtime extensions
//  3. Run setup.sh on the host with helper scripts and environment variables
func Apply(ctx context.Context, p *Profile, runner sandbox.Runner, sbx string, taskDir string, basePrompt string, vars map[string]string) ([]MCPExtension, error) {
	// 1. Write combined .goosehints
	goosehints := basePrompt + "\n\n# Profile: " + p.Name + "\n\n" + p.Claudemd
	if err := writeToSandbox(ctx, runner, sbx, goosehints, ":~/workspace/.goosehints"); err != nil {
		return nil, fmt.Errorf("writing .goosehints: %w", err)
	}

	// 2. Collect MCP extensions (returned to caller for runtime flags)
	var exts []MCPExtension
	if p.MCP != nil {
		for _, server := range p.MCP.Servers {
			ext := mcpServerToExtension(server)
			exts = append(exts, ext)
			slog.Debug("Collected MCP extension", "profile", p.Name, "server", server.Name)
		}
	}

	// 3. Run setup.sh on the host
	if p.Setup != "" {
		if err := runSetup(ctx, runner, sbx, p.Setup, taskDir, vars); err != nil {
			return nil, fmt.Errorf("running setup.sh: %w", err)
		}
		slog.Debug("Ran profile setup.sh", "profile", p.Name)
	}

	return exts, nil
}
```

- [ ] **Step 2: Replace registerMCPServer with mcpServerToExtension**

Replace:

```go
// registerMCPServer runs "claude mcp add" in the sandbox for a single server.
func registerMCPServer(ctx context.Context, runner sandbox.Runner, sbx string, server MCPServer) error {
	var args []string
	switch server.Transport {
	case "stdio":
		args = []string{"claude", "mcp", "add", "--transport", "stdio"}
		if server.Scope != "" {
			args = append(args, "--scope", server.Scope)
		}
		args = append(args, server.Name, server.Command)
		args = append(args, server.Args...)
	case "http":
		args = []string{"claude", "mcp", "add", "--transport", "http"}
		if server.Scope != "" {
			args = append(args, "--scope", server.Scope)
		}
		args = append(args, server.Name, server.URL)
	}
	return runner.SSH(ctx, sbx, "bash", "-c", fmt.Sprintf("su - claude -c '%s'", strings.Join(args, " ")))
}
```

With:

```go
// mcpServerToExtension converts a profile MCP server definition to a runtime extension.
func mcpServerToExtension(server MCPServer) MCPExtension {
	switch server.Transport {
	case "stdio":
		cmd := server.Command
		if len(server.Args) > 0 {
			cmd += " " + strings.Join(server.Args, " ")
		}
		return MCPExtension{Transport: "stdio", Value: cmd}
	case "http":
		return MCPExtension{Transport: "http", Value: server.URL}
	default:
		return MCPExtension{Transport: "http", Value: server.URL}
	}
}
```

- [ ] **Step 3: Update writeToSandbox to create ~/workspace instead of ~/.claude**

Replace:

```go
// writeToSandbox writes content to a file in the sandbox via a temp file + cp.
func writeToSandbox(ctx context.Context, runner sandbox.Runner, sbx, content, dest string) error {
	// Ensure the parent directory exists in the sandbox
	runner.SSH(ctx, sbx, "bash", "-c", "su - claude -c 'mkdir -p ~/.claude'")
```

With:

```go
// writeToSandbox writes content to a file in the sandbox via a temp file + cp.
func writeToSandbox(ctx context.Context, runner sandbox.Runner, sbx, content, dest string) error {
	// Ensure the parent directory exists in the sandbox
	runner.SSH(ctx, sbx, "bash", "-c", "su - claude -c 'mkdir -p ~/workspace'")
```

- [ ] **Step 4: Remove unused imports**

The `fmt` import used by `registerMCPServer` for `fmt.Sprintf` is still used by `runSetup` and `writeToSandbox`. The `strings` import is still used by `mcpServerToExtension`. The `os/exec` import is used by `runSetup`. Verify no unused imports remain.

Remove unused import `"os/exec"` only if `runSetup` no longer uses it — but it does (line 139), so keep it.

- [ ] **Step 5: Update setupSandboxWithProfile in task.go to use profile.MCPExtension**

In `internal/cmd/task.go`, the `setupSandboxWithProfile` function already handles the conversion in Step 8 of Task 3. Ensure the import for `profile` package is present and that the type conversion from `profile.MCPExtension` to local `mcpExtension` works:

```go
	// Convert profile MCP extensions to our type
	var exts []mcpExtension
	for _, pe := range profileExts {
		exts = append(exts, mcpExtension{Transport: pe.Transport, Value: pe.Value})
	}
```

- [ ] **Step 6: Verify compilation**

Run: `go build ./...`
Expected: PASS (all files now consistent).

- [ ] **Step 7: Run all tests**

Run: `go test ./internal/config/ ./internal/cmd/ -v`
Expected: All tests PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/profile/apply.go
git commit -m "profile: write .goosehints and return MCP extensions instead of registering

Replace ~/.claude/CLAUDE.md with ~/workspace/.goosehints, drop
settings.json, and return MCP server definitions as data instead of
running 'claude mcp add' commands."
```

---

### Task 6: Rewrite Transcript Parser (`internal/cmd/log.go`)

**Files:**
- Modify: `internal/cmd/log.go`

- [ ] **Step 1: Update the PodmanConfig construction in runLog**

Replace:

```go
		podmanCfg := &sandbox.PodmanConfig{
			Image:              cfg.PodmanImage,
			AnthropicKeyFile:   cfg.AnthropicKeyFile,
			APIProvider:        cfg.APIProvider,
			VertexProjectID:    cfg.VertexProjectID,
			VertexRegion:       cfg.VertexRegion,
			VertexModel:        cfg.VertexModel,
			GCPCredentialsFile: cfg.GCPCredentialsFile,
			MCPPort:            19090,
		}
```

With:

```go
		podmanCfg := &sandbox.PodmanConfig{
			Image:           cfg.PodmanImage,
			AgentProvider:   cfg.AgentProvider,
			AgentModel:      cfg.AgentModel,
			AgentAPIKeyFile: cfg.AgentAPIKeyFile,
			MCPPort:         19090,
		}
```

- [ ] **Step 2: Rewrite formatTranscriptLine for goose stream-json format**

Replace the entire `formatTranscriptLine` function:

```go
// formatTranscriptLine formats a single stream-json line for human readability.
// When verbose is true, thinking blocks are included in the output.
func formatTranscriptLine(line []byte, verbose bool) string {
	var msg struct {
		Type    string `json:"type"`
		Subtype string `json:"subtype"`
		Message struct {
			Content []struct {
				Type     string          `json:"type"`
				Text     string          `json:"text"`
				Name     string          `json:"name"`
				Input    json.RawMessage `json:"input"`
				Thinking string          `json:"thinking"`
				Content  json.RawMessage `json:"content"` // tool_result: string or array
			} `json:"content"`
		} `json:"message"`
		DurationMS   int     `json:"duration_ms"`
		NumTurns     int     `json:"num_turns"`
		TotalCostUSD float64 `json:"total_cost_usd"`
	}
	if err := json.Unmarshal(line, &msg); err != nil {
		return ""
	}

	var out string
	switch msg.Type {
	case "assistant":
		for _, c := range msg.Message.Content {
			switch c.Type {
			case "text":
				out += c.Text + "\n"
			case "tool_use":
				summary := toolInputSummary(c.Name, c.Input)
				if summary != "" {
					out += fmt.Sprintf("[tool] %s: %s\n", c.Name, summary)
				} else {
					out += fmt.Sprintf("[tool] %s\n", c.Name)
				}
			case "thinking":
				if verbose && c.Thinking != "" {
					out += fmt.Sprintf("[thinking] %s\n", c.Thinking)
				}
			}
		}
	case "user":
		for _, c := range msg.Message.Content {
			if c.Type != "tool_result" || len(c.Content) == 0 {
				continue
			}
			var s string
			if json.Unmarshal(c.Content, &s) == nil && s != "" {
				out += fmt.Sprintf("  → %s\n", firstLine(s, 200))
			}
		}
	case "result":
		subtype := msg.Subtype
		if subtype == "" {
			subtype = "done"
		}
		duration := float64(msg.DurationMS) / 1000
		if msg.TotalCostUSD > 0 {
			out = fmt.Sprintf("[result] %s (%d turns, %.1fs, $%.2f)\n", subtype, msg.NumTurns, duration, msg.TotalCostUSD)
		} else if msg.DurationMS > 0 {
			out = fmt.Sprintf("[result] %s (%d turns, %.1fs)\n", subtype, msg.NumTurns, duration)
		} else {
			out = fmt.Sprintf("[result] %s\n", subtype)
		}
	}
	return out
}
```

With:

```go
// formatTranscriptLine formats a single goose stream-json line for human readability.
// When verbose is true, thinking blocks and notifications are included in the output.
func formatTranscriptLine(line []byte, verbose bool) string {
	var event struct {
		Type        string `json:"type"` // "message", "error", "complete", "notification"
		Error       string `json:"error"`
		TotalTokens *int   `json:"total_tokens"`
		Message     *struct {
			Role    string `json:"role"`
			Content []struct {
				Type       string          `json:"type"` // "text", "toolRequest", "toolResponse", "thinking"
				Text       string          `json:"text"`
				Thinking   string          `json:"thinking"`
				ID         string          `json:"id"`
				ToolCall   json.RawMessage `json:"toolCall"`
				ToolResult json.RawMessage `json:"toolResult"`
			} `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(line, &event); err != nil {
		return ""
	}

	var out string
	switch event.Type {
	case "message":
		if event.Message == nil {
			return ""
		}
		for _, c := range event.Message.Content {
			switch c.Type {
			case "text":
				out += c.Text + "\n"
			case "toolRequest":
				name, summary := toolRequestSummary(c.ToolCall)
				if summary != "" {
					out += fmt.Sprintf("[tool] %s: %s\n", name, summary)
				} else if name != "" {
					out += fmt.Sprintf("[tool] %s\n", name)
				}
			case "toolResponse":
				result := toolResponseSummary(c.ToolResult)
				if result != "" {
					out += fmt.Sprintf("  → %s\n", result)
				}
			case "thinking":
				if verbose && c.Thinking != "" {
					out += fmt.Sprintf("[thinking] %s\n", c.Thinking)
				}
			}
		}
	case "complete":
		if event.TotalTokens != nil {
			out = fmt.Sprintf("[complete] total_tokens: %d\n", *event.TotalTokens)
		} else {
			out = "[complete]\n"
		}
	case "error":
		out = fmt.Sprintf("[error] %s\n", event.Error)
	case "notification":
		if verbose {
			out = fmt.Sprintf("[notification] %s\n", firstLine(string(line), 200))
		}
	}
	return out
}
```

- [ ] **Step 3: Replace toolInputSummary with toolRequestSummary and toolResponseSummary**

Replace:

```go
// toolInputSummary extracts a short description from a tool's input.
func toolInputSummary(name string, raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}

	switch name {
	case "Write", "Read", "Edit":
		if v, ok := m["file_path"].(string); ok {
			return v
		}
	case "Bash":
		if v, ok := m["description"].(string); ok {
			return v
		}
		if v, ok := m["command"].(string); ok {
			return firstLine(v, 80)
		}
	case "Grep", "Glob":
		if v, ok := m["pattern"].(string); ok {
			return v
		}
	}

	// Fallback: try common field names
	for _, key := range []string{"path", "query", "url", "name"} {
		if v, ok := m[key].(string); ok {
			return firstLine(v, 80)
		}
	}
	return ""
}
```

With:

```go
// toolRequestSummary extracts the tool name and a short description from a goose toolRequest.
// The toolCall field has structure: {"status":"success","value":{"name":"...","arguments":{...}}}
func toolRequestSummary(raw json.RawMessage) (name, summary string) {
	if len(raw) == 0 {
		return "", ""
	}
	var tc struct {
		Value struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		} `json:"value"`
	}
	if json.Unmarshal(raw, &tc) != nil {
		return "", ""
	}
	name = tc.Value.Name

	if len(tc.Value.Arguments) == 0 {
		return name, ""
	}

	var args map[string]any
	if json.Unmarshal(tc.Value.Arguments, &args) != nil {
		return name, ""
	}

	// Try common field names for a short summary
	for _, key := range []string{"file_path", "path", "command", "description", "pattern", "query", "url", "name"} {
		if v, ok := args[key].(string); ok {
			return name, firstLine(v, 80)
		}
	}
	return name, ""
}

// toolResponseSummary extracts a short description from a goose toolResponse.
// The toolResult field has structure: {"status":"success","value":{"content":[...],"isError":false}}
func toolResponseSummary(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var tr struct {
		Value struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"value"`
	}
	if json.Unmarshal(raw, &tr) != nil {
		return ""
	}
	for _, c := range tr.Value.Content {
		if c.Type == "text" && c.Text != "" {
			return firstLine(c.Text, 200)
		}
	}
	return ""
}
```

- [ ] **Step 4: Verify compilation**

Run: `go build ./internal/cmd/`
Expected: PASS.

- [ ] **Step 5: Run all tests**

Run: `go test ./... -v`
Expected: All tests PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/cmd/log.go
git commit -m "cmd/log: rewrite transcript parser for goose stream-json format

Handle goose StreamEvent types (message, complete, error, notification)
and message content types (text, toolRequest, toolResponse, thinking).
Replace Claude Code's assistant/user/result types."
```

---

### Task 7: Update log command description and verify full build

**Files:**
- Modify: `internal/cmd/log.go`

- [ ] **Step 1: Update log command help text**

Replace:

```go
var logCmd = &cobra.Command{
	Use:   "log <task-name>",
	Short: "Show Claude transcript for a task",
	Long: `Shows the stream-json transcript from a Claude session.

Without -f, reads the local transcript from the task directory (after the
task has completed and the transcript has been copied back).

With -f/--follow, tails the live transcript from the running sandbox VM
via SSH, formatted for human readability.

Use -v to also show the model's internal reasoning (thinking blocks).

Examples:
  orchestrator log my-task          Show completed task transcript
  orchestrator log -v my-task       Include model reasoning
  orchestrator log -f my-task       Follow live transcript`,
	Args: cobra.ExactArgs(1),
	RunE: runLog,
}
```

With:

```go
var logCmd = &cobra.Command{
	Use:   "log <task-name>",
	Short: "Show agent transcript for a task",
	Long: `Shows the stream-json transcript from a goose session.

Without -f, reads the local transcript from the task directory (after the
task has completed and the transcript has been copied back).

With -f/--follow, tails the live transcript from the running sandbox VM
via SSH, formatted for human readability.

Use -v to also show the model's internal reasoning (thinking blocks).

Examples:
  orchestrator log my-task          Show completed task transcript
  orchestrator log -v my-task       Include model reasoning
  orchestrator log -f my-task       Follow live transcript`,
	Args: cobra.ExactArgs(1),
	RunE: runLog,
}
```

- [ ] **Step 2: Update task command help text**

In `internal/cmd/task.go`, replace:

```go
var taskNewCmd = &cobra.Command{
	Use:   "new <task-name> <task-description...>",
	Short: "Run a new task in a sandboxed Claude instance",
	Long: `Provisions a sandbox VM via gjoll, starts an MCP server for code pulling,
launches Claude with the task description, and archives the results.`,
	Args: cobra.MinimumNArgs(2),
	RunE: runTask,
}

var taskContinueCmd = &cobra.Command{
	Use:   "continue <task-name> <task-description...>",
	Short: "Continue a stopped task with a new prompt",
	Long: `Resumes a stopped sandbox VM, starts an MCP server, and launches Claude
with --continue to resume the previous conversation with a new prompt.`,
	Args: cobra.MinimumNArgs(2),
	RunE: continueTask,
}
```

With:

```go
var taskNewCmd = &cobra.Command{
	Use:   "new <task-name> <task-description...>",
	Short: "Run a new task in a sandboxed agent instance",
	Long: `Provisions a sandbox, starts an MCP server for code pulling,
launches goose with the task description, and archives the results.`,
	Args: cobra.MinimumNArgs(2),
	RunE: runTask,
}

var taskContinueCmd = &cobra.Command{
	Use:   "continue <task-name> <task-description...>",
	Short: "Continue a stopped task with a new prompt",
	Long: `Resumes a stopped sandbox, starts an MCP server, and launches goose
with --resume to resume the previous conversation with a new prompt.`,
	Args: cobra.MinimumNArgs(2),
	RunE: continueTask,
}
```

- [ ] **Step 3: Run full build and tests**

Run: `go build ./... && go test ./... -v`
Expected: Build and all tests PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/cmd/log.go internal/cmd/task.go
git commit -m "cmd: update help text to reference goose instead of Claude"
```

---

### Task 8: Final verification

**Files:** None (verification only)

- [ ] **Step 1: Run full test suite**

Run: `go test ./... -v`
Expected: All tests PASS.

- [ ] **Step 2: Run go vet**

Run: `go vet ./...`
Expected: No issues.

- [ ] **Step 3: Verify no remaining Claude Code references in source**

Run: `grep -r "claude.ai\|claude-code\|Claude Code\|claude mcp add\|anthropic_key_file\|api_provider\|vertex_project\|vertex_region\|vertex_model\|gcp_credentials" internal/ --include="*.go" -l`
Expected: No output (no files match). Note: references to the UNIX user `claude` and `CLAUDE.md` in profile are expected and acceptable.

- [ ] **Step 4: Review changes**

Run: `git diff HEAD~7 --stat` to verify the scope of changes matches the spec.
