package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/model"
)

func TestProjectionUpsertCreatesOrdinaryStatuslessCommentAndObservesIt(t *testing.T) {
	const sourceDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	var comments []github.Comment
	backend := fakeGitHubBackend{
		info: github.BackendInfo{Name: "gh", Kind: "gh", Host: "github.com"},
		listIssueComments: func(context.Context, string, int) ([]github.Comment, error) {
			return append([]github.Comment(nil), comments...), nil
		},
		createComment: func(_ context.Context, repo string, issue int, body string) (github.Comment, error) {
			if repo != "o/r" || issue != 17 {
				t.Fatalf("create repo=%q issue=%d", repo, issue)
			}
			comment := github.Comment{ID: 91, HTMLURL: "https://github.com/o/r/issues/17#issuecomment-91", Body: body}
			comments = append(comments, comment)
			return comment, nil
		},
	}
	app, out, errOut := projectionTestApp(backend)
	bodyFile := writeTempInput(t, "Human review synthesis.\n\n```html-preview id=proposal-review version=1\n<p>Review</p>\n```\n")
	code := app.runProjection(t.Context(), []string{"upsert", "--repo", "o/r", "--issue", "17",
		"--phase", "proposal-choice-brief", "--source-digest", sourceDigest, "--body-file", bodyFile, "--json"})
	if code != 0 || errOut.Len() != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	if len(comments) != 1 || !strings.HasPrefix(comments[0].Body,
		"<!-- issue-spec:projection phase=proposal-choice-brief owner=17 version=1 source-digest="+sourceDigest+" -->\n") {
		t.Fatalf("created body:\n%s", comments[0].Body)
	}
	if marker, found, err := model.FindMarker(comments[0].Body); err != nil || found {
		t.Fatalf("projection became typed: marker=%+v found=%v err=%v", marker, found, err)
	}
	var result map[string]any
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result["action"] != "created" || result["phase"] != "proposal-choice-brief" ||
		result["owner"] != float64(17) || result["source_digest"] != sourceDigest {
		t.Fatalf("result=%v", result)
	}
	for _, forbidden := range []string{"status", "type", "id", "body"} {
		if _, found := result[forbidden]; found {
			t.Fatalf("projection result exposed authoritative field %q: %v", forbidden, result)
		}
	}
}

func TestProjectionUpsertUsesCASAndIsIdempotent(t *testing.T) {
	const (
		oldSource = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		newSource = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	)
	current := projectionBodyForTest("design-explainer", 22, oldSource, "Old explainer.")
	version := int64(4)
	writes := 0
	base := fakeGitHubBackend{
		info: github.BackendInfo{Name: "rest", Kind: "rest", Host: "issues.example.test"},
		listIssueComments: func(context.Context, string, int) ([]github.Comment, error) {
			return []github.Comment{{ID: 92, HTMLURL: "https://issues.example.test/o/r/issues/22#comment-92", Body: current}}, nil
		},
	}
	backend := conditionalTransitionBackend{
		fakeGitHubBackend: base,
		observe: func(context.Context, string, int64) (github.CommentRepresentation, error) {
			return github.CommentRepresentation{
				Comment:               github.Comment{ID: 92, HTMLURL: "https://issues.example.test/o/r/issues/22#comment-92", Body: current},
				RepresentationVersion: version, Guarantee: github.CommentMutationStrictConditional,
			}, nil
		},
		update: func(_ context.Context, _ string, id, expected int64, body string) (github.CommentRepresentation, error) {
			writes++
			if id != 92 || expected != version {
				t.Fatalf("update id=%d expected=%d version=%d", id, expected, version)
			}
			current = body
			version++
			return github.CommentRepresentation{
				Comment:               github.Comment{ID: id, HTMLURL: "https://issues.example.test/o/r/issues/22#comment-92", Body: body},
				RepresentationVersion: version, Guarantee: github.CommentMutationStrictConditional,
			}, nil
		},
	}
	app, out, errOut := projectionTestApp(backend)
	bodyFile := writeTempInput(t, "New explainer.")
	args := []string{"upsert", "--repo", "o/r", "--hostname", "issues.example.test", "--issue", "22",
		"--phase", "design-explainer", "--source-digest", newSource, "--body-file", bodyFile,
		"--expected-digest", bodyDigest(current), "--json"}
	if code := app.runProjection(t.Context(), args); code != 0 || errOut.Len() != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	var result projectionUpsertResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Action != "updated" || !result.Atomic || result.Guarantee != github.CommentMutationStrictConditional ||
		result.RepresentationVersion != 5 || writes != 1 || !strings.Contains(current, "source-digest="+newSource) {
		t.Fatalf("result=%+v writes=%d body=%s", result, writes, current)
	}

	out.Reset()
	errOut.Reset()
	args = []string{"upsert", "--repo", "o/r", "--hostname", "issues.example.test", "--issue", "22",
		"--phase", "design-explainer", "--source-digest", newSource, "--body-file", bodyFile, "--json"}
	if code := app.runProjection(t.Context(), args); code != 0 || errOut.Len() != 0 {
		t.Fatalf("retry exit=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Action != "unchanged" || result.RepresentationVersion != 5 || writes != 1 {
		t.Fatalf("retry result=%+v writes=%d", result, writes)
	}
}

func TestProjectionUpsertFailsClosedOnAmbiguity(t *testing.T) {
	const sourceDigest = "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	body := projectionBodyForTest("implement-execution-brief", 33, sourceDigest, "Execution brief.")
	writes := 0
	backend := fakeGitHubBackend{
		info: github.BackendInfo{Name: "gh", Kind: "gh", Host: "github.com"},
		listIssueComments: func(context.Context, string, int) ([]github.Comment, error) {
			return []github.Comment{{ID: 1, Body: body}, {ID: 2, Body: body}}, nil
		},
		createComment: func(context.Context, string, int, string) (github.Comment, error) {
			writes++
			return github.Comment{}, nil
		},
		updateComment: func(context.Context, string, int64, string) (github.Comment, error) {
			writes++
			return github.Comment{}, nil
		},
	}
	app, _, errOut := projectionTestApp(backend)
	bodyFile := writeTempInput(t, "Updated execution brief.")
	code := app.runProjection(t.Context(), []string{"upsert", "--repo", "o/r", "--issue", "33",
		"--phase", "implement-execution-brief", "--source-digest", sourceDigest, "--body-file", bodyFile})
	if code != 1 || !strings.Contains(errOut.String(), "ambiguous: found 2 matching markers") || writes != 0 {
		t.Fatalf("exit=%d stderr=%q writes=%d", code, errOut.String(), writes)
	}
}

func TestProjectionUpsertExpectedDigestConflictPreventsCASWrite(t *testing.T) {
	const sourceDigest = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	current := projectionBodyForTest("design-explainer", 22, sourceDigest, "Current.")
	writes := 0
	base := fakeGitHubBackend{
		info: github.BackendInfo{Name: "rest", Kind: "rest", Host: "issues.example.test"},
		listIssueComments: func(context.Context, string, int) ([]github.Comment, error) {
			return []github.Comment{{ID: 93, Body: current}}, nil
		},
	}
	backend := conditionalTransitionBackend{
		fakeGitHubBackend: base,
		observe: func(context.Context, string, int64) (github.CommentRepresentation, error) {
			return github.CommentRepresentation{Comment: github.Comment{ID: 93, Body: current}, RepresentationVersion: 7}, nil
		},
		update: func(context.Context, string, int64, int64, string) (github.CommentRepresentation, error) {
			writes++
			return github.CommentRepresentation{}, nil
		},
	}
	app, _, errOut := projectionTestApp(backend)
	bodyFile := writeTempInput(t, "Updated.")
	code := app.runProjection(t.Context(), []string{"upsert", "--repo", "o/r", "--hostname", "issues.example.test", "--issue", "22",
		"--phase", "design-explainer", "--source-digest", sourceDigest, "--body-file", bodyFile,
		"--expected-digest", strings.Repeat("0", 64)})
	if code != 1 || !strings.Contains(errOut.String(), "projection body digest conflict") || writes != 0 {
		t.Fatalf("exit=%d stderr=%q writes=%d", code, errOut.String(), writes)
	}
}

func TestProjectionUpsertRequiresExplicitNonAtomicFallbackAndObservesExactWrite(t *testing.T) {
	const (
		oldSource = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
		newSource = "1111111111111111111111111111111111111111111111111111111111111111"
	)
	current := projectionBodyForTest("proposal-choice-brief", 17, oldSource, "Old.")
	writes := 0
	backend := fakeGitHubBackend{
		info: github.BackendInfo{Name: "gh", Kind: "gh", Host: "github.com"},
		listIssueComments: func(context.Context, string, int) ([]github.Comment, error) {
			return []github.Comment{{ID: 94, HTMLURL: "https://github.com/o/r/issues/17#issuecomment-94", Body: current}}, nil
		},
		updateComment: func(_ context.Context, _ string, _ int64, body string) (github.Comment, error) {
			writes++
			current = body
			return github.Comment{ID: 94, HTMLURL: "https://github.com/o/r/issues/17#issuecomment-94", Body: body}, nil
		},
	}
	app, out, errOut := projectionTestApp(backend)
	bodyFile := writeTempInput(t, "New.")
	baseArgs := []string{"upsert", "--repo", "o/r", "--issue", "17", "--phase", "proposal-choice-brief",
		"--source-digest", newSource, "--body-file", bodyFile, "--json"}
	if code := app.runProjection(t.Context(), baseArgs); code != 1 ||
		!strings.Contains(errOut.String(), "--allow-nonatomic and --expected-digest") || writes != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q writes=%d", code, out.String(), errOut.String(), writes)
	}

	out.Reset()
	errOut.Reset()
	args := append(append([]string(nil), baseArgs...), "--allow-nonatomic", "--expected-digest", bodyDigest(current))
	if code := app.runProjection(t.Context(), args); code != 0 || errOut.Len() != 0 {
		t.Fatalf("fallback exit=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	var result projectionUpsertResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Action != "updated" || result.Atomic ||
		result.Guarantee != github.CommentMutationNonAtomicSingleWriter || writes != 1 ||
		result.AfterDigest != bodyDigest(current) || !strings.Contains(current, "source-digest="+newSource) {
		t.Fatalf("result=%+v writes=%d body=%s", result, writes, current)
	}
}

func TestProjectionUpsertRejectsNonAtomicPostWriteMismatch(t *testing.T) {
	const sourceDigest = "2222222222222222222222222222222222222222222222222222222222222222"
	current := projectionBodyForTest("design-explainer", 22, sourceDigest, "Old.")
	before := current
	backend := fakeGitHubBackend{
		info: github.BackendInfo{Name: "gh", Kind: "gh", Host: "github.com"},
		listIssueComments: func(context.Context, string, int) ([]github.Comment, error) {
			return []github.Comment{{ID: 95, Body: current}}, nil
		},
		updateComment: func(_ context.Context, _ string, _ int64, body string) (github.Comment, error) {
			current = body + "\nConcurrent overwrite.\n"
			return github.Comment{ID: 95, Body: body}, nil
		},
	}
	app, out, _ := projectionTestApp(backend)
	bodyFile := writeTempInput(t, "New.")
	code := app.runProjection(t.Context(), []string{"upsert", "--repo", "o/r", "--issue", "22",
		"--phase", "design-explainer", "--source-digest", sourceDigest, "--body-file", bodyFile,
		"--allow-nonatomic", "--expected-digest", bodyDigest(before), "--json"})
	if code != 1 {
		t.Fatalf("exit=%d stdout=%q", code, out.String())
	}
	var result map[string]any
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result["code"] != "projection_post_write_mismatch" ||
		result["planned_digest"] == result["current_digest"] {
		t.Fatalf("result=%v", result)
	}
}

func TestProjectionMarkersInsidePreviewSourceStayOpaque(t *testing.T) {
	const digest = "3333333333333333333333333333333333333333333333333333333333333333"
	raw := "```html-preview id=marker-demo version=1\n<!-- issue-spec:projection phase=design-explainer owner=22 version=1 source-digest=" + digest + " -->\n```\n"
	body, err := prepareProjectionBody(raw, projectionMarker{
		Phase: "design-explainer", Owner: 22, Version: 1, SourceDigest: digest,
	})
	if err != nil {
		t.Fatal(err)
	}
	markers, err := parseProjectionMarkers(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(markers) != 1 || markers[0].Phase != "design-explainer" {
		t.Fatalf("markers=%+v body=%s", markers, body)
	}
}

func projectionBodyForTest(phase string, owner int, sourceDigest, content string) string {
	return renderProjectionMarker(projectionMarker{
		Phase: phase, Owner: owner, Version: projectionMarkerVersion, SourceDigest: sourceDigest,
	}) + "\n\n" + content + "\n"
}

func projectionTestApp(backend github.Backend) (*app, *bytes.Buffer, *bytes.Buffer) {
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	app := newApp(strings.NewReader(""), out, errOut)
	app.selectGitHubBackend = func(context.Context, string) (auth.GitHubBackendSelection, error) {
		return auth.GitHubBackendSelection{Host: backend.BackendInfo().Host}, nil
	}
	app.newGitHubBackend = func(context.Context, auth.GitHubBackendSelection) (github.Backend, error) {
		return backend, nil
	}
	return app, out, errOut
}
