package commands

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestResolveRunnerOperatorSkillDirsNormalizesAndDeduplicates(t *testing.T) {
	root := t.TempDir()
	skill := filepath.Join(root, "code-host")
	if err := os.Mkdir(skill, 0o700); err != nil {
		t.Fatal(err)
	}
	got, err := resolveRunnerOperatorSkillDirs([]string{skill, skill})
	if err != nil {
		t.Fatalf("resolveRunnerOperatorSkillDirs() error = %v", err)
	}
	want, err := filepath.Abs(skill)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{want}) {
		t.Fatalf("resolveRunnerOperatorSkillDirs() = %#v, want %#v", got, []string{want})
	}
}

func TestResolveRunnerOperatorSkillDirsRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveRunnerOperatorSkillDirs([]string{link}); err == nil {
		t.Fatal("resolveRunnerOperatorSkillDirs() accepted a symlink")
	}
}
