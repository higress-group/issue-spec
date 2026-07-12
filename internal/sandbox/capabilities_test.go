package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCredentialCapabilityUsesPrivateBrokerSourceWhenUnsafeIsExplicit(t *testing.T) {
	path := privateCapabilityFile(t, "token", 0o600)
	root := t.TempDir()
	cfg := Config{UnsafeNoSandbox: true, WorkspacePath: filepath.Join(root, "workspace"),
		TempHome: filepath.Join(root, "home"), TempGHConfigDir: filepath.Join(root, "gh"),
		TempXDGConfigHome: filepath.Join(root, "xdg"), FileCapabilities: []FileCapability{{Source: path,
			Destination: "/run/issue-spec/credentials/issue.token", EnvName: "ISSUE_SPEC_TOKEN_FILE"}}}
	preflight, err := Preflight(context.Background(), cfg, Dependencies{})
	if err != nil {
		t.Fatalf("Preflight error = %v", err)
	}
	if preflight.SandboxProvider != ProviderNone || preflight.FSBoundary != FSBoundaryDisabled ||
		!strings.Contains(strings.Join(preflight.Diagnostics, "\n"), "private host path") {
		t.Fatalf("Preflight metadata = %+v", preflight)
	}
	prepared, err := Prepare(context.Background(), cfg, Command{Binary: "true"}, Dependencies{})
	if err != nil {
		t.Fatalf("Prepare error = %v", err)
	}
	env := envMap(prepared.Command.Env)
	if env["ISSUE_SPEC_TOKEN_FILE"] != path {
		t.Fatalf("ISSUE_SPEC_TOKEN_FILE = %q, want private broker source %q", env["ISSUE_SPEC_TOKEN_FILE"], path)
	}
	if strings.Contains(strings.Join(prepared.Command.Env, "\n"), "/run/issue-spec/credentials/issue.token") {
		t.Fatalf("unsafe command retained unavailable sandbox destination: %v", prepared.Command.Env)
	}
	if prepared.Metadata.SandboxProvider != ProviderNone || prepared.Metadata.FSBoundary != FSBoundaryDisabled {
		t.Fatalf("Prepare metadata = %+v", prepared.Metadata)
	}
}

func TestCredentialCapabilityIsOnlyTrustedTokenFileEnvPath(t *testing.T) {
	path := privateCapabilityFile(t, "token", 0o600)
	cfg := Config{HostEnv: []string{"PATH=/bin", "ISSUE_SPEC_TOKEN=secret", "ISSUE_SPEC_TOKEN_FILE=/host/escape"},
		FileCapabilities: []FileCapability{{Source: path, Destination: "/run/issue-spec/credentials/issue.token", EnvName: "ISSUE_SPEC_TOKEN_FILE"}}}
	result := scrubEnvironment(cfg, sandboxEnvPaths(), true)
	joined := strings.Join(result.entries, "\n")
	if strings.Contains(joined, "secret") || strings.Contains(joined, "/host/escape") ||
		!strings.Contains(joined, "ISSUE_SPEC_TOKEN_FILE=/run/issue-spec/credentials/issue.token") {
		t.Fatalf("scrubbed env = %s", joined)
	}
}

func TestCredentialCapabilityRejectsSymlinkHardlinkAndPublicMode(t *testing.T) {
	private := privateCapabilityFile(t, "private", 0o600)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(private, link); err != nil {
		t.Fatal(err)
	}
	hard := filepath.Join(filepath.Dir(private), "hard")
	if err := os.Link(private, hard); err != nil {
		t.Fatal(err)
	}
	public := privateCapabilityFile(t, "public", 0o644)
	for _, source := range []string{link, private, public} {
		if err := validateFileCapabilities([]FileCapability{{Source: source,
			Destination: "/run/issue-spec/credentials/issue.token", EnvName: "ISSUE_SPEC_TOKEN_FILE"}}); err == nil {
			t.Fatalf("unsafe source accepted: %s", source)
		}
	}
}

func privateCapabilityFile(t *testing.T, name string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte("value"), mode); err != nil {
		t.Fatal(err)
	}
	return path
}
