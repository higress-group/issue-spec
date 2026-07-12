package commands

import (
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/higress-group/issue-spec/internal/gates"
	"github.com/higress-group/issue-spec/internal/model"
	"github.com/higress-group/issue-spec/internal/processworkspace"
	"github.com/higress-group/issue-spec/internal/workflow"
)

func TestSummarizeStatusBlocksOnBlockedQuestion(t *testing.T) {
	specBody, err := model.EnsureTypedBody("SPEC", "SPEC-001", "## Requirement: X\n\nX MUST work.\n\n### Scenario: ok\n\n- **WHEN** x\n- **THEN** y", model.BodyOptions{Status: "confirmed"})
	if err != nil {
		t.Fatal(err)
	}
	questionBody, err := model.EnsureTypedBody("QUESTION", "QUESTION-001", "## Question\n\nDecide X.", model.BodyOptions{Status: "blocked"})
	if err != nil {
		t.Fatal(err)
	}
	summary := summarizeStatus("o/r", 1, 0, 0, []model.Artifact{
		{Issue: 1, URL: "https://github.com/o/r/issues/1#issuecomment-1", Comment: model.ParseTypedComment(specBody)},
		{Issue: 1, URL: "https://github.com/o/r/issues/1#issuecomment-2", Comment: model.ParseTypedComment(questionBody)},
	})
	if summary.OK {
		t.Fatal("blocked QUESTION should make status non-OK")
	}
	if summary.BlockingQuestions != 1 {
		t.Fatalf("blocking questions = %d", summary.BlockingQuestions)
	}
}

func TestResolveStatusGate(t *testing.T) {
	tests := []struct {
		raw               string
		design, implement int
		want              gates.Target
		wantErr           bool
	}{
		{want: gates.TargetProposal},
		{design: 2, want: gates.TargetDesign},
		{design: 2, implement: 3, want: gates.TargetImplement},
		{raw: "final", design: 2, implement: 3, want: gates.TargetFinal},
		{raw: "archive", design: 2, implement: 3, want: gates.TargetArchive},
		{raw: "final", design: 2, wantErr: true},
		{raw: "unknown", wantErr: true},
	}
	for _, test := range tests {
		got, err := resolveStatusGate(test.raw, test.design, test.implement)
		if (err != nil) != test.wantErr || got != test.want {
			t.Fatalf("resolveStatusGate(%q,%d,%d) = %q,%v want %q err=%v", test.raw, test.design, test.implement, got, err, test.want, test.wantErr)
		}
	}
}

func TestStatusFinalReportsDogfoodBlockersAndForecastUnknowns(t *testing.T) {
	spec := typedArtifact(t, 1, "SPEC", "SPEC-001", "confirmed", "## Requirement: X\n\nX MUST work.\n\n### Scenario: ok\n\n- **WHEN** x\n- **THEN** y")
	task := typedArtifact(t, 2, "TASK", "TASK-001", "ready", canonicalTaskContent)
	process := typedArtifact(t, 3, "PROCESS", "PROCESS-001", "in-progress", canonicalProcessContent)
	linkArtifacts(t, &spec, &task)
	linkArtifacts(t, &task, &process)
	summary := summarizeStatusForGate("o/r", 1, 2, 3, gates.TargetFinal,
		[]model.Artifact{spec, task, process}, workflow.Plan{}, nil)
	if summary.OK || summary.Gate.Ready || summary.Gate.PointInTime || summary.Gate.Mode != gates.ModeAuthoritative {
		t.Fatalf("final status must be authoritative and non-ready: %+v", summary.Gate)
	}
	want := []string{
		gates.CodePRChecksUnknown,
		gates.CodeProcessNotDone,
		gates.CodeProcessWorkspaceMigrationWarning,
		gates.CodeReviewFindingsUnknown,
		gates.CodeTaskNotDone,
		gates.CodeVerifyRequired,
		gates.CodeVerifySpecCoverageMissing,
	}
	if got := statusGateCodes(summary.Gate.Diagnostics); !reflect.DeepEqual(got, want) {
		t.Fatalf("gate codes = %v, want %v", got, want)
	}
	for _, diagnostic := range summary.Gate.Diagnostics {
		if (diagnostic.Code == gates.CodePRChecksUnknown || diagnostic.Code == gates.CodeReviewFindingsUnknown) && diagnostic.Freshness != gates.FreshnessUnknown {
			t.Fatalf("remote forecast is not explicit unknown: %+v", diagnostic)
		}
	}
}

func TestStatusAndVerifyLocallyKnowableCodesStayInParity(t *testing.T) {
	spec := typedArtifact(t, 1, "SPEC", "SPEC-001", "confirmed", "## Requirement: X\n\nX MUST work.\n\n### Scenario: ok\n\n- **WHEN** x\n- **THEN** y")
	task := typedArtifact(t, 2, "TASK", "TASK-001", "ready", canonicalTaskContent)
	process := typedArtifact(t, 3, "PROCESS", "PROCESS-001", "in-progress", canonicalProcessContent)
	linkArtifacts(t, &spec, &task)
	linkArtifacts(t, &task, &process)
	artifacts := []model.Artifact{spec, task, process}
	status := summarizeStatusForGate("o/r", 1, 2, 3, gates.TargetFinal, artifacts, workflow.Plan{}, nil)
	verify, err := buildFinalVerifyReport(artifacts, "https://github.com/o/r/issues/1", finalVerifyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	statusLocal := localStatusGateCodes(status.Gate.Diagnostics)
	verifyLocal := localStatusGateCodes(verify.Gate.Diagnostics)
	if !reflect.DeepEqual(statusLocal, verifyLocal) {
		t.Fatalf("locally knowable gate drift: status=%v verify=%v", statusLocal, verifyLocal)
	}
}

func statusGateCodes(diagnostics []gates.Diagnostic) []string {
	codes := make([]string, 0, len(diagnostics))
	for _, diagnostic := range diagnostics {
		codes = append(codes, diagnostic.Code)
	}
	sort.Strings(codes)
	return codes
}

func localStatusGateCodes(diagnostics []gates.Diagnostic) []string {
	var local []gates.Diagnostic
	for _, diagnostic := range diagnostics {
		if diagnostic.Freshness == gates.FreshnessLocal && diagnostic.Blocking {
			local = append(local, diagnostic)
		}
	}
	return statusGateCodes(local)
}

func TestStatusWorkspaceUsesExactTrustedCarrierRevision(t *testing.T) {
	process := statusWorkspaceProcess(t, model.ProcessExecutionReview, strings.Repeat("a", 40))
	collection := statusGateCollection{Remote: statusForecastRemoteFacts(gates.TargetFinal), ProcessEvidence: []gates.ProcessEvidenceInput{{
		Process: process, ActiveSpecs: map[string]string{"SPEC-001": "https://example.test/spec"},
		Reviews: []gates.ReviewEvidence{{ProcessID: "PROCESS-001", SpecID: "SPEC-001", Done: true,
			SubjectRevision: strings.Repeat("a", 40), Trusted: true, Source: "github-review:1"}},
	}}}
	collection.Remote.Workspace.ExpectedRevision = gates.Fact{Required: true, Known: true, Passed: true, Expected: strings.Repeat("a", 40)}
	summary := summarizeStatusForGate("o/r", 1, 2, 3, gates.TargetFinal, []model.Artifact{process}, workflow.Plan{}, nil, collection)
	if statusHasCode(summary, gates.CodeProcessWorkspaceRevisionUnknown) || statusHasCode(summary, gates.CodeProcessWorkspaceRevisionStale) {
		t.Fatalf("exact trusted status carrier was rejected: %+v", summary.Gate.Diagnostics)
	}

	collection.Remote.Workspace.ExpectedRevision.Expected = strings.Repeat("b", 40)
	stale := summarizeStatusForGate("o/r", 1, 2, 3, gates.TargetFinal, []model.Artifact{process}, workflow.Plan{}, nil, collection)
	if !statusHasCode(stale, gates.CodeProcessWorkspaceRevisionStale) {
		t.Fatalf("stale status carrier was accepted: %+v", stale.Gate.Diagnostics)
	}
}

func TestStatusWorkspaceExternalUsesAuthoritativeCarrier(t *testing.T) {
	process := statusWorkspaceProcess(t, model.ProcessExecutionExternal, "")
	revision := strings.Repeat("c", 40)
	collection := statusGateCollection{Remote: statusForecastRemoteFacts(gates.TargetFinal), ProcessEvidence: []gates.ProcessEvidenceInput{{
		Process: process, ActiveSpecs: map[string]string{"SPEC-001": "https://example.test/spec"},
		External: []gates.ExternalProcessEvidence{{ProcessID: "PROCESS-001", SpecID: "SPEC-001", SubjectRevision: revision,
			EvidenceRevision: revision, Consumed: true, EvidenceIDs: []string{"review-1"}, Trusted: true, Source: "native-authoritative-ledger:review-1"}},
	}}}
	collection.Remote.Workspace.ExpectedRevision = gates.Fact{Required: true, Known: true, Passed: true, Expected: revision}
	summary := summarizeStatusForGate("o/r", 1, 2, 3, gates.TargetFinal, []model.Artifact{process}, workflow.Plan{}, nil, collection)
	if statusHasCode(summary, gates.CodeProcessWorkspaceRevisionUnknown) || statusHasCode(summary, gates.CodeProcessWorkspaceRevisionStale) ||
		statusHasCode(summary, gates.CodeProcessWorkspaceProviderEvidenceMissing) {
		t.Fatalf("authoritative external carrier was not retained: %+v", summary.Gate.Diagnostics)
	}
}

func TestExactStatusPullRequestRejectsAmbiguousLinks(t *testing.T) {
	process := statusWorkspaceProcess(t, model.ProcessExecutionReview, strings.Repeat("a", 40))
	body, _, err := model.AddPRLink(process.Comment.Body, "https://github.com/o/r/pull/7")
	if err != nil {
		t.Fatal(err)
	}
	process.Comment = model.ParseTypedComment(body)
	if number, _, ok := exactStatusPullRequest([]model.Artifact{process}, "o/r"); !ok || number != 7 {
		t.Fatalf("exact PR link was not selected: number=%d ok=%v", number, ok)
	}
	body, _, err = model.AddPRLink(process.Comment.Body, "https://github.com/o/r/pull/8")
	if err != nil {
		t.Fatal(err)
	}
	process.Comment = model.ParseTypedComment(body)
	if _, _, ok := exactStatusPullRequest([]model.Artifact{process}, "o/r"); ok {
		t.Fatal("ambiguous PR links were accepted")
	}
}

func statusWorkspaceProcess(t *testing.T, class model.ProcessExecutionClass, revision string) model.Artifact {
	t.Helper()
	logical := "## Process: status workspace\n\n### Parent TASK\n\n- TASK-001\n\n### Execution Class\n\n- " + string(class) +
		"\n\n### Covers\n\n- SPEC-001\n\n### Handoff\n\ncomplete"
	process := typedArtifact(t, 3, "PROCESS", "PROCESS-001", "done", logical)
	now := time.Unix(100, 0).UTC()
	workspace := processworkspace.PortableLease{SchemaVersion: processworkspace.LeaseSchemaVersion, WorkspaceID: "ws-process-001", Repository: "o/r",
		ProcessID: "PROCESS-001", ExecutionClass: processworkspace.ExecutionClass(class), State: processworkspace.StatePrepared, CreatedAt: now, UpdatedAt: now}
	switch class {
	case model.ProcessExecutionReview, model.ProcessExecutionVerification:
		workspace.Mode, workspace.BaseSHA, workspace.DetachedRevision, workspace.RuntimeNamespace = processworkspace.ModeSnapshot, revision, revision, "ws-process-001"
	case model.ProcessExecutionExternal, model.ProcessExecutionOrchestration:
		workspace.Mode = processworkspace.ModeNone
	}
	transition, err := model.ApplyTypedTransition(process.Comment.Body, model.TransitionRequest{ExpectedType: "PROCESS", ExpectedID: "PROCESS-001", Workspace: &workspace})
	if err != nil {
		t.Fatal(err)
	}
	process.Comment = model.ParseTypedComment(transition.Body)
	return process
}

func statusHasCode(summary statusSummary, code string) bool {
	for _, diagnostic := range summary.Gate.Diagnostics {
		if diagnostic.Code == code {
			return true
		}
	}
	return false
}

func TestSummarizeStatusReportsSessionMetadataDiagnosticsWithoutBlocking(t *testing.T) {
	specBody, err := model.EnsureTypedBody("SPEC", "SPEC-001", "## Requirement: X\n\nX MUST work.\n\n### Scenario: ok\n\n- **WHEN** x\n- **THEN** y", model.BodyOptions{Status: "confirmed"})
	if err != nil {
		t.Fatal(err)
	}
	summary := summarizeStatus("o/r", 1, 0, 0, []model.Artifact{
		{Issue: 1, URL: "https://github.com/o/r/issues/1#issuecomment-1", Comment: model.ParseTypedComment(specBody)},
	})
	if !summary.OK {
		t.Fatalf("metadata diagnostics should not block status: %+v", summary.NextGates)
	}
	if len(summary.Diagnostics) != 1 || summary.Diagnostics[0].Code != "missing_session_metadata" {
		t.Fatalf("unexpected diagnostics: %+v", summary.Diagnostics)
	}
}
