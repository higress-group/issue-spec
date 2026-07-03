package sandbox

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestBuildArgsAndEnv(t *testing.T) {
	cfg := Config{
		BwrapPath: "/usr/bin/bwrap",
		Workspace: "/managed/ws",
		HomeDir:   "/tmp/home",
		GHConfig:  "/tmp/gh",
		XDGConfig: "/tmp/xdg",
		Env: []string{
			"http_proxy=http://proxy",
			"HTTPS_PROXY=https://proxy",
			"GH_TOKEN=secret",
		},
	}
	gotArgs := buildArgs(cfg, CommandSpec{Binary: "/bin/echo", Args: []string{"hello"}})
	wantContains := []string{
		"--clearenv",
		"--setenv", "HOME", "/tmp/home",
		"--setenv", "GH_CONFIG_DIR", "/tmp/gh",
		"--setenv", "XDG_CONFIG_HOME", "/tmp/xdg",
		"--bind", "/managed/ws", "/workspace",
		"--tmpfs", "/tmp",
		"--unsetenv", "GH_TOKEN",
		"--unsetenv", "GITHUB_TOKEN",
		"--unsetenv", "ISSUE_SPEC_TOKEN",
		"--chdir", "/workspace", "--", "/bin/echo", "hello",
	}
	for _, want := range wantContains {
		if !containsString(gotArgs, want) {
			t.Fatalf("args missing %q: %v", want, gotArgs)
		}
	}
	gotEnv := buildEnv(cfg.Env, cfg.HomeDir, cfg.GHConfig, cfg.XDGConfig)
	wantEnv := []string{
		"HOME=/tmp/home",
		"GH_CONFIG_DIR=/tmp/gh",
		"XDG_CONFIG_HOME=/tmp/xdg",
		"http_proxy=http://proxy",
		"HTTPS_PROXY=https://proxy",
	}
	if !reflect.DeepEqual(gotEnv, wantEnv) {
		t.Fatalf("env = %#v, want %#v", gotEnv, wantEnv)
	}
}

func TestPreflightFailureOnUnsupportedDefaultRunner(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	if !strings.Contains(ErrUnsupported.Error(), "Linux") {
		t.Fatalf("unsupported message = %q", ErrUnsupported)
	}
}

func TestBubblewrapPreflightPropagatesCapabilityFailure(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	b := Bubblewrap{Path: "/bin/false"}
	err := b.Preflight(context.Background(), PreflightOptions{BwrapPath: "/bin/false"})
	if err == nil {
		t.Fatal("expected preflight error")
	}
}

func TestUnsafeNoSandboxUsesExplicitWrapper(t *testing.T) {
	execRunner := ExecRunner{Config: Config{
		Workspace: "/workspace",
		HomeDir:   "/tmp/home",
		GHConfig:  "/tmp/gh",
		XDGConfig: "/tmp/xdg",
		Env:       []string{"GH_TOKEN=secret", "http_proxy=http://proxy"},
	}}
	got, err := execRunner.BuildUnsafeNoSandbox(CommandSpec{
		Binary: "/workspace/coord",
		Args:   []string{"--unsafe-no-sandbox"},
		Dir:    "/workspace",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Markers["sandbox_provider"] != "none" || got.Markers["fs_boundary"] != "disabled" {
		t.Fatalf("markers = %#v", got.Markers)
	}
	if got.Command.Dir != "/workspace" {
		t.Fatalf("dir = %q", got.Command.Dir)
	}
	if !containsString(got.Command.Env, "http_proxy=http://proxy") || containsString(got.Command.Env, "GH_TOKEN=secret") {
		t.Fatalf("unsafe env scrubber = %#v", got.Command.Env)
	}
}

func TestCompareVersions(t *testing.T) {
	if compareVersions("0.5.0", "0.4.9") <= 0 {
		t.Fatal("version comparison failed")
	}
	if compareVersions("0.5.0", "0.5.0") != 0 {
		t.Fatal("version comparison equality failed")
	}
}

type recordingRunner struct {
	commands []Command
}

func (r *recordingRunner) Run(_ context.Context, cmd Command) (Result, error) {
	r.commands = append(r.commands, cmd)
	return Result{}, nil
}

func containsString(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

var _ error = ErrUnsupported
var _ = errors.New
