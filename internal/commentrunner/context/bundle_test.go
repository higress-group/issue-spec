package context

import (
	"strings"
	"testing"

	"github.com/higress-group/issue-spec/internal/model"
)

func TestBuildContextBundleBoundsAndSources(t *testing.T) {
	spec := artifact(t, 1, "SPEC", "SPEC-001", "https://github.com/o/r/issues/1#issuecomment-1", strings.Repeat("a", 32))
	task := artifact(t, 2, "TASK", "TASK-001", "https://github.com/o/r/issues/2#issuecomment-2", "task body")
	history := artifact(t, 3, "PROCESS", "PROCESS-001", "https://github.com/o/r/issues/3#issuecomment-3", "history body")
	bundle, err := BuildContextBundle(BundleOptions{
		Runner:           RunnerMetadata{ProcessID: "PROCESS-009", Agent: "Worker", Repo: "o/r", IssueNumber: 9, TriggerComment: "https://github.com/o/r/issues/9#issuecomment-9"},
		Commands:         []CommandCandidate{{Name: "issue-spec comment upsert", Authorized: true}},
		Artifacts:        []model.Artifact{spec, task},
		History:          []model.Artifact{history},
		MaxArtifacts:     1,
		MaxArtifactBytes: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(bundle.Artifacts), 1; got != want {
		t.Fatalf("artifacts = %d, want %d", got, want)
	}
	if !bundle.Limits.Truncated {
		t.Fatal("expected truncation metadata")
	}
	if bundle.Artifacts[0].Source != "spec" {
		t.Fatalf("source = %q, want spec", bundle.Artifacts[0].Source)
	}
	if bundle.Artifacts[0].BodySHA256 == "" {
		t.Fatal("missing artifact hash")
	}
	if !bundle.Artifacts[0].Truncated {
		t.Fatal("expected body truncation")
	}
	if len(bundle.Sources) != 1 || bundle.Sources[0] != "spec" {
		t.Fatalf("sources = %#v", bundle.Sources)
	}
	if !bundle.History.Truncated {
		t.Fatal("history should be marked excluded")
	}
}

func TestBuildContextBundleRejectsMultipleCommands(t *testing.T) {
	_, err := BuildContextBundle(BundleOptions{
		Commands: []CommandCandidate{
			{Name: "a", Authorized: true},
			{Name: "b", Authorized: true},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "exactly one authorized command candidate") {
		t.Fatalf("err = %v", err)
	}
}

func TestRenderCoordinatorPromptBlocksHistoryRediscovery(t *testing.T) {
	bundle, err := BuildContextBundle(BundleOptions{
		Commands:  []CommandCandidate{{Name: "issue-spec comment upsert", Authorized: true}},
		Artifacts: []model.Artifact{artifact(t, 1, "PROCESS", "PROCESS-001", "https://github.com/o/r/issues/1#issuecomment-1", "body")},
	})
	if err != nil {
		t.Fatal(err)
	}
	prompt, err := RenderCoordinatorPrompt(bundle)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Do not rediscover the trigger comment from issue activity.",
		"Do not choose commands from issue history",
		"Preserve issue-spec DAG ready-set behavior",
		"write them directly with the CLI",
		`"selected_command": "issue-spec comment upsert"`,
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestParseSummarySchemaEdgeCases(t *testing.T) {
	if _, err := ParseSummarySchema(`{"records":[{"artifact_url":"u","command_name":"","exit_code":0}]}`); err == nil {
		t.Fatal("expected missing artifact_id and command_name to fail")
	}
	schema, err := ParseSummarySchema(`{"records":[{"artifact_id":"SPEC-001","artifact_url":"u","command_name":"cmd","exit_code":7,"stdout":"x","stderr":"y","child_ids":["PROCESS-2"],"process_ids":["PROCESS-1"],"diagnostics":["d"]}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if got := schema.Records[0].ExitCode; got != 7 {
		t.Fatalf("exit code = %d, want 7", got)
	}
	if got := len(schema.Records[0].ProcessIDs); got != 1 {
		t.Fatalf("process ids = %d, want 1", got)
	}
}

func artifact(t *testing.T, issue int, typ, id, url, body string) model.Artifact {
	t.Helper()
	typed, err := model.EnsureTypedBody(typ, id, body, model.BodyOptions{Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	return model.Artifact{Issue: issue, URL: url, APIURL: url, Comment: model.ParseTypedComment(typed)}
}
