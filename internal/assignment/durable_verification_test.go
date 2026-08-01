package assignment

import (
	"strings"
	"testing"

	"github.com/higress-group/issue-spec/internal/durable"
)

func TestWithDurableCheckAddsExactlyOneStableRevisionBoundSelector(t *testing.T) {
	baseline, subject := strings.Repeat("a", 40), strings.Repeat("b", 40)
	payload := VerificationPayload{SubjectRevision: subject,
		RequiredTests: []TestSelector{{ID: "unit", Command: "go test ./..."}}}
	binding := DurableCheckBinding{Repository: "o/r", Proposal: 308, BaselineRevision: baseline,
		SubjectRevision: subject, RepositoryRoot: "."}
	merged, err := payload.WithDurableCheck(durable.ModeRepository, binding)
	if err != nil {
		t.Fatal(err)
	}
	wantCommand := "issue-spec durable-spec check --repo o/r --proposal 308 --baseline " + baseline +
		" --root . --json"
	want := TestSelector{ID: DurableSpecTestID, Command: wantCommand, RevisionBinding: &RevisionBinding{
		Source: RevisionBindingSourceSubjectRevision, Argument: RevisionBindingArgumentSubject,
	}}
	if len(merged.RequiredTests) != 2 || !TestSelectorIdentityEqual(merged.RequiredTests[0], want) {
		t.Fatalf("required tests=%+v", merged.RequiredTests)
	}
	idempotent, err := merged.WithDurableCheck(durable.ModeRepository, binding)
	if err != nil || len(idempotent.RequiredTests) != 2 || !TestSelectorIdentityEqual(idempotent.RequiredTests[0], merged.RequiredTests[0]) {
		t.Fatalf("idempotent=%+v err=%v", idempotent, err)
	}
	none, err := payload.WithDurableCheck(durable.ModeNone, DurableCheckBinding{})
	if err != nil || len(none.RequiredTests) != 1 || none.RequiredTests[0].ID != "unit" {
		t.Fatalf("mode none=%+v err=%v", none, err)
	}
}

func TestDurableCheckSelectorRejectsUnboundOrConflictingAuthority(t *testing.T) {
	baseline, subject := strings.Repeat("a", 40), strings.Repeat("b", 40)
	valid := DurableCheckBinding{Repository: "o/r", Proposal: 308, BaselineRevision: baseline,
		SubjectRevision: subject, RepositoryRoot: "."}
	tests := []struct {
		name   string
		mutate func(*DurableCheckBinding)
	}{
		{name: "repository", mutate: func(value *DurableCheckBinding) { value.Repository = "invalid" }},
		{name: "proposal", mutate: func(value *DurableCheckBinding) { value.Proposal = 0 }},
		{name: "baseline", mutate: func(value *DurableCheckBinding) { value.BaselineRevision = "main" }},
		{name: "subject", mutate: func(value *DurableCheckBinding) { value.SubjectRevision = "HEAD" }},
		{name: "root", mutate: func(value *DurableCheckBinding) { value.RepositoryRoot = "/machine/path" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := valid
			test.mutate(&candidate)
			if _, err := DurableCheckSelector(durable.ModeRepository, candidate); err == nil {
				t.Fatal("accepted invalid durable binding")
			}
		})
	}
	payload := VerificationPayload{SubjectRevision: strings.Repeat("c", 40),
		RequiredTests: []TestSelector{{ID: "unit", Command: "go test ./..."}}}
	if _, err := payload.WithDurableCheck(durable.ModeRepository, valid); err == nil || !strings.Contains(err.Error(), "must equal") {
		t.Fatalf("subject mismatch error=%v", err)
	}
	payload.SubjectRevision = subject
	payload.RequiredTests = append(payload.RequiredTests, TestSelector{ID: DurableSpecTestID, Command: "forged"})
	if _, err := payload.WithDurableCheck(durable.ModeRepository, valid); err == nil || !strings.Contains(err.Error(), "conflicting commands") {
		t.Fatalf("selector conflict error=%v", err)
	}
}

func TestDurableVerificationReceiptCoverageRejectsMissingFailedStaleMismatchedAndForgedEvidence(t *testing.T) {
	baseline, subject := strings.Repeat("a", 40), strings.Repeat("b", 40)
	payload := VerificationPayload{SubjectRevision: subject,
		RequiredTests: []TestSelector{{ID: "unit", Command: "go test ./..."}}}
	payload, err := payload.WithDurableCheck(durable.ModeRepository, DurableCheckBinding{Repository: "o/r", Proposal: 308,
		BaselineRevision: baseline, SubjectRevision: subject, RepositoryRoot: "."})
	if err != nil {
		t.Fatal(err)
	}
	tests := []TestResult{
		resolvedTestResult(t, payload.RequiredTests[0], subject),
		{ID: "unit", Command: "go test ./...", Outcome: TestPassed, Assurance: AssuranceSelfReported},
	}
	valid := durableVerificationReceipt(t, subject, tests, "durable projection passed")
	if err := ValidateVerificationReceiptCoverage(payload, valid); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		mutate func(*Receipt)
		want   string
	}{
		{name: "missing", want: "exactly cover", mutate: func(value *Receipt) { value.Tests = value.Tests[1:] }},
		{name: "failed", want: "must pass", mutate: func(value *Receipt) { value.Tests[0].Outcome = TestFailed }},
		{name: "stale", want: "authoritative verification revision", mutate: func(value *Receipt) { value.SubjectRevision = baseline }},
		{name: "mismatched command", want: "deterministic resolved command", mutate: func(value *Receipt) { value.Tests[0].Command += " --forged" }},
		{name: "forged prose", want: "exactly cover", mutate: func(value *Receipt) {
			value.Tests = value.Tests[1:]
			value.Verification.Summary = "issue-spec/durable-spec passed according to prose"
		}},
		{name: "forged assurance", want: "not accepted", mutate: func(value *Receipt) { value.Tests[0].Assurance = AssuranceRuntimeAttested }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			candidate := valid
			candidate.Tests = cloneTestResults(valid.Tests)
			verification := *valid.Verification
			candidate.Verification = &verification
			test.mutate(&candidate)
			sealed, sealErr := SealReceipt(candidate)
			if sealErr != nil {
				if !strings.Contains(sealErr.Error(), test.want) && test.name != "forged assurance" {
					t.Fatalf("seal error=%v", sealErr)
				}
				return
			}
			if err := ValidateVerificationReceiptCoverage(payload, sealed); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("coverage error=%v", err)
			}
		})
	}
}

func durableVerificationReceipt(t *testing.T, subject string, tests []TestResult, summary string) Receipt {
	t.Helper()
	value := Receipt{SchemaVersion: ReceiptSchemaVersion, ID: "receipt-durable-1", AssignmentID: "assignment-durable-1",
		AssignmentDigest: strings.Repeat("c", 64), AssignmentGeneration: 1, Role: RoleVerification,
		ResultSchemaVersion: ReceiptSchemaVersion, SubjectRevision: subject, Tests: append([]TestResult(nil), tests...),
		Provenance: Provenance{Route: RouteRoleOwned, Assurance: AssuranceSelfReported,
			Writer: "Verifier", Subject: "Verifier", Source: "verify-submit"},
		Verification: &VerificationResult{Summary: summary}}
	sealed, err := SealReceipt(value)
	if err != nil {
		t.Fatal(err)
	}
	return sealed
}
