package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/model"
	"github.com/higress-group/issue-spec/internal/workflow"
)

type observingFakeBackend struct {
	fakeGitHubBackend
	observeIssueComment func(context.Context, string, int64) (github.IssueCommentObservation, error)
}

type deletingFakeBackend struct {
	fakeGitHubBackend
	deleteComment func(context.Context, string, int64) error
}

func (f deletingFakeBackend) DeleteComment(ctx context.Context, repo string, commentID int64) error {
	return f.deleteComment(ctx, repo, commentID)
}

func (f observingFakeBackend) ObserveIssueComment(ctx context.Context, repo string, commentID int64) (github.IssueCommentObservation, error) {
	return f.observeIssueComment(ctx, repo, commentID)
}

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

func TestCommentCreateUsesSelectedGHBackendAndProtectedStdin(t *testing.T) {
	const body = "Could you clarify the rollout boundary?\n\nThis is ordinary discussion, not workflow evidence.\n"
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(body), &out, &errOut)
	app.selectGitHubBackend = ghSelection
	app.newGitHubBackend = func(_ context.Context, selection auth.GitHubBackendSelection) (github.Backend, error) {
		if selection.Name != auth.GitHubBackendNameGH || selection.Host != "github.com" {
			t.Fatalf("selection = %+v", selection)
		}
		return fakeGitHubBackend{
			info: github.BackendInfo{Name: selection.Name, Kind: selection.Kind, Host: selection.Host},
			createComment: func(_ context.Context, repo string, issue int, gotBody string) (github.Comment, error) {
				if repo != "o/r" || issue != 17 || gotBody != body {
					t.Fatalf("CreateComment repo=%q issue=%d body=%q", repo, issue, gotBody)
				}
				return github.Comment{ID: 901, HTMLURL: "https://github.com/o/r/issues/17#issuecomment-901",
					URL: "https://api.github.com/repos/o/r/issues/comments/901", Body: gotBody}, nil
			},
		}, nil
	}
	code := app.runComment(t.Context(), []string{"create", "--repo", "o/r", "--issue", "17", "--body-file", "-", "--json"})
	if code != 0 {
		t.Fatalf("comment create exit=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	var result map[string]any
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result) != 6 || result["ok"] != true || result["action"] != "created" || result["comment_id"] != float64(901) ||
		result["url"] != "https://github.com/o/r/issues/17#issuecomment-901" {
		t.Fatalf("bounded result = %#v", result)
	}
	for _, forbidden := range []string{"body", "type", "id", "status"} {
		if _, found := result[forbidden]; found {
			t.Fatalf("ordinary comment result exposed typed or unbounded field %q: %#v", forbidden, result)
		}
	}
}

func TestExecuteCommentCreateUsesSelfHostedRESTBackendWithoutGH(t *testing.T) {
	clearCommandAuthEnv(t)
	const body = "Please confirm whether the compatibility window includes v1 clients.\n"
	requests := 0
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodPost || r.URL.Path != "/api/v3/repos/acme/widgets/issues/23/comments" {
			t.Fatalf("request = %s %s", r.Method, r.URL.String())
		}
		if got := r.Header.Get("Authorization"); got != "Bearer self-hosted-comment-secret" {
			t.Fatalf("authorization = %q", got)
		}
		var input map[string]string
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatal(err)
		}
		if len(input) != 1 || input["body"] != body {
			t.Fatalf("ordinary comment payload = %#v", input)
		}
		if model.IsLikelyTyped(input["body"]) {
			t.Fatalf("ordinary clarification was converted into a typed artifact: %q", input["body"])
		}
		_ = json.NewEncoder(w).Encode(github.Comment{ID: 2301,
			HTMLURL: server.URL + "/acme/widgets/issues/23#issuecomment-2301",
			URL:     server.URL + "/api/v3/repos/acme/widgets/issues/comments/2301",
			Body:    input["body"],
		})
	}))
	t.Cleanup(server.Close)
	profile := auth.Profile{Name: "comment-e2e", Kind: auth.ProfileKindHosted,
		APIURL: server.URL + "/api/v3", NativeAPIURL: server.URL + "/api/v1", WebURL: server.URL,
		ServerInstanceID: "comment-e2e-instance"}
	if err := auth.SaveProfile(profile, true); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.StoreProfileToken(t.Context(), profile, "self-hosted-comment-secret", true); err != nil {
		t.Fatal(err)
	}
	oldGHAuthenticated := ghAuthenticated
	t.Cleanup(func() { ghAuthenticated = oldGHAuthenticated })
	ghAuthenticated = func(context.Context, string) error {
		t.Fatal("self-hosted ordinary comment write probed gh authentication")
		return nil
	}

	var out, errOut bytes.Buffer
	code := Execute([]string{"--profile", "comment-e2e", "comment", "create", "--repo", "acme/widgets",
		"--issue", "23", "--body-file", "-", "--json"}, strings.NewReader(body), &out, &errOut)
	if code != 0 {
		t.Fatalf("self-hosted comment create exit=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	if requests != 1 || strings.Contains(out.String()+errOut.String(), body) ||
		strings.Contains(out.String()+errOut.String(), "self-hosted-comment-secret") {
		t.Fatalf("requests=%d stdout=%q stderr=%q", requests, out.String(), errOut.String())
	}
	var result struct {
		OK        bool   `json:"ok"`
		CommentID int64  `json:"comment_id"`
		URL       string `json:"url"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.OK || result.CommentID != 2301 || result.URL != server.URL+"/acme/widgets/issues/23#issuecomment-2301" {
		t.Fatalf("self-hosted result = %+v", result)
	}

	out.Reset()
	errOut.Reset()
	code = Execute([]string{"--profile", "comment-e2e", "comment", "create", "--repo", "acme/widgets",
		"--issue", "https://attacker.example.test/acme/widgets/issues/23", "--body-file", "-", "--json"},
		strings.NewReader(body), &out, &errOut)
	if code != 2 || !strings.Contains(errOut.String(), "does not match selected issue backend host") {
		t.Fatalf("mismatched issue host exit=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	if requests != 1 {
		t.Fatalf("mismatched issue host reached selected backend; requests=%d", requests)
	}
}

func TestCommentCreateRejectsEmptyBodyBeforeBackendSelection(t *testing.T) {
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(" \n\t"), &out, &errOut)
	app.selectGitHubBackend = func(context.Context, string) (auth.GitHubBackendSelection, error) {
		t.Fatal("empty ordinary comment selected a backend")
		return auth.GitHubBackendSelection{}, nil
	}
	if code := app.runCommentCreate(t.Context(), []string{"--repo", "o/r", "--issue", "1", "--body-file", "-"}); code != 2 ||
		!strings.Contains(errOut.String(), "--body-file must not be empty") {
		t.Fatalf("empty body exit/result unexpected: stdout=%q stderr=%q", out.String(), errOut.String())
	}
}

func TestCommentEditAndDeleteUseBoundedSelectedBackendContracts(t *testing.T) {
	const body = "Replacement body that must not appear in command output.\n"
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(body), &out, &errOut)
	app.selectGitHubBackend = ghSelection
	app.newGitHubBackend = newFakeBackend(func(fake *fakeGitHubBackend) {
		fake.updateComment = func(_ context.Context, repo string, commentID int64, gotBody string) (github.Comment, error) {
			if repo != "o/r" || commentID != 71 || gotBody != body {
				t.Fatalf("UpdateComment repo=%q comment=%d body=%q", repo, commentID, gotBody)
			}
			return github.Comment{ID: commentID, Body: gotBody}, nil
		}
	})
	if code := app.runComment(t.Context(), []string{"edit", "--repo", "o/r", "--comment-id", "71", "--body-file", "-", "--json"}); code != 0 {
		t.Fatalf("comment edit exit=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	var edited map[string]any
	if err := json.Unmarshal(out.Bytes(), &edited); err != nil {
		t.Fatal(err)
	}
	if len(edited) != 4 || edited["ok"] != true || edited["action"] != "edited" ||
		edited["repository"] != "o/r" || edited["comment_id"] != float64(71) ||
		strings.Contains(out.String()+errOut.String(), body) {
		t.Fatalf("bounded edit output=%#v stderr=%q", edited, errOut.String())
	}

	out.Reset()
	errOut.Reset()
	app = newApp(strings.NewReader(""), &out, &errOut)
	app.selectGitHubBackend = ghSelection
	app.newGitHubBackend = func(_ context.Context, selection auth.GitHubBackendSelection) (github.Backend, error) {
		return deletingFakeBackend{
			fakeGitHubBackend: fakeGitHubBackend{info: github.BackendInfo{Name: selection.Name, Kind: selection.Kind, Host: selection.Host}},
			deleteComment: func(_ context.Context, repo string, commentID int64) error {
				if repo != "o/r" || commentID != 71 {
					t.Fatalf("DeleteComment repo=%q comment=%d", repo, commentID)
				}
				return nil
			},
		}, nil
	}
	if code := app.runComment(t.Context(), []string{"delete", "--repo", "o/r", "--comment-id", "71", "--json"}); code != 0 {
		t.Fatalf("comment delete exit=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	var deleted map[string]any
	if err := json.Unmarshal(out.Bytes(), &deleted); err != nil {
		t.Fatal(err)
	}
	if len(deleted) != 4 || deleted["ok"] != true || deleted["action"] != "deleted" ||
		deleted["repository"] != "o/r" || deleted["comment_id"] != float64(71) {
		t.Fatalf("bounded delete output=%#v", deleted)
	}
}

func TestCommentEditDeleteValidateBeforeBackendSelectionAndRejectUnsupportedDelete(t *testing.T) {
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader("body"), &out, &errOut)
	app.selectGitHubBackend = func(context.Context, string) (auth.GitHubBackendSelection, error) {
		t.Fatal("invalid comment mutation selected a backend")
		return auth.GitHubBackendSelection{}, nil
	}
	if code := app.runComment(t.Context(), []string{"edit", "--repo", "o/r", "--comment-id", "0", "--body-file", "-"}); code != 2 ||
		!strings.Contains(errOut.String(), "--comment-id must be positive") {
		t.Fatalf("invalid edit exit=%d stderr=%q", code, errOut.String())
	}
	errOut.Reset()
	if code := app.runComment(t.Context(), []string{"delete", "--repo", "o/r", "--comment-id", "-1"}); code != 2 ||
		!strings.Contains(errOut.String(), "--comment-id must be positive") {
		t.Fatalf("invalid delete exit=%d stderr=%q", code, errOut.String())
	}

	out.Reset()
	errOut.Reset()
	app = newApp(strings.NewReader(" \n"), &out, &errOut)
	app.selectGitHubBackend = func(context.Context, string) (auth.GitHubBackendSelection, error) {
		t.Fatal("empty edit selected a backend")
		return auth.GitHubBackendSelection{}, nil
	}
	if code := app.runComment(t.Context(), []string{"edit", "--repo", "o/r", "--comment-id", "1", "--body-file", "-"}); code != 2 ||
		!strings.Contains(errOut.String(), "--body-file must not be empty") {
		t.Fatalf("empty edit exit=%d stderr=%q", code, errOut.String())
	}

	out.Reset()
	errOut.Reset()
	app = newApp(strings.NewReader(""), &out, &errOut)
	app.selectGitHubBackend = ghSelection
	app.newGitHubBackend = newFakeBackend(nil)
	if code := app.runComment(t.Context(), []string{"delete", "--repo", "o/r", "--comment-id", "1"}); code != 1 ||
		!strings.Contains(errOut.String(), github.ErrIssueCommentDeleteUnsupported.Error()) {
		t.Fatalf("unsupported delete exit=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
}

func TestExecuteCommentEditDeleteUseSelfHostedRESTWithoutGH(t *testing.T) {
	clearCommandAuthEnv(t)
	const (
		body  = "Self-hosted replacement body.\n"
		token = "self-hosted-mutation-secret"
	)
	var methods []string
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/repos/acme/widgets/issues/comments/2301" {
			t.Fatalf("request path = %s", r.URL.String())
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+token {
			t.Fatalf("authorization = %q", got)
		}
		methods = append(methods, r.Method)
		switch r.Method {
		case http.MethodPatch:
			var input map[string]string
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatal(err)
			}
			if input["body"] != body {
				t.Fatalf("edit body = %q", input["body"])
			}
			_ = json.NewEncoder(w).Encode(github.Comment{ID: 2301, Body: input["body"]})
		case http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("request method = %s", r.Method)
		}
	}))
	t.Cleanup(server.Close)
	profile := auth.Profile{Name: "comment-mutations", Kind: auth.ProfileKindHosted,
		APIURL: server.URL + "/api/v3", NativeAPIURL: server.URL + "/api/v1", WebURL: server.URL,
		ServerInstanceID: "comment-mutations-instance"}
	if err := auth.SaveProfile(profile, true); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.StoreProfileToken(t.Context(), profile, token, true); err != nil {
		t.Fatal(err)
	}
	oldGHAuthenticated := ghAuthenticated
	t.Cleanup(func() { ghAuthenticated = oldGHAuthenticated })
	ghAuthenticated = func(context.Context, string) error {
		t.Fatal("self-hosted comment mutation probed gh authentication")
		return nil
	}

	var out, errOut bytes.Buffer
	if code := Execute([]string{"--profile", profile.Name, "comment", "edit", "--repo", "acme/widgets",
		"--comment-id", "2301", "--body-file", "-", "--json"}, strings.NewReader(body), &out, &errOut); code != 0 {
		t.Fatalf("self-hosted edit exit=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	if strings.Contains(out.String()+errOut.String(), body) || strings.Contains(out.String()+errOut.String(), token) {
		t.Fatalf("edit leaked body or token stdout=%q stderr=%q", out.String(), errOut.String())
	}
	out.Reset()
	errOut.Reset()
	if code := Execute([]string{"--profile", profile.Name, "comment", "delete", "--repo", "acme/widgets",
		"--comment-id", "2301", "--json"}, strings.NewReader(""), &out, &errOut); code != 0 {
		t.Fatalf("self-hosted delete exit=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	if strings.Contains(out.String()+errOut.String(), body) || strings.Contains(out.String()+errOut.String(), token) {
		t.Fatalf("delete leaked body or token stdout=%q stderr=%q", out.String(), errOut.String())
	}
	if fmt.Sprint(methods) != fmt.Sprint([]string{http.MethodPatch, http.MethodDelete}) {
		t.Fatalf("methods = %#v", methods)
	}
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

func TestCommentGenerateSpecRendersStrictDurableIntent(t *testing.T) {
	input := strings.TrimSuffix(specInputJSON, "\n}") + `,
  "durable": {
    "version": 1,
    "intent": "OPERATIONS",
    "operations": [
      {
        "id": "SPEC-001-OP-01",
        "kind": "MODIFIED",
        "capability": "canonical-comments",
        "path": "issue-spec/specs/canonical-comments/spec.md",
        "current_requirement": "canonical SPEC comments",
        "projection": {"source": "current-spec"}
      }
    ]
  }
}`
	inPath := writeTempInput(t, input)
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	code := app.runCommentGenerate(context.Background(), []string{
		"--type", "SPEC", "--id", "SPEC-001", "--status", "confirmed", "--input-file", inPath,
	})
	if code != 0 || !strings.Contains(out.String(), "## Durable Intent") || !strings.Contains(out.String(), `"source": "current-spec"`) {
		t.Fatalf("generate durable SPEC exit=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	if _, found, err := model.ParseSpecDurableIntent("SPEC-001", out.String(), "."); err != nil || !found {
		t.Fatalf("generated durable intent found=%t err=%v", found, err)
	}
}

func TestCommentGenerateRejectsUnknownDurableFieldsAndMissingLegacyTarget(t *testing.T) {
	unknown := strings.TrimSuffix(specInputJSON, "\n}") + `,
  "durable": {"version":1,"intent":"UNCHANGED","plugin":"execute"}
}`
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	if code := app.runCommentGenerate(context.Background(), []string{"--type", "SPEC", "--id", "SPEC-001", "--input-file", writeTempInput(t, unknown)}); code == 0 || !strings.Contains(errOut.String(), "unknown field") {
		t.Fatalf("unknown durable field exit=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}

	legacy := strings.TrimSuffix(specInputJSON, "\n}") + `,
  "durable": {"version":1,"intent":"OPERATIONS","operations":[{
    "id":"SPEC-001-OP-01","kind":"REMOVED","capability":"definitely-missing",
    "path":"openspec/specs/definitely-missing/spec.md","current_requirement":"legacy"
  }]}
}`
	out.Reset()
	errOut.Reset()
	if code := app.runCommentGenerate(context.Background(), []string{"--type", "SPEC", "--id", "SPEC-001", "--input-file", writeTempInput(t, legacy)}); code == 0 || !strings.Contains(errOut.String(), "does not already exist") {
		t.Fatalf("missing legacy target exit=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
}

func TestCommentGenerateRequiresDurableIntentOnlyForConfirmedRepositoryMode(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "issue-spec"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "issue-spec", "config.yaml"), []byte("durable_specs:\n  mode: repository\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)

	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	args := []string{"--type", "SPEC", "--id", "SPEC-001", "--status", "confirmed", "--input-file", writeTempInput(t, specInputJSON)}
	if code := app.runCommentGenerate(context.Background(), args); code == 0 || !strings.Contains(errOut.String(), "requires durable input") {
		t.Fatalf("confirmed repository SPEC exit=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}

	out.Reset()
	errOut.Reset()
	args[5] = "draft"
	if code := app.runCommentGenerate(context.Background(), args); code != 0 {
		t.Fatalf("draft repository SPEC exit=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}

	out.Reset()
	errOut.Reset()
	args[5] = "confirmed"
	unchanged := strings.TrimSuffix(specInputJSON, "\n}") + `,
  "durable": {"version":1,"intent":"UNCHANGED"}
}`
	args[7] = writeTempInput(t, unchanged)
	if code := app.runCommentGenerate(context.Background(), args); code != 0 || !strings.Contains(out.String(), "## Durable Intent") {
		t.Fatalf("confirmed durable SPEC exit=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
}

func TestWorkflowSpecTemplateCannotInventDurableAuthority(t *testing.T) {
	body, err := model.EnsureTypedBody("SPEC", "SPEC-001",
		"## Requirement: canonical SPEC comments\n\nThe CLI MUST render comments.\n\n### Scenario: render\n\n- **WHEN** input exists\n- **THEN** output exists\n\n## Durable Intent\n\n```json\n{\"version\":1,\"intent\":\"UNCHANGED\"}\n```",
		model.BodyOptions{Status: "draft"})
	if err != nil {
		t.Fatal(err)
	}
	if err := validateGeneratedSpecDurablePolicy(workflow.Plan{}, "SPEC-001", "draft", specInputJSON, body); err == nil ||
		!strings.Contains(err.Error(), "introduced Durable Intent") {
		t.Fatalf("workflow template authority error = %v", err)
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

func TestCommentGenerateProcessExecutionClass(t *testing.T) {
	inPath := writeTempInput(t, `{"title":"review","parent_task":"TASK-005","execution_class":"review"}`)
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	code := app.runCommentGenerate(context.Background(), []string{"--type", "PROCESS", "--id", "PROCESS-008", "--input-file", inPath})
	if code != 0 || !strings.Contains(out.String(), "### Execution Class\n\n- review") {
		t.Fatalf("generate exit=%d stderr=%q body=%q", code, errOut.String(), out.String())
	}

	badPath := writeTempInput(t, `{"title":"bad","parent_task":"TASK-005","execution_class":"deploy"}`)
	out.Reset()
	errOut.Reset()
	code = app.runCommentGenerate(context.Background(), []string{"--type", "PROCESS", "--id", "PROCESS-008", "--input-file", badPath})
	if code == 0 || !strings.Contains(errOut.String(), "unknown PROCESS execution class") {
		t.Fatalf("unknown class should fail generation: exit=%d stderr=%q", code, errOut.String())
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

func TestCommentListDefaultJSONContractRemainsCompatible(t *testing.T) {
	body, err := model.EnsureTypedBody("QUESTION", "QUESTION-007", "## Question\n\nShould the default JSON contract remain unchanged?\n", model.BodyOptions{
		Agent:  "Compatibility Worker",
		Status: "confirmed",
		Scope:  "comment list output",
	})
	if err != nil {
		t.Fatal(err)
	}
	comment := github.Comment{
		ID:      71,
		HTMLURL: "https://github.com/o/r/issues/5#issuecomment-71",
		URL:     "https://api.github.com/repos/o/r/issues/comments/71",
		Body:    body,
	}
	var out bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &bytes.Buffer{})
	app.selectGitHubBackend = ghSelection
	app.newGitHubBackend = newFakeBackend(func(f *fakeGitHubBackend) {
		f.listIssueComments = func(context.Context, string, int) ([]github.Comment, error) {
			return []github.Comment{comment}, nil
		}
	})

	code := app.runCommentList(context.Background(), []string{"--repo", "o/r", "--issue", "5", "--json"})
	if code != 0 {
		t.Fatalf("list failed exit=%d out=%q", code, out.String())
	}
	artifact := model.Artifact{Issue: 5, CommentID: comment.ID, URL: comment.HTMLURL, APIURL: comment.URL, Comment: model.ParseTypedComment(body)}
	artifact.Canonical = model.ValidateArtifact(artifact)
	want, err := json.MarshalIndent(map[string]any{"ok": true, "issue": 5, "comments": []model.Artifact{artifact}}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want = append(want, '\n')
	if !bytes.Equal(out.Bytes(), want) {
		t.Fatalf("default JSON changed\nwant:\n%s\ngot:\n%s", want, out.Bytes())
	}

	var raw struct {
		Comments []map[string]json.RawMessage `json:"comments"`
	}
	if err := json.Unmarshal(out.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if _, found := raw.Comments[0]["body"]; found {
		t.Fatalf("default artifact unexpectedly exposes body: %s", out.String())
	}
	var parsedComment map[string]json.RawMessage
	if err := json.Unmarshal(raw.Comments[0]["comment"], &parsedComment); err != nil {
		t.Fatal(err)
	}
	if _, found := parsedComment["body"]; found {
		t.Fatalf("default typed comment unexpectedly exposes body: %s", out.String())
	}
}

func TestCommentListIncludeBodyPreservesExactMarkdownFilteringAndDiagnostics(t *testing.T) {
	specBody, err := model.EnsureTypedBody("SPEC", "SPEC-007", "## Requirement: Exact Markdown\n\nThe CLI MUST preserve `code`, 中文, and trailing whitespace.  \n\n", model.BodyOptions{
		Agent:  "Body Worker",
		Status: "confirmed",
		Scope:  "lossless output",
	})
	if err != nil {
		t.Fatal(err)
	}
	taskBody, err := model.EnsureTypedBody("TASK", "TASK-007", "## Task: excluded\n\n- [ ] Keep filtering\n\n### Execution Planning\n\n- Owned Areas: command tests\n", model.BodyOptions{Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &bytes.Buffer{})
	app.selectGitHubBackend = ghSelection
	app.newGitHubBackend = newFakeBackend(func(f *fakeGitHubBackend) {
		f.listIssueComments = func(context.Context, string, int) ([]github.Comment, error) {
			return []github.Comment{
				{ID: 70, HTMLURL: "https://github.com/o/r/issues/5#issuecomment-70", Body: taskBody},
				{ID: 71, HTMLURL: "https://github.com/o/r/issues/5#issuecomment-71", Body: specBody},
			}, nil
		}
	})

	code := app.runCommentList(context.Background(), []string{"--repo", "o/r", "--issue", "5", "--type", "spec", "--json", "--include-body"})
	if code != 0 {
		t.Fatalf("list failed exit=%d out=%q", code, out.String())
	}
	var got struct {
		Comments []struct {
			model.Artifact
			Body string `json:"body"`
		} `json:"comments"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Comments) != 1 || got.Comments[0].Comment.ID != "SPEC-007" {
		t.Fatalf("type filtering changed in include-body mode: %+v", got.Comments)
	}
	if got.Comments[0].Body != specBody {
		t.Fatalf("body did not round-trip exactly\nwant: %q\ngot:  %q", specBody, got.Comments[0].Body)
	}
	if len(got.Comments[0].Canonical) == 0 {
		t.Fatalf("include-body mode dropped canonical diagnostics: %+v", got.Comments[0])
	}
}

func TestCommentListIncludeBodyRequiresJSONBeforeBackendSelection(t *testing.T) {
	var out, errOut bytes.Buffer
	backendSelected := false
	app := newApp(strings.NewReader(""), &out, &errOut)
	app.selectGitHubBackend = func(context.Context, string) (auth.GitHubBackendSelection, error) {
		backendSelected = true
		return ghSelection(context.Background(), "github.com")
	}

	code := app.runCommentList(context.Background(), []string{"--repo", "o/r", "--issue", "5", "--include-body"})
	if code != 2 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	if backendSelected {
		t.Fatal("invalid flag combination selected a backend")
	}
	if !strings.Contains(errOut.String(), "--include-body requires --json") {
		t.Fatalf("unexpected error: %q", errOut.String())
	}
}

func TestCommentGetUsesDirectObservationAndReturnsExactBoundedProjection(t *testing.T) {
	links := make([]string, 12)
	for i := range links {
		links[i] = fmt.Sprintf("https://github.com/o/r/issues/8#issuecomment-%02d", i+1)
	}
	body, err := model.EnsureTypedBody("QUESTION", "QUESTION-007", "## Question\n\nKeep reads bounded?  \n", model.BodyOptions{
		Agent: "Reader", Status: "confirmed", Scope: "targeted reads",
		Links: map[string][]string{"Related Comments": links},
	})
	if err != nil {
		t.Fatal(err)
	}
	comment := github.Comment{
		ID: 77, HTMLURL: "https://github.com/o/r/issues/5#issuecomment-77",
		URL: "https://api.github.com/repos/o/r/issues/comments/77", IssueURL: "https://api.github.com/repos/o/r/issues/5", Body: body,
	}
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	app.selectGitHubBackend = ghSelection
	app.newGitHubBackend = func(_ context.Context, selection auth.GitHubBackendSelection) (github.Backend, error) {
		base := fakeGitHubBackend{
			info: github.BackendInfo{Name: selection.Name, Kind: selection.Kind, Host: selection.Host},
			listIssueComments: func(context.Context, string, int) ([]github.Comment, error) {
				t.Fatal("direct observation unexpectedly scanned the issue timeline")
				return nil, nil
			},
		}
		return observingFakeBackend{fakeGitHubBackend: base,
			observeIssueComment: func(_ context.Context, repo string, id int64) (github.IssueCommentObservation, error) {
				if repo != "o/r" || id != 77 {
					t.Fatalf("observe repo=%q id=%d", repo, id)
				}
				return github.IssueCommentObservation{Comment: comment, RepresentationVersion: 14}, nil
			}}, nil
	}
	code := app.runCommentGet(t.Context(), []string{"--repo", "o/r", "--issue", "5", "--id", "QUESTION-007", "--type", "QUESTION", "--comment-id", "77", "--include-body", "--json"})
	if code != 0 {
		t.Fatalf("get exit=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	var got struct {
		OK      bool                `json:"ok"`
		Comment commentReadArtifact `json:"comment"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.OK || got.Comment.CommentID != 77 || got.Comment.ID != "QUESTION-007" || got.Comment.Status != "confirmed" || got.Comment.RepresentationVersion != 14 {
		t.Fatalf("targeted result = %+v", got)
	}
	if got.Comment.Body != body || got.Comment.RepresentationDigest != model.RepresentationDigest(body) {
		t.Fatalf("exact representation was not preserved: %+v", got.Comment)
	}
	related := got.Comment.Links["Related Comments"]
	if related.Count != 12 || len(related.Items) != 10 || related.TruncatedCount != 2 {
		t.Fatalf("bounded related links = %+v", related)
	}

	out.Reset()
	errOut.Reset()
	code = app.runCommentGet(t.Context(), []string{"--repo", "o/r", "--issue", "5", "--id", "QUESTION-007", "--comment-id", "77", "--include-all-links", "--json"})
	if code != 0 {
		t.Fatalf("full-link get exit=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	related = got.Comment.Links["Related Comments"]
	if related.Count != 12 || len(related.Items) != 12 || related.TruncatedCount != 0 {
		t.Fatalf("full related links = %+v", related)
	}
}

func TestCommentGetFallsBackToScanButReturnsOnlyTarget(t *testing.T) {
	target, err := model.EnsureTypedBody("QUESTION", "QUESTION-001", "## Question\n\nTarget?\n", model.BodyOptions{Status: "confirmed"})
	if err != nil {
		t.Fatal(err)
	}
	unrelated, err := model.EnsureTypedBody("QUESTION", "QUESTION-002", "## Question\n\nUNRELATED-BODY\n", model.BodyOptions{Status: "confirmed"})
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	app.selectGitHubBackend = ghSelection
	app.newGitHubBackend = newFakeBackend(func(f *fakeGitHubBackend) {
		f.listIssueComments = func(context.Context, string, int) ([]github.Comment, error) {
			return []github.Comment{
				{ID: 1, HTMLURL: "https://github.com/o/r/issues/5#issuecomment-1", Body: target},
				{ID: 2, HTMLURL: "https://github.com/o/r/issues/5#issuecomment-2", Body: unrelated},
			}, nil
		}
	})
	code := app.runCommentGet(t.Context(), []string{"--repo", "o/r", "--issue", "5", "--id", "QUESTION-001", "--include-body", "--json"})
	if code != 0 {
		t.Fatalf("get exit=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	if strings.Contains(out.String(), "UNRELATED-BODY") || !strings.Contains(out.String(), "Target?") {
		t.Fatalf("targeted output leaked or omitted a body: %s", out.String())
	}
}

func TestCommentGetFailsClosedOnDuplicateAndDirectMismatch(t *testing.T) {
	body, err := model.EnsureTypedBody("QUESTION", "QUESTION-001", "## Question\n\nDuplicate?\n", model.BodyOptions{Status: "confirmed"})
	if err != nil {
		t.Fatal(err)
	}
	t.Run("duplicate scan", func(t *testing.T) {
		var out, errOut bytes.Buffer
		app := newApp(strings.NewReader(""), &out, &errOut)
		app.selectGitHubBackend = ghSelection
		app.newGitHubBackend = newFakeBackend(func(f *fakeGitHubBackend) {
			f.listIssueComments = func(context.Context, string, int) ([]github.Comment, error) {
				return []github.Comment{{ID: 1, Body: body}, {ID: 2, Body: body}}, nil
			}
		})
		if code := app.runCommentGet(t.Context(), []string{"--repo", "o/r", "--issue", "5", "--id", "QUESTION-001", "--json"}); code != 1 || !strings.Contains(errOut.String(), "duplicate") {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
		}
	})

	t.Run("direct marker mismatch", func(t *testing.T) {
		var out, errOut bytes.Buffer
		app := newApp(strings.NewReader(""), &out, &errOut)
		app.selectGitHubBackend = ghSelection
		app.newGitHubBackend = func(_ context.Context, selection auth.GitHubBackendSelection) (github.Backend, error) {
			base := fakeGitHubBackend{info: github.BackendInfo{Name: selection.Name, Kind: selection.Kind, Host: selection.Host}}
			return observingFakeBackend{fakeGitHubBackend: base,
				observeIssueComment: func(context.Context, string, int64) (github.IssueCommentObservation, error) {
					return github.IssueCommentObservation{Comment: github.Comment{
						ID: 1, IssueURL: "https://api.github.com/repos/o/r/issues/5", Body: body,
					}}, nil
				}}, nil
		}
		if code := app.runCommentGet(t.Context(), []string{"--repo", "o/r", "--issue", "5", "--id", "QUESTION-999", "--comment-id", "1", "--json"}); code != 1 || !strings.Contains(errOut.String(), "id mismatch") {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
		}
	})
}

func TestCommentListExplicitActiveStatusAndHistoryModes(t *testing.T) {
	makeBody := func(id, status, text string) string {
		body, err := model.EnsureTypedBody("QUESTION", id, "## Question\n\n"+text+"\n", model.BodyOptions{Status: status})
		if err != nil {
			t.Fatal(err)
		}
		return body
	}
	noncanonical, err := model.EnsureTypedBody("SPEC", "SPEC-004", "# Legacy body\n", model.BodyOptions{Status: "confirmed"})
	if err != nil {
		t.Fatal(err)
	}
	comments := []github.Comment{
		{ID: 1, HTMLURL: "https://example/1", Body: makeBody("QUESTION-001", "confirmed", "Confirmed")},
		{ID: 2, HTMLURL: "https://example/2", Body: makeBody("QUESTION-002", "done", "Done")},
		{ID: 3, HTMLURL: "https://example/3", Body: makeBody("QUESTION-003", "superseded", "Historical")},
		{ID: 4, HTMLURL: "https://example/4", Body: noncanonical},
	}
	run := func(t *testing.T, args ...string) []commentReadArtifact {
		t.Helper()
		var out, errOut bytes.Buffer
		app := newApp(strings.NewReader(""), &out, &errOut)
		app.selectGitHubBackend = ghSelection
		app.newGitHubBackend = newFakeBackend(func(f *fakeGitHubBackend) {
			f.listIssueComments = func(context.Context, string, int) ([]github.Comment, error) { return comments, nil }
		})
		base := []string{"--repo", "o/r", "--issue", "5", "--json"}
		if code := app.runCommentList(t.Context(), append(base, args...)); code != 0 {
			t.Fatalf("list exit=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
		}
		var got struct {
			Comments []commentReadArtifact `json:"comments"`
		}
		if err := json.Unmarshal(out.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		return got.Comments
	}
	active := run(t, "--active-only")
	if len(active) != 2 || active[0].Status != "confirmed" || active[1].Status != "done" {
		t.Fatalf("active comments = %+v", active)
	}
	done := run(t, "--status", "done")
	if len(done) != 1 || done[0].ID != "QUESTION-002" {
		t.Fatalf("status comments = %+v", done)
	}
	history := run(t, "--history", "--include-body")
	if len(history) != 1 || history[0].ID != "QUESTION-003" || !strings.Contains(history[0].Body, "Historical") {
		t.Fatalf("history comments = %+v", history)
	}
}

func TestCommentListProcessActivityUsesExplicitReplacementSelection(t *testing.T) {
	makeProcessBody := func(id, status, title string) string {
		body, err := model.EnsureTypedBody("PROCESS", id,
			"## Process: "+title+"\n\n### Parent TASK\n\n- TASK-001\n",
			model.BodyOptions{Agent: "Compatibility Worker", Status: status, Scope: "manual role-owned evidence"})
		if err != nil {
			t.Fatal(err)
		}
		return body
	}
	const (
		historicalURL = "https://example.test/issues/5#issuecomment-11"
		currentURL    = "https://example.test/issues/5#issuecomment-12"
		legacyURL     = "https://example.test/issues/5#issuecomment-13"
	)
	currentBody := makeProcessBody("PROCESS-002", "done", "current")
	historicalBody := makeProcessBody("PROCESS-001", "superseded", "historical source")
	var err error
	historicalBody, _, err = model.StampSupersededBy(historicalBody, "PROCESS-001",
		model.SupersededBy{ProcessID: "PROCESS-002", URL: currentURL})
	if err != nil {
		t.Fatal(err)
	}
	legacyBody := makeProcessBody("PROCESS-003", "superseded", "legacy status-only")
	comments := []github.Comment{
		{ID: 11, HTMLURL: historicalURL, Body: historicalBody},
		{ID: 12, HTMLURL: currentURL, Body: currentBody},
		{ID: 13, HTMLURL: legacyURL, Body: legacyBody},
	}
	run := func(t *testing.T, args ...string) []commentReadArtifact {
		t.Helper()
		var out, errOut bytes.Buffer
		app := newApp(strings.NewReader(""), &out, &errOut)
		app.selectGitHubBackend = ghSelection
		app.newGitHubBackend = newFakeBackend(func(f *fakeGitHubBackend) {
			f.listIssueComments = func(context.Context, string, int) ([]github.Comment, error) { return comments, nil }
		})
		base := []string{"--repo", "o/r", "--issue", "5", "--type", "PROCESS", "--json"}
		if code := app.runCommentList(t.Context(), append(base, args...)); code != 0 {
			t.Fatalf("list exit=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
		}
		var got struct {
			Comments []commentReadArtifact `json:"comments"`
		}
		if err := json.Unmarshal(out.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		return got.Comments
	}

	active := run(t, "--active-only")
	if len(active) != 2 || active[0].ID != "PROCESS-002" || active[1].ID != "PROCESS-003" {
		t.Fatalf("active PROCESS comments = %+v", active)
	}
	if active[1].Status != "superseded" {
		t.Fatalf("legacy status-only PROCESS was not retained as active: %+v", active[1])
	}
	for _, artifact := range active {
		if artifact.Body != "" {
			t.Fatalf("compact active read loaded body for %s", artifact.ID)
		}
	}

	history := run(t, "--history", "--include-body")
	if len(history) != 1 || history[0].ID != "PROCESS-001" || history[0].URL != historicalURL ||
		!strings.Contains(history[0].Body, "historical source") {
		t.Fatalf("historical PROCESS comments = %+v", history)
	}
}

func TestCommentListInvalidProcessReplacementRemainsActive(t *testing.T) {
	body, err := model.EnsureTypedBody("PROCESS", "PROCESS-001",
		"## Process: invalid replacement\n\n### Parent TASK\n\n- TASK-001\n",
		model.BodyOptions{Status: "superseded"})
	if err != nil {
		t.Fatal(err)
	}
	body += "\n<!-- issue-spec:superseded-by version=1 -->\nnot-json\n<!-- /issue-spec:superseded-by -->\n"
	comments := []github.Comment{{ID: 11, HTMLURL: "https://example.test/issues/5#issuecomment-11", Body: body}}
	run := func(t *testing.T, mode string) []commentReadArtifact {
		t.Helper()
		var out, errOut bytes.Buffer
		app := newApp(strings.NewReader(""), &out, &errOut)
		app.selectGitHubBackend = ghSelection
		app.newGitHubBackend = newFakeBackend(func(f *fakeGitHubBackend) {
			f.listIssueComments = func(context.Context, string, int) ([]github.Comment, error) { return comments, nil }
		})
		if code := app.runCommentList(t.Context(), []string{"--repo", "o/r", "--issue", "5", "--type", "PROCESS", "--json", mode}); code != 0 {
			t.Fatalf("list exit=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
		}
		var got struct {
			Comments []commentReadArtifact `json:"comments"`
		}
		if err := json.Unmarshal(out.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		return got.Comments
	}
	if active := run(t, "--active-only"); len(active) != 1 || active[0].ID != "PROCESS-001" {
		t.Fatalf("invalid replacement PROCESS was hidden from active view: %+v", active)
	}
	if history := run(t, "--history"); len(history) != 0 {
		t.Fatalf("invalid replacement PROCESS became authoritative history: %+v", history)
	}
}

func TestCommentListJSONEmptyResultsAreArrays(t *testing.T) {
	for _, tt := range []struct {
		name string
		args []string
	}{
		{name: "default", args: []string{"--repo", "o/r", "--issue", "5", "--json"}},
		{name: "include body", args: []string{"--repo", "o/r", "--issue", "5", "--json", "--include-body"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			app := newApp(strings.NewReader(""), &out, &bytes.Buffer{})
			app.selectGitHubBackend = ghSelection
			app.newGitHubBackend = newFakeBackend(func(f *fakeGitHubBackend) {
				f.listIssueComments = func(context.Context, string, int) ([]github.Comment, error) {
					return nil, nil
				}
			})

			if code := app.runCommentList(context.Background(), tt.args); code != 0 {
				t.Fatalf("list failed exit=%d out=%q", code, out.String())
			}
			var got struct {
				Comments []json.RawMessage `json:"comments"`
			}
			if err := json.Unmarshal(out.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.Comments == nil || len(got.Comments) != 0 {
				t.Fatalf("empty comments must be []: %s", out.String())
			}
		})
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

func TestCommentUpsertCoversIssueWritesOnlyTaskOwnedForwardLinks(t *testing.T) {
	taskBody := generateTaskBody(t, "TASK-001", taskCoversInput(`"SPEC-001"`))
	bodyPath := writeTempInput(t, taskBody)
	specBody := generateCanonicalSpecBody(t)
	specURL := "https://github.com/o/r/issues/100#issuecomment-501"
	taskURL := "https://github.com/o/r/issues/5#issuecomment-9"

	var createdTask string
	specUpdateCalls := 0
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
				specUpdateCalls++
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
	if specUpdateCalls != 0 {
		t.Fatalf("TASK coverage must cause zero SPEC fan-out writes, got %d", specUpdateCalls)
	}
	arts := []model.Artifact{
		{Issue: 5, CommentID: 9, URL: taskURL, Comment: model.ParseTypedComment(createdTask)},
		{Issue: 100, CommentID: 501, URL: specURL, Comment: model.ParseTypedComment(specBody)},
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

func TestCommentUpsertCoversIssueRejectsNonTaskType(t *testing.T) {
	// FINDING-002: --covers-issue only means something for a TASK; a wrong --type
	// must fail loudly instead of writing a link-less comment that looks successful.
	bodyPath := writeTempInput(t, generateCanonicalSpecBody(t))
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	// No backend override: it must reject before any client call.
	code := app.runCommentUpsert(context.Background(), []string{"--repo", "o/r", "--issue", "5", "--type", "SPEC", "--id", "SPEC-001", "--body-file", bodyPath, "--covers-issue", "100"})
	if code != 2 {
		t.Fatalf("expected exit 2 for --covers-issue on non-TASK, got %d stderr=%q", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "--covers-issue only applies to --type TASK") {
		t.Fatalf("rejection should explain the constraint:\n%s", errOut.String())
	}
}

func TestCommentUpsertReportsDroppedLinksWhenBodyLacksLinksBlock(t *testing.T) {
	// SPEC-003 end-to-end: the drop-warning wiring is reachable. A --allow-noncanonical
	// TASK body that carries a header but no Links block cannot absorb the existing
	// comment's Related Comments link on update, so the link is dropped and reported.
	peer := "https://github.com/o/r/issues/9#issuecomment-101"
	existing, changed, err := model.AddRelatedCommentLink(generateTaskBody(t, "TASK-001", taskCoversInput(``)), peer)
	if err != nil || !changed {
		t.Fatalf("seed existing link: changed=%v err=%v", changed, err)
	}
	// Header present (so EnsureTypedBody keeps it as-is) but no Links block.
	linkless := "Agent: Coordinator\nType: TASK\nID: TASK-001\nStatus: draft\nScope: N/A\n\n## Summary\n\nnoncanonical task without a Links block\n"
	bodyPath := writeTempInput(t, linkless)

	var out bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &bytes.Buffer{})
	app.selectGitHubBackend = ghSelection
	app.newGitHubBackend = newFakeBackend(func(f *fakeGitHubBackend) {
		f.listIssueComments = func(context.Context, string, int) ([]github.Comment, error) {
			return []github.Comment{{ID: 7, HTMLURL: "https://github.com/o/r/issues/5#issuecomment-7", Body: existing}}, nil
		}
		f.updateComment = func(_ context.Context, _ string, _ int64, _ string) (github.Comment, error) {
			return github.Comment{ID: 7, HTMLURL: "https://github.com/o/r/issues/5#issuecomment-7"}, nil
		}
	})
	code := app.runCommentUpsert(context.Background(), []string{"--repo", "o/r", "--issue", "5", "--type", "TASK", "--id", "TASK-001", "--body-file", bodyPath, "--allow-noncanonical", "--json"})
	if code != 0 {
		t.Fatalf("allow-noncanonical upsert failed exit=%d out=%q", code, out.String())
	}
	var got struct {
		Dropped []string `json:"dropped_related_comments"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Dropped) != 1 || got.Dropped[0] != peer {
		t.Fatalf("expected the unabsorbable link reported as dropped, got %v", got.Dropped)
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
