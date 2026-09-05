package commands

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/higress-group/issue-spec/internal/templates"
)

func TestWorkflowProjectionResourceLifecycleAndManifest(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "issue-spec", "config.yaml")
	writeWorkflowTestFile(t, configPath, "html_review:\n  enabled: true\n")
	custom := []string{
		"AGENTS.md",
		".agents/skills/custom-role/SKILL.md",
		".agents/skills/issue-spec-workflow/references/projections/operator-notes.md",
		".agents/skills/issue-spec-workflow/assets/custom-example.md",
	}
	for _, relative := range custom {
		writeWorkflowTestFile(t, filepath.Join(root, relative), "user-owned\n")
	}
	if _, err := writeWorkflowArtifacts(root, "owner/repo", "codex,claude", "both"); err != nil {
		t.Fatal(err)
	}
	manifest, err := readWorkflowReleaseManifest(root, "")
	if err != nil {
		t.Fatal(err)
	}
	manifestPaths := map[string]bool{}
	for _, file := range manifest.Files {
		manifestPaths[file.Path] = true
	}
	for _, resource := range templates.HumanReviewProjectionResources() {
		relative := ".agents/skills/issue-spec-workflow/" + resource.Path
		if !manifestPaths[relative] {
			t.Errorf("generated resource absent from release manifest: %s", relative)
		}
		if got := readTestFile(t, filepath.Join(root, relative)); got != resource.Content {
			t.Errorf("resource differs from template: %s", relative)
		}
	}
	if _, err := writeWorkflowArtifacts(root, "owner/repo", "codex,claude", "both"); err != nil {
		t.Fatal(err)
	}
	second, err := readWorkflowReleaseManifest(root, "")
	if err != nil || manifest.ContentDigest != second.ContentDigest {
		t.Fatalf("second generation changed content: %v", err)
	}
	// References participate in integrity verification, not just the entrypoints.
	changed := filepath.Join(root, ".agents/skills/issue-spec-workflow/references/projections/design.md")
	writeWorkflowTestFile(t, changed, "changed after generation\n")
	if _, err := readWorkflowReleaseManifest(root, ""); err == nil {
		t.Fatal("manifest accepted a changed phase reference")
	}
	writeWorkflowTestFile(t, configPath, "html_review:\n  enabled: false\n")
	if _, err := writeWorkflowArtifacts(root, "owner/repo", "codex,claude", "both"); err != nil {
		t.Fatal(err)
	}
	for _, resource := range templates.HumanReviewProjectionResources() {
		if _, err := os.Stat(filepath.Join(root, ".agents/skills/issue-spec-workflow", resource.Path)); !os.IsNotExist(err) {
			t.Errorf("opt-out retained projection resource %s: %v", resource.Path, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, ".agents/skills/issue-spec-workflow/references/implementation-review.md")); err != nil {
		t.Errorf("HTML opt-out removed non-HTML review instructions: %v", err)
	}
	for _, relative := range custom {
		if got := readTestFile(t, filepath.Join(root, relative)); got != "user-owned\n" {
			t.Errorf("generation changed user file %s", relative)
		}
	}
	if _, err := readWorkflowReleaseManifest(root, ""); err != nil {
		t.Fatal(err)
	}
}
