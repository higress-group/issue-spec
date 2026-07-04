package templates

import (
	"strings"
	"testing"

	runnercontext "github.com/higress-group/issue-spec/internal/commentrunner/context"
)

func TestRenderRunnerStatusCommentIncludesSafePublicFields(t *testing.T) {
	body, err := RenderRunnerStatusComment(RunnerStatusComment{
		Marker: RunnerStatusMarker{
			SchemaVersion:       RunnerStatusMarkerSchemaVersion,
			StatusWritebackKey:  "status:o/r:101",
			JobID:               "job-1",
			PublicSessionID:     "s_123",
			TriggerCommentID:    101,
			TriggeringUserLogin: "alice",
			AgentKind:           "native-codex",
			Model:               "gpt-5.5",
		},
		Status:              "running",
		Phase:               "dispatch",
		SessionCreatorLogin: "alice",
		CurrentUserLogin:    "bob",
		SandboxProvider:     "none",
		FSBoundary:          "disabled",
		UnsafeNoSandbox:     true,
		CoordinatorSummary: &runnercontext.CoordinatorSummary{
			Status: "completed",
			Artifacts: []runnercontext.WorkflowArtifact{{
				Kind: "typed_comment", ID: "PROCESS-001", URL: "https://github.com/o/r/issues/1#issuecomment-1", Action: "updated",
			}},
			Commands: []runnercontext.CLICommandSummary{{
				Name: "issue-spec comment upsert", ExitCode: 0, ArtifactID: "PROCESS-001",
			}},
			Children: []runnercontext.ChildSummary{{
				ID: "child-1", Role: "worker", ProcessID: "PROCESS-001", Status: "done", Evidence: "tests passed",
			}},
		},
		Diagnostics: []string{"short diagnostic"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"issue-spec-runner:status",
		"| Status | `running` |",
		"| Phase | `dispatch` |",
		"| Runner job | `job-1` |",
		"| Public session | `s_123` |",
		"| Trigger comment | `101` |",
		"| Current turn user | `bob` |",
		"| Sandbox | `none / disabled` |",
		"unsafe no-sandbox mode requested",
		"Coordinator-reported CLI artifact",
		"Workflow artifacts are written by the sandboxed coordinator through existing issue-spec CLI commands",
		"Child provenance",
		"short diagnostic",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "runner wrote") || strings.Contains(body, "action envelope") {
		t.Fatalf("body claims artifact ownership or action envelope:\n%s", body)
	}
	if strings.Contains(body, "## Continue Session") || strings.Contains(body, "/resume s_123") {
		t.Fatalf("running status should not invite resume:\n%s", body)
	}
}

func TestRenderRunnerStatusCommentIncludesResumeGuidanceForTerminalSession(t *testing.T) {
	body, err := RenderRunnerStatusComment(RunnerStatusComment{
		Status:          "completed",
		PublicSessionID: "s_123",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"## Continue Session",
		"/resume s_123 <answer or next instruction>",
		"runner only sends `/resume` command comments to the coordinator",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("terminal body missing %q:\n%s", want, body)
		}
	}
}

func TestRenderRunnerStatusCommentSkipsResumeGuidanceWithoutPublicSession(t *testing.T) {
	body, err := RenderRunnerStatusComment(RunnerStatusComment{Status: "completed"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, "## Continue Session") || strings.Contains(body, "/resume ") {
		t.Fatalf("empty public session should not render resume guidance:\n%s", body)
	}
}

func TestRunnerStatusMarkerRoundTrip(t *testing.T) {
	body, err := RenderRunnerStatusComment(RunnerStatusComment{
		Marker: RunnerStatusMarker{
			SchemaVersion:      RunnerStatusMarkerSchemaVersion,
			StatusWritebackKey: "status:o/r:101",
			JobID:              "job-1",
			PublicSessionID:    "s_123",
			TriggerCommentID:   101,
			AgentKind:          "native-codex",
			Model:              "gpt-5.5",
		},
		Status: "completed",
	})
	if err != nil {
		t.Fatal(err)
	}
	marker, ok, err := ParseRunnerStatusMarker(body)
	if err != nil || !ok {
		t.Fatalf("marker parse ok=%v err=%v", ok, err)
	}
	if marker.StatusWritebackKey != "status:o/r:101" || marker.JobID != "job-1" || marker.PublicSessionID != "s_123" {
		t.Fatalf("unexpected marker: %+v", marker)
	}
}

func TestRunnerStatusCommentBoundsDiagnostics(t *testing.T) {
	body, err := RenderRunnerStatusComment(RunnerStatusComment{
		Marker:       RunnerStatusMarker{SchemaVersion: RunnerStatusMarkerSchemaVersion, JobID: "job-1"},
		Status:       "failed",
		Error:        strings.Repeat("x", 40),
		MaxTextBytes: 12,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, strings.Repeat("x", 20)) || !strings.Contains(body, "xx...") {
		t.Fatalf("expected bounded diagnostic, got:\n%s", body)
	}
}
