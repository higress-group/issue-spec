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
		{"temporary CODEX_HOME path", cfg.TempCodexHome},
	} {
		if item.name == "temporary CODEX_HOME path" && strings.TrimSpace(item.value) == "" {
			continue
		}
		if strings.TrimSpace(item.value) == "" {
			return Command{}, nil, fmt.Errorf("%w: %s is required", ErrSandboxConfigInvalid, item.name)
		}
		if !filepath.IsAbs(item.value) {
			return Command{}, nil, fmt.Errorf("%w: %s must be absolute: %s", ErrSandboxConfigInvalid, item.name, item.value)
		}
	}
	chdir, err := sandboxWorkingDirectory(target.Dir, cfg.WorkspacePath)
	if err != nil {
		return Command{}, nil, err
	}

	args := []string{"--die-with-parent", "--clearenv"}
	for _, entry := range env {
		name, value, _ := strings.Cut(entry, "=")
		args = append(args, "--setenv", name, value)
	}

	workspacePath := filepath.Clean(cfg.WorkspacePath)
	mounts := []Mount{
		{Source: workspacePath, Destination: "/workspace", Mode: "rw"},
		{Destination: "/tmp", Mode: "tmpfs"},
		{Source: cfg.TempHome, Destination: "/tmp/issue-spec-home", Mode: "rw"},
		{Source: cfg.TempGHConfigDir, Destination: "/tmp/issue-spec-gh", Mode: "rw"},
		{Source: cfg.TempXDGConfigHome, Destination: "/tmp/issue-spec-xdg", Mode: "rw"},
		{Destination: "/proc", Mode: "proc"},
		{Destination: "/dev", Mode: "dev"},
	}

	args = append(args, "--bind", workspacePath, "/workspace", "--perms", "0700", "--tmpfs", "/tmp", "--dir", "/tmp/issue-spec-home", "--bind", cfg.TempHome, "/tmp/issue-spec-home", "--dir", "/tmp/issue-spec-gh", "--bind", cfg.TempGHConfigDir, "/tmp/issue-spec-gh", "--dir", "/tmp/issue-spec-xdg", "--bind", cfg.TempXDGConfigHome, "/tmp/issue-spec-xdg", "--proc", "/proc", "--dev", "/dev")
	if strings.TrimSpace(cfg.TempCodexHome) != "" {
		mounts = append(mounts, Mount{Source: cfg.TempCodexHome, Destination: "/tmp/issue-spec-codex", Mode: "rw"})
		args = append(args, "--dir", "/tmp/issue-spec-codex", "--bind", cfg.TempCodexHome, "/tmp/issue-spec-codex")
	}
	systemBinds := systemReadOnlyBinds(cfg)
	for _, bind := range systemBinds {
		args = append(args, "--ro-bind", bind, bind)
		mounts = append(mounts, Mount{Source: bind, Destination: bind, Mode: "ro"})
	}
	seenDirs := map[string]bool{
		"/":                     true,
		"/tmp":                  true,
		"/workspace":            true,
		"/tmp/issue-spec-home":  true,
		"/tmp/issue-spec-gh":    true,
		"/tmp/issue-spec-xdg":   true,
		"/tmp/issue-spec-codex": true,
		"/proc":                 true,
		"/dev":                  true,
	}
	if workspacePath != "/workspace" {
		args, mounts = appendBindParentDirs(args, mounts, workspacePath, seenDirs, systemBinds)
		args = append(args, "--bind", workspacePath, workspacePath)
		mounts = append(mounts, Mount{Source: workspacePath, Destination: workspacePath, Mode: "rw"})
	}
	coveredRoots := append([]string{}, systemBinds...)
	coveredRoots = append(coveredRoots, workspacePath)
	for _, bind := range readOnlyBinds(cfg) {
		if coveredByMount(bind, coveredRoots) {
			continue
		}
		args, mounts = appendBindParentDirs(args, mounts, bind, seenDirs, coveredRoots)
		args = append(args, "--ro-bind", bind, bind)
		mounts = append(mounts, Mount{Source: bind, Destination: bind, Mode: "ro"})
	}
	args = append(args, "--chdir", chdir)
	args = append(args, "--", target.Binary)
	args = append(args, target.Args...)

	return Command{Binary: bwrapPath, Args: args, Stdin: append([]byte(nil), target.Stdin...)}, mounts, nil
}

func sandboxWorkingDirectory(targetDir, workspacePath string) (string, error) {
	targetDir = strings.TrimSpace(targetDir)
	if targetDir == "" {
		return "/workspace", nil
	}
	if !filepath.IsAbs(targetDir) {
		return "", fmt.Errorf("%w: target working directory must be absolute under bubblewrap: %s", ErrSandboxConfigInvalid, targetDir)
	}
	dir := filepath.Clean(targetDir)
	workspace := filepath.Clean(workspacePath)
	if dir == "/workspace" || dir == workspace || pathInside(workspace, dir) {
		return dir, nil
	}
	return "", fmt.Errorf("%w: target working directory %s is outside workspace %s", ErrSandboxConfigInvalid, dir, workspace)
}

func pathInside(root, path string) bool {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	if root == "/" {
		return path != "/"
	}
	return strings.HasPrefix(path, root+string(os.PathSeparator))
}

func appendBindParentDirs(args []string, mounts []Mount, destination string, seen map[string]bool, coveredRoots []string) ([]string, []Mount) {
	for _, dir := range bindParentDirs(destination) {
		if seen[dir] || coveredByMount(dir, coveredRoots) {
			continue
		}
		seen[dir] = true
		args = append(args, "--dir", dir)
		mounts = append(mounts, Mount{Destination: dir, Mode: "dir"})
	}
	return args, mounts
}

func bindParentDirs(destination string) []string {
	parent := filepath.Dir(filepath.Clean(destination))
	if parent == "." || parent == "/" {
		return nil
	}
	var reversed []string
	for parent != "/" && parent != "." {
		reversed = append(reversed, parent)
		next := filepath.Dir(parent)
		if next == parent {
			break
		}
		parent = next
	}
	dirs := make([]string, 0, len(reversed))
	for i := len(reversed) - 1; i >= 0; i-- {
		dirs = append(dirs, reversed[i])
	}
	return dirs
}

func coveredByMount(path string, roots []string) bool {
	path = filepath.Clean(path)
	for _, root := range roots {
		root = filepath.Clean(strings.TrimSpace(root))
		if root == "." || !filepath.IsAbs(root) || root == "/tmp" {
			continue
		}
		if path == root || pathInside(root, path) {
			return true
		}
	}
	return false
}

func readOnlyBinds(cfg Config) []string {
	return cleanBinds(cfg.ReadOnlyBinds, false)
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
