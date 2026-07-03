//go:build linux

package sandbox

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

func preflightBwrap(ctx context.Context, cfg Config, envMeta EnvMetadata, deps Dependencies) (Metadata, error) {
	deps = deps.withDefaults()
	meta := Metadata{
		SandboxEnabled:    true,
		SandboxProvider:   ProviderBubblewrap,
		FSBoundary:        FSBoundaryWorkspace,
		Platform:          runtime.GOOS,
		PlatformSupported: true,
		Env:               envMeta,
	}
	fail := func(kind error, err error) (Metadata, error) {
		meta.Diagnostics = append(meta.Diagnostics, err.Error(), installOrUnsafeHint())
		return meta, fmt.Errorf("%w: %w: %s", ErrSandboxPreflightFailed, kind, installOrUnsafeHint())
	}

	path, source, err := locateBwrap(cfg, deps)
	if err != nil {
		return fail(ErrBubblewrapUnavailable, err)
	}
	meta.BwrapPath = path
	meta.BwrapPathSource = source

	version, err := probeBwrapVersion(ctx, deps.Runner, path)
	if err != nil {
		return fail(ErrBubblewrapUnsupported, err)
	}
	meta.BwrapVersion = version
	minVersion := configMinVersion(cfg)
	if !versionAtLeast(version, minVersion) {
		return fail(ErrBubblewrapUnsupported, fmt.Errorf("bubblewrap version %s is older than required %s", version, minVersion))
	}

	perms, err := probeBwrapPerms(ctx, deps.Runner, path)
	if err != nil {
		return fail(ErrBubblewrapUnsupported, err)
	}
	meta.BwrapPermsSupported = perms
	if !perms {
		return fail(ErrBubblewrapUnsupported, fmt.Errorf("bubblewrap help does not advertise required --perms support"))
	}

	if err := probeBwrapSmoke(ctx, deps.Runner, path, cfg); err != nil {
		return fail(ErrBubblewrapUnsupported, err)
	}
	meta.BwrapSmokeTest = true
	return meta, nil
}

func locateBwrap(cfg Config, deps Dependencies) (string, string, error) {
	if path := strings.TrimSpace(cfg.BwrapPath); path != "" {
		return path, "config", nil
	}
	hostEnv := cfg.HostEnv
	if hostEnv == nil {
		hostEnv = os.Environ()
	}
	for _, entry := range hostEnv {
		name, value, ok := strings.Cut(entry, "=")
		if ok && name == BwrapPathEnv && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value), BwrapPathEnv, nil
		}
	}
	path, err := deps.LookPath("bwrap")
	if err != nil {
		return "", "", fmt.Errorf("bubblewrap binary was not found from config, %s, or PATH", BwrapPathEnv)
	}
	return path, "PATH", nil
}

func probeBwrapVersion(ctx context.Context, runner Runner, path string) (string, error) {
	result, err := runner.Run(ctx, Command{Binary: path, Args: []string{"--version"}})
	if err != nil || result.ExitCode != 0 {
		return "", fmt.Errorf("bubblewrap --version failed: %s", commandOutputSummary(result, err))
	}
	version, ok := parseBwrapVersion(string(result.Stdout) + "\n" + string(result.Stderr))
	if !ok {
		return "", fmt.Errorf("bubblewrap --version did not include a parseable version")
	}
	return version, nil
}

func probeBwrapPerms(ctx context.Context, runner Runner, path string) (bool, error) {
	result, err := runner.Run(ctx, Command{Binary: path, Args: []string{"--help"}})
	if err != nil || result.ExitCode != 0 {
		return false, fmt.Errorf("bubblewrap --help failed: %s", commandOutputSummary(result, err))
	}
	return strings.Contains(string(result.Stdout)+"\n"+string(result.Stderr), "--perms"), nil
}

func probeBwrapSmoke(ctx context.Context, runner Runner, path string, cfg Config) error {
	args := []string{"--die-with-parent", "--clearenv", "--setenv", "PATH", "/usr/bin:/bin", "--perms", "0700", "--tmpfs", "/tmp", "--proc", "/proc", "--dev", "/dev"}
	for _, bind := range systemReadOnlyBinds(cfg) {
		args = append(args, "--ro-bind", bind, bind)
	}
	args = append(args, "--", "/usr/bin/env", "true")
	result, err := runner.Run(ctx, Command{Binary: path, Args: args})
	if err != nil || result.ExitCode != 0 {
		return fmt.Errorf("bubblewrap smoke test failed: %s", commandOutputSummary(result, err))
	}
	return nil
}

func buildBwrapCommand(cfg Config, target Command, env []string, bwrapPath string) (Command, []Mount, error) {
	if strings.TrimSpace(bwrapPath) == "" {
		return Command{}, nil, fmt.Errorf("%w: bwrap path is required", ErrSandboxConfigInvalid)
	}
	for _, item := range []struct {
		name  string
		value string
	}{
		{"workspace path", cfg.WorkspacePath},
		{"temporary HOME path", cfg.TempHome},
		{"temporary GH_CONFIG_DIR path", cfg.TempGHConfigDir},
		{"temporary XDG_CONFIG_HOME path", cfg.TempXDGConfigHome},
	} {
		if strings.TrimSpace(item.value) == "" {
			return Command{}, nil, fmt.Errorf("%w: %s is required", ErrSandboxConfigInvalid, item.name)
		}
		if !filepath.IsAbs(item.value) {
			return Command{}, nil, fmt.Errorf("%w: %s must be absolute: %s", ErrSandboxConfigInvalid, item.name, item.value)
		}
	}

	args := []string{"--die-with-parent", "--clearenv"}
	for _, entry := range env {
		name, value, _ := strings.Cut(entry, "=")
		args = append(args, "--setenv", name, value)
	}

	mounts := []Mount{
		{Source: cfg.WorkspacePath, Destination: "/workspace", Mode: "rw"},
		{Destination: "/tmp", Mode: "tmpfs"},
		{Source: cfg.TempHome, Destination: "/tmp/issue-spec-home", Mode: "rw"},
		{Source: cfg.TempGHConfigDir, Destination: "/tmp/issue-spec-gh", Mode: "rw"},
		{Source: cfg.TempXDGConfigHome, Destination: "/tmp/issue-spec-xdg", Mode: "rw"},
		{Destination: "/proc", Mode: "proc"},
		{Destination: "/dev", Mode: "dev"},
	}

	args = append(args, "--bind", cfg.WorkspacePath, "/workspace", "--chdir", "/workspace", "--perms", "0700", "--tmpfs", "/tmp", "--dir", "/tmp/issue-spec-home", "--bind", cfg.TempHome, "/tmp/issue-spec-home", "--dir", "/tmp/issue-spec-gh", "--bind", cfg.TempGHConfigDir, "/tmp/issue-spec-gh", "--dir", "/tmp/issue-spec-xdg", "--bind", cfg.TempXDGConfigHome, "/tmp/issue-spec-xdg", "--proc", "/proc", "--dev", "/dev")
	for _, bind := range systemReadOnlyBinds(cfg) {
		args = append(args, "--ro-bind", bind, bind)
		mounts = append(mounts, Mount{Source: bind, Destination: bind, Mode: "ro"})
	}
	args = append(args, "--", target.Binary)
	args = append(args, target.Args...)

	return Command{Binary: bwrapPath, Args: args, Stdin: append([]byte(nil), target.Stdin...)}, mounts, nil
}

func systemReadOnlyBinds(cfg Config) []string {
	if len(cfg.SystemReadOnlyBinds) > 0 {
		return cleanBinds(cfg.SystemReadOnlyBinds, false)
	}
	return cleanBinds([]string{"/usr", "/bin", "/lib", "/lib64", "/etc/ssl/certs", "/etc/pki", "/etc/alternatives"}, true)
}

func cleanBinds(paths []string, existingOnly bool) []string {
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		path = filepath.Clean(strings.TrimSpace(path))
		if path == "." || !filepath.IsAbs(path) {
			continue
		}
		if existingOnly {
			if _, err := os.Stat(path); err != nil {
				continue
			}
		}
		out = append(out, path)
	}
	return sortedUnique(out)
}
