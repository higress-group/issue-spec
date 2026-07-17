package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/model"
)

func TestCodeChangeRationaleHelpAndRequiresSession(t *testing.T) {
	t.Setenv(codexThreadIDEnv, "")
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	if code := app.runCodeChange(t.Context(), []string{"rationale", "--help"}); code != 0 {
		t.Fatalf("help exit=%d", code)
	}
	for _, flag := range []string{"--repo", "--implement", "--process", "--spec", "--spec-url", "--body", "--agent", "--agent-session", "--json"} {
		if !strings.Contains(out.String(), flag) {
			t.Fatalf("help missing %s:\n%s", flag, out.String())
		}
	}
	out.Reset()
	if code := app.runCodeChange(t.Context(), codeChangeRationaleArgs("why")); code != 2 ||
		!strings.Contains(errOut.String(), "CODEX_THREAD_ID or --agent-session") {
		t.Fatalf("exit=%d stderr=%q", code, errOut.String())
	}
}

func TestCodeChangeRationaleAppendOnlyExactRetryAndRefresh(t *testing.T) {
	t.Setenv(codexThreadIDEnv, "worker-session")
	backend := newFakeCodeChangeBackend()
	canonical := "https://code.example/acme/widgets/changes/42"
	backend.references = []github.NativeReference{codeChangeRationaleReference(canonical, "head-abc", 7)}
	processBody := codeChangeRationaleProcessBody(t, canonical)
	comments := []github.Comment{{ID: 17, HTMLURL: "https://issues.test/acme/widgets/issues/9#issuecomment-17", Body: processBody}}
	created := 0
	backend.issueBackend = fakeGitHubBackend{
		listIssueComments: func(context.Context, string, int) ([]github.Comment, error) {
			return append([]github.Comment(nil), comments...), nil
		},
		createComment: func(_ context.Context, repo string, issue int, body string) (github.Comment, error) {
			created++
			comment := github.Comment{ID: int64(20 + created), HTMLURL: "https://issues.test/acme/widgets/issues/9#issuecomment-created", Body: body}
			comments = append(comments, comment)
			return comment, nil
		},
	}
	app, out, errOut := setupCodeChangeLinkApp(t, backend)
	args := codeChangeRationaleArgs("first rationale")
	if code := app.runCodeChange(t.Context(), args); code != 0 || errOut.Len() != 0 {
		t.Fatalf("create exit=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	var result codeChangeRationaleResult
	decodeCommandJSON(t, out.Bytes(), &result)
	if !result.OK || !result.Created || result.RepresentationVersion != 7 || result.SubjectRevision != "head-abc" || created != 1 {
		t.Fatalf("result=%+v created=%d", result, created)
	}
	marker, found, err := model.FindCodeChangeRationaleMarker(comments[len(comments)-1].Body)
	if err != nil || !found || marker.Agent != "PROCESS-007 worker" || marker.AgentSessionID != "worker-session" ||
		marker.ReferenceVersion != 7 || marker.SubjectRevision != "head-abc" {
		t.Fatalf("marker=%+v found=%v err=%v", marker, found, err)
	}

	out.Reset()
	if code := app.runCodeChange(t.Context(), args); code != 0 || created != 1 {
		t.Fatalf("exact retry exit=%d created=%d stderr=%q", code, created, errOut.String())
	}
	decodeCommandJSON(t, out.Bytes(), &result)
	if result.Created {
		t.Fatalf("exact retry unexpectedly created: %+v", result)
	}

	// Same identity with different rationale text is not an exact retry and must
	// not be silently swallowed; the append-only carrier records a new comment.
	out.Reset()
	if code := app.runCodeChange(t.Context(), codeChangeRationaleArgs("materially different rationale")); code != 0 || created != 2 {
		t.Fatalf("non-exact retry exit=%d created=%d stderr=%q", code, created, errOut.String())
	}

	backend.references = []github.NativeReference{codeChangeRationaleReference(canonical, "head-def", 8)}
	out.Reset()
	if code := app.runCodeChange(t.Context(), codeChangeRationaleArgs("refreshed head rationale")); code != 0 || created != 3 {
		t.Fatalf("refresh exit=%d created=%d stderr=%q", code, created, errOut.String())
	}
	latest, found, err := model.FindCodeChangeRationaleMarker(comments[len(comments)-1].Body)
	if err != nil || !found || latest.ReferenceVersion != 8 || latest.SubjectRevision != "head-def" {
		t.Fatalf("latest=%+v found=%v err=%v", latest, found, err)
	}
	if old, _, _ := model.FindCodeChangeRationaleMarker(comments[1].Body); old.ReferenceVersion != 7 || old.SubjectRevision != "head-abc" {
		t.Fatalf("old append-only marker was changed: %+v", old)
	}
}

func TestCodeChangeRationaleRejectsMissingProcessSpecAndChangeLinks(t *testing.T) {
	t.Setenv(codexThreadIDEnv, "worker-session")
	canonical := "https://code.example/acme/widgets/changes/42"
	tests := map[string]struct {
		body string
		want string
	}{
		"wrong spec":     {body: codeChangeRationaleProcessBody(t, canonical), want: "does not cover"},
		"missing change": {body: strings.Replace(codeChangeRationaleProcessBody(t, canonical), canonical, "N/A", 1), want: "does not link"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			backend := newFakeCodeChangeBackend()
			backend.references = []github.NativeReference{codeChangeRationaleReference(canonical, "head-abc", 7)}
			backend.issueBackend = fakeGitHubBackend{listIssueComments: func(context.Context, string, int) ([]github.Comment, error) {
				return []github.Comment{{ID: 17, Body: test.body}}, nil
			}}
			app, out, errOut := setupCodeChangeLinkApp(t, backend)
			args := codeChangeRationaleArgs("why")
			if name == "wrong spec" {
				for i := range args {
					if args[i] == "SPEC-001" {
						args[i] = "SPEC-002"
					}
				}
			}
			if code := app.runCodeChange(t.Context(), args); code != 1 || !strings.Contains(out.String()+errOut.String(), test.want) {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
			}
		})
	}
}

func TestUniqueActiveCodeChangeIdentityRejectsTrailingMetadata(t *testing.T) {
	reference := codeChangeRationaleReference("https://code.example/acme/widgets/changes/42", "head-abc", 7)
	reference.Metadata = append(reference.Metadata, []byte(` {"head_revision":"shadow"}`)...)
	if _, _, err := uniqueActiveCodeChangeIdentity([]github.NativeReference{reference}); err == nil {
		t.Fatal("trailing reference metadata was accepted")
	}
}

func codeChangeRationaleArgs(body string) []string {
	return []string{"rationale", "--repo", "acme/widgets", "--implement", "9", "--process", "PROCESS-001",
		"--spec", "SPEC-001", "--spec-url", "https://issues.test/acme/widgets/issues/1#issuecomment-2",
		"--body", body, "--agent", "PROCESS-007 worker", "--json"}
}

func codeChangeRationaleReference(canonical, revision string, version int64) github.NativeReference {
	metadata, _ := json.Marshal(map[string]string{"head_revision": revision})
	return github.NativeReference{ID: "reference-1", ProviderKey: "code.example", RelationKind: "code_change",
		ExternalRepositoryID: "acme/widgets-code", ExternalID: "change-42", CanonicalURL: canonical,
		LifecycleState: "active", Metadata: metadata, RepresentationVersion: version}
}

func codeChangeRationaleProcessBody(t *testing.T, canonical string) string {
	t.Helper()
	body, err := model.EnsureTypedBody("PROCESS", "PROCESS-001", `## Process: implement provider flow

### Parent TASK

- TASK-001

### Execution Class

- change-bearing

### Covers

- SPEC-001

### Handoff

Implementation complete.`, model.BodyOptions{Agent: "Coordinator", AgentSessionID: "coordinator-session",
		AgentSessionSource: "CODEX_THREAD_ID", Status: "done", Scope: "provider",
		Links: map[string][]string{"Related Comments": {"https://issues.test/acme/widgets/issues/1#issuecomment-2"}, "PR": {canonical}}})
	if err != nil {
		t.Fatal(err)
	}
	return body
}
