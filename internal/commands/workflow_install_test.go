package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/higress-group/issue-spec/internal/workflow"
)

func TestWorkflowUpdateProtectsChangesAndPreservesProjectSkills(t *testing.T) {
	root := t.TempDir()
	_, err := writeWorkflowArtifacts(root, "o/r", "codex,claude", "skills")
	if err != nil {
		t.Fatal(err)
	}
	custom := filepath.Join(root, ".claude/skills/project/SKILL.md")
	writeWorkflowTestFile(t, custom, "project-owned")
	hostOnly := filepath.Join(root, ".agents/skills/host-only/SKILL.md")
	writeWorkflowTestFile(t, hostOnly, "native host")
	config := filepath.Join(root, "issue-spec/config.yaml")
	writeWorkflowTestFile(t, config, "html_review:\n  enabled: false\n")
	owned := filepath.Join(root, ".agents/skills/issue-spec-apply/SKILL.md")
	writeWorkflowTestFile(t, owned, "local customization")
	before := readTestFile(t, filepath.Join(root, workflowManifestPath))
	plan, err := workflow.Resolve(root)
	if err != nil {
		t.Fatal(err)
	}
	update, err := planWorkflowUpdate(root, "o/r", "skills", workflowTools, plan, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(update.Conflicts) != 1 || !strings.Contains(update.Conflicts[0], "issue-spec-apply") {
		t.Fatalf("conflicts: %v", update.Conflicts)
	}
	if err := applyWorkflowUpdate(root, update); err == nil {
		t.Fatal("accepted local edit")
	}
	if readTestFile(t, owned) != "local customization" || readTestFile(t, filepath.Join(root, workflowManifestPath)) != before || readTestFile(t, custom) != "project-owned" {
		t.Fatal("conflicting update mutated files")
	}
	if _, err := os.Lstat(filepath.Join(root, ".claude/skills/host-only")); !os.IsNotExist(err) {
		t.Fatal("host-only skill exposed")
	}
}
func TestWorkflowUpdateSplitsLegacyLinkAndIsIdempotent(t *testing.T) {
	root := t.TempDir()
	_, err := writeWorkflowArtifacts(root, "o/r", "codex", "skills")
	if err != nil {
		t.Fatal(err)
	}
	writeWorkflowTestFile(t, filepath.Join(root, ".agents/skills/custom/SKILL.md"), "custom")
	if err := os.Mkdir(filepath.Join(root, ".claude"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../.agents/skills", filepath.Join(root, ".claude/skills")); err != nil {
		t.Fatal(err)
	}
	plan, err := workflow.Resolve(root)
	if err != nil {
		t.Fatal(err)
	}
	update, err := planWorkflowUpdate(root, "o/r", "skills", workflowTools, plan, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := applyWorkflowUpdate(root, update); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(filepath.Join(root, ".claude/skills")); err != nil || !info.IsDir() {
		t.Fatal("whole-directory link remains")
	}
	if got, err := os.Readlink(filepath.Join(root, ".claude/skills/custom")); err != nil || got != "../../.agents/skills/custom" {
		t.Fatalf("lost previous exposure %s %v", got, err)
	}
	again, err := planWorkflowUpdate(root, "o/r", "skills", workflowTools, plan, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Changes) != 0 || len(again.Conflicts) != 0 {
		t.Fatalf("not idempotent: %+v", again)
	}
}
func TestWorkflowUpdateRollsBackWriteFailure(t *testing.T) {
	root := t.TempDir()
	writeWorkflowTestFile(t, filepath.Join(root, "one"), "original")
	update := workflowUpdatePlan{Changes: []workflowFileChange{{Path: "one", Action: "update", body: []byte("new")}, {Path: "new-dir/two", Action: "link", link: "invalid\x00target"}}}
	if err := applyWorkflowUpdate(root, update); err == nil {
		t.Fatal("expected symlink error")
	}
	if readTestFile(t, filepath.Join(root, "one")) != "original" {
		t.Fatal("rollback lost original")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("rollback left changes: %v", entries)
	}
}
func TestWorkflowUpdateRejectsSymlinkParents(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, ".agents")); err != nil {
		t.Fatal(err)
	}
	_, err := writeWorkflowArtifacts(root, "o/r", "codex", "skills")
	if err == nil {
		t.Fatal("wrote through external symlink")
	}
	entries, _ := os.ReadDir(outside)
	if len(entries) != 0 {
		t.Fatal("changed external directory")
	}
}

func TestWorkflowUpdateRejectsChangesAfterPreview(t *testing.T) {
	root := t.TempDir()
	plan, _ := workflow.Resolve(root)
	update, err := planWorkflowUpdate(root, "o/r", "skills", workflowTools, plan, nil)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ".agents/skills/issue-spec-apply/SKILL.md")
	writeWorkflowTestFile(t, path, "concurrent edit")
	if err := applyWorkflowUpdate(root, update); err == nil {
		t.Fatal("accepted concurrent creation")
	}
	if readTestFile(t, path) != "concurrent edit" {
		t.Fatal("overwrote concurrent creation")
	}
}

func TestLegacyCommandsUpdateKeepsCanonicalSkillsAndIntegrity(t *testing.T) {
	root := t.TempDir()
	_, err := writeWorkflowArtifacts(root, "o/r", "codex,claude", "both")
	if err != nil {
		t.Fatal(err)
	}
	skill := filepath.Join(root, ".agents/skills/issue-spec-apply/SKILL.md")
	before := readTestFile(t, skill)
	for i := 0; i < 2; i++ {
		if _, err := writeWorkflowArtifacts(root, "other/repo", "claude", "commands"); err != nil {
			t.Fatal(err)
		}
	}
	if got := readTestFile(t, skill); got != before {
		t.Fatal("command delivery changed skills")
	}
	if _, err := readWorkflowReleaseManifest(root, ""); err != nil {
		t.Fatal(err)
	}
}
