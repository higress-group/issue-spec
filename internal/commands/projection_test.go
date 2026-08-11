package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/model"
	"github.com/higress-group/issue-spec/internal/preview"
)

type conditionalProjectionCreateTestBackend struct {
	fakeGitHubBackend
	createProjection func(context.Context, string, int, string, int, string) (github.CommentRepresentation, error)
}

func (b conditionalProjectionCreateTestBackend) CreateProjectionCommentIfAbsent(ctx context.Context, repo string, issue int, phase string, owner int, body string) (github.CommentRepresentation, error) {
	return b.createProjection(ctx, repo, issue, phase, owner, body)
}

func TestProjectionValidateAcceptsPreviewFreeAndExecutableBodies(t *testing.T) {
	tests := []struct {
		name         string
		body         string
		previewCount int
	}{
		{name: "preview free", body: "Readable Markdown fallback.\n", previewCount: 0},
		{name: "executable preview", body: "Fallback.\n```html-preview id=design-review version=1\n<p>Review</p>\n```\n", previewCount: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			bodyFile := writeTempInput(t, test.body)
			app, out, errOut := projectionTestApp(fakeGitHubBackend{})
			code := app.runProjection(t.Context(), []string{"validate", "--phase", "design-explainer", "--body-file", bodyFile, "--json"})
			if code != 0 || errOut.Len() != 0 {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
			}
			var result projectionValidationResult
			if err := json.Unmarshal(out.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if !result.OK || result.Phase != "design-explainer" || result.PreviewCount != test.previewCount ||
				result.Code != "" || result.DiagnosticsTruncated != nil {
				t.Fatalf("result=%+v", result)
			}
		})
	}
}

func TestProjectionValidateReportsCanonicalPreviewDiagnostics(t *testing.T) {
	oversized := strings.Repeat("x", preview.MaxSourceSize+1)
	tests := []struct {
		name string
		body string
		code string
	}{
		{name: "missing id", body: "```html-preview version=1\nx\n```\n", code: "missing_id"},
		{name: "missing version", body: "```html-preview id=known\nx\n```\n", code: "missing_version"},
		{name: "malformed metadata", body: "```html-preview id=known version=no\nx\n```\n", code: "malformed_metadata"},
		{name: "duplicate id", body: "```html-preview id=dup version=1\na\n```\n```html-preview id=dup version=1\nb\n```\n", code: "duplicate_id"},
		{name: "unknown version", body: "```html-preview id=known version=2\nx\n```\n", code: "unknown_version"},
		{name: "unclosed fence", body: "```html-preview id=known version=1\nx\n", code: "unclosed_fence"},
		{name: "oversized source", body: "```html-preview id=known version=1\n" + oversized + "\n```\n", code: "source_too_large"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			bodyFile := writeTempInput(t, test.body)
			app, out, errOut := projectionTestApp(fakeGitHubBackend{})
			code := app.runProjection(t.Context(), []string{"validate", "--phase", "design-explainer", "--body-file", bodyFile, "--json"})
			if code != 1 || errOut.Len() != 0 {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
			}
			var result projectionValidationResult
			if err := json.Unmarshal(out.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result.OK || result.Code != invalidHTMLPreviewFailureCode || result.PreviewCount == 0 ||
				result.DiagnosticsTruncated == nil || len(result.Diagnostics) == 0 {
				t.Fatalf("result=%+v", result)
			}
			found := false
			for _, diagnostic := range result.Diagnostics {
				if diagnostic.Code == test.code {
					found = true
					if diagnostic.Block < 1 || diagnostic.Range.Start < 0 || diagnostic.Range.End > len(test.body) ||
						diagnostic.Range.Start >= diagnostic.Range.End {
						t.Fatalf("diagnostic range is not an exact bounded fence range: %+v body_len=%d", diagnostic, len(test.body))
					}
				}
				if diagnostic.Hint != "" && diagnostic.Code != "missing_id" && diagnostic.Code != "missing_version" {
					t.Fatalf("unexpected hint for %s: %+v", diagnostic.Code, diagnostic)
				}
			}
			if !found {
				t.Fatalf("missing code %q in %+v", test.code, result.Diagnostics)
			}
		})
	}
}

func TestProjectionValidateBoundsDiagnosticsWithoutEchoingSource(t *testing.T) {
	const source = "SENSITIVE_PREVIEW_SOURCE_MUST_NOT_BE_EMITTED"
	var body strings.Builder
	for range 11 {
		body.WriteString("```html-preview\n")
		body.WriteString(source)
		body.WriteString("\n```\n")
	}
	bodyFile := writeTempInput(t, body.String())
	app, out, errOut := projectionTestApp(fakeGitHubBackend{})
	code := app.runProjection(t.Context(), []string{"validate", "--phase", "proposal-choice-brief", "--body-file", bodyFile, "--json"})
	if code != 1 || errOut.Len() != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	var result projectionValidationResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.PreviewCount != 11 || len(result.Diagnostics) != maxProjectionDiagnostics ||
		result.DiagnosticsTruncated == nil || !*result.DiagnosticsTruncated || strings.Contains(out.String(), source) {
		t.Fatalf("result=%+v output_contains_source=%v", result, strings.Contains(out.String(), source))
	}

	out.Reset()
	errOut.Reset()
	code = app.runProjection(t.Context(), []string{"validate", "--phase", "proposal-choice-brief", "--body-file", bodyFile})
	if code != 1 || out.Len() != 0 || !strings.Contains(errOut.String(), "diagnostics truncated after 20 entries") ||
		!strings.Contains(errOut.String(), "missing_id: html-preview metadata requires id") ||
		!strings.Contains(errOut.String(), "missing_version: html-preview metadata requires version=1") ||
		strings.Contains(errOut.String(), source) {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
}

func TestProjectionPreflightRejectsOrdinaryBodyViolations(t *testing.T) {
	tests := []struct {
		name string
		body string
		code string
	}{
		{name: "empty", body: " \n\t", code: "empty_body"},
		{name: "projection marker", body: "<!-- issue-spec:projection phase=design-explainer owner=1 version=1 source-digest=" + strings.Repeat("a", 64) + " -->\n", code: "projection_marker_present"},
		{name: "typed marker", body: "<!-- issue-spec:type=TASK id=TASK-1001 version=1 -->\n# TASK-1001\n", code: "typed_marker_present"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := preflightProjectionBody(test.body)
			if err == nil || err.Code != test.code {
				t.Fatalf("error=%+v", err)
			}
		})
	}
}

func TestProjectionUpsertInvalidPreviewDoesNotSelectBackend(t *testing.T) {
	const sourceDigest = "4444444444444444444444444444444444444444444444444444444444444444"
	bodyFile := writeTempInput(t, "Fallback.\n```html-preview\nPRIVATE_SOURCE\n```\n")
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	app := newApp(strings.NewReader(""), out, errOut)
	backendSelections := 0
	app.selectGitHubBackend = func(context.Context, string) (auth.GitHubBackendSelection, error) {
		backendSelections++
		return auth.GitHubBackendSelection{}, nil
	}
	code := app.runProjection(t.Context(), []string{"upsert", "--repo", "o/r", "--issue", "44",
		"--phase", "design-explainer", "--source-digest", sourceDigest, "--body-file", bodyFile, "--json"})
	if code != 2 || backendSelections != 0 || errOut.Len() != 0 || strings.Contains(out.String(), "PRIVATE_SOURCE") {
		t.Fatalf("exit=%d selections=%d stdout=%q stderr=%q", code, backendSelections, out.String(), errOut.String())
	}
	var result projectionValidationResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Code != invalidHTMLPreviewFailureCode || result.PreviewCount != 1 {
		t.Fatalf("result=%+v", result)
	}
}

func TestProjectionValidateIsAdvertisedInGlobalUsage(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := Execute([]string{"--help"}, strings.NewReader(""), &out, &errOut); code != 0 || errOut.Len() != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	if !strings.Contains(out.String(), "issue-spec projection validate --phase") {
		t.Fatalf("global usage does not advertise projection validate:\n%s", out.String())
	}
}

func TestProjectionUpsertNonAtomicCreateRequiresExpectedAbsenceAcknowledgement(t *testing.T) {
	const sourceDigest = "abababababababababababababababababababababababababababababababab"
	creates := 0
	backend := fakeGitHubBackend{
		info: github.BackendInfo{Name: "gh", Kind: "gh", Host: "github.com"},
		listIssueComments: func(context.Context, string, int) ([]github.Comment, error) {
			return nil, nil
		},
		createComment: func(context.Context, string, int, string) (github.Comment, error) {
			creates++
			return github.Comment{}, nil
		},
	}
	app, out, errOut := projectionTestApp(backend)
	bodyFile := writeTempInput(t, "Human review synthesis.")
	code := app.runProjection(t.Context(), []string{"upsert", "--repo", "o/r", "--issue", "17",
		"--phase", "proposal-choice-brief", "--source-digest", sourceDigest, "--body-file", bodyFile,
		"--allow-nonatomic", "--json"})
	if code != 1 || creates != 0 || out.Len() != 0 ||
		!strings.Contains(errOut.String(), "--allow-nonatomic and --expected-absence") {
		t.Fatalf("exit=%d creates=%d stdout=%q stderr=%q", code, creates, out.String(), errOut.String())
	}
}

func TestProjectionUpsertCreatesOrdinaryStatuslessCommentWithObservedNonAtomicFallback(t *testing.T) {
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
		"--phase", "proposal-choice-brief", "--source-digest", sourceDigest, "--body-file", bodyFile,
		"--allow-nonatomic", "--expected-absence", "--json"})
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
	var result projectionUpsertResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Action != "created" || result.Phase != "proposal-choice-brief" ||
		result.Owner != 17 || result.SourceDigest != sourceDigest || result.Atomic ||
		result.Guarantee != github.CommentMutationNonAtomicSingleWriter {
		t.Fatalf("result=%+v", result)
	}
	var rawResult map[string]any
	if err := json.Unmarshal(out.Bytes(), &rawResult); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"status", "type", "id", "body"} {
		if _, found := rawResult[forbidden]; found {
			t.Fatalf("projection result exposed authoritative field %q: %v", forbidden, rawResult)
		}
	}
}

func TestProjectionUpsertUsesAtomicConditionalCreateWhenSupported(t *testing.T) {
	const sourceDigest = "acacacacacacacacacacacacacacacacacacacacacacacacacacacacacacacac"
	fallbackCreates := 0
	base := fakeGitHubBackend{
		info: github.BackendInfo{Name: "rest", Kind: "rest", Host: "issues.example.test"},
		listIssueComments: func(context.Context, string, int) ([]github.Comment, error) {
			return nil, nil
		},
		createComment: func(context.Context, string, int, string) (github.Comment, error) {
			fallbackCreates++
			return github.Comment{}, nil
		},
	}
	conditionalCreates := 0
	backend := conditionalProjectionCreateTestBackend{
		fakeGitHubBackend: base,
		createProjection: func(_ context.Context, repo string, issue int, phase string, owner int, body string) (github.CommentRepresentation, error) {
			conditionalCreates++
			if repo != "o/r" || issue != 19 || phase != "design-explainer" || owner != 19 {
				t.Fatalf("conditional create repo=%q issue=%d phase=%q owner=%d", repo, issue, phase, owner)
			}
			return github.CommentRepresentation{
				Comment:               github.Comment{ID: 190, Body: body},
				RepresentationVersion: 1,
				Guarantee:             github.CommentMutationStrictConditional,
			}, nil
		},
	}
	app, out, errOut := projectionTestApp(backend)
	bodyFile := writeTempInput(t, "Atomic explainer.")
	code := app.runProjection(t.Context(), []string{"upsert", "--repo", "o/r", "--hostname", "issues.example.test",
		"--issue", "19", "--phase", "design-explainer", "--source-digest", sourceDigest,
		"--body-file", bodyFile, "--json"})
	if code != 0 || errOut.Len() != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	var result projectionUpsertResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Action != "created" || !result.Atomic ||
		result.Guarantee != github.CommentMutationStrictConditional ||
		result.RepresentationVersion != 1 || conditionalCreates != 1 || fallbackCreates != 0 {
		t.Fatalf("result=%+v conditional=%d fallback=%d", result, conditionalCreates, fallbackCreates)
	}
}

func TestProjectionUpsertConcurrentNonAtomicFirstCreateFailsClosedOnAmbiguity(t *testing.T) {
	const sourceDigest = "adadadadadadadadadadadadadadadadadadadadadadadadadadadadadadadad"
	var (
		mu       sync.Mutex
		comments []github.Comment
		lists    int
		creates  int
	)
	initialListsReady := make(chan struct{})
	createsReady := make(chan struct{})
	backend := fakeGitHubBackend{
		info: github.BackendInfo{Name: "gh", Kind: "gh", Host: "github.com"},
		listIssueComments: func(context.Context, string, int) ([]github.Comment, error) {
			mu.Lock()
			lists++
			call := lists
			if call == 2 {
				close(initialListsReady)
			}
			snapshot := append([]github.Comment(nil), comments...)
			mu.Unlock()
			if call <= 2 {
				<-initialListsReady
				return nil, nil
			}
			return snapshot, nil
		},
		createComment: func(_ context.Context, _ string, _ int, body string) (github.Comment, error) {
			mu.Lock()
			creates++
			comment := github.Comment{ID: int64(creates), Body: body}
			comments = append(comments, comment)
			if creates == 2 {
				close(createsReady)
			}
			mu.Unlock()
			<-createsReady
			return comment, nil
		},
	}
	bodyFile := writeTempInput(t, "Concurrent execution brief.")
	type callResult struct {
		code int
		out  string
		err  string
	}
	results := make(chan callResult, 2)
	for range 2 {
		go func() {
			app, out, errOut := projectionTestApp(backend)
			code := app.runProjection(t.Context(), []string{"upsert", "--repo", "o/r", "--issue", "23",
				"--phase", "implement-execution-brief", "--source-digest", sourceDigest,
				"--body-file", bodyFile, "--allow-nonatomic", "--expected-absence", "--json"})
			results <- callResult{code: code, out: out.String(), err: errOut.String()}
		}()
	}
	for range 2 {
		result := <-results
		if result.code != 1 || result.err != "" {
			t.Fatalf("result=%+v", result)
		}
		var failure map[string]any
		if err := json.Unmarshal([]byte(result.out), &failure); err != nil {
			t.Fatalf("decode result %q: %v", result.out, err)
		}
		if failure["code"] != "projection_post_write_ambiguous" {
			t.Fatalf("failure=%v", failure)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if creates != 2 || len(comments) != 2 {
		t.Fatalf("creates=%d comments=%d", creates, len(comments))
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
	wantBody := renderProjectionMarker(projectionMarker{
		Phase: "design-explainer", Owner: 22, Version: 1, SourceDigest: digest,
	}) + "\n\n" + raw
	if body != wantBody {
		t.Fatalf("valid preview source changed:\ngot:  %q\nwant: %q", body, wantBody)
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
