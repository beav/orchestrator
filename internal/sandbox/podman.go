package sandbox

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

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

// Up provisions a new container sandbox.
func (r *PodmanRunner) Up(ctx context.Context, name string, config string) error {
	args := []string{
		"run", "-d",
		"--name", name,
		"--network", "host",
		"--security-opt", "label=disable",
	}

	args = append(args, r.image, "sleep", "infinity")

	cmd := exec.CommandContext(ctx, "podman", args...)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("podman run: %w", err)
	}

	// Create non-root user for Claude
	userSetupCmds := []string{
		"bash", "-c",
		"useradd -m -s /bin/bash claude && dnf install -y git-core curl sudo && chown claude:claude /home/claude && (chown -R claude:claude /home/claude 2>/dev/null || true)",
	}
	if err := r.SSH(ctx, name, userSetupCmds...); err != nil {
		_ = r.Down(context.Background(), name)
		return fmt.Errorf("user setup: %w", err)
	}

	// Install Claude Code as the claude user
	installCmds := []string{
		"bash", "-c",
		"su - claude -c 'curl -fsSL https://claude.ai/install.sh | bash'",
	}
	if err := r.SSH(ctx, name, installCmds...); err != nil {
		_ = r.Down(context.Background(), name)
		return fmt.Errorf("claude install: %w", err)
	}

	// Configure API credentials based on provider
	if err := r.setupCredentials(ctx, name); err != nil {
		_ = r.Down(context.Background(), name)
		return err
	}

	// Configure environment as claude user
	configCmds := []string{
		"bash", "-c",
		"su - claude -c 'mkdir -p ~/workspace ~/.config/claude-code && cd ~/workspace && git init'",
	}
	if err := r.SSH(ctx, name, configCmds...); err != nil {
		_ = r.Down(context.Background(), name)
		return fmt.Errorf("sandbox config: %w", err)
	}

	return nil
}

// Start starts a stopped container.
func (r *PodmanRunner) Start(ctx context.Context, name string) error {
	return r.run(ctx, "start", name)
}

// SSH runs a command in the container.
func (r *PodmanRunner) SSH(ctx context.Context, name string, command ...string) error {
	args := []string{"exec", name}
	args = append(args, command...)
	return r.run(ctx, args...)
}

// SSHProxy runs a command in the container.
func (r *PodmanRunner) SSHProxy(ctx context.Context, name string, opts *SSHOpts, command ...string) error {
	args := []string{"exec", "-it", name}
	args = append(args, command...)
	return r.runInteractive(ctx, args...)
}

// SSHProxyOutput runs a command in the container, writing stdout to w.
func (r *PodmanRunner) SSHProxyOutput(ctx context.Context, name string, w io.Writer, opts *SSHOpts, command ...string) error {
	args := []string{"exec", name}
	args = append(args, command...)
	cmd := exec.CommandContext(ctx, "podman", args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = w
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// Pull fetches committed code from the sandbox.
func (r *PodmanRunner) Pull(ctx context.Context, name, remotePath, localRepoDir string) error {
	if err := os.MkdirAll(localRepoDir, 0755); err != nil {
		return fmt.Errorf("creating local repo dir: %w", err)
	}

	if _, err := os.Stat(filepath.Join(localRepoDir, ".git")); os.IsNotExist(err) {
		cmd := exec.CommandContext(ctx, "git", "init")
		cmd.Dir = localRepoDir
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("git init: %w", err)
		}
	}

	tmpDir, err := os.MkdirTemp("", "drella-pull-*")
	if err != nil {
		return fmt.Errorf("creating temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	cpSrc := name + ":" + remotePath + "/."
	if err := r.Cp(ctx, name, cpSrc, tmpDir); err != nil {
		return fmt.Errorf("copying from container: %w", err)
	}

	cpCmd := exec.CommandContext(ctx, "cp", "-r", tmpDir+"/.", localRepoDir)
	if err := cpCmd.Run(); err != nil {
		return fmt.Errorf("copying to local repo: %w", err)
	}

	return nil
}

// Cp copies files to/from a container.
func (r *PodmanRunner) Cp(ctx context.Context, name, src, dest string) error {
	return r.run(ctx, "cp", src, dest)
}

// Stop stops a running container.
func (r *PodmanRunner) Stop(ctx context.Context, name string) error {
	return r.run(ctx, "stop", name)
}

// Down destroys a container.
func (r *PodmanRunner) Down(ctx context.Context, name string) error {
	return r.run(ctx, "rm", "-f", name)
}

// setupCredentials configures API credentials in the container based on the provider.
func (r *PodmanRunner) setupCredentials(ctx context.Context, name string) error {
	switch r.apiProvider {
	case "vertex":
		return r.setupVertexCredentials(ctx, name)
	default:
		return r.setupAnthropicCredentials(ctx, name)
	}
}

// setupAnthropicCredentials copies the Anthropic API key file into the container.
func (r *PodmanRunner) setupAnthropicCredentials(ctx context.Context, name string) error {
	if r.anthropicKey == "" {
		return nil
	}

	keyPath := r.resolveHomePath(r.anthropicKey)

	// Create .anthropic directory and copy key
	mkdirCmd := []string{"bash", "-c", "mkdir -p /home/claude/.anthropic && chown claude:claude /home/claude/.anthropic"}
	if err := r.SSH(ctx, name, mkdirCmd...); err != nil {
		return fmt.Errorf("creating .anthropic dir: %w", err)
	}

	if err := r.Cp(ctx, name, keyPath, name+":/home/claude/.anthropic/api_key"); err != nil {
		return fmt.Errorf("copying API key: %w", err)
	}

	chownCmd := []string{"bash", "-c", "chown claude:claude /home/claude/.anthropic/api_key && chmod 600 /home/claude/.anthropic/api_key"}
	if err := r.SSH(ctx, name, chownCmd...); err != nil {
		return fmt.Errorf("fixing API key permissions: %w", err)
	}

	return nil
}

// setupVertexCredentials copies the GCP ADC credentials file into the container.
func (r *PodmanRunner) setupVertexCredentials(ctx context.Context, name string) error {
	if r.gcpCredentialsFile == "" {
		return fmt.Errorf("gcp_credentials_file is required when api_provider is vertex")
	}

	credsPath := r.resolveHomePath(r.gcpCredentialsFile)

	// Create the gcloud config directory
	mkdirCmd := []string{"bash", "-c", "mkdir -p /home/claude/.config/gcloud && chown -R claude:claude /home/claude/.config/gcloud"}
	if err := r.SSH(ctx, name, mkdirCmd...); err != nil {
		return fmt.Errorf("creating gcloud config dir: %w", err)
	}

	if err := r.Cp(ctx, name, credsPath, name+":/home/claude/.config/gcloud/application_default_credentials.json"); err != nil {
		return fmt.Errorf("copying GCP credentials: %w", err)
	}

	chownCmd := []string{"bash", "-c", "chown claude:claude /home/claude/.config/gcloud/application_default_credentials.json && chmod 600 /home/claude/.config/gcloud/application_default_credentials.json"}
	if err := r.SSH(ctx, name, chownCmd...); err != nil {
		return fmt.Errorf("fixing GCP credentials permissions: %w", err)
	}

	return nil
}

// resolveHomePath expands a ~/ prefix to the current user's home directory.
func (r *PodmanRunner) resolveHomePath(p string) string {
	if strings.HasPrefix(p, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, p[2:])
	}
	return p
}

// APIProvider returns the configured API provider ("anthropic" or "vertex").
func (r *PodmanRunner) APIProvider() string {
	return r.apiProvider
}

// VertexProjectID returns the configured Vertex project ID.
func (r *PodmanRunner) VertexProjectID() string {
	return r.vertexProjectID
}

// VertexRegion returns the configured Vertex region.
func (r *PodmanRunner) VertexRegion() string {
	return r.vertexRegion
}

// VertexModel returns the configured Vertex model.
func (r *PodmanRunner) VertexModel() string {
	return r.vertexModel
}

func (r *PodmanRunner) run(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "podman", args...)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("podman %s: %w", args[0], err)
	}
	return nil
}

func (r *PodmanRunner) runInteractive(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "podman", args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("podman %s: %w", args[0], err)
	}
	return nil
}
