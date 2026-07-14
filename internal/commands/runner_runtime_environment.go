package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/higress-group/issue-spec/internal/commentrunner"
	"github.com/higress-group/issue-spec/internal/commentrunner/credentials"
	"github.com/higress-group/issue-spec/internal/sandbox"
	"github.com/higress-group/issue-spec/internal/workspace"
)

var currentRunnerHostSSHConfig = credentials.CurrentUserHostSSHGitProviderConfig
var newRunnerHostSSHProvider = credentials.NewHostSSHGitProvider

// runnerSandboxRuntimeConfig is the shared host-facing sandbox configuration
// for live dispatch and the opt-in runtime preflight. Runtime-local HOME,
// CODEX_HOME, ACPX override, profile, and capability paths are added by
// jobs.SandboxRunner for the concrete session or preflight workspace.
func runnerSandboxRuntimeConfig(cfg commentrunner.Config) (sandbox.Config, credentials.GitProvider, error) {
	cfg = cfg.Normalized()
	sandboxConfig := sandbox.Config{
		UnsafeNoSandbox: cfg.UnsafeNoSandbox,
		BwrapPath:       cfg.BwrapPath,
		HostGHConfigDir: cfg.GHConfigDir,
		HostEnv:         os.Environ(),
	}
	if !cfg.AllowHostSSH {
		return sandboxConfig, nil, nil
	}
	hostConfig, err := currentRunnerHostSSHConfig(os.Getenv("SSH_AUTH_SOCK"))
	if err != nil {
		return sandbox.Config{}, nil, err
	}
	provider, err := newRunnerHostSSHProvider(hostConfig)
	if err != nil {
		return sandbox.Config{}, nil, err
	}
	sandboxConfig.HostSSHDir = hostConfig.SSHDir
	sandboxConfig.HostSSHAgentSocket = hostConfig.AgentSocket
	return sandboxConfig, provider, nil
}

func runnerRuntimeHostHome(cfg sandbox.Config) (string, error) {
	if sshDir := strings.TrimSpace(cfg.HostSSHDir); sshDir != "" {
		return filepath.Dir(filepath.Clean(sshDir)), nil
	}
	for _, entry := range cfg.HostEnv {
		name, value, ok := strings.Cut(entry, "=")
		if ok && name == "HOME" && strings.TrimSpace(value) != "" {
			return filepath.Clean(value), nil
		}
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return "", fmt.Errorf("resolve runner host HOME")
	}
	return filepath.Clean(home), nil
}

func runnerWorkspaceManager(cfg commentrunner.Config) workspace.Manager {
	cfg = cfg.Normalized()
	return workspace.Manager{
		Root:           cfg.WorkspaceRoot,
		Retention:      cfg.WorkspaceRetention.Duration,
		GitAuthorName:  cfg.GitAuthorName,
		GitAuthorEmail: cfg.GitAuthorEmail,
	}
}
