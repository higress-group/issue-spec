package release

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const testRevision = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestPlanPublicationGuardsTrustedRefs(t *testing.T) {
	tests := []struct {
		name        string
		ref         string
		wantTag     string
		wantVersion string
		wantChannel string
		wantLatest  bool
		wantError   bool
	}{
		{name: "main rolling", ref: "refs/heads/main", wantTag: "rolling-" + testRevision, wantVersion: "0.0.0-main.1710000000+gaaaaaaaaaaaa", wantChannel: "rolling", wantLatest: true},
		{name: "stable", ref: "refs/tags/v1.2.3", wantTag: "v1.2.3", wantVersion: "v1.2.3", wantChannel: "stable"},
		{name: "prerelease", ref: "refs/tags/v1.2.3-rc.1", wantTag: "v1.2.3-rc.1", wantVersion: "v1.2.3-rc.1", wantChannel: "prerelease"},
		{name: "pull request", ref: "refs/pull/9/merge", wantError: true},
		{name: "other branch", ref: "refs/heads/feature", wantError: true},
		{name: "unsupported tag", ref: "refs/tags/latest", wantError: true},
		{name: "loose semver", ref: "refs/tags/v1.2", wantError: true},
		{name: "numeric prerelease leading zero", ref: "refs/tags/v1.2.3-01", wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan, err := PlanPublication(test.ref, testRevision, 1710000000)
			if test.wantError {
				if err == nil {
					t.Fatalf("PlanPublication(%q) = %+v", test.ref, plan)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if plan.Tag != test.wantTag || plan.Version != test.wantVersion || plan.Channel != test.wantChannel || plan.Latest != test.wantLatest || !plan.Immutable {
				t.Fatalf("plan = %+v", plan)
			}
			if plan.Channel == "rolling" && plan.Prerelease {
				t.Fatal("rolling latest must remain eligible for GitHub's latest pointer")
			}
		})
	}
	if _, err := PlanPublication("refs/heads/main", "abc123", 1710000000); err == nil {
		t.Fatal("abbreviated revision accepted")
	}
	if _, err := PlanPublication("refs/heads/main", strings.ToUpper(testRevision), 1710000000); err == nil {
		t.Fatal("uppercase revision accepted")
	}
	if _, err := PlanPublication("refs/heads/main", testRevision, 1); err == nil {
		t.Fatal("invalid source epoch accepted")
	}
}

func TestReleaseOutputCannotReplaceRepositoryOrAncestor(t *testing.T) {
	root := filepath.Join(t.TempDir(), "repository")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, unsafe := range []string{root, filepath.Dir(root)} {
		if err := validateOutputLocation(root, unsafe); err == nil {
			t.Fatalf("unsafe output %s accepted", unsafe)
		}
	}
	if err := validateOutputLocation(root, filepath.Join(root, "dist", "release")); err != nil {
		t.Fatalf("bounded repository output rejected: %v", err)
	}
	if err := validateOutputLocation(root, filepath.Join(t.TempDir(), "release")); err != nil {
		t.Fatalf("bounded external output rejected: %v", err)
	}
}

func TestAssemblyIsReproducibleAndComplete(t *testing.T) {
	plan, err := PlanPublication("refs/tags/v1.2.3", testRevision, 1710000000)
	if err != nil {
		t.Fatal(err)
	}
	binaries := fakeBinaries("deterministic binary\n")
	first, second := t.TempDir(), t.TempDir()
	if err := assemble(first, plan, binaries); err != nil {
		t.Fatal(err)
	}
	if err := assemble(second, plan, binaries); err != nil {
		t.Fatal(err)
	}
	firstManifest, err := VerifyDirectory(first)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyDirectory(second); err != nil {
		t.Fatal(err)
	}
	if firstManifest.Version != "v1.2.3" || firstManifest.Revision != testRevision || firstManifest.Channel != "stable" || firstManifest.CLICompatibility != "v1.2.3" {
		t.Fatalf("manifest identity = %+v", firstManifest)
	}
	if !strings.HasPrefix(firstManifest.RequirementsSkillContentID, "sha256:") || len(firstManifest.Assets) != 9 || len(firstManifest.PublishedAssets) != 11 {
		t.Fatalf("manifest completeness = %+v", firstManifest)
	}
	if diff := compareTrees(t, first, second); diff != "" {
		t.Fatalf("same revision produced different bytes: %s", diff)
	}
	for _, notes := range []string{"immutable stable semantic-version release", testRevision, "./install.sh --asset-dir .", "./install.ps1 -AssetDir .", "issue-spec version --json", "requirements setup", "gh attestation verify"} {
		data, err := os.ReadFile(filepath.Join(first, "release-notes.md"))
		if err != nil || !bytes.Contains(data, []byte(notes)) {
			t.Fatalf("release notes missing %q: %v\n%s", notes, err, data)
		}
	}
}

func TestVerificationRejectsCorruptMissingAndExtraAssets(t *testing.T) {
	plan, _ := PlanPublication("refs/heads/main", testRevision, 1710000000)
	for _, test := range []struct {
		name   string
		mutate func(t *testing.T, root string)
	}{
		{name: "corrupt", mutate: func(t *testing.T, root string) {
			path := filepath.Join(root, "publish", "issue-spec_linux_amd64.tar.gz")
			if err := os.WriteFile(path, []byte("corrupt"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "missing", mutate: func(t *testing.T, root string) {
			if err := os.Remove(filepath.Join(root, "publish", "install.ps1")); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "extra", mutate: func(t *testing.T, root string) {
			if err := os.WriteFile(filepath.Join(root, "publish", "unexpected"), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if err := assemble(root, plan, fakeBinaries("binary\n")); err != nil {
				t.Fatal(err)
			}
			test.mutate(t, root)
			if _, err := VerifyDirectory(root); err == nil {
				t.Fatal("modified release unexpectedly verified")
			}
		})
	}
}

func TestPackageUsesOneSixTargetBuildPathAndRerunsDeterministically(t *testing.T) {
	root := t.TempDir()
	logPath := filepath.Join(root, "builds.log")
	fakeGo := filepath.Join(root, "fake-go")
	script := `#!/bin/sh
set -eu
output=
while [ "$#" -gt 0 ]; do
  if [ "$1" = "-o" ]; then output=$2; shift 2; continue; fi
  shift
done
printf '%s\n' "$GOOS/$GOARCH" >> "$FAKE_GO_LOG"
printf 'binary for %s/%s\n' "$GOOS" "$GOARCH" > "$output"
`
	if err := os.WriteFile(fakeGo, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_GO_LOG", logPath)
	first := filepath.Join(root, "first")
	second := filepath.Join(root, "second")
	options := Options{Root: root, Output: first, Ref: "refs/heads/main", Revision: testRevision, SourceDateEpoch: 1710000000}
	if _, err := packageWithGo(context.Background(), options, fakeGo); err != nil {
		t.Fatal(err)
	}
	options.Output = second
	if _, err := packageWithGo(context.Background(), options, fakeGo); err != nil {
		t.Fatal(err)
	}
	if diff := compareTrees(t, first, second); diff != "" {
		t.Fatalf("packager rerun drift: %s", diff)
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, line := range strings.Fields(string(log)) {
		counts[line]++
	}
	for _, target := range targets {
		if counts[target.key()] != 2 {
			t.Errorf("target %s built %d times, want 2", target.key(), counts[target.key()])
		}
	}
}

func TestBuildFailurePreservesPreviousReleaseDirectory(t *testing.T) {
	root := t.TempDir()
	fakeGo := filepath.Join(root, "failing-go")
	script := `#!/bin/sh
set -eu
if [ "$GOOS/$GOARCH" = "windows/arm64" ]; then exit 9; fi
output=
while [ "$#" -gt 0 ]; do
  if [ "$1" = "-o" ]; then output=$2; shift 2; continue; fi
  shift
done
printf 'binary\n' > "$output"
`
	if err := os.WriteFile(fakeGo, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "release")
	if err := os.MkdirAll(output, 0o755); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(output, "previous-complete-release")
	if err := os.WriteFile(sentinel, []byte("preserve\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	options := Options{Root: root, Output: output, Ref: "refs/heads/main", Revision: testRevision, SourceDateEpoch: 1710000000}
	if _, err := packageWithGo(context.Background(), options, fakeGo); err == nil {
		t.Fatal("failed target unexpectedly produced a release")
	}
	if got, err := os.ReadFile(sentinel); err != nil || string(got) != "preserve\n" {
		t.Fatalf("previous release changed after failed build: %q, %v", got, err)
	}
}

func TestShellInstallerIsIdempotentAndPreservesExistingBinaryOnCorruption(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh unavailable")
	}
	root := t.TempDir()
	plan, _ := PlanPublication("refs/tags/v1.2.3", testRevision, 1710000000)
	fakeCLI := "#!/bin/sh\nif [ \"$1\" = version ] && [ \"$2\" = --json ]; then echo '{\"version\": \"v1.2.3\", \"revision\": \"" + testRevision + "\"}'; exit 0; fi\nexit 1\n"
	if err := assemble(root, plan, fakeBinaries(fakeCLI)); err != nil {
		t.Fatal(err)
	}
	publish := filepath.Join(root, "publish")
	installDir := filepath.Join(root, "bin")
	runInstaller := func() ([]byte, error) {
		command := exec.Command("sh", filepath.Join(publish, "install.sh"), "--asset-dir", publish, "--install-dir", installDir, "--os", "linux", "--arch", "amd64")
		return command.CombinedOutput()
	}
	for run := 0; run < 2; run++ {
		if output, err := runInstaller(); err != nil {
			t.Fatalf("installer run %d: %v\n%s", run+1, err, output)
		}
	}
	installed := filepath.Join(installDir, "issue-spec")
	if output, err := exec.Command(installed, "version", "--json").CombinedOutput(); err != nil || !bytes.Contains(output, []byte(`"version": "v1.2.3"`)) {
		t.Fatalf("installed CLI: %v\n%s", err, output)
	}
	if err := os.WriteFile(installed, []byte("keep existing\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(publish, "issue-spec_linux_amd64.tar.gz")
	if err := os.WriteFile(archive, []byte("modified"), 0o644); err != nil {
		t.Fatal(err)
	}
	if output, err := runInstaller(); err == nil || !bytes.Contains(output, []byte("integrity verification failed")) {
		t.Fatalf("modified archive result: %v\n%s", err, output)
	}
	if got, err := os.ReadFile(installed); err != nil || string(got) != "keep existing\n" {
		t.Fatalf("existing binary changed after failed verification: %q, %v", got, err)
	}
}

func TestPowerShellInstallerHasEquivalentIntegrityAndAtomicGuards(t *testing.T) {
	data, err := installerAssets.ReadFile("assets/install.ps1")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"ConvertFrom-Json", "Get-FileHash", "manifest.json", "SHA256SUMS", "[System.IO.File]::Replace", "version --json", "finally"} {
		if !bytes.Contains(data, []byte(required)) {
			t.Errorf("PowerShell installer missing %q", required)
		}
	}
}

func TestRollingLatestDecision(t *testing.T) {
	repository := t.TempDir()
	runGit(t, repository, "init", "--quiet")
	runGit(t, repository, "config", "user.name", "Release Test")
	runGit(t, repository, "config", "user.email", "release-test@example.com")
	commit := func(content string) string {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repository, "state"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		runGit(t, repository, "add", "state")
		runGit(t, repository, "commit", "--quiet", "--no-gpg-sign", "-m", content)
		return strings.TrimSpace(runGit(t, repository, "rev-parse", "HEAD"))
	}
	first := commit("first")
	second := commit("second")
	runGit(t, repository, "checkout", "--quiet", "--orphan", "diverged")
	if err := os.Remove(filepath.Join(repository, "state")); err != nil {
		t.Fatal(err)
	}
	runGit(t, repository, "rm", "--quiet", "--cached", "state")
	diverged := commit("diverged")

	script, err := filepath.Abs("rolling-latest.sh")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name      string
		candidate string
		main      string
		latest    string
		want      string
		wantError bool
	}{
		{name: "first rolling snapshot", candidate: first, main: first, latest: "-", want: "advance"},
		{name: "second rolling snapshot", candidate: second, main: second, latest: first, want: "advance"},
		{name: "already current", candidate: second, main: second, latest: second, want: "current"},
		{name: "older completion", candidate: first, main: second, latest: second, want: "noop"},
		{name: "older than main and newer than latest", candidate: first, main: second, latest: "-", want: "noop"},
		{name: "diverged latest", candidate: second, main: second, latest: diverged, wantError: true},
		{name: "candidate not on main", candidate: diverged, main: second, latest: first, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			command := exec.Command("sh", script, test.candidate, test.main, test.latest)
			command.Dir = repository
			output, err := command.CombinedOutput()
			if test.wantError {
				if err == nil {
					t.Fatalf("decision unexpectedly succeeded: %s", output)
				}
				return
			}
			if err != nil {
				t.Fatalf("decision failed: %v\n%s", err, output)
			}
			if got := strings.TrimSpace(string(output)); got != test.want {
				t.Fatalf("decision = %q, want %q", got, test.want)
			}
		})
	}
}

func TestWorkflowKeepsPublicationAuthorityBehindCompleteTrustedBuild(t *testing.T) {
	workflow, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(workflow)
	var parsed any
	if err := yaml.Unmarshal(workflow, &parsed); err != nil {
		t.Fatalf("release workflow is invalid YAML: %v", err)
	}
	for _, required := range []string{
		"branches: [main]", "tags: ['v*']", "contents: read", "contents: write", "id-token: write", "attestations: write",
		"cancel-in-progress: false", "go test ./...", "diff -r dist/release dist/release-repeat",
		"actions/attest-build-provenance@v4", "gh attestation verify", "hack/release/rolling-latest.sh",
		"repos/$GITHUB_REPOSITORY/releases/latest", `select(.draft == false and (.tag_name | startswith("rolling-")))`,
		`if [ "$rolling_count" = 0 ]; then`, "gh release create", "--draft",
		"gh release upload", "gh release edit \"$RELEASE_TAG\" --draft=false --latest \\",
		"gh release edit \"$RELEASE_TAG\" --draft=false --latest=false",
	} {
		if !strings.Contains(body, required) {
			t.Errorf("release workflow missing %q", required)
		}
	}
	for _, forbidden := range []string{"pull_request:", "workflow_dispatch:"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("release workflow unexpectedly grants a trigger to %q", forbidden)
		}
	}
	create := strings.Index(body, "gh release create")
	upload := strings.Index(body, "gh release upload")
	verify := strings.LastIndex(body, "verify_remote_assets")
	decision := strings.LastIndex(body, "rolling_latest_decision")
	publish := strings.LastIndex(body, "gh release edit")
	if create < 0 || upload < create || verify < upload || decision < verify || publish < decision {
		t.Fatalf("draft/upload/publish order is unsafe: create=%d upload=%d publish=%d", create, upload, publish)
	}
	semanticBranch := strings.Index(body, `if [ "$RELEASE_CHANNEL" != rolling ]; then`)
	semanticPublish := strings.Index(body, `gh release edit "$RELEASE_TAG" --draft=false --latest=false`)
	rollingPublish := strings.Index(body, `gh release edit "$RELEASE_TAG" --draft=false --latest \`)
	if semanticBranch < verify || semanticPublish < semanticBranch || decision < semanticPublish || rollingPublish < decision {
		t.Fatalf("semantic and rolling latest-pointer paths are not separated")
	}
}

func runGit(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = directory
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
	}
	return string(output)
}

func fakeBinaries(content string) map[string][]byte {
	result := make(map[string][]byte, len(targets))
	for _, target := range targets {
		result[target.key()] = []byte(content)
	}
	return result
}

func compareTrees(t *testing.T, left, right string) string {
	t.Helper()
	leftFiles := treeHashes(t, left)
	rightFiles := treeHashes(t, right)
	if len(leftFiles) != len(rightFiles) {
		return "file count differs"
	}
	for name, hash := range leftFiles {
		if rightFiles[name] != hash {
			return name
		}
	}
	return ""
}

func treeHashes(t *testing.T, root string) map[string]string {
	t.Helper()
	result := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		hash := sha256.Sum256(data)
		result[filepath.ToSlash(relative)] = hex.EncodeToString(hash[:])
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
