package sandbox

import (
	"context"
	"runtime"
	"strings"
	"testing"
)

func TestPrepareUnsafeScrubsEnvAndExposesMetadata(t *testing.T) {
	cfg := Config{
		UnsafeNoSandbox:   true,
		WorkspacePath:     testAbsPath("workspace"),
		TempHome:          testAbsPath("home"),
		TempGHConfigDir:   testAbsPath("gh"),
		TempXDGConfigHome: testAbsPath("xdg"),
		TempCodexHome:     testAbsPath("codex"),
		HostEnv: []string{
			"PATH=/usr/bin",
			"FOO=bar",
			"GH_TOKEN=secret",
			"GITHUB_TOKEN=secret",
			"ISSUE_SPEC_TOKEN=secret",
			"ACTIONS_ID_TOKEN_REQUEST_TOKEN=secret",
			"HTTPS_PROXY=http://proxy.example",
		},
		EnvAllowlist: []string{"PATH", "FOO"},
		ExtraEnv: map[string]string{
			"SAFE_EXTRA": "1",
			"API_TOKEN":  "secret",
		},
	}

	prepared, err := Prepare(context.Background(), cfg, Command{Binary: "acpx", Args: []string{"run"}}, Dependencies{})
	if err != nil {
		t.Fatalf("Prepare returned error: %v", err)
	}
	if prepared.Command.Dir != cfg.WorkspacePath {
		t.Fatalf("Dir = %q, want %q", prepared.Command.Dir, cfg.WorkspacePath)
	}
	env := envMap(prepared.Command.Env)
	for _, name := range []string{"GH_TOKEN", "GITHUB_TOKEN", "ISSUE_SPEC_TOKEN", "ACTIONS_ID_TOKEN_REQUEST_TOKEN", "API_TOKEN"} {
		if _, ok := env[name]; ok {
			t.Fatalf("sensitive env %s was not scrubbed: %v", name, prepared.Command.Env)
		}
	}
	for name, want := range map[string]string{
		"PATH":            "/usr/bin",
		"FOO":             "bar",
		"HTTPS_PROXY":     "http://proxy.example",
		"HOME":            cfg.TempHome,
		"GH_CONFIG_DIR":   cfg.TempGHConfigDir,
		"XDG_CONFIG_HOME": cfg.TempXDGConfigHome,
		"CODEX_HOME":      cfg.TempCodexHome,
		"SAFE_EXTRA":      "1",
	} {
		if got := env[name]; got != want {
			t.Fatalf("%s = %q, want %q in env %v", name, got, want, prepared.Command.Env)
		}
	}
	meta := prepared.Metadata
	if meta.SandboxProvider != ProviderNone || meta.FSBoundary != FSBoundaryDisabled || !meta.UnsafeNoSandbox {
		t.Fatalf("unsafe metadata not inspectable: %+v", meta)
	}
	if !contains(meta.Env.TokenUnset, "GH_TOKEN") || !contains(meta.Env.TokenUnset, "API_TOKEN") {
		t.Fatalf("token unset decisions missing: %+v", meta.Env)
	}
}

func TestPrepareMergesTrustedCommandEnvWithoutReintroducingHostSecrets(t *testing.T) {
	cfg := Config{
		UnsafeNoSandbox:   true,
		WorkspacePath:     testAbsPath("workspace"),
		TempHome:          testAbsPath("home"),
		TempGHConfigDir:   testAbsPath("gh"),
		TempXDGConfigHome: testAbsPath("xdg"),
		TempCodexHome:     testAbsPath("codex"),
		HostEnv: []string{
			"PATH=/usr/bin",
			"UNLISTED_HOST=value",
			"GH_TOKEN=host-secret",
		},
		EnvAllowlist: []string{"PATH"},
	}
	commandEnv := []string{
		"PATH=/custom/bin",
		"UNLISTED_HOST=value",
		"ACPX_CLAUDE_INCLUDE_USER_SETTINGS=1",
		"SAFE_COMMAND_ENV=1",
		"GH_TOKEN=command-secret",
		"HOME=/not-the-sandbox-home",
		"CODEX_HOME=/not-the-codex-home",
	}

	prepared, err := Prepare(context.Background(), cfg, Command{Binary: "acpx", Env: commandEnv}, Dependencies{})
	if err != nil {
		t.Fatalf("Prepare returned error: %v", err)
	}
	env := envMap(prepared.Command.Env)
	for name, want := range map[string]string{
		"PATH":                              "/custom/bin",
		"ACPX_CLAUDE_INCLUDE_USER_SETTINGS": "1",
		"SAFE_COMMAND_ENV":                  "1",
		"HOME":                              cfg.TempHome,
		"GH_CONFIG_DIR":                     cfg.TempGHConfigDir,
		"CODEX_HOME":                        cfg.TempCodexHome,
	} {
		if got := env[name]; got != want {
			t.Fatalf("%s = %q, want %q in env %v", name, got, want, prepared.Command.Env)
		}
	}
	for _, name := range []string{"GH_TOKEN", "UNLISTED_HOST"} {
		if _, ok := env[name]; ok {
			t.Fatalf("%s should not be reintroduced from command env: %v", name, prepared.Command.Env)
		}
	}
	if !contains(prepared.Metadata.Env.TokenUnset, "GH_TOKEN") {
		t.Fatalf("token skip not recorded: %+v", prepared.Metadata.Env)
	}
}

func TestPreparePreservesClaudeEffortEnvByDefault(t *testing.T) {
	cfg := Config{
		UnsafeNoSandbox:   true,
		WorkspacePath:     testAbsPath("workspace"),
		TempHome:          testAbsPath("home"),
		TempGHConfigDir:   testAbsPath("gh"),
		TempXDGConfigHome: testAbsPath("xdg"),
		HostEnv: []string{
			"PATH=/usr/bin",
			"CLAUDE_CODE_EFFORT_LEVEL=max",
			"CLAUDE_CODE_AUTH_TOKEN=secret",
		},
	}

	prepared, err := Prepare(context.Background(), cfg, Command{Binary: "acpx"}, Dependencies{})
	if err != nil {
		t.Fatalf("Prepare returned error: %v", err)
	}
	env := envMap(prepared.Command.Env)
	if got := env["CLAUDE_CODE_EFFORT_LEVEL"]; got != "max" {
		t.Fatalf("CLAUDE_CODE_EFFORT_LEVEL = %q, want max in env %v", got, prepared.Command.Env)
	}
	if _, ok := env["CLAUDE_CODE_AUTH_TOKEN"]; ok {
		t.Fatalf("token-like Claude env should be scrubbed: %v", prepared.Command.Env)
	}
	if !contains(prepared.Metadata.Env.TokenUnset, "CLAUDE_CODE_AUTH_TOKEN") {
		t.Fatalf("token skip not recorded: %+v", prepared.Metadata.Env)
	}
}

func TestPrepareUnsafeLeavesAbsoluteBinaryPathUnchanged(t *testing.T) {
	workspacePath := testAbsPath("workspace")
	binaryPath := testAbsPath("tmp/issue-spec-runner-e2e-001/bin/issue-spec")
	cfg := Config{
		UnsafeNoSandbox:   true,
		WorkspacePath:     workspacePath,
		TempHome:          testAbsPath("home"),
		TempGHConfigDir:   testAbsPath("gh"),
		TempXDGConfigHome: testAbsPath("xdg"),
		TempCodexHome:     testAbsPath("codex"),
		ReadOnlyBinds:     []string{binaryPath},
		HostEnv:           []string{"PATH=/usr/bin"},
	}

	prepared, err := Prepare(context.Background(), cfg, Command{Binary: binaryPath, Args: []string{"auth", "status", "--json"}}, Dependencies{})
	if err != nil {
		t.Fatalf("Prepare returned error: %v", err)
	}
	if prepared.Command.Binary != binaryPath {
		t.Fatalf("Binary = %q, want unchanged %q", prepared.Command.Binary, binaryPath)
	}
	if prepared.Command.Dir != workspacePath {
		t.Fatalf("Dir = %q, want workspace %q", prepared.Command.Dir, workspacePath)
	}
	if len(prepared.Metadata.Mounts) != 0 {
		t.Fatalf("unsafe mode should not build bwrap mounts: %+v", prepared.Metadata.Mounts)
	}
}

func TestPreflightUnsafeDoesNotRequireBwrap(t *testing.T) {
	_, err := Preflight(context.Background(), Config{UnsafeNoSandbox: true}, Dependencies{
		LookPath: func(string) (string, error) {
			t.Fatal("LookPath should not be called in unsafe mode")
			return "", nil
		},
		Runner: runnerFunc(func(context.Context, Command) (Result, error) {
			t.Fatal("Runner should not be called in unsafe mode")
			return Result{}, nil
		}),
	})
	if err != nil {
		t.Fatalf("Preflight unsafe returned error: %v", err)
	}
}

func TestVersionParsingAndComparison(t *testing.T) {
	version, ok := parseBwrapVersion("bubblewrap 0.10.1\n")
	if !ok || version != "0.10.1" {
		t.Fatalf("parseBwrapVersion returned %q/%v", version, ok)
	}
	for _, tt := range []struct {
		got, want string
		ok        bool
	}{
		{"0.5.0", "0.5.0", true},
		{"0.10.0", "0.5.0", true},
		{"0.4.9", "0.5.0", false},
	} {
		if got := versionAtLeast(tt.got, tt.want); got != tt.ok {
			t.Fatalf("versionAtLeast(%q, %q) = %v, want %v", tt.got, tt.want, got, tt.ok)
		}
	}
}

func envMap(entries []string) map[string]string {
	out := map[string]string{}
	for _, entry := range entries {
		name, value, ok := strings.Cut(entry, "=")
		if ok {
			out[name] = value
		}
	}
	return out
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

type runnerFunc func(context.Context, Command) (Result, error)

func (f runnerFunc) Run(ctx context.Context, command Command) (Result, error) {
	return f(ctx, command)
}

func testAbsPath(name string) string {
	if runtime.GOOS == "windows" {
		return `C:\issue-spec\` + name
	}
	return "/tmp/issue-spec-" + name
}
