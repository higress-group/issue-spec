package sandbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCredentialCapabilityFailsClosedWithoutSandbox(t *testing.T) {
	path := privateCapabilityFile(t, "token", 0o600)
	cfg := Config{UnsafeNoSandbox: true, FileCapabilities: []FileCapability{{Source: path,
		Destination: "/run/issue-spec/credentials/issue.token", EnvName: "ISSUE_SPEC_TOKEN_FILE"}}}
	_, err := Prepare(context.Background(), cfg, Command{Binary: "true"}, Dependencies{})
	if !errors.Is(err, ErrSandboxConfigInvalid) {
		t.Fatalf("Prepare error = %v", err)
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
