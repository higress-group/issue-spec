package jobs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	clientauth "github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/sandbox"
)

func TestSandboxRunnerSelfHostedProfileHasNoGHState(t *testing.T) {
	root := t.TempDir()
	hostGH := filepath.Join(root, "host-gh")
	if err := os.MkdirAll(hostGH, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hostGH, "hosts.yml"), []byte("github.com:\n  oauth_token: must-not-cross\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	token := filepath.Join(root, "issue.token")
	if err := os.WriteFile(token, []byte("dgt_child\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	profile := clientauth.Profile{Name: "runner", Kind: clientauth.ProfileKindHosted,
		APIURL: "https://issues.example.test/api/v3", NativeAPIURL: "https://issues.example.test/api/v1",
		WebURL: "https://issues.example.test", ServerInstanceID: "instance-a"}
	runtimeGH, runtimeXDG := filepath.Join(root, "runtime-gh"), filepath.Join(root, "runtime-xdg")
	cfg, _, mirror, err := (SandboxRunner{Config: sandbox.Config{UnsafeNoSandbox: true, HostGHConfigDir: hostGH}}).config(SandboxRequest{
		WorkspacePath: root, RuntimeHome: filepath.Join(root, "home"), RuntimeGHConfigDir: runtimeGH,
		RuntimeXDGConfigHome: runtimeXDG, RuntimeCodexHome: filepath.Join(root, "codex"), ChildProfile: &profile,
		FileCapabilities: []sandbox.FileCapability{{Source: token, Destination: "/run/issue-spec/credentials/issue.token", EnvName: clientauth.IssueSpecTokenFileEnv}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if mirror.Action != "" || cfg.ExtraEnv[clientauth.ProfileEnv] != "runner" || cfg.ExtraEnv[clientauth.GitHubBackendEnv] != "rest" {
		t.Fatalf("profile config = %+v mirror=%+v", cfg.ExtraEnv, mirror)
	}
	if got, want := cfg.ExtraEnv[clientauth.ConfigDirEnv], filepath.Join(runtimeXDG, "issue-spec"); got != want {
		t.Fatalf("%s = %q, want %q", clientauth.ConfigDirEnv, got, want)
	}
	if _, err := os.Stat(filepath.Join(runtimeGH, "hosts.yml")); !os.IsNotExist(err) {
		t.Fatalf("hosts.yml crossed self-hosted boundary: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(runtimeXDG, "issue-spec", "profiles.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "dgt_child") || !strings.Contains(string(data), "instance-a") {
		t.Fatalf("profile file = %s", data)
	}
}
