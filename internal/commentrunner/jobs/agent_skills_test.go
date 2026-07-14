package jobs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMaterializeTrustedAgentSkillsCopiesDirectAndCatalogRoots(t *testing.T) {
	root := t.TempDir()
	catalog := filepath.Join(root, "catalog")
	direct := filepath.Join(root, "direct-skill")
	writeTrustedSkill(t, filepath.Join(catalog, "generated-workflow"), "workflow")
	writeTrustedSkill(t, direct, "provider")
	codexHome := filepath.Join(root, "codex")

	if err := materializeTrustedAgentSkills(codexHome, []string{catalog, direct}); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{
		filepath.Join(codexHome, "skills", "generated-workflow", "SKILL.md"): "workflow",
		filepath.Join(codexHome, "skills", "direct-skill", "SKILL.md"):       "provider",
	} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Fatalf("skill %s = %q, %v; want %q", path, got, err, want)
		}
	}
}

func TestMaterializeTrustedAgentSkillsRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	skill := filepath.Join(root, "skill")
	writeTrustedSkill(t, skill, "safe")
	if err := os.Symlink(filepath.Join(skill, "SKILL.md"), filepath.Join(skill, "linked.md")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := materializeTrustedAgentSkills(filepath.Join(root, "codex"), []string{skill}); err == nil {
		t.Fatal("expected symlink rejection")
	}
}

func TestCollectTrustedAgentSkillsRejectsDuplicateNames(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "first", "same")
	second := filepath.Join(root, "second", "same")
	writeTrustedSkill(t, first, "one")
	writeTrustedSkill(t, second, "two")
	if _, err := collectTrustedAgentSkills([]string{first, second}); err == nil {
		t.Fatal("expected duplicate skill name rejection")
	}
}

func writeTrustedSkill(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
