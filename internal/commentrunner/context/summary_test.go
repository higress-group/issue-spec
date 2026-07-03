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
