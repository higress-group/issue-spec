package commands

import (
	"bytes"
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
	if got, want := len(result.SkillFiles), 12; got != want {
		t.Fatalf("skill file count = %d, want %d", got, want)
	}
	if got, want := len(result.SkillResourceFiles), 2; got != want {
		t.Fatalf("skill resource file count = %d, want %d", got, want)
	}
	if got, want := len(result.CommandFiles), 4; got != want {
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
	projectionReference := readTestFile(t, filepath.Join(root, ".agents", "skills", "issue-spec-workflow", "references", "human-review-projections.md"))
	for _, want := range []string{
		"# Human Review Projection Generation",
		"implement-execution-brief",
		"Decision needed",
		"GitHub displays the fenced HTML source and does not execute",
	} {
		if !strings.Contains(projectionReference, want) {
			t.Fatalf("projection reference missing %q:\n%s", want, projectionReference)
		}
	}
	claudeProjectionReference := readTestFile(t, filepath.Join(root, ".claude", "skills", "issue-spec-workflow", "references", "human-review-projections.md"))
	if projectionReference != claudeProjectionReference {
		t.Fatal("Codex and Claude projection references differ")
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
	}

	for _, relative := range []string{
		filepath.Join(".agents", "skills", "issue-spec-workflow", "SKILL.md"),
		filepath.Join(".agents", "skills", "issue-spec-apply", "SKILL.md"),
		filepath.Join(".claude", "skills", "issue-spec-workflow", "SKILL.md"),
		filepath.Join(".claude", "skills", "issue-spec-apply", "SKILL.md"),
		filepath.Join(".claude", "commands", "issue-spec", "apply.md"),
	} {
		_ = readTestFile(t, filepath.Join(root, relative))
	}

	if got := readTestFile(t, existingPrompt); got != "user customization\n" {
		t.Fatalf("default generation modified user-global prompt: %q", got)
	}
}

func TestWriteWorkflowArtifactsPrunesOnlyRecognizedManagedArchiveAssets(t *testing.T) {
	root := t.TempDir()
	managedSkill := filepath.Join(root, ".agents", "skills", "issue-spec-archive", "SKILL.md")
	managedCommand := filepath.Join(root, ".claude", "commands", "issue-spec", "archive.md")
	userSkill := filepath.Join(root, ".claude", "skills", "issue-spec-archive", "SKILL.md")
	for path, body := range map[string]string{
		managedSkill:   "---\nname: issue-spec-archive\nmetadata:\n  generatedBy: \"issue-spec\"\n---\n# Issue Spec Archive\n",
		managedCommand: "---\nname: \"Issue Spec: Archive\"\ncategory: \"Workflow\"\n---\n# Issue Spec Archive\n",
		userSkill:      "# User-owned archive notes\n",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	result, err := writeWorkflowArtifacts(root, "owner/repo", "codex,claude", "both")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.PrunedFiles) != 2 {
		t.Fatalf("pruned files = %v, want two recognized managed assets", result.PrunedFiles)
	}
	for _, path := range []string{managedSkill, managedCommand} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("managed archive asset was not pruned: %s err=%v", path, err)
		}
	}
	if got := readTestFile(t, userSkill); got != "# User-owned archive notes\n" {
		t.Fatalf("user-owned archive asset changed: %q", got)
	}
}

func TestWorkflowNoticeIsBackendNeutralAndOwnedArtifactsAreCurrent(t *testing.T) {
	plan := mustResolveWorkflow(t, t.TempDir())
	notice := workflowNotice(plan)
	const neutralFooter = "artifacts remain in the selected issue backend's issue-native storage"
	if !strings.Contains(notice, neutralFooter) || strings.Contains(notice, "remain in GitHub issue-native storage") {
		t.Fatalf("workflow notice is not backend-neutral:\n%s", notice)
	}

	root := t.TempDir()
	if _, err := writeWorkflowArtifacts(root, "higress-group/issue-spec", "codex,claude", "both"); err != nil {
		t.Fatal(err)
	}
	paths := make([]string, 0, 14)
	for _, skill := range []string{"apply", "propose", "review", "verify", "workflow"} {
		paths = append(paths,
			filepath.Join(".agents", "skills", "issue-spec-"+skill, "SKILL.md"),
			filepath.Join(".claude", "skills", "issue-spec-"+skill, "SKILL.md"))
	}
	for _, command := range []string{"apply", "propose", "review", "verify"} {
		paths = append(paths, filepath.Join(".claude", "commands", "issue-spec", command+".md"))
	}
	for _, relative := range paths {
		generated := readTestFile(t, filepath.Join(root, relative))
		if !strings.Contains(generated, neutralFooter) || strings.Contains(generated, "remain in GitHub issue-native storage") {
			t.Fatalf("generated workflow footer is not backend-neutral: %s", relative)
		}
	}
}

func TestCheckedInWorkflowArtifactsExactlyMatchGenerator(t *testing.T) {
	generatedRoot := t.TempDir()
	projectRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	config, err := os.ReadFile(filepath.Join(projectRoot, "issue-spec", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(generatedRoot, "issue-spec"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(generatedRoot, "issue-spec", "config.yaml"), config, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(generatedRoot)
	if _, err := writeWorkflowArtifacts(".", "higress-group/issue-spec", "codex,claude", "both"); err != nil {
		t.Fatal(err)
	}
	for _, relative := range append(
		[]string{
			filepath.Join(".agents", "skills", "issue-spec-apply", "SKILL.md"),
			filepath.Join(".agents", "skills", "issue-spec-github", "SKILL.md"),
			filepath.Join(".agents", "skills", "issue-spec-propose", "SKILL.md"),
			filepath.Join(".agents", "skills", "issue-spec-review", "SKILL.md"),
			filepath.Join(".agents", "skills", "issue-spec-verify", "SKILL.md"),
			filepath.Join(".agents", "skills", "issue-spec-workflow", "SKILL.md"),
			filepath.Join(".claude", "skills", "issue-spec-apply", "SKILL.md"),
			filepath.Join(".claude", "skills", "issue-spec-github", "SKILL.md"),
			filepath.Join(".claude", "skills", "issue-spec-propose", "SKILL.md"),
			filepath.Join(".claude", "skills", "issue-spec-review", "SKILL.md"),
			filepath.Join(".claude", "skills", "issue-spec-verify", "SKILL.md"),
			filepath.Join(".claude", "skills", "issue-spec-workflow", "SKILL.md"),
			filepath.Join(".agents", "skills", "issue-spec-workflow", "references", "human-review-projections.md"),
			filepath.Join(".claude", "skills", "issue-spec-workflow", "references", "human-review-projections.md"),
		},
		[]string{
			filepath.Join(".claude", "commands", "issue-spec", "apply.md"),
			filepath.Join(".claude", "commands", "issue-spec", "propose.md"),
			filepath.Join(".claude", "commands", "issue-spec", "review.md"),
			filepath.Join(".claude", "commands", "issue-spec", "verify.md"),
		}...,
	) {
		generated, err := os.ReadFile(filepath.Join(generatedRoot, relative))
		if err != nil {
			t.Fatal(err)
		}
		checkedIn, err := os.ReadFile(filepath.Join(projectRoot, relative))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(generated, checkedIn) {
			t.Fatalf("checked-in workflow artifact is stale: %s", relative)
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
	if len(result.SkillResourceFiles) != 0 {
		t.Fatalf("skill resources generated in commands-only mode: %v", result.SkillResourceFiles)
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
	if got, want := len(result.GlobalPromptFiles), 4; got != want {
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
	if !result.GlobalPromptsDryRun || len(result.GlobalPromptFiles) != 4 || result.WorkflowSource == "" {
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

	for _, tools := range []string{"none", " NoNe "} {
		result, err := writeWorkflowArtifacts(root, "owner/repo", tools, "both")
		if err != nil {
			t.Fatalf("--tools %q should not resolve workflow config: %v", tools, err)
		}
		if result.Delivery != "both" || len(result.Tools) != 0 || result.WorkflowSource != "" {
			t.Fatalf("unexpected generation result for tools %q: %+v", tools, result)
		}
	}
	providerResult, err := writeWorkflowArtifactsWithProvider(root, "owner/repo", "NONE", "both", workflow.ProviderPlan{ProviderKey: "code.example"})
	if err != nil {
		t.Fatalf("provider tools-none path should not resolve workflow config: %v", err)
	}
	if providerResult.Delivery != "both" || len(providerResult.Tools) != 0 || providerResult.WorkflowSource != "" {
		t.Fatalf("unexpected provider generation result for tools none: %+v", providerResult)
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
