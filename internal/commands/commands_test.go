package commands

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/model"
)

func TestTopLevelHelpRoutesDurableSpecAndCloseChangeWithoutArchiveGate(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := Execute([]string{"durable-spec", "check", "--help"}, strings.NewReader(""), &out, &errOut); code != 0 ||
		!strings.Contains(out.String(), "issue-spec durable-spec check") {
		t.Fatalf("durable route code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	out.Reset()
	errOut.Reset()
	if code := Execute([]string{"issue", "close-change", "--help"}, strings.NewReader(""), &out, &errOut); code != 0 ||
		!strings.Contains(out.String(), "issue-spec issue close-change") {
		t.Fatalf("close-change route code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	out.Reset()
	errOut.Reset()
	if code := Execute([]string{"--help"}, strings.NewReader(""), &out, &errOut); code != 0 {
		t.Fatalf("help code=%d stderr=%q", code, errOut.String())
	}
	for _, removed := range []string{"issue-spec archive durable-spec", "--durable-spec path"} {
		if strings.Contains(out.String(), removed) {
			t.Fatalf("normal help still exposes %q:\n%s", removed, out.String())
		}
	}
}

func TestCollectArtifactsKeepsCodeChangeRationaleAsUntypedEvidence(t *testing.T) {
	process := codeChangeRationaleProcessBody(t, "https://code.example/acme/widgets/changes/42")
	rationale, err := model.RenderCodeChangeRationaleBody(model.CodeChangeRationaleMarker{
		Process: "PROCESS-001", Spec: "SPEC-001", SpecURL: "https://issues.test/acme/widgets/issues/1#issuecomment-2",
		ProviderKey: "code.example", ExternalRepository: "acme/widgets-code", ChangeID: "change-42",
		ReferenceVersion: 7, SubjectRevision: "head-abc", Agent: "Worker", AgentSessionID: "worker-session",
		AgentSessionSource: "CODEX_THREAD_ID",
	}, "why")
	if err != nil {
		t.Fatal(err)
	}
	backend := fakeGitHubBackend{listIssueComments: func(context.Context, string, int) ([]github.Comment, error) {
		return []github.Comment{{ID: 1, Body: "ordinary prose"}, {ID: 2, Body: process},
			{ID: 3, HTMLURL: "https://issues.test/acme/widgets/issues/9#issuecomment-3", Body: rationale}}, nil
	}}
	artifacts, err := collectArtifacts(t.Context(), backend, "acme/widgets", 9)
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 2 || artifacts[0].Comment.Type != "PROCESS" || artifacts[1].Comment.Type != "" ||
		artifacts[1].Comment.ID != "" || artifacts[1].Issue != 9 || !model.IsLikelyCodeChangeRationale(artifacts[1].Comment.Body) {
		t.Fatalf("artifacts=%+v", artifacts)
	}
}
