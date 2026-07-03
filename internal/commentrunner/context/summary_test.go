package contextbundle

import (
	"strings"
	"testing"
)

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

func TestParseCoordinatorSummaryRejectsMalformedOrOversizedOutput(t *testing.T) {
	_, err := ParseCoordinatorSummary([]byte(`{"status":"queued"}`), SummaryBounds{})
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
