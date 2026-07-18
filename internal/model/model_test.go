package model

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/higress-group/issue-spec/internal/assignment"
)

func TestRepresentationDigestUsesExactMarkdownBytes(t *testing.T) {
	withoutNewline := RepresentationDigest("body")
	withNewline := RepresentationDigest("body\n")
	if withoutNewline != "230d8358dc8e8890b4c58deeb62912ee2f20357ae92a5cc861b98e68fe31acb5" {
		t.Fatalf("digest = %q", withoutNewline)
	}
	if withNewline != "9e2ec912af5dff2a72300863864fc4da04e81999339d9fac5c7590ba8a3f4e11" {
		t.Fatalf("newline digest = %q", withNewline)
	}
	if withoutNewline == withNewline {
		t.Fatal("digest normalized the trailing newline")
	}
}

func TestObserveAcceptedReceiptAuthorityAcrossRoleMarkers(t *testing.T) {
	payload := `{"receipt_id":"receipt-1","receipt_digest":"` + strings.Repeat("a", 64) +
		`","assignment_generation":2,"subject_revision":"not-projected","provenance":{"writer":"not-projected"}}`
	markers := map[assignment.Role][2]string{
		assignment.RoleImplementation: {"<!-- issue-spec:accepted-implementation-receipt version=1 -->", "<!-- /issue-spec:accepted-implementation-receipt -->"},
		assignment.RoleReview:         {"<!-- issue-spec:accepted-review-receipt version=1 -->", "<!-- /issue-spec:accepted-review-receipt -->"},
		assignment.RoleVerification:   {"<!-- issue-spec:accepted-verification-receipt version=1 -->", "<!-- /issue-spec:accepted-verification-receipt -->"},
	}
	for role, marker := range markers {
		t.Run(string(role), func(t *testing.T) {
			body := "typed carrier\n\n" + marker[0] + "\n" + payload + "\n" + marker[1] + "\n"
			authority, found, err := ObserveAcceptedReceiptAuthority(body, role)
			if err != nil || !found || authority.Role != role || authority.ReceiptID != "receipt-1" ||
				authority.Digest != strings.Repeat("a", 64) || authority.Generation != 2 {
				t.Fatalf("authority=%+v found=%t err=%v", authority, found, err)
			}
		})
	}
}

func TestObserveAcceptedReceiptAuthorityFailsClosedOnMissingOrMalformedMarker(t *testing.T) {
	if authority, found, err := ObserveAcceptedReceiptAuthority("typed PROCESS without receipt", assignment.RoleImplementation); err != nil || found || authority.ReceiptID != "" {
		t.Fatalf("missing marker authority=%+v found=%t err=%v", authority, found, err)
	}
	start := "<!-- issue-spec:accepted-review-receipt version=1 -->"
	end := "<!-- /issue-spec:accepted-review-receipt -->"
	valid := `{"receipt_id":"receipt-1","receipt_digest":"` + strings.Repeat("b", 64) + `","assignment_generation":1}`
	for name, body := range map[string]string{
		"duplicate field": start + "\n" + strings.TrimSuffix(valid, "}") + `,"receipt_id":"receipt-2"}` + "\n" + end,
		"noncompact":      start + "\n" + strings.Replace(valid, `,"receipt_digest"`, `, "receipt_digest"`, 1) + "\n" + end,
		"wrong version":   strings.Replace(start, "version=1", "version=2", 1) + "\n" + valid + "\n" + end,
		"duplicate pair":  start + "\n" + valid + "\n" + end + "\n" + start + "\n" + valid + "\n" + end,
	} {
		t.Run(name, func(t *testing.T) {
			if _, found, err := ObserveAcceptedReceiptAuthority(body, assignment.RoleReview); !found || err == nil {
				t.Fatalf("found=%t err=%v", found, err)
			}
		})
	}
}

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

func TestTypedProcessAssignmentCarrierStrictlyParsesStructuredInput(t *testing.T) {
	body, err := EnsureTypedBody("PROCESS", "PROCESS-005", `## Process: schema

### Assignment

`+"```json"+`
{
  "objective": "Define a portable schema",
  "scenario_selectors": [
    {"spec_id": "SPEC-001", "scenario": "bounded packet"}
  ],
  "required_tests": [
    {"id": "unit", "command": "go test ./internal/assignment"}
  ]
}
`+"```"+`
`, BodyOptions{Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	parsed := ParseTypedComment(body)
	if len(parsed.Errors) != 0 {
		t.Fatalf("parse errors = %v", parsed.Errors)
	}
	if parsed.Assignment == nil || parsed.Assignment.Objective != "Define a portable schema" {
		t.Fatalf("assignment = %+v", parsed.Assignment)
	}
	if len(parsed.Assignment.ScenarioSelectors) != 1 || parsed.Assignment.ScenarioSelectors[0].SpecID != "SPEC-001" {
		t.Fatalf("scenario selectors = %+v", parsed.Assignment.ScenarioSelectors)
	}
	data, err := json.Marshal(parsed)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"assignment":{"objective":"Define a portable schema"`) {
		t.Fatalf("typed JSON missing assignment: %s", data)
	}

	withoutSelector, err := assignment.ProcessInputJSON(assignment.ProcessInput{Objective: "All covered scenarios"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(withoutSelector), "scenario_selectors") {
		t.Fatalf("empty selector must remain the all-covered-scenarios sentinel: %s", withoutSelector)
	}
}

func TestTypedProcessAssignmentCarrierRejectsUnknownFields(t *testing.T) {
	body, err := EnsureTypedBody("PROCESS", "PROCESS-005", `## Process: schema

### Assignment

`+"```json"+`
{"objective":"schema","free_text_test_hint":"guess this"}
`+"```"+`
`, BodyOptions{Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	parsed := ParseTypedComment(body)
	if len(parsed.Errors) == 0 || !strings.Contains(strings.Join(parsed.Errors, "; "), `unknown field "free_text_test_hint"`) {
		t.Fatalf("errors = %v", parsed.Errors)
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
