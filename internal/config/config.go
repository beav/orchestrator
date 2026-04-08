package config

import (
	"fmt"
	"os"
	"path"

	"github.com/goccy/go-yaml"
)

// DaemonConfig holds settings for the daemon polling loop.
type DaemonConfig struct {
	PollInterval      string   `yaml:"poll_interval"`
	AllowedCommenters []string `yaml:"allowed_commenters"`
	TasksRepo         string   `yaml:"tasks_repo"`
}

type Config struct {
	SlackWebhook string       `yaml:"slack_webhook"`
	OutputDir    string       `yaml:"output_dir"`
	
	// Sandbox backend: "gjoll" (VMs) or "podman" (containers)
	SandboxBackend string `yaml:"sandbox_backend"`
	
	// Gjoll backend settings
	GjollEnv     string   `yaml:"gjoll_env"`      // path to .tf file for VM provisioning
	
	// Podman backend settings  
	PodmanImage      string `yaml:"podman_image"`       // container image (e.g. "fedora:43")
	AnthropicKeyFile string `yaml:"anthropic_key_file"` // path to API key for mounting
	
	AllowedRepos []string     `yaml:"allowed_repos"`
	ProfilesRepo string       `yaml:"profiles_repo"`
	ProfilesDir  string       `yaml:"profiles_dir"`
	Daemon       DaemonConfig `yaml:"daemon"`
}

// RepoAllowed reports whether repo is permitted by the AllowedRepos allowlist.
// Each entry may contain wildcards understood by path.Match (e.g. "org/*").
// An empty list denies all repos.
func (c *Config) RepoAllowed(repo string) bool {
	for _, pattern := range c.AllowedRepos {
		if matched, _ := path.Match(pattern, repo); matched {
			return true
		}
	}
	return false
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	cfg := &Config{}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parsing config file: %w", err)
	}

	// Defaults
	if cfg.OutputDir == "" {
		cfg.OutputDir = "./tasks"
	}
	if cfg.SandboxBackend == "" {
		cfg.SandboxBackend = "gjoll"  // default to gjoll for backward compat
	}
	if cfg.GjollEnv == "" {
		cfg.GjollEnv = "./configs/sandbox.tf"
	}
	if cfg.PodmanImage == "" {
		cfg.PodmanImage = "fedora:43"
	}
	if cfg.AnthropicKeyFile == "" {
		cfg.AnthropicKeyFile = "~/.anthropic/api_key"
	}

	return cfg, nil
}
