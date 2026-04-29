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
