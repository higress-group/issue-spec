package assignment

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

const (
	baseRevision    = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	subjectRevision = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	validDigest     = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

func implementationAssignment() Assignment {
	return Assignment{
		SchemaVersion: AssignmentSchemaVersion,
		ID:            "asg-005-1", Role: RoleImplementation,
		Repository: "higress-group/issue-spec", Issue: 297, ProcessID: "PROCESS-005",
		BaseRevision: baseRevision,
		Scenarios:    []ScenarioRef{{SpecID: "SPEC-005", Scenario: "receipt"}, {SpecID: "SPEC-001", Scenario: "packet"}},
		Dependencies: []string{"PROCESS-004", "PROCESS-003"}, Handoff: "stage 1 reviewed",
		Policy: Policy{RequireExactRevision: true, MaxResultItems: 64}, ResultSchemaVersion: ReceiptSchemaVersion,
		Implementation: &ImplementationPayload{
			Objective: "Define schemas", Branch: "codex/297-p005-assignment-schema",
			WriteOwnership:    []string{"internal/model/typed_comment.go", "internal/assignment/**"},
			SharedTouchpoints: []string{"internal/processworkspace"},
			Commit:            CommitPolicy{RequireSingleCommit: true, RequireDCO: true},
			Generators:        []GeneratorPolicy{{Name: "types", Command: "go generate ./internal/assignment", RequiredOutputs: []string{"internal/assignment/generated.go"}}},
			FocusedTests:      []TestSelector{{ID: "assignment", Command: "go test ./internal/assignment"}},
		},
	}
}

func reviewAssignment() Assignment {
	return Assignment{
		SchemaVersion: AssignmentSchemaVersion,
		ID:            "asg-007-1", Role: RoleReview,
		Repository: "higress-group/issue-spec", Issue: 297, ProcessID: "PROCESS-007",
		SubjectRevision: subjectRevision,
		Scenarios:       []ScenarioRef{{SpecID: "SPEC-002", Scenario: "independent review"}},
		Policy:          Policy{RequireExactRevision: true, MaxResultItems: 64}, ResultSchemaVersion: ReceiptSchemaVersion,
		Review: &ReviewPayload{SnapshotRevision: subjectRevision, DiffBaseRevision: baseRevision, Authors: []string{"Worker"}, Scope: []string{"internal/assignment/**"}, KnownTests: []KnownTestEvidence{{ID: "assignment", Command: "go test ./internal/assignment", Outcome: TestPassed}}},
	}
}

func verificationAssignment() Assignment {
	return Assignment{
		SchemaVersion: AssignmentSchemaVersion,
		ID:            "asg-016-1", Role: RoleVerification,
		Repository: "higress-group/issue-spec", Issue: 297, ProcessID: "PROCESS-016",
		SubjectRevision: subjectRevision,
		Scenarios:       []ScenarioRef{{SpecID: "SPEC-005", Scenario: "verification receipt"}},
		Policy:          Policy{RequireExactRevision: true, MaxResultItems: 64}, ResultSchemaVersion: ReceiptSchemaVersion,
		Verification: &VerificationPayload{SubjectRevision: subjectRevision, RequiredTests: []TestSelector{{ID: "unit", Command: "go test ./..."}}, RequiredChecks: []CheckSelector{{Provider: "github", Name: "test"}}},
	}
}

func TestAssignmentRolePayloadsValidate(t *testing.T) {
	for name, value := range map[string]Assignment{
		"implementation": implementationAssignment(),
		"review":         reviewAssignment(),
		"verification":   verificationAssignment(),
	} {
		t.Run(name, func(t *testing.T) {
			if err := value.Validate(); err != nil {
				t.Fatal(err)
			}
			encoded, err := CanonicalAssignmentJSON(value)
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := ParseAssignmentJSON(encoded)
			if err != nil {
				t.Fatal(err)
			}
			if parsed.Role != value.Role {
				t.Fatalf("role = %q", parsed.Role)
			}
		})
	}
}

func TestAssignmentCanonicalJSONAndDigestGolden(t *testing.T) {
	value := implementationAssignment()
	canonical, err := CanonicalAssignmentJSON(value)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"schema_version":"issue-spec.assignment/v1","assignment_id":"asg-005-1","role":"implementation","repository":"higress-group/issue-spec","issue":297,"process_id":"PROCESS-005","base_revision":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","scenarios":[{"spec_id":"SPEC-001","scenario":"packet"},{"spec_id":"SPEC-005","scenario":"receipt"}],"dependencies":["PROCESS-003","PROCESS-004"],"handoff":"stage 1 reviewed","policy":{"require_exact_revision":true,"max_result_items":64},"result_schema_version":"issue-spec.receipt/v1","implementation":{"objective":"Define schemas","branch":"codex/297-p005-assignment-schema","write_ownership":["internal/assignment/**","internal/model/typed_comment.go"],"shared_touchpoints":["internal/processworkspace"],"commit_policy":{"require_single_commit":true,"require_dco":true},"generators":[{"name":"types","command":"go generate ./internal/assignment","required_outputs":["internal/assignment/generated.go"]}],"focused_tests":[{"id":"assignment","command":"go test ./internal/assignment"}]}}`
	if string(canonical) != want {
		t.Fatalf("canonical JSON mismatch\n got: %s\nwant: %s", canonical, want)
	}
	digest, err := AssignmentDigest(value)
	if err != nil {
		t.Fatal(err)
	}
	if digest != "4f9554765e9f45c6c17af72fc69060e01ddf49a58bc5f0268d5f9ff7db116c4a" {
		t.Fatalf("digest = %q", digest)
	}

	reordered := implementationAssignment()
	reordered.Scenarios[0], reordered.Scenarios[1] = reordered.Scenarios[1], reordered.Scenarios[0]
	reordered.Dependencies[0], reordered.Dependencies[1] = reordered.Dependencies[1], reordered.Dependencies[0]
	reordered.Implementation.WriteOwnership[0], reordered.Implementation.WriteOwnership[1] = reordered.Implementation.WriteOwnership[1], reordered.Implementation.WriteOwnership[0]
	reorderedDigest, err := AssignmentDigest(reordered)
	if err != nil {
		t.Fatal(err)
	}
	if reorderedDigest != digest {
		t.Fatalf("reordered digest = %q, want %q", reorderedDigest, digest)
	}
}

func TestPacketDeliveryMetadataIsOutsideAssignmentDigest(t *testing.T) {
	assignmentValue := implementationAssignment()
	digest, err := AssignmentDigest(assignmentValue)
	if err != nil {
		t.Fatal(err)
	}
	first := Packet{Assignment: assignmentValue, AssignmentDigest: digest, Generation: 1, Delivery: &DeliveryMetadata{WorktreePath: "/private/tmp/one"}}
	second := first
	second.Delivery = &DeliveryMetadata{WorktreePath: "/different/host/two"}
	if err := first.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := second.Validate(); err != nil {
		t.Fatal(err)
	}
	if first.AssignmentDigest != second.AssignmentDigest {
		t.Fatal("delivery path changed assignment digest")
	}
}

func TestAssignmentRejectsUnknownFieldsRoleMismatchAndBounds(t *testing.T) {
	canonical, err := CanonicalAssignmentJSON(implementationAssignment())
	if err != nil {
		t.Fatal(err)
	}
	unknown := bytes.Replace(canonical, []byte(`{"schema_version"`), []byte(`{"unknown":true,"schema_version"`), 1)
	if _, err := ParseAssignmentJSON(unknown); err == nil || !strings.Contains(err.Error(), `unknown field "unknown"`) {
		t.Fatalf("unknown field error = %v", err)
	}

	mismatch := implementationAssignment()
	mismatch.Role = RoleReview
	if err := mismatch.Validate(); err == nil || !strings.Contains(err.Error(), "review role requires review") {
		t.Fatalf("role mismatch error = %v", err)
	}

	overflow := implementationAssignment()
	overflow.Scenarios = make([]ScenarioRef, maxListItems+1)
	for i := range overflow.Scenarios {
		overflow.Scenarios[i] = ScenarioRef{SpecID: "SPEC-001", Scenario: "scenario-" + strings.Repeat("x", i%2)}
	}
	if err := overflow.Validate(); err == nil || !strings.Contains(err.Error(), "exceeds 128 items") {
		t.Fatalf("bounds error = %v", err)
	}
}

func TestProcessInputStrictParsingAndNoSelectorMeansAllScenarios(t *testing.T) {
	input := ProcessInput{Objective: "Implement the typed schema"}
	payload, err := ProcessInputJSON(input)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseProcessInputJSON(payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.ScenarioSelectors) != 0 {
		t.Fatalf("scenario selectors = %v, want all-scenarios sentinel", parsed.ScenarioSelectors)
	}
	if _, err := ParseProcessInputJSON([]byte(`{"objective":"x","prose":"do tests"}`)); err == nil || !strings.Contains(err.Error(), `unknown field "prose"`) {
		t.Fatalf("unknown field error = %v", err)
	}
}

func implementationReceipt() Receipt {
	return Receipt{
		SchemaVersion: ReceiptSchemaVersion, ID: "receipt-005-1",
		AssignmentID: "asg-005-1", AssignmentDigest: validDigest, AssignmentGeneration: 1,
		Role: RoleImplementation, ResultSchemaVersion: ReceiptSchemaVersion,
		BaseRevision: baseRevision, ResultRevision: subjectRevision,
		Tests:          []TestResult{{ID: "unit", Command: "go test ./internal/assignment", Outcome: TestPassed, Assurance: AssuranceSelfReported}},
		Provenance:     Provenance{Route: RouteRoleOwned, Assurance: AssuranceSelfReported, Writer: "Worker", Subject: "Worker", Source: "role-command"},
		Implementation: &ImplementationResult{ChangedPaths: []string{"internal/assignment/types.go"}, Decisions: []string{"keep schema pure"}},
	}
}

func TestReceiptRolePayloadsAndDigest(t *testing.T) {
	values := []Receipt{
		implementationReceipt(),
		{SchemaVersion: ReceiptSchemaVersion, ID: "receipt-007-1", AssignmentID: "asg-007-1", AssignmentDigest: validDigest, AssignmentGeneration: 1, Role: RoleReview, ResultSchemaVersion: ReceiptSchemaVersion, SubjectRevision: subjectRevision, Provenance: Provenance{Route: RouteRoleOwned, Assurance: AssuranceSelfReported, Writer: "Reviewer", Subject: "Reviewer", Source: "review-submit"}, Review: &ReviewResult{Verdict: ReviewApprove}},
		{SchemaVersion: ReceiptSchemaVersion, ID: "receipt-016-1", AssignmentID: "asg-016-1", AssignmentDigest: validDigest, AssignmentGeneration: 1, Role: RoleVerification, ResultSchemaVersion: ReceiptSchemaVersion, SubjectRevision: subjectRevision, Tests: []TestResult{{ID: "unit", Command: "go test ./...", Outcome: TestPassed, Assurance: AssuranceSelfReported}}, Provenance: Provenance{Route: RouteRoleOwned, Assurance: AssuranceSelfReported, Writer: "Verifier", Subject: "Verifier", Source: "verify-submit"}, Verification: &VerificationResult{Summary: "focused tests passed"}},
	}
	for _, value := range values {
		sealed, err := SealReceipt(value)
		if err != nil {
			t.Fatalf("seal %s: %v", value.Role, err)
		}
		if err := sealed.ValidateForAcceptance(); err != nil {
			t.Fatalf("accept %s: %v", value.Role, err)
		}
		payload, err := json.Marshal(sealed)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := ParseReceiptJSON(payload)
		if err != nil {
			t.Fatal(err)
		}
		if parsed.ReceiptDigest != sealed.ReceiptDigest {
			t.Fatalf("receipt digest changed: %s != %s", parsed.ReceiptDigest, sealed.ReceiptDigest)
		}
	}
}

func TestReceiptAssuranceMatrixAndStrictParsing(t *testing.T) {
	runtime := implementationReceipt()
	runtime.Provenance.Assurance = AssuranceRuntimeAttested
	runtime.Tests[0].Assurance = AssuranceRuntimeAttested
	runtime, err := SealReceipt(runtime)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Validate(); err != nil {
		t.Fatalf("reserved assurance should be structurally valid: %v", err)
	}
	if err := runtime.ValidateForAcceptance(); err == nil || !strings.Contains(err.Error(), "not accepted in version 1") {
		t.Fatalf("runtime assurance acceptance error = %v", err)
	}

	unverified := implementationReceipt()
	unverified.Provenance.Route = RouteUnverifiedImport
	unverified, err = SealReceipt(unverified)
	if err != nil {
		t.Fatal(err)
	}
	if err := unverified.ValidateForAcceptance(); err == nil || !strings.Contains(err.Error(), "not accepted in version 1") {
		t.Fatalf("unverified route acceptance error = %v", err)
	}

	provider := implementationReceipt()
	provider.Provenance.Assurance = AssuranceProviderOwned
	provider.Tests[0].Assurance = AssuranceProviderOwned
	provider, err = SealReceipt(provider)
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.ValidateForAcceptance(); err != nil {
		t.Fatalf("provider-owned receipt rejected: %v", err)
	}

	payload, err := json.Marshal(provider)
	if err != nil {
		t.Fatal(err)
	}
	unknown := bytes.Replace(payload, []byte(`{"schema_version"`), []byte(`{"extra":true,"schema_version"`), 1)
	if _, err := ParseReceiptJSON(unknown); err == nil || !strings.Contains(err.Error(), `unknown field "extra"`) {
		t.Fatalf("unknown receipt field error = %v", err)
	}
	provider.ReceiptDigest = strings.Repeat("0", 64)
	if err := provider.Validate(); err == nil || !strings.Contains(err.Error(), "does not match canonical receipt") {
		t.Fatalf("tampered receipt validation error = %v", err)
	}
	tampered, err := json.Marshal(provider)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseReceiptJSON(tampered); err == nil || !strings.Contains(err.Error(), "does not match canonical receipt") {
		t.Fatalf("tampered receipt error = %v", err)
	}
}
