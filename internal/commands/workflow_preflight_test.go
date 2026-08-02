package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/higress-group/issue-spec/internal/buildinfo"
	"github.com/higress-group/issue-spec/internal/codereview"
)

func TestWorkflowPreflightAcceptsOneImmutableSetWithoutWrites(t *testing.T) {
	withMergeWorkflow(t)
	if _, err := writeWorkflowArtifacts(".", "o/r", "codex", "skills"); err != nil {
		t.Fatal(err)
	}
	manifest, err := readWorkflowReleaseManifest(".", "")
	if err != nil {
		t.Fatal(err)
	}
	backend := &mergeCommandBackend{issueState: "open"}
	provider := newMergeCommandProvider(codereview.CheckSuccess, false)
	app, out, errOut := mergeCommandApp(t, backend, provider)
	release := buildinfo.Current().Version
	code := app.runWorkflowPreflight(t.Context(), []string{
		"--repo", "o/r", "--release-set", release, "--server-release", release, "--runner-release", release,
		"--generated-digest", manifest.ContentDigest,
		"--provider-build", "bridge@sha256:1234",
		"--canonical-principals", "people-directory@sha256:0123456789abcdef", "--json",
	})
	if code != 0 || errOut.Len() != 0 || !strings.Contains(out.String(), `"ok": true`) {
		t.Fatalf("preflight exit=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	if backend.issueWrites != 0 || backend.prWrites != 0 || provider.snapshots != 0 || provider.merges != 0 {
		t.Fatalf("read-only preflight crossed mutation/authority collection boundary: issue=%d pr=%d snapshots=%d merges=%d",
			backend.issueWrites, backend.prWrites, provider.snapshots, provider.merges)
	}
}

func TestWorkflowPreflightFailsMixedReleaseAndTamperedAssetsWithoutWrites(t *testing.T) {
	for _, test := range []struct {
		name   string
		tamper bool
	}{
		{name: "mixed release"},
		{name: "tampered generated asset", tamper: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			withMergeWorkflow(t)
			if _, err := writeWorkflowArtifacts(".", "o/r", "codex", "skills"); err != nil {
				t.Fatal(err)
			}
			manifest, err := readWorkflowReleaseManifest(".", "")
			if err != nil {
				t.Fatal(err)
			}
			if test.tamper {
				path := filepath.Join(".agents", "skills", "issue-spec-workflow", "SKILL.md")
				body, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, append(body, []byte("\noperator drift\n")...), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			backend := &mergeCommandBackend{issueState: "open"}
			provider := newMergeCommandProvider(codereview.CheckSuccess, false)
			app, out, _ := mergeCommandApp(t, backend, provider)
			release := buildinfo.Current().Version
			serverRelease := "other-release"
			if test.tamper {
				serverRelease = release
			}
			code := app.runWorkflowPreflight(t.Context(), []string{
				"--repo", "o/r", "--release-set", release, "--server-release", serverRelease, "--runner-release", release,
				"--generated-digest", manifest.ContentDigest,
				"--provider-build", "bridge@sha256:1234",
				"--canonical-principals", "people-directory@sha256:0123456789abcdef", "--json",
			})
			if code != 1 || !strings.Contains(out.String(), `"ok": false`) {
				t.Fatalf("preflight exit=%d stdout=%q", code, out.String())
			}
			if backend.issueWrites != 0 || backend.prWrites != 0 || provider.snapshots != 0 || provider.merges != 0 {
				t.Fatalf("failed preflight wrote state: issue=%d pr=%d snapshots=%d merges=%d",
					backend.issueWrites, backend.prWrites, provider.snapshots, provider.merges)
			}
		})
	}
}
