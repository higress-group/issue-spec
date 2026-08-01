package commands

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/higress-group/issue-spec/internal/assignment"
	"github.com/higress-group/issue-spec/internal/codereview"
	coreevidence "github.com/higress-group/issue-spec/internal/evidence"
	"github.com/higress-group/issue-spec/internal/model"
	"github.com/higress-group/issue-spec/internal/processworkspace"
)

func TestReviewReceiptBindingAndImmutableProjection(t *testing.T) {
	receipt := testSealedReviewReceipt(t, assignment.ReviewApprove, nil)
	sealed := testReviewAssignment(t, receipt.SubjectRevision)
	binding := &processworkspace.AssignmentBinding{SchemaVersion: assignment.AssignmentSchemaVersion,
		AssignmentID: receipt.AssignmentID, Digest: receipt.AssignmentDigest, Role: assignment.RoleReview,
		SubjectRevision: receipt.SubjectRevision, Generation: receipt.AssignmentGeneration}
	if err := validateReviewReceiptBinding(receipt, sealed, binding); err != nil {
		t.Fatal(err)
	}
	body := "canonical REVIEW\n"
	projected := acceptedReviewReceiptFrom(receipt, sealed.ProcessID)
	stamped, changed, err := stampAcceptedReviewReceipt(body, projected)
	if err != nil || !changed {
		t.Fatalf("changed=%t err=%v", changed, err)
	}
	parsed, found, err := parseAcceptedReviewReceipt(stamped)
	if err != nil || !found || parsed.ReceiptDigest != receipt.ReceiptDigest || parsed.Verdict != assignment.ReviewApprove ||
		parsed.AssignmentProcessID != sealed.ProcessID || parsed.CarrierVersion != 2 || !parsed.TestsAvailable ||
		len(parsed.Tests) != 0 || !strings.Contains(stamped, acceptedReviewReceiptStart) ||
		!strings.Contains(stamped, `"tests":[]`) {
		t.Fatalf("parsed=%+v found=%t err=%v", parsed, found, err)
	}
	if retry, changed, err := stampAcceptedReviewReceipt(stamped, projected); err != nil || changed || retry != stamped {
		t.Fatalf("retry changed=%t err=%v", changed, err)
	}
	other := projected
	other.ReceiptID = "receipt-review-other"
	if _, _, err := stampAcceptedReviewReceipt(stamped, other); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("conflicting accepted receipt error=%v", err)
	}
}

func TestAcceptedReviewReceiptDualVersionCompatibility(t *testing.T) {
	receipt := testSealedReviewReceipt(t, assignment.ReviewApprove, nil)
	v1 := acceptedReviewReceiptV1{ReceiptID: receipt.ID, ReceiptDigest: receipt.ReceiptDigest,
		AssignmentID: receipt.AssignmentID, AssignmentDigest: receipt.AssignmentDigest,
		AssignmentGeneration: receipt.AssignmentGeneration, SubjectRevision: receipt.SubjectRevision,
		Verdict: receipt.Review.Verdict, Provenance: receipt.Provenance}
	raw, err := json.Marshal(v1)
	if err != nil {
		t.Fatal(err)
	}
	body := "canonical REVIEW\n\n" + acceptedReviewReceiptV1Start + "\n" + string(raw) + "\n" + acceptedReviewReceiptEnd + "\n"
	parsed, found, err := parseAcceptedReviewReceipt(body)
	if err != nil || !found || parsed.CarrierVersion != 1 || parsed.TestsAvailable ||
		parsed.AssignmentProcessID != "" || parsed.Tests != nil || parsed.ReceiptDigest != receipt.ReceiptDigest {
		t.Fatalf("historical authority=%+v found=%t err=%v", parsed, found, err)
	}
	if _, _, err := stampAcceptedReviewReceipt(body, acceptedReviewReceiptFrom(receipt, "PROCESS-101")); err == nil ||
		!strings.Contains(err.Error(), "immutable") {
		t.Fatalf("historical carrier was upgraded in place: %v", err)
	}

	withTests := strings.Replace(string(raw), `,"provenance"`, `,"tests":[],"provenance"`, 1)
	if _, found, err := parseAcceptedReviewReceipt(acceptedReviewReceiptV1Start + "\n" + withTests + "\n" + acceptedReviewReceiptEnd); !found || err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("version 1 fabricated tests: found=%t err=%v", found, err)
	}
}

func TestAcceptedReviewReceiptParserRejectsVersionAndFramingDrift(t *testing.T) {
	receipt := testSealedReviewReceipt(t, assignment.ReviewApprove, nil)
	projected := acceptedReviewReceiptFrom(receipt, "PROCESS-101")
	body, _, err := stampAcceptedReviewReceipt("canonical REVIEW\n", projected)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(acceptedReviewReceiptV2From(projected))
	if err != nil {
		t.Fatal(err)
	}
	block := acceptedReviewReceiptStart + "\n" + string(raw) + "\n" + acceptedReviewReceiptEnd
	v1Raw, err := json.Marshal(acceptedReviewReceiptV1{ReceiptID: receipt.ID, ReceiptDigest: receipt.ReceiptDigest,
		AssignmentID: receipt.AssignmentID, AssignmentDigest: receipt.AssignmentDigest,
		AssignmentGeneration: receipt.AssignmentGeneration, SubjectRevision: receipt.SubjectRevision,
		Verdict: receipt.Review.Verdict, Provenance: receipt.Provenance})
	if err != nil {
		t.Fatal(err)
	}
	v1Block := acceptedReviewReceiptV1Start + "\n" + string(v1Raw) + "\n" + acceptedReviewReceiptEnd
	for name, malformed := range map[string]string{
		"unknown version":  strings.Replace(body, "version=2", "version=3", 1),
		"mixed versions":   body + "\n" + v1Block,
		"duplicate marker": body + "\n" + block,
		"unknown field": strings.Replace(body, `,"provenance"`,
			`,"unvalidated":true,"provenance"`, 1),
		"trailing json": strings.Replace(body, "\n"+acceptedReviewReceiptEnd,
			"{}\n"+acceptedReviewReceiptEnd, 1),
		"noncanonical json": strings.Replace(body, `,"receipt_digest"`, `, "receipt_digest"`, 1),
		"missing tests":     strings.Replace(body, `,"tests":[]`, "", 1),
		"null tests":        strings.Replace(body, `"tests":[]`, `"tests":null`, 1),
		"start framing":     strings.Replace(body, acceptedReviewReceiptStart+"\n", acceptedReviewReceiptStart, 1),
		"inline marker":     strings.Replace(body, "\n\n"+acceptedReviewReceiptStart, " inline "+acceptedReviewReceiptStart, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, found, parseErr := parseAcceptedReviewReceipt(malformed); !found || parseErr == nil {
				t.Fatalf("found=%t error=%v\n%s", found, parseErr, malformed)
			}
		})
	}
}

func TestAcceptedReviewReceiptRejectsTamperedTestAndProvenance(t *testing.T) {
	subject := strings.Repeat("b", 40)
	bound := assignment.TestSelector{ID: "durable", Command: "issue-spec durable-spec check --repo o/r --proposal 9 --root . --json",
		RevisionBinding: &assignment.RevisionBinding{Source: assignment.RevisionBindingSourceSubjectRevision,
			Argument: assignment.RevisionBindingArgumentSubject}}
	literal := assignment.TestSelector{ID: "unit", Command: "go test ./internal/commands"}
	sealed := testReviewAssignment(t, subject)
	sealed.Review.RequiredTests = []assignment.TestSelector{bound, literal}
	receipt := testSealedReviewReceiptForAssignment(t, sealed, []assignment.TestResult{
		resolvedCommandTestResult(t, bound, subject),
		{ID: literal.ID, Command: literal.Command, Outcome: assignment.TestPassed, Assurance: assignment.AssuranceSelfReported},
	})
	base := acceptedReviewReceiptFrom(receipt, sealed.ProcessID)
	for name, mutate := range map[string]func(*acceptedReviewReceipt){
		"receipt id":     func(value *acceptedReviewReceipt) { value.ReceiptID = "bad id" },
		"receipt digest": func(value *acceptedReviewReceipt) { value.ReceiptDigest = strings.Repeat("A", 64) },
		"process id":     func(value *acceptedReviewReceipt) { value.AssignmentProcessID = "PROCESS-other" },
		"assignment id":  func(value *acceptedReviewReceipt) { value.AssignmentID = "bad id" },
		"assignment digest": func(value *acceptedReviewReceipt) {
			value.AssignmentDigest = strings.Repeat("g", 64)
		},
		"subject framing": func(value *acceptedReviewReceipt) { value.SubjectRevision += " " },
		"verdict findings": func(value *acceptedReviewReceipt) {
			value.FindingIDs = []string{"FINDING-101"}
		},
		"failed":  func(value *acceptedReviewReceipt) { value.Tests[0].Outcome = assignment.TestFailed },
		"skipped": func(value *acceptedReviewReceipt) { value.Tests[0].Outcome = assignment.TestSkipped },
		"assurance": func(value *acceptedReviewReceipt) {
			value.Tests[0].Assurance = assignment.AssuranceProviderOwned
		},
		"selector": func(value *acceptedReviewReceipt) { value.Tests[0].AssignedSelector.Command += " --tampered" },
		"revision": func(value *acceptedReviewReceipt) { value.Tests[0].ResolvedRevision = strings.Repeat("c", 40) },
		"command":  func(value *acceptedReviewReceipt) { value.Tests[0].Command += " --tampered" },
		"duplicate": func(value *acceptedReviewReceipt) {
			value.Tests = append(value.Tests, value.Tests[0])
		},
		"unsorted": func(value *acceptedReviewReceipt) { value.Tests[0], value.Tests[1] = value.Tests[1], value.Tests[0] },
		"provenance route": func(value *acceptedReviewReceipt) {
			value.Provenance.Route = assignment.RouteUnverifiedImport
		},
		"provenance identity": func(value *acceptedReviewReceipt) { value.Provenance.Subject = "Another Reviewer" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := base
			candidate.Tests = append([]acceptedReviewTest(nil), base.Tests...)
			for index := range candidate.Tests {
				if candidate.Tests[index].AssignedSelector != nil {
					selector := cloneFinalTestSelector(*candidate.Tests[index].AssignedSelector)
					candidate.Tests[index].AssignedSelector = &selector
				}
			}
			mutate(&candidate)
			raw, marshalErr := json.Marshal(acceptedReviewReceiptV2From(candidate))
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			carrier := acceptedReviewReceiptStart + "\n" + string(raw) + "\n" + acceptedReviewReceiptEnd
			if _, found, parseErr := parseAcceptedReviewReceipt(carrier); !found || parseErr == nil {
				t.Fatalf("tampered carrier was accepted: found=%t error=%v", found, parseErr)
			}
		})
	}
}

func TestAcceptedReviewReceiptProjectionPreservesExactTests(t *testing.T) {
	subject := strings.Repeat("b", 40)
	bound := assignment.TestSelector{ID: "durable", Command: "issue-spec durable-spec check --repo o/r --proposal 9 --root . --json",
		RevisionBinding: &assignment.RevisionBinding{Source: assignment.RevisionBindingSourceSubjectRevision,
			Argument: assignment.RevisionBindingArgumentSubject}}
	literal := assignment.TestSelector{ID: "unit", Command: "go test ./internal/commands"}
	sealed := testReviewAssignment(t, subject)
	sealed.Review.RequiredTests = []assignment.TestSelector{literal, bound}
	receipt := testSealedReviewReceiptForAssignment(t, sealed, []assignment.TestResult{
		{ID: literal.ID, Command: literal.Command, Outcome: assignment.TestPassed, Assurance: assignment.AssuranceSelfReported},
		resolvedCommandTestResult(t, bound, subject),
	})

	projected := acceptedReviewReceiptFrom(receipt, sealed.ProcessID)
	resolved, err := assignment.ResolveTestSelector(bound, subject)
	if err != nil {
		t.Fatal(err)
	}
	if len(projected.Tests) != 2 || projected.Tests[0].ID != bound.ID || projected.Tests[1].ID != literal.ID ||
		projected.AssignmentProcessID != sealed.ProcessID || projected.CarrierVersion != 2 || !projected.TestsAvailable ||
		projected.Tests[0].AssignedSelector == nil || projected.Tests[0].ResolvedRevision != subject ||
		projected.Tests[0].Command != resolved.Command || projected.Tests[0].Outcome != assignment.TestPassed ||
		projected.Tests[0].Assurance != assignment.AssuranceSelfReported {
		t.Fatalf("compact review tests lost exact receipt identity: %+v", projected.Tests)
	}
	var receiptBoundSelector *assignment.TestSelector
	for _, test := range receipt.Tests {
		if test.ID == bound.ID {
			receiptBoundSelector = test.AssignedSelector
		}
	}
	if receiptBoundSelector == nil || projected.Tests[0].AssignedSelector == receiptBoundSelector {
		t.Fatal("compact review projection retained a mutable selector pointer")
	}
	body, changed, err := stampAcceptedReviewReceipt("canonical REVIEW\n", projected)
	if err != nil || !changed {
		t.Fatalf("stamp changed=%t err=%v", changed, err)
	}
	parsed, found, err := parseAcceptedReviewReceipt(body)
	if err != nil || !found || len(parsed.Tests) != 2 || parsed.Tests[0].ResolvedRevision != subject {
		t.Fatalf("parsed=%+v found=%t err=%v", parsed, found, err)
	}
}

func TestReviewReceiptBindingRejectsAuthorityDrift(t *testing.T) {
	receipt := testSealedReviewReceipt(t, assignment.ReviewApprove, nil)
	sealed := testReviewAssignment(t, receipt.SubjectRevision)
	valid := processworkspace.AssignmentBinding{SchemaVersion: assignment.AssignmentSchemaVersion,
		AssignmentID: receipt.AssignmentID, Digest: receipt.AssignmentDigest, Role: assignment.RoleReview,
		SubjectRevision: receipt.SubjectRevision, Generation: receipt.AssignmentGeneration}
	tests := map[string]func(*assignment.Receipt, *processworkspace.AssignmentBinding){
		"generation": func(_ *assignment.Receipt, b *processworkspace.AssignmentBinding) { b.Generation++ },
		"revision":   func(_ *assignment.Receipt, b *processworkspace.AssignmentBinding) { b.SubjectRevision = "head-old" },
		"coordinator": func(r *assignment.Receipt, _ *processworkspace.AssignmentBinding) {
			r.Provenance.Writer, r.Provenance.Subject = "Coordinator", "Coordinator"
		},
		"impersonated": func(r *assignment.Receipt, _ *processworkspace.AssignmentBinding) {
			r.Provenance.Subject = "Another Reviewer"
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			candidate, binding := receipt, valid
			mutate(&candidate, &binding)
			if err := validateReviewReceiptBinding(candidate, sealed, &binding); err == nil {
				t.Fatal("authority drift was accepted")
			}
		})
	}
}

func TestReviewReceiptBindingRejectsPreD14StoredAssignment(t *testing.T) {
	receipt := testSealedReviewReceipt(t, assignment.ReviewApprove, nil)
	legacy := testReviewAssignment(t, receipt.SubjectRevision)
	legacy.DesignContext = nil
	binding := &processworkspace.AssignmentBinding{SchemaVersion: assignment.AssignmentSchemaVersion,
		AssignmentID: receipt.AssignmentID, Digest: receipt.AssignmentDigest, Role: assignment.RoleReview,
		SubjectRevision: receipt.SubjectRevision, Generation: receipt.AssignmentGeneration}
	if err := validateReviewReceiptBinding(receipt, legacy, binding); err == nil || !strings.Contains(err.Error(), "design_context") {
		t.Fatalf("new role submission accepted pre-D14 assignment: %v", err)
	}
}

func TestReviewReceiptBindingMatchesNormalizedExactDiffAuthors(t *testing.T) {
	sealed := testReviewAssignment(t, "head-abc")
	receipt := testSealedReviewReceipt(t, assignment.ReviewApprove, nil)
	binding := &processworkspace.AssignmentBinding{SchemaVersion: assignment.AssignmentSchemaVersion,
		AssignmentID: receipt.AssignmentID, Digest: receipt.AssignmentDigest, Role: assignment.RoleReview,
		SubjectRevision: receipt.SubjectRevision, Generation: receipt.AssignmentGeneration}
	for _, identity := range []string{"Independent Reviewer", "independent@example.com"} {
		candidate := receipt
		candidate.Provenance.Writer, candidate.Provenance.Subject = identity, identity
		candidate.ReceiptDigest = ""
		candidate, err := assignment.SealReceipt(candidate)
		if err != nil {
			t.Fatal(err)
		}
		if err := validateReviewReceiptBinding(candidate, sealed, binding); err != nil {
			t.Fatalf("identity %q: %v", identity, err)
		}
	}
	for _, identity := range []string{"CODE AUTHOR", "AUTHOR@EXAMPLE.COM", "Code Author <author@example.com>"} {
		candidate := receipt
		candidate.Provenance.Writer, candidate.Provenance.Subject = identity, identity
		candidate.ReceiptDigest = ""
		candidate, err := assignment.SealReceipt(candidate)
		if err != nil {
			t.Fatal(err)
		}
		if err := validateReviewReceiptBinding(candidate, sealed, binding); err == nil || !strings.Contains(err.Error(), "must not match") {
			t.Fatalf("code-author identity %q error=%v", identity, err)
		}
	}
}

func TestReviewReceiptBindingRejectsFindingOutsideSealedSpecs(t *testing.T) {
	finding := assignment.Finding{ID: "FINDING-101", SpecID: "SPEC-003", OwnerProcessID: "PROCESS-102",
		Path: "internal/foo.go", Side: "RIGHT", Line: 2, Severity: "P1", Message: "Wrong sealed SPEC."}
	receipt := testSealedReviewReceipt(t, assignment.ReviewChangesRequested, []assignment.Finding{finding})
	sealed := testReviewAssignment(t, receipt.SubjectRevision)
	binding := &processworkspace.AssignmentBinding{SchemaVersion: assignment.AssignmentSchemaVersion,
		AssignmentID: receipt.AssignmentID, Digest: receipt.AssignmentDigest, Role: assignment.RoleReview,
		SubjectRevision: receipt.SubjectRevision, Generation: receipt.AssignmentGeneration}
	if err := validateReviewReceiptBinding(receipt, sealed, binding); err == nil || !strings.Contains(err.Error(), "sealed assignment scenarios") {
		t.Fatalf("error=%v", err)
	}
}

func TestReviewReceiptBindingRequiresExactLiteralAndSubjectBoundTests(t *testing.T) {
	subject := strings.Repeat("b", 40)
	bound := assignment.TestSelector{ID: "durable", Command: "issue-spec durable-spec check --repo o/r --proposal 9 --root . --json",
		RevisionBinding: &assignment.RevisionBinding{Source: assignment.RevisionBindingSourceSubjectRevision, Argument: assignment.RevisionBindingArgumentSubject}}
	literal := assignment.TestSelector{ID: "unit", Command: "go test ./internal/commands"}
	sealed := testReviewAssignment(t, subject)
	sealed.Review.RequiredTests = []assignment.TestSelector{bound, literal}
	if err := sealed.Validate(); err != nil {
		t.Fatal(err)
	}
	receipt := testSealedReviewReceiptForAssignment(t, sealed, []assignment.TestResult{
		resolvedCommandTestResult(t, bound, subject),
		{ID: literal.ID, Command: literal.Command, Outcome: assignment.TestPassed, Assurance: assignment.AssuranceSelfReported},
	})
	binding := &processworkspace.AssignmentBinding{SchemaVersion: assignment.AssignmentSchemaVersion,
		AssignmentID: receipt.AssignmentID, Digest: receipt.AssignmentDigest, Role: assignment.RoleReview,
		SubjectRevision: subject, Generation: receipt.AssignmentGeneration}
	if err := validateReviewReceiptBinding(receipt, sealed, binding); err != nil {
		t.Fatal(err)
	}

	for name, mutate := range map[string]func(*assignment.Receipt){
		"missing": func(value *assignment.Receipt) { value.Tests = value.Tests[1:] },
		"failed":  func(value *assignment.Receipt) { value.Tests[0].Outcome = assignment.TestFailed },
		"skipped": func(value *assignment.Receipt) { value.Tests[0].Outcome = assignment.TestSkipped },
		"duplicate": func(value *assignment.Receipt) {
			value.Tests = []assignment.TestResult{value.Tests[0], value.Tests[0]}
		},
		"changed command": func(value *assignment.Receipt) { value.Tests[0].Command += " --tampered" },
		"changed revision": func(value *assignment.Receipt) {
			value.Tests[0].ResolvedRevision = strings.Repeat("c", 40)
		},
		"changed assurance": func(value *assignment.Receipt) {
			value.Tests[0].Assurance = assignment.AssuranceProviderOwned
		},
		"changed selector": func(value *assignment.Receipt) {
			changed := bound
			changed.Command += " --changed"
			value.Tests[0] = resolvedCommandTestResult(t, changed, subject)
		},
		"extra": func(value *assignment.Receipt) {
			value.Tests = append(value.Tests, assignment.TestResult{ID: "extra", Command: "go test ./extra",
				Outcome: assignment.TestPassed, Assurance: assignment.AssuranceSelfReported})
		},
		"provenance": func(value *assignment.Receipt) { value.Provenance.Subject = "Another Reviewer" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := receipt
			candidate.Tests = append([]assignment.TestResult(nil), receipt.Tests...)
			for index := range candidate.Tests {
				if candidate.Tests[index].AssignedSelector != nil {
					selector := cloneFinalTestSelector(*candidate.Tests[index].AssignedSelector)
					candidate.Tests[index].AssignedSelector = &selector
				}
			}
			mutate(&candidate)
			candidate.ReceiptDigest = ""
			candidate, sealErr := assignment.SealReceipt(candidate)
			if sealErr == nil && validateReviewReceiptBinding(candidate, sealed, binding) == nil {
				t.Fatal("accepted mismatched review test coverage")
			}
		})
	}
}

func testSealedReviewReceipt(t *testing.T, verdict assignment.ReviewVerdict, findings []assignment.Finding) assignment.Receipt {
	t.Helper()
	sealed := testReviewAssignment(t, "head-abc")
	return sealReviewReceiptForAssignment(t, sealed, verdict, findings, nil)
}

func testSealedReviewReceiptForAssignment(t *testing.T, sealed assignment.Assignment, tests []assignment.TestResult) assignment.Receipt {
	t.Helper()
	return sealReviewReceiptForAssignment(t, sealed, assignment.ReviewApprove, nil, tests)
}

func sealReviewReceiptForAssignment(t *testing.T, sealed assignment.Assignment, verdict assignment.ReviewVerdict,
	findings []assignment.Finding, tests []assignment.TestResult) assignment.Receipt {
	t.Helper()
	digest, err := assignment.AssignmentDigest(sealed)
	if err != nil {
		t.Fatal(err)
	}
	value := assignment.Receipt{SchemaVersion: assignment.ReceiptSchemaVersion, ID: "receipt-review-1",
		AssignmentID: sealed.ID, AssignmentDigest: digest, AssignmentGeneration: 1,
		Role: assignment.RoleReview, ResultSchemaVersion: assignment.ReceiptSchemaVersion, SubjectRevision: sealed.SubjectRevision, Tests: tests,
		Provenance: assignment.Provenance{Route: assignment.RouteRoleOwned, Assurance: assignment.AssuranceSelfReported,
			Writer: "Independent Reviewer", Subject: "Independent Reviewer", Source: "review-submit"},
		Review: &assignment.ReviewResult{Verdict: verdict, Findings: findings}}
	result, err := assignment.SealReceipt(value)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func resolvedCommandTestResult(t *testing.T, selector assignment.TestSelector, revision string) assignment.TestResult {
	t.Helper()
	resolved, err := assignment.ResolveTestSelector(selector, revision)
	if err != nil {
		t.Fatal(err)
	}
	assigned := resolved.AssignedSelector
	return assignment.TestResult{ID: selector.ID, Command: resolved.Command, AssignedSelector: &assigned,
		ResolvedRevision: resolved.ResolvedRevision, Outcome: assignment.TestPassed, Assurance: assignment.AssuranceSelfReported}
}

func testReviewAssignment(t *testing.T, subject string) assignment.Assignment {
	t.Helper()
	value := assignment.Assignment{SchemaVersion: assignment.AssignmentSchemaVersion, ID: "assignment-review-1",
		Role: assignment.RoleReview, Repository: "o/r", Issue: 9, ProcessID: "PROCESS-101", SubjectRevision: subject,
		Scenarios:           []assignment.ScenarioRef{{SpecID: "SPEC-002", Scenario: "exact review"}},
		DesignContext:       workspaceDesignContext(),
		Policy:              assignment.Policy{RequireExactRevision: true, MaxResultItems: 64},
		ResultSchemaVersion: assignment.ReceiptSchemaVersion,
		Review: &assignment.ReviewPayload{SnapshotRevision: subject, DiffBaseRevision: strings.Repeat("a", 40),
			Authors: []string{"Code Author <author@example.com>"}, Scope: []string{"internal/**"}}}
	if err := value.Validate(); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestValidateExternalReviewCompletionControlledTime(t *testing.T) {
	now := time.Date(2026, 7, 17, 8, 0, 0, 0, time.UTC)
	target := testReviewCompletionTarget()
	tests := []struct {
		name         string
		synchronized time.Time
		policy       ReviewCompletionPolicy
		want         string
	}{
		{name: "fresh", synchronized: now.Add(-30 * time.Minute), policy: ReviewCompletionPolicy{Required: true, Freshness: time.Hour}},
		{name: "freshness boundary", synchronized: now.Add(-time.Hour), policy: ReviewCompletionPolicy{Required: true, Freshness: time.Hour}},
		{name: "expired", synchronized: now.Add(-time.Hour - time.Nanosecond), policy: ReviewCompletionPolicy{Required: true, Freshness: time.Hour}, want: "older"},
		{name: "small clock skew", synchronized: now.Add(time.Minute), policy: ReviewCompletionPolicy{Required: true}},
		{name: "future without freshness", synchronized: now.Add(time.Minute + time.Nanosecond), policy: ReviewCompletionPolicy{Required: true}, want: "future"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			completion := completionForTarget(target, test.synchronized)
			review := reviewWithCompletion(t, completion, target.SubjectRevision, "done")
			err := validateExternalReviewCompletionAt(review, target, test.policy, now)
			if test.want == "" && err != nil {
				t.Fatal(err)
			}
			if test.want != "" && (err == nil || !strings.Contains(err.Error(), test.want)) {
				t.Fatalf("error=%v, want %q", err, test.want)
			}
		})
	}
}

func TestValidateExternalReviewCompletionStrictArtifactMatrix(t *testing.T) {
	now := time.Date(2026, 7, 17, 8, 0, 0, 0, time.UTC)
	target := testReviewCompletionTarget()
	valid := completionForTarget(target, now.Add(-time.Minute))
	base := reviewWithCompletion(t, valid, target.SubjectRevision, "done")
	unstamped := reviewWithoutCompletion(t, target.SubjectRevision, "done")
	ordinary := model.ParseTypedComment("generic comment edit")
	nonUTC := valid
	nonUTC.SynchronizedAt = now.In(time.FixedZone("UTC+8", 8*60*60))

	tests := []struct {
		name   string
		review model.TypedComment
		target coreevidence.NativeTarget
		policy ReviewCompletionPolicy
		want   string
	}{
		{name: "canonical", review: base, target: target, policy: ReviewCompletionPolicy{Required: true}},
		{name: "optional unstamped", review: unstamped, target: target},
		{name: "required unstamped", review: unstamped, target: target, policy: ReviewCompletionPolicy{Required: true}, want: "required"},
		{name: "generic comment edit", review: ordinary, target: target, policy: ReviewCompletionPolicy{Required: true}, want: "required"},
		{name: "not done", review: reviewWithCompletion(t, valid, target.SubjectRevision, "in-progress"), target: target,
			policy: ReviewCompletionPolicy{Required: true}, want: "done REVIEW"},
		{name: "wrong header revision", review: reviewWithCompletion(t, valid, "head-old", "done"), target: target,
			policy: ReviewCompletionPolicy{Required: true}, want: "revision"},
		{name: "non UTC", review: reviewWithCompletion(t, nonUTC, target.SubjectRevision, "done"), target: target,
			policy: ReviewCompletionPolicy{Required: true}, want: "must be UTC"},
		{name: "negative freshness", review: base, target: target, policy: ReviewCompletionPolicy{Required: true, Freshness: -time.Second}, want: "policy is invalid"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateExternalReviewCompletionAt(test.review, test.target, test.policy, now)
			if test.want == "" && err != nil {
				t.Fatal(err)
			}
			if test.want != "" && (err == nil || !strings.Contains(err.Error(), test.want)) {
				t.Fatalf("error=%v, want %q", err, test.want)
			}
		})
	}
}

func TestValidateExternalReviewCompletionRejectsWrongExactIdentity(t *testing.T) {
	now := time.Date(2026, 7, 17, 8, 0, 0, 0, time.UTC)
	target := testReviewCompletionTarget()
	base := completionForTarget(target, now)
	tests := map[string]func(*externalReviewCompletion){
		"provider":   func(value *externalReviewCompletion) { value.ProviderKey = "other.example" },
		"repository": func(value *externalReviewCompletion) { value.ExternalRepository = "other/widgets" },
		"change":     func(value *externalReviewCompletion) { value.ChangeID = "change-99" },
		"version":    func(value *externalReviewCompletion) { value.ReferenceVersion++ },
		"revision":   func(value *externalReviewCompletion) { value.SubjectRevision = "head-old" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			completion := base
			mutate(&completion)
			review := reviewWithCompletion(t, completion, target.SubjectRevision, "done")
			if err := validateExternalReviewCompletionAt(review, target, ReviewCompletionPolicy{Required: true}, now); err == nil {
				t.Fatal("wrong exact identity was accepted")
			}
		})
	}
}

func TestParseExternalReviewCompletionRejectsMalformedOrNoncanonicalBlock(t *testing.T) {
	now := time.Date(2026, 7, 17, 8, 0, 0, 0, time.UTC)
	target := testReviewCompletionTarget()
	completion := completionForTarget(target, now)
	canonical, err := json.Marshal(completion)
	if err != nil {
		t.Fatal(err)
	}
	base := reviewWithoutCompletion(t, target.SubjectRevision, "done").Body
	validBlock := externalReviewCompletionStart + "\n" + string(canonical) + "\n" + externalReviewCompletionEnd
	extra := strings.TrimSuffix(string(canonical), "}") + `,"approved":true}`
	reordered := `{"synchronized_at":"2026-07-17T08:00:00Z","provider_key":"code.example","external_repository":"acme/widgets-code","change_id":"change-42","reference_version":7,"subject_revision":"head-abc"}`
	tests := map[string]string{
		"extra field":         base + externalReviewCompletionStart + "\n" + extra + "\n" + externalReviewCompletionEnd,
		"reordered fields":    base + externalReviewCompletionStart + "\n" + reordered + "\n" + externalReviewCompletionEnd,
		"unsupported version": base + strings.Replace(validBlock, "version=1", "version=2", 1),
		"duplicate pair":      base + validBlock + "\n" + validBlock,
		"closing before open": base + externalReviewCompletionEnd + "\n" + externalReviewCompletionStart + "\n" + string(canonical) + "\n",
		"payload framing":     base + externalReviewCompletionStart + string(canonical) + externalReviewCompletionEnd,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			if _, found, err := parseExternalReviewCompletion(body); !found || err == nil {
				t.Fatalf("found=%t err=%v", found, err)
			}
		})
	}
}

func testReviewCompletionTarget() coreevidence.NativeTarget {
	return coreevidence.NativeTarget{Reference: codereview.Reference{ProviderKey: "code.example",
		ExternalRepository: "acme/widgets-code", ChangeID: "change-42"}, ReferenceVersion: 7, SubjectRevision: "head-abc"}
}

func completionForTarget(target coreevidence.NativeTarget, synchronized time.Time) externalReviewCompletion {
	return externalReviewCompletion{ProviderKey: target.Reference.ProviderKey,
		ExternalRepository: target.Reference.ExternalRepository, ChangeID: target.Reference.ChangeID,
		ReferenceVersion: target.ReferenceVersion, SubjectRevision: target.SubjectRevision, SynchronizedAt: synchronized}
}

func reviewWithoutCompletion(t *testing.T, revision, status string) model.TypedComment {
	t.Helper()
	body, err := model.EnsureTypedBody("REVIEW", "REVIEW-101", "## Review\n\nProvider review synchronized.", model.BodyOptions{
		Agent: "Review Agent", SubjectRevision: revision, Status: status, Scope: "external review",
	})
	if err != nil {
		t.Fatal(err)
	}
	return model.ParseTypedComment(body)
}

func reviewWithCompletion(t *testing.T, completion externalReviewCompletion, headerRevision, status string) model.TypedComment {
	t.Helper()
	review := reviewWithoutCompletion(t, headerRevision, status)
	raw, err := json.Marshal(completion)
	if err != nil {
		t.Fatal(err)
	}
	review.Body += externalReviewCompletionStart + "\n" + string(raw) + "\n" + externalReviewCompletionEnd + "\n"
	return model.ParseTypedComment(review.Body)
}
