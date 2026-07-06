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

func generateCanonicalSpecBody(t *testing.T) string {
	t.Helper()
	inPath := writeTempInput(t, specInputJSON)
	var genOut, errOut bytes.Buffer
	gen := newApp(strings.NewReader(""), &genOut, &errOut)
	if code := gen.runCommentGenerate(context.Background(), []string{"--type", "SPEC", "--id", "SPEC-001", "--status", "confirmed", "--input-file", inPath}); code != 0 {
		t.Fatalf("generate failed: %s", errOut.String())
	}
	return genOut.String()
}

func TestCommentUpsertUpdatePreservesRelatedLinks(t *testing.T) {
	// Reproduces the proposal #124 bug: a content-only regenerate must not drop the
	// Related Comments link a prior `issue-spec link` spliced onto the comment.
	peer := "https://github.com/o/r/issues/9#issuecomment-101"
	fresh := generateCanonicalSpecBody(t)
	existing, changed, err := model.AddRelatedCommentLink(fresh, peer)
	if err != nil || !changed {
		t.Fatalf("seed existing link: changed=%v err=%v", changed, err)
	}
	bodyPath := writeTempInput(t, fresh) // regenerated body carries Related Comments: N/A

	var updated string
	var out bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &bytes.Buffer{})
	app.selectGitHubBackend = ghSelection
	app.newGitHubBackend = newFakeBackend(func(f *fakeGitHubBackend) {
		f.listIssueComments = func(context.Context, string, int) ([]github.Comment, error) {
			return []github.Comment{{ID: 7, HTMLURL: "https://github.com/o/r/issues/5#issuecomment-7", Body: existing}}, nil
		}
		f.updateComment = func(_ context.Context, _ string, _ int64, body string) (github.Comment, error) {
			updated = body
			return github.Comment{ID: 7, HTMLURL: "https://github.com/o/r/issues/5#issuecomment-7"}, nil
		}
	})
	code := app.runCommentUpsert(context.Background(), []string{"--repo", "o/r", "--issue", "5", "--type", "SPEC", "--id", "SPEC-001", "--body-file", bodyPath, "--json"})
	if code != 0 {
		t.Fatalf("upsert update failed exit=%d out=%q", code, out.String())
	}
	if !strings.Contains(updated, peer) {
		t.Fatalf("update must preserve prior Related Comments link %q:\n%s", peer, updated)
	}
	if urls := model.RelatedCommentURLs(model.ParseTypedComment(updated)); len(urls) != 1 {
		t.Fatalf("expected exactly one preserved link, got %v", urls)
	}
	if strings.Contains(out.String(), "dropped") {
		t.Fatalf("preserving update must not warn about dropped links: %s", out.String())
	}
}

func TestCommentUpsertUpdateRetainsMultipleLinksWithoutDuplicates(t *testing.T) {
	peer1 := "https://github.com/o/r/issues/9#issuecomment-101"
	peer2 := "https://github.com/o/r/issues/9#issuecomment-102"
	fresh := generateCanonicalSpecBody(t)
	existing := fresh
	for _, p := range []string{peer1, peer2} {
		next, changed, err := model.AddRelatedCommentLink(existing, p)
		if err != nil || !changed {
			t.Fatalf("seed link %s: changed=%v err=%v", p, changed, err)
		}
		existing = next
	}
	// Regenerated body already carries peer1; peer2 exists only on the old comment.
	freshWithOne, _, err := model.AddRelatedCommentLink(fresh, peer1)
	if err != nil {
		t.Fatal(err)
	}
	bodyPath := writeTempInput(t, freshWithOne)

	var updated string
	app := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	app.selectGitHubBackend = ghSelection
	app.newGitHubBackend = newFakeBackend(func(f *fakeGitHubBackend) {
		f.listIssueComments = func(context.Context, string, int) ([]github.Comment, error) {
			return []github.Comment{{ID: 8, HTMLURL: "https://github.com/o/r/issues/5#issuecomment-8", Body: existing}}, nil
		}
		f.updateComment = func(_ context.Context, _ string, _ int64, body string) (github.Comment, error) {
			updated = body
			return github.Comment{ID: 8}, nil
		}
	})
	code := app.runCommentUpsert(context.Background(), []string{"--repo", "o/r", "--issue", "5", "--type", "SPEC", "--id", "SPEC-001", "--body-file", bodyPath, "--json"})
	if code != 0 {
		t.Fatalf("upsert update failed exit=%d", code)
	}
	urls := model.RelatedCommentURLs(model.ParseTypedComment(updated))
	if len(urls) != 2 {
		t.Fatalf("expected both links retained without duplicates, got %v", urls)
	}
	if strings.Count(updated, peer1) != 1 || strings.Count(updated, peer2) != 1 {
		t.Fatalf("links must appear exactly once each:\n%s", updated)
	}
}

func generateTaskBody(t *testing.T, id, inputJSON string) string {
	t.Helper()
	inPath := writeTempInput(t, inputJSON)
	var genOut, errOut bytes.Buffer
	gen := newApp(strings.NewReader(""), &genOut, &errOut)
	if code := gen.runCommentGenerate(context.Background(), []string{"--type", "TASK", "--id", id, "--input-file", inPath}); code != 0 {
		t.Fatalf("generate TASK failed: %s", errOut.String())
	}
	return genOut.String()
}

func taskCoversInput(covers string) string {
	return `{
  "title": "Do the thing",
  "summary": "A task under covers resolution.",
  "checklist": ["step one"],
  "covers": [` + covers + `],
  "execution_planning": {
    "owned_areas": ["internal/x"],
    "shared_touchpoints": ["internal/y"],
    "dependencies": ["none"],
    "coupling": "low",
    "execution_mode": "parallel",
    "complexity": "low"
  }
}`
}

func TestParseCoversSectionIDs(t *testing.T) {
	body := generateTaskBody(t, "TASK-001", taskCoversInput(`"SPEC-001", "SPEC-002"`))
	ids := parseCoversSectionIDs(body)
	if len(ids) != 2 || ids[0] != "SPEC-001" || ids[1] != "SPEC-002" {
		t.Fatalf("parseCoversSectionIDs = %v, want [SPEC-001 SPEC-002]", ids)
	}
	empty := generateTaskBody(t, "TASK-001", taskCoversInput(``))
	if ids := parseCoversSectionIDs(empty); len(ids) != 0 {
		t.Fatalf("N/A covers should yield no IDs, got %v", ids)
	}
}

func TestCommentUpsertCoversIssueCreatesDurableBidirectionalLinks(t *testing.T) {
	// SPEC-002: a single generate | upsert --covers-issue links the TASK to its
	// covered SPEC in both directions, with no separate `issue-spec link` call.
	taskBody := generateTaskBody(t, "TASK-001", taskCoversInput(`"SPEC-001"`))
	bodyPath := writeTempInput(t, taskBody)
	specBody := generateCanonicalSpecBody(t)
	specURL := "https://github.com/o/r/issues/100#issuecomment-501"
	taskURL := "https://github.com/o/r/issues/5#issuecomment-9"

	var createdTask, specUpdated string
	var out bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &bytes.Buffer{})
	app.selectGitHubBackend = ghSelection
	app.newGitHubBackend = newFakeBackend(func(f *fakeGitHubBackend) {
		f.listIssueComments = func(_ context.Context, _ string, issue int) ([]github.Comment, error) {
			if issue == 100 {
				return []github.Comment{{ID: 501, HTMLURL: specURL, Body: specBody}}, nil
			}
			return nil, nil
		}
		f.createComment = func(_ context.Context, _ string, _ int, body string) (github.Comment, error) {
			createdTask = body
			return github.Comment{ID: 9, HTMLURL: taskURL}, nil
		}
		f.updateComment = func(_ context.Context, _ string, id int64, body string) (github.Comment, error) {
			if id == 501 {
				specUpdated = body
			}
			return github.Comment{ID: id}, nil
		}
	})
	code := app.runCommentUpsert(context.Background(), []string{"--repo", "o/r", "--issue", "5", "--type", "TASK", "--id", "TASK-001", "--body-file", bodyPath, "--covers-issue", "100", "--json"})
	if code != 0 {
		t.Fatalf("covers upsert failed exit=%d out=%q", code, out.String())
	}
	if !strings.Contains(createdTask, specURL) {
		t.Fatalf("forward link (SPEC URL on TASK) missing:\n%s", createdTask)
	}
	if !strings.Contains(specUpdated, taskURL) {
		t.Fatalf("backlink (TASK URL on SPEC) missing:\n%s", specUpdated)
	}
	arts := []model.Artifact{
		{Issue: 5, CommentID: 9, URL: taskURL, Comment: model.ParseTypedComment(createdTask)},
		{Issue: 100, CommentID: 501, URL: specURL, Comment: model.ParseTypedComment(specUpdated)},
	}
	if rep := model.VerifyTraceability(arts); !rep.OK {
		t.Fatalf("traceability must be OK after covers linking: %v", rep.Errors)
	}
}

func TestCommentUpsertCoversIssueUnknownIDWarnsButSucceeds(t *testing.T) {
	taskBody := generateTaskBody(t, "TASK-001", taskCoversInput(`"SPEC-999"`))
	bodyPath := writeTempInput(t, taskBody)

	var createdTask string
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	app.selectGitHubBackend = ghSelection
	app.newGitHubBackend = newFakeBackend(func(f *fakeGitHubBackend) {
		f.listIssueComments = func(_ context.Context, _ string, _ int) ([]github.Comment, error) { return nil, nil }
		f.createComment = func(_ context.Context, _ string, _ int, body string) (github.Comment, error) {
			createdTask = body
			return github.Comment{ID: 9, HTMLURL: "https://github.com/o/r/issues/5#issuecomment-9"}, nil
		}
	})
	code := app.runCommentUpsert(context.Background(), []string{"--repo", "o/r", "--issue", "5", "--type", "TASK", "--id", "TASK-001", "--body-file", bodyPath, "--covers-issue", "100", "--json"})
	if code != 0 {
		t.Fatalf("upsert with unresolved covers must still succeed, exit=%d err=%q", code, errOut.String())
	}
	if createdTask == "" {
		t.Fatal("TASK should still be written when a covers ID cannot be resolved")
	}
	if !strings.Contains(errOut.String(), "SPEC-999") {
		t.Fatalf("expected a non-fatal warning naming the unresolved covers ID:\n%s", errOut.String())
	}
}

func TestDroppedRelatedLinks(t *testing.T) {
	// The link-drop warning (SPEC-003) can only fire on a link-reducing write; once
	// Decision 1's merge is in place the real path never reduces, so the detector is
	// exercised directly on synthetic before/after sets.
	before := []string{"https://x/#issuecomment-1", "https://x/#issuecomment-2"}
	after := []string{"https://x/#issuecomment-1"}
	dropped := droppedRelatedLinks(before, after)
	if len(dropped) != 1 || dropped[0] != "https://x/#issuecomment-2" {
		t.Fatalf("expected the reduced link reported as dropped, got %v", dropped)
	}
	if got := droppedRelatedLinks(before, before); len(got) != 0 {
		t.Fatalf("no drop expected when the set is preserved, got %v", got)
	}
	// Superset (a link added) is not a drop.
	if got := droppedRelatedLinks(after, before); len(got) != 0 {
		t.Fatalf("adding links must not report a drop, got %v", got)
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
