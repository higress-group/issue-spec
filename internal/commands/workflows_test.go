package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteWorkflowArtifactsUsesCurrentCodexSkillPath(t *testing.T) {
	root := t.TempDir()
	codexHome := filepath.Join(root, "codex-home")
	t.Setenv("CODEX_HOME", codexHome)

	result, err := writeWorkflowArtifacts(root, "owner/repo", "codex,claude", "both")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(result.SkillFiles), 14; got != want {
		t.Fatalf("skill file count = %d, want %d", got, want)
	}
	if got, want := len(result.CommandFiles), 10; got != want {
		t.Fatalf("command file count = %d, want %d", got, want)
	}
	if got := strings.Join(result.CommandsSkipped, ","); got != "" {
		t.Fatalf("commands skipped = %q, want none", got)
	}

	codexSkill := readTestFile(t, filepath.Join(root, ".agents", "skills", "issue-spec-propose", "SKILL.md"))
	for _, want := range []string{
		"name: issue-spec-propose",
		"compatibility: Requires issue-spec CLI.",
		`generatedBy: "issue-spec"`,
		"issue-spec issue create proposal --repo owner/repo",
	} {
		if !strings.Contains(codexSkill, want) {
			t.Fatalf("codex skill missing %q:\n%s", want, codexSkill)
		}
	}

	workflowSkill := readTestFile(t, filepath.Join(root, ".agents", "skills", "issue-spec-workflow", "SKILL.md"))
	for _, want := range []string{
		"native GitHub CLI support",
		"ISSUE_SPEC_GITHUB_BACKEND=gh",
		`ISSUE_SPEC_TOKEN="$(gh auth token)"`,
	} {
		if !strings.Contains(workflowSkill, want) {
			t.Fatalf("workflow skill missing %q:\n%s", want, workflowSkill)
		}
	}

	githubSkill := readTestFile(t, filepath.Join(root, ".agents", "skills", "issue-spec-github", "SKILL.md"))
	for _, want := range []string{
		"name: issue-spec-github",
		"compatibility: Requires GitHub CLI (gh).",
		"gh auth login",
		"gh pr checks",
		"issue-spec owns the proposal, design, implement",
	} {
		if !strings.Contains(githubSkill, want) {
			t.Fatalf("github skill missing %q:\n%s", want, githubSkill)
		}
	}
	if _, err := os.Stat(filepath.Join(root, ".agents", "skills", "github", "SKILL.md")); !os.IsNotExist(err) {
		t.Fatalf("generic github skill should not be generated, err=%v", err)
	}

	claudeCommand := readTestFile(t, filepath.Join(root, ".claude", "commands", "issue-spec", "propose.md"))
	for _, want := range []string{
		`name: "Issue Spec: Propose"`,
		`category: "Workflow"`,
		"Use when the user asks for /issue-spec:propose",
	} {
		if !strings.Contains(claudeCommand, want) {
			t.Fatalf("claude command missing %q:\n%s", want, claudeCommand)
		}
	}

	codexCommand := readTestFile(t, filepath.Join(codexHome, "prompts", "issue-spec-propose.md"))
	for _, want := range []string{
		"argument-hint: command arguments",
		"issue-spec issue create proposal --repo owner/repo",
	} {
		if !strings.Contains(codexCommand, want) {
			t.Fatalf("codex command missing %q:\n%s", want, codexCommand)
		}
	}
}

func TestWriteWorkflowArtifactsCommandsOnly(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CODEX_HOME", filepath.Join(root, "codex-home"))

	result, err := writeWorkflowArtifacts(root, "owner/repo", "codex", "commands")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.SkillFiles) != 0 {
		t.Fatalf("skills generated in commands-only mode: %v", result.SkillFiles)
	}
	if got, want := len(result.CommandFiles), 5; got != want {
		t.Fatalf("command file count = %d, want %d", got, want)
	}
	if _, err := os.Stat(filepath.Join(root, ".codex", "skills")); !os.IsNotExist(err) {
		t.Fatalf("commands-only mode should not create .codex skills, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".agents", "skills")); !os.IsNotExist(err) {
		t.Fatalf("commands-only mode should not create .agents skills, err=%v", err)
	}
}

func TestWriteWorkflowArtifactsToolsNoneSkipsWorkflowResolve(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "issue-spec"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "issue-spec", "config.yaml"), []byte("schema: [invalid\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := writeWorkflowArtifacts(root, "owner/repo", "none", "both")
	if err != nil {
		t.Fatalf("--tools none should not resolve workflow config: %v", err)
	}
	if result.Delivery != "both" || len(result.Tools) != 0 || result.WorkflowSource != "" {
		t.Fatalf("unexpected generation result for tools none: %+v", result)
	}
	if _, err := os.Stat(filepath.Join(root, ".agents")); !os.IsNotExist(err) {
		t.Fatalf("tools none should not create workflow artifacts, err=%v", err)
	}
}

func TestWriteWorkflowArtifactsEmbedsLanguageRule(t *testing.T) {
	root := t.TempDir()
	if _, err := writeWorkflowLanguageConfig(root, "zh"); err != nil {
		t.Fatal(err)
	}

	if _, err := writeWorkflowArtifacts(root, "owner/repo", "claude", "skills"); err != nil {
		t.Fatal(err)
	}

	skill := readTestFile(t, filepath.Join(root, ".claude", "skills", "issue-spec-propose", "SKILL.md"))
	for _, want := range []string{
		"Workflow Rules:",
		"Simplified Chinese (简体中文)",
		"## Requirement:",
	} {
		if !strings.Contains(skill, want) {
			t.Fatalf("generated skill missing %q:\n%s", want, skill)
		}
	}
}

func TestResolveWorkflowToolsDetectsExistingToolDirs(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".agents"), 0o755); err != nil {
		t.Fatal(err)
	}

	tools, err := resolveWorkflowTools(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 || tools[0].ID != "codex" {
		t.Fatalf("detected tools = %#v, want codex", tools)
	}
}

func TestResolveWorkflowToolsRejectsInvalidTool(t *testing.T) {
	_, err := resolveWorkflowTools(t.TempDir(), "codex,agents")
	if err == nil {
		t.Fatal("expected invalid tool to fail")
	}
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
