package release

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
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
		name          string
		ref           string
		wantTag       string
		wantVersion   string
		wantChannel   string
		wantLatest    bool
		wantImmutable bool
		wantError     bool
	}{
		{name: "main rolling", ref: "refs/heads/main", wantTag: "rolling", wantVersion: "0.0.0-main.1710000000+gaaaaaaaaaaaa", wantChannel: "rolling", wantLatest: true},
		{name: "stable", ref: "refs/tags/v1.2.3", wantTag: "v1.2.3", wantVersion: "v1.2.3", wantChannel: "stable", wantImmutable: true},
		{name: "prerelease", ref: "refs/tags/v1.2.3-rc.1", wantTag: "v1.2.3-rc.1", wantVersion: "v1.2.3-rc.1", wantChannel: "prerelease", wantImmutable: true},
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
			if plan.Tag != test.wantTag || plan.Version != test.wantVersion || plan.Channel != test.wantChannel || plan.Latest != test.wantLatest || plan.Immutable != test.wantImmutable {
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
	for _, notes := range []string{
		"immutable stable semantic-version release", testRevision,
		"https://github.com/higress-group/issue-spec/releases/download/v1.2.3/install.sh", "./install.sh --tag v1.2.3",
		"https://github.com/higress-group/issue-spec/releases/download/v1.2.3/install.ps1", ".\\install.ps1 -Tag v1.2.3",
		"curl.exe -fL --output install.ps1", "https://github.com/higress-group/issue-spec/releases/download/v1.2.3/issue-spec-requirements.zip",
		`"$HOME/.local/bin/issue-spec" version --json`, `Join-Path $env:LOCALAPPDATA "issue-spec\bin\issue-spec.exe"`,
		"requirements setup --server https://issue-spec.example.com", "do not pipe it into a shell",
	} {
		data, err := os.ReadFile(filepath.Join(first, "release-notes.md"))
		if err != nil || !bytes.Contains(data, []byte(notes)) {
			t.Fatalf("release notes missing %q: %v\n%s", notes, err, data)
		}
	}
	stableNotes := releaseNotes(plan)
	for _, forbidden := range []string{"gh attestation", "gh release", "Invoke-WebRequest", "--repo owner/repository", "--agent codex"} {
		if strings.Contains(stableNotes, forbidden) {
			t.Errorf("stable release notes unexpectedly contain %q", forbidden)
		}
	}
	rolling, err := PlanPublication("refs/heads/main", testRevision, 1710000000)
	if err != nil {
		t.Fatal(err)
	}
	rollingNotes := releaseNotes(rolling)
	for _, required := range []string{
		"mutable rolling release from main",
		"https://github.com/higress-group/issue-spec/releases/latest/download/install.sh", "./install.sh --latest",
		"https://github.com/higress-group/issue-spec/releases/latest/download/install.ps1", ".\\install.ps1 -Latest",
		"https://github.com/higress-group/issue-spec/releases/latest/download/issue-spec-requirements.zip",
	} {
		if !strings.Contains(rollingNotes, required) {
			t.Errorf("rolling release notes missing %q", required)
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

func TestShellInstallerDownloadsTagAndLatestAndPreservesExistingBinaryOnFailure(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh unavailable")
	}
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl unavailable")
	}
	root := t.TempDir()
	plan, _ := PlanPublication("refs/tags/v1.2.3", testRevision, 1710000000)
	fakeCLI := "#!/bin/sh\nif [ \"$1\" = version ] && [ \"$2\" = --json ]; then echo '{\"version\": \"v1.2.3\", \"revision\": \"" + testRevision + "\"}'; exit 0; fi\nexit 1\n"
	if err := assemble(root, plan, fakeBinaries(fakeCLI)); err != nil {
		t.Fatal(err)
	}
	publish := filepath.Join(root, "publish")
	installer := filepath.Join(publish, "install.sh")
	archiveName := "issue-spec_linux_amd64.tar.gz"

	runRemote := func(t *testing.T, mode, expectedPrefix string, corrupt, missing bool) ([]byte, error) {
		t.Helper()
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			name := filepath.Base(request.URL.Path)
			if !strings.HasPrefix(request.URL.Path, expectedPrefix) || (missing && name == archiveName) {
				http.NotFound(writer, request)
				return
			}
			if corrupt && name == archiveName {
				_, _ = writer.Write([]byte("corrupt archive"))
				return
			}
			data, err := os.ReadFile(filepath.Join(publish, name))
			if err != nil {
				http.NotFound(writer, request)
				return
			}
			_, _ = writer.Write(data)
		}))
		defer server.Close()
		installDir := filepath.Join(root, "remote-"+strings.NewReplacer("-", "", ".", "").Replace(mode))
		command := exec.Command("sh", installer, mode, "v1.2.3", "--base-url", server.URL+"/releases", "--install-dir", installDir, "--os", "linux", "--arch", "amd64")
		if mode == "--latest" {
			command = exec.Command("sh", installer, mode, "--base-url", server.URL+"/releases", "--install-dir", installDir, "--os", "linux", "--arch", "amd64")
		}
		output, err := command.CombinedOutput()
		return append([]byte(installDir+"\n"), output...), err
	}

	for _, test := range []struct {
		name   string
		mode   string
		prefix string
	}{
		{name: "immutable semantic tag", mode: "--tag", prefix: "/releases/download/v1.2.3/"},
		{name: "rolling latest", mode: "--latest", prefix: "/releases/latest/download/"},
	} {
		t.Run(test.name, func(t *testing.T) {
			output, err := runRemote(t, test.mode, test.prefix, false, false)
			if err != nil {
				t.Fatalf("remote install: %v\n%s", err, output)
			}
			parts := bytes.SplitN(output, []byte("\n"), 2)
			if _, err := os.Stat(filepath.Join(string(parts[0]), "issue-spec")); err != nil {
				t.Fatalf("installed binary: %v", err)
			}
		})
	}

	for _, test := range []struct {
		name    string
		corrupt bool
		missing bool
	}{
		{name: "corrupt archive", corrupt: true},
		{name: "404 archive", missing: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			installDir := filepath.Join(root, "remote-tag")
			installed := filepath.Join(installDir, "issue-spec")
			if err := os.MkdirAll(installDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(installed, []byte("keep existing\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			output, err := runRemote(t, "--tag", "/releases/download/v1.2.3/", test.corrupt, test.missing)
			if err == nil {
				t.Fatalf("failed remote install unexpectedly succeeded:\n%s", output)
			}
			if got, readErr := os.ReadFile(installed); readErr != nil || string(got) != "keep existing\n" {
				t.Fatalf("existing binary changed: %q, %v\n%s", got, readErr, output)
			}
		})
	}
}

func TestPowerShellInstallerHasEquivalentIntegrityAndAtomicGuards(t *testing.T) {
	data, err := installerAssets.ReadFile("assets/install.ps1")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"ConvertFrom-Json", "Get-FileHash", "manifest.json", "SHA256SUMS", "[System.IO.File]::Replace($stagedBinary, $destination, $backupBinary", "version --json", "finally", "$env:LOCALAPPDATA", "issue-spec\\bin", "/download/$Tag", "/latest/download", "curl.exe"} {
		if !bytes.Contains(data, []byte(required)) {
			t.Errorf("PowerShell installer missing %q", required)
		}
	}
	if bytes.Contains(data, []byte("[System.IO.File]::Replace($stagedBinary, $destination, $null")) {
		t.Error("PowerShell installer must not pass a null backup path to File.Replace")
	}
	if bytes.Contains(data, []byte("Invoke-WebRequest")) {
		t.Error("PowerShell installer must use curl.exe for release downloads")
	}
	if powerShell, err := exec.LookPath("pwsh"); err == nil {
		path, pathErr := filepath.Abs(filepath.Join("assets", "install.ps1"))
		if pathErr != nil {
			t.Fatal(pathErr)
		}
		command := exec.Command(powerShell, "-NoProfile", "-NonInteractive", "-Command", "$null = [scriptblock]::Create((Get-Content -LiteralPath '"+strings.ReplaceAll(path, "'", "''")+"' -Raw))")
		if output, parseErr := command.CombinedOutput(); parseErr != nil {
			t.Fatalf("PowerShell installer does not parse: %v\n%s", parseErr, output)
		}
	}
}

func TestShellInstallerUsesCurlWithoutDownloaderFallback(t *testing.T) {
	data, err := installerAssets.ReadFile("assets/install.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"command -v curl", "curl -fL", "manifest.json", "SHA256SUMS"} {
		if !bytes.Contains(data, []byte(required)) {
			t.Errorf("Shell installer missing %q", required)
		}
	}
	if bytes.Contains(data, []byte("wget")) {
		t.Error("Shell installer must not fall back to wget")
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
		"actions/attest-build-provenance@v4", "gh attestation verify",
		`if [ "$RELEASE_CHANNEL" = rolling ]; then`, `if [ "$current_main" != "$RELEASE_REVISION" ]; then`,
		`git/ref/tags/$RELEASE_TAG`, `git/refs/tags/$RELEASE_TAG`, `-F force=true`, `-f ref="refs/tags/$RELEASE_TAG"`,
		`gh release upload "$RELEASE_TAG" dist/release/publish/* --clobber`,
		`gh release edit "$RELEASE_TAG" --title "latest"`, `--title "issue-spec $RELEASE_VERSION"`,
		`gh release create "$RELEASE_TAG" --draft --verify-tag`,
		"gh release edit \"$RELEASE_TAG\" --draft=false --latest=false",
		`binary="$HOME/.local/bin/issue-spec"`, `"$binary" requirements setup --help`,
		`Join-Path $env:LOCALAPPDATA "issue-spec\bin\issue-spec.exe"`, `& $binary requirements setup --help`,
	} {
		if !strings.Contains(body, required) {
			t.Errorf("release workflow missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"pull_request:", "workflow_dispatch:", "hack/release/rolling-latest.sh", "rolling_latest_decision",
		"published_rolling_count", "read_latest_rolling_revision", "releases/latest", "rolling-", ".target_commitish",
	} {
		if strings.Contains(body, forbidden) {
			t.Errorf("release workflow unexpectedly contains %q", forbidden)
		}
	}

	rollingStart := strings.Index(body, `if [ "$RELEASE_CHANNEL" = rolling ]; then`)
	semanticStart := strings.Index(body, `test "$GITHUB_REF" = "refs/tags/$RELEASE_TAG"`)
	if rollingStart < 0 || semanticStart < rollingStart {
		t.Fatalf("rolling and semantic publication paths are not separated")
	}
	rollingBody := body[rollingStart:semanticStart]
	moveTag := strings.Index(rollingBody, `git/refs/tags/$RELEASE_TAG`)
	createRolling := strings.Index(rollingBody, `gh release create "$RELEASE_TAG" --verify-tag`)
	uploadRolling := strings.Index(rollingBody, `gh release upload "$RELEASE_TAG"`)
	verifyRolling := strings.LastIndex(rollingBody, "verify_remote_assets")
	publishRolling := strings.Index(rollingBody, `gh release edit "$RELEASE_TAG" --title "latest"`)
	guardRolling := strings.LastIndex(rollingBody, `guard_tag_revision "$RELEASE_TAG" "$RELEASE_REVISION"`)
	if moveTag < 0 || createRolling < moveTag || uploadRolling < createRolling || verifyRolling < uploadRolling || publishRolling < verifyRolling || guardRolling < publishRolling || strings.Contains(rollingBody, `--title "issue-spec $RELEASE_VERSION"`) {
		t.Fatalf("rolling update order is unsafe")
	}

	semanticBody := body[semanticStart:]
	createSemantic := strings.Index(semanticBody, `gh release create "$RELEASE_TAG" --draft --verify-tag`)
	uploadSemantic := strings.Index(semanticBody, `gh release upload "$RELEASE_TAG"`)
	verifySemantic := strings.LastIndex(semanticBody, "verify_remote_assets")
	publishSemantic := strings.Index(semanticBody, `gh release edit "$RELEASE_TAG" --draft=false --latest=false`)
	if createSemantic < 0 || uploadSemantic < createSemantic || verifySemantic < uploadSemantic || publishSemantic < verifySemantic || !strings.Contains(semanticBody, `--title "issue-spec $RELEASE_VERSION"`) || strings.Contains(semanticBody, `--title "latest"`) {
		t.Fatalf("semantic draft/upload/publish order is unsafe")
	}
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
