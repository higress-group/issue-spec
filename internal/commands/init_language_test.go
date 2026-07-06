package commands

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/github"
	"gopkg.in/yaml.v3"
)

func newInitTestApp(out, errOut *bytes.Buffer) *app {
	app := newApp(strings.NewReader(""), out, errOut)
	app.selectGitHubBackend = func(context.Context, string) (auth.GitHubBackendSelection, error) {
		return auth.GitHubBackendSelection{
			Mode:            auth.GitHubBackendModeAuto,
			Name:            auth.GitHubBackendNameREST,
			Kind:            auth.GitHubBackendKindREST,
			Host:            "github.com",
			SelectionSource: "auto:token",
			TokenSource:     "config",
			Token:           auth.Token{Value: "stored-secret", Source: "config", Host: "github.com"},
		}, nil
	}
	app.newGitHubBackend = func(_ context.Context, selection auth.GitHubBackendSelection) (github.Backend, error) {
		return fakeGitHubBackend{
			info: github.BackendInfo{Name: selection.Name, Kind: selection.Kind, Host: selection.Host},
			user: github.User{Login: "octocat"},
		}, nil
	}
	return app
}

func TestInitLanguageWritesWorkflowConfig(t *testing.T) {
	t.Chdir(t.TempDir())
	var out, errOut bytes.Buffer
	app := newInitTestApp(&out, &errOut)

	code := app.runInit(context.Background(), []string{"--repo", "o/r", "--tools", "none", "--language", "zh"})
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%q", code, errOut.String())
	}

	data, err := os.ReadFile(filepath.Join("issue-spec", "config.yaml"))
	if err != nil {
		t.Fatalf("read config.yaml: %v", err)
	}
	var cfg struct {
		Rules map[string]string `yaml:"rules"`
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("parse config.yaml: %v", err)
	}
	if cfg.Rules["language"] != "Simplified Chinese (简体中文)" {
		t.Fatalf("rules.language = %q", cfg.Rules["language"])
	}
	if !strings.Contains(cfg.Rules["language_instructions"], "## Requirement:") ||
		!strings.Contains(cfg.Rules["language_instructions"], "**WHEN**") {
		t.Fatalf("language_instructions missing canonical token guidance: %q", cfg.Rules["language_instructions"])
	}
	if !strings.Contains(out.String(), "workflow language: Simplified Chinese") {
		t.Fatalf("stdout missing language line: %q", out.String())
	}
}

func TestInitLanguageMergesExistingConfig(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.MkdirAll("issue-spec", 0o755); err != nil {
		t.Fatal(err)
	}
	existing := "schema: custom\ncontext:\n  team: platform\nrules:\n  strictness: high\n"
	if err := os.WriteFile(filepath.Join("issue-spec", "config.yaml"), []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	app := newInitTestApp(&out, &errOut)
	code := app.runInit(context.Background(), []string{"--repo", "o/r", "--tools", "none", "--language", "en"})
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%q", code, errOut.String())
	}

	data, err := os.ReadFile(filepath.Join("issue-spec", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Schema  string            `yaml:"schema"`
		Context map[string]string `yaml:"context"`
		Rules   map[string]string `yaml:"rules"`
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Schema != "custom" {
		t.Fatalf("schema not preserved: %q", cfg.Schema)
	}
	if cfg.Context["team"] != "platform" {
		t.Fatalf("context not preserved: %#v", cfg.Context)
	}
	if cfg.Rules["strictness"] != "high" {
		t.Fatalf("existing rule not preserved: %#v", cfg.Rules)
	}
	if cfg.Rules["language"] != "English" {
		t.Fatalf("rules.language = %q", cfg.Rules["language"])
	}
	if !strings.Contains(cfg.Rules["language_instructions"], "## Requirement:") {
		t.Fatalf("merge dropped canonical-token guidance: %q", cfg.Rules["language_instructions"])
	}
}

func TestInitWithoutLanguageDoesNotWriteWorkflowConfig(t *testing.T) {
	t.Chdir(t.TempDir())
	var out, errOut bytes.Buffer
	app := newInitTestApp(&out, &errOut)

	code := app.runInit(context.Background(), []string{"--repo", "o/r", "--tools", "none"})
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%q", code, errOut.String())
	}
	if _, err := os.Stat(filepath.Join("issue-spec", "config.yaml")); !os.IsNotExist(err) {
		t.Fatalf("issue-spec/config.yaml should not exist, err=%v", err)
	}
}
