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
	"github.com/higress-group/issue-spec/internal/workflow"
	"gopkg.in/yaml.v3"
)

func newInitTestApp(t *testing.T, out, errOut *bytes.Buffer) *app {
	t.Helper()
	t.Setenv(auth.ConfigDirEnv, t.TempDir())
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
			createLabel: func(_ context.Context, _ string, name, _, _ string) (github.LabelResult, error) {
				return github.LabelResult{Name: name, Skipped: true}, nil
			},
		}, nil
	}
	return app
}

func TestInitLanguageWritesWorkflowConfigWithGeneratedTools(t *testing.T) {
	t.Chdir(t.TempDir())
	var out, errOut bytes.Buffer
	app := newInitTestApp(t, &out, &errOut)

	code := app.runInit(context.Background(), []string{"--repo", "o/r", "--tools", "codex", "--language", "zh"})
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
	projectConfig, err := os.ReadFile(filepath.Join(".issue-spec", "config.json"))
	if err != nil {
		t.Fatalf("read project config: %v", err)
	}
	for _, want := range []string{`"version": 1`, `"repo": "o/r"`, `"hostname": "github.com"`, `"profile": "github"`} {
		if !strings.Contains(string(projectConfig), want) {
			t.Fatalf("project config missing %q:\n%s", want, projectConfig)
		}
	}
}

func TestInitLanguageMergesExistingConfigWithGeneratedTools(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.MkdirAll("issue-spec", 0o755); err != nil {
		t.Fatal(err)
	}
	existing := "schema: issue-spec\ncontext:\n  team: platform\nrules:\n  strictness: high\n"
	if err := os.WriteFile(filepath.Join("issue-spec", "config.yaml"), []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	app := newInitTestApp(t, &out, &errOut)
	code := app.runInit(context.Background(), []string{"--repo", "o/r", "--tools", "codex", "--language", "en"})
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
	if cfg.Schema != "issue-spec" {
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
	app := newInitTestApp(t, &out, &errOut)

	code := app.runInit(context.Background(), []string{"--repo", "o/r", "--tools", "none"})
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%q", code, errOut.String())
	}
	if _, err := os.Stat(filepath.Join("issue-spec", "config.yaml")); !os.IsNotExist(err) {
		t.Fatalf("issue-spec/config.yaml should not exist, err=%v", err)
	}
}

func TestInitToolsNoneReportsLanguageWithoutWorkflowWrites(t *testing.T) {
	for _, jsonOutput := range []bool{false, true} {
		name := "text"
		if jsonOutput {
			name = "json"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			t.Chdir(root)
			var out, errOut bytes.Buffer
			app := newInitTestApp(t, &out, &errOut)
			args := []string{"--repo", "o/r", "--tools", " NoNe ", "--delivery", "not-used", "--language", "zh", "--skip-labels"}
			if jsonOutput {
				args = append(args, "--json")
			}

			if code := app.runInit(context.Background(), args); code != 0 {
				t.Fatalf("exit code = %d, stderr=%q", code, errOut.String())
			}
			for _, path := range []string{
				filepath.Join(root, "issue-spec", "config.yaml"),
				filepath.Join(root, ".agents"),
				filepath.Join(root, ".claude"),
				filepath.Join(root, ".codex"),
			} {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("tools none created workflow path %q: %v", path, err)
				}
			}
			projectConfig := readTestFile(t, filepath.Join(root, ".issue-spec", "config.json"))
			if !strings.Contains(projectConfig, `"repo": "o/r"`) || strings.Contains(projectConfig, "language") {
				t.Fatalf("runtime config =\n%s", projectConfig)
			}
			for _, want := range []string{"Simplified Chinese", "language", "applied"} {
				if !strings.Contains(out.String(), want) {
					t.Fatalf("output missing %q: %s", want, out.String())
				}
			}
			if !strings.Contains(out.String(), "openspec/config.yaml") {
				t.Fatalf("output missing legacy OpenSpec guidance: %s", out.String())
			}
			if jsonOutput && !strings.Contains(out.String(), `"language_applied": false`) {
				t.Fatalf("JSON output did not report language as not applied: %s", out.String())
			}
		})
	}
}

func TestInitToolsNonePreservesInvalidWorkflowConfigs(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	files := map[string]string{
		filepath.Join(root, "issue-spec", "config.yaml"): "schema: [invalid issue-spec\n",
		filepath.Join(root, "openspec", "config.yaml"):   "rules: [invalid openspec\n",
	}
	for path, body := range files {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var out, errOut bytes.Buffer
	app := newInitTestApp(t, &out, &errOut)
	if code := app.runInit(context.Background(), []string{"--repo", "o/r", "--tools", "none", "--language", "en", "--skip-labels"}); code != 0 {
		t.Fatalf("exit code = %d, stderr=%q", code, errOut.String())
	}
	for path, want := range files {
		if got := readTestFile(t, path); got != want {
			t.Fatalf("workflow config %q changed:\nwant %q\n got %q", path, want, got)
		}
	}
}

func TestInitToolsNoneKeepsLegacyOpenSpecDiscoveryAndLanguage(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	path := filepath.Join(root, "openspec", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	existing := "schema: legacy-workflow\nrules:\n  language: Existing OpenSpec Language\n"
	if err := os.WriteFile(path, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}
	schemaPath := filepath.Join(root, "openspec", "schemas", "legacy-workflow", "schema.yaml")
	if err := os.MkdirAll(filepath.Dir(schemaPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(schemaPath, []byte("artifacts:\n  specs:\n    type: specs\n    generates: specs/**/*.md\n    template: spec.md\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	templatePath := filepath.Join(root, "openspec", "schemas", "legacy-workflow", "templates", "spec.md")
	if err := os.MkdirAll(filepath.Dir(templatePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(templatePath, []byte("## Requirement: {{.Input.requirement.title}}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	app := newInitTestApp(t, &out, &errOut)
	if code := app.runInit(context.Background(), []string{"--repo", "o/r", "--tools", "NONE", "--language", "zh", "--skip-labels"}); code != 0 {
		t.Fatalf("exit code = %d, stderr=%q", code, errOut.String())
	}
	if got := readTestFile(t, path); got != existing {
		t.Fatalf("OpenSpec config changed:\nwant %q\n got %q", existing, got)
	}
	plan, err := workflow.Resolve(root)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Source.Kind != workflow.SourceLegacyOpenSpec || plan.Config.Rules["language"] != "Existing OpenSpec Language" {
		t.Fatalf("workflow plan = source %q rules=%+v", plan.Source.Kind, plan.Config.Rules)
	}
}
