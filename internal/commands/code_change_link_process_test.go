package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/codereview"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/model"
)

func TestCodeChangeLinkProcessHelpAndVersionValidation(t *testing.T) {
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	if code := app.runCodeChange(t.Context(), []string{"link-process", "--help"}); code != 0 {
		t.Fatalf("help exit = %d", code)
	}
	for _, flag := range []string{"--repo", "--implement", "--process", "--expected-version", "--hostname", "--json"} {
		if !strings.Contains(out.String(), flag) {
			t.Fatalf("help is missing %s:\n%s", flag, out.String())
		}
	}
	for _, forbidden := range []string{"\n  --pr ", "--canonical-url", "--allow-nonatomic", "--expected-digest"} {
		if strings.Contains(out.String(), forbidden) {
			t.Fatalf("help exposes forbidden flag %s:\n%s", forbidden, out.String())
		}
	}
	for _, version := range []string{"0", "-1"} {
		out.Reset()
		errOut.Reset()
		code := app.runCodeChange(t.Context(), []string{"link-process", "--repo", "acme/widgets", "--implement", "9",
			"--process", "PROCESS-006", "--expected-version", version})
		if code != 2 || !strings.Contains(errOut.String(), "--expected-version must be positive") {
			t.Fatalf("version=%s exit=%d stderr=%q", version, code, errOut.String())
		}
	}
}

func TestCodeChangeLinkProcessStrictUpdateAndIdempotentRetry(t *testing.T) {
	backend := newFakeCodeChangeBackend()
	canonical := "https://code.example/acme/widgets/changes/42"
	backend.references = []github.NativeReference{
		{RelationKind: "build", LifecycleState: "active", CanonicalURL: "https://build.example/runs/1"},
		{RelationKind: "code_change", LifecycleState: "closed", CanonicalURL: "https://code.example/acme/widgets/changes/41"},
		codeChangeLinkReference(canonical),
	}
	body := codeChangeLinkProcessBody(t, "PROCESS-006", "N/A")
	current, version, writes := body, int64(4), 0
	var written string
	base := fakeGitHubBackend{listIssueComments: func(context.Context, string, int) ([]github.Comment, error) {
		return []github.Comment{{ID: 17, HTMLURL: "https://issues.test/acme/widgets/issues/9#issuecomment-17", Body: current}}, nil
	}}
	backend.issueBackend = conditionalTransitionBackend{fakeGitHubBackend: base,
		observe: func(_ context.Context, repo string, id int64) (github.CommentRepresentation, error) {
			if repo != "acme/widgets" || id != 17 {
				t.Fatalf("observe repo=%q id=%d", repo, id)
			}
			return github.CommentRepresentation{Comment: github.Comment{ID: id, Body: current}, RepresentationVersion: version,
				Guarantee: github.CommentMutationStrictConditional}, nil
		},
		update: func(_ context.Context, repo string, id, expected int64, updated string) (github.CommentRepresentation, error) {
			writes++
			if repo != "acme/widgets" || id != 17 || expected != version {
				t.Fatalf("update repo=%q id=%d expected=%d version=%d", repo, id, expected, version)
			}
			written, current = updated, updated
			version++
			return github.CommentRepresentation{Comment: github.Comment{ID: id,
				HTMLURL: "https://issues.test/acme/widgets/issues/9#issuecomment-17", Body: updated},
				RepresentationVersion: version, Guarantee: github.CommentMutationStrictConditional}, nil
		}}
	app, out, errOut := setupCodeChangeLinkApp(t, backend)
	args := []string{"link-process", "--repo", "acme/widgets", "--implement", "9", "--process", "PROCESS-006",
		"--expected-version", "4", "--json"}
	if code := app.runCodeChange(t.Context(), args); code != 0 || errOut.Len() != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	wantBody := strings.Replace(body, "- PR: N/A", "- PR: "+canonical, 1)
	if written != wantBody || writes != 1 {
		t.Fatalf("writes=%d\nwritten:\n%s\nwant:\n%s", writes, written, wantBody)
	}
	var result codeChangeLinkProcessResult
	decodeCommandJSON(t, out.Bytes(), &result)
	if !result.OK || result.Action != "updated" || !result.Atomic ||
		result.Guarantee != github.CommentMutationStrictConditional || result.ExpectedVersion != 4 ||
		result.RepresentationVersion != 5 || result.CanonicalURL != canonical || result.Process != "PROCESS-006" {
		t.Fatalf("result = %+v", result)
	}

	out.Reset()
	errOut.Reset()
	args[len(args)-2] = "5"
	if code := app.runCodeChange(t.Context(), args); code != 0 || errOut.Len() != 0 {
		t.Fatalf("retry exit=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	decodeCommandJSON(t, out.Bytes(), &result)
	if result.Action != "unchanged" || result.RepresentationVersion != 5 || writes != 1 {
		t.Fatalf("retry result=%+v writes=%d", result, writes)
	}
}

func TestCodeChangeLinkProcessAllowsProcessesToShareRelationship(t *testing.T) {
	backend := newFakeCodeChangeBackend()
	canonical := "https://code.example/acme/widgets/changes/42"
	backend.references = []github.NativeReference{codeChangeLinkReference(canonical)}
	comments := map[int64]string{21: codeChangeLinkProcessBody(t, "PROCESS-006", "N/A"),
		22: codeChangeLinkProcessBody(t, "PROCESS-007", "N/A")}
	versions := map[int64]int64{21: 1, 22: 1}
	writes := 0
	base := fakeGitHubBackend{listIssueComments: func(context.Context, string, int) ([]github.Comment, error) {
		return []github.Comment{{ID: 21, Body: comments[21]}, {ID: 22, Body: comments[22]}}, nil
	}}
	backend.issueBackend = conditionalTransitionBackend{fakeGitHubBackend: base,
		observe: func(_ context.Context, _ string, id int64) (github.CommentRepresentation, error) {
			return github.CommentRepresentation{Comment: github.Comment{ID: id, Body: comments[id]},
				RepresentationVersion: versions[id]}, nil
		},
		update: func(_ context.Context, _ string, id, expected int64, body string) (github.CommentRepresentation, error) {
			writes++
			if expected != versions[id] {
				t.Fatalf("id=%d expected=%d current=%d", id, expected, versions[id])
			}
			comments[id], versions[id] = body, versions[id]+1
			return github.CommentRepresentation{Comment: github.Comment{ID: id, Body: body},
				RepresentationVersion: versions[id]}, nil
		}}
	app, out, _ := setupCodeChangeLinkApp(t, backend)
	for _, process := range []string{"PROCESS-006", "PROCESS-007"} {
		out.Reset()
		if code := app.runCodeChange(t.Context(), []string{"link-process", "--repo", "acme/widgets", "--implement", "9",
			"--process", process, "--expected-version", "1", "--json"}); code != 0 {
			t.Fatalf("process=%s exit=%d output=%s", process, code, out.String())
		}
	}
	if writes != 2 || !strings.Contains(comments[21], "- PR: "+canonical) || !strings.Contains(comments[22], "- PR: "+canonical) {
		t.Fatalf("writes=%d comments=%v", writes, comments)
	}
}

func TestCodeChangeLinkProcessRequiresExactlyOneActiveRelationship(t *testing.T) {
	canonical := "https://code.example/acme/widgets/changes/42"
	tests := []struct {
		name     string
		refs     []github.NativeReference
		wantCode string
	}{
		{name: "missing", refs: []github.NativeReference{{RelationKind: "code_change", LifecycleState: "closed",
			CanonicalURL: canonical}}, wantCode: "active_code_change_missing"},
		{name: "ambiguous", refs: []github.NativeReference{codeChangeLinkReference(canonical),
			codeChangeLinkReference("https://code.example/acme/widgets/changes/43")}, wantCode: "active_code_change_ambiguous"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			backend := newFakeCodeChangeBackend()
			backend.references = test.refs
			backend.issueBackend = fakeGitHubBackend{listIssueComments: func(context.Context, string, int) ([]github.Comment, error) {
				t.Fatal("PROCESS lookup ran without exactly one active relationship")
				return nil, nil
			}}
			app, out, errOut := setupCodeChangeLinkApp(t, backend)
			code := app.runCodeChange(t.Context(), []string{"link-process", "--repo", "acme/widgets", "--implement", "9",
				"--process", "PROCESS-006", "--expected-version", "1", "--json"})
			if code != 1 || errOut.Len() != 0 {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
			}
			var result codeChangeLinkProcessErrorResult
			decodeCommandJSON(t, out.Bytes(), &result)
			if result.OK || result.Code != test.wantCode {
				t.Fatalf("result = %+v", result)
			}
		})
	}
}

func TestCodeChangeLinkProcessRejectsInvalidOrAmbiguousProcess(t *testing.T) {
	canonical := "https://code.example/acme/widgets/changes/42"
	valid := codeChangeLinkProcessBody(t, "PROCESS-006", "N/A")
	wrongType, err := model.EnsureTypedBody("TASK", "PROCESS-006", "## Task\n\nWrong type.", model.BodyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	malformed := strings.Replace(valid, "- PR: N/A", "- PR: N/A\n- PR: N/A", 1)
	tests := []struct {
		name        string
		comments    []github.Comment
		wantCode    string
		wantObserve int
	}{
		{name: "ambiguous markers", comments: []github.Comment{{ID: 1, Body: valid}, {ID: 2, Body: valid}},
			wantCode: "process_unavailable"},
		{name: "wrong type", comments: []github.Comment{{ID: 1, Body: wrongType}}, wantCode: "process_invalid"},
		{name: "malformed links", comments: []github.Comment{{ID: 1, Body: malformed}},
			wantCode: "process_mutation_invalid", wantObserve: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			backend := newFakeCodeChangeBackend()
			backend.references = []github.NativeReference{codeChangeLinkReference(canonical)}
			observations, writes := 0, 0
			base := fakeGitHubBackend{listIssueComments: func(context.Context, string, int) ([]github.Comment, error) {
				return test.comments, nil
			}}
			backend.issueBackend = conditionalTransitionBackend{fakeGitHubBackend: base,
				observe: func(_ context.Context, _ string, id int64) (github.CommentRepresentation, error) {
					observations++
					return github.CommentRepresentation{Comment: github.Comment{ID: id, Body: test.comments[0].Body},
						RepresentationVersion: 1}, nil
				},
				update: func(context.Context, string, int64, int64, string) (github.CommentRepresentation, error) {
					writes++
					return github.CommentRepresentation{}, nil
				}}
			app, out, _ := setupCodeChangeLinkApp(t, backend)
			if code := app.runCodeChange(t.Context(), []string{"link-process", "--repo", "acme/widgets", "--implement", "9",
				"--process", "PROCESS-006", "--expected-version", "1", "--json"}); code != 1 {
				t.Fatalf("exit=%d output=%s", code, out.String())
			}
			var result codeChangeLinkProcessErrorResult
			decodeCommandJSON(t, out.Bytes(), &result)
			if result.Code != test.wantCode || observations != test.wantObserve || writes != 0 {
				t.Fatalf("result=%+v observations=%d writes=%d", result, observations, writes)
			}
		})
	}
}

func TestCodeChangeLinkProcessConflictsFailClosed(t *testing.T) {
	canonical := "https://code.example/acme/widgets/changes/42"
	tests := []struct {
		name            string
		body            string
		observedVersion int64
		updateErr       error
		wantCode        string
		wantWrites      int
		wantCurrent     int64
	}{
		{name: "existing different URL", body: codeChangeLinkProcessBody(t, "PROCESS-006", "https://code.example/acme/widgets/changes/41"),
			observedVersion: 4, wantCode: "process_pr_link_conflict"},
		{name: "stale caller version", body: codeChangeLinkProcessBody(t, "PROCESS-006", "N/A"),
			observedVersion: 5, wantCode: "comment_representation_conflict", wantCurrent: 5},
		{name: "concurrent mutation", body: codeChangeLinkProcessBody(t, "PROCESS-006", "N/A"), observedVersion: 4,
			updateErr: &github.CommentMutationConflictError{Expected: 4, Current: 5},
			wantCode:  "comment_representation_conflict", wantWrites: 1, wantCurrent: 5},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			backend := newFakeCodeChangeBackend()
			backend.references = []github.NativeReference{codeChangeLinkReference(canonical)}
			writes := 0
			base := fakeGitHubBackend{listIssueComments: func(context.Context, string, int) ([]github.Comment, error) {
				return []github.Comment{{ID: 17, Body: test.body}}, nil
			}}
			backend.issueBackend = conditionalTransitionBackend{fakeGitHubBackend: base,
				observe: func(context.Context, string, int64) (github.CommentRepresentation, error) {
					return github.CommentRepresentation{Comment: github.Comment{ID: 17, Body: test.body},
						RepresentationVersion: test.observedVersion}, nil
				},
				update: func(context.Context, string, int64, int64, string) (github.CommentRepresentation, error) {
					writes++
					return github.CommentRepresentation{}, test.updateErr
				}}
			app, out, _ := setupCodeChangeLinkApp(t, backend)
			if code := app.runCodeChange(t.Context(), []string{"link-process", "--repo", "acme/widgets", "--implement", "9",
				"--process", "PROCESS-006", "--expected-version", "4", "--json"}); code != 1 {
				t.Fatalf("exit=%d output=%s", code, out.String())
			}
			var result codeChangeLinkProcessErrorResult
			decodeCommandJSON(t, out.Bytes(), &result)
			if result.Code != test.wantCode || writes != test.wantWrites || result.Current != test.wantCurrent {
				t.Fatalf("result=%+v writes=%d", result, writes)
			}
		})
	}
}

func TestCodeChangeLinkProcessRequiresConditionalBackend(t *testing.T) {
	backend := newFakeCodeChangeBackend()
	backend.references = []github.NativeReference{codeChangeLinkReference("https://code.example/acme/widgets/changes/42")}
	body := codeChangeLinkProcessBody(t, "PROCESS-006", "N/A")
	writes := 0
	backend.issueBackend = fakeGitHubBackend{
		listIssueComments: func(context.Context, string, int) ([]github.Comment, error) {
			return []github.Comment{{ID: 17, Body: body}}, nil
		},
		updateComment: func(context.Context, string, int64, string) (github.Comment, error) {
			writes++
			return github.Comment{}, nil
		},
	}
	app, out, _ := setupCodeChangeLinkApp(t, backend)
	if code := app.runCodeChange(t.Context(), []string{"link-process", "--repo", "acme/widgets", "--implement", "9",
		"--process", "PROCESS-006", "--expected-version", "1", "--json"}); code != 1 {
		t.Fatalf("exit=%d output=%s", code, out.String())
	}
	var result codeChangeLinkProcessErrorResult
	decodeCommandJSON(t, out.Bytes(), &result)
	if result.Code != "conditional_comment_required" || writes != 0 {
		t.Fatalf("result=%+v writes=%d", result, writes)
	}
}

func TestCodeChangeLinkProcessRejectsGitHubProfile(t *testing.T) {
	t.Setenv(auth.ConfigDirEnv, t.TempDir())
	t.Setenv(codereview.OperatorProvidersFileEnv, "")
	profile := auth.BuiltinGitHubProfile("github.com")
	profile.Name = "github-link-process"
	if err := auth.SaveProfile(profile, false); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	app.profileName = profile.Name
	app.newNativeCodeChangeBackend = func(auth.Profile, string) (nativeCodeChangeBackend, error) {
		t.Fatal("GitHub profile reached native backend")
		return nil, nil
	}
	code := app.runCodeChange(t.Context(), []string{"link-process", "--repo", "acme/widgets", "--implement", "9",
		"--process", "PROCESS-006", "--expected-version", "1", "--json"})
	if code != 1 || errOut.Len() != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	var result codeChangeLinkProcessErrorResult
	decodeCommandJSON(t, out.Bytes(), &result)
	if result.Code != "self_hosted_required" {
		t.Fatalf("result = %+v", result)
	}
}

func TestLegacyPRLinkProcessRemainsGitHubCompatible(t *testing.T) {
	body := codeChangeLinkProcessBody(t, "PROCESS-006", "N/A")
	writes := 0
	backend := fakeGitHubBackend{
		getPullRequest: func(context.Context, string, int) (github.PullRequest, error) {
			return github.PullRequest{Number: 7, HTMLURL: "https://github.com/acme/widgets/pull/7"}, nil
		},
		listIssueComments: func(context.Context, string, int) ([]github.Comment, error) {
			return []github.Comment{{ID: 17, Body: body}}, nil
		},
		updateComment: func(_ context.Context, _ string, id int64, updated string) (github.Comment, error) {
			writes++
			if id != 17 || !strings.Contains(updated, "- PR: https://github.com/acme/widgets/pull/7") {
				t.Fatalf("id=%d body=%s", id, updated)
			}
			return github.Comment{ID: id, Body: updated}, nil
		},
	}
	app, out, errOut := transitionAppWithError(backend)
	code := app.runPRLinkProcess(t.Context(), []string{"--repo", "acme/widgets", "--issue", "9", "--process", "PROCESS-006", "--pr", "7", "--json"})
	if code != 0 || errOut.Len() != 0 || writes != 1 {
		t.Fatalf("exit=%d writes=%d stdout=%q stderr=%q", code, writes, out.String(), errOut.String())
	}
	var result map[string]any
	if err := json.Unmarshal(out.Bytes(), &result); err != nil || result["changed"] != true {
		t.Fatalf("result=%v error=%v", result, err)
	}
}

func setupCodeChangeLinkApp(t *testing.T, backend *fakeCodeChangeBackend) (*app, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	profile := setupCodeChangeProfile(t)
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	app := newApp(strings.NewReader(""), out, errOut)
	app.profileName = profile.Name
	app.newNativeCodeChangeBackend = func(got auth.Profile, token string) (nativeCodeChangeBackend, error) {
		if got.Name != profile.Name || token != "attach-secret" {
			t.Fatalf("profile=%+v token=%q", got, token)
		}
		return backend, nil
	}
	return app, out, errOut
}

func codeChangeLinkReference(canonical string) github.NativeReference {
	return github.NativeReference{RelationKind: "code_change", LifecycleState: "active", CanonicalURL: canonical}
}

func codeChangeLinkProcessBody(t *testing.T, processID, pr string) string {
	t.Helper()
	body, err := model.EnsureTypedBody("PROCESS", processID, `## Process: link code change

### Parent TASK

- TASK-004

### Handoff

Preserve handoff.`, model.BodyOptions{Agent: "Worker Agent", AgentSessionID: "session-006",
		AgentSessionSource: "CODEX_THREAD_ID", SubjectRevision: "head-abc", Status: "in-progress", Scope: "cli",
		Links: map[string][]string{"Proposal Issue": {"https://issues.test/acme/widgets/issues/1"},
			"Design Issue": {"https://issues.test/acme/widgets/issues/2"}, "Implement Issue": {"https://issues.test/acme/widgets/issues/9"},
			"Related Comments": {"https://issues.test/acme/widgets/issues/9#issuecomment-4"}}})
	if err != nil {
		t.Fatal(err)
	}
	if pr != "N/A" {
		body = strings.Replace(body, "- PR: N/A", "- PR: "+pr, 1)
	}
	return body
}
