package cmd

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/drellabot/orchestrator/internal/config"
	gh "github.com/drellabot/orchestrator/internal/github"
	"github.com/drellabot/orchestrator/internal/sandbox"
	"github.com/drellabot/orchestrator/internal/logging"
	mcpserver "github.com/drellabot/orchestrator/internal/mcp"
	"github.com/drellabot/orchestrator/internal/profile"
	"github.com/drellabot/orchestrator/internal/prompts"
	"github.com/drellabot/orchestrator/internal/task"
	"github.com/spf13/cobra"
)

var author string
var profileName string
var profileVars []string

var taskCmd = &cobra.Command{
	Use:   "task",
	Short: "Manage tasks",
}

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

func init() {
	taskNewCmd.Flags().StringVar(&author, "author", "", "co-author to add to PR commits (e.g. \"Jane Doe <jane@example.com>\")")
	taskNewCmd.Flags().StringVar(&profileName, "profile", "", "profile to apply to the sandbox (e.g. \"code-review\")")
	taskNewCmd.Flags().StringSliceVar(&profileVars, "var", nil, "profile variables as KEY=VALUE (e.g. --var PROFILE_PR=42)")
	taskCmd.AddCommand(taskNewCmd)
	taskCmd.AddCommand(taskContinueCmd)
	taskCmd.AddCommand(taskWatchCmd)
}

func runTask(cmd *cobra.Command, args []string) error {
	taskName := args[0]
	taskDescription := strings.Join(args[1:], " ")

	cfg, err := loadConfigAndSetupLogging()
	if err != nil {
		return err
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	ghRunner := logPreflightWarnings(ctx, cfg)

	slog.Info("Task started", "task", taskName)

	taskDir, err := task.Create(cfg.OutputDir, taskName)
	if err != nil {
		return fmt.Errorf("creating task directory: %w", err)
	}

	if err := taskDir.SaveMetadata(taskName, taskDescription, author, time.Now()); err != nil {
		return fmt.Errorf("saving metadata: %w", err)
	}

	return executeTask(ctx, taskName, taskDescription, taskDir, cfg, ghRunner, false, author, profileName, profileVars)
}

func continueTask(cmd *cobra.Command, args []string) error {
	taskName := args[0]
	taskDescription := strings.Join(args[1:], " ")

	cfg, err := loadConfigAndSetupLogging()
	if err != nil {
		return err
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	ghRunner := logPreflightWarnings(ctx, cfg)

	slog.Info("Task continuing", "task", taskName)

	taskDir, err := task.Open(cfg.OutputDir, taskName)
	if err != nil {
		return fmt.Errorf("opening task directory: %w", err)
	}

	state, err := taskDir.LoadState()
	if err != nil {
		return fmt.Errorf("loading task state: %w", err)
	}

	return executeTask(ctx, taskName, taskDescription, taskDir, cfg, ghRunner, true, state.Author, "", nil)
}

func loadConfigAndSetupLogging() (*config.Config, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, fmt.Errorf("loading config: %w", err)
	}

	logger := logging.SetupLogger(cfg.SlackWebhook, verbose)
	slog.SetDefault(logger)

	return cfg, nil
}

func logPreflightWarnings(ctx context.Context, cfg *config.Config) *gh.Runner {
	if len(cfg.AllowedRepos) == 0 {
		slog.Warn("allowed_repos is empty; open_pr, update_pr, and comment_on_pr tools will not be available")
	}

	ghRunner := gh.New("")
	if _, err := ghRunner.AuthenticatedUser(ctx); err != nil {
		slog.Warn("GitHub CLI not authenticated; open_pr, update_pr, and comment_on_pr tools will not be available", "error", err)
	}

	return ghRunner
}

func createSandboxRunner(cfg *config.Config) sandbox.Runner {
	slog.Info("Creating sandbox runner", "backend", cfg.SandboxBackend)
	return sandbox.NewFromConfig(cfg.SandboxBackend, cfg.GjollEnv, cfg.PodmanImage, cfg.AnthropicKeyFile, mcpserver.MCPRemotePort)
}

func executeTask(ctx context.Context, taskName, taskDescription string, taskDir *task.Dir, cfg *config.Config, ghRunner *gh.Runner, continueSession bool, author string, profileName string, profileVars []string) error {
	runner := createSandboxRunner(cfg)

	logger := slog.Default()

	// Start MCP server
	mcpSrv := mcpserver.New(logger, taskName, taskDir, runner, ghRunner, cfg.AllowedRepos, author)
	if err := mcpSrv.Start(); err != nil {
		return fmt.Errorf("starting MCP server: %w", err)
	}
	defer func() { _ = mcpSrv.Stop(context.Background()) }()

	mcpPort := mcpSrv.Port()
	mcpTunnel := fmt.Sprintf("%d:localhost:%d", mcpserver.MCPRemotePort, mcpPort)
	slog.Debug("MCP server started", "task", taskName, "port", mcpPort)

	if continueSession {
		slog.Info("Resuming sandbox", "task", taskName)
		if err := runner.Start(ctx, taskName); err != nil {
			return fmt.Errorf("resuming sandbox: %w", err)
		}
	} else {
		tfPath, err := filepath.Abs(cfg.GjollEnv)
		if err != nil {
			return fmt.Errorf("resolving tf path: %w", err)
		}

		slog.Info("Provisioning sandbox", "task", taskName)
		if err := runner.Up(ctx, taskName, tfPath); err != nil {
			return fmt.Errorf("provisioning sandbox: %w", err)
		}
	}

	// After successful provisioning, ensure cleanup on exit
	defer func() {
		slog.Debug("Copying transcript", "task", taskName)
		if copyErr := runner.Cp(context.Background(), taskName, taskName+":/home/claude/transcript.jsonl", taskDir.TranscriptPath()); copyErr != nil {
			slog.Warn("Failed to copy transcript", "task", taskName, "error", copyErr)
		}

		slog.Debug("Copying conversations", "task", taskName)
		if copyErr := runner.Cp(context.Background(), taskName, taskName+":/home/claude/.claude/", taskDir.ConversationsPath()); copyErr != nil {
			slog.Warn("Failed to copy conversations", "task", taskName, "error", copyErr)
		}

		// Flush filesystem writes before stopping — the libvirt provider
		// only waits 5 seconds for graceful ACPI shutdown before force-
		// destroying the VM, which can lose unflushed data.
		slog.Debug("Syncing filesystem", "task", taskName)
		_ = runner.SSH(context.Background(), taskName, "sync")

		slog.Debug("Stopping sandbox", "task", taskName)
		if stopErr := runner.Stop(context.Background(), taskName); stopErr != nil {
			slog.Warn("Failed to stop sandbox", "task", taskName, "error", stopErr)
		}
	}()

	slog.Info("Sandbox provisioned", "task", taskName)

	if !continueSession {
		if err := setupSandbox(ctx, runner, taskName, taskDir, cfg, profileName, profileVars); err != nil {
			return fmt.Errorf("setting up sandbox: %w", err)
		}
		slog.Debug("Sandbox setup complete", "task", taskName)
	}

	// Build the Claude run script
	slog.Info("Running Claude", "task", taskName)
	runScript := buildRunScript(taskDescription, continueSession)

	tmpRun, err := os.CreateTemp("", "run-claude-*.sh")
	if err != nil {
		return fmt.Errorf("creating run script: %w", err)
	}
	defer os.Remove(tmpRun.Name())
	if _, err := tmpRun.WriteString(runScript); err != nil {
		tmpRun.Close()
		return fmt.Errorf("writing run script: %w", err)
	}
	tmpRun.Close()

	if err := runner.Cp(ctx, taskName, tmpRun.Name(), taskName+":/home/claude/run-claude.sh"); err != nil {
		return fmt.Errorf("copying run script: %w", err)
	}
	if err := runner.SSH(ctx, taskName, "bash", "-c", "chown claude:claude /home/claude/run-claude.sh && chmod +x /home/claude/run-claude.sh"); err != nil {
		return fmt.Errorf("making run script executable: %w", err)
	}

	sshOpts := &sandbox.SSHOpts{
		Proxy:          true,
		ReverseTunnels: []string{mcpTunnel},
	}

	// Write the raw JSONL transcript to the host file in real-time so the
	// dashboard can poll it while the task is still running.
	var transcriptFlags int
	if continueSession {
		transcriptFlags = os.O_WRONLY | os.O_CREATE | os.O_APPEND
	} else {
		transcriptFlags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	}
	transcriptFile, err := os.OpenFile(taskDir.TranscriptPath(), transcriptFlags, 0644)
	if err != nil {
		return fmt.Errorf("opening transcript file: %w", err)
	}
	defer transcriptFile.Close()

	tw := newTranscriptWriter(os.Stdout, verbose)
	w := io.MultiWriter(tw, transcriptFile)
	if err := runner.SSHProxyOutput(ctx, taskName, w, sshOpts, "bash", "-c", "su - claude -c /home/claude/run-claude.sh"); err != nil {
		slog.Error("Claude exited with error", "task", taskName, "error", err)
		// Don't return error - still want to archive results
	}

	slog.Info("Claude finished", "task", taskName)

	if err := taskDir.TouchUpdatedAt(time.Now()); err != nil {
		slog.Warn("Failed to update updated_at", "task", taskName, "error", err)
	}

	slog.Info("Task completed", "task", taskName)
	return nil
}

func buildRunScript(taskDescription string, continueSession bool) string {
	escapedDesc := strings.ReplaceAll(taskDescription, "'", "'\\''")

	var claudeFlags string
	var teeFlag string
	if continueSession {
		claudeFlags = "--continue"
		teeFlag = "-a"
	}

	return fmt.Sprintf(`#!/bin/bash
source ~/.bashrc
export PATH="/home/claude/.local/bin:$PATH"
export ANTHROPIC_API_KEY="$(cat ~/.anthropic/api_key)"
stdbuf -oL claude --dangerously-skip-permissions -p --verbose \
  --effort max \
  --output-format stream-json --append-system-prompt-file ~/system-prompt.md \
  %s '%s' \
  </dev/null | stdbuf -oL tee %s ~/transcript.jsonl
`, claudeFlags, escapedDesc, teeFlag)
}

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

// setupSandboxDefault preserves the existing behavior when no profile is specified.
func setupSandboxDefault(ctx context.Context, runner sandbox.Runner, taskName string) error {
	// Write system prompt to a temp file and copy it to the sandbox
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

	if err := runner.Cp(ctx, taskName, tmpFile.Name(), taskName+":/home/claude/system-prompt.md"); err != nil {
		return fmt.Errorf("copying system prompt: %w", err)
	}

	// Fix ownership of system prompt
	if err := runner.SSH(ctx, taskName, "bash", "-c", "chown claude:claude /home/claude/system-prompt.md"); err != nil {
		return fmt.Errorf("chowning system prompt: %w", err)
	}

	return nil
}

// parseVarFlags parses --var KEY=VALUE flags into a map.
func parseVarFlags(flags []string) map[string]string {
	if len(flags) == 0 {
		return nil
	}
	vars := make(map[string]string, len(flags))
	for _, f := range flags {
		k, v, ok := strings.Cut(f, "=")
		if ok {
			vars[k] = v
		}
	}
	return vars
}

// resolveProfileSource returns the directory containing profiles.
// If profiles_dir is set, it's used directly. Otherwise, profiles_repo
// is shallow-cloned to a temp directory (returned cleanup removes it).
func resolveProfileSource(ctx context.Context, cfg *config.Config) (dir string, cleanup func(), err error) {
	if cfg.ProfilesDir != "" {
		return cfg.ProfilesDir, nil, nil
	}

	if cfg.ProfilesRepo == "" {
		return "", nil, fmt.Errorf("--profile requires profiles_repo or profiles_dir in config")
	}

	tmpDir, err := os.MkdirTemp("", "profiles-*")
	if err != nil {
		return "", nil, fmt.Errorf("creating temp dir for profiles: %w", err)
	}

	cloneDir := filepath.Join(tmpDir, "profiles")
	slog.Debug("Cloning profiles repo", "repo", cfg.ProfilesRepo, "dest", cloneDir)

	cmd := exec.CommandContext(ctx, "gh", "repo", "clone", cfg.ProfilesRepo, cloneDir, "--", "--depth=1")
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		os.RemoveAll(tmpDir)
		return "", nil, fmt.Errorf("cloning profiles repo %q: %w", cfg.ProfilesRepo, err)
	}

	return cloneDir, func() { os.RemoveAll(tmpDir) }, nil
}
