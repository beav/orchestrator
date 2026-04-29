package profile

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/drellabot/orchestrator/internal/sandbox"
)

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

// writeToSandbox writes content to a file in the sandbox via a temp file + cp.
func writeToSandbox(ctx context.Context, runner sandbox.Runner, sbx, content, dest string) error {
	// Ensure the parent directory exists in the sandbox
	runner.SSH(ctx, sbx, "bash", "-c", "su - claude -c 'mkdir -p ~/workspace'")

	tmpFile, err := os.CreateTemp("", "profile-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.WriteString(content); err != nil {
		tmpFile.Close()
		return err
	}
	tmpFile.Close()

	return runner.Cp(ctx, sbx, tmpFile.Name(), dest)
}

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

// runSetup executes setup.sh on the host with helper scripts on PATH.
func runSetup(ctx context.Context, runner sandbox.Runner, sbx, setupPath, taskDir string, vars map[string]string) error {
	// Create a temp directory for helper scripts
	helpersDir, err := os.MkdirTemp("", "profile-helpers-*")
	if err != nil {
		return fmt.Errorf("creating helpers dir: %w", err)
	}
	defer os.RemoveAll(helpersDir)

	gjollBin := "gjoll"

	// Write sandbox-cp helper
	sandboxCp := fmt.Sprintf(`#!/bin/bash
set -euo pipefail
%s cp %s "$1" "$2"
`, gjollBin, sbx)
	if err := os.WriteFile(filepath.Join(helpersDir, "sandbox-cp"), []byte(sandboxCp), 0755); err != nil {
		return fmt.Errorf("writing sandbox-cp: %w", err)
	}

	// Write sandbox-ssh helper
	sandboxSSH := fmt.Sprintf(`#!/bin/bash
set -euo pipefail
%s ssh %s -- "$@"
`, gjollBin, sbx)
	if err := os.WriteFile(filepath.Join(helpersDir, "sandbox-ssh"), []byte(sandboxSSH), 0755); err != nil {
		return fmt.Errorf("writing sandbox-ssh: %w", err)
	}

	// Build environment
	env := os.Environ()
	env = append(env,
		"SANDBOX="+sbx,
		"TASK_DIR="+taskDir,
		"PATH="+helpersDir+":"+os.Getenv("PATH"),
	)
	for k, v := range vars {
		env = append(env, k+"="+v)
	}

	cmd := exec.CommandContext(ctx, "bash", setupPath)
	cmd.Env = env
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("setup.sh failed: %w", err)
	}

	return nil
}
