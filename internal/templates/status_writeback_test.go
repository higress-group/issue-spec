package templates

import (
	"strings"
	"testing"

	runnercontext "github.com/higress-group/issue-spec/internal/commentrunner/context"
)

func TestRenderRunnerStatusCommentKeepsPublicBodyConcise(t *testing.T) {
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
		CLIDirect: []RunnerCLICommand{{
			Name:          "issue-spec comment upsert",
			ExitCode:      0,
			ArtifactID:    "PROCESS-001",
			ArtifactURL:   "https://github.com/o/r/issues/1#issuecomment-1",
			StdoutSummary: "updated PROCESS with implementation details",
			Diagnostics:   "temporary file removed; git status clean",
		}},
		Diagnostics: []string{"short diagnostic"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"issue-spec-runner:status",
		"| Status | `running` |",
		"| Phase | `dispatch` |",
		"| Public session | `s_123` |",
		"## Result",
		"Completed the requested command.",
		"updated typed_comment PROCESS-001: https://github.com/o/r/issues/1#issuecomment-1",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %q:\n%s", want, body)
		}
	}
	for _, forbidden := range []string{
		"| Runner job |",
		"| Trigger comment |",
		"| Triggering user |",
		"| Session creator |",
		"| Current turn user |",
		"| Agent |",
		"| Sandbox |",
		"unsafe no-sandbox mode requested",
		"Coordinator-reported CLI artifact",
		"Coordinator CLI command",
		"Child provenance",
		"PROCESS evidence",
		"Stored coordinator CLI provenance",
		"issue-spec comment upsert",
		"updated PROCESS with implementation details",
		"temporary file removed",
		"short diagnostic",
		"tests passed",
		"child-1",
		"job-1",
		"alice",
		"bob",
		"native-codex",
		"gpt-5.5",
		"\"job_id\"",
		"\"trigger_comment_id\"",
		"\"triggering_user_login\"",
		"\"agent_kind\"",
		"\"model\"",
		"workflow artifact PROCESS-001",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("body leaked %q:\n%s", forbidden, body)
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

func TestRenderRunnerStatusCommentShowsResultWithoutCoordinatorSummary(t *testing.T) {
	body, err := RenderRunnerStatusComment(RunnerStatusComment{
		Status:          "completed",
		PublicSessionID: "s_123",
		Diagnostics:     []string{"coordinator summary was missing or malformed"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"| Status | `completed` |",
		"## Result",
		"Completed the requested command.",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("terminal body missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "coordinator summary was missing or malformed") {
		t.Fatalf("diagnostic should not be rendered in public body:\n%s", body)
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

func TestRunnerStatusMarkerRoundTripOnlyWritesOpaqueKey(t *testing.T) {
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
	if marker.StatusWritebackKey != "status:o/r:101" || marker.JobID != "" || marker.PublicSessionID != "" {
		t.Fatalf("unexpected marker: %+v", marker)
	}
	for _, forbidden := range []string{"\"job_id\"", "\"public_session_id\"", "\"trigger_comment_id\"", "\"agent_kind\"", "\"model\""} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("marker leaked %q:\n%s", forbidden, body)
		}
	}
}

func TestRunnerStatusMarkerParsesLegacyMetadata(t *testing.T) {
	body := `<!-- issue-spec-runner:status {"schema_version":1,"status_writeback_key":"status:o/r:101","job_id":"job-1","public_session_id":"s_123","trigger_comment_id":101,"triggering_user_login":"alice","agent_kind":"native-codex","model":"gpt-5.5"} -->`
	marker, ok, err := ParseRunnerStatusMarker(body)
	if err != nil || !ok {
		t.Fatalf("legacy marker parse ok=%v err=%v", ok, err)
	}
	if marker.StatusWritebackKey != "status:o/r:101" ||
		marker.JobID != "job-1" ||
		marker.PublicSessionID != "s_123" ||
		marker.TriggerCommentID != 101 ||
		marker.TriggeringUserLogin != "alice" ||
		marker.AgentKind != "native-codex" ||
		marker.Model != "gpt-5.5" {
		t.Fatalf("unexpected legacy marker: %+v", marker)
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
	if strings.Contains(body, strings.Repeat("x", 20)) || strings.Contains(body, "xx...") {
		t.Fatalf("diagnostic should not be rendered in public body:\n%s", body)
	}
}

func TestRunnerStatusCommentShowsConciseRejectedReason(t *testing.T) {
	body, err := RenderRunnerStatusComment(RunnerStatusComment{
		Status:       "rejected",
		Phase:        "command-unauthorized",
		Diagnostics:  []string{"command unauthorized; auth=insufficient_permission"},
		MaxTextBytes: 64,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"| Status | `rejected` |",
		"| Phase | `command-unauthorized` |",
		"## Reason",
		"command unauthorized; auth=insufficient_permission",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("rejected body missing %q:\n%s", want, body)
		}
	}
}
