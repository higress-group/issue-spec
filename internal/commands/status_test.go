package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/higress-group/issue-spec/internal/assignment"
	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/codereview"
	coreevidence "github.com/higress-group/issue-spec/internal/evidence"
	"github.com/higress-group/issue-spec/internal/gates"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/model"
	"github.com/higress-group/issue-spec/internal/processworkspace"
	"github.com/higress-group/issue-spec/internal/workflow"
)

func TestRunStatusSummaryRequiresJSON(t *testing.T) {
	var errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &bytes.Buffer{}, &errOut)
	if code := app.runStatus(t.Context(), []string{"--summary"}); code != 2 || !strings.Contains(errOut.String(), "--summary requires --json") {
		t.Fatalf("status exit=%d stderr=%q", code, errOut.String())
	}
}

func TestRunStatusSummaryIsAdditiveAndExitEquivalent(t *testing.T) {
	args := []string{"--repo", "o/r", "--proposal", "1", "--design", "2", "--implement", "3", "--gate", "final", "--json"}
	fullApp, fullOut, fullErr := newGitHubVerifyWithoutPRApp(t, canonicalProcessContentWithClass(model.ProcessExecutionVerification))
	fullCode := fullApp.runStatus(t.Context(), args)
	if fullErr.Len() != 0 {
		t.Fatalf("full status stderr=%q", fullErr.String())
	}
	var full statusSummary
	if err := json.Unmarshal(fullOut.Bytes(), &full); err != nil {
		t.Fatalf("decode full status: %v\n%s", err, fullOut.String())
	}
	if strings.Contains(fullOut.String(), `"schema_version"`) || !strings.Contains(fullOut.String(), `"traceability"`) {
		t.Fatalf("existing full JSON contract changed: %s", fullOut.String())
	}

	compactApp, compactOut, compactErr := newGitHubVerifyWithoutPRApp(t, canonicalProcessContentWithClass(model.ProcessExecutionVerification))
	compactCode := compactApp.runStatus(t.Context(), append(append([]string(nil), args...), "--summary"))
	if compactErr.Len() != 0 {
		t.Fatalf("compact status stderr=%q", compactErr.String())
	}
	var compact gates.CompactSummary
	if err := json.Unmarshal(compactOut.Bytes(), &compact); err != nil {
		t.Fatalf("decode compact status: %v\n%s", err, compactOut.String())
	}
	if compactCode != fullCode || compact.OK != full.OK || compact.Gate.Target != full.Gate.Target || compact.Gate.Mode != full.Gate.Mode {
		t.Fatalf("decision drift: fullCode=%d compactCode=%d full=%+v compact=%+v", fullCode, compactCode, full.Gate, compact.Gate)
	}
	if compact.SchemaVersion != 1 || len(compact.Blockers) == 0 || compact.Counts["PROCESS"]["done"] != 1 {
		t.Fatalf("compact routing data missing: %+v", compact)
	}
	for _, blocker := range compact.Blockers {
		if blocker.Detail.CommandFamily != "status" || containsArgument(blocker.Detail.Arguments, "--summary") ||
			!containsArgument(blocker.Detail.Arguments, "--json") {
			t.Fatalf("invalid structured detail action: %+v", blocker.Detail)
		}
	}
	for _, forbidden := range []string{`"traceability"`, `"next_gates"`, `"processes"`, `"evaluation_digest"`} {
		if strings.Contains(compactOut.String(), forbidden) {
			t.Fatalf("compact status contains %s: %s", forbidden, compactOut.String())
		}
	}
}

func containsArgument(arguments []string, want string) bool {
	for _, argument := range arguments {
		if argument == want {
			return true
		}
	}
	return false
}

func TestSelfHostedStatusOnlyForcesProviderSyncForFinal(t *testing.T) {
	now := time.Now().UTC()
	reference := codereview.Reference{ProviderKey: "code.example", ExternalRepository: "acme/widgets-code", ChangeID: "change-42"}
	for _, test := range []struct {
		target    gates.Target
		wantSyncs int
		wantCalls string
	}{
		{target: gates.TargetFinal, wantSyncs: 1, wantCalls: "operator,persist,ledger"},
		{target: gates.TargetArchive, wantCalls: "ledger"},
	} {
		t.Run(string(test.target), func(t *testing.T) {
			var calls []string
			check := testEvidenceRecord("check-ledger", codereview.EvidenceCheck, "passed", "head-abc", now)
			check.Name = "unit"
			ledger := &commandEvidenceProvider{label: "ledger", calls: &calls, snapshot: codereview.Snapshot{
				ProtocolVersion: codereview.ProtocolVersion, Reference: reference, SubjectRevision: "head-abc", CapturedAt: now,
				Records: []codereview.EvidenceRecord{check}}}
			native := &commandNativeEvidence{target: coreevidence.NativeTarget{Reference: reference, ReferenceVersion: 7,
				SubjectRevision: "head-abc", Provider: ledger}, calls: &calls}
			app := newApp(strings.NewReader(""), nil, nil)
			app.newNativeEvidenceProvider = func(auth.Profile, string) (nativeEvidenceProvider, error) { return native, nil }
			app.lookupOperatorProvider = func(context.Context, auth.Profile, string) (codereview.Provider, error) {
				return &commandEvidenceProvider{label: "operator", calls: &calls, snapshot: codereview.Snapshot{
					ProtocolVersion: codereview.ProtocolVersion, Reference: reference, SubjectRevision: "head-abc", CapturedAt: now}}, nil
			}
			collection := app.collectStatusGateFacts(t.Context(), fakeGitHubBackend{}, auth.Profile{Kind: auth.ProfileKindHosted},
				"token", "acme/widgets", 3, test.target, nil)
			if !collection.Remote.ProviderEvidence.Passed || native.syncs != test.wantSyncs || strings.Join(calls, ",") != test.wantCalls {
				t.Fatalf("provider=%+v syncs=%d calls=%v", collection.Remote.ProviderEvidence, native.syncs, calls)
			}
		})
	}
}

func TestStatusAcceptedVerificationUsesExactGitHubHead(t *testing.T) {
	revision := strings.Repeat("b", 40)
	for _, target := range []gates.Target{gates.TargetFinal, gates.TargetArchive} {
		t.Run(string(target), func(t *testing.T) {
			artifacts := statusAcceptedVerificationArtifacts(t, revision, "https://github.com/o/r/pull/7")
			pr := pullRequestAtHead(7, revision)
			backend := &sequencedPullRequestCommitBackend{fakeGitHubBackend: fakeGitHubBackend{},
				pulls: []github.PullRequest{pr, pr}, commits: []github.PullRequestCommit{{SHA: revision}}}
			collection := (&app{}).collectStatusGateFacts(t.Context(), backend,
				auth.Profile{Kind: auth.ProfileKindGitHub}, "", "o/r", 3, target, artifacts)
			assertStatusAcceptedVerification(t, collection, revision)
		})
	}
}

func TestStatusAcceptedVerificationUsesExactSelfHostedRevision(t *testing.T) {
	revision := strings.Repeat("b", 40)
	now := time.Now().UTC()
	reference := codereview.Reference{ProviderKey: "code.example", ExternalRepository: "acme/widgets-code", ChangeID: "change-42"}
	for _, target := range []gates.Target{gates.TargetFinal, gates.TargetArchive} {
		t.Run(string(target), func(t *testing.T) {
			check := testEvidenceRecord("check-ledger", codereview.EvidenceCheck, "passed", revision, now)
			check.Name = "unit"
			ledger := &commandEvidenceProvider{snapshot: codereview.Snapshot{ProtocolVersion: codereview.ProtocolVersion,
				Reference: reference, SubjectRevision: revision, CapturedAt: now, Records: []codereview.EvidenceRecord{check}}}
			native := &commandNativeEvidence{target: coreevidence.NativeTarget{Reference: reference, ReferenceVersion: 7,
				SubjectRevision: revision, Provider: ledger}}
			app := newApp(strings.NewReader(""), nil, nil)
			app.newNativeEvidenceProvider = func(auth.Profile, string) (nativeEvidenceProvider, error) { return native, nil }
			app.lookupOperatorProvider = func(context.Context, auth.Profile, string) (codereview.Provider, error) {
				return &commandEvidenceProvider{snapshot: codereview.Snapshot{ProtocolVersion: codereview.ProtocolVersion,
					Reference: reference, SubjectRevision: revision, CapturedAt: now}}, nil
			}
			collection := app.collectStatusGateFacts(t.Context(), fakeGitHubBackend{},
				auth.Profile{Kind: auth.ProfileKindHosted}, "token", "acme/widgets", 3, target,
				statusAcceptedVerificationArtifacts(t, revision, ""))
			assertStatusAcceptedVerification(t, collection, revision)
		})
	}
}

func statusAcceptedVerificationArtifacts(t *testing.T, revision, prURL string) []model.Artifact {
	t.Helper()
	spec := typedArtifact(t, 1, "SPEC", "SPEC-001", "confirmed", "## Requirement: X\n\nX MUST work.\n\n### Scenario: ok\n\n- **WHEN** x\n- **THEN** y")
	spec.URL = "https://github.com/o/r/issues/1#issuecomment-spec"
	task := typedArtifact(t, 2, "TASK", "TASK-001", "done", canonicalTaskContent)
	task.URL = "https://github.com/o/r/issues/2#issuecomment-task"
	process := statusWorkspaceProcess(t, model.ProcessExecutionChangeBearing, revision)
	process.URL = "https://github.com/o/r/issues/3#issuecomment-process"
	linkArtifacts(t, &spec, &task)
	linkArtifacts(t, &task, &process)
	linkArtifacts(t, &spec, &process)
	if prURL != "" {
		body, _, err := model.AddPRLink(process.Comment.Body, prURL)
		if err != nil {
			t.Fatal(err)
		}
		process.Comment = model.ParseTypedComment(body)
	}
	receipt := testSealedVerificationReceipt(t, []assignment.TestResult{{ID: "unit", Command: "go test ./internal/commands",
		Outcome: assignment.TestPassed, Assurance: assignment.AssuranceSelfReported}}, nil)
	verifyBody, err := renderSubmittedVerification("VERIFY-001", process.URL, []string{"SPEC-001"}, receipt, nil,
		testVerificationSubmission("Verifier"))
	if err != nil {
		t.Fatal(err)
	}
	verify := model.Artifact{Issue: 3, CommentID: 4, URL: "https://github.com/o/r/issues/3#issuecomment-verify",
		Comment: model.ParseTypedComment(verifyBody)}
	return []model.Artifact{spec, task, process, verify}
}

func assertStatusAcceptedVerification(t *testing.T, collection statusGateCollection, revision string) {
	t.Helper()
	if len(collection.ProcessEvidence) != 1 || len(collection.ProcessEvidence[0].Verifications) != 1 {
		t.Fatalf("status verification evidence missing: remote=%+v process=%+v", collection.Remote, collection.ProcessEvidence)
	}
	evidence := collection.ProcessEvidence[0].Verifications[0]
	if !evidence.Trusted || evidence.SubjectRevision != revision || !evidence.StructuredTests ||
		evidence.TestAssurance != string(assignment.AssuranceSelfReported) ||
		evidence.Source != "accepted-verification-receipt:self-reported-tests" {
		t.Fatalf("accepted verification was not projected at exact status revision: %+v", evidence)
	}
}

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
		gates.CodeProcessCarrierMissing,
		gates.CodeProcessExecutionClassLegacy,
		gates.CodeProcessNotDone,
		gates.CodeProcessPRLinkMissing,
		gates.CodeProcessSpecLinkMissing,
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
	for _, code := range []string{
		gates.CodeProcessCarrierMissing,
		gates.CodeProcessPRLinkMissing,
		gates.CodeProcessSpecLinkMissing,
	} {
		if !statusHasCode(status, code) {
			t.Fatalf("final status without PR authority omitted local fail-closed code %s: %+v", code, status.Gate.Diagnostics)
		}
	}
	archive := summarizeStatusForGate("o/r", 1, 2, 3, gates.TargetArchive, artifacts, workflow.Plan{}, nil)
	for _, code := range []string{
		gates.CodeProcessCarrierMissing,
		gates.CodeProcessPRLinkMissing,
		gates.CodeProcessSpecLinkMissing,
	} {
		if !statusHasCode(archive, code) {
			t.Fatalf("archive status without PR authority omitted local fail-closed code %s: %+v", code, archive.Gate.Diagnostics)
		}
	}
}

func TestStatusIncludesCollectedExactRevisionFailure(t *testing.T) {
	verify := typedArtifact(t, 3, "VERIFY", "VERIFY-001", "done",
		canonicalVerifyContent+"\n\n### Revision\n\n`stale-head`")
	verify.URL = "https://issues.example/acme/widgets/issues/3#issuecomment-3"
	collection := statusGateCollection{Remote: statusForecastRemoteFacts(gates.TargetFinal)}
	collection.Remote.ProviderEvidence = gates.Fact{Required: true, Known: true, Passed: true}
	collection.Remote.VerifyRevision, _ = collectVerifyRevisionFact([]model.Artifact{verify}, "head-abc",
		time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC))

	summary := summarizeStatusForGate("acme/widgets", 1, 2, 3, gates.TargetFinal,
		[]model.Artifact{verify}, workflow.Plan{}, nil, collection)
	diagnostic := statusGateDiagnostic(summary, gates.CodeVerifyRevisionInvalid)
	if summary.OK || diagnostic == nil || diagnostic.Artifact.ID != "VERIFY-001" ||
		diagnostic.Expected != "head-abc" || diagnostic.Remediation.CommandFamily != "comment upsert" {
		t.Fatalf("status exact-revision diagnostic = %+v gate=%+v", diagnostic, summary.Gate)
	}
}

func TestStatusExplicitWorkspaceProcessEvidenceMatchesVerifyByClassAndGate(t *testing.T) {
	classes := []model.ProcessExecutionClass{
		model.ProcessExecutionChangeBearing,
		model.ProcessExecutionReview,
		model.ProcessExecutionVerification,
		model.ProcessExecutionOrchestration,
		model.ProcessExecutionExternal,
	}
	for _, class := range classes {
		for _, target := range []gates.Target{gates.TargetFinal, gates.TargetArchive} {
			t.Run(string(class)+"/"+string(target), func(t *testing.T) {
				spec := typedArtifact(t, 1, "SPEC", "SPEC-001", "confirmed", "## Requirement: X\n\nX MUST work.\n\n### Scenario: ok\n\n- **WHEN** x\n- **THEN** y")
				spec.URL = "https://github.com/o/r/issues/1#issuecomment-1"
				task := typedArtifact(t, 2, "TASK", "TASK-001", "done", canonicalTaskContent)
				task.URL = "https://github.com/o/r/issues/2#issuecomment-2"
				process := statusWorkspaceProcess(t, class, strings.Repeat("a", 40))
				process.URL = "https://github.com/o/r/issues/3#issuecomment-3"
				verify := typedArtifact(t, 3, "VERIFY", "VERIFY-001", "done", canonicalVerifyContent)
				linkArtifacts(t, &spec, &task)
				linkArtifacts(t, &task, &process)
				linkArtifacts(t, &spec, &process)
				artifacts := []model.Artifact{spec, task, process, verify}

				verifyOptions := finalVerifyOptions{}
				if target == gates.TargetArchive {
					verifyOptions.DurableSpecPath = statusValidDurableSpec(t, "https://github.com/o/r/issues/1", spec.URL)
				}
				verifyReport, err := buildFinalVerifyReport(artifacts, "https://github.com/o/r/issues/1", verifyOptions)
				if err != nil {
					t.Fatal(err)
				}
				status := summarizeStatusForGate("o/r", 1, 2, 3, target, artifacts, workflow.Plan{}, nil)
				if verifyReport.Gate.Target != target || status.Gate.Target != target {
					t.Fatalf("gate target drift: want=%s verify=%s status=%s", target, verifyReport.Gate.Target, status.Gate.Target)
				}
				if len(verifyReport.ProcessEvidence) != 1 || len(status.Gate.Processes) != 1 {
					t.Fatalf("process evidence missing: verify=%+v status=%+v", verifyReport.ProcessEvidence, status.Gate.Processes)
				}
				verifyLocal := localStatusGateCodes(verifyReport.Gate.Diagnostics)
				statusLocal := localStatusGateCodes(status.Gate.Diagnostics)
				if !reflect.DeepEqual(statusLocal, verifyLocal) {
					t.Fatalf("locally knowable gate drift: verify=%v status=%v", verifyLocal, statusLocal)
				}
				for _, code := range []string{gates.CodePRChecksUnknown, gates.CodeReviewFindingsUnknown} {
					if !statusHasCode(status, code) {
						t.Fatalf("status stopped reporting remote fact %s as unknown: %+v", code, status.Gate.Diagnostics)
					}
				}
			})
		}
	}
}

func statusValidDurableSpec(t *testing.T, proposalURL, specURL string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "spec.md")
	body := "# issue-spec-cli\n\n## Purpose\n\nPurpose.\n\nProposal Issues:\n- " + proposalURL +
		"\n\n## Requirements\n\n### Requirement: X\n\nX MUST work.\n\nSource SPEC comment: " + specURL + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestStatusDefaultProcessEvidenceDoesNotOverrideCollectedEvidence(t *testing.T) {
	const (
		specURL    = "https://github.com/o/r/issues/1#issuecomment-1"
		taskURL    = "https://github.com/o/r/issues/2#issuecomment-2"
		processURL = "https://github.com/o/r/issues/3#issuecomment-3"
		prURL      = "https://github.com/o/r/pull/7"
	)
	spec := typedArtifact(t, 1, "SPEC", "SPEC-001", "confirmed", "## Requirement: X\n\nX MUST work.\n\n### Scenario: ok\n\n- **WHEN** x\n- **THEN** y")
	spec.URL = specURL
	task := typedArtifact(t, 2, "TASK", "TASK-001", "done", canonicalTaskContent)
	task.URL = taskURL
	process := typedArtifact(t, 3, "PROCESS", "PROCESS-001", "done", canonicalProcessContentWithClass(model.ProcessExecutionChangeBearing))
	process.URL = processURL
	verify := typedArtifact(t, 3, "VERIFY", "VERIFY-001", "done", canonicalVerifyContent)
	linkArtifacts(t, &spec, &task)
	linkArtifacts(t, &task, &process)
	linkArtifacts(t, &spec, &process)
	processBody, _, err := model.AddPRLink(process.Comment.Body, prURL)
	if err != nil {
		t.Fatal(err)
	}
	process.Comment = model.ParseTypedComment(processBody)
	collection := statusGateCollection{
		Remote: statusForecastRemoteFacts(gates.TargetFinal),
		ProcessEvidence: []gates.ProcessEvidenceInput{{
			Process: process, RequiredPRURL: prURL,
			ActiveSpecs: map[string]string{"SPEC-001": specURL},
			TaskURLs:    map[string]bool{model.NormalizeURL(taskURL): true},
			Rationales: []gates.RationaleEvidence{{
				ProcessID: "PROCESS-001", SpecID: "SPEC-001", SpecURL: specURL,
				MarkerPath: "internal/foo.go", MarkerLine: 12, CommentPath: "internal/foo.go", CommentLine: 12,
				AuthorAgent: "Worker Agent A",
			}},
		}},
	}
	summary := summarizeStatusForGate("o/r", 1, 2, 3, gates.TargetFinal,
		[]model.Artifact{spec, task, process, verify}, workflow.Plan{}, nil, collection)
	if statusHasCode(summary, gates.CodeProcessCarrierMissing) {
		t.Fatalf("default local evidence overwrote collected carrier facts: %+v", summary.Gate.Diagnostics)
	}
	if !statusHasCode(summary, gates.CodeProcessReviewRequired) {
		t.Fatalf("collected carrier did not reach the independent-review gate: %+v", summary.Gate.Diagnostics)
	}
	for _, code := range []string{gates.CodePRChecksUnknown, gates.CodeReviewFindingsUnknown} {
		if !statusHasCode(summary, code) {
			t.Fatalf("status stopped reporting remote fact %s as unknown: %+v", code, summary.Gate.Diagnostics)
		}
	}
}

func TestStatusSurfacesCoordinatorAuthoredChangeBearingEvidence(t *testing.T) {
	const (
		specURL = "https://github.com/o/r/issues/1#issuecomment-1"
		taskURL = "https://github.com/o/r/issues/2#issuecomment-2"
		prURL   = "https://github.com/o/r/pull/7"
	)
	process := typedArtifactWithAgent(t, 3, "PROCESS", "PROCESS-001", "done", "Coordinator",
		canonicalProcessContentWithClass(model.ProcessExecutionChangeBearing))
	process.URL = "https://github.com/o/r/issues/3#issuecomment-3"
	processBody, _, err := model.AddPRLink(process.Comment.Body, prURL)
	if err != nil {
		t.Fatal(err)
	}
	process.Comment = model.ParseTypedComment(processBody)
	collection := statusGateCollection{
		Remote: statusForecastRemoteFacts(gates.TargetFinal),
		ProcessEvidence: []gates.ProcessEvidenceInput{{
			Process: process, RequiredPRURL: prURL,
			ActiveSpecs: map[string]string{"SPEC-001": specURL},
			TaskURLs:    map[string]bool{model.NormalizeURL(taskURL): true},
			Rationales: []gates.RationaleEvidence{{
				ProcessID: "PROCESS-001", SpecID: "SPEC-001", SpecURL: specURL,
				MarkerPath: "internal/foo.go", MarkerLine: 12, CommentPath: "internal/foo.go", CommentLine: 12,
				AuthorAgent: "Coordinator",
			}},
		}},
	}
	summary := summarizeStatusForGate("o/r", 1, 2, 3, gates.TargetFinal,
		[]model.Artifact{process}, workflow.Plan{}, nil, collection)
	if summary.OK || !statusHasCode(summary, gates.CodeProcessExecutorCoordinatorConflict) {
		t.Fatalf("final status must surface coordinator-authored change-bearing evidence: %+v", summary.Gate.Diagnostics)
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

func TestStatusForecastUsesSameExternalReviewCompletionCarrier(t *testing.T) {
	now := time.Date(2026, 7, 18, 1, 0, 0, 0, time.UTC)
	artifacts, externalReview := externalReviewCompletionFixture(t, now, "Independent Reviewer")
	collection := statusGateCollection{Remote: statusForecastRemoteFacts(gates.TargetFinal)}
	collection.Remote.ProviderEvidence = gates.Fact{Required: true, Known: true, Passed: true}
	collection.Remote.Workspace.ExpectedRevision = gates.Fact{Required: true, Known: true, Passed: true,
		Expected: externalReview.Target.SubjectRevision, Current: externalReview.Target.SubjectRevision}
	collection.ProcessEvidence = buildProcessEvidenceInputsWithExternalReview(artifacts, "", nil,
		reviewSyncReport{}, nil, &externalReview, now)
	summary := summarizeStatusForGate("acme/widgets", 1, 2, 3, gates.TargetFinal, artifacts, workflow.Plan{}, nil, collection)
	var reviewCarrier gates.CarrierRevisionFact
	for _, process := range summary.Gate.Processes {
		if process.ProcessID == "PROCESS-002" {
			reviewCarrier = process.CarrierRevision
		}
	}
	if !reviewCarrier.Trusted || reviewCarrier.Revision != externalReview.Target.SubjectRevision {
		t.Fatalf("status forecast discarded completion carrier: %+v", summary.Gate.Processes)
	}

	completion, found, err := parseExternalReviewCompletion(artifacts[3].Comment.Body)
	if err != nil || !found {
		t.Fatalf("completion found=%t err=%v", found, err)
	}
	completion.SynchronizedAt = now.Add(time.Minute + time.Nanosecond)
	futureBody, _, err := stampExternalReviewCompletion(artifacts[3].Comment.Body, completion)
	if err != nil {
		t.Fatal(err)
	}
	artifacts[3].Comment = model.ParseTypedComment(futureBody)
	collection.ProcessEvidence = buildProcessEvidenceInputsWithExternalReview(artifacts, "", nil,
		reviewSyncReport{}, nil, &externalReview, now)
	future := summarizeStatusForGate("acme/widgets", 1, 2, 3, gates.TargetFinal, artifacts, workflow.Plan{}, nil, collection)
	for _, process := range future.Gate.Processes {
		if process.ProcessID == "PROCESS-002" && process.CarrierRevision.Trusted {
			t.Fatalf("future completion survived status forecast: %+v", process)
		}
	}
}

func TestStatusWorkspaceUsesAuthoritativePullRequestAncestry(t *testing.T) {
	ancestor := strings.Repeat("a", 40)
	head := strings.Repeat("b", 40)
	unrelated := strings.Repeat("c", 40)
	process := statusWorkspaceProcess(t, model.ProcessExecutionChangeBearing, ancestor)
	collection := statusGateCollection{Remote: statusForecastRemoteFacts(gates.TargetFinal)}
	collection.Remote.Workspace.ExpectedRevision = gates.Fact{Required: true, Known: true, Passed: true, Expected: head}
	collection.Remote.Workspace.IntegrationAncestry = pullRequestIntegrationAncestry([]model.Artifact{process}, []github.PullRequestCommit{
		{SHA: strings.Repeat("0", 40)}, {SHA: ancestor}, {SHA: head},
	}, head)
	summary := summarizeStatusForGate("o/r", 1, 2, 3, gates.TargetFinal, []model.Artifact{process}, workflow.Plan{}, nil, collection)
	if statusHasCode(summary, gates.CodeProcessWorkspaceRevisionStale) {
		t.Fatalf("authoritative PR ancestor was rejected: %+v", summary.Gate.Diagnostics)
	}

	collection.Remote.Workspace.IntegrationAncestry = pullRequestIntegrationAncestry([]model.Artifact{process}, []github.PullRequestCommit{{SHA: unrelated}}, head)
	stale := summarizeStatusForGate("o/r", 1, 2, 3, gates.TargetFinal, []model.Artifact{process}, workflow.Plan{}, nil, collection)
	if !statusHasCode(stale, gates.CodeProcessWorkspaceRevisionStale) {
		t.Fatalf("unrelated integration was accepted: %+v", stale.Gate.Diagnostics)
	}

	collection.Remote.Workspace.IntegrationAncestry = pullRequestIntegrationAncestry([]model.Artifact{process}, []github.PullRequestCommit{{SHA: ancestor}}, head)
	missingHead := summarizeStatusForGate("o/r", 1, 2, 3, gates.TargetFinal, []model.Artifact{process}, workflow.Plan{}, nil, collection)
	if !statusHasCode(missingHead, gates.CodeProcessWorkspaceRevisionStale) {
		t.Fatalf("commit set missing expected head was accepted: %+v", missingHead.Gate.Diagnostics)
	}

	headProcess := statusWorkspaceProcess(t, model.ProcessExecutionChangeBearing, head)
	collection.Remote.Workspace.IntegrationAncestry = nil
	headSummary := summarizeStatusForGate("o/r", 1, 2, 3, gates.TargetFinal, []model.Artifact{headProcess}, workflow.Plan{}, nil, collection)
	if statusHasCode(headSummary, gates.CodeProcessWorkspaceRevisionStale) {
		t.Fatalf("exact PR head was rejected: %+v", headSummary.Gate.Diagnostics)
	}
}

func TestStatusWorkspaceHeadChangeFailsClosed(t *testing.T) {
	initialHead := strings.Repeat("a", 40)
	advancedHead := strings.Repeat("b", 40)
	process := statusWorkspaceProcess(t, model.ProcessExecutionChangeBearing, initialHead)
	body, _, err := model.AddPRLink(process.Comment.Body, "https://github.com/o/r/pull/7")
	if err != nil {
		t.Fatal(err)
	}
	process.Comment = model.ParseTypedComment(body)
	backend := &sequencedPullRequestCommitBackend{
		fakeGitHubBackend: fakeGitHubBackend{},
		pulls: []github.PullRequest{
			pullRequestAtHead(7, initialHead),
			pullRequestAtHead(7, advancedHead),
		},
		commits: []github.PullRequestCommit{{SHA: initialHead}},
	}
	collection := (&app{}).collectStatusGateFacts(t.Context(), backend, auth.Profile{Kind: auth.ProfileKindGitHub}, "", "o/r", 3, gates.TargetFinal, []model.Artifact{process})
	if collection.Remote.Workspace.ExpectedRevision.Known || len(collection.Remote.Workspace.IntegrationAncestry) != 0 || collection.Remote.PRChecks.Known {
		t.Fatalf("head-changing status snapshot was trusted: %+v", collection.Remote)
	}
}

func TestStatusWorkspacePRIdentityFailureFailsClosed(t *testing.T) {
	head := strings.Repeat("a", 40)
	valid := pullRequestAtHead(7, head)
	tests := []struct {
		name      string
		initial   github.PullRequest
		refreshed github.PullRequest
	}{
		{name: "missing number", initial: func() github.PullRequest { pr := valid; pr.Number = 0; return pr }(), refreshed: valid},
		{name: "empty identity", initial: func() github.PullRequest { pr := valid; pr.HTMLURL = ""; return pr }(), refreshed: valid},
		{name: "wrong final number", initial: valid, refreshed: func() github.PullRequest { pr := valid; pr.Number = 8; return pr }()},
		{name: "wrong repository URL", initial: func() github.PullRequest { pr := valid; pr.HTMLURL = "https://github.com/o/other/pull/7"; return pr }(), refreshed: func() github.PullRequest { pr := valid; pr.HTMLURL = "https://github.com/o/other/pull/7"; return pr }()},
		{name: "wrong PR URL", initial: func() github.PullRequest { pr := valid; pr.HTMLURL = "https://github.com/o/r/pull/8"; return pr }(), refreshed: func() github.PullRequest { pr := valid; pr.HTMLURL = "https://github.com/o/r/pull/8"; return pr }()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			process := statusWorkspaceProcess(t, model.ProcessExecutionChangeBearing, head)
			body, _, err := model.AddPRLink(process.Comment.Body, "https://github.com/o/r/pull/7")
			if err != nil {
				t.Fatal(err)
			}
			process.Comment = model.ParseTypedComment(body)
			backend := &sequencedPullRequestCommitBackend{fakeGitHubBackend: fakeGitHubBackend{},
				pulls: []github.PullRequest{tt.initial, tt.refreshed}, commits: []github.PullRequestCommit{{SHA: head}}}
			collection := (&app{}).collectStatusGateFacts(t.Context(), backend, auth.Profile{Kind: auth.ProfileKindGitHub}, "", "o/r", 3, gates.TargetFinal, []model.Artifact{process})
			if collection.Remote.Workspace.ExpectedRevision.Known || len(collection.Remote.Workspace.IntegrationAncestry) != 0 || collection.Remote.PRChecks.Known {
				t.Fatalf("invalid PR identity was trusted: %+v", collection.Remote)
			}
		})
	}
}

func TestSamePullRequestRevisionRequiresExactIdentity(t *testing.T) {
	head := strings.Repeat("a", 40)
	valid := pullRequestAtHead(7, head)
	if !samePullRequestRevision(valid, valid, "o/r", 7) {
		t.Fatal("valid stable PR identity was rejected")
	}
	zero := valid
	zero.Number = 0
	wrongRepo := valid
	wrongRepo.HTMLURL = "https://github.com/o/other/pull/7"
	wrongPR := valid
	wrongPR.HTMLURL = "https://github.com/o/r/pull/8"
	empty := valid
	empty.HTMLURL = ""
	for name, pair := range map[string][2]github.PullRequest{
		"zero number": {zero, zero}, "empty URL": {empty, empty}, "wrong repo": {wrongRepo, wrongRepo}, "wrong PR": {wrongPR, wrongPR},
	} {
		t.Run(name, func(t *testing.T) {
			if samePullRequestRevision(pair[0], pair[1], "o/r", 7) {
				t.Fatal("invalid PR identity was accepted")
			}
		})
	}
}

func TestStatusWorkspaceCommitCollectionFailureFailsClosed(t *testing.T) {
	ancestor := strings.Repeat("a", 40)
	head := strings.Repeat("b", 40)
	process := statusWorkspaceProcess(t, model.ProcessExecutionChangeBearing, ancestor)
	if _, err := listPullRequestCommits(t.Context(), failingPullRequestCommitBackend{fakeGitHubBackend: fakeGitHubBackend{}}, "o/r", 7); err == nil {
		t.Fatal("commit collection failure was ignored")
	}
	collection := statusGateCollection{Remote: statusForecastRemoteFacts(gates.TargetFinal)}
	collection.Remote.Workspace.ExpectedRevision = gates.Fact{Required: true, Known: true, Passed: true, Expected: head}
	summary := summarizeStatusForGate("o/r", 1, 2, 3, gates.TargetFinal, []model.Artifact{process}, workflow.Plan{}, nil, collection)
	if !statusHasCode(summary, gates.CodeProcessWorkspaceRevisionStale) {
		t.Fatalf("missing authoritative ancestry was accepted: %+v", summary.Gate.Diagnostics)
	}
}

type failingPullRequestCommitBackend struct{ fakeGitHubBackend }

func (failingPullRequestCommitBackend) ListPullRequestCommits(context.Context, string, int) ([]github.PullRequestCommit, error) {
	return nil, errors.New("commit collection failed")
}

type sequencedPullRequestCommitBackend struct {
	fakeGitHubBackend
	pulls      []github.PullRequest
	pullCalls  int
	commits    []github.PullRequestCommit
	commitsErr error
}

func (b *sequencedPullRequestCommitBackend) GetPullRequest(context.Context, string, int) (github.PullRequest, error) {
	if b.pullCalls >= len(b.pulls) {
		return github.PullRequest{}, errors.New("unexpected pull request read")
	}
	pr := b.pulls[b.pullCalls]
	b.pullCalls++
	return pr, nil
}

func (*sequencedPullRequestCommitBackend) ListPullRequestReviewComments(context.Context, string, int) ([]github.PullRequestReviewComment, error) {
	return nil, nil
}

func (*sequencedPullRequestCommitBackend) GetCombinedStatus(context.Context, string, string) (github.CombinedStatus, error) {
	return github.CombinedStatus{}, nil
}

func (*sequencedPullRequestCommitBackend) ListCheckRuns(context.Context, string, string) ([]github.CheckRun, error) {
	return nil, nil
}

func (b *sequencedPullRequestCommitBackend) ListPullRequestCommits(context.Context, string, int) ([]github.PullRequestCommit, error) {
	return b.commits, b.commitsErr
}

func pullRequestAtHead(number int, head string) github.PullRequest {
	pr := github.PullRequest{Number: number, HTMLURL: "https://github.com/o/r/pull/7"}
	pr.Head.SHA = head
	return pr
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

func TestExactStatusPullRequestUsesSharedActiveSelection(t *testing.T) {
	current := statusWorkspaceProcess(t, model.ProcessExecutionReview, strings.Repeat("a", 40))
	current.URL = "https://github.com/o/r/issues/3#issuecomment-current"
	body, _, err := model.AddPRLink(current.Comment.Body, "https://github.com/o/r/pull/7")
	if err != nil {
		t.Fatal(err)
	}
	current.Comment = model.ParseTypedComment(body)
	historical := statusWorkspaceProcess(t, model.ProcessExecutionReview, strings.Repeat("a", 40))
	historical.URL = "https://github.com/o/r/issues/3#issuecomment-historical"
	historicalBody, err := model.EnsureTypedBody("PROCESS", "PROCESS-002", model.LogicalBody(historical.Comment.Body),
		model.BodyOptions{Status: "superseded", Links: historical.Comment.Links})
	if err != nil {
		t.Fatal(err)
	}
	historical.Comment = model.ParseTypedComment(historicalBody)
	historicalBody, _, err = model.AddPRLink(historical.Comment.Body, "https://github.com/o/r/pull/8")
	if err != nil {
		t.Fatal(err)
	}
	historicalBody, _, err = model.StampSupersededBy(historicalBody, "PROCESS-002",
		model.SupersededBy{ProcessID: current.Comment.ID, URL: current.URL})
	if err != nil {
		t.Fatal(err)
	}
	historical.Comment = model.ParseTypedComment(historicalBody)
	if number, _, ok := exactStatusPullRequest([]model.Artifact{current, historical}, "o/r"); !ok || number != 7 {
		t.Fatalf("historical PR link affected active selection: number=%d ok=%v", number, ok)
	}

	legacyBody, err := model.EnsureTypedBody("PROCESS", "PROCESS-002", model.LogicalBody(historical.Comment.Body),
		model.BodyOptions{Status: "superseded", Links: historical.Comment.Links})
	if err != nil {
		t.Fatal(err)
	}
	historical.Comment = model.ParseTypedComment(legacyBody)
	if _, _, ok := exactStatusPullRequest([]model.Artifact{current, historical}, "o/r"); ok {
		t.Fatal("legacy status-only supersession incorrectly removed an ambiguous active PR carrier")
	}
}

func TestStatusAndVerifyUseSameActiveProcessSelection(t *testing.T) {
	spec := typedArtifact(t, 1, "SPEC", "SPEC-001", "confirmed", "## Requirement: X\n\nX MUST work.\n\n### Scenario: ok\n\n- **WHEN** x\n- **THEN** y")
	task := typedArtifact(t, 2, "TASK", "TASK-001", "done", canonicalTaskContent)
	current := typedArtifact(t, 3, "PROCESS", "PROCESS-001", "done", canonicalProcessContentWithClass(model.ProcessExecutionOrchestration))
	legacy := typedArtifact(t, 3, "PROCESS", "PROCESS-002", "superseded", canonicalProcessContentWithClass(model.ProcessExecutionOrchestration))
	historical := typedArtifact(t, 3, "PROCESS", "PROCESS-003", "superseded", canonicalProcessContentWithClass(model.ProcessExecutionOrchestration))
	verify := typedArtifact(t, 3, "VERIFY", "VERIFY-001", "done", canonicalVerifyContent)
	for _, process := range []*model.Artifact{&current, &legacy, &historical} {
		linkArtifacts(t, &task, process)
	}
	body, _, err := model.StampSupersededBy(historical.Comment.Body, historical.Comment.ID,
		model.SupersededBy{ProcessID: current.Comment.ID, URL: current.URL})
	if err != nil {
		t.Fatal(err)
	}
	historical.Comment = model.ParseTypedComment(body)
	artifacts := []model.Artifact{spec, task, current, legacy, historical, verify}
	status := summarizeStatusForGate("o/r", 1, 2, 3, gates.TargetFinal, artifacts, workflow.Plan{}, nil)
	final, err := buildFinalVerifyReport(artifacts, spec.URL, finalVerifyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for name, diagnostics := range map[string][]gates.Diagnostic{"status": status.Gate.Diagnostics, "verify": final.Gate.Diagnostics} {
		var legacyBlocked bool
		for _, diagnostic := range diagnostics {
			if diagnostic.Artifact.ID == historical.Comment.ID {
				t.Fatalf("%s evaluated explicitly historical PROCESS: %+v", name, diagnostics)
			}
			if diagnostic.Code == gates.CodeProcessNotDone && diagnostic.Artifact.ID == legacy.Comment.ID {
				legacyBlocked = true
			}
		}
		if !legacyBlocked {
			t.Fatalf("%s did not block legacy status-only supersession: %+v", name, diagnostics)
		}
	}
}

func TestStatusBindsPersistedProcessSelectionToExactImplementIssue(t *testing.T) {
	artifacts, currentID, foreignID := persistedCrossIssueProcessReplacement(t)
	summary := summarizeStatusForGate("o/r", 1, 2, 3, gates.TargetFinal, artifacts, workflow.Plan{}, nil)

	var currentBlocked bool
	for _, diagnostic := range summary.Gate.Diagnostics {
		if diagnostic.Artifact.ID == foreignID && diagnostic.Code != gates.CodeArtifactNoncanonical &&
			diagnostic.Code != gates.CodeTraceabilityInvalid {
			t.Fatalf("foreign PROCESS became status gate authority: %+v", summary.Gate.Diagnostics)
		}
		if diagnostic.Code == gates.CodeProcessNotDone && diagnostic.Artifact.ID == currentID {
			currentBlocked = true
		}
	}
	if !currentBlocked {
		t.Fatalf("cross-issue replacement removed the exact Implement PROCESS from status: %+v", summary.Gate.Diagnostics)
	}
	if statusHasCode(summary, gates.CodeSpecRequired) || statusHasCode(summary, gates.CodeTaskRequired) {
		t.Fatalf("Proposal SPEC or Design TASK disappeared from status projection: %+v", summary.Gate.Diagnostics)
	}
	if !strings.Contains(strings.Join(summary.Traceability.Errors, "\n"), foreignID) {
		t.Fatalf("full cross-issue traceability diagnostics were dropped: %+v", summary.Traceability)
	}
	if !canonicalDiagnosticsContain(summary.Malformed, foreignID) {
		t.Fatalf("full cross-issue canonical diagnostics were dropped: %+v", summary.Malformed)
	}
}

func persistedCrossIssueProcessReplacement(t *testing.T) ([]model.Artifact, string, string) {
	t.Helper()
	spec := typedArtifact(t, 1, "SPEC", "SPEC-001", "confirmed", "## Requirement: X\n\nX MUST work.\n\n### Scenario: ok\n\n- **WHEN** x\n- **THEN** y")
	task := typedArtifact(t, 2, "TASK", "TASK-001", "done", canonicalTaskContent)
	current := typedArtifact(t, 3, "PROCESS", "PROCESS-001", "done", canonicalProcessContentWithClass(model.ProcessExecutionOrchestration))
	foreign := typedArtifact(t, 1, "PROCESS", "PROCESS-900", "done", canonicalProcessContentWithClass(model.ProcessExecutionOrchestration))
	spec.URL = "https://github.com/o/r/issues/1#issuecomment-spec"
	task.URL = "https://github.com/o/r/issues/2#issuecomment-task"
	current.URL = "https://github.com/o/r/issues/3#issuecomment-current"
	foreign.URL = "https://github.com/o/r/issues/1#issuecomment-foreign"
	linkArtifacts(t, &spec, &task)
	linkArtifacts(t, &task, &current)

	body, err := model.EnsureTypedBody("PROCESS", current.Comment.ID, model.LogicalBody(current.Comment.Body),
		model.BodyOptions{Status: "superseded", Links: current.Comment.Links})
	if err != nil {
		t.Fatal(err)
	}
	body, _, err = model.StampSupersededBy(body, current.Comment.ID,
		model.SupersededBy{ProcessID: foreign.Comment.ID, URL: foreign.URL})
	if err != nil {
		t.Fatal(err)
	}
	current.Comment = model.ParseTypedComment(body)
	foreignBody, err := model.EnsureTypedBody("PROCESS", foreign.Comment.ID, "# hand-written foreign process",
		model.BodyOptions{Status: "done"})
	if err != nil {
		t.Fatal(err)
	}
	foreign.Comment = model.ParseTypedComment(foreignBody)
	verify := typedArtifact(t, 3, "VERIFY", "VERIFY-001", "done", canonicalVerifyContent)
	verify.URL = "https://github.com/o/r/issues/3#issuecomment-verify"
	return []model.Artifact{spec, task, current, foreign, verify}, current.Comment.ID, foreign.Comment.ID
}

func canonicalDiagnosticsContain(diagnostics []model.CanonicalDiagnostic, id string) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.ID == id {
			return true
		}
	}
	return false
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
	case model.ProcessExecutionChangeBearing:
		workspace.Mode, workspace.BaseSHA, workspace.Branch, workspace.ResultCommit, workspace.IntegrationSHA, workspace.RuntimeNamespace =
			processworkspace.ModeWritable, strings.Repeat("0", 40), "codex/process-001", strings.Repeat("1", 40), revision, "ws-process-001"
		workspace.WriteOwnership = []string{"internal/x"}
		workspace.State = processworkspace.StateIntegrated
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

func statusGateDiagnostic(summary statusSummary, code string) *gates.Diagnostic {
	for index := range summary.Gate.Diagnostics {
		if summary.Gate.Diagnostics[index].Code == code {
			return &summary.Gate.Diagnostics[index]
		}
	}
	return nil
}

func TestSummarizeStatusDoesNotRequireSessionMetadata(t *testing.T) {
	specBody, err := model.EnsureTypedBody("SPEC", "SPEC-001", "## Requirement: X\n\nX MUST work.\n\n### Scenario: ok\n\n- **WHEN** x\n- **THEN** y", model.BodyOptions{Status: "confirmed"})
	if err != nil {
		t.Fatal(err)
	}
	summary := summarizeStatus("o/r", 1, 0, 0, []model.Artifact{
		{Issue: 1, URL: "https://github.com/o/r/issues/1#issuecomment-1", Comment: model.ParseTypedComment(specBody)},
	})
	if !summary.OK {
		t.Fatalf("status should remain OK: %+v", summary.NextGates)
	}
	if len(summary.Diagnostics) != 0 {
		t.Fatalf("status should not report session metadata diagnostics: %+v", summary.Diagnostics)
	}
}
