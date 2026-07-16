package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/higress-group/issue-spec/internal/codereview"
	"github.com/higress-group/issue-spec/internal/workflow"
)

func TestWriteWorkflowArtifactsUsesCurrentCodexSkillPathWithoutGlobalWrites(t *testing.T) {
	root := t.TempDir()
	codexHome := filepath.Join(t.TempDir(), "codex-home")
	t.Setenv("CODEX_HOME", codexHome)
	existingPrompt := filepath.Join(codexHome, "prompts", "issue-spec-propose.md")
	if err := os.MkdirAll(filepath.Dir(existingPrompt), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(existingPrompt, []byte("user customization\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := writeWorkflowArtifacts(root, "owner/repo", "codex,claude", "both")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(result.SkillFiles), 14; got != want {
		t.Fatalf("skill file count = %d, want %d", got, want)
	}
	if got, want := len(result.CommandFiles), 5; got != want {
		t.Fatalf("command file count = %d, want %d", got, want)
	}
	if got := strings.Join(result.CommandsSkipped, ","); got != "codex" {
		t.Fatalf("commands skipped = %q, want codex", got)
	}
	if len(result.GlobalPromptFiles) != 0 {
		t.Fatalf("default generation planned global prompts: %v", result.GlobalPromptFiles)
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
		"Ordinary issue discussion writes",
		"issue-spec comment create --repo owner/repo --issue 42 --body-file reply.md --json",
		"selected issue backend owns the write",
		"issue-spec owns the proposal, design, implement",
	} {
		if !strings.Contains(githubSkill, want) {
			t.Fatalf("github skill missing %q:\n%s", want, githubSkill)
		}
	}
	for _, forbidden := range []string{
		"gh issue comment",
		"gh api repos/owner/repo/issues/42/comments",
		"or commenting on GitHub issues",
	} {
		if strings.Contains(githubSkill, forbidden) {
			t.Fatalf("generated github skill recommends forbidden ordinary discussion write %q:\n%s", forbidden, githubSkill)
		}
	}
	for _, relative := range []string{
		filepath.Join(".agents", "skills", "issue-spec-github", "SKILL.md"),
		filepath.Join(".claude", "skills", "issue-spec-github", "SKILL.md"),
	} {
		if checkedIn := readTestFile(t, filepath.Join("..", "..", relative)); githubSkill != checkedIn {
			t.Fatalf("checked-in generated GitHub guidance is stale: %s", relative)
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

	for _, relative := range []string{
		filepath.Join(".agents", "skills", "issue-spec-review", "SKILL.md"),
		filepath.Join(".claude", "skills", "issue-spec-review", "SKILL.md"),
		filepath.Join(".claude", "commands", "issue-spec", "review.md"),
	} {
		path := filepath.Join(root, relative)
		reviewGuidance := readTestFile(t, path)
		for _, want := range []string{
			"--from REVIEW-<n> --from-issue <implement-issue> --to PROCESS-<n>",
			"--from REVIEW-<n> --from-issue <implement-issue> --to SPEC-<n>",
			"Run these commands after the final review sync",
		} {
			if !strings.Contains(reviewGuidance, want) {
				t.Fatalf("generated review guidance %s missing %q:\n%s", path, want, reviewGuidance)
			}
		}
		checkedIn := strings.ReplaceAll(readTestFile(t, filepath.Join("..", "..", relative)), "higress-group/issue-spec", "owner/repo")
		if reviewGuidance != checkedIn {
			t.Fatalf("checked-in generated review guidance is stale: %s", relative)
		}
	}

	for _, relative := range []string{
		filepath.Join(".agents", "skills", "issue-spec-workflow", "SKILL.md"),
		filepath.Join(".agents", "skills", "issue-spec-apply", "SKILL.md"),
		filepath.Join(".claude", "skills", "issue-spec-workflow", "SKILL.md"),
		filepath.Join(".claude", "skills", "issue-spec-apply", "SKILL.md"),
		filepath.Join(".claude", "commands", "issue-spec", "apply.md"),
	} {
		generated := readTestFile(t, filepath.Join(root, relative))
		checkedIn := strings.ReplaceAll(readTestFile(t, filepath.Join("..", "..", relative)), "higress-group/issue-spec", "owner/repo")
		if generated != checkedIn {
			t.Fatalf("checked-in generated inline/delegated workflow guidance is stale: %s", relative)
		}
	}

	if got := readTestFile(t, existingPrompt); got != "user customization\n" {
		t.Fatalf("default generation modified user-global prompt: %q", got)
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
	if got, want := len(result.CommandFiles), 0; got != want {
		t.Fatalf("command file count = %d, want %d", got, want)
	}
	if got := strings.Join(result.CommandsSkipped, ","); got != "codex" {
		t.Fatalf("commands skipped = %q, want codex", got)
	}
	if _, err := os.Stat(filepath.Join(root, ".codex", "skills")); !os.IsNotExist(err) {
		t.Fatalf("commands-only mode should not create .codex skills, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".agents", "skills")); !os.IsNotExist(err) {
		t.Fatalf("commands-only mode should not create .agents skills, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "codex-home")); !os.IsNotExist(err) {
		t.Fatalf("commands-only mode should not create user-global prompts, err=%v", err)
	}
}

func TestInstallGlobalCodexPromptsUsesExplicitDirectoryAndSharedTemplates(t *testing.T) {
	root := t.TempDir()
	result, err := writeWorkflowArtifacts(root, "owner/repo", "codex,claude", "both")
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "isolated-prompts")
	if err := installGlobalCodexPrompts(root, "owner/repo", nil, globalPromptInstallOptions{
		Enabled: true, Directory: target,
	}, &result); err != nil {
		t.Fatal(err)
	}
	if got, want := len(result.GlobalPromptFiles), 5; got != want {
		t.Fatalf("global prompt file count = %d, want %d", got, want)
	}
	for _, path := range result.GlobalPromptFiles {
		if !filepath.IsAbs(filepath.FromSlash(path)) {
			t.Fatalf("global prompt path is not absolute: %q", path)
		}
	}

	globalPrompt := readTestFile(t, filepath.Join(target, "issue-spec-propose.md"))
	commands := workflowCommandContents("owner/repo", mustResolveWorkflow(t, root))
	if got, want := globalPrompt, formatCodexCommand(commands[0]); got != want {
		t.Fatalf("global prompt did not use shared command template:\n%s", got)
	}
}

func TestInstallGlobalCodexPromptsDryRunReportsPathsWithoutWrites(t *testing.T) {
	root := t.TempDir()
	result, err := writeWorkflowArtifacts(root, "owner/repo", "none", "both")
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "dry-run-prompts")
	if err := installGlobalCodexPrompts(root, "owner/repo", nil, globalPromptInstallOptions{
		Enabled: true, Directory: target, DryRun: true,
	}, &result); err != nil {
		t.Fatal(err)
	}
	if !result.GlobalPromptsDryRun || len(result.GlobalPromptFiles) != 5 || result.WorkflowSource == "" {
		t.Fatalf("unexpected global prompt dry-run result: %+v", result)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("dry-run created global prompt directory, err=%v", err)
	}
}

func mustResolveWorkflow(t *testing.T, root string) workflow.Plan {
	t.Helper()
	plan, err := workflow.Resolve(root)
	if err != nil {
		t.Fatal(err)
	}
	return plan
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

func TestWriteWorkflowArtifactsWithProviderFollowsCapabilityMatrix(t *testing.T) {
	root := t.TempDir()
	provider := workflow.ProviderPlan{ProviderKey: "aone", DisplayName: "Aone Code",
		CodeChangeLabel: "Merge request", ChangeComment: true, EvidenceSnapshot: true,
		Capabilities: []codereview.Capability{codereview.CapabilityChangeComment, codereview.CapabilityEvidenceSnapshot}}
	result, err := writeWorkflowArtifactsWithProvider(root, "browser-e2e/httpbin", "codex", "skills", provider)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.SkillFiles) == 0 {
		t.Fatal("provider workflow generated no skills")
	}
	generated := readTestFile(t, filepath.Join(root, ".agents", "skills", "issue-spec-code-provider", "SKILL.md"))
	for _, want := range []string{
		"Provider-neutral Code Workflow",
		"`change.create`: unavailable",
		"`change.comment`: available",
		"`evidence.snapshot`: available",
		"policy and evidence contracts, not implied issue-spec CLI commands",
		"Project/work-item tracker authority is independent",
	} {
		if !strings.Contains(generated, want) {
			t.Fatalf("provider workflow missing %q:\n%s", want, generated)
		}
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
