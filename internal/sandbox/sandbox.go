package sandbox

import (
	"context"
	"io"
)

// SSHOpts configures proxy and reverse tunnels for sandbox command execution.
type SSHOpts struct {
	Proxy          bool     // activate credential-injecting proxies
	ReverseTunnels []string // reverse tunnel specs (e.g. "19090:localhost:12345")
}

// Runner defines the interface for sandbox backends (gjoll VMs, podman containers, etc).
type Runner interface {
	// Up provisions a new sandbox.
	// For gjoll: config is the path to .tf file
	// For podman: config is the container image name
	Up(ctx context.Context, name string, config string) error

	// Start starts a stopped sandbox.
	Start(ctx context.Context, name string) error

	// SSH runs a command in the sandbox without proxy/tunnels.
	SSH(ctx context.Context, name string, command ...string) error

	// SSHProxy runs a command in the sandbox with proxy and reverse tunnels.
	// Stdin/stdout/stderr are connected to the current process.
	SSHProxy(ctx context.Context, name string, opts *SSHOpts, command ...string) error

	// SSHProxyOutput runs a command in the sandbox, writing stdout to w.
	SSHProxyOutput(ctx context.Context, name string, w io.Writer, opts *SSHOpts, command ...string) error

	// Pull fetches committed code from the sandbox into a local git repo.
	Pull(ctx context.Context, name, remotePath, localRepoDir string) error

	// Cp copies files to/from a sandbox.
	Cp(ctx context.Context, name, src, dest string) error

	// Stop stops a running sandbox.
	Stop(ctx context.Context, name string) error

	// Down destroys a sandbox and all its resources.
	Down(ctx context.Context, name string) error
}

// NewFromConfig creates the appropriate Runner based on configuration.
func NewFromConfig(backend, gjollEnv, podmanImage, anthropicKeyFile string, mcpPort int) Runner {
	switch backend {
	case "podman":
		return NewPodman(podmanImage, anthropicKeyFile, mcpPort)
	case "gjoll":
		return NewGjollAdapter(gjollEnv)
	default:
		// Default to gjoll for backward compatibility
		return NewGjollAdapter(gjollEnv)
	}
}
