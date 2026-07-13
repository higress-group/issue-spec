package gates

import (
	"strings"
	"testing"
	"time"

	"github.com/higress-group/issue-spec/internal/model"
	"github.com/higress-group/issue-spec/internal/processworkspace"
)

const workspaceGateRevision = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestWorkspaceEvidenceExecutionClassMatrix(t *testing.T) {
	cases := []struct {
		class     model.ProcessExecutionClass
		satisfied string
	}{
		{model.ProcessExecutionChangeBearing, ""},
		{model.ProcessExecutionReview, "review evidence"},
		{model.ProcessExecutionVerification, "verification evidence"},
		{model.ProcessExecutionOrchestration, ""},
		{model.ProcessExecutionExternal, "exact-revision external evidence"},
	}
	for _, test := range cases {
		t.Run(string(test.class), func(t *testing.T) {
			process := workspaceGateProcess(t, test.class, true, workspaceGateRevision)
			var evidence []ProcessEvidenceReport
			var carrier map[string]CarrierRevisionFact
			if test.satisfied != "" {
				evidence = []ProcessEvidenceReport{{ProcessID: process.Comment.ID, Satisfied: []string{test.satisfied}}}
				carrier = map[string]CarrierRevisionFact{process.Comment.ID: {Known: true, Revision: workspaceGateRevision, Trusted: true, Source: "test-carrier"}}
			}
			report, err := EvaluateWorkspaceEvidence(WorkspaceEvaluationInput{Target: TargetFinal, Mode: ModeAuthoritative,
				Artifacts: []model.Artifact{process}, ExpectedRevision: Fact{Known: true, Expected: workspaceGateRevision}, ProcessEvidence: evidence, CarrierRevisions: carrier})
			if err != nil || hasBlockingWorkspaceDiagnostic(report.Diagnostics) {
				t.Fatalf("class=%s diagnostics=%+v err=%v", test.class, report.Diagnostics, err)
			}
		})
	}
}

func TestWorkspaceCarrierRevisionMustBeTrustedAndExact(t *testing.T) {
	process := workspaceGateProcess(t, model.ProcessExecutionReview, true, workspaceGateRevision)
	evidence := []ProcessEvidenceReport{{ProcessID: process.Comment.ID, Satisfied: []string{"review evidence"}}}
	cases := []struct {
		name string
		fact *CarrierRevisionFact
		code string
	}{
		{name: "unbound", code: CodeProcessWorkspaceRevisionUnknown},
		{name: "stale", fact: &CarrierRevisionFact{Known: true, Revision: strings.Repeat("b", 40), Trusted: true, Source: "review"}, code: CodeProcessWorkspaceRevisionStale},
		{name: "untrusted", fact: &CarrierRevisionFact{Known: true, Revision: workspaceGateRevision, Source: "process-body"}, code: CodeProcessWorkspaceRevisionStale},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			facts := map[string]CarrierRevisionFact{}
			if test.fact != nil {
				facts[process.Comment.ID] = *test.fact
			}
			report, err := EvaluateWorkspaceEvidence(WorkspaceEvaluationInput{Target: TargetFinal, Mode: ModeAuthoritative,
				Artifacts: []model.Artifact{process}, ExpectedRevision: Fact{Known: true, Expected: workspaceGateRevision}, ProcessEvidence: evidence, CarrierRevisions: facts})
			if err != nil || !workspaceHasBlockingCode(report.Diagnostics, test.code) {
				t.Fatalf("diagnostics=%+v err=%v", report.Diagnostics, err)
			}
		})
	}
}

func TestWorkspaceCarrierMissingRootCauseIsNotDuplicated(t *testing.T) {
	process := workspaceGateProcess(t, model.ProcessExecutionVerification, true, workspaceGateRevision)
	base := ProcessEvidenceReport{ProcessID: process.Comment.ID, Diagnostics: []Diagnostic{{Code: CodeProcessCarrierMissing, Blocking: true}}}
	report, err := EvaluateWorkspaceEvidence(WorkspaceEvaluationInput{Target: TargetFinal, Mode: ModeAuthoritative,
		Artifacts: []model.Artifact{process}, ExpectedRevision: Fact{Known: true, Expected: workspaceGateRevision}, ProcessEvidence: []ProcessEvidenceReport{base}})
	if err != nil || workspaceHasCode(report.Diagnostics, CodeProcessWorkspaceVerifyEvidenceMissing) {
		t.Fatalf("duplicate diagnostics=%+v err=%v", report.Diagnostics, err)
	}
}

func TestWorkspaceEvidenceLegacyMigrationAndManagedFinalRequirement(t *testing.T) {
	legacy := workspaceGateProcess(t, model.ProcessExecutionChangeBearing, false, "")
	legacy.Comment.Body = removeExecutionClass(legacy.Comment.Body)
	legacy.Comment = model.ParseTypedComment(legacy.Comment.Body)
	report, err := EvaluateWorkspaceEvidence(WorkspaceEvaluationInput{Target: TargetFinal, Mode: ModeAuthoritative, Artifacts: []model.Artifact{legacy}})
	if err != nil || hasBlockingWorkspaceDiagnostic(report.Diagnostics) || !workspaceHasCode(report.Diagnostics, CodeProcessWorkspaceMigrationWarning) {
		t.Fatalf("legacy diagnostics=%+v err=%v", report.Diagnostics, err)
	}
	managed := workspaceGateProcess(t, model.ProcessExecutionChangeBearing, false, "")
	report, err = EvaluateWorkspaceEvidence(WorkspaceEvaluationInput{Target: TargetFinal, Mode: ModeAuthoritative, Artifacts: []model.Artifact{managed}})
	if err != nil || !workspaceHasBlockingCode(report.Diagnostics, CodeProcessWorkspaceRequired) {
		t.Fatalf("managed missing workspace diagnostics=%+v err=%v", report.Diagnostics, err)
	}
	early, err := EvaluateWorkspaceEvidence(WorkspaceEvaluationInput{Target: TargetImplement, Mode: ModeForecast, Artifacts: []model.Artifact{managed}})
	if err != nil || hasBlockingWorkspaceDiagnostic(early.Diagnostics) || !workspaceHasCode(early.Diagnostics, CodeProcessWorkspaceMigrationWarning) {
		t.Fatalf("early forecast diagnostics=%+v err=%v", early.Diagnostics, err)
	}
}

func TestWorkspaceEvidenceMalformedLocalFieldsAndStaleRevisionBlock(t *testing.T) {
	malformed := workspaceGateProcess(t, model.ProcessExecutionChangeBearing, false, "")
	section := "### Workspace\n\n```json\n{\"schema_version\":1,\"worktree_path\":\"/tmp/leak\"}\n```\n\n"
	malformed.Comment.Body = strings.Replace(malformed.Comment.Body, "### Handoff", section+"### Handoff", 1)
	malformed.Comment = model.ParseTypedComment(malformed.Comment.Body)
	report, err := EvaluateWorkspaceEvidence(WorkspaceEvaluationInput{Target: TargetFinal, Mode: ModeAuthoritative, Artifacts: []model.Artifact{malformed}})
	if err != nil || !workspaceHasBlockingCode(report.Diagnostics, CodeProcessWorkspaceInvalid) {
		t.Fatalf("malformed diagnostics=%+v err=%v", report.Diagnostics, err)
	}
	stale := workspaceGateProcess(t, model.ProcessExecutionChangeBearing, true, strings.Repeat("b", 40))
	report, err = EvaluateWorkspaceEvidence(WorkspaceEvaluationInput{Target: TargetFinal, Mode: ModeAuthoritative,
		Artifacts: []model.Artifact{stale}, ExpectedRevision: Fact{Known: true, Expected: workspaceGateRevision}})
	if err != nil || !workspaceHasBlockingCode(report.Diagnostics, CodeProcessWorkspaceRevisionStale) {
		t.Fatalf("stale diagnostics=%+v err=%v", report.Diagnostics, err)
	}
	unknown, err := EvaluateWorkspaceEvidence(WorkspaceEvaluationInput{Target: TargetFinal, Mode: ModeForecast, Artifacts: []model.Artifact{workspaceGateProcess(t, model.ProcessExecutionChangeBearing, true, workspaceGateRevision)}})
	if err != nil || !workspaceHasBlockingCode(unknown.Diagnostics, CodeProcessWorkspaceRevisionUnknown) || unknown.Diagnostics[0].Freshness != FreshnessUnknown {
		t.Fatalf("unknown diagnostics=%+v err=%v", unknown.Diagnostics, err)
	}
}

func TestWorkspaceEvidenceExternalCannotUseLocalWorkspaceAsProviderEvidence(t *testing.T) {
	process := workspaceGateProcess(t, model.ProcessExecutionExternal, true, workspaceGateRevision)
	report, err := EvaluateWorkspaceEvidence(WorkspaceEvaluationInput{Target: TargetFinal, Mode: ModeAuthoritative,
		Artifacts: []model.Artifact{process}, ExpectedRevision: Fact{Known: true, Expected: workspaceGateRevision}})
	if err != nil || !workspaceHasBlockingCode(report.Diagnostics, CodeProcessWorkspaceProviderEvidenceMissing) {
		t.Fatalf("external diagnostics=%+v err=%v", report.Diagnostics, err)
	}
}

func TestExternalWorkspaceGateRequiresNoCheckoutDespiteExactProviderEvidence(t *testing.T) {
	process := workspaceGateProcess(t, model.ProcessExecutionExternal, true, workspaceGateRevision)
	evidence := ProcessEvidenceReport{ProcessID: process.Comment.ID, Satisfied: []string{"exact-revision external evidence"}}
	input := WorkspaceEvaluationInput{Target: TargetFinal, Mode: ModeAuthoritative,
		ExpectedRevision: Fact{Known: true, Expected: workspaceGateRevision},
		CarrierRevisions: map[string]CarrierRevisionFact{process.Comment.ID: {Known: true, Revision: workspaceGateRevision, Trusted: true, Source: "provider"}}}
	now := time.Unix(100, 0).UTC()
	base := processworkspace.PortableLease{SchemaVersion: processworkspace.LeaseSchemaVersion, WorkspaceID: "ws-process-001", Repository: "o/r",
		ProcessID: "PROCESS-001", ExecutionClass: processworkspace.ExecutionExternal, Mode: processworkspace.ModeNone,
		State: processworkspace.StatePrepared, CreatedAt: now, UpdatedAt: now}
	for _, test := range []struct {
		mode     processworkspace.WorkspaceMode
		blocking bool
	}{
		{mode: processworkspace.ModeNone},
		{mode: processworkspace.ModeWritable, blocking: true},
		{mode: processworkspace.ModeSnapshot, blocking: true},
	} {
		t.Run(string(test.mode), func(t *testing.T) {
			portable := base
			portable.Mode = test.mode
			diagnostics := evaluateExternalWorkspace(process, input, portable, evidence)
			if got := workspaceHasBlockingCode(diagnostics, CodeProcessWorkspaceModeInvalid); got != test.blocking {
				t.Fatalf("external %s mode diagnostics=%+v blocking=%v want=%v", test.mode, diagnostics, got, test.blocking)
			}
			if workspaceHasCode(diagnostics, CodeProcessWorkspaceProviderEvidenceMissing) || workspaceHasCode(diagnostics, CodeProcessWorkspaceRevisionUnknown) || workspaceHasCode(diagnostics, CodeProcessWorkspaceRevisionStale) {
				t.Fatalf("satisfied exact provider evidence was lost for external %s: %+v", test.mode, diagnostics)
			}
		})
	}
}

func workspaceGateProcess(t *testing.T, class model.ProcessExecutionClass, includeWorkspace bool, revision string) model.Artifact {
	t.Helper()
	body, err := model.EnsureTypedBody("PROCESS", "PROCESS-001", "## Process: workspace\n\n### Parent TASK\n\n- TASK-001\n\n### Execution Class\n\n- "+string(class)+"\n\n### Covers\n\n- SPEC-001\n\n### Handoff\n\ncomplete", model.BodyOptions{Status: "done"})
	if err != nil {
		t.Fatal(err)
	}
	if includeWorkspace {
		now := time.Unix(100, 0).UTC()
		workspace := processworkspace.PortableLease{SchemaVersion: processworkspace.LeaseSchemaVersion, WorkspaceID: "ws-process-001", Repository: "o/r",
			ProcessID: "PROCESS-001", ExecutionClass: processworkspace.ExecutionClass(class), State: processworkspace.StatePrepared, CreatedAt: now, UpdatedAt: now}
		switch class {
		case model.ProcessExecutionChangeBearing:
			workspace.Mode, workspace.BaseSHA, workspace.Branch = processworkspace.ModeWritable, strings.Repeat("c", 40), "process/one"
			workspace.WriteOwnership, workspace.RuntimeNamespace = []string{"internal/**"}, "ws-process-001"
			workspace.State, workspace.ResultCommit, workspace.IntegrationSHA = processworkspace.StateIntegrated, strings.Repeat("d", 40), revision
		case model.ProcessExecutionReview, model.ProcessExecutionVerification:
			workspace.Mode, workspace.BaseSHA, workspace.DetachedRevision = processworkspace.ModeSnapshot, revision, revision
			workspace.RuntimeNamespace = "ws-process-001"
		case model.ProcessExecutionOrchestration, model.ProcessExecutionExternal:
			workspace.Mode = processworkspace.ModeNone
		}
		transition, err := model.ApplyTypedTransition(body, model.TransitionRequest{ExpectedType: "PROCESS", ExpectedID: "PROCESS-001", Workspace: &workspace})
		if err != nil {
			t.Fatal(err)
		}
		body = transition.Body
	}
	return model.Artifact{URL: "https://example/process", Comment: model.ParseTypedComment(body)}
}

func workspaceHasCode(diagnostics []Diagnostic, code string) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == code {
			return true
		}
	}
	return false
}

func workspaceHasBlockingCode(diagnostics []Diagnostic, code string) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == code && diagnostic.Blocking {
			return true
		}
	}
	return false
}

func hasBlockingWorkspaceDiagnostic(diagnostics []Diagnostic) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Blocking {
			return true
		}
	}
	return false
}
