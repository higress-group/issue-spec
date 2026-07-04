//go:build linux

package sandbox

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestLinuxPreflightResolvesPathAndRunsCapabilityChecks(t *testing.T) {
	runner := runnerFunc(func(ctx context.Context, command Command) (Result, error) {
		_ = ctx
		switch {
		case len(command.Args) == 1 && command.Args[0] == "--version":
			return Result{Stdout: []byte("bubblewrap 0.8.0\n")}, nil
		case len(command.Args) == 1 && command.Args[0] == "--help":
			return Result{Stdout: []byte("usage: bwrap --perms OCTAL ...\n")}, nil
		default:
			if !argsContain(command.Args, "--perms") {
				t.Fatalf("smoke test did not use --perms: %v", command.Args)
			}
			return Result{}, nil
		}
	})

	meta, err := Preflight(context.Background(), Config{
		HostEnv: []string{BwrapPathEnv + "=/opt/bwrap"},
	}, Dependencies{
		LookPath: func(string) (string, error) {
			t.Fatal("PATH lookup should not run when ISSUE_SPEC_BWRAP_PATH is set")
			return "", nil
		},
		Runner: runner,
	})
	if err != nil {
		t.Fatalf("Preflight returned error: %v", err)
	}
	if meta.BwrapPath != "/opt/bwrap" || meta.BwrapPathSource != BwrapPathEnv {
		t.Fatalf("unexpected bwrap path metadata: %+v", meta)
	}
	if meta.BwrapVersion != "0.8.0" || !meta.BwrapPermsSupported || !meta.BwrapSmokeTest {
		t.Fatalf("capability metadata missing: %+v", meta)
	}
}

func TestLinuxPreflightMissingBwrapFailsSafely(t *testing.T) {
	meta, err := Preflight(context.Background(), Config{HostEnv: []string{}}, Dependencies{
		LookPath: func(string) (string, error) { return "", errors.New("not found") },
	})
	if !errors.Is(err, ErrSandboxPreflightFailed) || !errors.Is(err, ErrBubblewrapUnavailable) {
		t.Fatalf("Preflight error = %v, want preflight unavailable", err)
	}
	if meta.SandboxProvider != ProviderBubblewrap || meta.FSBoundary != FSBoundaryWorkspace {
		t.Fatalf("unexpected failure metadata: %+v", meta)
	}
	if !strings.Contains(err.Error(), "--unsafe-no-sandbox") {
		t.Fatalf("error did not include unsafe hint: %v", err)
	}
}

func TestLinuxPreflightRejectsOldOrMissingPermsBwrap(t *testing.T) {
	for _, tt := range []struct{ output, help string }{
		{"bubblewrap 0.4.9\n", "usage: --perms\n"},
		{"bubblewrap 0.8.0\n", "usage\n"},
	} {
		runner := runnerFunc(func(ctx context.Context, command Command) (Result, error) {
			_ = ctx
			switch {
			case len(command.Args) == 1 && command.Args[0] == "--version":
				return Result{Stdout: []byte(tt.output)}, nil
			case len(command.Args) == 1 && command.Args[0] == "--help":
				return Result{Stdout: []byte(tt.help)}, nil
			default:
				return Result{}, nil
			}
		})
		_, err := Preflight(context.Background(), Config{BwrapPath: "/usr/bin/bwrap"}, Dependencies{Runner: runner})
		if !errors.Is(err, ErrSandboxPreflightFailed) || !errors.Is(err, ErrBubblewrapUnsupported) {
			t.Fatalf("Preflight error = %v, want unsupported", err)
		}
	}
}

func TestLinuxPrepareBuildsBwrapCommand(t *testing.T) {
	runner := runnerFunc(func(ctx context.Context, command Command) (Result, error) {
		_ = ctx
		switch {
		case len(command.Args) == 1 && command.Args[0] == "--version":
			return Result{Stdout: []byte("bubblewrap 0.8.0\n")}, nil
		case len(command.Args) == 1 && command.Args[0] == "--help":
			return Result{Stdout: []byte("usage: --perms\n")}, nil
		default:
			return Result{}, nil
		}
	})
	cfg := Config{
		BwrapPath:           "/usr/bin/bwrap",
		WorkspacePath:       "/tmp/workspace",
		TempHome:            "/tmp/home",
		TempGHConfigDir:     "/tmp/gh",
		TempXDGConfigHome:   "/tmp/xdg",
		TempCodexHome:       "/tmp/codex",
		HostEnv:             []string{"PATH=/usr/bin", "HTTPS_PROXY=http://proxy", "GH_TOKEN=secret"},
		SystemReadOnlyBinds: []string{"/usr"},
	}
	prepared, err := Prepare(context.Background(), cfg, Command{Binary: "acpx", Args: []string{"run"}, Stdin: []byte("prompt")}, Dependencies{Runner: runner})
	if err != nil {
		t.Fatalf("Prepare returned error: %v", err)
	}
	cmd := prepared.Command
	if cmd.Binary != "/usr/bin/bwrap" {
		t.Fatalf("Binary = %q, want bwrap path", cmd.Binary)
	}
	assertArgSequence(t, cmd.Args, "--clearenv")
	assertArgSequence(t, cmd.Args, "--bind", "/tmp/workspace", "/workspace")
	assertArgSequence(t, cmd.Args, "--bind", "/tmp/workspace", "/tmp/workspace")
	assertArgSequence(t, cmd.Args, "--chdir", "/workspace")
	assertArgSequence(t, cmd.Args, "--perms", "0700", "--tmpfs", "/tmp")
	assertArgSequence(t, cmd.Args, "--bind", "/tmp/gh", "/tmp/issue-spec-gh")
	assertArgSequence(t, cmd.Args, "--bind", "/tmp/codex", "/tmp/issue-spec-codex")
	assertArgSequence(t, cmd.Args, "--ro-bind", "/usr", "/usr")
	assertArgSequence(t, cmd.Args, "--setenv", "HOME", "/tmp/issue-spec-home")
	assertArgSequence(t, cmd.Args, "--setenv", "GH_CONFIG_DIR", "/tmp/issue-spec-gh")
	assertArgSequence(t, cmd.Args, "--setenv", "CODEX_HOME", "/tmp/issue-spec-codex")
	assertArgSequence(t, cmd.Args, "--setenv", "HTTPS_PROXY", "http://proxy")
	assertArgSequence(t, cmd.Args, "--", "acpx", "run")
	if argsContain(cmd.Args, "--unshare-net") {
		t.Fatalf("sandbox command must not unshare network by default: %v", cmd.Args)
	}
	if prepared.Metadata.SandboxProvider != ProviderBubblewrap || prepared.Metadata.FSBoundary != FSBoundaryWorkspace {
		t.Fatalf("unexpected metadata: %+v", prepared.Metadata)
	}
}

func TestLinuxPrepareDefaultBindsResolverConfig(t *testing.T) {
	runner := capableBwrapRunner(t)
	cfg := Config{
		BwrapPath:         "/usr/bin/bwrap",
		WorkspacePath:     "/tmp/workspace",
		TempHome:          "/tmp/home",
		TempGHConfigDir:   "/tmp/gh",
		TempXDGConfigHome: "/tmp/xdg",
		HostEnv:           []string{"PATH=/usr/bin"},
	}
	prepared, err := Prepare(context.Background(), cfg, Command{Binary: "gh", Args: []string{"auth", "status"}, Dir: "/tmp/workspace"}, Dependencies{Runner: runner})
	if err != nil {
		t.Fatalf("Prepare returned error: %v", err)
	}
	for _, path := range []string{"/etc/resolv.conf", "/etc/hosts", "/etc/nsswitch.conf"} {
		if _, err := os.Stat(path); err != nil {
			assertArgSequenceMissing(t, prepared.Command.Args, "--ro-bind", path, path)
			continue
		}
		assertArgSequence(t, prepared.Command.Args, "--ro-bind", path, path)
		assertMount(t, prepared.Metadata.Mounts, Mount{Source: path, Destination: path, Mode: "ro"})
	}
}

func TestLinuxPrepareBindsWorkspaceAtOriginalPathForHostCWD(t *testing.T) {
	runner := capableBwrapRunner(t)
	workspacePath := "/tmp/issue-spec-runner/workspace"
	cfg := Config{
		BwrapPath:           "/usr/bin/bwrap",
		WorkspacePath:       workspacePath,
		TempHome:            "/tmp/home",
		TempGHConfigDir:     "/tmp/gh",
		TempXDGConfigHome:   "/tmp/xdg",
		TempCodexHome:       "/tmp/codex",
		HostEnv:             []string{"PATH=/usr/bin"},
		SystemReadOnlyBinds: []string{"/usr"},
	}
	prepared, err := Prepare(context.Background(), cfg, Command{
		Binary: "acpx",
		Args:   []string{"--cwd", workspacePath, "codex", "set-mode", "agent-full-access"},
		Dir:    workspacePath,
	}, Dependencies{Runner: runner})
	if err != nil {
		t.Fatalf("Prepare returned error: %v", err)
	}
	assertArgSequence(t, prepared.Command.Args, "--bind", workspacePath, "/workspace")
	assertArgSequence(t, prepared.Command.Args, "--dir", "/tmp/issue-spec-runner")
	assertArgSequence(t, prepared.Command.Args, "--bind", workspacePath, workspacePath)
	assertArgSequence(t, prepared.Command.Args, "--chdir", workspacePath)
	assertArgSequence(t, prepared.Command.Args, "--", "acpx", "--cwd", workspacePath)
	assertArgSequenceMissing(t, prepared.Command.Args, "--bind", "/tmp/issue-spec-runner", "/tmp/issue-spec-runner")
	assertArgSequenceMissing(t, prepared.Command.Args, "--ro-bind", "/tmp", "/tmp")
}

func TestLinuxPrepareAddsReadOnlyFileBindWithTmpfsParents(t *testing.T) {
	runner := capableBwrapRunner(t)
	issueSpecPath := "/tmp/issue-spec-runner-e2e-001/bin/issue-spec"
	systemIssueSpecPath := "/usr/local/bin/issue-spec"
	cfg := Config{
		BwrapPath:           "/usr/bin/bwrap",
		WorkspacePath:       "/tmp/workspace",
		TempHome:            "/tmp/home",
		TempGHConfigDir:     "/tmp/gh",
		TempXDGConfigHome:   "/tmp/xdg",
		TempCodexHome:       "/tmp/codex",
		HostEnv:             []string{"PATH=/usr/bin"},
		SystemReadOnlyBinds: []string{"/usr"},
		ReadOnlyBinds:       []string{issueSpecPath, systemIssueSpecPath},
	}
	prepared, err := Prepare(context.Background(), cfg, Command{Binary: issueSpecPath, Args: []string{"auth", "status", "--json"}, Dir: "/tmp/workspace"}, Dependencies{Runner: runner})
	if err != nil {
		t.Fatalf("Prepare returned error: %v", err)
	}
	assertArgSequence(t, prepared.Command.Args, "--dir", "/tmp/issue-spec-runner-e2e-001")
	assertArgSequence(t, prepared.Command.Args, "--dir", "/tmp/issue-spec-runner-e2e-001/bin")
	assertArgSequence(t, prepared.Command.Args, "--ro-bind", issueSpecPath, issueSpecPath)
	assertArgSequence(t, prepared.Command.Args, "--", issueSpecPath, "auth", "status", "--json")
	assertArgSequenceMissing(t, prepared.Command.Args, "--bind", "/tmp/issue-spec-runner-e2e-001", "/tmp/issue-spec-runner-e2e-001")
	assertArgSequenceMissing(t, prepared.Command.Args, "--ro-bind", systemIssueSpecPath, systemIssueSpecPath)
}

func argsContain(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func assertArgSequence(t *testing.T, args []string, want ...string) {
	t.Helper()
	for i := 0; i <= len(args)-len(want); i++ {
		ok := true
		for j := range want {
			if args[i+j] != want[j] {
				ok = false
				break
			}
		}
		if ok {
			return
		}
	}
	t.Fatalf("args missing sequence %v in %v", want, args)
}

func assertArgSequenceMissing(t *testing.T, args []string, want ...string) {
	t.Helper()
	for i := 0; i <= len(args)-len(want); i++ {
		ok := true
		for j := range want {
			if args[i+j] != want[j] {
				ok = false
				break
			}
		}
		if ok {
			t.Fatalf("args unexpectedly contained sequence %v in %v", want, args)
		}
	}
}

func assertMount(t *testing.T, mounts []Mount, want Mount) {
	t.Helper()
	for _, mount := range mounts {
		if mount == want {
			return
		}
	}
	t.Fatalf("mounts missing %+v in %+v", want, mounts)
}

func capableBwrapRunner(t *testing.T) Runner {
	t.Helper()
	return runnerFunc(func(ctx context.Context, command Command) (Result, error) {
		_ = ctx
		switch {
		case len(command.Args) == 1 && command.Args[0] == "--version":
			return Result{Stdout: []byte("bubblewrap 0.8.0\n")}, nil
		case len(command.Args) == 1 && command.Args[0] == "--help":
			return Result{Stdout: []byte("usage: --perms\n")}, nil
		default:
			return Result{}, nil
		}
	})
}
