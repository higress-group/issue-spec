package contextbundle

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseCoordinatorSummaryAcceptsPlainURLStringArtifacts(t *testing.T) {
	summary, err := ParseCoordinatorSummary([]byte(`{
  "status": "completed",
  "artifacts": ["https://github.com/higress-group/higress/pull/4485"]
}`), SummaryBounds{})
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Artifacts) != 1 {
		t.Fatalf("artifact count = %d, want 1: %+v", len(summary.Artifacts), summary.Artifacts)
	}
	artifact := summary.Artifacts[0]
	if artifact.Kind != WorkflowArtifactKindURL || artifact.URL != "https://github.com/higress-group/higress/pull/4485" {
		t.Fatalf("plain-string artifact decoded incorrectly: %+v", artifact)
	}
	if artifact.ID != "" || artifact.Issue != 0 || artifact.CommentID != 0 || artifact.Action != "" {
		t.Fatalf("plain-string artifact fabricated identity fields: %+v", artifact)
	}
}

func TestParseCoordinatorSummaryAcceptsMixedStringAndObjectArtifacts(t *testing.T) {
	summary, err := ParseCoordinatorSummary([]byte(`{
  "status": "completed",
  "artifacts": [
    "https://github.com/higress-group/higress/pull/4485",
    {"kind": "typed_comment", "id": "PROCESS-001", "url": "https://github.com/owner/repo/issues/1#issuecomment-1", "action": "updated"}
  ]
}`), SummaryBounds{})
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Artifacts) != 2 {
		t.Fatalf("artifact count = %d, want 2: %+v", len(summary.Artifacts), summary.Artifacts)
	}
	if summary.Artifacts[0].Kind != WorkflowArtifactKindURL || summary.Artifacts[0].URL != "https://github.com/higress-group/higress/pull/4485" {
		t.Fatalf("string artifact decoded incorrectly: %+v", summary.Artifacts[0])
	}
	if summary.Artifacts[1].Kind != "typed_comment" || summary.Artifacts[1].ID != "PROCESS-001" || summary.Artifacts[1].Action != "updated" {
		t.Fatalf("object artifact not preserved verbatim: %+v", summary.Artifacts[1])
	}
}

func TestParseCoordinatorSummaryRejectsInvalidArtifactEntryTypes(t *testing.T) {
	for _, tc := range []struct {
		name string
		json string
	}{
		{name: "number", json: `{"status": "completed", "artifacts": [123]}`},
		{name: "boolean", json: `{"status": "completed", "artifacts": [true]}`},
		{name: "null", json: `{"status": "completed", "artifacts": [null]}`},
		{name: "empty-string", json: `{"status": "completed", "artifacts": ["   "]}`},
		{name: "nested-array", json: `{"status": "completed", "artifacts": [["https://example.com"]]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseCoordinatorSummary([]byte(tc.json), SummaryBounds{}); err == nil {
				t.Fatal("expected invalid artifact entry to fail summary parsing")
			}
		})
	}
}

func TestParseCoordinatorSummaryAcceptsProvenanceOnlySchema(t *testing.T) {
	summary, err := ParseCoordinatorSummary([]byte(`{
  "status": "completed",
  "artifacts": [
    {"kind": "typed_comment", "id": "PROCESS-001", "url": "https://github.com/owner/repo/issues/1#issuecomment-1", "action": "updated"}
  ],
  "commands": [
    {"name": "issue-spec comment upsert", "exit_code": 0, "artifact_id": "PROCESS-001", "stdout_summary": "updated", "stderr_summary": ""}
  ],
  "diagnostics": []
}`), SummaryBounds{MaxOutputBytes: 32, MaxDiagnosticBytes: 64})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Status != "completed" {
		t.Fatalf("status = %q", summary.Status)
	}
	if summary.Commands[0].Name != "issue-spec comment upsert" {
		t.Fatalf("unexpected command summary: %+v", summary.Commands[0])
	}
}

func TestParseCoordinatorSummaryIgnoresAdditiveTopLevelFields(t *testing.T) {
	summary, err := ParseCoordinatorSummary([]byte(`{
  "status": "completed",
  "artifacts": [
    {"kind": "typed_comment", "id": "PROCESS-001", "action": "updated"}
  ],
  "commands": [
    {"name": "issue-spec comment upsert", "exit_code": 0, "artifact_id": "PROCESS-001", "stdout_summary": "updated"}
  ],
  "smoke_test_evidence": {
    "repository_full_name": "owner/repo",
    "external_repository_id": 12345,
    "checks": [{"name": "binding", "passed": true}]
  }
}`), SummaryBounds{})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Status != "completed" || len(summary.Artifacts) != 1 || summary.Artifacts[0].ID != "PROCESS-001" {
		t.Fatalf("recognized summary fields were not preserved: %+v", summary)
	}
	if len(summary.Commands) != 1 || summary.Commands[0].StdoutSummary != "updated" {
		t.Fatalf("recognized command fields were not preserved: %+v", summary.Commands)
	}
}

func TestParseCoordinatorSummaryRejectsInvalidRecognizedFieldType(t *testing.T) {
	_, err := ParseCoordinatorSummary([]byte(`{
  "status": "completed",
  "commands": "not-an-array",
  "future_field": {"ignored": true}
}`), SummaryBounds{})
	if err == nil || !strings.Contains(err.Error(), "cannot unmarshal string") {
		t.Fatalf("expected invalid recognized field type failure, got %v", err)
	}
}

func TestParseCoordinatorSummaryAcceptsE2EStringDiagnosticsAndNullCommandRefs(t *testing.T) {
	summary, err := ParseCoordinatorSummary([]byte(`{
  "status": "completed",
  "artifacts": [
    {"kind": "issue", "id": "36", "url": "https://github.com/higress-group/issue-spec/issues/36", "action": "created"}
  ],
  "commands": [
    {"name": "issue-spec proposal create", "exit_code": 0, "artifact_id": null, "artifact_url": null, "stdout_summary": "proposal #36", "stderr_summary": null}
  ],
  "diagnostics": ["No native sub-agents were dispatched because the task was handled locally."]
}`), SummaryBounds{})
	if err != nil {
		t.Fatal(err)
	}
	if got := summary.Diagnostics[0].Message; got != "No native sub-agents were dispatched because the task was handled locally." {
		t.Fatalf("diagnostic message = %q", got)
	}
	if summary.Commands[0].ArtifactID != "" || summary.Commands[0].ArtifactURL != "" || summary.Commands[0].StderrSummary != "" {
		t.Fatalf("nullable command refs should decode as empty strings: %+v", summary.Commands[0])
	}
}

func TestParseCoordinatorSummaryAcceptsDiagnosticLevelAlias(t *testing.T) {
	summary, err := ParseCoordinatorSummary([]byte(`{
  "status": "completed",
  "diagnostics": [
    {"level": "info", "message": "runner recovered summary from acpx history"}
  ]
}`), SummaryBounds{})
	if err != nil {
		t.Fatal(err)
	}
	if got := summary.Diagnostics[0].Severity; got != "info" {
		t.Fatalf("diagnostic severity = %q, want info", got)
	}
	if got := summary.Diagnostics[0].Message; got != "runner recovered summary from acpx history" {
		t.Fatalf("diagnostic message = %q", got)
	}
}

func TestParseCoordinatorSummaryAcceptsDiagnosticCode(t *testing.T) {
	summary, err := ParseCoordinatorSummary([]byte(`{
  "status": "completed",
  "diagnostics": [
    {"code": "selector_echo", "message": "selector=claude; agent kind confirmed"}
  ]
}`), SummaryBounds{})
	if err != nil {
		t.Fatal(err)
	}
	if got := summary.Diagnostics[0].Code; got != "selector_echo" {
		t.Fatalf("diagnostic code = %q, want selector_echo", got)
	}
	if got := summary.Diagnostics[0].Message; got != "selector=claude; agent kind confirmed" {
		t.Fatalf("diagnostic message = %q", got)
	}
}

func TestDiagnosticSummaryUnmarshalJSONResetsReusedReceiver(t *testing.T) {
	var diagnostic DiagnosticSummary
	for _, test := range []struct {
		name string
		json string
		want DiagnosticSummary
	}{
		{
			name: "coded object",
			json: `{"code":"selector_echo","severity":"info","message":"selector confirmed"}`,
			want: DiagnosticSummary{Code: "selector_echo", Severity: "info", Message: "selector confirmed"},
		},
		{
			name: "string clears object fields",
			json: `"plain diagnostic"`,
			want: DiagnosticSummary{Message: "plain diagnostic"},
		},
		{
			name: "object with omitted fields stays clear",
			json: `{"message":"object diagnostic"}`,
			want: DiagnosticSummary{Message: "object diagnostic"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			diagnostic = DiagnosticSummary{Code: "stale", Severity: "warning", Message: "stale"}
			if err := json.Unmarshal([]byte(test.json), &diagnostic); err != nil {
				t.Fatal(err)
			}
			if diagnostic != test.want {
				t.Fatalf("diagnostic = %+v, want %+v", diagnostic, test.want)
			}
		})
	}
}

func TestParseCoordinatorSummaryRejectsMalformedOrOversizedOutput(t *testing.T) {
	_, err := ParseCoordinatorSummary([]byte(`{"status":"completed"`), SummaryBounds{})
	if err == nil {
		t.Fatal("expected malformed JSON to fail")
	}

	_, err = ParseCoordinatorSummary([]byte(`{"status":"queued"}`), SummaryBounds{})
	if err == nil {
		t.Fatal("expected unsupported status to fail")
	}

	_, err = ParseCoordinatorSummary([]byte(`{
  "status": "completed",
  "artifacts": [{"kind": "typed_comment"}]
}`), SummaryBounds{})
	if err == nil {
		t.Fatal("expected artifact without id or URL to fail")
	}

	_, err = ParseCoordinatorSummary([]byte(`{
  "status": "failed",
  "commands": [{"name": "issue-spec status", "exit_code": 1, "stdout_summary": "too long"}]
}`), SummaryBounds{MaxOutputBytes: 3})
	if err == nil || !strings.Contains(err.Error(), "stdout_summary exceeds limit") {
		t.Fatalf("expected stdout bound failure, got %v", err)
	}

	_, err = ParseCoordinatorSummary([]byte(`{
  "status": "completed",
  "diagnostics": [{"category": "runtime", "message": "unknown fields stay rejected"}]
}`), SummaryBounds{})
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("expected unknown diagnostic field failure, got %v", err)
	}
}

func TestExtractCoordinatorSummaryFromReplyBody(t *testing.T) {
	reply := `work completed

` + "```issue_spec_coordinator_summary" + `
{
  "status": "completed",
  "artifacts": [{"kind": "typed_comment", "id": "PROCESS-001", "action": "updated"}],
  "commands": [{"name": "issue-spec comment upsert", "exit_code": 0}]
}
` + "```" + `
trailing text`
	summary, found, err := ExtractCoordinatorSummary(reply, SummaryBounds{})
	if err != nil {
		t.Fatal(err)
	}
	if !found || summary.Status != "completed" || summary.Artifacts[0].ID != "PROCESS-001" {
		t.Fatalf("summary=%+v found=%v", summary, found)
	}
}

func TestExtractCoordinatorSummaryAcceptsBodyPrefixOnFenceOpener(t *testing.T) {
	reply := `work completed

` + "```issue_spec_coordinator_summary{" + `
  "status": "completed",
  "artifacts": [{"kind": "typed_comment", "id": "PROCESS-001", "action": "updated"}],
  "commands": [{"name": "issue-spec comment upsert", "exit_code": 0}]
}
` + "```"
	summary, found, err := ExtractCoordinatorSummary(reply, SummaryBounds{})
	if err != nil {
		t.Fatal(err)
	}
	if !found || summary.Status != "completed" || summary.Artifacts[0].ID != "PROCESS-001" {
		t.Fatalf("summary=%+v found=%v", summary, found)
	}
}

func TestExtractCoordinatorSummaryReportsMissingCloseFence(t *testing.T) {
	_, found, err := ExtractCoordinatorSummary("```issue_spec_coordinator_summary\n{}", SummaryBounds{})
	if !found || err == nil || !strings.Contains(err.Error(), "not closed") {
		t.Fatalf("found=%v err=%v", found, err)
	}
}
