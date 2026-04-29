package cmd

import (
	"strings"
	"testing"
)

func TestParseVarFlags(t *testing.T) {
	tests := []struct {
		name  string
		flags []string
		want  map[string]string
	}{
		{
			name:  "nil flags",
			flags: nil,
			want:  nil,
		},
		{
			name:  "empty flags",
			flags: []string{},
			want:  nil,
		},
		{
			name:  "single var",
			flags: []string{"PROFILE_PR=42"},
			want:  map[string]string{"PROFILE_PR": "42"},
		},
		{
			name:  "multiple vars",
			flags: []string{"PROFILE_REPO=org/repo", "PROFILE_PR=42"},
			want:  map[string]string{"PROFILE_REPO": "org/repo", "PROFILE_PR": "42"},
		},
		{
			name:  "value with equals sign",
			flags: []string{"KEY=a=b=c"},
			want:  map[string]string{"KEY": "a=b=c"},
		},
		{
			name:  "no equals sign is skipped",
			flags: []string{"NOEQUALS"},
			want:  map[string]string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseVarFlags(tt.flags)
			if tt.want == nil {
				if got != nil {
					t.Errorf("parseVarFlags(%v) = %v, want nil", tt.flags, got)
				}
				return
			}
			if len(got) != len(tt.want) {
				t.Fatalf("parseVarFlags(%v) has %d entries, want %d", tt.flags, len(got), len(tt.want))
			}
			for k, wantV := range tt.want {
				if got[k] != wantV {
					t.Errorf("parseVarFlags(%v)[%q] = %q, want %q", tt.flags, k, got[k], wantV)
				}
			}
		})
	}
}

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
				"claude --",
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
