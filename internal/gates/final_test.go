package gates

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/higress-group/issue-spec/internal/assignment"
	"github.com/higress-group/issue-spec/internal/model"
)

const (
	minimalFinalRevision = "0123456789abcdef0123456789abcdef01234567"
	minimalFinalSubject  = "https://example.test/pulls/7"
)

func TestMinimalFinalFactGroupsFailIndependently(t *testing.T) {
	tests := []struct {
		name string
		code string
		edit func(*Snapshot)
	}{
		{name: "current subject", code: CodeFinalSubjectUnknown, edit: func(snapshot *Snapshot) {
			snapshot.FinalEvidence.Subject.Known = false
		}},
		{name: "active selection", code: CodeFinalSelectionInvalid, edit: func(snapshot *Snapshot) {
			duplicate := snapshot.Artifacts[2]
			duplicate.URL += "-duplicate"
			snapshot.Artifacts = append(snapshot.Artifacts, duplicate)
		}},
		{name: "planning chain", code: CodeFinalPlanningInvalid, edit: func(snapshot *Snapshot) {
			snapshot.Artifacts[1].Comment.Links["Related Comments"] = nil
		}},
		{name: "PROCESS code subject", code: CodeProcessPRLinkMissing, edit: func(snapshot *Snapshot) {
			snapshot.Artifacts[2].Comment.Links["PR"] = nil
		}},
		{name: "exact code carrier", code: CodeProcessCarrierMissing, edit: func(snapshot *Snapshot) {
			snapshot.ProcessEvidence[0].Rationales = nil
		}},
		{name: "independent review", code: CodeProcessReviewRequired, edit: func(snapshot *Snapshot) {
			snapshot.FinalEvidence.Records = recordsWithoutKind(snapshot.FinalEvidence.Records, FinalEvidenceReview)
		}},
		{name: "independent verification", code: CodeFinalVerificationRequired, edit: func(snapshot *Snapshot) {
			snapshot.FinalEvidence.Records = recordsWithoutKind(snapshot.FinalEvidence.Records, FinalEvidenceVerification)
		}},
		{name: "bounded canonical index", code: CodeFinalEvidenceInvalid, edit: func(snapshot *Snapshot) {
			snapshot.FinalEvidence.Index.Passed = false
			snapshot.FinalEvidence.Index.Current = "conflicting receipt identity"
		}},
		{name: "blocking findings", code: CodeReviewFindingsOpen, edit: func(snapshot *Snapshot) {
			snapshot.Remote.ReviewFindings.Passed = false
			snapshot.Remote.ReviewFindings.Current = "blocking=1"
		}},
		{name: "sealed test", code: CodeFinalRequiredTestMissing, edit: func(snapshot *Snapshot) {
			snapshot.FinalEvidence.Records = recordsWithoutKind(snapshot.FinalEvidence.Records, FinalEvidenceTest)
		}},
		{name: "sealed provider check", code: CodeFinalRequiredCheckMissing, edit: func(snapshot *Snapshot) {
			snapshot.FinalEvidence.Records = recordsWithoutKind(snapshot.FinalEvidence.Records, FinalEvidenceCheck)
		}},
		{name: "direct evidence identity", code: CodeFinalEvidenceInvalid, edit: func(snapshot *Snapshot) {
			snapshot.FinalEvidence.Records[0].SubjectRevision = "stale"
		}},
		{name: "direct evidence kind", code: CodeFinalEvidenceInvalid, edit: func(snapshot *Snapshot) {
			snapshot.FinalEvidence.Records[0].Kind = "prose"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := minimalFinalSnapshot(t, ModeAuthoritative)
			test.edit(&snapshot)
			report := evaluate(t, snapshot)
			if report.Ready || !containsCode(report, test.code) {
				t.Fatalf("fact group did not fail closed with %s: %+v", test.code, report.Diagnostics)
			}
		})
	}
}

func TestMinimalFinalForecastAndAuthoritativeDecisionsMatch(t *testing.T) {
	forecastSnapshot := minimalFinalSnapshot(t, ModeForecast)
	forecastSnapshot.FinalEvidence.Records = recordsWithoutKind(forecastSnapshot.FinalEvidence.Records, FinalEvidenceVerification)
	forecast := evaluate(t, forecastSnapshot)
	authoritativeSnapshot := minimalFinalSnapshot(t, ModeAuthoritative)
	authoritativeSnapshot.FinalEvidence.Records = recordsWithoutKind(authoritativeSnapshot.FinalEvidence.Records, FinalEvidenceVerification)
	authoritative := evaluate(t, authoritativeSnapshot)
	if forecast.Ready != authoritative.Ready || !reflect.DeepEqual(diagnosticCodes(forecast), diagnosticCodes(authoritative)) {
		t.Fatalf("minimal final policy drifted by mode: forecast=%v/%v authoritative=%v/%v",
			forecast.Ready, diagnosticCodes(forecast), authoritative.Ready, diagnosticCodes(authoritative))
	}
	if !forecast.PointInTime || authoritative.PointInTime {
		t.Fatalf("mode projection lost point-in-time semantics: forecast=%+v authoritative=%+v", forecast, authoritative)
	}
}

func TestMinimalFinalIgnoresUnrelatedLifecycleAndArchiveBookkeeping(t *testing.T) {
	baseline := evaluate(t, minimalFinalSnapshot(t, ModeAuthoritative))
	if !baseline.Ready || len(baseline.Diagnostics) != 0 {
		t.Fatalf("baseline minimal snapshot is not ready: %+v", baseline.Diagnostics)
	}

	snapshot := minimalFinalSnapshot(t, ModeAuthoritative)
	// Lifecycle status and generic navigation are not final evidence authority.
	snapshot.Artifacts[0].Comment.Status = "draft"
	snapshot.Artifacts[1].Comment.Status = "ready"
	snapshot.Artifacts[2].Comment.Status = "in-progress"
	snapshot.Artifacts[0].Comment.Links["Related Comments"] = []string{"https://unrelated.test/comments/1"}
	snapshot.Artifacts[1].Comment.Links["Related Comments"] = append(snapshot.Artifacts[1].Comment.Links["Related Comments"], "https://unrelated.test/comments/2")
	snapshot.Artifacts[2].Comment.Links["Related Comments"] = append(snapshot.Artifacts[2].Comment.Links["Related Comments"], "https://unrelated.test/comments/3")
	snapshot.Artifacts = append(snapshot.Artifacts,
		model.Artifact{URL: "https://example.test/comments/QUESTION-001", Comment: model.TypedComment{Type: "QUESTION", ID: "QUESTION-001", Status: "blocked", Body: "project prose"}},
		model.Artifact{URL: "https://example.test/comments/ARCHIVE-001", Comment: model.TypedComment{Type: "ARCHIVE", ID: "ARCHIVE-001", Status: "blocked", Body: "durable archive state"}},
	)
	snapshot.Traceability = TraceabilityFacts{Observed: true, Report: model.VerifyReport{OK: false, Errors: []string{"unrelated backlink"}}}
	snapshot.Canonical = CanonicalFacts{Observed: true, Diagnostics: []model.CanonicalDiagnostic{{Type: "QUESTION", ID: "QUESTION-001", Message: "unrelated prose"}}}
	snapshot.Workflow = WorkflowFacts{Required: true, Known: true, Valid: false, Errors: []string{"project prose invalid"}}
	snapshot.Remote.PRChecks = Fact{Required: true, Known: true, Passed: false, Current: "unassigned provider check"}
	snapshot.Remote.ProviderEvidence = Fact{Required: true, Known: true, Passed: false, Current: "unrelated provider fact"}
	snapshot.Remote.Workspace = WorkspaceFacts{Observed: true, ExpectedRevision: Fact{Required: true, Known: true, Passed: false}}
	historicalBody, err := model.EnsureTypedBody("PROCESS", "PROCESS-099",
		"## Process: historical\n\n### Parent TASK\n\n- TASK-001\n\n### Execution Class\n\n- change-bearing", model.BodyOptions{Status: "superseded"})
	if err != nil {
		t.Fatal(err)
	}
	historicalBody, _, err = model.StampSupersededBy(historicalBody, "PROCESS-099",
		model.SupersededBy{ProcessID: "PROCESS-001", URL: snapshot.Artifacts[2].URL})
	if err != nil {
		t.Fatal(err)
	}
	historicalBody = strings.Replace(historicalBody, "## Process: historical", "arbitrary historical body", 1)
	snapshot.Artifacts = append(snapshot.Artifacts, model.Artifact{URL: "https://example.test/comments/PROCESS-099",
		Comment: model.ParseTypedComment(historicalBody)})

	report := evaluate(t, snapshot)
	if !report.Ready || len(report.Diagnostics) != 0 {
		t.Fatalf("unrelated bookkeeping changed minimal readiness: %+v", report.Diagnostics)
	}
}

func TestMinimalFinalCompactProjectionIsDeterministicAndBounded(t *testing.T) {
	success := evaluate(t, minimalFinalSnapshot(t, ModeAuthoritative))
	compactSuccess := ProjectCompactSummary(success, nil, &CompactSubject{Revision: minimalFinalRevision},
		Remediation{CommandFamily: "verify", Arguments: []string{"--json"}})
	encodedSuccess, err := json.Marshal(compactSuccess)
	if err != nil {
		t.Fatal(err)
	}
	if !compactSuccess.OK || len(encodedSuccess) > 1024 {
		t.Fatalf("compact success is not bounded: ok=%v bytes=%d payload=%s", compactSuccess.OK, len(encodedSuccess), encodedSuccess)
	}

	snapshot := minimalFinalSnapshot(t, ModeAuthoritative)
	for index := 0; index < 64; index++ {
		snapshot.FinalEvidence.Records = append(snapshot.FinalEvidence.Records, FinalEvidenceRecord{
			ProcessID: fmt.Sprintf("PROCESS-%03d", index+100), SpecID: "SPEC-001", Kind: FinalEvidenceReview,
			EvidenceID: "unknown-evidence-" + string(rune('A'+index)), SubjectRevision: minimalFinalRevision, Source: "test",
		})
	}
	report := evaluate(t, snapshot)
	first := ProjectCompactSummary(report, nil, nil, Remediation{CommandFamily: "verify", Arguments: []string{"--json"}})
	second := ProjectCompactSummary(report, nil, nil, Remediation{CommandFamily: "verify", Arguments: []string{"--json"}})
	firstJSON, _ := json.Marshal(first)
	secondJSON, _ := json.Marshal(second)
	if first.OK || !reflect.DeepEqual(firstJSON, secondJSON) || len(firstJSON) > 4096 {
		t.Fatalf("compact blockers are not deterministic and bounded: ok=%v bytes=%d", first.OK, len(firstJSON))
	}
	truncated := false
	for _, blocker := range first.Blockers {
		if len(blocker.Affected) > compactAffectedLimit {
			t.Fatalf("blocker %s exposed %d affected identities", blocker.Code, len(blocker.Affected))
		}
		truncated = truncated || blocker.TruncatedCount > 0
	}
	if !truncated {
		t.Fatal("compact blocker did not report truncated affected identities")
	}
}

func TestFinalEvidenceAssignmentJoinReproducesActiveBoundTest(t *testing.T) {
	revision := strings.Repeat("b", 40)
	selector := assignment.TestSelector{ID: "durable", Command: "issue-spec durable-spec check --repo o/r --proposal 381 --root . --json",
		RevisionBinding: &assignment.RevisionBinding{Source: assignment.RevisionBindingSourceSubjectRevision,
			Argument: assignment.RevisionBindingArgumentSubject}}
	resolved, err := assignment.ResolveTestSelector(selector, revision)
	if err != nil {
		t.Fatal(err)
	}
	active := &ActiveAssignmentEvidence{ProcessID: "PROCESS-009", AssignmentID: "assignment-2",
		AssignmentDigest: strings.Repeat("d", 64), Generation: 2, Role: assignment.RoleVerification,
		SubjectRevision: revision, RequiredTests: []assignment.TestSelector{selector}}
	assigned := resolved.AssignedSelector
	record := FinalEvidenceRecord{ProcessID: "PROCESS-001", SpecID: "SPEC-001", Kind: FinalEvidenceTest,
		EvidenceID: "receipt-2:test:durable", Name: selector.ID, SubjectRevision: revision,
		Source: "accepted-verification-receipt:self-reported-tests", AssignmentProcessID: active.ProcessID,
		ReceiptID: "receipt-2", ReceiptDigest: strings.Repeat("a", 64), AssignmentID: active.AssignmentID,
		AssignmentDigest: active.AssignmentDigest, AssignmentGeneration: active.Generation,
		AssignmentRole:   assignment.RoleVerification,
		AssignedSelector: &assigned, ResolvedRevision: revision, ExecutedCommand: resolved.Command}
	inputs := map[string]ProcessEvidenceInput{active.ProcessID: {ActiveAssignment: active}}
	if err := validateFinalEvidenceAssignment(record, inputs, revision, map[string]string{}); err != nil {
		t.Fatal(err)
	}
	if matched, _ := matchingRequiredTestEvidence([]FinalEvidenceRecord{record}, selector, revision); !matched {
		t.Fatal("exact bound selector did not satisfy the target requirement")
	}
	changedRequirement := selector
	changedRequirement.Command += " --changed"
	if matched, sameID := matchingRequiredTestEvidence([]FinalEvidenceRecord{record}, changedRequirement, revision); matched || !sameID {
		t.Fatalf("same-named different selector matched=%t same_id=%t", matched, sameID)
	}
	reviewActive := *active
	reviewActive.ProcessID, reviewActive.AssignmentID = "PROCESS-010", "assignment-review-2"
	reviewActive.Role = assignment.RoleReview
	reviewRecord := record
	reviewRecord.AssignmentProcessID, reviewRecord.AssignmentID = reviewActive.ProcessID, reviewActive.AssignmentID
	reviewRecord.AssignmentRole = assignment.RoleReview
	reviewRecord.Source = "accepted-review-receipt:self-reported"
	if err := validateFinalEvidenceAssignment(reviewRecord,
		map[string]ProcessEvidenceInput{reviewActive.ProcessID: {ActiveAssignment: &reviewActive}},
		revision, map[string]string{}); err != nil {
		t.Fatalf("review-backed final test was rejected: %v", err)
	}
	verificationCompletion := record
	verificationCompletion.Kind, verificationCompletion.EvidenceID, verificationCompletion.Name = FinalEvidenceVerification, "receipt-2", ""
	verificationCompletion.AssignedSelector, verificationCompletion.ResolvedRevision, verificationCompletion.ExecutedCommand = nil, "", ""
	if err := validateFinalEvidenceAssignment(verificationCompletion, inputs, revision, map[string]string{}); err != nil {
		t.Fatalf("explicit verification completion role was rejected: %v", err)
	}
	reviewCompletion := reviewRecord
	reviewCompletion.Kind, reviewCompletion.EvidenceID, reviewCompletion.Name = FinalEvidenceReview, "receipt-review-2", ""
	reviewCompletion.AssignedSelector, reviewCompletion.ResolvedRevision, reviewCompletion.ExecutedCommand = nil, "", ""
	if err := validateFinalEvidenceAssignment(reviewCompletion,
		map[string]ProcessEvidenceInput{reviewActive.ProcessID: {ActiveAssignment: &reviewActive}},
		revision, map[string]string{}); err != nil {
		t.Fatalf("explicit review completion role was rejected: %v", err)
	}
	for name, mutate := range map[string]func(*FinalEvidenceRecord){
		"wrong generation": func(value *FinalEvidenceRecord) { value.AssignmentGeneration-- },
		"wrong digest":     func(value *FinalEvidenceRecord) { value.AssignmentDigest = strings.Repeat("e", 64) },
		"wrong selector": func(value *FinalEvidenceRecord) {
			changed := *value.AssignedSelector
			changed.Command += " --changed"
			value.AssignedSelector = &changed
		},
		"wrong expanded command": func(value *FinalEvidenceRecord) { value.ExecutedCommand += " --forged" },
		"wrong test role":        func(value *FinalEvidenceRecord) { value.AssignmentRole = assignment.RoleReview },
		"wrong receipt source":   func(value *FinalEvidenceRecord) { value.Source = "accepted-review-receipt:self-reported" },
		"unassigned extra": func(value *FinalEvidenceRecord) {
			value.Name, value.EvidenceID = "extra", "receipt-2:test:extra"
			value.AssignedSelector, value.ResolvedRevision, value.ExecutedCommand = nil, "", "go test ./extra"
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := record
			mutate(&candidate)
			if err := validateFinalEvidenceAssignment(candidate, inputs, revision, map[string]string{}); err == nil {
				t.Fatal("final evaluator accepted mismatched assignment-bound evidence")
			}
		})
	}
	activeReceipts := map[string]string{}
	if err := validateFinalEvidenceAssignment(record, inputs, revision, activeReceipts); err != nil {
		t.Fatal(err)
	}
	duplicate := record
	duplicate.ReceiptID, duplicate.ReceiptDigest = "receipt-other", strings.Repeat("f", 64)
	if err := validateFinalEvidenceAssignment(duplicate, inputs, revision, activeReceipts); err == nil ||
		!strings.Contains(err.Error(), "duplicate active assignment generation") {
		t.Fatalf("duplicate active receipt error=%v", err)
	}
	checkSelector := assignment.CheckSelector{Provider: "github", Name: "test"}
	check := FinalEvidenceRecord{ProcessID: "PROCESS-001", SpecID: "SPEC-001", Kind: FinalEvidenceCheck,
		EvidenceID: "receipt-2:check:github:test", Name: "github\x00test", SubjectRevision: revision,
		Source: "accepted-verification-receipt:provider-checks", AssignmentProcessID: active.ProcessID,
		ReceiptID: record.ReceiptID, ReceiptDigest: record.ReceiptDigest, AssignmentID: active.AssignmentID,
		AssignmentDigest: active.AssignmentDigest, AssignmentGeneration: active.Generation,
		AssignmentRole: assignment.RoleVerification, CheckSelector: &checkSelector}
	if err := validateFinalEvidenceAssignment(check, inputs, revision, map[string]string{}); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*FinalEvidenceRecord){
		"missing check selector": func(value *FinalEvidenceRecord) { value.CheckSelector = nil },
		"wrong check name":       func(value *FinalEvidenceRecord) { value.Name = "github\x00other" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := check
			mutate(&candidate)
			if err := validateFinalEvidenceAssignment(candidate, inputs, revision, map[string]string{}); err == nil {
				t.Fatal("final evaluator accepted mismatched assignment-bound check evidence")
			}
		})
	}
}

func minimalFinalSnapshot(t *testing.T, mode Mode) Snapshot {
	t.Helper()
	spec := artifact(t, "SPEC", "SPEC-001", "confirmed", specLogical)
	taskBody, err := model.EnsureTypedBody("TASK", "TASK-001", taskLogical, model.BodyOptions{
		Status: "ready", Links: map[string][]string{"Related Comments": {spec.URL}},
	})
	if err != nil {
		t.Fatal(err)
	}
	task := model.Artifact{Issue: 1, CommentID: 2, URL: "https://example.test/comments/TASK-001", Comment: model.ParseTypedComment(taskBody)}
	processLogical := "## Process: implementation\n\n### Parent TASK\n\n- TASK-001\n\n### Execution Class\n\n- change-bearing\n\n### Covers\n\n- SPEC-001\n\n### Handoff\n\nN/A"
	processBody, err := model.EnsureTypedBody("PROCESS", "PROCESS-001", processLogical, model.BodyOptions{
		Status: "in-progress", Links: map[string][]string{
			"Related Comments": {task.URL, spec.URL},
			"PR":               {minimalFinalSubject},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	process := model.Artifact{Issue: 1, CommentID: 3, URL: "https://example.test/comments/PROCESS-001", Comment: model.ParseTypedComment(processBody)}
	process.Comment.Assignment = &assignment.ProcessInput{
		ScenarioSelectors: []assignment.ScenarioRef{{SpecID: "SPEC-001", Scenario: "evaluate"}},
		RequiredTests:     []assignment.TestSelector{{ID: "unit", Command: "go test ./internal/gates"}},
		RequiredChecks:    []assignment.CheckSelector{{Provider: "github", Name: "test"}},
	}
	input := ProcessEvidenceInput{
		Process: process, RequiredPRURL: minimalFinalSubject,
		ActiveSpecs: map[string]string{"SPEC-001": spec.URL},
		TaskURLs:    map[string]bool{model.NormalizeURL(task.URL): true},
		Rationales: []RationaleEvidence{{ProcessID: "PROCESS-001", SpecID: "SPEC-001", SpecURL: spec.URL,
			MarkerPath: "internal/gates/final.go", MarkerLine: 1, CommentPath: "internal/gates/final.go", CommentLine: 1, AuthorAgent: "Implementation Worker"}},
		AuthorAgentsBySpec: map[string]map[string]bool{"SPEC-001": {"implementation worker": true}},
	}
	active := &ActiveAssignmentEvidence{ProcessID: "PROCESS-001", AssignmentID: "assignment-verify-1",
		AssignmentDigest: strings.Repeat("d", 64), Generation: 1, Role: assignment.RoleVerification,
		SubjectRevision: minimalFinalRevision,
		RequiredTests:   []assignment.TestSelector{{ID: "unit", Command: "go test ./internal/gates"}},
		RequiredChecks:  []assignment.CheckSelector{{Provider: "github", Name: "test"}}}
	input.ActiveAssignment = active
	verificationIdentity := FinalEvidenceRecord{AssignmentProcessID: active.ProcessID,
		ReceiptID: "receipt-verify-1", ReceiptDigest: strings.Repeat("a", 64), AssignmentID: active.AssignmentID,
		AssignmentDigest: active.AssignmentDigest, AssignmentGeneration: active.Generation,
		AssignmentRole: assignment.RoleVerification}
	verification := verificationIdentity
	verification.ProcessID, verification.SpecID, verification.Kind = "PROCESS-001", "SPEC-001", FinalEvidenceVerification
	verification.EvidenceID, verification.SubjectRevision = "verify-1", minimalFinalRevision
	verification.Source, verification.Independent = "accepted-verification-receipt:self-reported-tests", true
	test := verificationIdentity
	test.ProcessID, test.SpecID, test.Kind = "PROCESS-001", "SPEC-001", FinalEvidenceTest
	test.EvidenceID, test.Name, test.SubjectRevision = "verify-1:test:unit", "unit", minimalFinalRevision
	test.Source, test.ExecutedCommand, test.Independent = "accepted-verification-receipt:self-reported-tests", "go test ./internal/gates", true
	checkSelector := assignment.CheckSelector{Provider: "github", Name: "test"}
	check := verificationIdentity
	check.ProcessID, check.SpecID, check.Kind = "PROCESS-001", "SPEC-001", FinalEvidenceCheck
	check.EvidenceID, check.Name, check.SubjectRevision = "verify-1:check:github:test", "github\x00test", minimalFinalRevision
	check.Source, check.CheckSelector, check.Independent = "accepted-verification-receipt:self-reported-tests", &checkSelector, true
	return Snapshot{
		Target: TargetFinal, Mode: mode, Artifacts: []model.Artifact{spec, task, process},
		ProcessEvidence: []ProcessEvidenceInput{input},
		Remote:          RemoteFacts{ReviewFindings: Fact{Required: true, Known: true, Passed: true, Current: "blocking=0", Expected: "blocking=0"}},
		FinalEvidence: FinalEvidenceSnapshot{
			Observed: true,
			Subject: FinalSubject{Required: true, Known: true, Trusted: true, Kind: "pull_request", URL: minimalFinalSubject,
				Revision: minimalFinalRevision, Source: "github-pull-request-head:7"},
			Index: Fact{Required: true, Known: true, Passed: true, Current: "4 records", Expected: "validated bounded exact-current index"},
			Records: []FinalEvidenceRecord{
				{ProcessID: "PROCESS-001", SpecID: "SPEC-001", Kind: FinalEvidenceReview, EvidenceID: "review-1", SubjectRevision: minimalFinalRevision, Source: "github-pr-review-comment:review-1", Independent: true},
				verification,
				test,
				check,
			},
		},
	}
}

func recordsWithoutKind(records []FinalEvidenceRecord, kind FinalEvidenceKind) []FinalEvidenceRecord {
	result := make([]FinalEvidenceRecord, 0, len(records))
	for _, record := range records {
		if record.Kind != kind {
			result = append(result, record)
		}
	}
	return result
}
