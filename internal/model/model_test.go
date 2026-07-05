package model

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEnsureTypedBodyAddsMarkerAndHeader(t *testing.T) {
	body, err := EnsureTypedBody("SPEC", "SPEC-001", "## Requirement: X\n\nX MUST work.", BodyOptions{Agent: "Coordinator", Status: "confirmed", Scope: "workflow"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "<!-- issue-spec:type=SPEC id=SPEC-001 version=1 -->") {
		t.Fatalf("missing marker:\n%s", body)
	}
	tc := ParseTypedComment(body)
	if len(tc.Errors) > 0 {
		t.Fatalf("unexpected parse errors: %v", tc.Errors)
	}
	if tc.Type != "SPEC" || tc.ID != "SPEC-001" || tc.Status != "confirmed" || tc.Scope != "workflow" {
		t.Fatalf("unexpected typed comment: %+v", tc)
	}
}

func TestTypedCommentSessionMetadataRenderParseAndJSON(t *testing.T) {
	body, err := EnsureTypedBody("PROCESS", "PROCESS-001", "## Process\n\nDo work.", BodyOptions{
		Agent:              "Worker A",
		AgentSessionID:     "codex-session-123",
		AgentSessionSource: "CODEX_THREAD_ID",
		Status:             "in-progress",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Agent: Worker A",
		"Agent Session ID: codex-session-123",
		"Agent Session Source: CODEX_THREAD_ID",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in:\n%s", want, body)
		}
	}
	tc := ParseTypedComment(body)
	if tc.Agent != "Worker A" || tc.AgentSessionID != "codex-session-123" || tc.AgentSessionSource != "CODEX_THREAD_ID" {
		t.Fatalf("unexpected parsed metadata: %+v", tc)
	}
	data, err := json.Marshal(tc)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"agent":"Worker A"`, `"agent_session_id":"codex-session-123"`, `"agent_session_source":"CODEX_THREAD_ID"`} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("json missing %q: %s", want, data)
		}
	}
}

func TestTypedCommentLegacyAndFutureHeaderCompatibility(t *testing.T) {
	body := `<!-- issue-spec:type=TASK id=TASK-001 version=1 -->
Agent: Coordinator
Future Header: kept for later
Type: TASK
ID: TASK-001
Status: ready
Scope: workflow
Links:
- Proposal Issue: N/A
- Design Issue: N/A
- Implement Issue: N/A
- Related Comments: N/A
- PR: N/A

## Task
`
	tc := ParseTypedComment(body)
	if len(tc.Errors) > 0 {
		t.Fatalf("unexpected parse errors: %v", tc.Errors)
	}
	if tc.Type != "TASK" || tc.ID != "TASK-001" || tc.Status != "ready" || tc.AgentSessionID != "" || tc.AgentSessionSource != "" {
		t.Fatalf("unexpected parsed comment: %+v", tc)
	}
	data, err := json.Marshal(tc)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "agent_session_id") || strings.Contains(string(data), "agent_session_source") {
		t.Fatalf("legacy empty session fields should be omitted: %s", data)
	}
}

func TestStampTypedSessionMetadataOverridesPreRenderedHeaders(t *testing.T) {
	body := `<!-- issue-spec:type=PROCESS id=PROCESS-001 version=1 -->
Agent: Worker A
Agent Session ID: stale
Agent Session Source: stale-source
Type: PROCESS
ID: PROCESS-001
Status: in-progress
Scope: cli
Links:
- Proposal Issue: N/A
- Design Issue: N/A
- Implement Issue: N/A
- Related Comments: N/A
- PR: N/A

## Process
`
	updated, err := StampTypedSessionMetadata(body, "codex-session-456", "CODEX_THREAD_ID")
	if err != nil {
		t.Fatal(err)
	}
	tc := ParseTypedComment(updated)
	if tc.Agent != "Worker A" || tc.AgentSessionID != "codex-session-456" || tc.AgentSessionSource != "CODEX_THREAD_ID" {
		t.Fatalf("unexpected stamped metadata: %+v\n%s", tc, updated)
	}
}

func TestAddRelatedCommentLinkIsIdempotent(t *testing.T) {
	body, err := EnsureTypedBody("TASK", "TASK-001", "## Task\n\n- [ ] 1. Test", BodyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	updated, changed, err := AddRelatedCommentLink(body, "https://github.com/o/r/issues/1#issuecomment-1")
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("first link should change body")
	}
	updatedAgain, changed, err := AddRelatedCommentLink(updated, "https://github.com/o/r/issues/1#issuecomment-1")
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("second link should be idempotent")
	}
	if updatedAgain != updated {
		t.Fatal("idempotent update changed body")
	}
}

func TestAddPRLinkIsIdempotent(t *testing.T) {
	body, err := EnsureTypedBody("PROCESS", "PROCESS-001", "## Process\n\nDone.", BodyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	updated, changed, err := AddPRLink(body, "https://github.com/o/r/pull/4")
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("first PR link should change body")
	}
	updatedAgain, changed, err := AddPRLink(updated, "https://github.com/o/r/pull/4")
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("second PR link should be idempotent")
	}
	if updatedAgain != updated {
		t.Fatal("idempotent PR update changed body")
	}
}

func TestVerifyTraceabilityRequiresBacklinks(t *testing.T) {
	specBody, _ := EnsureTypedBody("SPEC", "SPEC-001", "## Requirement: X\n\nX MUST work.\n\n### Scenario: ok\n\n- **WHEN** x\n- **THEN** y", BodyOptions{})
	taskBody, _ := EnsureTypedBody("TASK", "TASK-001", "## Task\n\n- [ ] 1. Test", BodyOptions{})
	taskBody, _, _ = AddRelatedCommentLink(taskBody, "https://github.com/o/r/issues/1#issuecomment-1")

	report := VerifyTraceability([]Artifact{
		{URL: "https://github.com/o/r/issues/1#issuecomment-1", Comment: ParseTypedComment(specBody)},
		{URL: "https://github.com/o/r/issues/2#issuecomment-2", Comment: ParseTypedComment(taskBody)},
	})
	if report.OK {
		t.Fatal("expected missing SPEC backlink to fail")
	}

	specBody, _, _ = AddRelatedCommentLink(specBody, "https://github.com/o/r/issues/2#issuecomment-2")
	report = VerifyTraceability([]Artifact{
		{URL: "https://github.com/o/r/issues/1#issuecomment-1", Comment: ParseTypedComment(specBody)},
		{URL: "https://github.com/o/r/issues/2#issuecomment-2", Comment: ParseTypedComment(taskBody)},
	})
	if !report.OK {
		t.Fatalf("expected traceability OK, got %v", report.Errors)
	}
}

func TestSetStatusAndAppendResolution(t *testing.T) {
	body, err := EnsureTypedBody("QUESTION", "QUESTION-001", "## Question\n\nUse confirmed?\n\n## Resolution Log\n\n- Pending.", BodyOptions{Status: "blocked"})
	if err != nil {
		t.Fatal(err)
	}
	body, err = SetTypedCommentStatus(body, "confirmed")
	if err != nil {
		t.Fatal(err)
	}
	body = AppendResolutionLog(body, "Use confirmed as the default resolved status.")
	tc := ParseTypedComment(body)
	if tc.Status != "confirmed" {
		t.Fatalf("status = %s", tc.Status)
	}
	if !strings.Contains(body, "Use confirmed as the default resolved status.") {
		t.Fatalf("missing resolution log:\n%s", body)
	}
}
