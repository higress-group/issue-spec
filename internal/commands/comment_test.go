package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/model"
)

func newFakeBackend(configure func(*fakeGitHubBackend)) func(context.Context, auth.GitHubBackendSelection) (github.Backend, error) {
	return func(_ context.Context, selection auth.GitHubBackendSelection) (github.Backend, error) {
		f := &fakeGitHubBackend{info: github.BackendInfo{Name: selection.Name, Kind: selection.Kind, Host: selection.Host}}
		if configure != nil {
			configure(f)
		}
		return *f, nil
	}
}

const specInputJSON = `{
  "requirement": {
    "title": "canonical SPEC comments",
    "text": "The CLI MUST render canonical SPEC Markdown from structured fields."
  },
  "scenarios": [
    {
      "title": "structured fields render a canonical SPEC body",
      "when": "a caller provides requirement and scenario fields",
      "then": "the CLI renders a body accepted by comment upsert"
    }
  ]
}`

func writeTempInput(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "input.json")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCommentGenerateSpecProducesUpsertReadyBody(t *testing.T) {
	inPath := writeTempInput(t, specInputJSON)
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	code := app.runCommentGenerate(context.Background(), []string{
		"--type", "SPEC", "--id", "SPEC-001", "--status", "confirmed", "--scope", "canonical SPEC generation", "--input-file", inPath,
	})
	if code != 0 {
		t.Fatalf("generate exit=%d stderr=%q", code, errOut.String())
	}
	body := out.String()
	// The generated body must be accepted by upsert preparation and canonical
	// validation without manual edits.
	prepared, err := model.EnsureTypedBody("SPEC", "SPEC-001", body, model.BodyOptions{Status: "confirmed"})
	if err != nil {
		t.Fatalf("generated body rejected by EnsureTypedBody: %v", err)
	}
	if diags := model.ValidateCanonicalBody("SPEC", "SPEC-001", "", prepared); len(diags) != 0 {
		t.Fatalf("generated body not canonical: %+v", diags)
	}
}

func TestCommentGenerateRejectsUnknownJSONFields(t *testing.T) {
	inPath := writeTempInput(t, `{"requirement":{"title":"t","text":"The CLI MUST x."},"scenarios":[{"title":"s","when":"w","then":"z"}],"bogus":true}`)
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	code := app.runCommentGenerate(context.Background(), []string{"--type", "SPEC", "--id", "SPEC-001", "--input-file", inPath})
	if code == 0 {
		t.Fatalf("expected unknown field to fail, stdout=%q", out.String())
	}
}

func TestCommentUpsertRejectsMalformedSpecByDefault(t *testing.T) {
	bodyPath := writeTempInput(t, "# SPEC-001\n\nThis is a hand-written non-canonical spec.")
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	// No backend override: validation must reject before any client call.
	code := app.runCommentUpsert(context.Background(), []string{
		"--repo", "o/r", "--issue", "5", "--type", "SPEC", "--id", "SPEC-001", "--body-file", bodyPath, "--status", "confirmed",
	})
	if code != 2 {
		t.Fatalf("expected exit 2 for malformed SPEC, got %d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	if !strings.Contains(errOut.String(), "--allow-noncanonical") {
		t.Fatalf("rejection should mention the escape hatch:\n%s", errOut.String())
	}
	if !strings.Contains(errOut.String(), "requirement-heading") {
		t.Fatalf("rejection should name missing elements:\n%s", errOut.String())
	}
}

func TestCommentUpsertAcceptsCanonicalSpec(t *testing.T) {
	inPath := writeTempInput(t, specInputJSON)
	var genOut, errOut bytes.Buffer
	gen := newApp(strings.NewReader(""), &genOut, &errOut)
	if code := gen.runCommentGenerate(context.Background(), []string{"--type", "SPEC", "--id", "SPEC-001", "--status", "confirmed", "--input-file", inPath}); code != 0 {
		t.Fatalf("generate failed: %s", errOut.String())
	}
	bodyPath := writeTempInput(t, genOut.String())

	var created string
	var out bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &bytes.Buffer{})
	app.selectGitHubBackend = ghSelection
	app.newGitHubBackend = newFakeBackend(func(f *fakeGitHubBackend) {
		f.listIssueComments = func(context.Context, string, int) ([]github.Comment, error) { return nil, nil }
		f.createComment = func(_ context.Context, _ string, _ int, body string) (github.Comment, error) {
			created = body
			return github.Comment{ID: 1, HTMLURL: "https://github.com/o/r/issues/5#issuecomment-1"}, nil
		}
	})
	code := app.runCommentUpsert(context.Background(), []string{"--repo", "o/r", "--issue", "5", "--type", "SPEC", "--id", "SPEC-001", "--body-file", bodyPath, "--json"})
	if code != 0 {
		t.Fatalf("canonical upsert failed exit=%d out=%q", code, out.String())
	}
	if !strings.Contains(created, "## Requirement:") {
		t.Fatalf("created body not canonical:\n%s", created)
	}
	if strings.Contains(out.String(), "noncanonical") {
		t.Fatalf("canonical upsert should not report noncanonical: %s", out.String())
	}
}

func TestCommentUpsertAllowNoncanonicalWritesButMarksState(t *testing.T) {
	bodyPath := writeTempInput(t, "# SPEC-001\n\nLegacy non-canonical spec.")
	var created string
	var out bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &bytes.Buffer{})
	app.selectGitHubBackend = ghSelection
	app.newGitHubBackend = newFakeBackend(func(f *fakeGitHubBackend) {
		f.listIssueComments = func(context.Context, string, int) ([]github.Comment, error) { return nil, nil }
		f.createComment = func(_ context.Context, _ string, _ int, body string) (github.Comment, error) {
			created = body
			return github.Comment{ID: 2, HTMLURL: "https://github.com/o/r/issues/5#issuecomment-2"}, nil
		}
	})
	code := app.runCommentUpsert(context.Background(), []string{"--repo", "o/r", "--issue", "5", "--type", "SPEC", "--id", "SPEC-001", "--body-file", bodyPath, "--allow-noncanonical", "--json"})
	if code != 0 {
		t.Fatalf("allow-noncanonical upsert failed exit=%d out=%q", code, out.String())
	}
	if created == "" {
		t.Fatal("expected comment to be written under bypass")
	}
	var got struct {
		Noncanonical bool `json:"noncanonical"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Noncanonical {
		t.Fatalf("noncanonical bypass must be marked in output: %s", out.String())
	}
	// The written body remains detectable as malformed via the shared validator.
	if diags := model.ValidateCanonicalBody("SPEC", "SPEC-001", "", created); len(diags) == 0 {
		t.Fatal("bypassed body should still be detectably noncanonical")
	}
}

func TestCommentListReportsCanonicalDiagnosticsForMalformedExistingSpec(t *testing.T) {
	// Migration case: a malformed existing SPEC comment (marker present, body
	// non-canonical) must remain listed and be flagged.
	malformed, err := model.EnsureTypedBody("SPEC", "SPEC-001", "# SPEC-001\n\nlegacy body", model.BodyOptions{Status: "confirmed"})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &bytes.Buffer{})
	app.selectGitHubBackend = ghSelection
	app.newGitHubBackend = newFakeBackend(func(f *fakeGitHubBackend) {
		f.listIssueComments = func(context.Context, string, int) ([]github.Comment, error) {
			return []github.Comment{{ID: 3, HTMLURL: "https://github.com/o/r/issues/5#issuecomment-3", Body: malformed}}, nil
		}
	})
	code := app.runCommentList(context.Background(), []string{"--repo", "o/r", "--issue", "5", "--json"})
	if code != 0 {
		t.Fatalf("list failed exit=%d out=%q", code, out.String())
	}
	var got struct {
		Comments []model.Artifact `json:"comments"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Comments) != 1 {
		t.Fatalf("malformed existing SPEC should remain listed: %+v", got.Comments)
	}
	if len(got.Comments[0].Canonical) == 0 {
		t.Fatalf("list must flag noncanonical diagnostics: %+v", got.Comments[0])
	}
}

func TestCommentListKeepsLegacyTypedLookingCommentsInspectable(t *testing.T) {
	// A legacy typed-looking comment (no marker, but Type/ID/Status header) must
	// still be inspectable during migration.
	legacy := "Type: SPEC\nID: SPEC-001\nStatus: confirmed\n\n# SPEC-001\n\nlegacy shape"
	if !model.IsLikelyTyped(legacy) {
		t.Fatal("precondition: legacy comment should be recognized as typed-looking")
	}
	var out bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &bytes.Buffer{})
	app.selectGitHubBackend = ghSelection
	app.newGitHubBackend = newFakeBackend(func(f *fakeGitHubBackend) {
		f.listIssueComments = func(context.Context, string, int) ([]github.Comment, error) {
			return []github.Comment{{ID: 4, HTMLURL: "https://github.com/o/r/issues/5#issuecomment-4", Body: legacy}}, nil
		}
	})
	code := app.runCommentList(context.Background(), []string{"--repo", "o/r", "--issue", "5", "--json"})
	if code != 0 {
		t.Fatalf("list failed exit=%d out=%q", code, out.String())
	}
	var got struct {
		Comments []model.Artifact `json:"comments"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Comments) != 1 {
		t.Fatalf("legacy typed-looking comment must remain listed: %+v", got.Comments)
	}
}
