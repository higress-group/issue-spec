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

func assignmentDesignContext() *DesignContext {
	return &DesignContext{
		SourceURL:               "https://github.com/higress-group/issue-spec/issues/296",
		ReadMode:                DesignReadModeCompleteIssueBody,
		Invariant:               "Portable assignments preserve Design authority.",
		ApplicableDecisions:     []string{"D14", "D6"},
		ImplementationDirection: "Carry the coordinator-authored projection without reinterpretation.",
		MustPreserve:            []string{"exact text", "list order"},
		MustNot:                 []string{"summarize", "trust session metadata"},
		MinimumVerification:     []string{"schema validation", "digest coverage"},
		ConflictPolicy:          DesignConflictPolicyAuthoritativeStop,
	}
}

func implementationAssignment() Assignment {
	return Assignment{
		SchemaVersion: AssignmentSchemaVersion,
		ID:            "asg-005-1", Role: RoleImplementation,
		Repository: "higress-group/issue-spec", Issue: 297, ProcessID: "PROCESS-005",
		BaseRevision: baseRevision,
		Scenarios:    []ScenarioRef{{SpecID: "SPEC-005", Scenario: "receipt"}, {SpecID: "SPEC-001", Scenario: "packet"}},
		Dependencies: []string{"PROCESS-004", "PROCESS-003"}, Handoff: "stage 1 reviewed",
		DesignContext: assignmentDesignContext(),
		Policy:        Policy{RequireExactRevision: true, MaxResultItems: 64}, ResultSchemaVersion: ReceiptSchemaVersion,
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
		DesignContext:   assignmentDesignContext(),
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
	want := `{"schema_version":"issue-spec.assignment/v1","assignment_id":"asg-005-1","role":"implementation","repository":"higress-group/issue-spec","issue":297,"process_id":"PROCESS-005","base_revision":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","scenarios":[{"spec_id":"SPEC-001","scenario":"packet"},{"spec_id":"SPEC-005","scenario":"receipt"}],"dependencies":["PROCESS-003","PROCESS-004"],"handoff":"stage 1 reviewed","design_context":{"source_url":"https://github.com/higress-group/issue-spec/issues/296","read_mode":"complete-issue-body","invariant":"Portable assignments preserve Design authority.","applicable_decisions":["D14","D6"],"implementation_direction":"Carry the coordinator-authored projection without reinterpretation.","must_preserve":["exact text","list order"],"must_not":["summarize","trust session metadata"],"minimum_verification":["schema validation","digest coverage"],"conflict_policy":"design-authoritative-stop"},"policy":{"require_exact_revision":true,"max_result_items":64},"result_schema_version":"issue-spec.receipt/v1","implementation":{"objective":"Define schemas","branch":"codex/297-p005-assignment-schema","write_ownership":["internal/assignment/**","internal/model/typed_comment.go"],"shared_touchpoints":["internal/processworkspace"],"commit_policy":{"require_single_commit":true,"require_dco":true},"generators":[{"name":"types","command":"go generate ./internal/assignment","required_outputs":["internal/assignment/generated.go"]}],"focused_tests":[{"id":"assignment","command":"go test ./internal/assignment"}]}}`
	if string(canonical) != want {
		t.Fatalf("canonical JSON mismatch\n got: %s\nwant: %s", canonical, want)
	}
	digest, err := AssignmentDigest(value)
	if err != nil {
		t.Fatal(err)
	}
	if digest != "409667915538033f4d3da79d25e18e601404cc6933d059f3e5a58e1d3f860dd5" {
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

	designReordered := implementationAssignment()
	designReordered.DesignContext.MustPreserve[0], designReordered.DesignContext.MustPreserve[1] = designReordered.DesignContext.MustPreserve[1], designReordered.DesignContext.MustPreserve[0]
	designDigest, err := AssignmentDigest(designReordered)
	if err != nil {
		t.Fatal(err)
	}
	if designDigest == digest {
		t.Fatal("design_context list order was not covered by the assignment digest")
	}
}

func TestAssignmentDesignContextIsRequiredAndPreservedExactly(t *testing.T) {
	missing := implementationAssignment()
	missing.DesignContext = nil
	if err := missing.Validate(); err == nil || !strings.Contains(err.Error(), "design_context") {
		t.Fatalf("missing design context error = %v", err)
	}
	malformed := implementationAssignment()
	malformed.DesignContext.SourceURL = "Design #296"
	if err := malformed.Validate(); err == nil || !strings.Contains(err.Error(), "canonical HTTP(S) issue URL") {
		t.Fatalf("malformed design source error = %v", err)
	}

	verification := verificationAssignment()
	verification.DesignContext = assignmentDesignContext()
	if err := verification.Validate(); err == nil || !strings.Contains(err.Error(), "must not carry") {
		t.Fatalf("verification design context error = %v", err)
	}

	value := implementationAssignment()
	payload, err := CanonicalAssignmentJSON(value)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseAssignmentJSON(payload)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(mustJSON(t, value.DesignContext), mustJSON(t, parsed.DesignContext)) {
		t.Fatalf("design context changed during round trip\n got: %s\nwant: %s", mustJSON(t, parsed.DesignContext), mustJSON(t, value.DesignContext))
	}
}

func TestAssignmentDigestCoversEveryDesignContextProjectionField(t *testing.T) {
	base := implementationAssignment()
	want, err := AssignmentDigest(base)
	if err != nil {
		t.Fatal(err)
	}
	mutations := map[string]func(*DesignContext){
		"source_url":               func(value *DesignContext) { value.SourceURL += "/changed" },
		"invariant":                func(value *DesignContext) { value.Invariant += " Changed." },
		"applicable_decisions":     func(value *DesignContext) { value.ApplicableDecisions[0] += "-changed" },
		"implementation_direction": func(value *DesignContext) { value.ImplementationDirection += " Changed." },
		"must_preserve":            func(value *DesignContext) { value.MustPreserve[0] += " changed" },
		"must_not":                 func(value *DesignContext) { value.MustNot[0] += " changed" },
		"minimum_verification":     func(value *DesignContext) { value.MinimumVerification[0] += " changed" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			value := implementationAssignment()
			mutate(value.DesignContext)
			got, err := AssignmentDigest(value)
			if err != nil {
				t.Fatal(err)
			}
			if got == want {
				t.Fatalf("digest did not cover design_context.%s", name)
			}
		})
	}
}

func TestPreD14AssignmentIsStorageReadableButStrictPathsRejectIt(t *testing.T) {
	for name, legacy := range map[string]Assignment{
		"implementation": implementationAssignment(),
		"review":         reviewAssignment(),
	} {
		t.Run(name, func(t *testing.T) {
			legacy.DesignContext = nil
			if err := legacy.ValidateForStorageRead(); err != nil {
				t.Fatalf("historical storage validation failed: %v", err)
			}
			if err := legacy.Validate(); err == nil || !strings.Contains(err.Error(), "design_context") {
				t.Fatalf("strict validation accepted pre-D14 assignment: %v", err)
			}
			digest, err := AssignmentDigestForStorageRead(legacy)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := AssignmentDigest(legacy); err == nil || !strings.Contains(err.Error(), "design_context") {
				t.Fatalf("strict digest accepted pre-D14 assignment: %v", err)
			}
			payload, err := json.Marshal(legacy)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ParseAssignmentJSON(payload); err == nil || !strings.Contains(err.Error(), "design_context") {
				t.Fatalf("assignment-file parser accepted pre-D14 assignment: %v", err)
			}
			packet := Packet{Assignment: legacy, AssignmentDigest: digest, Generation: 1}
			if err := packet.Validate(); err == nil || !strings.Contains(err.Error(), "design_context") {
				t.Fatalf("packet issuance accepted pre-D14 assignment: %v", err)
			}
		})
	}

	incomplete := implementationAssignment()
	incomplete.DesignContext.Invariant = ""
	if err := incomplete.ValidateForStorageRead(); err == nil || !strings.Contains(err.Error(), "design_context.invariant") {
		t.Fatalf("storage compatibility accepted incomplete present design_context: %v", err)
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return payload
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

func TestAssignmentRejectsDuplicateIdentityKeys(t *testing.T) {
	tests := []struct {
		name  string
		value Assignment
		want  string
	}{
		{
			name: "generator name with different commands",
			value: func() Assignment {
				value := implementationAssignment()
				value.Implementation.Generators = append(value.Implementation.Generators,
					GeneratorPolicy{Name: "types", Command: "go generate ./...", RequiredOutputs: []string{"internal/assignment/other.go"}})
				return value
			}(),
			want: `implementation.generators[1]: duplicate key "types"`,
		},
		{
			name: "test selector id with different commands",
			value: func() Assignment {
				value := implementationAssignment()
				value.Implementation.FocusedTests = append(value.Implementation.FocusedTests,
					TestSelector{ID: "assignment", Command: "go test ./internal/assignment -run TestDigest"})
				return value
			}(),
			want: `implementation.focused_tests[1]: duplicate key "assignment"`,
		},
		{
			name: "known test id with different evidence",
			value: func() Assignment {
				value := reviewAssignment()
				value.Review.KnownTests = append(value.Review.KnownTests,
					KnownTestEvidence{ID: "assignment", Command: "go test ./...", Outcome: TestFailed})
				return value
			}(),
			want: `review.known_tests[1]: duplicate key "assignment"`,
		},
		{
			name: "provider check composite key",
			value: func() Assignment {
				value := verificationAssignment()
				value.Verification.RequiredChecks = append(value.Verification.RequiredChecks,
					CheckSelector{Provider: "github", Name: "test"})
				return value
			}(),
			want: `verification.required_checks[1]: duplicate key "github/test"`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.value.Validate()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestReceiptRejectsDuplicateIdentityKeys(t *testing.T) {
	implementation := implementationReceipt()
	implementation.Tests = append(implementation.Tests,
		TestResult{ID: "unit", Command: "go test ./...", Outcome: TestFailed, Assurance: AssuranceProviderOwned})

	review := Receipt{
		SchemaVersion: ReceiptSchemaVersion, ID: "receipt-007-duplicate",
		AssignmentID: "asg-007-1", AssignmentDigest: validDigest, AssignmentGeneration: 1,
		Role: RoleReview, ResultSchemaVersion: ReceiptSchemaVersion, SubjectRevision: subjectRevision,
		Provenance: Provenance{Route: RouteRoleOwned, Assurance: AssuranceSelfReported, Writer: "Reviewer", Subject: "Reviewer", Source: "review-submit"},
		Review: &ReviewResult{Verdict: ReviewChangesRequested, Findings: []Finding{
			{ID: "FINDING-001", SpecID: "SPEC-001", OwnerProcessID: "PROCESS-002", Path: "internal/assignment/types.go", Side: "RIGHT", Line: 10, Severity: "P2", Message: "first"},
			{ID: "FINDING-001", SpecID: "SPEC-001", OwnerProcessID: "PROCESS-002", Path: "internal/assignment/codec.go", Side: "RIGHT", Line: 20, Severity: "P1", Message: "conflicting duplicate"},
		}},
	}

	verification := Receipt{
		SchemaVersion: ReceiptSchemaVersion, ID: "receipt-016-duplicate",
		AssignmentID: "asg-016-1", AssignmentDigest: validDigest, AssignmentGeneration: 1,
		Role: RoleVerification, ResultSchemaVersion: ReceiptSchemaVersion, SubjectRevision: subjectRevision,
		Provenance: Provenance{Route: RouteRoleOwned, Assurance: AssuranceSelfReported, Writer: "Verifier", Subject: "Verifier", Source: "verify-submit"},
		Verification: &VerificationResult{CheckSelectors: []CheckSelector{
			{Provider: "github", Name: "test"},
			{Provider: "github", Name: "test"},
		}},
	}

	for name, test := range map[string]struct {
		value Receipt
		want  string
	}{
		"test result id with different results": {implementation, `tests[1]: duplicate key "unit"`},
		"finding id with different anchors":     {review, `review.findings[1]: duplicate key "FINDING-001"`},
		"verification check composite key":      {verification, `verification.check_selectors[1]: duplicate key "github/test"`},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := SealReceipt(test.value)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestReviewFindingRequiresSealedSpecAndOwnerProcessIdentity(t *testing.T) {
	base := Receipt{SchemaVersion: ReceiptSchemaVersion, ID: "receipt-review-finding",
		AssignmentID: "assignment-review-1", AssignmentDigest: validDigest, AssignmentGeneration: 1,
		Role: RoleReview, ResultSchemaVersion: ReceiptSchemaVersion, SubjectRevision: subjectRevision,
		Provenance: Provenance{Route: RouteRoleOwned, Assurance: AssuranceSelfReported,
			Writer: "Reviewer", Subject: "Reviewer", Source: "review-submit"},
		Review: &ReviewResult{Verdict: ReviewChangesRequested, Findings: []Finding{{ID: "FINDING-001",
			SpecID: "SPEC-001", OwnerProcessID: "PROCESS-002", Path: "internal/x.go", Side: "RIGHT", Line: 10,
			Severity: "P1", Message: "repair exact identity"}}}}
	for name, mutate := range map[string]func(*Finding){
		"missing spec":  func(f *Finding) { f.SpecID = "" },
		"invalid spec":  func(f *Finding) { f.SpecID = "SPEC-one" },
		"missing owner": func(f *Finding) { f.OwnerProcessID = "" },
		"invalid owner": func(f *Finding) { f.OwnerProcessID = "PROCESS-one" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := base
			candidate.Review = &ReviewResult{Verdict: base.Review.Verdict,
				Findings: append([]Finding(nil), base.Review.Findings...)}
			mutate(&candidate.Review.Findings[0])
			if _, err := SealReceipt(candidate); err == nil {
				t.Fatal("sealed finding identity omission was accepted")
			}
		})
	}
}

func TestDifferentIdentityKeysKeepOrderIndependentDigests(t *testing.T) {
	firstAssignment := implementationAssignment()
	firstAssignment.Implementation.FocusedTests = append(firstAssignment.Implementation.FocusedTests,
		TestSelector{ID: "model", Command: "go test ./internal/model"})
	secondAssignment := firstAssignment
	secondAssignment.Implementation = &ImplementationPayload{}
	*secondAssignment.Implementation = *firstAssignment.Implementation
	secondAssignment.Implementation.FocusedTests = append([]TestSelector(nil), firstAssignment.Implementation.FocusedTests...)
	secondAssignment.Implementation.FocusedTests[0], secondAssignment.Implementation.FocusedTests[1] = secondAssignment.Implementation.FocusedTests[1], secondAssignment.Implementation.FocusedTests[0]
	firstDigest, err := AssignmentDigest(firstAssignment)
	if err != nil {
		t.Fatal(err)
	}
	secondDigest, err := AssignmentDigest(secondAssignment)
	if err != nil {
		t.Fatal(err)
	}
	if firstDigest != secondDigest {
		t.Fatalf("assignment digests differ: %s != %s", firstDigest, secondDigest)
	}

	firstReceipt := implementationReceipt()
	firstReceipt.Tests = append(firstReceipt.Tests,
		TestResult{ID: "model", Command: "go test ./internal/model", Outcome: TestPassed, Assurance: AssuranceSelfReported})
	secondReceipt := firstReceipt
	secondReceipt.Tests = append([]TestResult(nil), firstReceipt.Tests...)
	secondReceipt.Tests[0], secondReceipt.Tests[1] = secondReceipt.Tests[1], secondReceipt.Tests[0]
	sealedFirst, err := SealReceipt(firstReceipt)
	if err != nil {
		t.Fatal(err)
	}
	sealedSecond, err := SealReceipt(secondReceipt)
	if err != nil {
		t.Fatal(err)
	}
	if sealedFirst.ReceiptDigest != sealedSecond.ReceiptDigest {
		t.Fatalf("receipt digests differ: %s != %s", sealedFirst.ReceiptDigest, sealedSecond.ReceiptDigest)
	}
}

func TestVerifierPacketSealsCanonicalGuidanceAndMergesExactSelectors(t *testing.T) {
	value := verificationAssignment()
	packet := VerifierPacket{
		Guidance: &VerifierGuidance{
			Context:     json.RawMessage(`{ "language": "Go", "project": "proxy" }`),
			RulesVerify: json.RawMessage(`{"business":["run fixture command"],"mode":"strict"}`),
			Instructions: []VerifierInstruction{
				{ArtifactID: "verify-z", Text: "Check the affected route."},
				{ArtifactID: "verify-a", Text: "Run the exact assigned commands."},
			},
		},
		RequiredTests: []TestSelector{
			{ID: "business", Command: "./scripts/verify-business.sh"},
			{ID: "unit", Command: "go test ./..."},
		},
		RequiredChecks: []CheckSelector{
			{Provider: "code.example", Name: "policy"},
			{Provider: "github", Name: "test"},
		},
	}
	merged, err := value.Verification.WithVerifierPacket(packet)
	if err != nil {
		t.Fatal(err)
	}
	value.Verification = &merged
	if len(merged.RequiredTests) != 2 || merged.RequiredTests[0].ID != "business" || merged.RequiredTests[1].ID != "unit" {
		t.Fatalf("required tests = %+v", merged.RequiredTests)
	}
	if len(merged.RequiredChecks) != 2 || merged.RequiredChecks[0].Provider != "code.example" || merged.RequiredChecks[1].Provider != "github" {
		t.Fatalf("required checks = %+v", merged.RequiredChecks)
	}
	if got := string(merged.Guidance.Context); got != `{"language":"Go","project":"proxy"}` {
		t.Fatalf("canonical context = %s", got)
	}
	if merged.Guidance.Instructions[0].ArtifactID != "verify-a" {
		t.Fatalf("instructions are not stable: %+v", merged.Guidance.Instructions)
	}

	canonical, err := CanonicalAssignmentJSON(value)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseAssignmentJSON(canonical)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(parsed.Verification.Guidance.RulesVerify); got != `{"business":["run fixture command"],"mode":"strict"}` {
		t.Fatalf("rules.verify changed during sealing: %s", got)
	}
	firstDigest, err := AssignmentDigest(value)
	if err != nil {
		t.Fatal(err)
	}
	idempotent, err := parsed.Verification.WithVerifierPacket(packet)
	if err != nil {
		t.Fatal(err)
	}
	parsed.Verification = &idempotent
	secondDigest, err := AssignmentDigest(parsed)
	if err != nil {
		t.Fatal(err)
	}
	if firstDigest != secondDigest {
		t.Fatalf("idempotent verifier packet changed digest: %s != %s", firstDigest, secondDigest)
	}
}

func TestVerifierPacketRejectsSelectorAndGuidanceConflicts(t *testing.T) {
	payload := *verificationAssignment().Verification
	if _, err := payload.WithVerifierPacket(VerifierPacket{RequiredTests: []TestSelector{
		{ID: "unit", Command: "go test ./internal/workflow"},
	}}); err == nil || !strings.Contains(err.Error(), "conflicting commands") {
		t.Fatalf("selector conflict error = %v", err)
	}

	withGuidance, err := payload.WithVerifierPacket(VerifierPacket{Guidance: &VerifierGuidance{
		Context: json.RawMessage(`{"project":"one"}`),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := withGuidance.WithVerifierPacket(VerifierPacket{Guidance: &VerifierGuidance{
		Context: json.RawMessage(`{"project":"two"}`),
	}}); err == nil || !strings.Contains(err.Error(), "guidance conflicts") {
		t.Fatalf("guidance conflict error = %v", err)
	}
}

func TestVerificationReceiptCoveragePreservesSelectorIdentityRevisionAndAssurance(t *testing.T) {
	required := VerificationPayload{
		SubjectRevision: subjectRevision,
		RequiredTests:   []TestSelector{{ID: "business", Command: "./scripts/verify-business.sh"}},
		RequiredChecks:  []CheckSelector{{Provider: "code.example", Name: "policy"}},
	}
	receipt := Receipt{
		SchemaVersion: ReceiptSchemaVersion, ID: "receipt-business-1",
		AssignmentID: "asg-business-1", AssignmentDigest: validDigest, AssignmentGeneration: 1,
		Role: RoleVerification, ResultSchemaVersion: ReceiptSchemaVersion, SubjectRevision: subjectRevision,
		Tests:        []TestResult{{ID: "business", Command: "./scripts/verify-business.sh", Outcome: TestPassed, Assurance: AssuranceSelfReported}},
		Provenance:   Provenance{Route: RouteRoleOwned, Assurance: AssuranceSelfReported, Writer: "Verifier", Subject: "Verifier", Source: "verify-submit"},
		Verification: &VerificationResult{Summary: "Business policy passed.", CheckSelectors: []CheckSelector{{Provider: "code.example", Name: "policy"}}},
	}
	sealed, err := SealReceipt(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateVerificationReceiptCoverage(required, sealed); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(sealed)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseReceiptJSON(payload)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Tests[0].Assurance != AssuranceSelfReported || parsed.SubjectRevision != subjectRevision {
		t.Fatalf("receipt assurance/revision changed: %+v", parsed)
	}

	proseOnly := sealed
	proseOnly.Verification = &VerificationResult{Summary: "Provider policy passed."}
	proseOnly, err = SealReceipt(proseOnly)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateVerificationReceiptCoverage(required, proseOnly); err == nil || !strings.Contains(err.Error(), "exactly cover") {
		t.Fatalf("natural-language provider conclusion became authority: %v", err)
	}

	stale := sealed
	stale.SubjectRevision = baseRevision
	stale, err = SealReceipt(stale)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateVerificationReceiptCoverage(required, stale); err == nil || !strings.Contains(err.Error(), "exact assigned revision") {
		t.Fatalf("stale revision error = %v", err)
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
