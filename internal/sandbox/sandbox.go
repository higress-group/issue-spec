package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	DefaultWorkspacePath = "/workspace"
	DefaultTmpPath        = "/tmp"
)

var ErrUnsupported = errors.New("issue-spec runner sandbox requires Linux and bubblewrap with --perms support")

type Runner interface {
	Run(context.Context, Command) (Result, error)
}

type Command struct {
	Binary string
	Args   []string
	Env    []string
	Dir    string
}

type Result struct {
	Stdout []byte
	Stderr []byte
	Err    error
}

type Config struct {
	BwrapPath string
	Workspace string
	TmpDir    string
	HomeDir   string
	GHConfig  string
	XDGConfig string
	Env       []string
}

type CommandSpec struct {
	Binary string
	Args   []string
	Dir    string
	Env    []string
}

type ExecRunner struct {
	Config Config
	Runner  Runner
}

type UnsafeResult struct {
	Command Command
	Markers map[string]string
}

type PreflightOptions struct {
	BwrapPath string
	Runner    Runner
	Env       []string
}

func NewConfigFromEnv() Config {
	tmp := os.TempDir()
	home := filepath.Join(tmp, "issue-spec-home")
	gh := filepath.Join(tmp, "issue-spec-gh-config")
	xdg := filepath.Join(tmp, "issue-spec-xdg-config")
	return Config{
		BwrapPath: os.Getenv("ISSUE_SPEC_BWRAP_PATH"),
		Workspace: DefaultWorkspacePath,
		TmpDir:    tmp,
		HomeDir:   home,
		GHConfig:  gh,
		XDGConfig: xdg,
		Env:       os.Environ(),
	}
}

func (r ExecRunner) RunSandbox(ctx context.Context, spec CommandSpec) (Result, error) {
	path := strings.TrimSpace(r.Config.BwrapPath)
	if path == "" {
		return Result{}, fmt.Errorf("bwrap path is required")
	}
	cmd := Command{
		Binary: path,
		Args:   buildArgs(r.Config, spec),
		Env:    buildEnv(r.Config.Env, r.Config.HomeDir, r.Config.GHConfig, r.Config.XDGConfig),
		Dir:    r.Config.Workspace,
	}
	if r.Runner == nil {
		return Result{}, fmt.Errorf("sandbox runner is not configured")
	}
	return r.Runner.Run(ctx, cmd)
}

func (r ExecRunner) BuildUnsafeNoSandbox(spec CommandSpec) (UnsafeResult, error) {
	if strings.TrimSpace(r.Config.Workspace) == "" {
		r.Config.Workspace = DefaultWorkspacePath
	}
	cmd := Command{
		Binary: spec.Binary,
		Args:   spec.Args,
		Dir:    firstNonEmpty(spec.Dir, r.Config.Workspace),
		Env:    buildEnv(r.Config.Env, r.Config.HomeDir, r.Config.GHConfig, r.Config.XDGConfig),
	}
	return UnsafeResult{
		Command: cmd,
		Markers: map[string]string{
			"sandbox_provider": "none",
			"fs_boundary":      "disabled",
		},
	}, nil
}

func buildArgs(cfg Config, spec CommandSpec) []string {
	args := []string{
		"--clearenv",
		"--setenv", "HOME", cfg.HomeDir,
		"--setenv", "GH_CONFIG_DIR", cfg.GHConfig,
		"--setenv", "XDG_CONFIG_HOME", cfg.XDGConfig,
		"--bind", cfg.Workspace, DefaultWorkspacePath,
		"--tmpfs", DefaultTmpPath,
		"--ro-bind", "/usr", "/usr",
		"--ro-bind", "/bin", "/bin",
		"--ro-bind", "/lib", "/lib",
		"--ro-bind", "/lib64", "/lib64",
		"--ro-bind", "/etc", "/etc",
	}
	args = append(args, unsetTokenArgs()...)
	args = append(args, "--chdir", DefaultWorkspacePath, "--")
	args = append(args, spec.Binary)
	args = append(args, spec.Args...)
	return args
}

func buildEnv(hostEnv []string, home, ghConfig, xdgConfig string) []string {
	env := []string{
		"HOME=" + home,
		"GH_CONFIG_DIR=" + ghConfig,
		"XDG_CONFIG_HOME=" + xdgConfig,
	}
	for _, key := range []string{"http_proxy", "https_proxy", "HTTP_PROXY", "HTTPS_PROXY", "no_proxy", "NO_PROXY"} {
		if value, ok := lookupEnv(hostEnv, key); ok {
			env = append(env, key+"="+value)
		}
	}
	return env
}

func unsetTokenArgs() []string {
	return []string{"--unsetenv", "GH_TOKEN", "--unsetenv", "GITHUB_TOKEN", "--unsetenv", "ISSUE_SPEC_TOKEN"}
}

func lookupEnv(env []string, key string) (string, bool) {
	prefix := key + "="
	for _, item := range env {
		if strings.HasPrefix(item, prefix) {
			return strings.TrimPrefix(item, prefix), true
		}
	}
	return "", false
}
