package commands

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/higress-group/issue-spec/internal/codereview"
	"github.com/higress-group/issue-spec/internal/templates"
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
	if got, want := len(result.SkillFiles), 4; got != want {
		t.Fatalf("skill file count = %d, want %d", got, want)
	}
	if got, want := len(result.SkillResourceFiles), len(templates.HumanReviewProjectionResources())+2; got != want {
		t.Fatalf("skill resource file count = %d, want %d", got, want)
	}
	if got, want := strings.Join(result.SkillLinks, ","), ".claude/skills -> ../.agents/skills"; got != want {
		t.Fatalf("skill links = %q, want %q", got, want)
	}
	if got, want := len(result.CommandFiles), 2; got != want {
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
		"exact-head human review handoff",
		"Stop before approval or merge",
		"current provider-native CI",
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
	claudeSkills := filepath.Join(root, ".claude", "skills")
	if info, err := os.Lstat(claudeSkills); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("Claude skills path is not a symlink: mode=%v err=%v", info, err)
	}
	if target, err := os.Readlink(claudeSkills); err != nil || target != filepath.Join("..", ".agents", "skills") {
		t.Fatalf("Claude skills link target = %q, err=%v", target, err)
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
		"issue-spec owns optional planning, implementation coordination, durable projection, PR context, and human handoff",
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
		"Use for /issue-spec:propose or issue-native planning in a selected issue-spec change",
	} {
		if !strings.Contains(claudeCommand, want) {
			t.Fatalf("claude command missing %q:\n%s", want, claudeCommand)
		}
	}

	for _, relative := range []string{
		filepath.Join(".agents", "skills", "issue-spec-review", "SKILL.md"),
		filepath.Join(".agents", "skills", "issue-spec-verify", "SKILL.md"),
		filepath.Join(".claude", "commands", "issue-spec", "review.md"),
		filepath.Join(".claude", "commands", "issue-spec", "verify.md"),
	} {
		if _, err := os.Stat(filepath.Join(root, relative)); !os.IsNotExist(err) {
			t.Fatalf("retired generated authority asset exists: %s err=%v", relative, err)
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

func TestWriteWorkflowArtifactsDisabledHTMLReviewPrunesExactManagedReferenceIdempotently(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "issue-spec", "config.yaml")
	writeWorkflowTestFile(t, configPath, "html_review:\n  enabled: true\n")
	if _, err := writeWorkflowArtifacts(root, "owner/repo", "codex,claude", "both"); err != nil {
		t.Fatal(err)
	}
	reference := filepath.Join(root, ".agents", "skills", "issue-spec-workflow", "references", "human-review-projections.md")
	if _, err := os.Stat(reference); err != nil {
		t.Fatalf("enabled generation did not create projection reference: %v", err)
	}
	sibling := filepath.Join(filepath.Dir(reference), "operator-notes.md")
	writeWorkflowTestFile(t, sibling, "operator owned\n")

	writeWorkflowTestFile(t, configPath, "html_review:\n  enabled: false\n")
	result, err := writeWorkflowArtifacts(root, "owner/repo", "codex,claude", "both")
	if err != nil {
		t.Fatal(err)
	}
	var prunedPaths []string
	for _, resource := range templates.HumanReviewProjectionResources() {
		path := filepath.Join(root, ".agents", "skills", "issue-spec-workflow", filepath.FromSlash(resource.Path))
		prunedPaths = append(prunedPaths, cleanGeneratedPath(path))
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("disabled generation retained managed resource %s: %v", resource.Path, err)
		}
	}
	wantPruned := strings.Join(prunedPaths, ",")
	if got := strings.Join(result.PrunedFiles, ","); got != wantPruned {
		t.Fatalf("pruned files = %q, want %q", got, wantPruned)
	}
	if _, err := os.Stat(reference); !os.IsNotExist(err) {
		t.Fatalf("disabled generation retained managed projection reference, err=%v", err)
	}
	if got := readTestFile(t, sibling); got != "operator owned\n" {
		t.Fatalf("disabled generation changed sibling resource: %q", got)
	}
	workflowSkill := readTestFile(t, filepath.Join(root, ".agents", "skills", "issue-spec-workflow", "SKILL.md"))
	proposeCommand := readTestFile(t, filepath.Join(root, ".claude", "commands", "issue-spec", "propose.md"))
	for name, body := range map[string]string{"workflow skill": workflowSkill, "propose command": proposeCommand} {
		for _, forbidden := range []string{"human-review-projections.md", "proposal-choice-brief", "design-explainer", "implement-execution-brief", "projection upsert"} {
			if strings.Contains(body, forbidden) {
				t.Fatalf("%s retains disabled HTML review guidance %q:\n%s", name, forbidden, body)
			}
		}
	}

	again, err := writeWorkflowArtifacts(root, "owner/repo", "codex,claude", "both")
	if err != nil {
		t.Fatal(err)
	}
	if len(again.PrunedFiles) != 0 {
		t.Fatalf("repeated disabled generation was not idempotent: %+v", again.PrunedFiles)
	}
}

func TestWriteWorkflowArtifactsPrunesOnlyRecognizedManagedArchiveAssets(t *testing.T) {
	root := t.TempDir()
	managedSkill := filepath.Join(root, ".agents", "skills", "issue-spec-archive", "SKILL.md")
	managedProviderSkill := filepath.Join(root, ".agents", "skills", "issue-spec-code-provider", "SKILL.md")
	managedCommand := filepath.Join(root, ".claude", "commands", "issue-spec", "archive.md")
	for path, body := range map[string]string{
		managedSkill:         "---\nname: issue-spec-archive\nmetadata:\n  generatedBy: \"issue-spec\"\n---\n# Issue Spec Archive\n",
		managedProviderSkill: "---\nname: issue-spec-code-provider\nmetadata:\n  generatedBy: \"issue-spec\"\n---\n# Provider\n",
		managedCommand:       "---\nname: \"Issue Spec: Archive\"\ncategory: \"Workflow\"\n---\n# Issue Spec Archive\n",
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
	if len(result.PrunedFiles) != 3 {
		t.Fatalf("pruned files = %v, want three recognized managed assets", result.PrunedFiles)
	}
	for _, path := range []string{managedSkill, managedProviderSkill, managedCommand} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("managed archive asset was not pruned: %s err=%v", path, err)
		}
	}
}

func TestWriteWorkflowArtifactsMigratesManagedClaudeSkillsToSharedLink(t *testing.T) {
	root := t.TempDir()
	staleSkill := filepath.Join(root, ".claude", "skills", "issue-spec-propose", "SKILL.md")
	staleResource := filepath.Join(root, ".claude", "skills", "issue-spec-propose", "references", "stale.md")
	for path, body := range map[string]string{
		staleSkill:    "---\nname: issue-spec-propose\nmetadata:\n  generatedBy: \"issue-spec\"\n---\nstale\n",
		staleResource: "stale generated resource\n",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	result, err := writeWorkflowArtifacts(root, "owner/repo", "claude", "skills")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(result.SkillLinks, ","), ".claude/skills -> ../.agents/skills"; got != want {
		t.Fatalf("skill links = %q, want %q", got, want)
	}
	claudeSkills := filepath.Join(root, ".claude", "skills")
	if info, err := os.Lstat(claudeSkills); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("managed Claude skills directory was not replaced with a symlink: mode=%v err=%v", info, err)
	}
	generated := readTestFile(t, staleSkill)
	if strings.Contains(generated, "stale") || !strings.Contains(generated, "name: issue-spec-propose") {
		t.Fatalf("Claude link does not resolve to current canonical skill:\n%s", generated)
	}
	if _, err := os.Stat(staleResource); !os.IsNotExist(err) {
		t.Fatalf("stale managed resource survived migration: %v", err)
	}
	second, err := writeWorkflowArtifacts(root, "owner/repo", "claude", "skills")
	if err != nil {
		t.Fatalf("refresh through existing shared skills link: %v", err)
	}
	if got, want := strings.Join(second.SkillLinks, ","), ".claude/skills -> ../.agents/skills"; got != want {
		t.Fatalf("refreshed skill links = %q, want %q", got, want)
	}
}

func TestWriteWorkflowArtifactsRejectsConflictingClaudeSkillsDirectory(t *testing.T) {
	root := t.TempDir()
	customSkill := filepath.Join(root, ".claude", "skills", "custom-review", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(customSkill), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(customSkill, []byte("# User-owned Claude skill\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := writeWorkflowArtifacts(root, "owner/repo", "claude", "skills")
	if err == nil || !strings.Contains(err.Error(), "reconcile it manually") {
		t.Fatalf("conflicting Claude skill error = %v", err)
	}
	if got := readTestFile(t, customSkill); got != "# User-owned Claude skill\n" {
		t.Fatalf("conflicting Claude skill changed after rejected migration: %q", got)
	}
	if _, err := os.Stat(filepath.Join(root, ".agents")); !os.IsNotExist(err) {
		t.Fatalf("rejected migration wrote canonical skills before validation: %v", err)
	}
}

func TestWriteWorkflowArtifactsRejectsUnexpectedClaudeSkillsLink(t *testing.T) {
	root := t.TempDir()
	claudeDir := filepath.Join(root, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	claudeSkills := filepath.Join(claudeDir, "skills")
	if err := os.Symlink(filepath.Join("..", "custom-skills"), claudeSkills); err != nil {
		t.Fatal(err)
	}

	_, err := writeWorkflowArtifacts(root, "owner/repo", "claude", "skills")
	if err == nil || !strings.Contains(err.Error(), "expected ../.agents/skills") {
		t.Fatalf("unexpected Claude skills link error = %v", err)
	}
	if target, readErr := os.Readlink(claudeSkills); readErr != nil || target != filepath.Join("..", "custom-skills") {
		t.Fatalf("unexpected Claude skills link changed: target=%q err=%v", target, readErr)
	}
	if _, err := os.Stat(filepath.Join(root, ".agents")); !os.IsNotExist(err) {
		t.Fatalf("rejected link migration wrote canonical skills before validation: %v", err)
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
	paths := make([]string, 0, 8)
	for _, skill := range []string{"apply", "propose", "workflow"} {
		paths = append(paths,
			filepath.Join(".agents", "skills", "issue-spec-"+skill, "SKILL.md"),
			filepath.Join(".claude", "skills", "issue-spec-"+skill, "SKILL.md"))
	}
	for _, command := range []string{"apply", "propose"} {
		paths = append(paths, filepath.Join(".claude", "commands", "issue-spec", command+".md"))
	}
	for _, relative := range paths {
		generated := readTestFile(t, filepath.Join(root, relative))
		if !strings.Contains(generated, neutralFooter) || strings.Contains(generated, "remain in GitHub issue-native storage") {
			t.Fatalf("generated workflow footer is not backend-neutral: %s", relative)
		}
	}
}

func TestWorkflowNoticeEndsWithAuthoritativeEnabledPhaseProtocol(t *testing.T) {
	const projectConflict = "Create initial SPEC typed comments first, then create QUESTION typed comments."
	const authority = "The built-in phase sequence and canonical artifact carriers are authoritative."
	for _, test := range []struct {
		name           string
		htmlReview     *workflow.HTMLReviewConfig
		wantProjection bool
	}{
		{name: "enabled", htmlReview: &workflow.HTMLReviewConfig{Enabled: true}, wantProjection: true},
		{name: "disabled", htmlReview: &workflow.HTMLReviewConfig{Enabled: false}},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan := workflow.Plan{Config: workflow.Config{
				Context:    workflow.WorkflowContext{"project_guidance": projectConflict},
				HTMLReview: test.htmlReview,
			}}
			notice := workflowNotice(plan)
			if strings.LastIndex(notice, authority) <= strings.LastIndex(notice, projectConflict) {
				t.Fatalf("authoritative protocol does not follow project text:\n%s", notice)
			}
			if got := strings.Contains(notice, "upsert the phase projection"); got != test.wantProjection {
				t.Fatalf("projection checkpoint present=%v, want %v:\n%s", got, test.wantProjection, notice)
			}
			if got := strings.Contains(notice, "Issue-body and projection prose never carry an open decision"); got != test.wantProjection {
				t.Fatalf("projection carrier present=%v, want %v:\n%s", got, test.wantProjection, notice)
			}
		})
	}
}

func TestWriteWorkflowArtifactsEmbedsProtocolConflictDiagnostics(t *testing.T) {
	root := t.TempDir()
	writeWorkflowTestFile(t, filepath.Join(root, "issue-spec", "config.yaml"), `
context:
  project_guidance: Create QUESTION typed comments after initial SPEC comments.
`)
	if _, err := writeWorkflowArtifacts(root, "owner/repo", "codex", "skills"); err != nil {
		t.Fatal(err)
	}
	skill := readTestFile(t, filepath.Join(root, ".agents", "skills", "issue-spec-propose", "SKILL.md"))
	for _, want := range []string{
		"`warning/phase_order_conflict`",
		"project workflow text context.project_guidance places QUESTION authoring after SPEC authoring",
		"The built-in phase sequence and canonical artifact carriers are authoritative.",
	} {
		if !strings.Contains(skill, want) {
			t.Fatalf("generated skill missing %q:\n%s", want, skill)
		}
	}
	if strings.LastIndex(skill, "The built-in phase sequence") <= strings.LastIndex(skill, "Create QUESTION typed comments after initial SPEC comments") {
		t.Fatalf("authoritative protocol does not follow conflicting project guidance:\n%s", skill)
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
	for name, root := range map[string]string{"generated": generatedRoot, "checked-in": projectRoot} {
		path := filepath.Join(root, ".claude", "skills")
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("%s Claude skills path is not a symlink: mode=%v err=%v", name, info, err)
		}
		target, err := os.Readlink(path)
		if err != nil || target != filepath.Join("..", ".agents", "skills") {
			t.Fatalf("%s Claude skills target = %q, err=%v", name, target, err)
		}
	}
	for _, relative := range append(
		[]string{
			filepath.Join(".agents", "skills", "issue-spec-apply", "SKILL.md"),
			filepath.Join(".agents", "skills", "issue-spec-github", "SKILL.md"),
			filepath.Join(".agents", "skills", "issue-spec-propose", "SKILL.md"),
			filepath.Join(".agents", "skills", "issue-spec-workflow", "SKILL.md"),
			filepath.Join(".agents", "skills", "issue-spec-workflow", "release.json"),
			filepath.Join(".claude", "skills", "issue-spec-apply", "SKILL.md"),
			filepath.Join(".claude", "skills", "issue-spec-github", "SKILL.md"),
			filepath.Join(".claude", "skills", "issue-spec-propose", "SKILL.md"),
			filepath.Join(".claude", "skills", "issue-spec-workflow", "SKILL.md"),
			filepath.Join(".claude", "skills", "issue-spec-workflow", "release.json"),
		},
		[]string{
			filepath.Join(".claude", "commands", "issue-spec", "apply.md"),
			filepath.Join(".claude", "commands", "issue-spec", "propose.md"),
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
	for name, root := range map[string]string{"generated": generatedRoot, "checked-in": projectRoot} {
		path := filepath.Join(root, ".agents", "skills", "issue-spec-workflow", "references", "human-review-projections.md")
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s disabled workflow retains projection reference, err=%v", name, err)
		}
		for _, retired := range []string{
			filepath.Join(".agents", "skills", "issue-spec-review", "SKILL.md"),
			filepath.Join(".agents", "skills", "issue-spec-verify", "SKILL.md"),
			filepath.Join(".claude", "commands", "issue-spec", "review.md"),
			filepath.Join(".claude", "commands", "issue-spec", "verify.md"),
		} {
			if _, err := os.Stat(filepath.Join(root, retired)); !os.IsNotExist(err) {
				t.Fatalf("%s retains retired workflow authority %s: %v", name, retired, err)
			}
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
	if got, want := len(result.GlobalPromptFiles), 2; got != want {
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
	if !result.GlobalPromptsDryRun || len(result.GlobalPromptFiles) != 2 || result.WorkflowSource == "" {
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
	provider := minimalProviderPlanForTest("aone")
	provider.DisplayName, provider.CodeChangeLabel = "Aone Code", "Merge request"
	provider.ChangeComment, provider.EvidenceSnapshot = true, true
	provider.Capabilities = append(provider.Capabilities, codereview.CapabilityChangeComment, codereview.CapabilityEvidenceSnapshot)
	result, err := writeWorkflowArtifactsWithProvider(root, "browser-e2e/httpbin", "codex", "skills", provider)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.SkillFiles) == 0 {
		t.Fatal("provider workflow generated no skills")
	}
	generated := readTestFile(t, filepath.Join(root, ".agents", "skills", "issue-spec-code-provider", "SKILL.md"))
	for _, want := range []string{
		"Provider-bound Human Handoff Workflow",
		"change-create=true change-comment=true audit-snapshot=true",
		"provider and human reviewer own CI",
		"Report the exact head",
		"Stop. The human reviews current provider-native CI",
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
