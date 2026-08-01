package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/assignment"
	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/codereview"
	"github.com/higress-group/issue-spec/internal/durable"
	coreevidence "github.com/higress-group/issue-spec/internal/evidence"
	"github.com/higress-group/issue-spec/internal/gates"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/model"
	"github.com/higress-group/issue-spec/internal/processworkspace"
	"github.com/higress-group/issue-spec/internal/relationships"
	"github.com/higress-group/issue-spec/internal/templates"
)

const (
	canonicalTaskContent    = "## Task: work\n\n### Implementation Checklist\n\n- [x] 1. work\n\n### Execution Planning\n\n- Owned modules / write areas:\n  - internal/x\n- Coupling class: low\n- Recommended execution mode: coordinator-owned\n\n### Covers\n\n- SPEC-001"
	canonicalProcessContent = "## Process: impl\n\n### Owner\n\n- Worker\n\n### Parent TASK\n\n- TASK-001\n\n### Write Ownership\n\n- internal/x\n\n### Dependencies\n\n- N/A\n\n### Covers\n\n- TASK-001\n\n### Handoff\n\nN/A"
	canonicalReviewProcess  = "## Process: review\n\n### Owner\n\n- Reviewer\n\n### Parent TASK\n\n- TASK-001\n\n### Execution Class\n\n- review\n\n### Write Ownership\n\n- N/A\n\n### Dependencies\n\n- N/A\n\n### Covers\n\n- TASK-001\n- SPEC-001\n\n### Handoff\n\nN/A"
	canonicalVerifyContent  = "## Verification Summary: final\n\nTests, review, and traceability confirmed.\n\n### Evidence\n\n- go test ./...\n\n### Covered SPECs\n\n- SPEC-001"
)

func TestVerificationReceiptBindingAndImmutableProjection(t *testing.T) {
	receipt := testSealedVerificationReceipt(t, []assignment.TestResult{{ID: "unit", Command: "go test ./internal/gates",
		Outcome: assignment.TestPassed, Assurance: assignment.AssuranceSelfReported}},
		[]assignment.CheckSelector{{Provider: "github", Name: "unit"}})
	sealed := testVerificationAssignment(t, receipt.SubjectRevision, receipt.Tests, receipt.Verification.CheckSelectors)
	binding := &processworkspace.AssignmentBinding{SchemaVersion: assignment.AssignmentSchemaVersion,
		AssignmentID: receipt.AssignmentID, Digest: receipt.AssignmentDigest, Role: assignment.RoleVerification,
		SubjectRevision: receipt.SubjectRevision, Generation: receipt.AssignmentGeneration}
	submission := testVerificationSubmission("Verifier")
	if err := validateVerificationReceiptBinding(receipt, sealed, binding, submission); err != nil {
		t.Fatal(err)
	}
	checks := []observedVerificationCheck{{Provider: "github", Name: "unit", EvidenceID: "42", State: "success",
		SubjectRevision: receipt.SubjectRevision, Source: "github-check-run:42"}}
	projected := acceptedVerificationReceiptFrom(receipt, checks, submission)
	body, changed, err := stampAcceptedVerificationReceipt("canonical VERIFY\n", projected)
	if err != nil || !changed {
		t.Fatalf("changed=%t err=%v", changed, err)
	}
	parsed, found, err := parseAcceptedVerificationReceipt(body)
	if err != nil || !found || parsed.ReceiptDigest != receipt.ReceiptDigest || len(parsed.Tests) != 1 || len(parsed.Checks) != 1 {
		t.Fatalf("parsed=%+v found=%t err=%v", parsed, found, err)
	}
	if retry, changed, err := stampAcceptedVerificationReceipt(body, projected); err != nil || changed || retry != body {
		t.Fatalf("retry changed=%t err=%v", changed, err)
	}
	other := projected
	other.ReceiptID = "receipt-verification-other"
	if _, _, err := stampAcceptedVerificationReceipt(body, other); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("conflicting accepted receipt error=%v", err)
	}
}

func TestBuildMinimalFinalEvidenceIndexesAcceptedRecordsWithoutProcessWrites(t *testing.T) {
	const (
		revision = "head-abc"
		specID   = "SPEC-001"
		specURL  = "https://github.com/o/r/issues/9#issuecomment-spec"
		prURL    = "https://github.com/o/r/pull/7"
	)
	processBody, err := model.EnsureTypedBody("PROCESS", "PROCESS-101",
		"## Process: implementation\n\n### Parent TASK\n\n- TASK-001\n\n### Execution Class\n\n- change-bearing\n\n### Covers\n\n- "+specID,
		model.BodyOptions{Status: "in-progress", Links: map[string][]string{"PR": {prURL}}})
	if err != nil {
		t.Fatal(err)
	}
	process := model.Artifact{URL: "https://github.com/o/r/issues/9#issuecomment-process", Comment: model.ParseTypedComment(processBody)}
	processBodyBefore := process.Comment.Body

	reviewReceipt := testSealedReviewReceipt(t, assignment.ReviewApprove, nil)
	reviewBody, err := renderSubmittedReview("REVIEW-101", process.Comment.ID, process.URL, prURL, []string{specURL}, reviewReceipt)
	if err != nil {
		t.Fatal(err)
	}
	reviewBody = downgradeAcceptedReviewBodyToV1(t, reviewBody)
	review := model.Artifact{URL: "https://github.com/o/r/issues/9#issuecomment-review", Comment: model.ParseTypedComment(reviewBody)}

	tests := []assignment.TestResult{{ID: "unit", Command: "go test ./internal/gates", Outcome: assignment.TestPassed,
		Assurance: assignment.AssuranceSelfReported}}
	selectors := []assignment.CheckSelector{{Provider: "github", Name: "unit"}}
	sealed := testVerificationAssignment(t, revision, tests, selectors)
	verificationReceipt := testSealedVerificationReceiptForAssignment(t, sealed, tests, selectors)
	checks := []observedVerificationCheck{{Provider: "github", Name: "unit", EvidenceID: "42", State: "success",
		SubjectRevision: revision, Source: "github-check-run:42"}}
	verifyBody, err := renderSubmittedVerification("VERIFY-101", process.URL, []string{specID}, verificationReceipt,
		checks, testVerificationSubmission("Verifier"))
	if err != nil {
		t.Fatal(err)
	}
	verify := model.Artifact{URL: "https://github.com/o/r/issues/9#issuecomment-verify", Comment: model.ParseTypedComment(verifyBody)}

	input := gates.ProcessEvidenceInput{Process: process, ActiveSpecs: map[string]string{specID: specURL},
		AuthorAgentsBySpec: map[string]map[string]bool{specID: {"implementation worker": true}},
		Reviews: []gates.ReviewEvidence{{ProcessID: process.Comment.ID, SpecID: specID, URL: review.URL, Done: true,
			ReviewerAgent: "Independent Reviewer", SubjectRevision: revision, Trusted: true, Source: "accepted-review-receipt:self-reported"}},
		Verifications: []gates.VerificationEvidence{{ProcessID: process.Comment.ID, SpecID: specID, URL: verify.URL, Done: true,
			TestEvidence: true, SubjectRevision: revision, Trusted: true,
			Source: "accepted-verification-receipt:mixed-self-reported-tests-and-provider-checks"}},
	}
	snapshot := buildMinimalFinalEvidence([]model.Artifact{process, review, verify}, []gates.ProcessEvidenceInput{input},
		gates.FinalSubject{Required: true, Known: true, Trusted: true, Kind: "pull_request", URL: prURL,
			Revision: revision, Source: "github-pull-request-head:7"})
	if !snapshot.Index.Passed || len(snapshot.Records) != 4 {
		t.Fatalf("accepted canonical records were not indexed: index=%+v records=%+v", snapshot.Index, snapshot.Records)
	}
	wantKinds := map[gates.FinalEvidenceKind]bool{gates.FinalEvidenceReview: true, gates.FinalEvidenceVerification: true,
		gates.FinalEvidenceTest: true, gates.FinalEvidenceCheck: true}
	for _, record := range snapshot.Records {
		delete(wantKinds, record.Kind)
		if record.ProcessID != process.Comment.ID || record.SpecID != specID || record.SubjectRevision != revision {
			t.Fatalf("record escaped exact PROCESS/SPEC/revision scope: %+v", record)
		}
	}
	if len(wantKinds) != 0 {
		t.Fatalf("missing canonical evidence kinds: %v", wantKinds)
	}
	if process.Comment.Body != processBodyBefore {
		t.Fatal("in-memory evidence indexing mutated PROCESS")
	}

	stale := buildMinimalFinalEvidence([]model.Artifact{process, review, verify}, []gates.ProcessEvidenceInput{input},
		gates.FinalSubject{Required: true, Known: true, Trusted: true, Kind: "pull_request", URL: prURL,
			Revision: "head-new", Source: "github-pull-request-head:7"})
	if !stale.Index.Passed || len(stale.Records) != 0 {
		t.Fatalf("stale accepted evidence entered the exact-current index: %+v", stale)
	}
}

func TestBuildMinimalFinalEvidenceIgnoresSupersededGenerationAndSelectsActiveBoundReceipt(t *testing.T) {
	subject := strings.Repeat("b", 40)
	selector := assignment.TestSelector{ID: "durable", Command: "issue-spec durable-spec check --repo o/r --proposal 381 --root . --json",
		RevisionBinding: &assignment.RevisionBinding{Source: assignment.RevisionBindingSourceSubjectRevision,
			Argument: assignment.RevisionBindingArgumentSubject}}
	processBody, err := model.EnsureTypedBody("PROCESS", "PROCESS-901",
		"## Process: verify active generation\n\n### Parent TASK\n\n- TASK-001\n\n### Execution Class\n\n- verification\n\n### Covers\n\n- SPEC-001\n\n### Handoff\n\nN/A",
		model.BodyOptions{Status: "done"})
	if err != nil {
		t.Fatal(err)
	}
	process := model.Artifact{URL: "https://example.test/process/901", Comment: model.ParseTypedComment(processBody)}
	currentAssignment := testVerificationAssignment(t, subject, []assignment.TestResult{resolvedCommandTestResult(t, selector, subject)}, nil)
	currentAssignment.ID = "assignment-active-2"
	currentDigest, err := assignment.AssignmentDigest(currentAssignment)
	if err != nil {
		t.Fatal(err)
	}
	oldAssignment := currentAssignment
	oldAssignment.ID = "assignment-history-1"

	makeReceipt := func(t *testing.T, sealed assignment.Assignment, generation uint64, id string,
		outcome assignment.TestOutcome) assignment.Receipt {
		t.Helper()
		result := resolvedCommandTestResult(t, selector, subject)
		result.Outcome = outcome
		receipt := testSealedVerificationReceiptForAssignmentGeneration(t, sealed, generation,
			[]assignment.TestResult{result}, nil)
		receipt.ID, receipt.ReceiptDigest = id, ""
		receipt, err := assignment.SealReceipt(receipt)
		if err != nil {
			t.Fatal(err)
		}
		return receipt
	}
	makeCarrier := func(t *testing.T, id string, receipt assignment.Receipt) model.Artifact {
		t.Helper()
		body, err := renderSubmittedVerification(id, process.URL, []string{"SPEC-001"}, receipt, nil,
			testVerificationSubmission("Verifier"))
		if err != nil {
			t.Fatal(err)
		}
		return model.Artifact{URL: "https://example.test/verify/" + id, Comment: model.ParseTypedComment(body)}
	}
	historicalFailure := makeCarrier(t, "VERIFY-901", makeReceipt(t, oldAssignment, 1, "receipt-failed-1", assignment.TestFailed))
	historicalSupplement := makeCarrier(t, "VERIFY-902", makeReceipt(t, oldAssignment, 1, "receipt-supplement-1", assignment.TestPassed))
	current := makeCarrier(t, "VERIFY-903", makeReceipt(t, currentAssignment, 2, "receipt-active-2", assignment.TestPassed))
	input := gates.ProcessEvidenceInput{Process: process, ActiveSpecs: map[string]string{"SPEC-001": "https://example.test/spec/1"},
		ActiveAssignment: &gates.ActiveAssignmentEvidence{ProcessID: process.Comment.ID,
			AssignmentID: currentAssignment.ID, AssignmentDigest: currentDigest, Generation: 2,
			Role: assignment.RoleVerification, SubjectRevision: subject, RequiredTests: []assignment.TestSelector{selector}},
		Verifications: []gates.VerificationEvidence{
			{ProcessID: process.Comment.ID, SpecID: "SPEC-001", URL: historicalFailure.URL, Done: true,
				SubjectRevision: subject, Trusted: true, Source: "accepted-verification-receipt:self-reported-tests"},
			{ProcessID: process.Comment.ID, SpecID: "SPEC-001", URL: historicalSupplement.URL, Done: true,
				SubjectRevision: subject, Trusted: true, Source: "accepted-verification-receipt:self-reported-tests"},
			{ProcessID: process.Comment.ID, SpecID: "SPEC-001", URL: current.URL, Done: true,
				SubjectRevision: subject, Trusted: true, Source: "accepted-verification-receipt:self-reported-tests"},
		}}
	snapshot := buildMinimalFinalEvidence([]model.Artifact{process, historicalFailure, historicalSupplement, current},
		[]gates.ProcessEvidenceInput{input}, gates.FinalSubject{Required: true, Known: true, Trusted: true,
			Kind: "pull_request", URL: "https://example.test/pull/7", Revision: subject, Source: "github-pull-request-head:7"})
	if !snapshot.Index.Passed || len(snapshot.Records) != 2 {
		t.Fatalf("active generation snapshot index=%+v records=%+v", snapshot.Index, snapshot.Records)
	}
	for _, record := range snapshot.Records {
		if record.ReceiptID != "receipt-active-2" || record.AssignmentID != currentAssignment.ID ||
			record.AssignmentGeneration != 2 || record.AssignmentProcessID != process.Comment.ID {
			t.Fatalf("superseded generation entered final snapshot: %+v", record)
		}
		if record.Kind == gates.FinalEvidenceTest && (record.AssignedSelector == nil ||
			record.ResolvedRevision != subject || !assignment.TestSelectorIdentityEqual(*record.AssignedSelector, selector)) {
			t.Fatalf("active bound test lost stable/executed identity: %+v", record)
		}
	}
}

func TestBuildMinimalFinalEvidenceProjectsSharedRoleReceiptsFromIssuingAssignments(t *testing.T) {
	fixture := newMultiCarrierRoleEvidenceFixture(t)
	snapshot := fixture.snapshot()
	if !snapshot.Index.Passed {
		t.Fatalf("multi-carrier role evidence index failed: %+v", snapshot.Index)
	}
	for _, target := range fixture.targetProcessIDs {
		seen := map[gates.FinalEvidenceKind]bool{}
		seenReviewTest, seenVerificationTest := false, false
		for _, record := range snapshot.Records {
			if record.ProcessID != target {
				continue
			}
			seen[record.Kind] = true
			switch record.Kind {
			case gates.FinalEvidenceReview:
				if record.AssignmentProcessID != fixture.reviewProcessID || record.AssignmentRole != assignment.RoleReview {
					t.Fatalf("projected REVIEW changed assignment authority: %+v", record)
				}
			case gates.FinalEvidenceVerification:
				if record.AssignmentProcessID != fixture.verifyProcessID || record.AssignmentRole != assignment.RoleVerification {
					t.Fatalf("projected VERIFY changed assignment authority: %+v", record)
				}
			case gates.FinalEvidenceTest:
				switch record.AssignmentRole {
				case assignment.RoleReview:
					seenReviewTest = true
					if record.AssignmentProcessID != fixture.reviewProcessID ||
						!strings.HasPrefix(record.Source, "accepted-review-receipt:") ||
						len(record.ReceiptDigest) != 64 || record.AssignmentGeneration != 1 ||
						record.SubjectRevision != fixture.subject || record.AssignedSelector == nil ||
						record.ResolvedRevision != fixture.subject {
						t.Fatalf("projected review test changed assignment/source authority: %+v", record)
					}
					resolved, err := assignment.ResolveTestSelector(*record.AssignedSelector, record.ResolvedRevision)
					if err != nil || resolved.Command != record.ExecutedCommand {
						t.Fatalf("projected review test lost reproducible command identity: %+v err=%v", record, err)
					}
				case assignment.RoleVerification:
					seenVerificationTest = true
					if record.AssignmentProcessID != fixture.verifyProcessID ||
						!strings.HasPrefix(record.Source, "accepted-verification-receipt:") {
						t.Fatalf("projected verification test changed assignment/source authority: %+v", record)
					}
				default:
					t.Fatalf("projected test lacks explicit authority role: %+v", record)
				}
			}
		}
		for _, kind := range []gates.FinalEvidenceKind{gates.FinalEvidenceReview, gates.FinalEvidenceVerification, gates.FinalEvidenceTest} {
			if !seen[kind] {
				t.Fatalf("target %s lacks projected %s evidence: %+v", target, kind, snapshot.Records)
			}
		}
		if !seenReviewTest || !seenVerificationTest {
			t.Fatalf("target %s lacks role-separated review/verification tests: %+v", target, snapshot.Records)
		}
	}
}

func TestBuildMinimalFinalEvidenceReviewTestCarrierTamperingFailsClosed(t *testing.T) {
	otherSubject := strings.Repeat("c", 40)
	for name, mutate := range map[string]func(*acceptedReviewReceipt){
		"changed selector": func(value *acceptedReviewReceipt) {
			changed := cloneFinalTestSelector(*value.Tests[0].AssignedSelector)
			changed.Command += " --changed"
			resolved, err := assignment.ResolveTestSelector(changed, value.SubjectRevision)
			if err != nil {
				t.Fatal(err)
			}
			value.Tests[0].AssignedSelector = &changed
			value.Tests[0].Command = resolved.Command
		},
		"changed command":  func(value *acceptedReviewReceipt) { value.Tests[0].Command += " --forged" },
		"changed revision": func(value *acceptedReviewReceipt) { value.Tests[0].ResolvedRevision = otherSubject },
		"failed":           func(value *acceptedReviewReceipt) { value.Tests[0].Outcome = assignment.TestFailed },
		"skipped":          func(value *acceptedReviewReceipt) { value.Tests[0].Outcome = assignment.TestSkipped },
		"wrong assurance": func(value *acceptedReviewReceipt) {
			value.Tests[0].Assurance = assignment.AssuranceProviderOwned
		},
		"duplicate": func(value *acceptedReviewReceipt) {
			value.Tests = append(value.Tests, value.Tests[0])
		},
		"extra": func(value *acceptedReviewReceipt) {
			value.Tests = append(value.Tests, acceptedReviewTest{ID: "extra", Command: "go test ./extra",
				Outcome: assignment.TestPassed, Assurance: assignment.AssuranceSelfReported})
		},
		"wrong assignment": func(value *acceptedReviewReceipt) { value.AssignmentID = "assignment-review-other" },
		"wrong digest":     func(value *acceptedReviewReceipt) { value.AssignmentDigest = strings.Repeat("f", 64) },
		"wrong generation": func(value *acceptedReviewReceipt) { value.AssignmentGeneration++ },
		"wrong subject":    func(value *acceptedReviewReceipt) { value.SubjectRevision = otherSubject },
		"wrong process": func(value *acceptedReviewReceipt) {
			value.AssignmentProcessID = "PROCESS-911"
		},
	} {
		t.Run(name, func(t *testing.T) {
			fixture := newMultiCarrierRoleEvidenceFixture(t)
			mutateReviewCarrier(t, &fixture, mutate)
			snapshot := fixture.snapshot()
			for _, record := range snapshot.Records {
				if record.Kind == gates.FinalEvidenceTest && record.AssignmentRole == assignment.RoleReview {
					t.Fatalf("tampered review test entered final evidence: %+v", record)
				}
			}
		})
	}

	t.Run("legacy marker without tests", func(t *testing.T) {
		fixture := newMultiCarrierRoleEvidenceFixture(t)
		downgradeReviewCarrierToV1(t, &fixture)
		snapshot := fixture.snapshot()
		if !snapshot.Index.Passed {
			t.Fatalf("historical compact review marker became unreadable: %+v", snapshot.Index)
		}
		reviewSeen := false
		for _, record := range snapshot.Records {
			reviewSeen = reviewSeen || record.Kind == gates.FinalEvidenceReview
			if record.Kind == gates.FinalEvidenceTest && record.AssignmentRole == assignment.RoleReview {
				t.Fatalf("historical marker without tests supplied test evidence: %+v", record)
			}
		}
		if !reviewSeen {
			t.Fatal("historical marker without tests no longer supplies its accepted review verdict")
		}
	})
}

func TestBuildMinimalFinalEvidenceUsesV2IssuingProcessWithoutReviewBacklink(t *testing.T) {
	fixture := newMultiCarrierRoleEvidenceFixture(t)
	var reviewURL string
	for _, artifact := range fixture.artifacts {
		if artifact.Comment.Type == "REVIEW" {
			reviewURL = artifact.URL
			break
		}
	}
	for index := range fixture.artifacts {
		artifact := &fixture.artifacts[index]
		if artifact.Comment.Type != "PROCESS" && artifact.Comment.Type != "SPEC" {
			continue
		}
		if artifact.Comment.Links == nil {
			artifact.Comment.Links = map[string][]string{}
		}
		artifact.Comment.Links["Related Comments"] = append(artifact.Comment.Links["Related Comments"], reviewURL)
	}
	withBacklinks, err := relationships.BuildIndex(fixture.artifacts)
	if err != nil || withBacklinks.Totals.LegacyBacklink == 0 {
		t.Fatalf("fixture did not classify legacy reverse backlinks: index=%+v err=%v", withBacklinks, err)
	}
	discovered := buildProcessEvidenceInputs(fixture.artifacts, "", nil, reviewSyncReport{}, nil)
	for index := range discovered {
		for _, original := range fixture.inputs {
			if original.Process.Comment.ID != discovered[index].Process.Comment.ID {
				continue
			}
			discovered[index].ActiveAssignment = original.ActiveAssignment
			discovered[index].AuthorAgentsBySpec = original.AuthorAgentsBySpec
			break
		}
	}
	fixture.inputs = discovered
	wantSnapshot := fixture.snapshot()
	for index := range fixture.artifacts {
		artifact := &fixture.artifacts[index]
		if artifact.Comment.Type == "PROCESS" || artifact.Comment.Type == "SPEC" {
			artifact.Comment.Links["Related Comments"] = nil
		}
	}
	withoutBacklinks, err := relationships.BuildIndex(fixture.artifacts)
	if err != nil || withoutBacklinks.Totals.LegacyBacklink != 0 ||
		withoutBacklinks.Totals.Canonical != withBacklinks.Totals.Canonical {
		t.Fatalf("removing reverse backlinks changed canonical graph: before=%+v after=%+v err=%v",
			withBacklinks.Totals, withoutBacklinks.Totals, err)
	}
	snapshot := fixture.snapshot()
	if !reflect.DeepEqual(snapshot, wantSnapshot) {
		t.Fatalf("reverse backlinks changed evidence selection:\nwant=%+v\n got=%+v", wantSnapshot, snapshot)
	}
	if !snapshot.Index.Passed {
		t.Fatalf("v2 issuing PROCESS identity required a reverse backlink: %+v", snapshot.Index)
	}
	seen := map[string]bool{}
	for _, record := range snapshot.Records {
		if record.AssignmentRole != assignment.RoleReview {
			continue
		}
		if record.AssignmentProcessID != fixture.reviewProcessID {
			t.Fatalf("v2 projection changed issuing PROCESS: %+v", record)
		}
		seen[record.ProcessID] = true
	}
	for _, processID := range append([]string{fixture.reviewProcessID}, fixture.targetProcessIDs...) {
		if !seen[processID] {
			t.Fatalf("v2 REVIEW evidence was not projected to %s: %+v", processID, snapshot.Records)
		}
	}
	var issuing, projected *gates.FinalEvidenceRecord
	for index := range snapshot.Records {
		record := &snapshot.Records[index]
		if record.Kind != gates.FinalEvidenceTest || record.AssignmentRole != assignment.RoleReview {
			continue
		}
		if record.ProcessID == fixture.reviewProcessID {
			issuing = record
		}
		if record.ProcessID == fixture.targetProcessIDs[0] {
			projected = record
		}
	}
	if issuing == nil || projected == nil {
		t.Fatalf("missing issuing/projected REVIEW test records: %+v", snapshot.Records)
	}
	wantProjected := *issuing
	wantProjected.ProcessID = fixture.targetProcessIDs[0]
	if !reflect.DeepEqual(*projected, wantProjected) {
		t.Fatalf("relationship projection changed #394 evidence authority:\nwant=%+v\n got=%+v", wantProjected, *projected)
	}
	if projected.AssignmentRole != assignment.RoleReview ||
		projected.AssignmentProcessID != fixture.reviewProcessID ||
		projected.Source != "accepted-review-receipt:self-reported" {
		t.Fatalf("explicit role/process/source authority drifted: %+v", *projected)
	}
}

func TestBuildMinimalFinalEvidenceV2ReviewAssignmentCandidatesRequireActiveAuthority(t *testing.T) {
	t.Run("unrelated unmanaged process preserves valid index", func(t *testing.T) {
		fixture := newMultiCarrierRoleEvidenceFixture(t)
		want := fixture.snapshot()
		if !want.Index.Passed {
			t.Fatalf("fixture does not start with a valid index: %+v", want.Index)
		}

		unmanaged := finalEvidenceRoleProcess(t, "PROCESS-915", assignment.RoleImplementation, fixture.specID)
		fixture.artifacts = append(fixture.artifacts, unmanaged)
		fixture.inputs = append(fixture.inputs, gates.ProcessEvidenceInput{Process: unmanaged,
			ActiveSpecs: map[string]string{fixture.specID: fixture.specURL}})

		got := fixture.snapshot()
		if !got.Index.Passed {
			t.Fatalf("unrelated unmanaged PROCESS changed valid v2 REVIEW authority: %+v", got.Index)
		}
		if !reflect.DeepEqual(got.Records, want.Records) {
			t.Fatalf("unrelated unmanaged PROCESS changed valid v2 REVIEW records:\nwant=%+v\n got=%+v", want.Records, got.Records)
		}
	})

	t.Run("zero real review authority fails closed", func(t *testing.T) {
		fixture := newMultiCarrierRoleEvidenceFixture(t)
		fixture.roleInput(assignment.RoleReview).ActiveAssignment = nil
		snapshot := fixture.snapshot()
		if snapshot.Index.Passed || !strings.Contains(snapshot.Index.Current, "no active assignment authority") {
			t.Fatalf("v2 REVIEW without a real active assignment index=%+v records=%+v", snapshot.Index, snapshot.Records)
		}
	})

	t.Run("duplicate real review authority fails closed", func(t *testing.T) {
		fixture := newMultiCarrierRoleEvidenceFixture(t)
		original := fixture.roleInput(assignment.RoleReview)
		duplicate := *original
		duplicate.Process = finalEvidenceRoleProcess(t, "PROCESS-915", assignment.RoleReview, fixture.specID)
		active := *original.ActiveAssignment
		active.ProcessID = duplicate.Process.Comment.ID
		duplicate.ActiveAssignment = &active
		duplicate.Reviews = nil
		fixture.artifacts = append(fixture.artifacts, duplicate.Process)
		fixture.inputs = append(fixture.inputs, duplicate)

		snapshot := fixture.snapshot()
		if snapshot.Index.Passed || !strings.Contains(snapshot.Index.Current, "duplicate active assignment authority") {
			t.Fatalf("duplicate real v2 REVIEW authority index=%+v records=%+v", snapshot.Index, snapshot.Records)
		}
	})
}

func mutateReviewCarrier(t *testing.T, fixture *multiCarrierRoleEvidenceFixture,
	mutate func(*acceptedReviewReceipt)) {
	t.Helper()
	for index := range fixture.artifacts {
		artifact := &fixture.artifacts[index]
		if artifact.Comment.Type != "REVIEW" {
			continue
		}
		authority, found, err := parseAcceptedReviewReceipt(artifact.Comment.Body)
		if err != nil || !found {
			t.Fatalf("parse accepted review carrier: found=%t err=%v", found, err)
		}
		mutate(&authority)
		raw, err := json.Marshal(authority)
		if err != nil {
			t.Fatal(err)
		}
		start := strings.Index(artifact.Comment.Body, acceptedReviewReceiptStart)
		end := strings.Index(artifact.Comment.Body, acceptedReviewReceiptEnd) + len(acceptedReviewReceiptEnd)
		block := acceptedReviewReceiptStart + "\n" + string(raw) + "\n" + acceptedReviewReceiptEnd
		artifact.Comment = model.ParseTypedComment(artifact.Comment.Body[:start] + block + artifact.Comment.Body[end:])
		return
	}
	t.Fatal("fixture lacks accepted REVIEW carrier")
}

func downgradeReviewCarrierToV1(t *testing.T, fixture *multiCarrierRoleEvidenceFixture) {
	t.Helper()
	for index := range fixture.artifacts {
		artifact := &fixture.artifacts[index]
		if artifact.Comment.Type != "REVIEW" {
			continue
		}
		artifact.Comment = model.ParseTypedComment(downgradeAcceptedReviewBodyToV1(t, artifact.Comment.Body))
		return
	}
	t.Fatal("fixture lacks accepted REVIEW carrier")
}

func downgradeAcceptedReviewBodyToV1(t *testing.T, body string) string {
	t.Helper()
	authority, found, err := parseAcceptedReviewReceipt(body)
	if err != nil || !found || authority.CarrierVersion != 2 {
		t.Fatalf("parse version-2 review carrier: authority=%+v found=%t err=%v", authority, found, err)
	}
	v1 := acceptedReviewReceiptV1{ReceiptID: authority.ReceiptID, ReceiptDigest: authority.ReceiptDigest,
		AssignmentID: authority.AssignmentID, AssignmentDigest: authority.AssignmentDigest,
		AssignmentGeneration: authority.AssignmentGeneration, SubjectRevision: authority.SubjectRevision,
		Verdict: authority.Verdict, FindingIDs: authority.FindingIDs, Provenance: authority.Provenance}
	raw, err := json.Marshal(v1)
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(body, acceptedReviewReceiptStart)
	end := strings.Index(body, acceptedReviewReceiptEnd) + len(acceptedReviewReceiptEnd)
	block := acceptedReviewReceiptV1Start + "\n" + string(raw) + "\n" + acceptedReviewReceiptEnd
	return body[:start] + block + body[end:]
}

func TestBuildMinimalFinalEvidenceRoleAuthorityMismatchFailsClosedForReviewAndVerify(t *testing.T) {
	for _, role := range []assignment.Role{assignment.RoleReview, assignment.RoleVerification} {
		for name, mutate := range map[string]func(*multiCarrierRoleEvidenceFixture){
			"wrong role": func(fixture *multiCarrierRoleEvidenceFixture) {
				fixture.roleInput(role).ActiveAssignment.Role = assignment.RoleImplementation
			},
			"wrong digest": func(fixture *multiCarrierRoleEvidenceFixture) {
				fixture.roleInput(role).ActiveAssignment.AssignmentDigest = strings.Repeat("f", 64)
			},
			"wrong generation": func(fixture *multiCarrierRoleEvidenceFixture) {
				fixture.roleInput(role).ActiveAssignment.Generation++
			},
			"wrong subject": func(fixture *multiCarrierRoleEvidenceFixture) {
				fixture.roleInput(role).ActiveAssignment.SubjectRevision = strings.Repeat("c", 40)
			},
			"duplicate authority": func(fixture *multiCarrierRoleEvidenceFixture) {
				original := fixture.roleInput(role)
				duplicate := *original
				duplicate.Process = finalEvidenceRoleProcess(t, "PROCESS-929", role, fixture.specID)
				active := *original.ActiveAssignment
				active.ProcessID = duplicate.Process.Comment.ID
				duplicate.ActiveAssignment = &active
				if role == assignment.RoleReview {
					duplicate.Reviews = append([]gates.ReviewEvidence(nil), original.Reviews...)
					duplicate.Reviews[0].ProcessID = duplicate.Process.Comment.ID
				} else {
					duplicate.Verifications = append([]gates.VerificationEvidence(nil), original.Verifications...)
					duplicate.Verifications[0].ProcessID = duplicate.Process.Comment.ID
				}
				fixture.inputs = append(fixture.inputs, duplicate)
			},
		} {
			t.Run(string(role)+"/"+name, func(t *testing.T) {
				fixture := newMultiCarrierRoleEvidenceFixture(t)
				mutate(&fixture)
				snapshot := fixture.snapshot()
				for _, record := range snapshot.Records {
					target := false
					for _, processID := range fixture.targetProcessIDs {
						target = target || record.ProcessID == processID
					}
					if !target {
						continue
					}
					if role == assignment.RoleReview && (record.Kind == gates.FinalEvidenceReview ||
						(record.Kind == gates.FinalEvidenceTest && record.AssignmentRole == assignment.RoleReview)) {
						t.Fatalf("mismatched REVIEW authority entered target evidence: %+v", record)
					}
					if role == assignment.RoleVerification &&
						(record.Kind == gates.FinalEvidenceVerification ||
							(record.Kind == gates.FinalEvidenceTest && record.AssignmentRole == assignment.RoleVerification) ||
							record.Kind == gates.FinalEvidenceCheck) {
						t.Fatalf("mismatched VERIFY authority entered target evidence: %+v", record)
					}
				}
				if name != "wrong generation" && snapshot.Index.Passed {
					t.Fatalf("same/future/duplicate %s authority did not invalidate canonical index: %+v", role, snapshot.Index)
				}
			})
		}
	}
}

type multiCarrierRoleEvidenceFixture struct {
	t                *testing.T
	subject          string
	specID           string
	specURL          string
	reviewProcessID  string
	verifyProcessID  string
	targetProcessIDs []string
	artifacts        []model.Artifact
	inputs           []gates.ProcessEvidenceInput
}

func newMultiCarrierRoleEvidenceFixture(t *testing.T) multiCarrierRoleEvidenceFixture {
	t.Helper()
	fixture := multiCarrierRoleEvidenceFixture{t: t, subject: strings.Repeat("b", 40), specID: "SPEC-001",
		specURL: "https://example.test/issues/381#issuecomment-1", reviewProcessID: "PROCESS-913", verifyProcessID: "PROCESS-914",
		targetProcessIDs: []string{"PROCESS-911", "PROCESS-912"}}
	targets := []model.Artifact{
		finalEvidenceRoleProcess(t, fixture.targetProcessIDs[0], assignment.RoleImplementation, fixture.specID),
		finalEvidenceRoleProcess(t, fixture.targetProcessIDs[1], assignment.RoleImplementation, fixture.specID),
	}
	reviewProcess := finalEvidenceRoleProcess(t, fixture.reviewProcessID, assignment.RoleReview, fixture.specID)
	verifyProcess := finalEvidenceRoleProcess(t, fixture.verifyProcessID, assignment.RoleVerification, fixture.specID)
	for index := range targets {
		targets[index].Issue = 383
		targets[index].URL = fmt.Sprintf("https://example.test/issues/383#issuecomment-%d", 911+index)
	}
	reviewProcess.Issue, reviewProcess.URL = 383, "https://example.test/issues/383#issuecomment-913"
	verifyProcess.Issue, verifyProcess.URL = 383, "https://example.test/issues/383#issuecomment-914"

	reviewSelector := assignment.TestSelector{ID: "review-durable",
		Command: "issue-spec durable-spec check --repo o/r --proposal 381 --root . --json",
		RevisionBinding: &assignment.RevisionBinding{Source: assignment.RevisionBindingSourceSubjectRevision,
			Argument: assignment.RevisionBindingArgumentSubject}}
	reviewTest := resolvedCommandTestResult(t, reviewSelector, fixture.subject)
	reviewAssignment := testReviewAssignment(t, fixture.subject)
	reviewAssignment.ID, reviewAssignment.ProcessID = "assignment-review-shared-1", fixture.reviewProcessID
	reviewAssignment.Review.RequiredTests = []assignment.TestSelector{reviewSelector}
	reviewReceipt := testSealedReviewReceiptForAssignment(t, reviewAssignment, []assignment.TestResult{reviewTest})
	reviewBody, err := renderSubmittedReview("REVIEW-913", fixture.reviewProcessID, reviewProcess.URL,
		"https://example.test/pull/7", []string{fixture.specURL}, reviewReceipt)
	if err != nil {
		t.Fatal(err)
	}
	reviewBody += "\n\n### Covered PROCESSes\n\n- " + fixture.reviewProcessID
	for _, target := range targets {
		reviewBody += "\n- " + target.Comment.ID
	}
	reviewBody += "\n\n### Covered SPECs\n\n- " + fixture.specID + "\n"
	for _, target := range targets {
		reviewBody, _, err = model.AddRelatedCommentLink(reviewBody, target.URL)
		if err != nil {
			t.Fatal(err)
		}
	}
	review := model.Artifact{Issue: 383, URL: "https://example.test/issues/383#issuecomment-1913", Comment: model.ParseTypedComment(reviewBody)}

	tests := []assignment.TestResult{{ID: "unit", Command: "go test ./internal/commands",
		Outcome: assignment.TestPassed, Assurance: assignment.AssuranceSelfReported}}
	verifyAssignment := testVerificationAssignment(t, fixture.subject, tests, nil)
	verifyAssignment.ID, verifyAssignment.ProcessID = "assignment-verify-shared-1", fixture.verifyProcessID
	verifyReceipt := testSealedVerificationReceiptForAssignment(t, verifyAssignment, tests, nil)
	verifyBody, err := renderSubmittedVerification("VERIFY-914", verifyProcess.URL, []string{fixture.specID},
		verifyReceipt, nil, testVerificationSubmission("Verifier"))
	if err != nil {
		t.Fatal(err)
	}
	verifyBody += "\n\n### Covered PROCESSes\n\n- " + fixture.verifyProcessID
	for _, target := range targets {
		verifyBody += "\n- " + target.Comment.ID
	}
	verifyBody += "\n\n### Covered SPECs\n\n- " + fixture.specID + "\n"
	for _, target := range targets {
		verifyBody, _, err = model.AddRelatedCommentLink(verifyBody, target.URL)
		if err != nil {
			t.Fatal(err)
		}
	}
	verifyBody, _, err = model.AddRelatedCommentLink(verifyBody, fixture.specURL)
	if err != nil {
		t.Fatal(err)
	}
	verify := model.Artifact{Issue: 383, URL: "https://example.test/issues/383#issuecomment-1914", Comment: model.ParseTypedComment(verifyBody)}

	activeSpecs := map[string]string{fixture.specID: fixture.specURL}
	authors := map[string]map[string]bool{fixture.specID: {"implementation worker": true}}
	for _, target := range targets {
		fixture.inputs = append(fixture.inputs, gates.ProcessEvidenceInput{Process: target, ActiveSpecs: activeSpecs,
			AuthorAgentsBySpec: authors, ActiveAssignment: &gates.ActiveAssignmentEvidence{ProcessID: target.Comment.ID,
				AssignmentID: "assignment-" + target.Comment.ID, AssignmentDigest: strings.Repeat("a", 64), Generation: 1,
				Role: assignment.RoleImplementation},
			Reviews: []gates.ReviewEvidence{{ProcessID: target.Comment.ID, SpecID: fixture.specID, URL: review.URL,
				Done: true, ReviewerAgent: "Independent Reviewer", SubjectRevision: fixture.subject, Trusted: true}},
			Verifications: []gates.VerificationEvidence{{ProcessID: target.Comment.ID, SpecID: fixture.specID, URL: verify.URL,
				Done: true, TestEvidence: true}},
		})
	}
	reviewDigest, err := assignment.AssignmentDigest(reviewAssignment)
	if err != nil {
		t.Fatal(err)
	}
	fixture.inputs = append(fixture.inputs, gates.ProcessEvidenceInput{Process: reviewProcess, ActiveSpecs: activeSpecs,
		AuthorAgentsBySpec: authors, ActiveAssignment: &gates.ActiveAssignmentEvidence{ProcessID: fixture.reviewProcessID,
			AssignmentID: reviewAssignment.ID, AssignmentDigest: reviewDigest, Generation: 1,
			Role: assignment.RoleReview, SubjectRevision: fixture.subject, RequiredTests: []assignment.TestSelector{reviewSelector}},
		Reviews: []gates.ReviewEvidence{{ProcessID: fixture.reviewProcessID, SpecID: fixture.specID, URL: review.URL,
			Done: true, ReviewerAgent: "Independent Reviewer", SubjectRevision: fixture.subject, Trusted: true}}})
	verifyDigest, err := assignment.AssignmentDigest(verifyAssignment)
	if err != nil {
		t.Fatal(err)
	}
	fixture.inputs = append(fixture.inputs, gates.ProcessEvidenceInput{Process: verifyProcess, ActiveSpecs: activeSpecs,
		AuthorAgentsBySpec: authors, ActiveAssignment: &gates.ActiveAssignmentEvidence{ProcessID: fixture.verifyProcessID,
			AssignmentID: verifyAssignment.ID, AssignmentDigest: verifyDigest, Generation: 1,
			Role: assignment.RoleVerification, SubjectRevision: fixture.subject,
			RequiredTests: []assignment.TestSelector{{ID: "unit", Command: "go test ./internal/commands"}}},
		Verifications: []gates.VerificationEvidence{{ProcessID: fixture.verifyProcessID, SpecID: fixture.specID,
			URL: verify.URL, Done: true, TestEvidence: true}}})
	fixture.artifacts = append(fixture.artifacts, model.Artifact{Issue: 381, URL: fixture.specURL,
		Comment: model.TypedComment{Type: "SPEC", ID: fixture.specID, Status: "confirmed"}})
	fixture.artifacts = append(fixture.artifacts, targets...)
	fixture.artifacts = append(fixture.artifacts, reviewProcess, verifyProcess, review, verify)
	return fixture
}

func (fixture *multiCarrierRoleEvidenceFixture) roleInput(role assignment.Role) *gates.ProcessEvidenceInput {
	processID := fixture.verifyProcessID
	if role == assignment.RoleReview {
		processID = fixture.reviewProcessID
	}
	for index := range fixture.inputs {
		if fixture.inputs[index].Process.Comment.ID == processID {
			return &fixture.inputs[index]
		}
	}
	fixture.t.Fatalf("missing %s role input", role)
	return nil
}

func (fixture multiCarrierRoleEvidenceFixture) snapshot() gates.FinalEvidenceSnapshot {
	return buildMinimalFinalEvidence(fixture.artifacts, fixture.inputs, gates.FinalSubject{Required: true, Known: true,
		Trusted: true, Kind: "pull_request", URL: "https://example.test/pull/7", Revision: fixture.subject,
		Source: "github-pull-request-head:7"})
}

func finalEvidenceRoleProcess(t *testing.T, processID string, role assignment.Role, specID string) model.Artifact {
	t.Helper()
	class := model.ProcessExecutionChangeBearing
	if role == assignment.RoleReview {
		class = model.ProcessExecutionReview
	} else if role == assignment.RoleVerification {
		class = model.ProcessExecutionVerification
	}
	body, err := model.EnsureTypedBody("PROCESS", processID,
		"## Process: role evidence\n\n### Parent TASK\n\n- TASK-001\n\n### Execution Class\n\n- "+string(class)+
			"\n\n### Covers\n\n- "+specID+"\n\n### Handoff\n\nN/A", model.BodyOptions{Status: "done"})
	if err != nil {
		t.Fatal(err)
	}
	return model.Artifact{URL: "https://example.test/process/" + processID, Comment: model.ParseTypedComment(body)}
}

func TestVerificationReceiptBindingRejectsUntrustedLocalCompletion(t *testing.T) {
	valid := testSealedVerificationReceipt(t, []assignment.TestResult{{ID: "unit", Command: "go test ./...",
		Outcome: assignment.TestPassed, Assurance: assignment.AssuranceSelfReported}}, nil)
	sealed := testVerificationAssignment(t, valid.SubjectRevision, valid.Tests, valid.Verification.CheckSelectors)
	binding := processworkspace.AssignmentBinding{SchemaVersion: assignment.AssignmentSchemaVersion,
		AssignmentID: valid.AssignmentID, Digest: valid.AssignmentDigest, Role: assignment.RoleVerification,
		SubjectRevision: valid.SubjectRevision, Generation: valid.AssignmentGeneration}
	tests := map[string]func(*assignment.Receipt, *processworkspace.AssignmentBinding){
		"generation": func(_ *assignment.Receipt, b *processworkspace.AssignmentBinding) { b.Generation++ },
		"revision":   func(_ *assignment.Receipt, b *processworkspace.AssignmentBinding) { b.SubjectRevision = "head-old" },
		"impersonated": func(r *assignment.Receipt, _ *processworkspace.AssignmentBinding) {
			r.Provenance.Subject = "Another Verifier"
		},
		"provider-owned local result": func(r *assignment.Receipt, _ *processworkspace.AssignmentBinding) {
			r.Tests[0].Assurance = assignment.AssuranceProviderOwned
		},
		"failed local result": func(r *assignment.Receipt, _ *processworkspace.AssignmentBinding) {
			r.Tests[0].Outcome = assignment.TestFailed
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			receipt, candidateBinding := valid, binding
			receipt.Tests = append([]assignment.TestResult(nil), valid.Tests...)
			mutate(&receipt, &candidateBinding)
			if err := validateVerificationReceiptBinding(receipt, sealed, &candidateBinding,
				testVerificationSubmission("Verifier")); err == nil {
				t.Fatal("invalid verification authority was accepted")
			}
		})
	}
	coordinator := valid
	coordinator.Provenance.Writer, coordinator.Provenance.Subject = "Coordinator", "Coordinator"
	coordinator.ReceiptDigest = ""
	coordinator, err := assignment.SealReceipt(coordinator)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateVerificationReceiptBinding(coordinator, sealed, &binding,
		testVerificationSubmission("Coordinator")); err == nil ||
		!strings.Contains(err.Error(), "non-Coordinator") {
		t.Fatalf("coordinator verifier error=%v", err)
	}
}

func TestVerificationReceiptBindingUsesLogicalAgentWithoutSessionMetadata(t *testing.T) {
	receipt := testSealedVerificationReceipt(t, []assignment.TestResult{{ID: "unit", Command: "go test ./...",
		Outcome: assignment.TestPassed, Assurance: assignment.AssuranceSelfReported}}, nil)
	sealed := testVerificationAssignment(t, receipt.SubjectRevision, receipt.Tests, nil)
	binding := &processworkspace.AssignmentBinding{SchemaVersion: assignment.AssignmentSchemaVersion,
		AssignmentID: receipt.AssignmentID, Digest: receipt.AssignmentDigest, Role: assignment.RoleVerification,
		SubjectRevision: receipt.SubjectRevision, Generation: receipt.AssignmentGeneration}
	submission := testVerificationSubmission("Verifier")
	if err := validateVerificationReceiptBinding(receipt, sealed, binding, submission); err != nil {
		t.Fatalf("logical role-owned submission rejected: %v", err)
	}
	if submission.Assurance != assignment.AssuranceSelfReported {
		t.Fatalf("logical agent metadata upgraded assurance: %+v", submission)
	}
}

func TestVerificationReceiptRequiresExactAssignedTestsAndChecks(t *testing.T) {
	receipt := testSealedVerificationReceipt(t, []assignment.TestResult{{ID: "unit", Command: "go test ./internal/gates",
		Outcome: assignment.TestPassed, Assurance: assignment.AssuranceSelfReported}},
		[]assignment.CheckSelector{{Provider: "github", Name: "unit"}})
	sealed := testVerificationAssignment(t, receipt.SubjectRevision, receipt.Tests, receipt.Verification.CheckSelectors)
	binding := &processworkspace.AssignmentBinding{SchemaVersion: assignment.AssignmentSchemaVersion,
		AssignmentID: receipt.AssignmentID, Digest: receipt.AssignmentDigest, Role: assignment.RoleVerification,
		SubjectRevision: receipt.SubjectRevision, Generation: receipt.AssignmentGeneration}
	tests := map[string]func(*assignment.Receipt){
		"omitted test": func(r *assignment.Receipt) { r.Tests = nil },
		"renamed test": func(r *assignment.Receipt) { r.Tests[0].ID = "alternative" },
		"substituted command": func(r *assignment.Receipt) {
			r.Tests[0].Command = "go test ./internal/commands"
		},
		"omitted check": func(r *assignment.Receipt) { r.Verification.CheckSelectors = nil },
		"renamed check": func(r *assignment.Receipt) { r.Verification.CheckSelectors[0].Name = "alternative" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			candidate := receipt
			candidate.Tests = append([]assignment.TestResult(nil), receipt.Tests...)
			candidate.Verification = &assignment.VerificationResult{Summary: receipt.Verification.Summary,
				CheckSelectors: append([]assignment.CheckSelector(nil), receipt.Verification.CheckSelectors...)}
			mutate(&candidate)
			candidate.ReceiptDigest = ""
			candidate, err := assignment.SealReceipt(candidate)
			if err != nil {
				t.Fatal(err)
			}
			if err := validateVerificationReceiptBinding(candidate, sealed, binding,
				testVerificationSubmission("Verifier")); err == nil ||
				!strings.Contains(err.Error(), "assigned") {
				t.Fatalf("coverage substitution error=%v", err)
			}
		})
	}
}

func TestVerificationReceiptBindingTreatsDurableCheckAsOrdinaryExactEvidence(t *testing.T) {
	baseline, subject := strings.Repeat("a", 40), strings.Repeat("b", 40)
	payload := assignment.VerificationPayload{SubjectRevision: subject,
		RequiredTests: []assignment.TestSelector{{ID: "unit", Command: "go test ./..."}}}
	payload, err := payload.WithDurableCheck(durable.ModeRepository, assignment.DurableCheckBinding{
		Repository: "o/r", Proposal: 308, BaselineRevision: baseline, SubjectRevision: subject, RepositoryRoot: "."})
	if err != nil {
		t.Fatal(err)
	}
	sealed := assignment.Assignment{SchemaVersion: assignment.AssignmentSchemaVersion, ID: "assignment-durable-verification-1",
		Role: assignment.RoleVerification, Repository: "o/r", Issue: 9, ProcessID: "PROCESS-101", SubjectRevision: subject,
		Scenarios: []assignment.ScenarioRef{{SpecID: "SPEC-003", Scenario: "durable projection is an ordinary exact-revision check"}},
		Policy:    assignment.Policy{RequireExactRevision: true, MaxResultItems: 64}, ResultSchemaVersion: assignment.ReceiptSchemaVersion,
		Verification: &payload}
	if err := sealed.Validate(); err != nil {
		t.Fatal(err)
	}
	tests := make([]assignment.TestResult, 0, len(payload.RequiredTests))
	for _, selector := range payload.RequiredTests {
		if selector.RevisionBinding != nil {
			tests = append(tests, resolvedCommandTestResult(t, selector, subject))
		} else {
			tests = append(tests, assignment.TestResult{ID: selector.ID, Command: selector.Command,
				Outcome: assignment.TestPassed, Assurance: assignment.AssuranceSelfReported})
		}
	}
	receipt := testSealedVerificationReceiptForAssignment(t, sealed, tests, nil)
	binding := &processworkspace.AssignmentBinding{SchemaVersion: assignment.AssignmentSchemaVersion,
		AssignmentID: receipt.AssignmentID, Digest: receipt.AssignmentDigest, Role: assignment.RoleVerification,
		SubjectRevision: subject, Generation: receipt.AssignmentGeneration}
	if err := validateVerificationReceiptBinding(receipt, sealed, binding, testVerificationSubmission("Verifier")); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		mutate func(*assignment.Receipt)
	}{
		{name: "missing", mutate: func(value *assignment.Receipt) { value.Tests = value.Tests[1:] }},
		{name: "failed", mutate: func(value *assignment.Receipt) { value.Tests[0].Outcome = assignment.TestFailed }},
		{name: "stale", mutate: func(value *assignment.Receipt) { value.SubjectRevision = baseline }},
		{name: "mismatched", mutate: func(value *assignment.Receipt) { value.Tests[0].Command += " --forged" }},
		{name: "forged prose", mutate: func(value *assignment.Receipt) {
			value.Tests = value.Tests[1:]
			value.Verification.Summary = "durable check passed according to prose"
		}},
		{name: "forged assurance", mutate: func(value *assignment.Receipt) {
			value.Tests[0].Assurance = assignment.AssuranceRuntimeAttested
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			candidate := receipt
			candidate.Tests = append([]assignment.TestResult(nil), receipt.Tests...)
			verification := *receipt.Verification
			candidate.Verification = &verification
			test.mutate(&candidate)
			candidate.ReceiptDigest = ""
			candidate, sealErr := assignment.SealReceipt(candidate)
			if sealErr != nil {
				return
			}
			if err := validateVerificationReceiptBinding(candidate, sealed, binding,
				testVerificationSubmission("Verifier")); err == nil {
				t.Fatal("invalid durable evidence was accepted")
			}
		})
	}
}

func TestPublishAcceptedVerificationIsAppendOnlyUnderConcurrentReceipt(t *testing.T) {
	receipt := testSealedVerificationReceipt(t, []assignment.TestResult{{ID: "unit", Command: "go test ./internal/gates",
		Outcome: assignment.TestPassed, Assurance: assignment.AssuranceSelfReported}}, nil)
	body, err := renderSubmittedVerification("VERIFY-101", "https://github.com/o/r/issues/9#issuecomment-10",
		[]string{"SPEC-005"}, receipt, nil, testVerificationSubmission("Verifier"))
	if err != nil {
		t.Fatal(err)
	}
	other := receipt
	other.ID = "receipt-verification-concurrent"
	other.ReceiptDigest = ""
	other, err = assignment.SealReceipt(other)
	if err != nil {
		t.Fatal(err)
	}
	otherBody, err := renderSubmittedVerification("VERIFY-102", "https://github.com/o/r/issues/9#issuecomment-10",
		[]string{"SPEC-005"}, other, nil, testVerificationSubmission("Verifier"))
	if err != nil {
		t.Fatal(err)
	}
	comments := []github.Comment{}
	creates := 0
	backend := fakeGitHubBackend{listIssueComments: func(context.Context, string, int) ([]github.Comment, error) {
		return append([]github.Comment(nil), comments...), nil
	}, createComment: func(_ context.Context, _ string, _ int, submitted string) (github.Comment, error) {
		creates++
		created := github.Comment{ID: 11, Body: submitted}
		comments = append(comments, created, github.Comment{ID: 12, Body: otherBody})
		return created, nil
	}}
	if err := validateExistingVerificationReceipt(nil, "VERIFY-101", receipt); err != nil {
		t.Fatal(err)
	}
	comments = []github.Comment{{ID: 12, Body: otherBody}}
	if _, _, err := publishAcceptedVerification(t.Context(), backend, "o/r", 9, "VERIFY-101", body, receipt); err == nil ||
		!strings.Contains(err.Error(), "different receipt") || creates != 0 {
		t.Fatalf("fresh competing observation creates=%d err=%v", creates, err)
	}
	comments = nil
	if _, _, err := publishAcceptedVerification(t.Context(), backend, "o/r", 9, "VERIFY-101", body, receipt); err == nil ||
		!strings.Contains(err.Error(), "conflicted") || creates != 1 {
		t.Fatalf("concurrent create creates=%d err=%v", creates, err)
	}
	comments = []github.Comment{{ID: 11, Body: body}}
	creates = 0
	action, existing, err := publishAcceptedVerification(t.Context(), backend, "o/r", 9, "VERIFY-101", body, receipt)
	if err != nil || action != "unchanged" || existing.ID != 11 || creates != 0 {
		t.Fatalf("exact replay action=%q existing=%+v creates=%d err=%v", action, existing, creates, err)
	}
}

func TestAcceptedVerificationReceiptReadsPreUpgradeSessionFields(t *testing.T) {
	receipt := testSealedVerificationReceipt(t, []assignment.TestResult{{ID: "unit", Command: "go test ./internal/gates",
		Outcome: assignment.TestPassed, Assurance: assignment.AssuranceSelfReported}}, nil)
	body, err := renderSubmittedVerification("VERIFY-101", "https://github.com/o/r/issues/9#issuecomment-10",
		[]string{"SPEC-005"}, receipt, nil, testVerificationSubmission("Verifier"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, "agent_session_") {
		t.Fatalf("new VERIFY contains deprecated session metadata:\n%s", body)
	}
	legacyBody := strings.Replace(body, `"submission":{"agent":"Verifier","assurance":"self-reported"}`,
		`"submission":{"agent":"Verifier","agent_session_id":"verifier-session","agent_session_source":"CODEX_THREAD_ID","assurance":"self-reported"}`, 1)
	if legacyBody == body {
		t.Fatal("failed to construct pre-upgrade VERIFY fixture")
	}
	authority, found, err := parseAcceptedVerificationReceipt(legacyBody)
	if err != nil || !found || authority.Submission == nil || authority.Submission.AgentSessionID != "verifier-session" ||
		authority.Submission.AgentSessionSource != "CODEX_THREAD_ID" {
		t.Fatalf("pre-upgrade VERIFY authority=%+v found=%v err=%v", authority, found, err)
	}
	artifact := model.Artifact{Comment: model.ParseTypedComment(legacyBody)}
	if source, trusted, _, ok := exactAcceptedVerificationCarrier(artifact, receipt.SubjectRevision); !ok || !trusted ||
		source != "accepted-verification-receipt:self-reported-tests" {
		t.Fatalf("pre-upgrade VERIFY carrier source=%q trusted=%v ok=%v", source, trusted, ok)
	}
}

func TestObserveGitHubVerificationChecksRequiresStableExactSnapshot(t *testing.T) {
	receipt := testSealedVerificationReceipt(t, nil,
		[]assignment.CheckSelector{{Provider: "github", Name: "unit"}})
	head := receipt.SubjectRevision
	prReads := 0
	backend := verifySubmitCommandBackend{fakeGitHubBackend: fakeGitHubBackend{
		getPullRequest: func(context.Context, string, int) (github.PullRequest, error) {
			prReads++
			pr := github.PullRequest{Number: 7}
			pr.Head.SHA = head
			if prReads == 2 {
				pr.Head.SHA = strings.Repeat("c", 40)
			}
			return pr, nil
		}}, checkRuns: []github.CheckRun{{ID: 42, Name: "unit", HeadSHA: head, Status: "completed", Conclusion: "success"}}}
	if _, err := observeGitHubVerificationChecks(t.Context(), backend, "o/r", 7, receipt); err == nil ||
		!strings.Contains(err.Error(), "changed while observing") {
		t.Fatalf("unstable snapshot error=%v", err)
	}
	prReads = 0
	backend.fakeGitHubBackend.getPullRequest = func(context.Context, string, int) (github.PullRequest, error) {
		prReads++
		pr := github.PullRequest{Number: 7}
		pr.Head.SHA = head
		return pr, nil
	}
	checks, err := observeGitHubVerificationChecks(t.Context(), backend, "o/r", 7, receipt)
	if err != nil || len(checks) != 1 || checks[0].SubjectRevision != head || checks[0].Source != "github-check-run:42" {
		t.Fatalf("checks=%+v err=%v", checks, err)
	}
}

func TestObserveNativeVerificationChecksUsesTrustedExactRevision(t *testing.T) {
	now := time.Now().UTC()
	external := externalGateResult{Target: coreevidence.NativeTarget{Reference: codereview.Reference{ProviderKey: "code.example"},
		SubjectRevision: "head-abc"}, Snapshot: codereview.Snapshot{Records: []codereview.EvidenceRecord{
		{ID: "stale", Kind: codereview.EvidenceCheck, Name: "unit", State: "passed", SubjectRevision: "head-old",
			Trusted: true, ObservedAt: now.Add(time.Minute)},
		{ID: "current", Kind: codereview.EvidenceCheck, Name: "unit", State: "passed", SubjectRevision: "head-abc",
			Trusted: true, ObservedAt: now},
	}}}
	checks, err := observeNativeVerificationChecks([]assignment.CheckSelector{{Provider: "code.example", Name: "unit"}}, external)
	if err != nil || len(checks) != 1 || checks[0].EvidenceID != "current" || checks[0].Source != "native-evidence:current" {
		t.Fatalf("checks=%+v err=%v", checks, err)
	}
	external.Snapshot.Records[1].Trusted = false
	if _, err := observeNativeVerificationChecks([]assignment.CheckSelector{{Provider: "code.example", Name: "unit"}}, external); err == nil {
		t.Fatal("untrusted native check was accepted")
	}
}

func TestRunVerifySubmitProjectsStructuredEvidenceAndRecoversRetry(t *testing.T) {
	receipt := testSealedVerificationReceipt(t, []assignment.TestResult{{ID: "unit", Command: "go test ./internal/gates",
		Outcome: assignment.TestPassed, Assurance: assignment.AssuranceSelfReported}},
		[]assignment.CheckSelector{{Provider: "github", Name: "unit"}})
	sealedAssignment := testVerificationAssignment(t, receipt.SubjectRevision, receipt.Tests, receipt.Verification.CheckSelectors)
	binding := &processworkspace.AssignmentBinding{SchemaVersion: assignment.AssignmentSchemaVersion,
		AssignmentID: receipt.AssignmentID, Digest: receipt.AssignmentDigest, Role: assignment.RoleVerification,
		SubjectRevision: receipt.SubjectRevision, Generation: receipt.AssignmentGeneration}
	now := time.Date(2026, 7, 19, 1, 0, 0, 0, time.UTC)
	workspace := model.ProcessWorkspace{SchemaVersion: processworkspace.LeaseSchemaVersion,
		WorkspaceID: "verification-process-101", Repository: "o/r", ProcessID: "PROCESS-101",
		ExecutionClass: processworkspace.ExecutionVerification, Mode: processworkspace.ModeSnapshot,
		BaseSHA: receipt.SubjectRevision, DetachedRevision: receipt.SubjectRevision,
		RuntimeNamespace: "verification-process-101", Assignment: binding, State: processworkspace.StatePrepared,
		CreatedAt: now, UpdatedAt: now}
	processBody, err := templates.ProcessComment(templates.ProcessCommentOptions{
		Common: templates.CommonOptions{ID: "PROCESS-101", Status: "in-progress"}, Input: templates.ProcessInput{
			Title: "verify exact receipt", Owner: "Verifier", ParentTask: "TASK-006",
			ExecutionClass: model.ProcessExecutionVerification, WorkspaceManagement: model.ProcessWorkspaceManaged,
			Workspace: &workspace, Covers: []string{"SPEC-005"}, Handoff: "N/A"}})
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(receipt)
	resultPath := filepath.Join(t.TempDir(), "verification-receipt.json")
	if err := os.WriteFile(resultPath, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	assignmentPayload, _ := json.Marshal(sealedAssignment)
	assignmentPath := filepath.Join(t.TempDir(), "verification-assignment.json")
	if err := os.WriteFile(assignmentPath, assignmentPayload, 0o600); err != nil {
		t.Fatal(err)
	}
	specBody := verificationCoverageSpecBody(t, "SPEC-005")
	comments := []github.Comment{{ID: 10, HTMLURL: "https://github.com/o/r/issues/9#issuecomment-10", Body: processBody}}
	created, updated := 0, 0
	backend := verifySubmitCommandBackend{checkRuns: []github.CheckRun{{ID: 42, Name: "unit",
		HeadSHA: receipt.SubjectRevision, Status: "completed", Conclusion: "success"}}}
	backend.fakeGitHubBackend = fakeGitHubBackend{info: github.BackendInfo{Name: "gh", Kind: "external-cli", Host: "github.com"},
		getIssue: verificationCoverageIssue,
		listIssueComments: func(_ context.Context, _ string, issue int) ([]github.Comment, error) {
			if issue == 1 {
				return []github.Comment{{ID: 1, HTMLURL: "https://github.com/o/r/issues/1#issuecomment-1", Body: specBody}}, nil
			}
			if issue == 9 {
				return append([]github.Comment(nil), comments...), nil
			}
			return nil, nil
		}, getPullRequest: func(context.Context, string, int) (github.PullRequest, error) {
			pr := github.PullRequest{Number: 7}
			pr.Head.SHA = receipt.SubjectRevision
			return pr, nil
		}, createComment: func(_ context.Context, _ string, _ int, body string) (github.Comment, error) {
			created++
			comment := github.Comment{ID: 11, HTMLURL: "https://github.com/o/r/issues/9#issuecomment-11", Body: body}
			comments = append(comments, comment)
			return comment, nil
		}, updateComment: func(_ context.Context, _ string, id int64, body string) (github.Comment, error) {
			updated++
			for index := range comments {
				if comments[index].ID == id {
					comments[index].Body = body
					return comments[index], nil
				}
			}
			return github.Comment{}, errors.New("missing comment")
		}}
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	app.selectGitHubBackend = ghSelection
	app.newGitHubBackend = func(context.Context, auth.GitHubBackendSelection) (github.Backend, error) { return backend, nil }
	args := []string{"submit", "--repo", "o/r", "--implement", "9", "--pr", "7", "--process", "PROCESS-101",
		"--id", "VERIFY-101", "--result-file", resultPath, "--assignment-file", assignmentPath,
		"--agent", "Verifier"}
	for run := 0; run < 2; run++ {
		out.Reset()
		errOut.Reset()
		if code := app.runVerify(t.Context(), args); code != 0 {
			t.Fatalf("run=%d exit=%d out=%q err=%q", run, code, out.String(), errOut.String())
		}
	}
	if created != 1 || updated != 0 || len(comments) != 2 {
		t.Fatalf("created=%d updated=%d comments=%d", created, updated, len(comments))
	}
	parsed := model.ParseTypedComment(comments[1].Body)
	authority, found, err := parseAcceptedVerificationReceipt(comments[1].Body)
	if err != nil || !found || parsed.Agent != "Verifier" || parsed.SubjectRevision != receipt.SubjectRevision ||
		parsed.Status != "done" || len(authority.Tests) != 1 || len(authority.Checks) != 1 ||
		authority.Submission == nil || authority.Submission.Agent != "Verifier" ||
		authority.Submission.Assurance != assignment.AssuranceSelfReported ||
		!linksContainURL(parsed.Links["Related Comments"], "https://github.com/o/r/issues/1#issuecomment-1") ||
		!linksContainURL(parsed.Links["Related Comments"], "https://github.com/o/r/issues/9#issuecomment-10") ||
		!strings.Contains(comments[1].Body, "### Local Tests") || !strings.Contains(comments[1].Body, "### Provider Checks") ||
		strings.Contains(comments[1].Body, "### Evidence") {
		t.Fatalf("VERIFY=%+v authority=%+v found=%t err=%v body=%s", parsed, authority, found, err, comments[1].Body)
	}
}

func TestRunVerifySubmitLocalTestReceiptFeedsExactFinalEvidence(t *testing.T) {
	receipt := testSealedVerificationReceipt(t, []assignment.TestResult{{ID: "unit", Command: "go test ./internal/gates",
		Outcome: assignment.TestPassed, Assurance: assignment.AssuranceSelfReported}}, nil)
	sealedAssignment := testVerificationAssignment(t, receipt.SubjectRevision, receipt.Tests, nil)
	binding := &processworkspace.AssignmentBinding{SchemaVersion: assignment.AssignmentSchemaVersion,
		AssignmentID: receipt.AssignmentID, Digest: receipt.AssignmentDigest, Role: assignment.RoleVerification,
		SubjectRevision: receipt.SubjectRevision, Generation: receipt.AssignmentGeneration}
	now := time.Date(2026, 7, 19, 2, 0, 0, 0, time.UTC)
	workspace := model.ProcessWorkspace{SchemaVersion: processworkspace.LeaseSchemaVersion,
		WorkspaceID: "verification-process-101", Repository: "o/r", ProcessID: "PROCESS-101",
		ExecutionClass: processworkspace.ExecutionVerification, Mode: processworkspace.ModeSnapshot,
		BaseSHA: receipt.SubjectRevision, DetachedRevision: receipt.SubjectRevision,
		RuntimeNamespace: "verification-process-101", Assignment: binding, State: processworkspace.StatePrepared,
		CreatedAt: now, UpdatedAt: now}
	processBody, err := templates.ProcessComment(templates.ProcessCommentOptions{
		Common: templates.CommonOptions{ID: "PROCESS-101", Status: "in-progress"}, Input: templates.ProcessInput{
			Title: "verify exact local receipt", Owner: "Verifier", ParentTask: "TASK-006",
			ExecutionClass: model.ProcessExecutionVerification, WorkspaceManagement: model.ProcessWorkspaceManaged,
			Workspace: &workspace, Covers: []string{"SPEC-005"}, Handoff: "N/A"}})
	if err != nil {
		t.Fatal(err)
	}
	processBody, err = model.StampTypedSessionMetadata(processBody, "coordinator-session", "CODEX_THREAD_ID")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	resultPath, assignmentPath := filepath.Join(dir, "receipt.json"), filepath.Join(dir, "assignment.json")
	payload, _ := json.Marshal(receipt)
	if err := os.WriteFile(resultPath, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	payload, _ = json.Marshal(sealedAssignment)
	if err := os.WriteFile(assignmentPath, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	coordinator := receipt
	coordinator.Provenance.Writer, coordinator.Provenance.Subject = "Coordinator", "Coordinator"
	coordinator.ReceiptDigest = ""
	coordinator, err = assignment.SealReceipt(coordinator)
	if err != nil {
		t.Fatal(err)
	}
	coordinatorPath := filepath.Join(dir, "coordinator-receipt.json")
	payload, _ = json.Marshal(coordinator)
	if err := os.WriteFile(coordinatorPath, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	specBody := verificationCoverageSpecBody(t, "SPEC-005")
	comments := []github.Comment{{ID: 10, HTMLURL: "https://github.com/o/r/issues/9#issuecomment-10", Body: processBody}}
	providerReads, creates := 0, 0
	backend := verifySubmitCommandBackend{fakeGitHubBackend: fakeGitHubBackend{
		info:     github.BackendInfo{Name: "gh", Kind: "external-cli", Host: "github.com"},
		getIssue: verificationCoverageIssue,
		listIssueComments: func(_ context.Context, _ string, issue int) ([]github.Comment, error) {
			if issue == 1 {
				return []github.Comment{{ID: 1, HTMLURL: "https://github.com/o/r/issues/1#issuecomment-1", Body: specBody}}, nil
			}
			if issue == 9 {
				return append([]github.Comment(nil), comments...), nil
			}
			return nil, nil
		},
		getPullRequest: func(context.Context, string, int) (github.PullRequest, error) {
			providerReads++
			pr := github.PullRequest{Number: 7, HTMLURL: "https://github.com/o/r/pull/7"}
			pr.Head.SHA = receipt.SubjectRevision
			return pr, nil
		},
		createComment: func(_ context.Context, _ string, _ int, body string) (github.Comment, error) {
			creates++
			comment := github.Comment{ID: 11, HTMLURL: "https://github.com/o/r/issues/9#issuecomment-11", Body: body}
			comments = append(comments, comment)
			return comment, nil
		}}, checkRuns: nil}
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	app.selectGitHubBackend = ghSelection
	app.newGitHubBackend = func(context.Context, auth.GitHubBackendSelection) (github.Backend, error) { return backend, nil }
	coordinatorArgs := []string{"submit", "--repo", "o/r", "--implement", "9", "--pr", "7", "--process", "PROCESS-101",
		"--id", "VERIFY-COORDINATOR", "--result-file", coordinatorPath, "--assignment-file", assignmentPath,
		"--agent", "Coordinator"}
	if code := app.runVerify(t.Context(), coordinatorArgs); code != 1 || providerReads != 0 || creates != 0 ||
		len(comments) != 1 || !strings.Contains(errOut.String(), "non-Coordinator") {
		t.Fatalf("coordinator exit=%d providerReads=%d creates=%d comments=%d err=%q",
			code, providerReads, creates, len(comments), errOut.String())
	}
	out.Reset()
	errOut.Reset()
	args := []string{"submit", "--repo", "o/r", "--implement", "9", "--pr", "7", "--process", "PROCESS-101",
		"--id", "VERIFY-101", "--result-file", resultPath, "--assignment-file", assignmentPath,
		"--agent", "Verifier"}
	legacyImport := append(append([]string(nil), args...), "--owner-token", "coordinator-owner-token")
	if code := app.runVerify(t.Context(), legacyImport); code != 2 || providerReads != 0 || creates != 0 || len(comments) != 1 ||
		!strings.Contains(errOut.String(), "flag provided but not defined") {
		t.Fatalf("legacy import exit=%d providerReads=%d creates=%d comments=%d err=%q",
			code, providerReads, creates, len(comments), errOut.String())
	}
	out.Reset()
	errOut.Reset()
	directArgs := append(append([]string(nil), args...), "--agent-session", "verifier-session")
	if code := app.runVerify(t.Context(), directArgs); code != 0 || len(comments) != 2 || providerReads != 2 || creates != 1 {
		t.Fatalf("submit exit=%d comments=%d providerReads=%d creates=%d out=%q err=%q",
			code, len(comments), providerReads, creates, out.String(), errOut.String())
	}
	spec := typedArtifact(t, 1, "SPEC", "SPEC-005", "confirmed", "## Requirement: X\n\nX MUST work.\n\n### Scenario: ok\n\n- **WHEN** x\n- **THEN** y")
	spec.URL = "https://github.com/o/r/issues/1#issuecomment-1"
	task := typedArtifact(t, 2, "TASK", "TASK-006", "done", strings.ReplaceAll(canonicalTaskContent, "TASK-001", "TASK-006"))
	task.Comment = model.ParseTypedComment(strings.ReplaceAll(task.Comment.Body, "SPEC-001", "SPEC-005"))
	task.URL = "https://github.com/o/r/issues/2#issuecomment-2"
	finalProcess := typedArtifact(t, 9, "PROCESS", "PROCESS-101", "done", "## Process: verify\n\n### Parent TASK\n\n- TASK-006\n\n### Execution Class\n\n- verification\n\n### Covers\n\n- SPEC-005\n\n### Handoff\n\nN/A")
	finalProcess.URL = comments[0].HTMLURL
	linkArtifacts(t, &spec, &task)
	linkArtifacts(t, &task, &finalProcess)
	linkedProcess, _, err := model.AddPRLink(finalProcess.Comment.Body, "https://github.com/o/r/pull/7")
	if err != nil {
		t.Fatal(err)
	}
	finalProcess.Comment = model.ParseTypedComment(linkedProcess)
	verify := model.Artifact{Issue: 9, CommentID: comments[1].ID, URL: comments[1].HTMLURL,
		Comment: model.ParseTypedComment(comments[1].Body)}
	canonicalizeVerificationFixture(t, &verify, finalProcess, spec)
	authority, found, err := parseAcceptedVerificationReceipt(comments[1].Body)
	if err != nil || !found || authority.Submission == nil || authority.Submission.Agent != "Verifier" ||
		authority.Submission.Assurance != assignment.AssuranceSelfReported ||
		verify.Comment.AgentSessionID != "" || verify.Comment.AgentSessionSource != "" {
		t.Fatalf("persisted verification submission=%+v found=%t err=%v typed=%+v", authority.Submission, found, err, verify.Comment)
	}
	report, err := buildFinalVerifyReport([]model.Artifact{spec, task, finalProcess, verify}, spec.URL,
		finalVerifyOptions{PR: 7, PRURL: "https://github.com/o/r/pull/7", ExpectedRevision: receipt.SubjectRevision, RationaleRequired: true})
	if err != nil {
		t.Fatal(err)
	}
	if !report.OK || len(report.ProcessEvidence) != 1 || !report.ProcessEvidence[0].CarrierRevision.Trusted ||
		report.ProcessEvidence[0].CarrierRevision.Source != "accepted-verification-receipt:self-reported-tests" {
		t.Fatalf("submit-to-final local receipt errors=%v evidence=%+v", report.Errors, report.ProcessEvidence)
	}
}

func verificationCoverageSpecBody(t *testing.T, id string) string {
	t.Helper()
	body, err := templates.SpecComment(templates.SpecCommentOptions{Common: templates.CommonOptions{ID: id, Status: "confirmed"},
		Input: templates.SpecInput{Requirement: templates.SpecRequirementInput{Title: "verification coverage",
			Text: "Verification publication MUST include canonical SPEC coverage."}, Scenarios: []templates.SpecScenarioInput{{
			Title: "complete owner", When: "verification completes", Then: "the owner includes the SPEC"}}}})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func verificationCoverageIssue(_ context.Context, _ string, issue int) (github.Issue, error) {
	items := map[int]github.Issue{
		1: {Number: 1, HTMLURL: "https://github.com/o/r/issues/1", Body: "<!-- issue-spec:issue=proposal change=verify-coverage version=1 -->"},
		2: {Number: 2, HTMLURL: "https://github.com/o/r/issues/2", Body: "<!-- issue-spec:issue=design change=verify-coverage version=1 -->\n- Proposal Issue: 1"},
		9: {Number: 9, HTMLURL: "https://github.com/o/r/issues/9", Body: "<!-- issue-spec:issue=implement change=verify-coverage version=1 -->\n- Design Issue: 2"},
	}
	item, ok := items[issue]
	if !ok {
		return github.Issue{}, errors.New("unexpected issue")
	}
	return item, nil
}

func TestBuildFinalVerifyReportConsumesExactAcceptedLocalTestReceipt(t *testing.T) {
	const current = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	spec := typedArtifact(t, 1, "SPEC", "SPEC-001", "confirmed", "## Requirement: X\n\nX MUST work.\n\n### Scenario: ok\n\n- **WHEN** x\n- **THEN** y")
	spec.URL = "https://github.com/o/r/issues/1#issuecomment-1"
	task := typedArtifact(t, 2, "TASK", "TASK-001", "done", canonicalTaskContent)
	task.URL = "https://github.com/o/r/issues/2#issuecomment-2"
	process := typedArtifact(t, 3, "PROCESS", "PROCESS-001", "done", "## Process: verify\n\n### Parent TASK\n\n- TASK-001\n\n### Execution Class\n\n- verification\n\n### Covers\n\n- SPEC-001\n\n### Handoff\n\nN/A")
	process.URL = "https://github.com/o/r/issues/3#issuecomment-3"
	linkArtifacts(t, &spec, &task)
	linkArtifacts(t, &task, &process)
	processBody, _, err := model.AddPRLink(process.Comment.Body, "https://github.com/o/r/pull/7")
	if err != nil {
		t.Fatal(err)
	}
	process.Comment = model.ParseTypedComment(processBody)
	receipt := testSealedVerificationReceipt(t, []assignment.TestResult{{ID: "unit", Command: "go test ./internal/gates",
		Outcome: assignment.TestPassed, Assurance: assignment.AssuranceSelfReported}}, nil)
	verifyBody, err := renderSubmittedVerification("VERIFY-001", process.URL, []string{"SPEC-001"}, receipt, nil,
		testVerificationSubmission("Verifier"))
	if err != nil {
		t.Fatal(err)
	}
	verify := model.Artifact{Issue: 3, CommentID: 4, URL: "https://github.com/o/r/issues/3#issuecomment-4",
		Comment: model.ParseTypedComment(verifyBody)}
	canonicalizeVerificationFixture(t, &verify, process, spec)
	report, err := buildFinalVerifyReport([]model.Artifact{spec, task, process, verify}, "https://github.com/o/r/issues/1",
		finalVerifyOptions{PR: 7, PRURL: "https://github.com/o/r/pull/7", ExpectedRevision: current, RationaleRequired: true})
	if err != nil {
		t.Fatal(err)
	}
	if !report.OK || len(report.ProcessEvidence) != 1 || !report.ProcessEvidence[0].CarrierRevision.Trusted ||
		report.ProcessEvidence[0].CarrierRevision.Revision != current ||
		report.ProcessEvidence[0].CarrierRevision.Source != "accepted-verification-receipt:self-reported-tests" {
		t.Fatalf("accepted local-test receipt final evidence errors=%v evidence=%+v", report.Errors, report.ProcessEvidence)
	}
	tampered := strings.Replace(verifyBody, `"assurance":"self-reported"`, `"assurance":"provider-owned"`, 1)
	if _, _, _, ok := exactAcceptedVerificationCarrier(model.Artifact{Comment: model.ParseTypedComment(tampered)}, current); ok {
		t.Fatal("self-reported local test was upgraded to provider-owned final evidence")
	}
}

type verifySubmitCommandBackend struct {
	fakeGitHubBackend
	checkRuns []github.CheckRun
}

func testVerificationSubmission(agent string) processworkspace.RoleOwnedSubmissionEvidence {
	return processworkspace.RoleOwnedSubmissionEvidence{Agent: agent, Assurance: assignment.AssuranceSelfReported}
}

func (b verifySubmitCommandBackend) ListCheckRuns(context.Context, string, string) ([]github.CheckRun, error) {
	return append([]github.CheckRun(nil), b.checkRuns...), nil
}

func testSealedVerificationReceipt(t *testing.T, tests []assignment.TestResult,
	selectors []assignment.CheckSelector) assignment.Receipt {
	t.Helper()
	sealedAssignment := testVerificationAssignment(t, strings.Repeat("b", 40), tests, selectors)
	return testSealedVerificationReceiptForAssignment(t, sealedAssignment, tests, selectors)
}

func testSealedVerificationReceiptForAssignment(t *testing.T, sealedAssignment assignment.Assignment,
	tests []assignment.TestResult, selectors []assignment.CheckSelector) assignment.Receipt {
	return testSealedVerificationReceiptForAssignmentGeneration(t, sealedAssignment, 1, tests, selectors)
}

func testSealedVerificationReceiptForAssignmentGeneration(t *testing.T, sealedAssignment assignment.Assignment,
	generation uint64, tests []assignment.TestResult, selectors []assignment.CheckSelector) assignment.Receipt {
	t.Helper()
	digest, err := assignment.AssignmentDigest(sealedAssignment)
	if err != nil {
		t.Fatal(err)
	}
	value := assignment.Receipt{SchemaVersion: assignment.ReceiptSchemaVersion, ID: "receipt-verification-1",
		AssignmentID: sealedAssignment.ID, AssignmentDigest: digest, AssignmentGeneration: generation,
		Role: assignment.RoleVerification, ResultSchemaVersion: assignment.ReceiptSchemaVersion,
		SubjectRevision: sealedAssignment.SubjectRevision, Tests: tests,
		Provenance: assignment.Provenance{Route: assignment.RouteRoleOwned, Assurance: assignment.AssuranceSelfReported,
			Writer: "Verifier", Subject: "Verifier", Source: "verify-submit"},
		Verification: &assignment.VerificationResult{Summary: "Exact revision verified.", CheckSelectors: selectors}}
	sealed, err := assignment.SealReceipt(value)
	if err != nil {
		t.Fatal(err)
	}
	return sealed
}

func testVerificationAssignment(t *testing.T, subject string, tests []assignment.TestResult,
	selectors []assignment.CheckSelector) assignment.Assignment {
	t.Helper()
	requiredTests := make([]assignment.TestSelector, 0, len(tests))
	for _, test := range tests {
		if test.AssignedSelector != nil {
			requiredTests = append(requiredTests, *test.AssignedSelector)
		} else {
			requiredTests = append(requiredTests, assignment.TestSelector{ID: test.ID, Command: test.Command})
		}
	}
	value := assignment.Assignment{SchemaVersion: assignment.AssignmentSchemaVersion, ID: "assignment-verification-1",
		Role: assignment.RoleVerification, Repository: "o/r", Issue: 9, ProcessID: "PROCESS-101", SubjectRevision: subject,
		Scenarios:           []assignment.ScenarioRef{{SpecID: "SPEC-005", Scenario: "exact verification"}},
		Policy:              assignment.Policy{RequireExactRevision: true, MaxResultItems: 64},
		ResultSchemaVersion: assignment.ReceiptSchemaVersion,
		Verification: &assignment.VerificationPayload{SubjectRevision: subject, RequiredTests: requiredTests,
			RequiredChecks: append([]assignment.CheckSelector(nil), selectors...)}}
	if err := value.Validate(); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestRunVerifySummaryRequiresJSON(t *testing.T) {
	var errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &bytes.Buffer{}, &errOut)
	if code := app.runVerify(t.Context(), []string{"--summary"}); code != 2 || !strings.Contains(errOut.String(), "--summary requires --json") {
		t.Fatalf("verify exit=%d stderr=%q", code, errOut.String())
	}
}

func TestRunVerifySummaryIsAdditiveAndExitEquivalent(t *testing.T) {
	for _, test := range []struct {
		name         string
		processClass model.ProcessExecutionClass
		wantCode     int
	}{
		{name: "success", processClass: model.ProcessExecutionVerification, wantCode: 0},
		{name: "blocked", processClass: model.ProcessExecutionExternal, wantCode: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			args := []string{"--repo", "o/r", "--proposal", "1", "--design", "2", "--implement", "3", "--json"}
			process := canonicalProcessContentWithClass(test.processClass)
			fullApp, fullOut, fullErr := newGitHubVerifyWithoutPRApp(t, process)
			fullCode := fullApp.runVerify(t.Context(), args)
			if fullErr.Len() != 0 {
				t.Fatalf("full verify stderr=%q", fullErr.String())
			}
			var full finalVerifyReport
			if err := json.Unmarshal(fullOut.Bytes(), &full); err != nil {
				t.Fatalf("decode full verify: %v\n%s", err, fullOut.String())
			}
			if strings.Contains(fullOut.String(), `"schema_version"`) || !strings.Contains(fullOut.String(), `"traceability"`) {
				t.Fatalf("existing full JSON contract changed: %s", fullOut.String())
			}

			compactApp, compactOut, compactErr := newGitHubVerifyWithoutPRApp(t, process)
			compactCode := compactApp.runVerify(t.Context(), append(append([]string(nil), args...), "--summary"))
			if compactErr.Len() != 0 {
				t.Fatalf("compact verify stderr=%q", compactErr.String())
			}
			var compact gates.CompactSummary
			if err := json.Unmarshal(compactOut.Bytes(), &compact); err != nil {
				t.Fatalf("decode compact verify: %v\n%s", err, compactOut.String())
			}
			if fullCode != test.wantCode || compactCode != fullCode || compact.OK != full.OK || compact.OK != full.Gate.Ready ||
				compact.Gate.Target != full.Gate.Target || compact.Gate.Mode != full.Gate.Mode {
				t.Fatalf("decision drift: fullCode=%d compactCode=%d full=%+v compact=%+v", fullCode, compactCode, full.Gate, compact.Gate)
			}
			if compact.SchemaVersion != 1 || compact.Counts["PROCESS"]["done"] != 1 {
				t.Fatalf("compact routing data missing: %+v", compact)
			}
			if test.wantCode == 0 && len(compact.Blockers) != 0 {
				t.Fatalf("successful summary contains blockers: %+v", compact.Blockers)
			}
			for _, blocker := range compact.Blockers {
				if blocker.Detail.CommandFamily != "verify" || containsArgument(blocker.Detail.Arguments, "--summary") ||
					!containsArgument(blocker.Detail.Arguments, "--json") {
					t.Fatalf("invalid structured detail action: %+v", blocker.Detail)
				}
			}
			for _, forbidden := range []string{`"errors"`, `"traceability"`, `"process_evidence"`, `"external_evidence"`, `"evaluation_digest"`} {
				if strings.Contains(compactOut.String(), forbidden) {
					t.Fatalf("compact verify contains %s: %s", forbidden, compactOut.String())
				}
			}
		})
	}
}

func TestRunVerifySummaryCarriesExternalSubjectIdentity(t *testing.T) {
	app, out, _, updates := newSelfHostedVerifyAppAtRevision(t, "head-abc")
	code := app.runVerify(t.Context(), []string{
		"--repo", "acme/widgets", "--proposal", "1", "--design", "2", "--implement", "3", "--json", "--summary",
	})
	if code != 1 {
		t.Fatalf("verify exit=%d stdout=%q", code, out.String())
	}
	var summary gates.CompactSummary
	if err := json.Unmarshal(out.Bytes(), &summary); err != nil {
		t.Fatalf("decode compact verify: %v\n%s", err, out.String())
	}
	if summary.Subject == nil || summary.Subject.Revision != "head-abc" || summary.Subject.Evidence == nil ||
		summary.Subject.Evidence.Kind != "code_change" || summary.Subject.Evidence.Provider != "code.example" ||
		summary.Subject.Evidence.Repository != "acme/widgets-code" || summary.Subject.Evidence.ID != "change-42" {
		t.Fatalf("external subject identity = %+v", summary.Subject)
	}
	if *updates != 0 {
		t.Fatalf("blocked summary consumed evidence with %d updates", *updates)
	}
}

func TestBuildFinalVerifyReportRequiresDoneTasksAndCoverage(t *testing.T) {
	spec := typedArtifact(t, 1, "SPEC", "SPEC-001", "confirmed", "## Requirement: X\n\nX MUST work.\n\n### Scenario: ok\n\n- **WHEN** x\n- **THEN** y")
	task := typedArtifact(t, 2, "TASK", "TASK-001", "ready", canonicalTaskContent)
	verify := typedArtifact(t, 3, "VERIFY", "VERIFY-001", "done", canonicalVerifyContent)
	report, err := buildFinalVerifyReport([]model.Artifact{spec, task, verify}, "https://github.com/o/r/issues/1", finalVerifyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if report.OK {
		t.Fatal("ready TASK should fail final verify")
	}
	if !report.SpecCoverage["SPEC-001"] {
		t.Fatalf("expected SPEC-001 coverage: %+v", report.SpecCoverage)
	}
}

func TestFinalVerifyUsesAuthoritativePullRequestAncestry(t *testing.T) {
	ancestor := strings.Repeat("a", 40)
	head := strings.Repeat("b", 40)
	unrelated := strings.Repeat("c", 40)

	ancestorArtifacts := finalVerifyChangeBearingArtifacts(t, ancestor)
	ancestorReport, err := buildFinalVerifyReport(ancestorArtifacts, "https://github.com/o/r/issues/1", finalVerifyOptions{
		PR: 7, PRURL: "https://github.com/o/r/pull/7", ExpectedRevision: head,
		PRCommits: []github.PullRequestCommit{{SHA: strings.Repeat("0", 40)}, {SHA: ancestor}, {SHA: head}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if finalReportHasGateCode(ancestorReport, gates.CodeProcessWorkspaceRevisionStale) {
		t.Fatalf("authoritative multi-commit PR ancestor was rejected: %+v", ancestorReport.Gate.Diagnostics)
	}

	headReport, err := buildFinalVerifyReport(finalVerifyChangeBearingArtifacts(t, head), "https://github.com/o/r/issues/1", finalVerifyOptions{
		PR: 7, PRURL: "https://github.com/o/r/pull/7", ExpectedRevision: head,
	})
	if err != nil {
		t.Fatal(err)
	}
	if finalReportHasGateCode(headReport, gates.CodeProcessWorkspaceRevisionStale) {
		t.Fatalf("exact PR head was rejected: %+v", headReport.Gate.Diagnostics)
	}

	for name, commits := range map[string][]github.PullRequestCommit{
		"unrelated":                       {{SHA: unrelated}},
		"integration present head absent": {{SHA: ancestor}},
		"collection failed":               nil,
	} {
		t.Run(name, func(t *testing.T) {
			report, buildErr := buildFinalVerifyReport(ancestorArtifacts, "https://github.com/o/r/issues/1", finalVerifyOptions{
				PR: 7, PRURL: "https://github.com/o/r/pull/7", ExpectedRevision: head, PRCommits: commits,
			})
			if buildErr != nil {
				t.Fatal(buildErr)
			}
			if !finalReportHasGateCode(report, gates.CodeProcessWorkspaceRevisionStale) {
				t.Fatalf("non-authoritative ancestry was accepted: %+v", report.Gate.Diagnostics)
			}
		})
	}
}

func TestRunVerifyRejectsUnstablePullRequestIdentity(t *testing.T) {
	t.Setenv(auth.ConfigDirEnv, t.TempDir())
	t.Setenv(auth.ProfileEnv, auth.DefaultProfileName)
	t.Setenv(auth.GitHubBackendAPIURLEnv, "")
	initialHead := strings.Repeat("a", 40)
	advancedHead := strings.Repeat("b", 40)
	valid := pullRequestAtHead(7, initialHead)
	advanced := pullRequestAtHead(7, advancedHead)
	missingNumber := valid
	missingNumber.Number = 0
	wrongRepo := valid
	wrongRepo.HTMLURL = "https://github.com/o/other/pull/7"
	for name, pulls := range map[string][]github.PullRequest{
		"head advanced":  {valid, advanced},
		"missing number": {missingNumber, valid},
		"wrong repo URL": {wrongRepo, wrongRepo},
	} {
		t.Run(name, func(t *testing.T) {
			backend := &sequencedPullRequestCommitBackend{
				fakeGitHubBackend: fakeGitHubBackend{
					info: github.BackendInfo{Name: "rest", Kind: "rest", Host: "github.com"},
					getIssue: func(_ context.Context, _ string, issue int) (github.Issue, error) {
						return github.Issue{Number: issue, HTMLURL: "https://github.com/o/r/issues/1"}, nil
					},
					listIssueComments: func(context.Context, string, int) ([]github.Comment, error) { return nil, nil },
				},
				pulls: pulls, commits: []github.PullRequestCommit{{SHA: initialHead}},
			}
			var out, errOut bytes.Buffer
			app := newApp(strings.NewReader(""), &out, &errOut)
			app.selectGitHubBackend = ghSelection
			app.newGitHubBackend = func(context.Context, auth.GitHubBackendSelection) (github.Backend, error) { return backend, nil }
			code := app.runVerify(t.Context(), []string{"--repo", "o/r", "--proposal", "1", "--design", "2", "--implement", "3", "--pr", "7", "--json"})
			if code != 1 || !strings.Contains(errOut.String(), "pull request changed while collecting gate facts") {
				t.Fatalf("verify code=%d out=%q err=%q", code, out.String(), errOut.String())
			}
		})
	}
}

func TestRunVerifyRequiresGitHubPRAuthorityForChangeBearingProcesses(t *testing.T) {
	for _, test := range []struct {
		name           string
		processContent string
		pr             string
	}{
		{name: "legacy defaults to change-bearing", processContent: canonicalProcessContent},
		{name: "explicit change-bearing", processContent: canonicalProcessContentWithClass(model.ProcessExecutionChangeBearing)},
		{name: "non-positive PR cannot bypass authority", processContent: canonicalProcessContentWithClass(model.ProcessExecutionChangeBearing), pr: "-1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			app, out, errOut := newGitHubVerifyWithoutPRApp(t, test.processContent)
			args := []string{
				"--repo", "o/r", "--proposal", "1", "--design", "2", "--implement", "3", "--json",
			}
			if test.pr != "" {
				args = append(args, "--pr", test.pr)
			}
			code := app.runVerify(t.Context(), args)
			if code != 2 || !strings.Contains(errOut.String(), "--pr is required for GitHub verify") {
				t.Fatalf("verify exit=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
			}
		})
	}
}

func TestRunVerifyWithoutPRAuthorityAllowsNonChangeBearingProcesses(t *testing.T) {
	for _, test := range []struct {
		class    model.ProcessExecutionClass
		wantCode int
	}{
		{class: model.ProcessExecutionVerification, wantCode: 0},
		{class: model.ProcessExecutionReview, wantCode: 0},
		{class: model.ProcessExecutionOrchestration, wantCode: 0},
		// External PROCESS has its own exact-revision provider-evidence gate.
		// The missing GitHub PR authority check must not replace that contract.
		{class: model.ProcessExecutionExternal, wantCode: 1},
	} {
		t.Run(string(test.class), func(t *testing.T) {
			app, out, errOut := newGitHubVerifyWithoutPRApp(t, canonicalProcessContentWithClass(test.class))
			code := app.runVerify(t.Context(), []string{
				"--repo", "o/r", "--proposal", "1", "--design", "2", "--implement", "3", "--json",
			})
			if code != test.wantCode || strings.Contains(errOut.String(), "--pr is required") {
				t.Fatalf("verify exit=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
			}
			var report finalVerifyReport
			if err := json.Unmarshal(out.Bytes(), &report); err != nil {
				t.Fatalf("decode verify report: %v\n%s", err, out.String())
			}
			if test.wantCode == 0 && !report.OK {
				t.Fatalf("non-change-bearing verify unexpectedly failed: %+v", report)
			}
			if test.class == model.ProcessExecutionExternal &&
				!finalReportHasGateCode(report, gates.CodeProcessWorkspaceProviderEvidenceMissing) {
				t.Fatalf("external PROCESS lost its provider-evidence contract: %+v", report.Gate.Diagnostics)
			}
		})
	}
}

func newGitHubVerifyWithoutPRApp(t *testing.T, processContent string) (*app, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	t.Setenv(auth.ConfigDirEnv, t.TempDir())
	t.Setenv(auth.ProfileEnv, auth.DefaultProfileName)
	t.Setenv(auth.GitHubBackendAPIURLEnv, "")
	const (
		specURL    = "https://github.com/o/r/issues/1#issuecomment-1"
		taskURL    = "https://github.com/o/r/issues/2#issuecomment-2"
		processURL = "https://github.com/o/r/issues/3#issuecomment-3"
		verifyURL  = "https://github.com/o/r/issues/3#issuecomment-4"
	)
	spec := typedCommentWithLinks(t, "SPEC", "SPEC-001", "confirmed", "## Requirement: X\n\nX MUST work.\n\n### Scenario: ok\n\n- **WHEN** x\n- **THEN** y", 1, specURL, taskURL)
	task := typedCommentWithLinks(t, "TASK", "TASK-001", "done", canonicalTaskContent, 2, taskURL, specURL, processURL)
	process := typedCommentWithLinks(t, "PROCESS", "PROCESS-001", "done", processContent, 3, processURL, taskURL)
	verify := typedCommentWithLinks(t, "VERIFY", "VERIFY-001", "done", canonicalVerifyContent, 4, verifyURL)
	backend := fakeGitHubBackend{
		info: github.BackendInfo{Name: "rest", Kind: "rest", Host: "github.com"},
		getIssue: func(_ context.Context, _ string, issue int) (github.Issue, error) {
			return github.Issue{Number: issue, HTMLURL: "https://github.com/o/r/issues/1"}, nil
		},
		listIssueComments: func(_ context.Context, _ string, issue int) ([]github.Comment, error) {
			switch issue {
			case 1:
				return []github.Comment{spec}, nil
			case 2:
				return []github.Comment{task}, nil
			case 3:
				return []github.Comment{process, verify}, nil
			default:
				return nil, nil
			}
		},
	}
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	application := newApp(strings.NewReader(""), out, errOut)
	application.selectGitHubBackend = ghSelection
	application.newGitHubBackend = func(context.Context, auth.GitHubBackendSelection) (github.Backend, error) {
		return backend, nil
	}
	return application, out, errOut
}

func canonicalProcessContentWithClass(class model.ProcessExecutionClass) string {
	return strings.Replace(canonicalProcessContent, "### Write Ownership",
		"### Execution Class\n\n- "+string(class)+"\n\n### Write Ownership", 1)
}

func TestRunVerifySelfHostedPreservesBlockingGateAndSkipsEvidenceConsumption(t *testing.T) {
	app, out, errOut, updates := newSelfHostedVerifyApp(t)

	buildBlockedReport := func([]model.Artifact, string, finalVerifyOptions) (finalVerifyReport, error) {
		return finalVerifyReport{OK: false, Gate: gates.Report{
			Ready: false, Target: gates.TargetFinal, Mode: gates.ModeAuthoritative,
			Diagnostics: []gates.Diagnostic{{
				Code: "future.blocking", Gate: gates.TargetFinal, Severity: gates.SeverityError,
				Blocking: true, Message: "future blocking diagnostic without a legacy error projection",
			}},
		}}, nil
	}
	code := app.runVerifyWithReportBuilder(t.Context(), []string{
		"--repo", "acme/widgets", "--proposal", "1", "--design", "2", "--implement", "3", "--json",
	}, buildBlockedReport)
	if code != 1 {
		t.Fatalf("self-hosted verify exit=%d, want 1; stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	if *updates != 0 {
		t.Fatalf("blocking gate consumed external evidence with %d comment updates", *updates)
	}
	var report finalVerifyReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("decode verify report: %v\n%s", err, out.String())
	}
	if report.OK || report.Gate.Ready || len(report.Errors) != 0 {
		t.Fatalf("blocking gate was not preserved without legacy errors: %+v", report)
	}
}

func TestRunVerifyRejectsRemovedDurableSpecGateFlag(t *testing.T) {
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	called := false
	code := app.runVerifyWithReportBuilder(t.Context(), []string{"--durable-spec", "spec.md"},
		func([]model.Artifact, string, finalVerifyOptions) (finalVerifyReport, error) {
			called = true
			return finalVerifyReport{}, nil
		})
	if code != 2 || called || !strings.Contains(errOut.String(), "flag provided but not defined: -durable-spec") {
		t.Fatalf("removed durable gate flag code=%d called=%t stdout=%q stderr=%q", code, called, out.String(), errOut.String())
	}
}

func TestRunVerifySelfHostedRevisionFailureIsAuthoritativeDiagnostic(t *testing.T) {
	app, out, errOut, updates := newSelfHostedVerifyAppAtRevision(t, "stale-head")
	code := app.runVerify(t.Context(), []string{
		"--repo", "acme/widgets", "--proposal", "1", "--design", "2", "--implement", "3", "--json",
	})
	if code != 1 {
		t.Fatalf("self-hosted verify exit=%d, want 1; stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	var report finalVerifyReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("decode verify report: %v\n%s", err, out.String())
	}
	diagnostic := finalReportGateDiagnostic(report, gates.CodeVerifyRevisionInvalid)
	if report.OK || diagnostic == nil || diagnostic.Artifact.ID != "VERIFY-001" ||
		diagnostic.Expected != "head-abc" || diagnostic.Remediation.CommandFamily != "comment upsert" {
		t.Fatalf("exact-revision failure missing authoritative diagnostic: %+v", report.Gate)
	}
	if !errorsContain(report.Errors, "exact external head revision head-abc") {
		t.Fatalf("legacy exact-revision error projection changed: %+v", report.Errors)
	}
	if *updates != 0 {
		t.Fatalf("revision mismatch consumed external evidence with %d comment updates", *updates)
	}
}

func TestRunVerifySelfHostedRejectsAnyExplicitPR(t *testing.T) {
	const externalEvidenceError = "external evidence fixture failed"
	failures := []struct {
		name   string
		inject func(*app)
	}{
		{
			name: "provider construction fails",
			inject: func(app *app) {
				app.newNativeEvidenceProvider = func(auth.Profile, string) (nativeEvidenceProvider, error) {
					return nil, errors.New(externalEvidenceError)
				}
			},
		},
		{
			name: "target resolution fails",
			inject: func(app *app) {
				baseProvider := app.newNativeEvidenceProvider
				app.newNativeEvidenceProvider = func(profile auth.Profile, token string) (nativeEvidenceProvider, error) {
					provider, err := baseProvider(profile, token)
					if err != nil {
						return nil, err
					}
					return &failingResolveNativeEvidence{nativeEvidenceProvider: provider, err: errors.New(externalEvidenceError)}, nil
				}
			},
		},
	}
	arguments := []struct {
		name     string
		prArg    string
		wantCode int
		rejected bool
	}{
		{name: "omitted uses external evidence", wantCode: 1},
		{name: "explicit zero", prArg: "--pr=0", wantCode: 2, rejected: true},
		{name: "explicit negative", prArg: "--pr=-1", wantCode: 2, rejected: true},
		{name: "explicit positive", prArg: "--pr=7", wantCode: 2, rejected: true},
	}
	for _, failure := range failures {
		t.Run(failure.name, func(t *testing.T) {
			for _, argument := range arguments {
				t.Run(argument.name, func(t *testing.T) {
					app, out, errOut, updates := newSelfHostedVerifyApp(t)
					failure.inject(app)
					args := []string{"--repo", "acme/widgets", "--proposal", "1", "--design", "2", "--implement", "3", "--json"}
					if argument.prArg != "" {
						args = append(args, argument.prArg)
					}
					code := app.runVerify(t.Context(), args)
					stderr := errOut.String()
					rejected := strings.Contains(stderr, "--pr is not a self-hosted code authority")
					externalError := strings.Contains(stderr, "verify external evidence:") || strings.Contains(stderr, externalEvidenceError)
					if code != argument.wantCode || rejected != argument.rejected {
						t.Fatalf("self-hosted verify exit=%d rejected=%t stdout=%q stderr=%q", code, rejected, out.String(), stderr)
					}
					if argument.rejected && externalError {
						t.Fatalf("explicit --pr exposed lower-priority external evidence error: %q", stderr)
					}
					if !argument.rejected && (!strings.Contains(stderr, "verify external evidence:") || !strings.Contains(stderr, externalEvidenceError)) {
						t.Fatalf("omitted --pr did not report external evidence failure: %q", stderr)
					}
					if !argument.rejected {
						var report finalVerifyReport
						if decodeErr := json.Unmarshal(out.Bytes(), &report); decodeErr != nil {
							t.Fatalf("provider failure did not return final decision JSON: %v\nstdout=%q", decodeErr, out.String())
						}
						diagnostic := finalReportGateDiagnostic(report, gates.CodeProviderEvidenceMissing)
						if report.OK || diagnostic == nil || diagnostic.Remediation.CommandFamily != "evidence explain" {
							t.Fatalf("provider failure missing actionable gate diagnostic: %+v", report.Gate)
						}
					}
					if *updates != 0 {
						t.Fatalf("self-hosted verify unexpectedly consumed evidence with %d comment updates", *updates)
					}
				})
			}
		})
	}
}

type failingResolveNativeEvidence struct {
	nativeEvidenceProvider
	err error
}

func (e *failingResolveNativeEvidence) ResolveTarget(context.Context, string, int, string) (coreevidence.NativeTarget, error) {
	return coreevidence.NativeTarget{}, e.err
}

func newSelfHostedVerifyApp(t *testing.T) (*app, *bytes.Buffer, *bytes.Buffer, *int) {
	return newSelfHostedVerifyAppAtRevision(t, "head-abc")
}

func newSelfHostedVerifyAppAtRevision(t *testing.T, verifyRevision string) (*app, *bytes.Buffer, *bytes.Buffer, *int) {
	t.Helper()
	clearCommandAuthEnv(t)
	revision := "head-abc"
	profile := auth.Profile{Name: "verify-fail-closed", Kind: auth.ProfileKindHosted,
		APIURL: "https://issues.example/api/v3", NativeAPIURL: "https://issues.example/api/v1",
		WebURL: "https://issues.example", ServerInstanceID: "instance-verify-fail-closed"}
	if err := auth.SaveProfile(profile, true); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.StoreProfileToken(t.Context(), profile, "realm-token", true); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	reference := codereview.Reference{ProviderKey: "code.example", ExternalRepository: "acme/widgets-code", ChangeID: "change-42"}
	review := testEvidenceRecord("review-1", codereview.EvidenceReview, "resolved", revision, now)
	check := testEvidenceRecord("check-1", codereview.EvidenceCheck, "passed", revision, now)
	check.Name = "unit"
	provider := &commandEvidenceProvider{snapshot: codereview.Snapshot{
		ProtocolVersion: codereview.ProtocolVersion, Reference: reference, SubjectRevision: revision, CapturedAt: now,
		Records: []codereview.EvidenceRecord{review, check},
	}}
	native := &commandNativeEvidence{target: coreevidence.NativeTarget{
		Reference: reference, ReferenceVersion: 7, SubjectRevision: revision, Provider: provider,
		IssueID: uuid.New(), OrgID: uuid.New(), RepoID: uuid.New(),
	}}
	verify := typedCommentWithLinks(t, "VERIFY", "VERIFY-001", "done",
		canonicalVerifyContent+"\n\n### Revision\n\n`"+verifyRevision+"`", 4,
		"https://issues.example/acme/widgets/issues/3#issuecomment-4")

	updates := new(int)
	backend := fakeGitHubBackend{
		info: github.BackendInfo{Name: "rest", Kind: "rest", Host: "issues.example"},
		getIssue: func(_ context.Context, _ string, issue int) (github.Issue, error) {
			return github.Issue{Number: issue, HTMLURL: "https://issues.example/acme/widgets/issues/1"}, nil
		},
		listIssueComments: func(_ context.Context, _ string, issue int) ([]github.Comment, error) {
			if issue == 3 {
				return []github.Comment{verify}, nil
			}
			return nil, nil
		},
		updateComment: func(context.Context, string, int64, string) (github.Comment, error) {
			(*updates)++
			return github.Comment{}, nil
		},
	}
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	app := newApp(strings.NewReader(""), out, errOut)
	app.profileName = profile.Name
	app.newGitHubBackend = func(context.Context, auth.GitHubBackendSelection) (github.Backend, error) {
		return backend, nil
	}
	app.newNativeEvidenceProvider = func(auth.Profile, string) (nativeEvidenceProvider, error) {
		return native, nil
	}
	app.lookupOperatorProvider = func(context.Context, auth.Profile, string) (codereview.Provider, error) {
		return &commandEvidenceProvider{snapshot: codereview.Snapshot{ProtocolVersion: codereview.ProtocolVersion,
			Reference: reference, SubjectRevision: revision, CapturedAt: now}}, nil
	}
	return app, out, errOut, updates
}

func finalVerifyChangeBearingArtifacts(t *testing.T, integrationSHA string) []model.Artifact {
	t.Helper()
	spec := typedArtifact(t, 1, "SPEC", "SPEC-001", "confirmed", "## Requirement: X\n\nX MUST work.\n\n### Scenario: ok\n\n- **WHEN** x\n- **THEN** y")
	task := typedArtifact(t, 2, "TASK", "TASK-001", "done", canonicalTaskContent)
	process := typedArtifact(t, 3, "PROCESS", "PROCESS-001", "done", "## Process: impl\n\n### Parent TASK\n\n- TASK-001\n\n### Execution Class\n\n- change-bearing\n\n### Covers\n\n- SPEC-001\n\n### Handoff\n\ncomplete")
	verify := typedArtifact(t, 4, "VERIFY", "VERIFY-001", "done", canonicalVerifyContent+"\n\nTest evidence covers PROCESS-001.")
	linkArtifacts(t, &spec, &task)
	linkArtifacts(t, &task, &process)
	body, _, err := model.AddPRLink(process.Comment.Body, "https://github.com/o/r/pull/7")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(100, 0).UTC()
	workspace := processworkspace.PortableLease{SchemaVersion: processworkspace.LeaseSchemaVersion, WorkspaceID: "ws-process-001", Repository: "o/r",
		ProcessID: "PROCESS-001", ExecutionClass: processworkspace.ExecutionChangeBearing, Mode: processworkspace.ModeWritable,
		BaseSHA: strings.Repeat("0", 40), Branch: "codex/process-001", ResultCommit: strings.Repeat("1", 40), IntegrationSHA: integrationSHA,
		WriteOwnership: []string{"internal/x"}, RuntimeNamespace: "ws-process-001", State: processworkspace.StateIntegrated, CreatedAt: now, UpdatedAt: now}
	transition, err := model.ApplyTypedTransition(body, model.TransitionRequest{ExpectedType: "PROCESS", ExpectedID: "PROCESS-001", Workspace: &workspace})
	if err != nil {
		t.Fatal(err)
	}
	process.Comment = model.ParseTypedComment(transition.Body)
	return []model.Artifact{spec, task, process, verify}
}

func finalReportHasGateCode(report finalVerifyReport, code string) bool {
	for _, diagnostic := range report.Gate.Diagnostics {
		if diagnostic.Code == code {
			return true
		}
	}
	return false
}

func finalReportGateDiagnostic(report finalVerifyReport, code string) *gates.Diagnostic {
	for index := range report.Gate.Diagnostics {
		if report.Gate.Diagnostics[index].Code == code {
			return &report.Gate.Diagnostics[index]
		}
	}
	return nil
}

func errorsContain(errs []string, substr string) bool {
	for _, err := range errs {
		if strings.Contains(err, substr) {
			return true
		}
	}
	return false
}

func TestBuildFinalVerifyReportDoesNotRequireSessionMetadata(t *testing.T) {
	spec := typedArtifact(t, 1, "SPEC", "SPEC-001", "confirmed", "## Requirement: X\n\nX MUST work.\n\n### Scenario: ok\n\n- **WHEN** x\n- **THEN** y")
	task := typedArtifact(t, 2, "TASK", "TASK-001", "done", canonicalTaskContent)
	process := typedArtifact(t, 3, "PROCESS", "PROCESS-001", "done", canonicalProcessContentWithClass(model.ProcessExecutionVerification))
	verify := typedArtifact(t, 3, "VERIFY", "VERIFY-001", "done", canonicalVerifyContent)
	linkArtifacts(t, &spec, &task)
	linkArtifacts(t, &task, &process)
	report, err := buildFinalVerifyReport([]model.Artifact{spec, task, process, verify}, "https://github.com/o/r/issues/1", finalVerifyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !report.OK {
		t.Fatalf("metadata diagnostics should not fail verify: %+v", report.Errors)
	}
	if len(report.Diagnostics) != 0 {
		t.Fatalf("diagnostics = %+v", report.Diagnostics)
	}
}

func TestBuildFinalVerifyReportWithoutPRAuthorityFailsChangeBearing(t *testing.T) {
	fixture := func(t *testing.T) (model.Artifact, model.Artifact, model.Artifact, model.Artifact) {
		t.Helper()
		spec := typedArtifact(t, 1, "SPEC", "SPEC-001", "confirmed", "## Requirement: X\n\nX MUST work.\n\n### Scenario: ok\n\n- **WHEN** x\n- **THEN** y")
		spec.URL = "https://github.com/o/r/issues/1#issuecomment-1"
		task := typedArtifact(t, 2, "TASK", "TASK-001", "done", canonicalTaskContent)
		task.URL = "https://github.com/o/r/issues/2#issuecomment-2"
		process := typedArtifact(t, 3, "PROCESS", "PROCESS-001", "done", canonicalProcessContent)
		process.URL = "https://github.com/o/r/issues/3#issuecomment-3"
		verify := typedArtifact(t, 3, "VERIFY", "VERIFY-001", "done", canonicalVerifyContent)
		linkArtifacts(t, &spec, &task)
		linkArtifacts(t, &task, &process)
		return spec, task, process, verify
	}

	t.Run("missing carrier", func(t *testing.T) {
		spec, task, process, verify := fixture(t)
		report, err := buildFinalVerifyReport([]model.Artifact{spec, task, process, verify},
			"https://github.com/o/r/issues/1", finalVerifyOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if report.OK || !finalReportHasGateCode(report, gates.CodeProcessCarrierMissing) {
			t.Fatalf("change-bearing helper call without PR carrier must fail closed: %+v", report)
		}
	})

	t.Run("carrier still requires independent review", func(t *testing.T) {
		spec, task, process, verify := fixture(t)
		linkArtifacts(t, &spec, &process)
		processBody, _, err := model.AddPRLink(process.Comment.Body, "https://github.com/o/r/pull/7")
		if err != nil {
			t.Fatal(err)
		}
		process.Comment = model.ParseTypedComment(processBody)
		rationale, err := model.RenderRationaleBody("Worker Agent A", "PROCESS-001", "SPEC-001", spec.URL,
			"Explain why.", "internal/foo.go", 12)
		if err != nil {
			t.Fatal(err)
		}
		report, err := buildFinalVerifyReport([]model.Artifact{spec, task, process, verify},
			"https://github.com/o/r/issues/1", finalVerifyOptions{
				RationaleComments: []github.PullRequestReviewComment{{Body: rationale, Path: "internal/foo.go", Line: 12}},
			})
		if err != nil {
			t.Fatal(err)
		}
		if report.OK || !finalReportHasGateCode(report, gates.CodeProcessReviewRequired) {
			t.Fatalf("change-bearing helper call without independent review must fail closed: %+v", report)
		}
		if finalReportHasGateCode(report, gates.CodeProcessCarrierMissing) {
			t.Fatalf("valid carrier was not credited before the review gate: %+v", report.Gate.Diagnostics)
		}
	})
}

func TestBuildFinalVerifyReportChecksRationaleCoverageWhenPRProvided(t *testing.T) {
	spec := typedArtifact(t, 1, "SPEC", "SPEC-001", "confirmed", "## Requirement: X\n\nX MUST work.\n\n### Scenario: ok\n\n- **WHEN** x\n- **THEN** y")
	spec.URL = "https://github.com/o/r/issues/1#issuecomment-1"
	task := typedArtifact(t, 2, "TASK", "TASK-001", "done", canonicalTaskContent)
	task.URL = "https://github.com/o/r/issues/2#issuecomment-2"
	process := typedArtifact(t, 3, "PROCESS", "PROCESS-001", "done", canonicalProcessContent)
	process.URL = "https://github.com/o/r/issues/3#issuecomment-3"
	// An independent review PROCESS is mandatory for any SPEC with a valid
	// change-bearing carrier. Its reviewing agent differs from the code author,
	// so it satisfies both the presence and independence requirements.
	reviewProcess := typedArtifact(t, 3, "PROCESS", "PROCESS-002", "done", canonicalReviewProcess)
	reviewProcess.URL = "https://github.com/o/r/issues/3#issuecomment-4"
	reviewProcessBody, _, err := model.AddPRLink(reviewProcess.Comment.Body, "https://github.com/o/r/pull/7")
	if err != nil {
		t.Fatal(err)
	}
	reviewProcess.Comment = model.ParseTypedComment(reviewProcessBody)
	review := typedArtifactWithAgent(t, 3, "REVIEW", "REVIEW-001", "done", "Reviewer Agent B", "## Review\n\nReviewed PROCESS-002 covering SPEC-001. No blocking findings.")
	verify := typedArtifact(t, 3, "VERIFY", "VERIFY-001", "done", canonicalVerifyContent)
	linkArtifacts(t, &spec, &task)
	linkArtifacts(t, &task, &process)
	linkArtifacts(t, &task, &reviewProcess)
	canonicalizeReviewFixture(t, &review, []model.Artifact{reviewProcess}, spec)
	report, err := buildFinalVerifyReport([]model.Artifact{spec, task, process, reviewProcess, review, verify}, "https://github.com/o/r/issues/1", finalVerifyOptions{
		PR:                7,
		PRURL:             "https://github.com/o/r/pull/7",
		RationaleRequired: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.OK {
		t.Fatal("missing rationale should fail when PR is supplied")
	}
	body, err := model.RenderRationaleBody("Worker Agent A", "PROCESS-001", "SPEC-001", spec.URL, "Explain why.", "internal/foo.go", 12)
	if err != nil {
		t.Fatal(err)
	}
	processWithPR := process
	processBody, changed, err := model.AddPRLink(processWithPR.Comment.Body, "https://github.com/o/r/pull/7")
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected PR link to change process body")
	}
	processWithPR.Comment = model.ParseTypedComment(processBody)
	report, err = buildFinalVerifyReport([]model.Artifact{spec, task, process, reviewProcess, review, verify}, "https://github.com/o/r/issues/1", finalVerifyOptions{
		PR:                7,
		PRURL:             "https://github.com/o/r/pull/7",
		RationaleRequired: true,
		RationaleComments: []github.PullRequestReviewComment{{Body: body, Path: "internal/foo.go", Line: 12}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.OK {
		t.Fatal("missing PROCESS PR link should fail even when rationale exists")
	}
	report, err = buildFinalVerifyReport([]model.Artifact{spec, task, processWithPR, reviewProcess, review, verify}, "https://github.com/o/r/issues/1", finalVerifyOptions{
		PR:                7,
		PRURL:             "https://github.com/o/r/pull/7",
		RationaleRequired: true,
		RationaleComments: []github.PullRequestReviewComment{{Body: body, Path: "internal/foo.go", Line: 12}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !report.OK {
		t.Fatalf("expected rationale coverage OK: %+v", report.Errors)
	}
}

func TestBuildFinalVerifyReportAcceptsExactSelfHostedRationaleAndNativeLedgerReview(t *testing.T) {
	const (
		specURL   = "https://issues.example/acme/widgets/issues/1#issuecomment-1"
		changeURL = "https://code.example/acme/widgets-code/changes/change-42"
		revision  = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	)
	spec := typedArtifact(t, 1, "SPEC", "SPEC-001", "confirmed", "## Requirement: X\n\nX MUST work.\n\n### Scenario: ok\n\n- **WHEN** x\n- **THEN** y")
	spec.URL = specURL
	task := typedArtifact(t, 2, "TASK", "TASK-001", "done", canonicalTaskContent)
	task.URL = "https://issues.example/acme/widgets/issues/2#issuecomment-2"
	process := typedArtifactWithAgent(t, 3, "PROCESS", "PROCESS-001", "done", "Coordinator", canonicalProcessContentWithClass(model.ProcessExecutionChangeBearing))
	process.URL = "https://issues.example/acme/widgets/issues/3#issuecomment-3"
	reviewProcess := typedArtifact(t, 3, "PROCESS", "PROCESS-002", "done", canonicalReviewProcess)
	reviewProcess.URL = "https://issues.example/acme/widgets/issues/3#issuecomment-4"
	reviewBody, err := model.EnsureTypedBody("REVIEW", "REVIEW-001",
		"## Review\n\nReviewed PROCESS-002 and implementation PROCESS-001 covering SPEC-001 with no blocking findings.",
		model.BodyOptions{Agent: "Independent Reviewer", Status: "done", SubjectRevision: revision})
	if err != nil {
		t.Fatal(err)
	}
	review := model.Artifact{Issue: 3, URL: "https://issues.example/acme/widgets/issues/3#issuecomment-review",
		Comment: model.ParseTypedComment(reviewBody)}
	verify := typedArtifact(t, 3, "VERIFY", "VERIFY-001", "done", canonicalVerifyContent)
	linkArtifacts(t, &spec, &task)
	linkArtifacts(t, &task, &process)
	linkArtifacts(t, &spec, &process)
	linkArtifacts(t, &task, &reviewProcess)
	linkArtifacts(t, &spec, &reviewProcess)
	linkArtifacts(t, &review, &reviewProcess)
	linkArtifacts(t, &review, &process)
	linkArtifacts(t, &review, &spec)
	for _, candidate := range []*model.Artifact{&process, &reviewProcess} {
		body, _, err := model.AddPRLink(candidate.Comment.Body, changeURL)
		if err != nil {
			t.Fatal(err)
		}
		candidate.Comment = model.ParseTypedComment(body)
	}
	now := time.Unix(100, 0).UTC()
	workspace := processworkspace.PortableLease{SchemaVersion: processworkspace.LeaseSchemaVersion,
		WorkspaceID: "ws-process-001", Repository: "acme/widgets", ProcessID: "PROCESS-001",
		ExecutionClass: processworkspace.ExecutionChangeBearing, Mode: processworkspace.ModeWritable,
		BaseSHA: strings.Repeat("0", 40), Branch: "codex/process-001", WriteOwnership: []string{"internal/x"},
		RuntimeNamespace: "ws-process-001", State: processworkspace.StateIntegrated,
		ResultCommit: strings.Repeat("1", 40), IntegrationSHA: revision, CreatedAt: now, UpdatedAt: now}
	transition, err := model.ApplyTypedTransition(process.Comment.Body,
		model.TransitionRequest{ExpectedType: "PROCESS", ExpectedID: "PROCESS-001", Workspace: &workspace})
	if err != nil {
		t.Fatal(err)
	}
	process.Comment = model.ParseTypedComment(transition.Body)
	rationaleBody, err := model.RenderCodeChangeRationaleBody(model.CodeChangeRationaleMarker{
		Process: "PROCESS-001", Spec: "SPEC-001", SpecURL: specURL, ProviderKey: "code.example",
		ExternalRepository: "acme/widgets-code", ChangeID: "change-42", ReferenceVersion: 7,
		SubjectRevision: revision, Agent: "Worker Agent A", AgentSessionID: "worker-session",
		AgentSessionSource: "CODEX_THREAD_ID",
	}, "The implementation preserves the provider-neutral boundary.")
	if err != nil {
		t.Fatal(err)
	}
	rationale := model.Artifact{Issue: 3, URL: "https://issues.example/acme/widgets/issues/3#issuecomment-5",
		Comment: model.ParseTypedComment(rationaleBody)}
	consumption := externalEvidenceConsumption{ProviderKey: "code.example", ExternalRepository: "acme/widgets-code",
		ChangeID: "change-42", ReferenceVersion: 7, SubjectRevision: revision}
	reference := codereview.Reference{ProviderKey: consumption.ProviderKey,
		ExternalRepository: consumption.ExternalRepository, ChangeID: consumption.ChangeID}
	target := coreevidence.NativeTarget{Reference: reference, ReferenceVersion: consumption.ReferenceVersion,
		SubjectRevision: consumption.SubjectRevision}
	stampedReview, _, err := stampExternalReviewCompletion(review.Comment.Body, externalReviewCompletion{
		ProviderKey: reference.ProviderKey, ExternalRepository: reference.ExternalRepository,
		ChangeID: reference.ChangeID, ReferenceVersion: target.ReferenceVersion,
		SubjectRevision: target.SubjectRevision, SynchronizedAt: now.Add(-30 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	review.Comment = model.ParseTypedComment(stampedReview)
	externalReview := externalGateResult{Target: target, ReviewCompletionPolicy: ReviewCompletionPolicy{Required: true, Freshness: time.Hour},
		Evaluation: coreevidence.Result{Passed: true}, Consumption: consumption,
		Snapshot: codereview.Snapshot{ProtocolVersion: codereview.ProtocolVersion, Reference: reference,
			SubjectRevision: target.SubjectRevision, CapturedAt: now}}
	report, err := buildFinalVerifyReport([]model.Artifact{spec, task, process, reviewProcess, review, verify, rationale},
		"https://issues.example/acme/widgets/issues/1", finalVerifyOptions{ExpectedRevision: revision,
			ExternalEvidence: &consumption, ExternalReview: &externalReview, ValidationNow: now})
	if err != nil {
		t.Fatal(err)
	}
	if !report.OK || !report.Gate.Ready {
		t.Fatalf("exact-current self-hosted flow must pass final verify: errors=%+v diagnostics=%+v processes=%+v",
			report.Errors, report.Gate.Diagnostics, report.ProcessEvidence)
	}
	if len(report.ProcessEvidence) != 2 || report.ProcessEvidence[0].CarrierRevision.Revision != revision ||
		!report.ProcessEvidence[0].CarrierRevision.Trusted || report.ProcessEvidence[1].CarrierRevision.Revision != revision ||
		!report.ProcessEvidence[1].CarrierRevision.Trusted {
		t.Fatalf("self-hosted carrier revisions were not exact and trusted: %+v", report.ProcessEvidence)
	}
	futureBody, _, err := stampExternalReviewCompletion(review.Comment.Body, externalReviewCompletion{
		ProviderKey: reference.ProviderKey, ExternalRepository: reference.ExternalRepository,
		ChangeID: reference.ChangeID, ReferenceVersion: target.ReferenceVersion,
		SubjectRevision: target.SubjectRevision, SynchronizedAt: now.Add(time.Minute + time.Nanosecond),
	})
	if err != nil {
		t.Fatal(err)
	}
	futureReview := review
	futureReview.Comment = model.ParseTypedComment(futureBody)
	futureReport, err := buildFinalVerifyReport([]model.Artifact{spec, task, process, reviewProcess, futureReview, verify, rationale},
		"https://issues.example/acme/widgets/issues/1", finalVerifyOptions{ExpectedRevision: revision,
			ExternalEvidence: &consumption, ExternalReview: &externalReview, ValidationNow: now})
	if err != nil {
		t.Fatal(err)
	}
	if futureReport.OK || futureReport.Gate.Ready {
		t.Fatalf("future REVIEW completion satisfied final verify: %+v", futureReport.ProcessEvidence)
	}
}

func TestBuildFinalVerifyReportAppliesLegacyReviewFreshness(t *testing.T) {
	now := time.Date(2026, 7, 18, 1, 0, 0, 0, time.UTC)
	for name, observedAt := range map[string]time.Time{
		"fresh":   now.Add(-30 * time.Minute),
		"expired": now.Add(-2 * time.Hour),
	} {
		t.Run(name, func(t *testing.T) {
			artifacts, externalReview := legacyExternalReviewFixture(t, now, []time.Time{observedAt}, false, false)
			report, err := buildFinalVerifyReport(artifacts, artifacts[0].URL, finalVerifyOptions{
				ExpectedRevision: externalReview.Target.SubjectRevision, ExternalReview: &externalReview, ValidationNow: now,
			})
			if err != nil {
				t.Fatal(err)
			}
			var carrier gates.CarrierRevisionFact
			for _, process := range report.ProcessEvidence {
				if process.ProcessID == "PROCESS-002" {
					carrier = process.CarrierRevision
				}
			}
			if got, want := carrier.Trusted, name == "fresh"; got != want {
				t.Fatalf("legacy carrier trusted=%t want=%t process evidence=%+v", got, want, report.ProcessEvidence)
			}
		})
	}
}

// TestBuildFinalVerifyReportRequiresIndependentReviewProcess proves the command
// layer fails closed when a change-bearing SPEC has valid rationale but no review
// PROCESS covers it. Before OK was anchored to gateReport.Ready, the
// process.review.required diagnostic was silently dropped by the
// legacyVerifyGateError allowlist and final verify passed.
func TestBuildFinalVerifyReportRequiresIndependentReviewProcess(t *testing.T) {
	spec := typedArtifact(t, 1, "SPEC", "SPEC-001", "confirmed", "## Requirement: X\n\nX MUST work.\n\n### Scenario: ok\n\n- **WHEN** x\n- **THEN** y")
	spec.URL = "https://github.com/o/r/issues/1#issuecomment-1"
	task := typedArtifact(t, 2, "TASK", "TASK-001", "done", canonicalTaskContent)
	task.URL = "https://github.com/o/r/issues/2#issuecomment-2"
	process := typedArtifact(t, 3, "PROCESS", "PROCESS-001", "done", canonicalProcessContent)
	process.URL = "https://github.com/o/r/issues/3#issuecomment-3"
	verify := typedArtifact(t, 3, "VERIFY", "VERIFY-001", "done", canonicalVerifyContent)
	linkArtifacts(t, &spec, &task)
	linkArtifacts(t, &task, &process)
	processBody, _, err := model.AddPRLink(process.Comment.Body, "https://github.com/o/r/pull/7")
	if err != nil {
		t.Fatal(err)
	}
	process.Comment = model.ParseTypedComment(processBody)
	body, err := model.RenderRationaleBody("Worker Agent A", "PROCESS-001", "SPEC-001", spec.URL, "Explain why.", "internal/foo.go", 12)
	if err != nil {
		t.Fatal(err)
	}
	report, err := buildFinalVerifyReport([]model.Artifact{spec, task, process, verify}, "https://github.com/o/r/issues/1", finalVerifyOptions{
		PR:                7,
		PRURL:             "https://github.com/o/r/pull/7",
		RationaleRequired: true,
		RationaleComments: []github.PullRequestReviewComment{{Body: body, Path: "internal/foo.go", Line: 12}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.OK {
		t.Fatal("change-bearing SPEC without any review PROCESS must fail final verify")
	}
	if !finalReportHasGateCode(report, gates.CodeProcessReviewRequired) {
		t.Fatalf("expected %s diagnostic: %+v", gates.CodeProcessReviewRequired, report.Gate.Diagnostics)
	}
	if !errorsContain(report.Errors, "no independent review PROCESS") && !errorsContain(report.Errors, "independent review") {
		t.Fatalf("expected review-required error projected to report.Errors: %+v", report.Errors)
	}
}

func TestBuildFinalVerifyReportRejectsCoordinatorAuthoredChangeBearingEvidence(t *testing.T) {
	spec := typedArtifact(t, 1, "SPEC", "SPEC-001", "confirmed", "## Requirement: X\n\nX MUST work.\n\n### Scenario: ok\n\n- **WHEN** x\n- **THEN** y")
	spec.URL = "https://github.com/o/r/issues/1#issuecomment-1"
	task := typedArtifact(t, 2, "TASK", "TASK-001", "done", canonicalTaskContent)
	task.URL = "https://github.com/o/r/issues/2#issuecomment-2"
	process := typedArtifactWithAgent(t, 3, "PROCESS", "PROCESS-001", "done", "Coordinator", canonicalProcessContent)
	process.URL = "https://github.com/o/r/issues/3#issuecomment-3"
	verify := typedArtifact(t, 3, "VERIFY", "VERIFY-001", "done", canonicalVerifyContent)
	linkArtifacts(t, &spec, &task)
	linkArtifacts(t, &spec, &process)
	linkArtifacts(t, &task, &process)
	processBody, _, err := model.AddPRLink(process.Comment.Body, "https://github.com/o/r/pull/7")
	if err != nil {
		t.Fatal(err)
	}
	process.Comment = model.ParseTypedComment(processBody)
	rationale, err := model.RenderRationaleBody("  cOoRdInAtOr  ", "PROCESS-001", "SPEC-001", spec.URL,
		"Explain why.", "internal/foo.go", 12)
	if err != nil {
		t.Fatal(err)
	}
	report, err := buildFinalVerifyReport([]model.Artifact{spec, task, process, verify}, "https://github.com/o/r/issues/1", finalVerifyOptions{
		PR: 7, PRURL: "https://github.com/o/r/pull/7", RationaleRequired: true,
		RationaleComments: []github.PullRequestReviewComment{{Body: rationale, Path: "internal/foo.go", Line: 12}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.OK || !finalReportHasGateCode(report, gates.CodeProcessExecutorCoordinatorConflict) {
		t.Fatalf("coordinator-authored change-bearing evidence must fail final verify: %+v", report)
	}
	if !errorsContain(report.Errors, "also the PROCESS coordinator") {
		t.Fatalf("coordinator conflict must be projected to final verify errors: %+v", report.Errors)
	}
}

// TestBuildFinalVerifyReportRejectsSelfAuthoredReview proves a review PROCESS
// whose reviewing agent equals the code author of the SPEC does not satisfy the
// independence requirement and blocks final verify at the command layer.
func TestBuildFinalVerifyReportRejectsSelfAuthoredReview(t *testing.T) {
	spec := typedArtifact(t, 1, "SPEC", "SPEC-001", "confirmed", "## Requirement: X\n\nX MUST work.\n\n### Scenario: ok\n\n- **WHEN** x\n- **THEN** y")
	spec.URL = "https://github.com/o/r/issues/1#issuecomment-1"
	task := typedArtifact(t, 2, "TASK", "TASK-001", "done", canonicalTaskContent)
	task.URL = "https://github.com/o/r/issues/2#issuecomment-2"
	process := typedArtifact(t, 3, "PROCESS", "PROCESS-001", "done", canonicalProcessContent)
	process.URL = "https://github.com/o/r/issues/3#issuecomment-3"
	reviewProcess := typedArtifact(t, 3, "PROCESS", "PROCESS-002", "done", canonicalReviewProcess)
	reviewProcess.URL = "https://github.com/o/r/issues/3#issuecomment-4"
	reviewProcessBody, _, err := model.AddPRLink(reviewProcess.Comment.Body, "https://github.com/o/r/pull/7")
	if err != nil {
		t.Fatal(err)
	}
	reviewProcess.Comment = model.ParseTypedComment(reviewProcessBody)
	// The reviewing agent is the same identity that authored the change-bearing
	// rationale for SPEC-001, so the review is not independent.
	review := typedArtifactWithAgent(t, 3, "REVIEW", "REVIEW-001", "done", "Worker Agent A", "## Review\n\nReviewed PROCESS-002 covering SPEC-001. No blocking findings.")
	verify := typedArtifact(t, 3, "VERIFY", "VERIFY-001", "done", canonicalVerifyContent)
	linkArtifacts(t, &spec, &task)
	linkArtifacts(t, &spec, &process)
	linkArtifacts(t, &task, &process)
	linkArtifacts(t, &task, &reviewProcess)
	processBody, _, err := model.AddPRLink(process.Comment.Body, "https://github.com/o/r/pull/7")
	if err != nil {
		t.Fatal(err)
	}
	process.Comment = model.ParseTypedComment(processBody)
	canonicalizeReviewFixture(t, &review, []model.Artifact{reviewProcess}, spec)
	body, err := model.RenderRationaleBody("Worker Agent A", "PROCESS-001", "SPEC-001", spec.URL, "Explain why.", "internal/foo.go", 12)
	if err != nil {
		t.Fatal(err)
	}
	report, err := buildFinalVerifyReport([]model.Artifact{spec, task, process, reviewProcess, review, verify}, "https://github.com/o/r/issues/1", finalVerifyOptions{
		PR:                7,
		PRURL:             "https://github.com/o/r/pull/7",
		RationaleRequired: true,
		RationaleComments: []github.PullRequestReviewComment{{Body: body, Path: "internal/foo.go", Line: 12}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.OK {
		t.Fatal("self-authored review must fail final verify")
	}
	if !finalReportHasGateCode(report, gates.CodeProcessReviewAuthorConflict) {
		t.Fatalf("expected %s diagnostic: %+v", gates.CodeProcessReviewAuthorConflict, report.Gate.Diagnostics)
	}
	if !errorsContain(report.Errors, "authored by agent") {
		t.Fatalf("expected author-conflict error projected to report.Errors: %+v", report.Errors)
	}
}

func TestBuildFinalVerifyReportUsesVerificationCarrierInsteadOfRationale(t *testing.T) {
	spec := typedArtifact(t, 1, "SPEC", "SPEC-001", "confirmed", "## Requirement: X\n\nX MUST work.\n\n### Scenario: ok\n\n- **WHEN** x\n- **THEN** y")
	spec.URL = "https://github.com/o/r/issues/1#issuecomment-1"
	task := typedArtifact(t, 2, "TASK", "TASK-001", "done", canonicalTaskContent)
	task.URL = "https://github.com/o/r/issues/2#issuecomment-2"
	process := typedArtifact(t, 3, "PROCESS", "PROCESS-001", "done", "## Process: verify\n\n### Parent TASK\n\n- TASK-001\n\n### Execution Class\n\n- verification\n\n### Covers\n\n- SPEC-001\n\n### Handoff\n\nN/A")
	process.URL = "https://github.com/o/r/issues/3#issuecomment-3"
	verify := typedArtifact(t, 3, "VERIFY", "VERIFY-001", "done", "## Verification Summary: final\n\nTests passed for PROCESS-001.\n\n### Covered SPECs\n\n- SPEC-001")
	linkArtifacts(t, &spec, &task)
	linkArtifacts(t, &task, &process)
	body, _, err := model.AddPRLink(process.Comment.Body, "https://github.com/o/r/pull/7")
	if err != nil {
		t.Fatal(err)
	}
	process.Comment = model.ParseTypedComment(body)
	canonicalizeVerificationFixture(t, &verify, process, spec)
	report, err := buildFinalVerifyReport([]model.Artifact{spec, task, process, verify}, "https://github.com/o/r/issues/1", finalVerifyOptions{
		PR: 7, PRURL: "https://github.com/o/r/pull/7", RationaleRequired: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !report.OK || len(report.ProcessEvidence) != 1 || report.ProcessEvidence[0].ExecutionClass != model.ProcessExecutionVerification {
		t.Fatalf("verification carrier should pass without arbitrary rationale: errors=%v evidence=%+v", report.Errors, report.ProcessEvidence)
	}
}

func TestBuildFinalVerifyReportBlocksOpenP0P1Findings(t *testing.T) {
	spec := typedArtifact(t, 1, "SPEC", "SPEC-001", "confirmed", "## Requirement: X\n\nX MUST work.\n\n### Scenario: ok\n\n- **WHEN** x\n- **THEN** y")
	spec.URL = "https://github.com/o/r/issues/1#issuecomment-1"
	task := typedArtifact(t, 2, "TASK", "TASK-001", "done", canonicalTaskContent)
	task.URL = "https://github.com/o/r/issues/2#issuecomment-2"
	process := typedArtifact(t, 3, "PROCESS", "PROCESS-001", "done", canonicalProcessContent)
	process.URL = "https://github.com/o/r/issues/3#issuecomment-3"
	reviewProcess := typedArtifact(t, 3, "PROCESS", "PROCESS-002", "done", canonicalReviewProcess)
	reviewProcess.URL = "https://github.com/o/r/issues/3#issuecomment-4"
	reviewProcessBody, _, err := model.AddPRLink(reviewProcess.Comment.Body, "https://github.com/o/r/pull/7")
	if err != nil {
		t.Fatal(err)
	}
	reviewProcess.Comment = model.ParseTypedComment(reviewProcessBody)
	review := typedArtifactWithAgent(t, 3, "REVIEW", "REVIEW-001", "done", "Reviewer Agent B", "## Review\n\nReviewed PROCESS-002 covering SPEC-001. No blocking findings.")
	verify := typedArtifact(t, 3, "VERIFY", "VERIFY-001", "done", canonicalVerifyContent)
	linkArtifacts(t, &spec, &task)
	linkArtifacts(t, &task, &process)
	linkArtifacts(t, &task, &reviewProcess)
	processBody, changed, err := model.AddPRLink(process.Comment.Body, "https://github.com/o/r/pull/7")
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected PR link to change process body")
	}
	process.Comment = model.ParseTypedComment(processBody)
	canonicalizeReviewFixture(t, &review, []model.Artifact{reviewProcess}, spec)
	rationale, err := model.RenderRationaleBody("Worker Agent A", "PROCESS-001", "SPEC-001", spec.URL, "Explain why.", "internal/foo.go", 12)
	if err != nil {
		t.Fatal(err)
	}
	finding, err := model.RenderFindingBody("Review", "FINDING-001", "P1", "PROCESS-001", "SPEC-001", spec.URL, "Fix this before merge.", "open", "internal/foo.go", 12)
	if err != nil {
		t.Fatal(err)
	}
	report, err := buildFinalVerifyReport([]model.Artifact{spec, task, process, reviewProcess, review, verify}, "https://github.com/o/r/issues/1", finalVerifyOptions{
		PR:                7,
		PRURL:             "https://github.com/o/r/pull/7",
		RationaleRequired: true,
		RationaleComments: []github.PullRequestReviewComment{
			{ID: 1, Body: rationale, Path: "internal/foo.go", Line: 12},
			{ID: 2, Body: finding, Path: "internal/foo.go", Line: 12},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.OK {
		t.Fatal("open P1 finding should fail final verify")
	}
	if len(report.ReviewFindingBlockers) != 1 {
		t.Fatalf("expected one review finding blocker: %+v", report.ReviewFindingBlockers)
	}
	reply, err := model.RenderFindingReplyBody("Review", "FINDING-001", "PROCESS-001", "resolved", "Re-checked; fix satisfies the finding.")
	if err != nil {
		t.Fatal(err)
	}
	report, err = buildFinalVerifyReport([]model.Artifact{spec, task, process, reviewProcess, review, verify}, "https://github.com/o/r/issues/1", finalVerifyOptions{
		PR:                7,
		PRURL:             "https://github.com/o/r/pull/7",
		RationaleRequired: true,
		RationaleComments: []github.PullRequestReviewComment{
			{ID: 1, Body: rationale, Path: "internal/foo.go", Line: 12},
			{ID: 2, Body: finding, Path: "internal/foo.go", Line: 12},
			{ID: 3, InReplyToID: 2, Body: reply},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !report.OK {
		t.Fatalf("resolved P1 finding should pass final verify: %+v", report.Errors)
	}
}

func TestBuildFinalVerifyReportBlocksFailedAndPendingChecks(t *testing.T) {
	spec := typedArtifact(t, 1, "SPEC", "SPEC-001", "confirmed", "## Requirement: X\n\nX MUST work.\n\n### Scenario: ok\n\n- **WHEN** x\n- **THEN** y")
	spec.URL = "https://github.com/o/r/issues/1#issuecomment-1"
	task := typedArtifact(t, 2, "TASK", "TASK-001", "done", canonicalTaskContent)
	task.URL = "https://github.com/o/r/issues/2#issuecomment-2"
	process := typedArtifact(t, 3, "PROCESS", "PROCESS-001", "done", canonicalProcessContent)
	process.URL = "https://github.com/o/r/issues/3#issuecomment-3"
	review := typedArtifact(t, 3, "REVIEW", "REVIEW-001", "done", "## Review\n\nnone")
	verify := typedArtifact(t, 3, "VERIFY", "VERIFY-001", "done", canonicalVerifyContent)
	linkArtifacts(t, &spec, &task)
	linkArtifacts(t, &task, &process)
	processBody, changed, err := model.AddPRLink(process.Comment.Body, "https://github.com/o/r/pull/7")
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected PR link to change process body")
	}
	process.Comment = model.ParseTypedComment(processBody)
	rationale, err := model.RenderRationaleBody("Worker Agent A", "PROCESS-001", "SPEC-001", spec.URL, "Explain why.", "internal/foo.go", 12)
	if err != nil {
		t.Fatal(err)
	}
	report, err := buildFinalVerifyReport([]model.Artifact{spec, task, process, review, verify}, "https://github.com/o/r/issues/1", finalVerifyOptions{
		PR:                7,
		PRURL:             "https://github.com/o/r/pull/7",
		RationaleRequired: true,
		RationaleComments: []github.PullRequestReviewComment{{ID: 1, Body: rationale, Path: "internal/foo.go", Line: 12}},
		PRStatus: github.CombinedStatus{Statuses: []github.Status{
			{Context: "ci/test", State: "failure"},
		}},
		PRCheckRuns: []github.CheckRun{
			{Name: "build", Status: "queued"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.OK {
		t.Fatal("failed and pending checks should fail final verify")
	}
	if len(report.FailedChecks) != 1 || len(report.PendingChecks) != 1 {
		t.Fatalf("unexpected check blockers: failed=%+v pending=%+v", report.FailedChecks, report.PendingChecks)
	}
}

func TestBuildFinalVerifyReportRequiresSerialHandoff(t *testing.T) {
	// PROCESS-002 depends on PROCESS-001, so PROCESS-001 is a serial-chain
	// predecessor that must record ### Handoff evidence when done.
	buildReport := func(handoff string) finalVerifyReport {
		t.Helper()
		spec := typedArtifact(t, 1, "SPEC", "SPEC-001", "confirmed", "## Requirement: X\n\nX MUST work.\n\n### Scenario: ok\n\n- **WHEN** x\n- **THEN** y")
		spec.URL = "https://github.com/o/r/issues/1#issuecomment-1"
		task := typedArtifact(t, 2, "TASK", "TASK-001", "done", canonicalTaskContent)
		task.URL = "https://github.com/o/r/issues/2#issuecomment-2"
		p1 := typedArtifact(t, 3, "PROCESS", "PROCESS-001", "done", "## Process: p1\n\n### Owner\n\n- Worker\n\n### Parent TASK\n\n- TASK-001\n\n### Execution Class\n\n- orchestration\n\n### Dependencies\n\n- N/A\n\n### Covers\n\n- TASK-001\n\n### Handoff\n\n"+handoff)
		p1.URL = "https://github.com/o/r/issues/3#issuecomment-31"
		p2 := typedArtifact(t, 3, "PROCESS", "PROCESS-002", "done", "## Process: p2\n\n### Owner\n\n- Worker\n\n### Parent TASK\n\n- TASK-001\n\n### Execution Class\n\n- orchestration\n\n### Dependencies\n\n- PROCESS-001\n\n### Covers\n\n- TASK-001\n\n### Handoff\n\nN/A")
		p2.URL = "https://github.com/o/r/issues/3#issuecomment-32"
		verify := typedArtifact(t, 3, "VERIFY", "VERIFY-001", "done", canonicalVerifyContent)
		linkArtifacts(t, &spec, &task)
		linkArtifacts(t, &task, &p1)
		linkArtifacts(t, &task, &p2)
		linkArtifacts(t, &p1, &p2)
		report, err := buildFinalVerifyReport([]model.Artifact{spec, task, p1, p2, verify}, "https://github.com/o/r/issues/1", finalVerifyOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return report
	}

	failReport := buildReport("N/A")
	if failReport.OK {
		t.Fatal("serial-chain predecessor without handoff must fail final verify")
	}
	foundHandoff := false
	for _, e := range failReport.Errors {
		if strings.Contains(e, "PROCESS-001") && strings.Contains(e, "Handoff") {
			foundHandoff = true
		}
	}
	if !foundHandoff {
		t.Fatalf("expected serial handoff error for PROCESS-001: %v", failReport.Errors)
	}

	passReport := buildReport("state.json contract fixed; successor may parse it")
	if !passReport.OK {
		t.Fatalf("recorded handoff evidence should pass final verify: %v", passReport.Errors)
	}
}

func TestBuildFinalVerifyReportRequiresVerifyTestEvidence(t *testing.T) {
	spec := typedArtifact(t, 1, "SPEC", "SPEC-001", "confirmed", "## Requirement: X\n\nX MUST work.\n\n### Scenario: ok\n\n- **WHEN** x\n- **THEN** y")
	// VERIFY references SPEC-001 coverage but no test evidence.
	verify := typedArtifact(t, 3, "VERIFY", "VERIFY-001", "done", "## Verification Summary: final\n\n### Covered SPECs\n\n- SPEC-001")
	report, err := buildFinalVerifyReport([]model.Artifact{spec, verify}, "https://github.com/o/r/issues/1", finalVerifyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if report.OK {
		t.Fatal("VERIFY without test evidence must fail final verify")
	}
	if !strings.Contains(strings.Join(report.Errors, "\n"), "test evidence") {
		t.Fatalf("expected test-evidence error: %v", report.Errors)
	}
}

func TestBuildFinalVerifyReportTestEvidenceIgnoresSubstringMatch(t *testing.T) {
	spec := typedArtifact(t, 1, "SPEC", "SPEC-001", "confirmed", "## Requirement: X\n\nX MUST work.\n\n### Scenario: ok\n\n- **WHEN** x\n- **THEN** y")
	// "latest" contains the substring "test" but is not test evidence.
	verify := typedArtifact(t, 3, "VERIFY", "VERIFY-001", "done", "## Verification Summary: final\n\nRan the latest greatest review.\n\n### Covered SPECs\n\n- SPEC-001")
	report, err := buildFinalVerifyReport([]model.Artifact{spec, verify}, "https://github.com/o/r/issues/1", finalVerifyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if report.OK {
		t.Fatal("VERIFY whose only \"test\" is a substring of another word must fail final verify")
	}
	if !strings.Contains(strings.Join(report.Errors, "\n"), "test evidence") {
		t.Fatalf("expected test-evidence error: %v", report.Errors)
	}
}

func TestFinalVerifyBindsPersistedProcessSelectionToExactImplementIssue(t *testing.T) {
	artifacts, currentID, foreignID := persistedCrossIssueProcessReplacement(t)
	report, err := buildFinalVerifyReport(artifacts, "https://github.com/o/r/issues/1", finalVerifyOptions{ImplementIssue: 3})
	if err != nil {
		t.Fatal(err)
	}
	var currentBlocked bool
	for _, diagnostic := range report.Gate.Diagnostics {
		if diagnostic.Artifact.ID == foreignID && diagnostic.Code != gates.CodeArtifactNoncanonical &&
			diagnostic.Code != gates.CodeTraceabilityInvalid {
			t.Fatalf("foreign PROCESS became final verify authority: %+v", report.Gate.Diagnostics)
		}
		if diagnostic.Code == gates.CodeProcessNotDone && diagnostic.Artifact.ID == currentID {
			currentBlocked = true
		}
	}
	if !currentBlocked {
		t.Fatalf("cross-issue replacement removed the exact Implement PROCESS from final verify: %+v", report.Gate.Diagnostics)
	}
	for _, evidence := range report.ProcessEvidence {
		if evidence.ProcessID == foreignID {
			t.Fatalf("foreign PROCESS re-entered final verify evidence: %+v", report.ProcessEvidence)
		}
	}
	if _, ok := report.SpecCoverage["SPEC-001"]; !ok {
		t.Fatalf("Proposal SPEC disappeared from final verify projection: %+v", report.SpecCoverage)
	}
	if !strings.Contains(strings.Join(report.Traceability.Errors, "\n"), foreignID) {
		t.Fatalf("full cross-issue traceability diagnostics were dropped: %+v", report.Traceability)
	}
	if !canonicalDiagnosticsContain(report.Noncanonical, foreignID) {
		t.Fatalf("full cross-issue canonical diagnostics were dropped: %+v", report.Noncanonical)
	}
}

func linkArtifacts(t *testing.T, from, to *model.Artifact) {
	t.Helper()
	fromBody, changed, err := model.AddRelatedCommentLink(from.Comment.Body, to.URL)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatalf("expected %s -> %s link to change body", from.Comment.ID, to.Comment.ID)
	}
	toBody, changed, err := model.AddRelatedCommentLink(to.Comment.Body, from.URL)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatalf("expected %s -> %s link to change body", to.Comment.ID, from.Comment.ID)
	}
	from.Comment = model.ParseTypedComment(fromBody)
	to.Comment = model.ParseTypedComment(toBody)
}

func canonicalizeVerificationFixture(t *testing.T, verify *model.Artifact, process model.Artifact, specs ...model.Artifact) {
	t.Helper()
	links := map[string][]string{}
	for name, values := range verify.Comment.Links {
		links[name] = append([]string(nil), values...)
	}
	appendLink := func(url string) {
		if !linksContainURL(links[relationships.RelatedCommentsField], url) {
			links[relationships.RelatedCommentsField] = append(links[relationships.RelatedCommentsField], url)
		}
	}
	appendLink(process.URL)
	specIDs := make([]string, 0, len(specs))
	for _, spec := range specs {
		appendLink(spec.URL)
		specIDs = append(specIDs, spec.Comment.ID)
	}
	sort.Strings(specIDs)
	logical := verify.Comment.Body
	if _, after, found := strings.Cut(logical, "\n\n"); found {
		logical = after
	}
	logical = strings.TrimSpace(logical)
	if !strings.Contains(logical, "### Covered PROCESSes") {
		logical += "\n\n### Covered PROCESSes\n\n- " + process.Comment.ID
	}
	if !strings.Contains(logical, "### Covered SPECs") {
		logical += "\n\n### Covered SPECs\n\n- " + strings.Join(specIDs, "\n- ")
	}
	agent := verify.Comment.Agent
	if strings.TrimSpace(agent) == "" {
		agent = "Verifier"
	}
	body, err := model.EnsureTypedBody("VERIFY", verify.Comment.ID, logical, model.BodyOptions{
		Agent: agent, SubjectRevision: verify.Comment.SubjectRevision,
		Status: verify.Comment.Status, Scope: verify.Comment.Scope, Links: links,
	})
	if err != nil {
		t.Fatal(err)
	}
	verify.Comment = model.ParseTypedComment(body)
	if verify.Comment.Agent == "" || len(verify.Comment.Errors) > 0 {
		t.Fatalf("canonical VERIFY fixture is invalid: %+v", verify.Comment)
	}
}

func typedArtifact(t *testing.T, issue int, typ, id, status, content string) model.Artifact {
	t.Helper()
	return typedArtifactWithAgent(t, issue, typ, id, status, "", content)
}

func typedArtifactWithAgent(t *testing.T, issue int, typ, id, status, agent, content string) model.Artifact {
	t.Helper()
	body, err := model.EnsureTypedBody(typ, id, content, model.BodyOptions{Status: status, Agent: agent})
	if err != nil {
		t.Fatal(err)
	}
	return model.Artifact{
		Issue:   issue,
		URL:     fmt.Sprintf("https://github.com/o/r/issues/%d#issuecomment-%s", issue, id),
		Comment: model.ParseTypedComment(body),
	}
}
