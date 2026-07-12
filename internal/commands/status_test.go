package commands

import (
	"reflect"
	"sort"
	"testing"

	"github.com/higress-group/issue-spec/internal/gates"
	"github.com/higress-group/issue-spec/internal/model"
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
	if summary.OK || summary.Gate.Ready || !summary.Gate.PointInTime {
		t.Fatalf("final forecast must be non-ready: %+v", summary.Gate)
	}
	want := []string{
		gates.CodePRChecksUnknown,
		gates.CodeProcessNotDone,
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
		if diagnostic.Freshness == gates.FreshnessLocal {
			local = append(local, diagnostic)
		}
	}
	return statusGateCodes(local)
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
