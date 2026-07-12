//go:build linux

package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildBwrapCredentialCapabilityUsesFixedReadOnlyDestination(t *testing.T) {
	root := t.TempDir()
	dirs := []string{"workspace", "home", "gh", "xdg", "codex"}
	for _, dir := range dirs {
		if err := os.Mkdir(filepath.Join(root, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	token := filepath.Join(root, "token")
	if err := os.WriteFile(token, []byte("dgt_child"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{WorkspacePath: filepath.Join(root, "workspace"), TempHome: filepath.Join(root, "home"),
		TempGHConfigDir: filepath.Join(root, "gh"), TempXDGConfigHome: filepath.Join(root, "xdg"), TempCodexHome: filepath.Join(root, "codex"),
		SystemReadOnlyBinds: []string{}, FileCapabilities: []FileCapability{{Source: token,
			Destination: "/run/issue-spec/credentials/issue.token", EnvName: "ISSUE_SPEC_TOKEN_FILE"}}}
	command, mounts, err := buildBwrapCommand(cfg, Command{Binary: "/bin/true", Dir: cfg.WorkspacePath},
		[]string{"ISSUE_SPEC_TOKEN_FILE=/run/issue-spec/credentials/issue.token"}, "/usr/bin/bwrap")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(command.Args, "\n")
	if !strings.Contains(joined, token+"\n/run/issue-spec/credentials/issue.token") ||
		!strings.Contains(joined, "ISSUE_SPEC_TOKEN_FILE\n/run/issue-spec/credentials/issue.token") {
		t.Fatalf("bwrap args = %s", joined)
	}
	for _, mount := range mounts {
		if mount.Mode == "ro-capability" && mount.Source != "" {
			t.Fatalf("capability source leaked into metadata: %+v", mount)
		}
	}
}
