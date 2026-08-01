package assignment

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func revisionBoundSelector(source RevisionBindingSource) TestSelector {
	return TestSelector{
		ID:      "durable",
		Command: "issue-spec durable-spec check --repo higress-group/issue-spec --proposal 381 --root . --json",
		RevisionBinding: &RevisionBinding{
			Source: source, Argument: RevisionBindingArgumentSubject,
		},
	}
}

func resolvedTestResult(t *testing.T, selector TestSelector, revision string) TestResult {
	t.Helper()
	resolved, err := ResolveTestSelector(selector, revision)
	if err != nil {
		t.Fatal(err)
	}
	assigned := resolved.AssignedSelector
	return TestResult{ID: selector.ID, Command: resolved.Command, AssignedSelector: &assigned,
		ResolvedRevision: revision, Outcome: TestPassed, Assurance: AssuranceSelfReported}
}

func TestResolveTestSelectorPreservesBytesForBothClosedSources(t *testing.T) {
	for _, source := range []RevisionBindingSource{
		RevisionBindingSourceResultRevision,
		RevisionBindingSourceSubjectRevision,
	} {
		t.Run(string(source), func(t *testing.T) {
			selector := revisionBoundSelector(source)
			selector.Command = "issue-spec  durable-spec check --repo o/r --proposal 381 --root . --json"
			for _, revision := range []string{strings.Repeat("a", 40), strings.Repeat("b", 64)} {
				resolved, err := ResolveTestSelector(selector, revision)
				if err != nil {
					t.Fatal(err)
				}
				want := selector.Command + " --subject " + revision
				if resolved.Command != want || resolved.ResolvedRevision != revision ||
					!TestSelectorIdentityEqual(resolved.AssignedSelector, selector) {
					t.Fatalf("resolved identity=%+v, want command %q", resolved, want)
				}
				selector.RevisionBinding.Argument = "changed"
				if resolved.AssignedSelector.RevisionBinding.Argument != RevisionBindingArgumentSubject {
					t.Fatal("resolved identity aliased the caller's binding")
				}
				selector.RevisionBinding.Argument = RevisionBindingArgumentSubject
			}
		})
	}
}

func TestRevisionContractValidationIsRoleAwareAndLiteralExact(t *testing.T) {
	revision := strings.Repeat("b", 40)
	resultBound := revisionBoundSelector(RevisionBindingSourceResultRevision)
	subjectBound := revisionBoundSelector(RevisionBindingSourceSubjectRevision)
	for name, test := range map[string]struct {
		role      Role
		revision  string
		selector  TestSelector
		wantError string
	}{
		"future implementation result": {RoleImplementation, "", resultBound, ""},
		"known implementation result":  {RoleImplementation, revision, resultBound, ""},
		"known review subject":         {RoleReview, revision, subjectBound, ""},
		"known verification subject":   {RoleVerification, revision, subjectBound, ""},
		"result source in review":      {RoleReview, revision, resultBound, "not supported for review"},
		"subject source in implementation": {
			RoleImplementation, revision, subjectBound, "not supported for implementation",
		},
		"subject source in verification without authority": {
			RoleVerification, "", subjectBound, "full Git object ID",
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := ValidateTestSelectorRevisionContract(test.role, test.revision, test.selector)
			if test.wantError == "" && err != nil {
				t.Fatal(err)
			}
			if test.wantError != "" && (err == nil || !strings.Contains(err.Error(), test.wantError)) {
				t.Fatalf("error=%v, want %q", err, test.wantError)
			}
		})
	}

	literal := TestSelector{ID: "legacy", Command: "issue-spec durable-spec check --repo o/r --subject " + revision + " --json"}
	for _, role := range []Role{RoleImplementation, RoleReview, RoleVerification} {
		if err := ValidateTestSelectorRevisionContract(role, revision, literal); err != nil {
			t.Fatalf("%s exact literal: %v", role, err)
		}
	}
	if err := ValidateTestSelectorRevisionContract(RoleReview, strings.Repeat("c", 40), literal); err == nil || !strings.Contains(err.Error(), "must equal") {
		t.Fatalf("literal mismatch error=%v", err)
	}
	generic := TestSelector{ID: "generic", Command: "custom-tool verify --subject " + revision}
	if err := ValidateTestSelectorRevisionContract(RoleReview, revision, generic); err != nil {
		t.Fatalf("agreeing generic direct literal: %v", err)
	}
	if err := ValidateTestSelectorRevisionContract(RoleReview, strings.Repeat("c", 40), generic); err == nil || !strings.Contains(err.Error(), "must equal") {
		t.Fatalf("generic literal mismatch error=%v", err)
	}
	// Version 1 does not guess sensitivity for arbitrary commands without a
	// typed binding, built-in signature, or direct literal --subject argument.
	opaque := TestSelector{ID: "opaque", Command: "custom-tool verify --revision historical-value"}
	if err := ValidateTestSelectorRevisionContract(RoleImplementation, "", opaque); err != nil {
		t.Fatalf("opaque literal selector changed behavior: %v", err)
	}
	opaqueShell := TestSelector{ID: "opaque-shell", Command: `sh -c "custom-tool --subject fixed"`}
	if err := ValidateTestSelectorRevisionContract(RoleReview, revision, opaqueShell); err != nil {
		t.Fatalf("arbitrary historical shell literal changed behavior: %v", err)
	}
}

func TestRecognizedDurableLiteralRequiresOneExactSubjectContract(t *testing.T) {
	revision := strings.Repeat("b", 40)
	for name, command := range map[string]string{
		"missing":     "issue-spec durable-spec check --repo o/r --json",
		"abbreviated": "issue-spec durable-spec check --subject bbbbbbb --json",
		"placeholder": "issue-spec durable-spec check --subject <subject-revision> --json",
		"equals":      "issue-spec durable-spec check --subject=" + revision + " --json",
		"duplicate":   "issue-spec durable-spec check --subject " + revision + " --subject " + revision,
		"operator":    "issue-spec durable-spec check --subject " + revision + " | tee out",
		"tab":         "issue-spec\tdurable-spec check --subject " + revision,
	} {
		t.Run(name, func(t *testing.T) {
			err := ValidateTestSelectorRevisionContract(RoleVerification, revision, TestSelector{ID: "legacy", Command: command})
			if err == nil {
				t.Fatal("accepted malformed recognized durable literal")
			}
		})
	}
}

func TestHistoricalRegistryDurableLiteralIsStorageReadableOnly(t *testing.T) {
	const historicalDigest = "bb46efdc2bcfc8bade5f9c03f9034f5e476b1ca76aead055cf09fc711e6368e2"
	const historicalJSON = `{
  "schema_version": "issue-spec.assignment/v1",
  "assignment_id": "issue-353-process-003-verification-assignment-1",
  "role": "verification",
  "repository": "higress-group/issue-spec",
  "issue": 353,
  "process_id": "PROCESS-353003",
  "subject_revision": "62303a684e8b951c923f03f169e846a5dcf8ee3a",
  "scenarios": [
    {"spec_id":"SPEC-351001","scenario":"Webhook configuration remains management-only"},
    {"spec_id":"SPEC-351001","scenario":"integration manager retains source-binding management"},
    {"spec_id":"SPEC-351001","scenario":"read-only source view exposes no mutation interaction"},
    {"spec_id":"SPEC-351001","scenario":"repository reader locates the active external source repository"},
    {"spec_id":"SPEC-351001","scenario":"repository visibility remains authoritative"},
    {"spec_id":"SPEC-351001","scenario":"unbound repository remains explicit to a reader"}
  ],
  "dependencies": ["PROCESS-353002"],
  "handoff": "N/A",
  "policy": {"require_exact_revision":true},
  "result_schema_version": "issue-spec.receipt/v1",
  "verification": {
    "subject_revision": "62303a684e8b951c923f03f169e846a5dcf8ee3a",
    "required_tests": [
      {"id":"durable-spec","command":"issue-spec durable-spec check --repo higress-group/issue-spec --proposal 351 --root . --baseline \"$(git merge-base origin/main HEAD)\" --subject \"$(git rev-parse HEAD)\" --json"},
      {"id":"issue-spec/durable-spec","command":"issue-spec durable-spec check --repo higress-group/issue-spec --proposal 351 --baseline 2c6330c6c4ae2ec459d378673cb9416f914192a2 --subject 62303a684e8b951c923f03f169e846a5dcf8ee3a --root . --json"},
      {"id":"web-integrations","command":"cd web && npm test -- src/repos/integrations.test.tsx"},
      {"id":"web-typecheck","command":"cd web && npm run typecheck"}
    ]
  }
}`
	var historical Assignment
	if err := json.Unmarshal([]byte(historicalJSON), &historical); err != nil {
		t.Fatal(err)
	}
	if err := historical.ValidateForStorageRead(); err != nil {
		t.Fatalf("historical registry assignment is not storage-readable: %v", err)
	}
	digest, err := AssignmentDigestForStorageRead(historical)
	if err != nil {
		t.Fatal(err)
	}
	if digest != historicalDigest {
		t.Fatalf("historical digest changed: got %s want %s", digest, historicalDigest)
	}
	assertStrictHistoricalAssignmentRejected(t, historical, digest)

	for name, binding := range map[string]RevisionBinding{
		"shell grammar": {Source: RevisionBindingSourceSubjectRevision, Argument: RevisionBindingArgumentSubject},
		"role source":   {Source: RevisionBindingSourceResultRevision, Argument: RevisionBindingArgumentSubject},
		"argument":      {Source: RevisionBindingSourceSubjectRevision, Argument: "--revision"},
	} {
		t.Run("typed binding "+name, func(t *testing.T) {
			candidate := historical
			candidate.Verification = cloneVerificationPayload(historical.Verification)
			candidate.Verification.RequiredTests[0].RevisionBinding = &binding
			if err := candidate.ValidateForStorageRead(); err == nil {
				t.Fatal("storage compatibility weakened typed binding validation")
			}
		})
	}
}

func assertStrictHistoricalAssignmentRejected(t *testing.T, historical Assignment, storageDigest string) {
	t.Helper()
	if err := historical.Validate(); err == nil {
		t.Fatal("ordinary validation accepted a historical shell-expanded durable selector")
	}
	if _, err := AssignmentDigest(historical); err == nil {
		t.Fatal("ordinary assignment digest accepted a historical shell-expanded durable selector")
	}
	if err := (Packet{Assignment: historical, AssignmentDigest: storageDigest, Generation: 1}).Validate(); err == nil {
		t.Fatal("packet issuance accepted a historical shell-expanded durable selector")
	}
	payload, err := json.Marshal(historical)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseAssignmentJSON(payload); err == nil {
		t.Fatal("assignment-file parser accepted a historical shell-expanded durable selector")
	}
}

func cloneVerificationPayload(value *VerificationPayload) *VerificationPayload {
	clone := *value
	clone.RequiredTests = cloneTestSelectors(value.RequiredTests)
	clone.RequiredChecks = append([]CheckSelector(nil), value.RequiredChecks...)
	return &clone
}

func TestAssignmentsRoundTripAndDigestBothBindingSources(t *testing.T) {
	implementation := implementationAssignment()
	literalDigest, err := AssignmentDigest(implementation)
	if err != nil {
		t.Fatal(err)
	}
	implementation.Implementation.FocusedTests[0] = revisionBoundSelector(RevisionBindingSourceResultRevision)

	review := reviewAssignment()
	review.Review.RequiredTests = []TestSelector{revisionBoundSelector(RevisionBindingSourceSubjectRevision)}
	verification := verificationAssignment()
	verification.Verification.RequiredTests = []TestSelector{revisionBoundSelector(RevisionBindingSourceSubjectRevision)}

	for name, value := range map[string]Assignment{
		"implementation": implementation,
		"review":         review,
		"verification":   verification,
	} {
		t.Run(name, func(t *testing.T) {
			canonical, err := CanonicalAssignmentJSON(value)
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := ParseAssignmentJSON(canonical)
			if err != nil {
				t.Fatal(err)
			}
			var selector TestSelector
			switch parsed.Role {
			case RoleImplementation:
				selector = parsed.Implementation.FocusedTests[0]
			case RoleReview:
				selector = parsed.Review.RequiredTests[0]
			case RoleVerification:
				selector = parsed.Verification.RequiredTests[0]
			}
			if selector.RevisionBinding == nil {
				t.Fatal("binding disappeared during round trip")
			}
		})
	}
	boundDigest, err := AssignmentDigest(implementation)
	if err != nil {
		t.Fatal(err)
	}
	if boundDigest == literalDigest {
		t.Fatal("assignment digest did not cover revision_binding")
	}

	input := ProcessInput{RequiredTests: []TestSelector{revisionBoundSelector(RevisionBindingSourceSubjectRevision)}}
	payload, err := ProcessInputJSON(input)
	if err != nil {
		t.Fatal(err)
	}
	parsedInput, err := ParseProcessInputJSON(payload)
	if err != nil || parsedInput.RequiredTests[0].RevisionBinding == nil {
		t.Fatalf("PROCESS input round trip=%+v err=%v", parsedInput, err)
	}
}

func TestAssignmentRejectsWrongRoleSourceAndConflictingMergeIdentity(t *testing.T) {
	implementation := implementationAssignment()
	implementation.Implementation.FocusedTests[0] = revisionBoundSelector(RevisionBindingSourceSubjectRevision)
	if err := implementation.Validate(); err == nil || !strings.Contains(err.Error(), "not supported for implementation") {
		t.Fatalf("implementation role error=%v", err)
	}
	review := reviewAssignment()
	review.Review.RequiredTests = []TestSelector{revisionBoundSelector(RevisionBindingSourceResultRevision)}
	if err := review.Validate(); err == nil || !strings.Contains(err.Error(), "not supported for review") {
		t.Fatalf("review role error=%v", err)
	}
	verification := verificationAssignment()
	verification.Verification.RequiredTests = []TestSelector{revisionBoundSelector(RevisionBindingSourceResultRevision)}
	if err := verification.Validate(); err == nil || !strings.Contains(err.Error(), "not supported for verification") {
		t.Fatalf("verification role error=%v", err)
	}
	if err := (VerifierPacket{RequiredTests: []TestSelector{revisionBoundSelector(RevisionBindingSourceResultRevision)}}).Validate(); err == nil || !strings.Contains(err.Error(), "not supported for verification") {
		t.Fatalf("verifier packet role error=%v", err)
	}

	left := revisionBoundSelector(RevisionBindingSourceSubjectRevision)
	right := revisionBoundSelector(RevisionBindingSourceResultRevision)
	if _, err := MergeRequiredSelectors(RequiredSelectors{Tests: []TestSelector{left}}, RequiredSelectors{Tests: []TestSelector{right}}); err == nil || !strings.Contains(err.Error(), "conflicting revision bindings") {
		t.Fatalf("merge conflict error=%v", err)
	}
}

func TestBoundSelectorRejectsUnsafeGrammarDuplicatesRevisionAndSize(t *testing.T) {
	selector := revisionBoundSelector(RevisionBindingSourceResultRevision)
	unsupportedSource := selector
	unsupportedSource.RevisionBinding = &RevisionBinding{Source: "head", Argument: RevisionBindingArgumentSubject}
	if err := ValidateTestSelectorRevisionContract(RoleImplementation, "", unsupportedSource); err == nil || !strings.Contains(err.Error(), "unsupported value") {
		t.Fatalf("unsupported source error=%v", err)
	}
	unsupportedArgument := selector
	unsupportedArgument.RevisionBinding = &RevisionBinding{Source: RevisionBindingSourceResultRevision, Argument: "--revision"}
	if err := ValidateTestSelectorRevisionContract(RoleImplementation, "", unsupportedArgument); err == nil || !strings.Contains(err.Error(), "unsupported value") {
		t.Fatalf("unsupported argument error=%v", err)
	}
	unsafe := map[string]string{
		"single quote":     "go test './internal/assignment'",
		"double quote":     `go test "./internal/assignment"`,
		"escape":           `go test ./internal/assignment\ test`,
		"variable":         "go test $PACKAGE",
		"percent variable": "go test %PACKAGE%",
		"substitution":     "go test $(pwd)",
		"backtick":         "go test `pwd`",
		"redirection":      "go test >out",
		"pipeline":         "go test | tee out",
		"list":             "go test; next",
		"background":       "go test &",
		"newline":          "go test\nnext",
		"tab":              "go\ttest",
		"shell wrapper":    "sh -c test",
		"bash wrapper":     "bash -c test",
		"env wrapper":      "env bash -c test",
		"placeholder":      "go test <result-revision>",
		"subject":          "go test --subject value",
		"subject equals":   "go test --subject=value",
	}
	for name, command := range unsafe {
		t.Run(name, func(t *testing.T) {
			candidate := selector
			candidate.Command = command
			if err := ValidateTestSelectorRevisionContract(RoleImplementation, "", candidate); err == nil {
				t.Fatal("accepted unsafe bound selector")
			}
		})
	}

	for name, revision := range map[string]string{
		"missing": "", "abbreviated": strings.Repeat("a", 39), "too long sha1": strings.Repeat("a", 41),
		"short sha256": strings.Repeat("a", 63), "too long sha256": strings.Repeat("a", 65),
		"uppercase": strings.Repeat("A", 40), "non hex": strings.Repeat("g", 40),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ResolveTestSelector(selector, revision); err == nil {
				t.Fatal("accepted non-full result revision")
			}
		})
	}

	maxAssigned := maxCommandLength - len(revisionCommandSuffix) - 64
	selector.Command = "cmd " + strings.Repeat("x", maxAssigned-len("cmd "))
	if err := ValidateTestSelectorRevisionContract(RoleImplementation, "", selector); err != nil {
		t.Fatalf("maximum bound command rejected: %v", err)
	}
	if _, err := ResolveTestSelector(selector, strings.Repeat("a", 64)); err != nil {
		t.Fatalf("maximum expansion rejected: %v", err)
	}
	selector.Command += "x"
	if err := ValidateTestSelectorRevisionContract(RoleImplementation, "", selector); err == nil || !strings.Contains(err.Error(), "cannot fit") {
		t.Fatalf("oversize error=%v", err)
	}
}

func TestBoundReceiptIdentityIsPairedRoleAwareAndDigestCovered(t *testing.T) {
	resultRevision := strings.Repeat("b", 40)
	resultSelector := revisionBoundSelector(RevisionBindingSourceResultRevision)
	implementation := implementationReceipt()
	implementation.ResultRevision = resultRevision
	implementation.Tests = []TestResult{resolvedTestResult(t, resultSelector, resultRevision)}
	sealedImplementation, err := SealReceipt(implementation)
	if err != nil {
		t.Fatal(err)
	}

	subjectSelector := revisionBoundSelector(RevisionBindingSourceSubjectRevision)
	review := reviewReceiptWithTests(resultRevision, []TestResult{resolvedTestResult(t, subjectSelector, resultRevision)})
	if _, err := SealReceipt(review); err != nil {
		t.Fatal(err)
	}
	verification := verificationReceiptWithTests(resultRevision, []TestResult{resolvedTestResult(t, subjectSelector, resultRevision)})
	if _, err := SealReceipt(verification); err != nil {
		t.Fatal(err)
	}

	for name, mutate := range map[string]func(*Receipt){
		"selector without revision": func(value *Receipt) { value.Tests[0].ResolvedRevision = "" },
		"revision without selector": func(value *Receipt) { value.Tests[0].AssignedSelector = nil },
		"different selector id":     func(value *Receipt) { value.Tests[0].AssignedSelector.ID = "other" },
		"different command":         func(value *Receipt) { value.Tests[0].Command += " --forged" },
		"different outer revision":  func(value *Receipt) { value.ResultRevision = strings.Repeat("c", 40) },
		"wrong role source": func(value *Receipt) {
			value.Tests[0].AssignedSelector.RevisionBinding.Source = RevisionBindingSourceSubjectRevision
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := implementation
			candidate.Tests = append([]TestResult(nil), implementation.Tests...)
			assigned := cloneTestSelector(*implementation.Tests[0].AssignedSelector)
			candidate.Tests[0].AssignedSelector = &assigned
			mutate(&candidate)
			if _, err := SealReceipt(candidate); err == nil {
				t.Fatal("sealed invalid paired result identity")
			}
		})
	}

	tampered := sealedImplementation
	tampered.Tests = append([]TestResult(nil), sealedImplementation.Tests...)
	assigned := cloneTestSelector(*sealedImplementation.Tests[0].AssignedSelector)
	assigned.Command += "x"
	tampered.Tests[0].AssignedSelector = &assigned
	resolved, err := ResolveTestSelector(assigned, resultRevision)
	if err != nil {
		t.Fatal(err)
	}
	tampered.Tests[0].Command = resolved.Command
	if err := tampered.Validate(); err == nil || !strings.Contains(err.Error(), "does not match canonical receipt") {
		t.Fatalf("digest tamper error=%v", err)
	}
}

func TestReviewAndVerificationCoverageRequireExactLiteralAndBoundIdentity(t *testing.T) {
	revision := strings.Repeat("b", 40)
	bound := revisionBoundSelector(RevisionBindingSourceSubjectRevision)
	literal := TestSelector{ID: "unit", Command: "go test ./internal/assignment"}
	results := []TestResult{
		resolvedTestResult(t, bound, revision),
		{ID: literal.ID, Command: literal.Command, Outcome: TestPassed, Assurance: AssuranceSelfReported},
	}

	reviewPayload := *reviewAssignment().Review
	reviewPayload.RequiredTests = []TestSelector{bound, literal}
	review := reviewReceiptWithTests(revision, results)
	sealedReview, err := SealReceipt(review)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateReviewReceiptCoverage(reviewPayload, sealedReview); err != nil {
		t.Fatal(err)
	}

	verificationPayload := VerificationPayload{SubjectRevision: revision, RequiredTests: []TestSelector{bound, literal}}
	verification := verificationReceiptWithTests(revision, results)
	sealedVerification, err := SealReceipt(verification)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateVerificationReceiptCoverage(verificationPayload, sealedVerification); err != nil {
		t.Fatal(err)
	}

	for name, mutate := range map[string]func([]TestResult){
		"missing": func(values []TestResult) { values[0].ID = "unassigned" },
		"failed":  func(values []TestResult) { values[0].Outcome = TestFailed },
		"literal as bound": func(values []TestResult) {
			values[1] = resolvedTestResult(t, bound, revision)
			values[1].ID = literal.ID
		},
		"changed selector":   func(values []TestResult) { values[0].AssignedSelector.Command += "x"; values[0].Command += "x" },
		"changed resolution": func(values []TestResult) { values[0].ResolvedRevision = strings.Repeat("c", 40) },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := review
			candidate.Tests = cloneTestResults(review.Tests)
			mutate(candidate.Tests)
			sealed, sealErr := SealReceipt(candidate)
			if sealErr == nil {
				if err := ValidateReviewReceiptCoverage(reviewPayload, sealed); err == nil {
					t.Fatal("accepted mismatched review coverage")
				}
			}
		})
	}
}

func TestRevisionBindingStrictJSONAndGoldenIdentities(t *testing.T) {
	selector := revisionBoundSelector(RevisionBindingSourceSubjectRevision)
	selectorJSON, err := json.Marshal(selector)
	if err != nil {
		t.Fatal(err)
	}
	wantSelector := `{"id":"durable","command":"issue-spec durable-spec check --repo higress-group/issue-spec --proposal 381 --root . --json","revision_binding":{"source":"subject-revision","argument":"--subject"}}`
	if string(selectorJSON) != wantSelector {
		t.Fatalf("selector JSON=%s", selectorJSON)
	}

	result := resolvedTestResult(t, selector, subjectRevision)
	resultJSON, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	wantResult := `{"id":"durable","command":"issue-spec durable-spec check --repo higress-group/issue-spec --proposal 381 --root . --json --subject bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","assigned_selector":{"id":"durable","command":"issue-spec durable-spec check --repo higress-group/issue-spec --proposal 381 --root . --json","revision_binding":{"source":"subject-revision","argument":"--subject"}},"resolved_revision":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","outcome":"passed","assurance":"self-reported"}`
	if string(resultJSON) != wantResult {
		t.Fatalf("result JSON=%s", resultJSON)
	}

	assignmentValue := implementationAssignment()
	assignmentValue.Implementation.FocusedTests[0] = revisionBoundSelector(RevisionBindingSourceResultRevision)
	canonical, err := CanonicalAssignmentJSON(assignmentValue)
	if err != nil {
		t.Fatal(err)
	}
	unknownBinding := bytes.Replace(canonical, []byte(`"revision_binding":{"source"`), []byte(`"revision_binding":{"extra":true,"source"`), 1)
	if _, err := ParseAssignmentJSON(unknownBinding); err == nil || !strings.Contains(err.Error(), `unknown field "extra"`) {
		t.Fatalf("unknown binding error=%v", err)
	}

	receipt := reviewReceiptWithTests(subjectRevision, []TestResult{result})
	sealed, err := SealReceipt(receipt)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(sealed)
	if err != nil {
		t.Fatal(err)
	}
	unknownAssigned := bytes.Replace(payload, []byte(`"assigned_selector":{"id"`), []byte(`"assigned_selector":{"extra":true,"id"`), 1)
	if _, err := ParseReceiptJSON(unknownAssigned); err == nil || !strings.Contains(err.Error(), `unknown field "extra"`) {
		t.Fatalf("unknown assigned selector error=%v", err)
	}
}

func TestHistoricalLiteralReceiptCanonicalJSONAndDigestGolden(t *testing.T) {
	value := implementationReceipt()
	canonical, err := CanonicalReceiptJSON(value)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"schema_version":"issue-spec.receipt/v1","receipt_id":"receipt-005-1","assignment_id":"asg-005-1","assignment_digest":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef","assignment_generation":1,"role":"implementation","result_schema_version":"issue-spec.receipt/v1","base_revision":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","result_revision":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","tests":[{"id":"unit","command":"go test ./internal/assignment","outcome":"passed","assurance":"self-reported"}],"provenance":{"route":"role-owned","assurance":"self-reported","writer":"Worker","subject":"Worker","source":"role-command"},"implementation":{"changed_paths":["internal/assignment/types.go"],"decisions":["keep schema pure"]}}`
	if string(canonical) != want {
		t.Fatalf("canonical historical receipt mismatch\n got: %s\nwant: %s", canonical, want)
	}
	digest, err := ReceiptDigest(value)
	if err != nil {
		t.Fatal(err)
	}
	if digest != "107135de24aa47336da8228f218ac70919714ad17d068cd038d1bddabfdc1c4c" {
		t.Fatalf("historical receipt digest=%s", digest)
	}
}

func reviewReceiptWithTests(subject string, tests []TestResult) Receipt {
	return Receipt{
		SchemaVersion: ReceiptSchemaVersion, ID: "receipt-review-bound-1",
		AssignmentID: "assignment-review-bound-1", AssignmentDigest: validDigest, AssignmentGeneration: 1,
		Role: RoleReview, ResultSchemaVersion: ReceiptSchemaVersion, SubjectRevision: subject,
		Tests: cloneTestResults(tests),
		Provenance: Provenance{Route: RouteRoleOwned, Assurance: AssuranceSelfReported,
			Writer: "Reviewer", Subject: "Reviewer", Source: "review-submit"},
		Review: &ReviewResult{Verdict: ReviewApprove},
	}
}

func verificationReceiptWithTests(subject string, tests []TestResult) Receipt {
	return Receipt{
		SchemaVersion: ReceiptSchemaVersion, ID: "receipt-verification-bound-1",
		AssignmentID: "assignment-verification-bound-1", AssignmentDigest: validDigest, AssignmentGeneration: 1,
		Role: RoleVerification, ResultSchemaVersion: ReceiptSchemaVersion, SubjectRevision: subject,
		Tests: cloneTestResults(tests),
		Provenance: Provenance{Route: RouteRoleOwned, Assurance: AssuranceSelfReported,
			Writer: "Verifier", Subject: "Verifier", Source: "verify-submit"},
		Verification: &VerificationResult{Summary: "required tests passed"},
	}
}

func cloneTestResults(values []TestResult) []TestResult {
	clones := append([]TestResult(nil), values...)
	for i := range clones {
		if values[i].AssignedSelector != nil {
			selector := cloneTestSelector(*values[i].AssignedSelector)
			clones[i].AssignedSelector = &selector
		}
	}
	return clones
}
