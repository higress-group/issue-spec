package gates

import (
	"testing"

	"github.com/higress-group/issue-spec/internal/model"
)

func TestProcessEvidenceFiveClassMatrix(t *testing.T) {
	const taskURL = "https://example/issues/2#issuecomment-task"
	const prURL = "https://example/pull/7"
	active := map[string]string{"SPEC-001": "https://example/issues/1#issuecomment-spec"}
	base := func(class model.ProcessExecutionClass) ProcessEvidenceInput {
		body, err := model.EnsureTypedBody("PROCESS", "PROCESS-001", "## Process: p\n\n### Parent TASK\n\n- TASK-001\n\n### Execution Class\n\n- "+string(class)+"\n\n### Covers\n\n- SPEC-001\n\n### Handoff\n\ncoordination complete", model.BodyOptions{Status: "done", Links: map[string][]string{"Related Comments": {taskURL}, "PR": {prURL}}})
		if err != nil {
			t.Fatal(err)
		}
		return ProcessEvidenceInput{Process: model.Artifact{URL: "https://example/process", Comment: model.ParseTypedComment(body)}, RequiredPRURL: prURL,
			ActiveSpecs: active, TaskURLs: map[string]bool{model.NormalizeURL(taskURL): true}}
	}
	cases := []struct {
		class model.ProcessExecutionClass
		add   func(*ProcessEvidenceInput)
	}{
		{model.ProcessExecutionChangeBearing, func(in *ProcessEvidenceInput) {
			in.Rationales = []RationaleEvidence{{ProcessID: "PROCESS-001", SpecID: "SPEC-001", SpecURL: active["SPEC-001"], MarkerPath: "a.go", MarkerLine: 12, CommentPath: "a.go", CommentLine: 12}}
		}},
		{model.ProcessExecutionReview, func(in *ProcessEvidenceInput) {
			in.Reviews = []ReviewEvidence{{ProcessID: "PROCESS-001", SpecID: "SPEC-001", Done: true}}
		}},
		{model.ProcessExecutionVerification, func(in *ProcessEvidenceInput) {
			in.Verifications = []VerificationEvidence{{ProcessID: "PROCESS-001", SpecID: "SPEC-001", Done: true, TestEvidence: true}}
		}},
		{model.ProcessExecutionOrchestration, func(*ProcessEvidenceInput) {}},
		{model.ProcessExecutionExternal, func(in *ProcessEvidenceInput) {
			in.External = []ExternalProcessEvidence{{ProcessID: "PROCESS-001", SpecID: "SPEC-001", SubjectRevision: "abc", EvidenceRevision: "abc", Consumed: true, EvidenceIDs: []string{"check-1"}, Trusted: true, Source: "provider:check-1"}}
		}},
	}
	for _, tc := range cases {
		t.Run(string(tc.class), func(t *testing.T) {
			input := base(tc.class)
			tc.add(&input)
			report := EvaluateProcessEvidence(input, TargetFinal, ModeAuthoritative)
			if len(report.Missing) != 0 {
				t.Fatalf("unexpected missing evidence: %+v", report)
			}
		})
	}
}

func TestProcessEvidenceCarrierRevisionTrustAndMixing(t *testing.T) {
	input := processEvidenceFixture(t, model.ProcessExecutionVerification)
	input.Checks = []CheckEvidence{
		{ProcessID: "PROCESS-001", SpecID: "SPEC-001", Name: "unit", Required: true, Passed: true, TestEvidence: true,
			SubjectRevision: "head-new", Trusted: true, Source: "github-check-run:1"},
	}
	report := EvaluateProcessEvidence(input, TargetFinal, ModeAuthoritative)
	if !report.CarrierRevision.Known || !report.CarrierRevision.Trusted || report.CarrierRevision.Revision != "head-new" {
		t.Fatalf("exact check-run head was not retained: %+v", report.CarrierRevision)
	}

	input.Checks = append(input.Checks, CheckEvidence{ProcessID: "PROCESS-001", SpecID: "SPEC-001", Name: "integration",
		Required: true, Passed: true, TestEvidence: true, SubjectRevision: "head-old", Trusted: true, Source: "github-check-run:2"})
	report = EvaluateProcessEvidence(input, TargetFinal, ModeAuthoritative)
	if !report.CarrierRevision.Known || report.CarrierRevision.Trusted {
		t.Fatalf("mixed required carrier revisions must fail closed: %+v", report.CarrierRevision)
	}
}

func TestProcessEvidenceLegacyTypedCarrierHasUnknownRevision(t *testing.T) {
	input := processEvidenceFixture(t, model.ProcessExecutionReview)
	input.Reviews = []ReviewEvidence{{ProcessID: "PROCESS-001", SpecID: "SPEC-001", Done: true, Source: "typed-review"}}
	report := EvaluateProcessEvidence(input, TargetFinal, ModeAuthoritative)
	if !containsString(report.Satisfied, "review evidence") || report.CarrierRevision.Known || report.CarrierRevision.Trusted {
		t.Fatalf("legacy typed evidence must remain semantically visible but revision-unknown: %+v", report)
	}
	facts := ProcessCarrierRevisionFacts([]ProcessEvidenceReport{report})
	if fact, ok := facts["PROCESS-001"]; !ok || fact.Known || fact.Trusted {
		t.Fatalf("unexpected helper projection: %+v", facts)
	}
}

func TestProcessEvidenceRejectsForgedRationalePathLine(t *testing.T) {
	input := processEvidenceFixture(t, model.ProcessExecutionChangeBearing)
	input.Rationales = []RationaleEvidence{{ProcessID: "PROCESS-001", SpecID: "SPEC-001", MarkerPath: "forged.go", MarkerLine: 99, CommentPath: "real.go", CommentLine: 12}}
	report := EvaluateProcessEvidence(input, TargetFinal, ModeAuthoritative)
	if !containsString(report.Missing, "matching inline rationale") {
		t.Fatalf("forged marker must not satisfy evidence: %+v", report)
	}
}

func TestProcessEvidenceLegacyAndUnknownClass(t *testing.T) {
	legacy := processEvidenceFixture(t, "")
	legacy.Process.Comment.Body = removeExecutionClass(legacy.Process.Comment.Body)
	legacy.Process.Comment = model.ParseTypedComment(legacy.Process.Comment.Body)
	report := EvaluateProcessEvidence(legacy, TargetFinal, ModeAuthoritative)
	if report.ExecutionClass != model.ProcessExecutionChangeBearing || report.ExplicitClass || !hasDiagnostic(report.Diagnostics, CodeProcessExecutionClassLegacy, false) {
		t.Fatalf("legacy projection mismatch: %+v", report)
	}

	unknown := processEvidenceFixture(t, model.ProcessExecutionReview)
	unknown.Process.Comment.Body = replaceExecutionClass(unknown.Process.Comment.Body, "deploy")
	unknown.Process.Comment = model.ParseTypedComment(unknown.Process.Comment.Body)
	report = EvaluateProcessEvidence(unknown, TargetFinal, ModeAuthoritative)
	if !hasDiagnostic(report.Diagnostics, CodeProcessExecutionClassInvalid, true) {
		t.Fatalf("unknown class must block: %+v", report)
	}
}

func TestReferencesArtifactIDRejectsPrefixCollisions(t *testing.T) {
	for _, tc := range []struct{ body, id string }{
		{"verified PROCESS-0010", "PROCESS-001"},
		{"covered SPEC-0010", "SPEC-001"},
	} {
		if ReferencesArtifactID(tc.body, tc.id) {
			t.Fatalf("%q must not be an exact reference to %s", tc.body, tc.id)
		}
	}
	for _, tc := range []struct{ body, id string }{
		{"verified PROCESS-001.", "PROCESS-001"},
		{"- SPEC-001", "SPEC-001"},
	} {
		if !ReferencesArtifactID(tc.body, tc.id) {
			t.Fatalf("%q should reference %s", tc.body, tc.id)
		}
	}
}

func TestOrchestrationSpecCoverageRejectsPrefixCollision(t *testing.T) {
	input := processEvidenceFixture(t, model.ProcessExecutionOrchestration)
	input.Process.Comment.Body += "\n\n### Covers\n\n- SPEC-0010\n\n### Handoff\n\ncoordination complete\n"
	input.Process.Comment = model.ParseTypedComment(input.Process.Comment.Body)
	report := EvaluateProcessEvidence(input, TargetFinal, ModeAuthoritative)
	if !containsString(report.Missing, "active SPEC coverage") {
		t.Fatalf("SPEC-0010 must not cover active SPEC-001: %+v", report)
	}
}

func TestReviewProcessRejectsSelfReviewByAgentName(t *testing.T) {
	// Reviewer --agent name equals a code author of the same SPEC: blocked.
	input := processEvidenceFixture(t, model.ProcessExecutionReview)
	input.AuthorAgentsBySpec = map[string]map[string]bool{"SPEC-001": {"coordinator": true}}
	input.Reviews = []ReviewEvidence{{ProcessID: "PROCESS-001", SpecID: "SPEC-001", Done: true, ReviewerAgent: "Coordinator"}}
	report := EvaluateProcessEvidence(input, TargetFinal, ModeAuthoritative)
	if !hasDiagnostic(report.Diagnostics, CodeProcessReviewAuthorConflict, true) {
		t.Fatalf("self-review by same agent name must block: %+v", report)
	}
	if containsString(report.Satisfied, "review evidence") {
		t.Fatalf("conflicted review must not count as satisfied: %+v", report)
	}

	// Different reviewer name for the same SPEC: independent review passes.
	input.Reviews = []ReviewEvidence{{ProcessID: "PROCESS-001", SpecID: "SPEC-001", Done: true, ReviewerAgent: "Independent Reviewer"}}
	report = EvaluateProcessEvidence(input, TargetFinal, ModeAuthoritative)
	if hasDiagnostic(report.Diagnostics, CodeProcessReviewAuthorConflict, true) || len(report.Missing) != 0 {
		t.Fatalf("independent reviewer must pass: %+v", report)
	}

	// Same name but author of a different SPEC only: no cross-SPEC false positive.
	input.AuthorAgentsBySpec = map[string]map[string]bool{"SPEC-999": {"coordinator": true}}
	input.Reviews = []ReviewEvidence{{ProcessID: "PROCESS-001", SpecID: "SPEC-001", Done: true, ReviewerAgent: "Coordinator"}}
	report = EvaluateProcessEvidence(input, TargetFinal, ModeAuthoritative)
	if hasDiagnostic(report.Diagnostics, CodeProcessReviewAuthorConflict, true) || len(report.Missing) != 0 {
		t.Fatalf("reviewer who authored a different SPEC must pass: %+v", report)
	}
}

func TestReviewProcessRejectsMultiSpecConflictOnSameArtifact(t *testing.T) {
	// One REVIEW artifact covers two active SPECs. The reviewer authored code
	// for SPEC-001, so the whole artifact is conflicted; a clean SPEC-002 on the
	// same artifact MUST NOT rescue it.
	input := processEvidenceFixture(t, model.ProcessExecutionReview)
	input.ActiveSpecs = map[string]string{"SPEC-001": "https://example/spec1", "SPEC-002": "https://example/spec2"}
	input.AuthorAgentsBySpec = map[string]map[string]bool{"SPEC-001": {"coordinator": true}}
	const reviewURL = "https://example/issues/9#issuecomment-review"
	input.Reviews = []ReviewEvidence{
		{ProcessID: "PROCESS-001", SpecID: "SPEC-001", URL: reviewURL, Done: true, ReviewerAgent: "Coordinator"},
		{ProcessID: "PROCESS-001", SpecID: "SPEC-002", URL: reviewURL, Done: true, ReviewerAgent: "Coordinator"},
	}
	report := EvaluateProcessEvidence(input, TargetFinal, ModeAuthoritative)
	if !hasDiagnostic(report.Diagnostics, CodeProcessReviewAuthorConflict, true) {
		t.Fatalf("clean SPEC on a conflicted REVIEW artifact must not rescue it: %+v", report)
	}
	if containsString(report.Satisfied, "review evidence") {
		t.Fatalf("conflicted REVIEW artifact must not count as satisfied: %+v", report)
	}

	// A separate, fully independent REVIEW artifact for SPEC-002 still satisfies.
	input.Reviews = append(input.Reviews, ReviewEvidence{ProcessID: "PROCESS-001", SpecID: "SPEC-002",
		URL: "https://example/issues/9#issuecomment-review2", Done: true, ReviewerAgent: "Independent Reviewer"})
	report = EvaluateProcessEvidence(input, TargetFinal, ModeAuthoritative)
	if !containsString(report.Satisfied, "review evidence") || containsString(report.Missing, "review evidence") {
		t.Fatalf("independent REVIEW artifact must satisfy the node: %+v", report)
	}
}

func processEvidenceFixture(t *testing.T, class model.ProcessExecutionClass) ProcessEvidenceInput {
	t.Helper()
	if class == "" {
		class = model.ProcessExecutionChangeBearing
	}
	const taskURL, prURL = "https://example/task", "https://example/pr"
	body, err := model.EnsureTypedBody("PROCESS", "PROCESS-001", "## Process: p\n\n### Parent TASK\n\n- TASK-001\n\n### Execution Class\n\n- "+string(class)+"\n\n### Handoff\n\nN/A", model.BodyOptions{Status: "done", Links: map[string][]string{"Related Comments": {taskURL}, "PR": {prURL}}})
	if err != nil {
		t.Fatal(err)
	}
	return ProcessEvidenceInput{Process: model.Artifact{URL: "https://example/process", Comment: model.ParseTypedComment(body)}, RequiredPRURL: prURL, ActiveSpecs: map[string]string{"SPEC-001": "https://example/spec"}, TaskURLs: map[string]bool{model.NormalizeURL(taskURL): true}}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
func hasDiagnostic(values []Diagnostic, code string, blocking bool) bool {
	for _, value := range values {
		if value.Code == code && value.Blocking == blocking {
			return true
		}
	}
	return false
}
func removeExecutionClass(body string) string { return replaceExecutionClass(body, "") }
func replaceExecutionClass(body, value string) string {
	start := "### Execution Class\n\n- "
	index := len(body)
	if i := find(body, start); i >= 0 {
		index = i
	}
	if index == len(body) {
		return body
	}
	end := index + len(start)
	for end < len(body) && body[end] != '\n' {
		end++
	}
	replacement := ""
	if value != "" {
		replacement = start + value
	}
	return body[:index] + replacement + body[end:]
}
func find(value, sub string) int {
	for i := 0; i+len(sub) <= len(value); i++ {
		if value[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
