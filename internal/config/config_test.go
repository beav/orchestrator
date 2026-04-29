package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLoad(t *testing.T) {
	tests := []struct {
		name      string
		yaml      string
		writeFile bool
		want      Config
		wantErr   bool
	}{
		{
			name:      "full config",
			writeFile: true,
			yaml:      "slack_webhook: https://hooks.slack.com/test\noutput_dir: /tmp/tasks\ngjoll_env: /path/to/sandbox.tf\n",
			want: Config{
				SlackWebhook:     "https://hooks.slack.com/test",
				OutputDir:        "/tmp/tasks",
				SandboxBackend:   "gjoll",
				GjollEnv:         "/path/to/sandbox.tf",
				PodmanImage:      "fedora:43",
				AnthropicKeyFile: "~/.anthropic/api_key",
			},
		},
		{
			name:      "defaults applied",
			writeFile: true,
			yaml:      "slack_webhook: https://hooks.slack.com/test\n",
			want: Config{
				SlackWebhook:     "https://hooks.slack.com/test",
				OutputDir:        "./tasks",
				SandboxBackend:   "gjoll",
				GjollEnv:         "./configs/sandbox.tf",
				PodmanImage:      "fedora:43",
				AnthropicKeyFile: "~/.anthropic/api_key",
			},
		},
		{
			name:      "empty file uses all defaults",
			writeFile: true,
			yaml:      "",
			want: Config{
				OutputDir:        "./tasks",
				SandboxBackend:   "gjoll",
				GjollEnv:         "./configs/sandbox.tf",
				PodmanImage:      "fedora:43",
				AnthropicKeyFile: "~/.anthropic/api_key",
			},
		},
		{
			name:      "allowed_repos parsed",
			writeFile: true,
			yaml:      "allowed_repos:\n  - osbuild/osbuild\n  - drellabot/*\n",
			want: Config{
				OutputDir:        "./tasks",
				SandboxBackend:   "gjoll",
				GjollEnv:         "./configs/sandbox.tf",
				PodmanImage:      "fedora:43",
				AnthropicKeyFile: "~/.anthropic/api_key",
				AllowedRepos:     []string{"osbuild/osbuild", "drellabot/*"},
			},
		},
		{
			name:      "daemon config parsed",
			writeFile: true,
			yaml:      "daemon:\n  poll_interval: \"30s\"\n  allowed_commenters:\n    - alice\n    - bob\n",
			want: Config{
				OutputDir:        "./tasks",
				SandboxBackend:   "gjoll",
				GjollEnv:         "./configs/sandbox.tf",
				PodmanImage:      "fedora:43",
				AnthropicKeyFile: "~/.anthropic/api_key",
				Daemon: DaemonConfig{
					PollInterval:      "30s",
					AllowedCommenters: []string{"alice", "bob"},
				},
			},
		},
		{
			name:      "profiles config parsed",
			writeFile: true,
			yaml:      "profiles_repo: drellabot/profiles\nprofiles_dir: /tmp/profiles\n",
			want: Config{
				OutputDir:        "./tasks",
				SandboxBackend:   "gjoll",
				GjollEnv:         "./configs/sandbox.tf",
				PodmanImage:      "fedora:43",
				AnthropicKeyFile: "~/.anthropic/api_key",
				ProfilesRepo:     "drellabot/profiles",
				ProfilesDir:      "/tmp/profiles",
			},
		},
		{
			name:      "invalid yaml",
			writeFile: true,
			yaml:      "{{invalid",
			wantErr:   true,
		},
		{
			name:      "missing file",
			writeFile: false,
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.yaml")
			if tt.writeFile {
				if err := os.WriteFile(path, []byte(tt.yaml), 0644); err != nil {
					t.Fatal(err)
				}
			}

			got, err := Load(path)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(*got, tt.want) {
				t.Errorf("got %+v, want %+v", *got, tt.want)
			}
		})
	}
}

func TestRepoAllowed(t *testing.T) {
	tests := []struct {
		name         string
		allowedRepos []string
		repo         string
		want         bool
	}{
		{
			name:         "exact match",
			allowedRepos: []string{"osbuild/osbuild"},
			repo:         "osbuild/osbuild",
			want:         true,
		},
		{
			name:         "wildcard match",
			allowedRepos: []string{"drellabot/*"},
			repo:         "drellabot/orchestrator",
			want:         true,
		},
		{
			name:         "no match",
			allowedRepos: []string{"osbuild/osbuild"},
			repo:         "evil/repo",
			want:         false,
		},
		{
			name:         "empty list denies all",
			allowedRepos: nil,
			repo:         "osbuild/osbuild",
			want:         false,
		},
		{
			name:         "wildcard does not cross slash",
			allowedRepos: []string{"org/*"},
			repo:         "org/a/b",
			want:         false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{AllowedRepos: tt.allowedRepos}
			if got := cfg.RepoAllowed(tt.repo); got != tt.want {
				t.Errorf("RepoAllowed(%q) = %v, want %v", tt.repo, got, tt.want)
			}
		})
	}
}
