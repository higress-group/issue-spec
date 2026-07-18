package commands

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/requirements"
)

type countingRequirementsReader struct {
	reader io.Reader
	reads  int
}

func (r *countingRequirementsReader) Read(data []byte) (int, error) {
	r.reads++
	return r.reader.Read(data)
}

func TestRequirementsSetupTokenStdinRejectsTerminalBeforeRead(t *testing.T) {
	input := &countingRequirementsReader{reader: strings.NewReader("must-not-be-read\n")}
	var output, errOutput bytes.Buffer
	app := newApp(input, &output, &errOutput)
	predicateCalls := 0
	app.stdinIsTerminal = func(got io.Reader) bool {
		predicateCalls++
		if got != input {
			t.Fatalf("terminal predicate input = %T, want the command stdin", got)
		}
		return true
	}

	if code := app.runRequirementsSetup(t.Context(), []string{"--token-stdin"}); code != 1 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, output.String(), errOutput.String())
	}
	if predicateCalls != 1 || input.reads != 0 || !strings.Contains(errOutput.String(), "refuses terminal input") ||
		!strings.Contains(errOutput.String(), "hidden PAT prompt") {
		t.Fatalf("predicate_calls=%d reads=%d stdout=%q stderr=%q", predicateCalls, input.reads, output.String(), errOutput.String())
	}
}

func TestRequirementsSetupSkillFlagValidation(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "archive needs checksum", args: []string{"--skill-archive", "skill.zip"}, want: "--skill-archive requires --skill-archive-sha256"},
		{name: "checksum needs archive", args: []string{"--skill-archive-sha256", strings.Repeat("a", 64)}, want: "--skill-archive-sha256 requires --skill-archive"},
		{name: "alternate needs target", args: []string{"--skill-conflict", "alternate"}, want: "--skill-conflict alternate requires --skill-alternate-target"},
		{name: "target needs alternate", args: []string{"--skill-alternate-target", "elsewhere"}, want: "--skill-alternate-target requires --skill-conflict alternate"},
		{name: "only three decisions", args: []string{"--skill-conflict", "alternate-destination"}, want: "--skill-conflict must be cancel, replace, or alternate"},
		{name: "no release selector", args: []string{"--skill-archive-release", "latest"}, want: "flag provided but not defined"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output, errOutput bytes.Buffer
			app := newApp(strings.NewReader("must-not-be-read"), &output, &errOutput)
			app.stdinIsTerminal = func(io.Reader) bool { return false }
			if code := app.runRequirementsSetup(t.Context(), test.args); code != 2 {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, output.String(), errOutput.String())
			}
			if !strings.Contains(errOutput.String(), test.want) {
				t.Fatalf("stderr=%q, want %q", errOutput.String(), test.want)
			}
		})
	}
}

func TestRequirementsSetupVerifiedArchivePreviewAndFailuresDoNotWrite(t *testing.T) {
	t.Run("verified development archive", func(t *testing.T) {
		root, target := configureRequirementsRepairEnvironment(t)
		archivePath, checksum := writeRequirementsRepairArchive(t, root, requirements.Distribution{})
		secret := "verified-archive-secret"
		actions := []string{"contribute"}
		server := newRequirementsTestServer(t, "issue-spec:verified-archive", secret, &actions)
		defer server.Close()

		var output, errOutput bytes.Buffer
		app := newApp(strings.NewReader(secret+"\n"), &output, &errOutput)
		app.resolveRequirementsToken = noRequirementsToken
		args := []string{"--server", server.URL, "--repo", "owner/repo", "--agent", "codex", "--profile", "verified-archive",
			"--token-stdin", "--skill-archive", archivePath, "--skill-archive-sha256", checksum, "--json"}
		if code := app.runRequirementsSetup(t.Context(), args); code != 0 {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, output.String(), errOutput.String())
		}
		var result requirementsSetupResult
		if err := json.Unmarshal(output.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.Applied || result.SkillSource != "archive:"+archivePath || !strings.Contains(result.Compatibility, "development") ||
			result.SkillPlan.Target != requirements.TargetCodex || result.SkillPlan.Action != requirements.ActionCreate ||
			result.SkillPlan.Path != target || result.SkillPlan.Reason == "" || result.SkillPlan.ContentID == "" || result.ConflictDecision != "cancel" {
			t.Fatalf("archive preview=%+v", result)
		}
		assertRequirementsRepairSetupUnapplied(t, "verified-archive", target)

		output.Reset()
		errOutput.Reset()
		apply := newApp(strings.NewReader(secret+"\n"), &output, &errOutput)
		apply.resolveRequirementsToken = noRequirementsToken
		apply.storeRequirementsToken = func(context.Context, auth.Profile, string, bool) (string, error) { return "keyring", nil }
		if code := apply.runRequirementsSetup(t.Context(), append(args, "--yes")); code != 0 {
			t.Fatalf("apply exit=%d stdout=%q stderr=%q", code, output.String(), errOutput.String())
		}
		if err := json.Unmarshal(output.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if !result.Applied || result.SkillResult == nil || !result.SkillResult.Changed || result.SkillSource != "archive:"+archivePath {
			t.Fatalf("archive apply=%+v", result)
		}
		assertRequirementsRepairCanonicalTarget(t, target)
	})

	t.Run("checksum mismatch", func(t *testing.T) {
		root, target := configureRequirementsRepairEnvironment(t)
		archivePath, _ := writeRequirementsRepairArchive(t, root, requirements.Distribution{})
		secret := "corrupt-archive-secret"
		actions := []string{"contribute"}
		server := newRequirementsTestServer(t, "issue-spec:corrupt-archive", secret, &actions)
		defer server.Close()

		var output, errOutput bytes.Buffer
		app := newApp(strings.NewReader(secret+"\n"), &output, &errOutput)
		app.resolveRequirementsToken = noRequirementsToken
		app.saveRequirementsProfile = func(auth.Profile, bool) error {
			t.Fatal("checksum failure attempted to save a profile")
			return nil
		}
		app.storeRequirementsToken = func(context.Context, auth.Profile, string, bool) (string, error) {
			t.Fatal("checksum failure attempted to store a token")
			return "", nil
		}
		args := []string{"--server", server.URL, "--repo", "owner/repo", "--agent", "codex", "--profile", "corrupt-archive",
			"--token-stdin", "--skill-archive", archivePath, "--skill-archive-sha256", strings.Repeat("0", 64), "--yes"}
		if code := app.runRequirementsSetup(t.Context(), args); code != 1 || !strings.Contains(errOutput.String(), "checksum mismatch") {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, output.String(), errOutput.String())
		}
		assertRequirementsRepairSetupUnapplied(t, "corrupt-archive", target)
	})

	t.Run("corrupt archive with matching checksum", func(t *testing.T) {
		root, target := configureRequirementsRepairEnvironment(t)
		raw := []byte("not a zip archive")
		archivePath := filepath.Join(root, "corrupt-skill.zip")
		if err := os.WriteFile(archivePath, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(raw)
		app := newApp(strings.NewReader(""), io.Discard, io.Discard)
		if _, _, _, err := app.requirementsInstallPlanFrom(requirements.TargetCodex, archivePath, hex.EncodeToString(digest[:])); err == nil || !strings.Contains(err.Error(), "open requirements skill archive") {
			t.Fatalf("corrupt archive error=%v", err)
		}
		if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("corrupt archive mutated target %s: %v", target, err)
		}
	})

	t.Run("incompatible published archive", func(t *testing.T) {
		root, target := configureRequirementsRepairEnvironment(t)
		archivePath, checksum := writeRequirementsRepairArchive(t, root, requirements.Distribution{
			Channel: "stable", SourceRevision: strings.Repeat("a", 40), CLIBuild: "0.1.0",
		})
		secret := "incompatible-archive-secret"
		actions := []string{"contribute"}
		server := newRequirementsTestServer(t, "issue-spec:incompatible-archive", secret, &actions)
		defer server.Close()

		var output, errOutput bytes.Buffer
		app := newApp(strings.NewReader(secret+"\n"), &output, &errOutput)
		app.resolveRequirementsToken = noRequirementsToken
		app.saveRequirementsProfile = func(auth.Profile, bool) error {
			t.Fatal("compatibility failure attempted to save a profile")
			return nil
		}
		app.storeRequirementsToken = func(context.Context, auth.Profile, string, bool) (string, error) {
			t.Fatal("compatibility failure attempted to store a token")
			return "", nil
		}
		args := []string{"--server", server.URL, "--repo", "owner/repo", "--agent", "codex", "--profile", "incompatible-archive",
			"--token-stdin", "--skill-archive", archivePath, "--skill-archive-sha256", checksum, "--yes"}
		if code := app.runRequirementsSetup(t.Context(), args); code != 1 || !strings.Contains(errOutput.String(), "development CLI cannot install") {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, output.String(), errOutput.String())
		}
		assertRequirementsRepairSetupUnapplied(t, "incompatible-archive", target)
	})
}

func TestRequirementsSetupUserModifiedDecisions(t *testing.T) {
	tests := []struct {
		name     string
		decision string
		apply    bool
	}{
		{name: "cancel", decision: "cancel"},
		{name: "replace", decision: "replace", apply: true},
		{name: "alternate", decision: "alternate", apply: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root, target := configureRequirementsRepairEnvironment(t)
			if err := os.MkdirAll(target, 0o755); err != nil {
				t.Fatal(err)
			}
			customPath := filepath.Join(target, "mine.txt")
			if err := os.WriteFile(customPath, []byte("keep me\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			secret := "conflict-" + test.name + "-secret"
			actions := []string{"contribute"}
			server := newRequirementsTestServer(t, "issue-spec:conflict-"+test.name, secret, &actions)
			defer server.Close()

			var output, errOutput bytes.Buffer
			app := newApp(strings.NewReader(secret+"\n"), &output, &errOutput)
			app.resolveRequirementsToken = noRequirementsToken
			app.storeRequirementsToken = func(context.Context, auth.Profile, string, bool) (string, error) { return "keyring", nil }
			args := []string{"--server", server.URL, "--repo", "owner/repo", "--agent", "codex", "--profile", "conflict-" + test.name,
				"--token-stdin", "--skill-conflict", test.decision, "--json"}
			alternateTarget := filepath.Join(root, "alternate", requirements.SkillName)
			if test.decision == "alternate" {
				args = append(args, "--skill-alternate-target", alternateTarget)
			}
			if test.apply {
				args = append(args, "--yes")
			}
			if code := app.runRequirementsSetup(t.Context(), args); code != 0 {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, output.String(), errOutput.String())
			}
			var result requirementsSetupResult
			if err := json.Unmarshal(output.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result.ConflictDecision != test.decision || result.Applied != test.apply {
				t.Fatalf("result=%+v", result)
			}
			switch test.decision {
			case "cancel":
				if result.SkillPlan.Action != requirements.ActionUserModified {
					t.Fatalf("cancel preview=%+v", result)
				}
				assertRequirementsRepairSetupUnapplied(t, "conflict-cancel", "")
				if data, err := os.ReadFile(customPath); err != nil || string(data) != "keep me\n" {
					t.Fatalf("cancel changed custom target: data=%q err=%v", data, err)
				}
			case "replace":
				if result.SkillPlan.Action != requirements.ActionUserModified || result.SkillResult == nil || !result.SkillResult.Changed {
					t.Fatalf("replace result=%+v", result)
				}
				if _, err := os.Stat(customPath); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("replace retained custom file: %v", err)
				}
				assertRequirementsRepairCanonicalTarget(t, target)
			case "alternate":
				if result.SkillPlan.Action != requirements.ActionCreate || result.SkillPlan.Path != alternateTarget ||
					result.SkillResult == nil || !result.SkillResult.Changed || !strings.Contains(result.SkillPlan.Reason, "own confirmation") {
					t.Fatalf("alternate result=%+v", result)
				}
				if data, err := os.ReadFile(customPath); err != nil || string(data) != "keep me\n" {
					t.Fatalf("alternate changed primary target: data=%q err=%v", data, err)
				}
				assertRequirementsRepairCanonicalTarget(t, alternateTarget)
			}
		})
	}
}

func TestRequirementsSetupCancelWithConfirmationStillDoesNotWrite(t *testing.T) {
	_, target := configureRequirementsRepairEnvironment(t)
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "mine.txt"), []byte("keep me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	secret := "cancel-confirmed-secret"
	actions := []string{"contribute"}
	server := newRequirementsTestServer(t, "issue-spec:cancel-confirmed", secret, &actions)
	defer server.Close()

	var output, errOutput bytes.Buffer
	app := newApp(strings.NewReader(secret+"\n"), &output, &errOutput)
	app.resolveRequirementsToken = noRequirementsToken
	app.saveRequirementsProfile = func(auth.Profile, bool) error {
		t.Fatal("cancel attempted to save a profile")
		return nil
	}
	app.storeRequirementsToken = func(context.Context, auth.Profile, string, bool) (string, error) {
		t.Fatal("cancel attempted to store a token")
		return "", nil
	}
	app.installRequirements = func(requirements.Bundle, requirements.InstallPlan) (requirements.InstallResult, error) {
		t.Fatal("cancel attempted to install the skill")
		return requirements.InstallResult{}, nil
	}
	args := []string{"--server", server.URL, "--repo", "owner/repo", "--agent", "codex", "--profile", "cancel-confirmed",
		"--token-stdin", "--skill-conflict", "cancel", "--yes"}
	if code := app.runRequirementsSetup(t.Context(), args); code != 1 || !strings.Contains(errOutput.String(), "installation cancelled") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, output.String(), errOutput.String())
	}
	assertRequirementsRepairSetupUnapplied(t, "cancel-confirmed", "")
	if data, err := os.ReadFile(filepath.Join(target, "mine.txt")); err != nil || string(data) != "keep me\n" {
		t.Fatalf("cancel changed target: data=%q err=%v", data, err)
	}
}

func TestRequirementsSetupTextPreviewShowsSkillDecision(t *testing.T) {
	result := requirementsSetupResult{
		Profile: "team", Repository: "owner/repo", Agent: requirements.TargetCodex, User: "external-user",
		Visibility: "public", Policy: "public", AllowedActions: []string{"contribute"}, ContextPath: "/config/requirements-context.json",
		SkillSource: "archive:/tmp/skill.zip", Compatibility: "compatible with CLI 0.1.0", ConflictDecision: "alternate",
		SkillPlan: requirements.InstallPlan{Target: requirements.TargetCodex, Path: "/tmp/alternate", Action: requirements.ActionCreate,
			Reason: "target does not exist", ContentID: "sha256:content"},
	}
	var output bytes.Buffer
	printRequirementsSetupPreview(&output, result)
	for _, want := range []string{"skill source: archive:/tmp/skill.zip", "skill compatibility: compatible with CLI 0.1.0",
		"skill target: codex", "skill action: create", "skill reason: target does not exist", "skill content ID: sha256:content",
		"skill conflict decision: alternate"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("preview %q does not contain %q", output.String(), want)
		}
	}
}

func configureRequirementsRepairEnvironment(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv(auth.ConfigDirEnv, filepath.Join(root, "config"))
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("CODEX_HOME", filepath.Join(root, "codex"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(root, "claude"))
	return root, filepath.Join(root, "codex", "skills", requirements.SkillName)
}

func writeRequirementsRepairArchive(t *testing.T, root string, distribution requirements.Distribution) (string, string) {
	t.Helper()
	raw, err := requirements.BuildArchive(distribution)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "requirements-skill.zip")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	return path, hex.EncodeToString(digest[:])
}

func assertRequirementsRepairSetupUnapplied(t *testing.T, profile, target string) {
	t.Helper()
	if _, _, err := auth.ResolveProfile(profile, ""); err == nil {
		t.Fatalf("setup persisted profile %q", profile)
	}
	if _, err := requirements.LoadActiveContext(); !errors.Is(err, requirements.ErrContextNotConfigured) {
		t.Fatalf("setup persisted context: %v", err)
	}
	if target != "" {
		if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("setup mutated target %s: %v", target, err)
		}
	}
}

func assertRequirementsRepairCanonicalTarget(t *testing.T, target string) {
	t.Helper()
	for _, name := range []string{"SKILL.md", requirements.ManagedManifestName} {
		if info, err := os.Stat(filepath.Join(target, name)); err != nil || !info.Mode().IsRegular() {
			t.Fatalf("canonical target file %s: info=%v err=%v", name, info, err)
		}
	}
}
