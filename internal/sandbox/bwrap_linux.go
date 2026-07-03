//go:build linux

package sandbox

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

type Bubblewrap struct {
	Path   string
	Runner Runner
}

func (b Bubblewrap) Preflight(ctx context.Context, opts PreflightOptions) error {
	path := strings.TrimSpace(firstNonEmpty(b.Path, opts.BwrapPath))
	if path == "" {
		return ErrUnsupported
	}
	if _, err := exec.LookPath(path); err != nil && !strings.Contains(path, "/") {
		return fmt.Errorf("locate bwrap %q: %w", path, err)
	}
	if err := ensureHelpSupportsPerms(ctx, path); err != nil {
		return err
	}
	if err := ensureVersionAtLeast(ctx, path, "0.5.0"); err != nil {
		return err
	}
	if err := ensureSmokeTest(ctx, path, opts.Runner, opts.Env); err != nil {
		return err
	}
	return nil
}

func (b Bubblewrap) Run(ctx context.Context, spec CommandSpec) (Result, error) {
	path := strings.TrimSpace(b.Path)
	if path == "" {
		return Result{}, ErrUnsupported
	}
	runner := b.Runner
	if runner == nil {
		runner = execRunnerFunc(func(ctx context.Context, cmd Command) (Result, error) {
			execCmd := exec.CommandContext(ctx, cmd.Binary, cmd.Args...)
			execCmd.Dir = cmd.Dir
			execCmd.Env = cmd.Env
			out, err := execCmd.CombinedOutput()
			return Result{Stdout: out, Err: err}, err
		})
	}
	cfg := NewConfigFromEnv()
	cfg.BwrapPath = path
	cfg.Env = spec.Env
	execRunner := ExecRunner{Config: cfg, Runner: runner}
	return execRunner.RunSandbox(ctx, spec)
}

func ensureHelpSupportsPerms(ctx context.Context, path string) error {
	out, err := exec.CommandContext(ctx, path, "--help").CombinedOutput()
	if err != nil {
		return fmt.Errorf("check bwrap --help: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if !strings.Contains(string(out), "--perms") {
		return fmt.Errorf("bwrap at %s does not support --perms", path)
	}
	return nil
}

func ensureVersionAtLeast(ctx context.Context, path, min string) error {
	out, err := exec.CommandContext(ctx, path, "--version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("check bwrap version: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if compareVersions(strings.TrimSpace(string(out)), min) < 0 {
		return fmt.Errorf("bwrap version %s is older than required %s", strings.TrimSpace(string(out)), min)
	}
	return nil
}

func ensureSmokeTest(ctx context.Context, path string, runner Runner, env []string) error {
	if runner == nil {
		runner = execRunnerFunc(func(context.Context, Command) (Result, error) {
			cmd := exec.CommandContext(ctx, path, "--ro-bind", "/", "/", "--tmpfs", "/tmp", "--", "/bin/true")
			cmd.Env = env
			out, err := cmd.CombinedOutput()
			return Result{Stdout: out, Err: err}, err
		})
	}
	_, err := runner.Run(ctx, Command{Binary: path, Args: []string{"--ro-bind", "/", "/", "--tmpfs", "/tmp", "--", "/bin/true"}, Env: env})
	if err != nil {
		return fmt.Errorf("bwrap smoke test: %w", err)
	}
	return nil
}

type execRunnerFunc func(context.Context, Command) (Result, error)

func (f execRunnerFunc) Run(ctx context.Context, c Command) (Result, error) { return f(ctx, c) }

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func compareVersions(a, b string) int {
	ap := versionParts(a)
	bp := versionParts(b)
	for i := 0; i < len(ap) || i < len(bp); i++ {
		av, bv := 0, 0
		if i < len(ap) {
			av = ap[i]
		}
		if i < len(bp) {
			bv = bp[i]
		}
		if av < bv {
			return -1
		}
		if av > bv {
			return 1
		}
	}
	return 0
}

func versionParts(v string) []int {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	fields := strings.FieldsFunc(v, func(r rune) bool { return r < '0' || r > '9' })
	out := make([]int, 0, len(fields))
	for _, f := range fields {
		n, _ := strconv.Atoi(f)
		out = append(out, n)
	}
	return out
}
