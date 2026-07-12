package gates

import (
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/higress-group/issue-spec/internal/model"
)

const (
	specLogical = "## Requirement: evaluator\n\nThe evaluator MUST be deterministic.\n\n### Scenario: evaluate\n\n- **WHEN** a snapshot is evaluated\n- **THEN** stable diagnostics are returned"
	taskLogical = "## Task: evaluator\n\n### Execution Planning\n\n- Owned modules / write areas:\n  - internal/gates\n- Coupling class: low\n- Recommended execution mode: coordinator-owned\n\n### Covers\n\n- SPEC-001"
)

func TestEvaluateRejectsUnsupportedTargetAndMode(t *testing.T) {
	if _, err := Evaluate(Snapshot{Target: "release", Mode: ModeForecast}); err == nil {
		t.Fatal("unsupported target should fail")
	}
	if _, err := Evaluate(Snapshot{Target: TargetFinal, Mode: "maybe"}); err == nil {
		t.Fatal("unsupported mode should fail")
	}
}

func TestEvaluateStageRequirements(t *testing.T) {
	tests := []struct {
		name   string
		target Target
		codes  []string
	}{
		{name: "proposal", target: TargetProposal, codes: []string{CodeSpecRequired}},
		{name: "design", target: TargetDesign, codes: []string{CodeSpecRequired, CodeTaskRequired}},
		{name: "implement", target: TargetImplement, codes: []string{CodeProcessRequired, CodeSpecRequired, CodeTaskRequired}},
		{name: "final", target: TargetFinal, codes: []string{CodeProcessRequired, CodeSpecRequired, CodeTaskRequired, CodeVerifyRequired}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			report := evaluate(t, Snapshot{Target: test.target, Mode: ModeForecast})
			if report.Ready {
				t.Fatal("empty snapshot must not be ready")
			}
			if got := diagnosticCodes(report); !reflect.DeepEqual(got, test.codes) {
				t.Fatalf("codes = %v, want %v", got, test.codes)
			}
			for _, diagnostic := range report.Diagnostics {
				if diagnostic.Gate != test.target || diagnostic.Remediation.CommandFamily == "" || diagnostic.Freshness != FreshnessLocal {
					t.Fatalf("incomplete diagnostic: %+v", diagnostic)
				}
			}
		})
	}
}

func TestEvaluateLocallyKnowableFinalRules(t *testing.T) {
	spec := artifact(t, "SPEC", "SPEC-001", "draft", specLogical)
	question := artifact(t, "QUESTION", "QUESTION-001", "blocked", "## Question\n\nReady?")
	task := artifact(t, "TASK", "TASK-001", "ready", taskLogical)
	processOne := artifact(t, "PROCESS", "PROCESS-001", "done", processLogical("N/A", "N/A"))
	processTwo := artifact(t, "PROCESS", "PROCESS-002", "in-progress", processLogical("PROCESS-001", "N/A"))
	review := artifact(t, "REVIEW", "REVIEW-001", "in-progress", "## Review\n\nPending")
	verify := artifact(t, "VERIFY", "VERIFY-001", "done", "## Verification Summary\n\nNo evidence yet.")
	link(t, &spec, &task)
	link(t, &task, &processOne)
	link(t, &task, &processTwo)

	report := evaluate(t, Snapshot{Target: TargetFinal, Mode: ModeForecast, Artifacts: []model.Artifact{
		spec, question, task, processOne, processTwo, review, verify,
	}})
	want := []string{
		CodeProcessHandoffMissing,
		CodeProcessNotDone,
		CodeQuestionBlocked,
		CodeReviewOpen,
		CodeSpecStatusInvalid,
		CodeTaskNotDone,
		CodeVerifySpecCoverageMissing,
		CodeVerifyTestEvidenceMissing,
	}
	if got := diagnosticCodes(report); !reflect.DeepEqual(got, want) {
		t.Fatalf("codes = %v, want %v\n%+v", got, want, report.Diagnostics)
	}
	for _, diagnostic := range report.Diagnostics {
		if diagnostic.Code == CodeTaskNotDone && (diagnostic.Artifact.ID != "TASK-001" || diagnostic.Current != "ready" || diagnostic.Expected != "done") {
			t.Fatalf("TASK diagnostic lost actionable identity: %+v", diagnostic)
		}
	}
}

func TestEvaluateReadyFinalSnapshot(t *testing.T) {
	spec, task, process, verify := readyChain(t)
	report := evaluate(t, Snapshot{Target: TargetFinal, Mode: ModeAuthoritative, Artifacts: []model.Artifact{spec, task, process, verify}})
	if !report.Ready || report.PointInTime || len(report.Diagnostics) != 0 {
		t.Fatalf("ready final report = %+v", report)
	}
}

func TestEvaluateSpecCoverageRequiresExactArtifactID(t *testing.T) {
	spec := artifact(t, "SPEC", "SPEC-001", "confirmed", specLogical)
	task := artifact(t, "TASK", "TASK-001", "done", taskLogical)
	process := artifact(t, "PROCESS", "PROCESS-001", "done", processLogical("N/A", "implementation complete"))
	verify := artifact(t, "VERIFY", "VERIFY-001", "done", "## Verification Summary\n\nSPEC-0010 covered by go test ./internal/gates.")
	link(t, &spec, &task)
	link(t, &task, &process)
	report := evaluate(t, Snapshot{Target: TargetFinal, Mode: ModeAuthoritative, Artifacts: []model.Artifact{spec, task, process, verify}})
	if report.Ready || !containsCode(report, CodeVerifySpecCoverageMissing) {
		t.Fatalf("SPEC-0010 must not cover active SPEC-001: %+v", report)
	}
}

func TestEvaluateCanonicalAndTraceabilityCanBeCollectedOrComputed(t *testing.T) {
	bad := artifact(t, "SPEC", "SPEC-001", "confirmed", "hand-written")
	computed := evaluate(t, Snapshot{Target: TargetProposal, Mode: ModeForecast, Artifacts: []model.Artifact{bad}})
	if !containsCode(computed, CodeArtifactNoncanonical) {
		t.Fatalf("computed canonical diagnostic missing: %+v", computed.Diagnostics)
	}

	var observed Snapshot
	observed.Target, observed.Mode = TargetProposal, ModeForecast
	observed.Artifacts = []model.Artifact{artifact(t, "SPEC", "SPEC-001", "confirmed", specLogical)}
	observed.Canonical.Observed = true
	observed.Canonical.Diagnostics = []model.CanonicalDiagnostic{{Type: "SPEC", ID: "SPEC-009", URL: "https://example.test/c", Element: "scenario-heading", Message: "missing scenario"}}
	observed.Traceability.Observed = true
	observed.Traceability.Report = model.VerifyReport{OK: false, Errors: []string{"synthetic trace failure"}}
	report := evaluate(t, observed)
	if got := diagnosticCodes(report); !reflect.DeepEqual(got, []string{CodeArtifactNoncanonical, CodeTraceabilityInvalid}) {
		t.Fatalf("observed codes = %v", got)
	}
	if report.Diagnostics[0].Artifact.ID != "SPEC-009" {
		t.Fatalf("observed canonical identity not preserved: %+v", report.Diagnostics[0])
	}
}

func TestEvaluateWorkflowFacts(t *testing.T) {
	spec := artifact(t, "SPEC", "SPEC-001", "confirmed", specLogical)
	unknown := Snapshot{Target: TargetProposal, Mode: ModeForecast, Artifacts: []model.Artifact{spec}, Workflow: WorkflowFacts{Required: true}}
	report := evaluate(t, unknown)
	diagnostic := findDiagnostic(t, report, CodeWorkflowUnknown)
	if diagnostic.Freshness != FreshnessUnknown || diagnostic.Current != "unknown" {
		t.Fatalf("workflow unknown diagnostic = %+v", diagnostic)
	}

	unknown.Workflow = WorkflowFacts{Required: true, Known: true, Errors: []string{"schema invalid"}}
	report = evaluate(t, unknown)
	if !containsCode(report, CodeWorkflowInvalid) || containsCode(report, CodeWorkflowUnknown) {
		t.Fatalf("workflow invalid diagnostics = %+v", report.Diagnostics)
	}
}

func TestEvaluateRemoteFactsDistinguishesUnknownAndPointInTimeFailure(t *testing.T) {
	spec, task, process, verify := readyChain(t)
	observed := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	snapshot := Snapshot{Target: TargetFinal, Mode: ModeForecast, Artifacts: []model.Artifact{spec, task, process, verify}}
	snapshot.Remote.PRChecks = Fact{Required: true}
	snapshot.Remote.ReviewFindings = Fact{Required: true, Known: true, Passed: false, Current: "1 open P1", Expected: "no blocking findings", ObservedAt: &observed}
	snapshot.Remote.ProviderEvidence = Fact{Required: true, Known: true, Passed: true}
	snapshot.Remote.Processes = []ProcessEvidenceFact{{
		ProcessID: "PROCESS-001", ProcessURL: process.URL,
		PRLink:   Fact{Required: true, Known: true, Passed: false, Current: "missing", Expected: "linked"},
		Evidence: Fact{Required: true},
	}}
	report := evaluate(t, snapshot)
	if report.Ready || !report.PointInTime {
		t.Fatalf("forecast with gaps must be non-ready point-in-time: %+v", report)
	}
	for _, code := range []string{CodePRChecksUnknown, CodeProcessEvidenceUnknown, CodeProcessPRLinkMissing, CodeReviewFindingsOpen} {
		if !containsCode(report, code) {
			t.Fatalf("missing %s: %+v", code, report.Diagnostics)
		}
	}
	if got := findDiagnostic(t, report, CodePRChecksUnknown).Freshness; got != FreshnessUnknown {
		t.Fatalf("checks freshness = %s", got)
	}
	finding := findDiagnostic(t, report, CodeReviewFindingsOpen)
	if finding.Freshness != FreshnessPointInTime || finding.ObservedAt == nil || !finding.ObservedAt.Equal(observed) {
		t.Fatalf("finding freshness = %+v", finding)
	}

	snapshot.Mode = ModeAuthoritative
	authoritative := evaluate(t, snapshot)
	if authoritative.Ready || authoritative.PointInTime {
		t.Fatalf("authoritative unknown must fail closed: %+v", authoritative)
	}
	if !reflect.DeepEqual(diagnosticCodes(report), diagnosticCodes(authoritative)) {
		t.Fatalf("mode changed stable diagnostic codes: forecast=%v authoritative=%v", diagnosticCodes(report), diagnosticCodes(authoritative))
	}
}

func TestEvaluateArchiveRequiresDurableSpec(t *testing.T) {
	spec, task, process, verify := readyChain(t)
	snapshot := Snapshot{Target: TargetArchive, Mode: ModeAuthoritative, Artifacts: []model.Artifact{spec, task, process, verify}}
	snapshot.Remote.DurableSpec = Fact{Required: true}
	report := evaluate(t, snapshot)
	if !containsCode(report, CodeDurableSpecUnknown) || report.Ready {
		t.Fatalf("archive unknown durable spec = %+v", report)
	}
	snapshot.Remote.DurableSpec = Fact{Required: true, Known: true, Passed: true}
	if report = evaluate(t, snapshot); !report.Ready {
		t.Fatalf("valid durable spec should pass: %+v", report.Diagnostics)
	}
}

func TestEvaluateDiagnosticsAreDeterministicAndSupersededArtifactsAreIgnored(t *testing.T) {
	a := artifact(t, "QUESTION", "QUESTION-002", "blocked", "## Question\n\nTwo")
	b := artifact(t, "QUESTION", "QUESTION-001", "blocked", "## Question\n\nOne")
	superseded := artifact(t, "TASK", "TASK-999", "superseded", "not canonical")
	spec := artifact(t, "SPEC", "SPEC-001", "confirmed", specLogical)
	snapshot := Snapshot{Target: TargetProposal, Mode: ModeForecast, Artifacts: []model.Artifact{a, superseded, spec, b}}
	first := evaluate(t, snapshot)
	snapshot.Artifacts = []model.Artifact{b, spec, superseded, a}
	second := evaluate(t, snapshot)
	if !reflect.DeepEqual(first.Diagnostics, second.Diagnostics) {
		t.Fatalf("diagnostics depend on artifact order:\n%+v\n%+v", first.Diagnostics, second.Diagnostics)
	}
	if containsCode(first, CodeArtifactNoncanonical) || containsCode(first, CodeTaskNotDone) {
		t.Fatalf("superseded artifact affected report: %+v", first.Diagnostics)
	}
	ids := []string{first.Diagnostics[0].Artifact.ID, first.Diagnostics[1].Artifact.ID}
	if !reflect.DeepEqual(ids, []string{"QUESTION-001", "QUESTION-002"}) {
		t.Fatalf("artifact order = %v", ids)
	}
}

func TestLocalDiagnosticParityAcrossModes(t *testing.T) {
	spec := artifact(t, "SPEC", "SPEC-001", "draft", specLogical)
	task := artifact(t, "TASK", "TASK-001", "ready", taskLogical)
	process := artifact(t, "PROCESS", "PROCESS-001", "in-progress", processLogical("N/A", "N/A"))
	link(t, &spec, &task)
	link(t, &task, &process)
	snapshot := Snapshot{Target: TargetFinal, Mode: ModeForecast, Artifacts: []model.Artifact{spec, task, process}}
	forecast := evaluate(t, snapshot)
	snapshot.Mode = ModeAuthoritative
	authoritative := evaluate(t, snapshot)
	if !reflect.DeepEqual(diagnosticCodes(forecast), diagnosticCodes(authoritative)) {
		t.Fatalf("local parity failed: forecast=%v authoritative=%v", diagnosticCodes(forecast), diagnosticCodes(authoritative))
	}
	for _, code := range []string{CodeVerifyRequired, CodeVerifySpecCoverageMissing} {
		if !containsCode(forecast, code) {
			t.Fatalf("final snapshot without VERIFY must expose %s: %+v", code, forecast.Diagnostics)
		}
	}
}

func readyChain(t *testing.T) (model.Artifact, model.Artifact, model.Artifact, model.Artifact) {
	t.Helper()
	spec := artifact(t, "SPEC", "SPEC-001", "confirmed", specLogical)
	task := artifact(t, "TASK", "TASK-001", "done", taskLogical)
	process := artifact(t, "PROCESS", "PROCESS-001", "done", processLogical("N/A", "implementation complete"))
	verify := artifact(t, "VERIFY", "VERIFY-001", "done", "## Verification Summary\n\nSPEC-001 covered by go test ./internal/gates.")
	link(t, &spec, &task)
	link(t, &task, &process)
	return spec, task, process, verify
}

func processLogical(dependency, handoff string) string {
	return "## Process: evaluator\n\n### Owner\n\n- Worker\n\n### Parent TASK\n\n- TASK-001\n\n### Write Ownership\n\n- internal/gates\n\n### Dependencies\n\n- " + dependency + "\n\n### Covers\n\n- TASK-001\n\n### Handoff\n\n" + handoff
}

func artifact(t *testing.T, typ, id, status, logical string) model.Artifact {
	t.Helper()
	body, err := model.EnsureTypedBody(typ, id, logical, model.BodyOptions{Status: status})
	if err != nil {
		t.Fatal(err)
	}
	return model.Artifact{Issue: 1, CommentID: int64(len(id)), URL: "https://example.test/comments/" + id, Comment: model.ParseTypedComment(body)}
}

func link(t *testing.T, left, right *model.Artifact) {
	t.Helper()
	leftBody, _, err := model.AddRelatedCommentLink(left.Comment.Body, right.URL)
	if err != nil {
		t.Fatal(err)
	}
	rightBody, _, err := model.AddRelatedCommentLink(right.Comment.Body, left.URL)
	if err != nil {
		t.Fatal(err)
	}
	left.Comment = model.ParseTypedComment(leftBody)
	right.Comment = model.ParseTypedComment(rightBody)
}

func evaluate(t *testing.T, snapshot Snapshot) Report {
	t.Helper()
	report, err := Evaluate(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return report
}

func diagnosticCodes(report Report) []string {
	codes := make([]string, 0, len(report.Diagnostics))
	for _, diagnostic := range report.Diagnostics {
		codes = append(codes, diagnostic.Code)
	}
	sort.Strings(codes)
	return codes
}

func containsCode(report Report, code string) bool {
	for _, diagnostic := range report.Diagnostics {
		if diagnostic.Code == code {
			return true
		}
	}
	return false
}

func findDiagnostic(t *testing.T, report Report, code string) Diagnostic {
	t.Helper()
	for _, diagnostic := range report.Diagnostics {
		if diagnostic.Code == code {
			return diagnostic
		}
	}
	t.Fatalf("diagnostic %s not found in %s", code, strings.Join(diagnosticCodes(report), ","))
	return Diagnostic{}
}
