package jobs

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

func TestMaterializeTrustedAgentSkillsRejectsRuntimeSkillRootSymlink(t *testing.T) {
	root := t.TempDir()
	skill := filepath.Join(root, "skill")
	writeTrustedSkill(t, skill, "safe")
	codexHome := filepath.Join(root, "codex")
	escape := filepath.Join(root, "escape")
	if err := os.MkdirAll(codexHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(escape, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(escape, filepath.Join(codexHome, "skills")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	err := materializeTrustedAgentSkills(codexHome, []string{skill})
	if err == nil || !strings.Contains(err.Error(), "non-symlink directory") {
		t.Fatalf("runtime skill root symlink error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(escape, "skill")); !os.IsNotExist(err) {
		t.Fatalf("skill escaped through runtime root symlink: %v", err)
	}
}

func TestCollectTrustedAgentSkillsRejectsCatalogChildSymlink(t *testing.T) {
	root := t.TempDir()
	catalog := filepath.Join(root, "catalog")
	valid := filepath.Join(catalog, "valid")
	linked := filepath.Join(root, "linked")
	writeTrustedSkill(t, valid, "valid")
	writeTrustedSkill(t, linked, "linked")
	if err := os.Symlink(linked, filepath.Join(catalog, "linked")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := collectTrustedAgentSkills([]string{catalog}); err == nil || !strings.Contains(err.Error(), "catalog contains symlink") {
		t.Fatalf("catalog child symlink error = %v", err)
	}
}

func TestMaterializeTrustedAgentSkillsPreservesExecutableFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("executable mode is not portable on Windows")
	}
	root := t.TempDir()
	skill := filepath.Join(root, "skill")
	writeTrustedSkill(t, skill, "safe")
	script := filepath.Join(skill, "scripts", "run.sh")
	if err := os.MkdirAll(filepath.Dir(script), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	codexHome := filepath.Join(root, "codex")
	if err := materializeTrustedAgentSkills(codexHome, []string{skill}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(codexHome, "skills", "skill", "scripts", "run.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("materialized executable mode = %04o, want 0700", info.Mode().Perm())
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
