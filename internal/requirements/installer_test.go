package requirements

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveTargetPreviewIsExplicit(t *testing.T) {
	home := t.TempDir()
	tests := []struct {
		name    string
		target  Target
		options TargetOptions
		want    string
	}{
		{name: "codex default", target: TargetCodex, options: TargetOptions{Home: home}, want: filepath.Join(home, ".codex", "skills", SkillName)},
		{name: "codex env root", target: TargetCodex, options: TargetOptions{Home: home, CodexHome: filepath.Join(home, "codex-home")}, want: filepath.Join(home, "codex-home", "skills", SkillName)},
		{name: "claude default", target: TargetClaude, options: TargetOptions{Home: home}, want: filepath.Join(home, ".claude", "skills", SkillName)},
		{name: "claude env root", target: TargetClaude, options: TargetOptions{Home: home, ClaudeConfigDir: filepath.Join(home, "claude-home")}, want: filepath.Join(home, "claude-home", "skills", SkillName)},
		{name: "expert override", target: TargetClaude, options: TargetOptions{TargetDir: filepath.Join(home, "elsewhere")}, want: filepath.Join(home, "elsewhere")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ResolveTarget(test.target, test.options)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want || !filepath.IsAbs(got) {
				t.Fatalf("target = %q, want absolute %q", got, test.want)
			}
		})
	}
	if _, err := ResolveTarget(Target("other"), TargetOptions{TargetDir: filepath.Join(home, "override")}); err == nil {
		t.Fatal("explicit override accepted an unknown target kind")
	}
}

func TestInstallerCreateNoopManagedUpgradeAndConflict(t *testing.T) {
	home := t.TempDir()
	options := TargetOptions{Home: home}
	current, err := Canonical(Distribution{})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := PreviewInstall(current, TargetCodex, options, "development")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Action != ActionCreate || !filepath.IsAbs(plan.Path) {
		t.Fatalf("create plan = %+v", plan)
	}
	result, err := Install(current, plan)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed || result.Action != ActionCreate {
		t.Fatalf("create result = %+v", result)
	}
	if _, err := os.Stat(filepath.Join(plan.Path, ManagedManifestName)); err != nil {
		t.Fatalf("managed manifest not installed: %v", err)
	}

	plan, err = PreviewInstall(current, TargetCodex, options, "development")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Action != ActionNoop {
		t.Fatalf("second plan action = %s", plan.Action)
	}
	result, err = Install(current, plan)
	if err != nil || result.Changed {
		t.Fatalf("no-op result = %+v, %v", result, err)
	}

	old := testBundle(t, "older managed skill\n")
	oldHome := t.TempDir()
	oldOptions := TargetOptions{Home: oldHome}
	oldPlan, err := PreviewInstall(old, TargetClaude, oldOptions, "development")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Install(old, oldPlan); err != nil {
		t.Fatal(err)
	}
	upgrade, err := PreviewInstall(current, TargetClaude, oldOptions, "development")
	if err != nil {
		t.Fatal(err)
	}
	if upgrade.Action != ActionManagedUpgrade || upgrade.CurrentContentID != old.Manifest.ContentID {
		t.Fatalf("upgrade plan = %+v", upgrade)
	}
	if _, err := Install(current, upgrade); err != nil {
		t.Fatal(err)
	}
	upgraded, err := PreviewInstall(current, TargetClaude, oldOptions, "development")
	if err != nil || upgraded.Action != ActionNoop {
		t.Fatalf("post-upgrade plan = %+v, %v", upgraded, err)
	}

	if err := os.WriteFile(filepath.Join(upgraded.Path, "SKILL.md"), []byte("user edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	conflict, err := PreviewInstall(current, TargetClaude, oldOptions, "development")
	if err != nil {
		t.Fatal(err)
	}
	if conflict.Action != ActionUserModified {
		t.Fatalf("conflict plan = %+v", conflict)
	}
	if _, err := Install(current, conflict); !errors.Is(err, ErrUserModified) {
		t.Fatalf("unconfirmed replace error = %v", err)
	}
	if _, err := ApplyConflictDecision(current, conflict, "development", ConflictCancel, ""); !errors.Is(err, ErrCancelled) {
		t.Fatalf("cancel error = %v", err)
	}
	alternatePath := filepath.Join(t.TempDir(), "alternate-skill")
	alternate, err := ApplyConflictDecision(current, conflict, "development", ConflictAlternate, alternatePath)
	if err != nil {
		t.Fatal(err)
	}
	if alternate.Path != alternatePath || alternate.Action != ActionCreate || !strings.Contains(alternate.Reason, "own confirmation") {
		t.Fatalf("alternate plan = %+v", alternate)
	}
	replace, err := ApplyConflictDecision(current, conflict, "development", ConflictReplace, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Install(current, replace); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(filepath.Join(replace.Path, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	canonicalSkill, _ := fileNamed(current.Files, "SKILL.md")
	if string(contents) != string(canonicalSkill.Data) {
		t.Fatal("explicit replace did not install canonical bytes")
	}
}

func TestInstallerDetectsExtraFilesAndStalePlan(t *testing.T) {
	bundle, err := Canonical(Distribution{})
	if err != nil {
		t.Fatal(err)
	}
	options := TargetOptions{Home: t.TempDir()}
	create, err := PreviewInstall(bundle, TargetCodex, options, "development")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(create.Path, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(bundle, create); !errors.Is(err, ErrPlanChanged) {
		t.Fatalf("stale create error = %v", err)
	}

	options = TargetOptions{Home: t.TempDir()}
	create, err = PreviewInstall(bundle, TargetCodex, options, "development")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Install(bundle, create); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(create.Path, "notes.txt"), []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	conflict, err := PreviewInstall(bundle, TargetCodex, options, "development")
	if err != nil {
		t.Fatal(err)
	}
	if conflict.Action != ActionUserModified {
		t.Fatalf("extra file action = %s", conflict.Action)
	}
}

func TestInstallerLeavesNoStagingOrBackupAfterSuccess(t *testing.T) {
	bundle, err := Canonical(Distribution{})
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	plan, err := PreviewInstall(bundle, TargetClaude, TargetOptions{Home: home}, "development")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Install(bundle, plan); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Dir(plan.Path))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".stage-") || strings.Contains(entry.Name(), ".backup-") {
			t.Fatalf("temporary install directory remains: %s", entry.Name())
		}
	}
}
