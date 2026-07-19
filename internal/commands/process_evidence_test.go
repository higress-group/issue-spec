package commands

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/higress-group/issue-spec/internal/assignment"
	"github.com/higress-group/issue-spec/internal/codereview"
	coreevidence "github.com/higress-group/issue-spec/internal/evidence"
	"github.com/higress-group/issue-spec/internal/gates"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/model"
)

func TestFilterSharedVerificationIdentitySupportsAcceptedAndManualSelfReportedPerPair(t *testing.T) {
	const specID = "SPEC-001"
	revision := strings.Repeat("b", 40)
	specURL := "https://example/spec-001"
	one := processClassArtifact(t, "PROCESS-001", "change-bearing", specID, "done")
	two := processClassArtifact(t, "PROCESS-002", "change-bearing", specID, "done")
	manualCarrier := func(agent, subject string, links []string) model.Artifact {
		t.Helper()
		body, err := model.EnsureTypedBody("VERIFY", "VERIFY-002", "Verified both processes with tests.",
			model.BodyOptions{Agent: agent, Status: "done", SubjectRevision: subject,
				Links: map[string][]string{"Related Comments": links}})
		if err != nil {
			t.Fatal(err)
		}
		return model.Artifact{URL: "https://example/verify-manual", Comment: model.ParseTypedComment(body)}
	}
	manualLinks := []string{one.URL, two.URL, specURL}
	manual := manualCarrier("Verifier", revision, manualLinks)
	receipt := testSealedVerificationReceipt(t, []assignment.TestResult{{ID: "unit", Command: "go test ./internal/gates",
		Outcome: assignment.TestPassed, Assurance: assignment.AssuranceSelfReported}}, nil)
	acceptedBody, err := renderSubmittedVerification("VERIFY-001", one.URL, []string{specID}, receipt, nil,
		testVerificationSubmission("Verifier"))
	if err != nil {
		t.Fatal(err)
	}
	accepted := model.Artifact{URL: "https://example/verify-accepted", Comment: model.ParseTypedComment(acceptedBody)}

	newInputs := func(url string) []gates.ProcessEvidenceInput {
		return []gates.ProcessEvidenceInput{
			{Process: one, RequiredRevision: revision, ActiveSpecs: map[string]string{specID: specURL},
				Verifications: []gates.VerificationEvidence{{ProcessID: one.Comment.ID, SpecID: specID, URL: url, Done: true, TestEvidence: true}}},
			{Process: two, RequiredRevision: revision, ActiveSpecs: map[string]string{specID: specURL},
				Verifications: []gates.VerificationEvidence{{ProcessID: two.Comment.ID, SpecID: specID, URL: url, Done: true, TestEvidence: true}}},
		}
	}
	independentAuthors := map[string]map[string]map[string]bool{
		one.Comment.ID: {specID: {"worker one": true}},
		two.Comment.ID: {specID: {"worker two": true}},
	}

	t.Run("independent accepted verifier is projected for each pair", func(t *testing.T) {
		inputs := newInputs(accepted.URL)
		filterSharedVerificationIdentity(inputs, []model.Artifact{accepted}, independentAuthors, revision)
		if len(inputs[0].Verifications) != 1 || len(inputs[1].Verifications) != 1 {
			t.Fatalf("independent verifier was not projected: %+v", inputs)
		}
	})

	t.Run("exact-current manual self-reported verifier is projected for each explicit pair", func(t *testing.T) {
		inputs := newInputs(manual.URL)
		filterSharedVerificationIdentity(inputs, []model.Artifact{manual}, independentAuthors, revision)
		for _, input := range inputs {
			if len(input.Verifications) != 1 || !input.Verifications[0].Trusted ||
				input.Verifications[0].SubjectRevision != revision ||
				input.Verifications[0].Source != "manual-self-reported-verify:exact-current" {
				t.Fatalf("manual self-reported verifier was not exactly projected: %+v", inputs)
			}
		}
	})

	t.Run("manual Coordinator is rejected", func(t *testing.T) {
		coordinator := manualCarrier("Coordinator", revision, manualLinks)
		inputs := newInputs(coordinator.URL)
		filterSharedVerificationIdentity(inputs, []model.Artifact{coordinator}, independentAuthors, revision)
		if len(inputs[0].Verifications) != 0 || len(inputs[1].Verifications) != 0 {
			t.Fatalf("Coordinator established manual verifier identity: %+v", inputs)
		}
	})

	t.Run("manual empty Agent is rejected as noncanonical", func(t *testing.T) {
		empty := manual
		empty.Comment = model.ParseTypedComment(strings.Replace(manual.Comment.Body, "Agent: Verifier", "Agent: ", 1))
		inputs := newInputs(empty.URL)
		filterSharedVerificationIdentity(inputs, []model.Artifact{empty}, independentAuthors, revision)
		if len(inputs[0].Verifications) != 0 || len(inputs[1].Verifications) != 0 {
			t.Fatalf("empty Agent established manual verifier identity: %+v", inputs)
		}
	})

	t.Run("manual stale revision is rejected", func(t *testing.T) {
		stale := manualCarrier("Verifier", strings.Repeat("c", 40), manualLinks)
		inputs := newInputs(stale.URL)
		filterSharedVerificationIdentity(inputs, []model.Artifact{stale}, independentAuthors, revision)
		if len(inputs[0].Verifications) != 0 || len(inputs[1].Verifications) != 0 {
			t.Fatalf("stale manual verifier identity was projected: %+v", inputs)
		}
	})

	t.Run("manual missing test evidence rejects only that pair", func(t *testing.T) {
		inputs := newInputs(manual.URL)
		inputs[1].Verifications[0].TestEvidence = false
		filterSharedVerificationIdentity(inputs, []model.Artifact{manual}, independentAuthors, revision)
		if len(inputs[0].Verifications) != 1 || len(inputs[1].Verifications) != 0 {
			t.Fatalf("missing manual test evidence did not fail closed per pair: %+v", inputs)
		}
	})

	for _, test := range []struct {
		name  string
		links []string
	}{
		{name: "PROCESS link", links: []string{one.URL, specURL}},
		{name: "SPEC link", links: []string{one.URL, two.URL}},
	} {
		t.Run("manual missing explicit "+test.name+" is rejected", func(t *testing.T) {
			carrier := manualCarrier("Verifier", revision, test.links)
			inputs := newInputs(carrier.URL)
			filterSharedVerificationIdentity(inputs, []model.Artifact{carrier}, independentAuthors, revision)
			if len(inputs[1].Verifications) != 0 {
				t.Fatalf("missing manual %s did not fail closed: %+v", test.name, inputs)
			}
		})
	}

	t.Run("accepted writer and typed agent must agree", func(t *testing.T) {
		tampered := accepted
		tampered.Comment = model.ParseTypedComment(strings.Replace(accepted.Comment.Body, "Agent: Verifier", "Agent: Another Verifier", 1))
		inputs := newInputs(tampered.URL)
		filterSharedVerificationIdentity(inputs, []model.Artifact{tampered}, independentAuthors, revision)
		if len(inputs[0].Verifications) != 0 || len(inputs[1].Verifications) != 0 {
			t.Fatalf("mismatched typed agent established verifier identity: %+v", inputs)
		}
	})

	t.Run("manual missing code author identity fails closed", func(t *testing.T) {
		inputs := newInputs(manual.URL)
		authors := map[string]map[string]map[string]bool{one.Comment.ID: {specID: {"worker one": true}}}
		filterSharedVerificationIdentity(inputs, []model.Artifact{manual}, authors, revision)
		if len(inputs[0].Verifications) != 1 || len(inputs[1].Verifications) != 0 {
			t.Fatalf("missing pair author identity did not fail closed: %+v", inputs)
		}
	})

	t.Run("manual verifier author conflict rejects only conflicting pair", func(t *testing.T) {
		inputs := newInputs(manual.URL)
		authors := map[string]map[string]map[string]bool{
			one.Comment.ID: {specID: {"worker one": true, "verifier": true}},
			two.Comment.ID: {specID: {"worker two": true}},
		}
		filterSharedVerificationIdentity(inputs, []model.Artifact{manual}, authors, revision)
		if len(inputs[0].Verifications) != 0 || len(inputs[1].Verifications) != 1 {
			t.Fatalf("verifier conflict was not scoped to its PROCESS/SPEC pair: %+v", inputs)
		}
	})

	t.Run("single carrier preserves manual compatibility", func(t *testing.T) {
		inputs := newInputs(manual.URL)[:1]
		filterSharedVerificationIdentity(inputs, []model.Artifact{manual},
			map[string]map[string]map[string]bool{one.Comment.ID: {specID: {"worker one": true}}}, revision)
		if len(inputs[0].Verifications) != 1 {
			t.Fatalf("single-carrier manual verification regressed: %+v", inputs)
		}
	})
}

func TestArtifactReferencesRejectIDPrefixCollisions(t *testing.T) {
	process := model.Artifact{URL: "https://example/process-1", Comment: model.TypedComment{ID: "PROCESS-001"}}
	review := model.Artifact{Comment: model.TypedComment{Body: "Reviewed PROCESS-0010 and covered SPEC-0010.", Links: map[string][]string{}}}
	if artifactReferencesProcess(review, process) {
		t.Fatal("PROCESS-0010 must not satisfy PROCESS-001")
	}
	if artifactReferencesSpec(review, "SPEC-001", "https://example/spec-1") {
		t.Fatal("SPEC-0010 must not satisfy SPEC-001")
	}
}

func TestBuildProcessEvidenceMapsAuthoritativeBindingsToExactProcesses(t *testing.T) {
	artifacts := []model.Artifact{
		{URL: "https://example/spec-1", Comment: model.TypedComment{Type: "SPEC", ID: "SPEC-001", Status: "proposed"}},
		{URL: "https://example/spec-2", Comment: model.TypedComment{Type: "SPEC", ID: "SPEC-002", Status: "proposed"}},
		externalProcessArtifact(t, "PROCESS-001"), externalProcessArtifact(t, "PROCESS-002"),
	}
	consumption := externalEvidenceConsumption{ProviderKey: "code.example", ExternalRepository: "acme/widgets-code",
		ChangeID: "change-1", ReferenceVersion: 7, SubjectRevision: "head-new", EvidenceIDs: []string{"review-2", "review-1"}, Bindings: []externalEvidenceBinding{
			{ProcessID: "PROCESS-002", SpecID: "SPEC-002", EvidenceID: "review-2", Kind: codereview.EvidenceReview, SubjectRevision: "head-new", Trusted: true, Source: "native-authoritative-ledger"},
			{ProcessID: "PROCESS-001", SpecID: "SPEC-001", EvidenceID: "review-1", Kind: codereview.EvidenceReview, SubjectRevision: "head-new", Trusted: true, Source: "native-authoritative-ledger"},
		}}
	inputs := buildProcessEvidenceInputs(artifacts, "", nil, reviewSyncReport{}, &consumption)
	if len(inputs) != 2 || len(inputs[0].External) != 1 || len(inputs[1].External) != 1 ||
		inputs[0].External[0].ProcessID != "PROCESS-001" || inputs[1].External[0].ProcessID != "PROCESS-002" {
		t.Fatalf("inputs=%+v", inputs)
	}
	for _, input := range inputs {
		report := gates.EvaluateProcessEvidence(input, gates.TargetFinal, gates.ModeAuthoritative)
		if !report.CarrierRevision.Trusted || report.CarrierRevision.Revision != "head-new" {
			t.Fatalf("process=%s carrier=%+v", input.Process.Comment.ID, report.CarrierRevision)
		}
	}
}

func TestBuildProcessEvidenceRejectsMixedReplayAndUnknownBindings(t *testing.T) {
	artifacts := []model.Artifact{
		{URL: "https://example/spec-1", Comment: model.TypedComment{Type: "SPEC", ID: "SPEC-001", Status: "proposed"}},
		{URL: "https://example/spec-2", Comment: model.TypedComment{Type: "SPEC", ID: "SPEC-002", Status: "proposed"}},
		externalProcessArtifact(t, "PROCESS-001"), externalProcessArtifact(t, "PROCESS-002"),
	}
	valid := externalEvidenceBinding{ProcessID: "PROCESS-001", SpecID: "SPEC-001", EvidenceID: "review-1",
		Kind: codereview.EvidenceReview, SubjectRevision: "head-new", Trusted: true, Source: "native-authoritative-ledger"}
	tests := map[string]struct {
		binding externalEvidenceBinding
		ids     []string
	}{
		"old replay":          {binding: func() externalEvidenceBinding { b := valid; b.SubjectRevision = "head-old"; return b }(), ids: []string{"review-1"}},
		"untrusted":           {binding: func() externalEvidenceBinding { b := valid; b.Trusted = false; return b }(), ids: []string{"review-1"}},
		"unknown evidence id": {binding: func() externalEvidenceBinding { b := valid; b.EvidenceID = "unknown"; return b }(), ids: []string{"review-1"}},
		"unknown process": {binding: func() externalEvidenceBinding {
			b := valid
			b.ProcessID = "PROCESS-999"
			b.EvidenceID = "review-2"
			return b
		}(), ids: []string{"review-1", "review-2"}},
		"inactive spec": {binding: func() externalEvidenceBinding { b := valid; b.SpecID = "SPEC-999"; b.EvidenceID = "review-2"; return b }(), ids: []string{"review-1", "review-2"}},
		"duplicate across process": {binding: externalEvidenceBinding{ProcessID: "PROCESS-002", SpecID: "SPEC-002", EvidenceID: "review-1",
			Kind: codereview.EvidenceReview, SubjectRevision: "head-new", Trusted: true, Source: "native-authoritative-ledger"}, ids: []string{"review-1"}},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			consumption := externalEvidenceConsumption{ProviderKey: "code.example", ExternalRepository: "acme/widgets-code",
				ChangeID: "change-1", ReferenceVersion: 7, SubjectRevision: "head-new", EvidenceIDs: test.ids,
				Bindings: []externalEvidenceBinding{valid, test.binding}}
			inputs := buildProcessEvidenceInputs(artifacts, "", nil, reviewSyncReport{}, &consumption)
			if len(inputs) != 2 || len(inputs[0].External) != 0 || len(inputs[1].External) != 0 {
				t.Fatalf("invalid mixed binding retained carrier: %+v", inputs)
			}
		})
	}
}

func TestBuildProcessEvidenceValidatesAuthorRationaleBeforeCrediting(t *testing.T) {
	const specURL = "https://example/proposal#issuecomment-spec"
	current := processClassArtifact(t, "PROCESS-001", "change-bearing", "SPEC-001", "done")
	historical := processClassArtifact(t, "PROCESS-004", "change-bearing", "SPEC-001", "superseded")
	historicalBody, _, err := model.StampSupersededBy(historical.Comment.Body, historical.Comment.ID,
		model.SupersededBy{ProcessID: current.Comment.ID, URL: current.URL})
	if err != nil {
		t.Fatal(err)
	}
	historical.Comment = model.ParseTypedComment(historicalBody)
	artifacts := []model.Artifact{
		{URL: specURL, Comment: model.TypedComment{Type: "SPEC", ID: "SPEC-001", Status: "proposed"}},
		current, // genuine carrier
		processClassArtifact(t, "PROCESS-002", "review", "SPEC-001", "done"),         // wrong class
		processClassArtifact(t, "PROCESS-003", "change-bearing", "SPEC-999", "done"), // does not cover SPEC-001
		historical, // explicitly historical
	}
	mustRationale := func(agent, process, path string, line int) string {
		body, err := model.RenderRationaleBody(agent, process, "SPEC-001", specURL, "rationale", path, line)
		if err != nil {
			t.Fatal(err)
		}
		return body
	}
	comments := []github.PullRequestReviewComment{
		{Body: mustRationale("coordinator", "PROCESS-001", "real.go", 12), Path: "real.go", Line: 12}, // credited
		{Body: mustRationale("ghost", "PROCESS-001", "forged.go", 99), Path: "real.go", Line: 12},     // forged path/line
		{Body: mustRationale("reviewbot", "PROCESS-002", "real2.go", 5), Path: "real2.go", Line: 5},   // non change-bearing PROCESS
		{Body: mustRationale("wanderer", "PROCESS-003", "real3.go", 7), Path: "real3.go", Line: 7},    // PROCESS does not cover SPEC-001
		{Body: mustRationale("zombie", "PROCESS-004", "real4.go", 9), Path: "real4.go", Line: 9},      // superseded PROCESS
	}
	inputs := buildProcessEvidenceInputs(artifacts, "", comments, reviewSyncReport{}, nil)
	if len(inputs) == 0 {
		t.Fatalf("expected PROCESS inputs, got none")
	}
	authors := inputs[0].AuthorAgentsBySpec["SPEC-001"]
	if !authors["coordinator"] {
		t.Fatalf("validated change-bearing rationale author must be credited: %+v", authors)
	}
	for _, bad := range []string{"ghost", "reviewbot", "wanderer", "zombie"} {
		if authors[bad] {
			t.Fatalf("%q rationale must not credit an author: %+v", bad, authors)
		}
	}
}

func TestBuildProcessEvidenceKeepsLegacySupersededProcessActive(t *testing.T) {
	legacy := processClassArtifact(t, "PROCESS-001", "change-bearing", "SPEC-001", "superseded")
	inputs := buildProcessEvidenceInputs([]model.Artifact{legacy}, "", nil, reviewSyncReport{}, nil)
	if len(inputs) != 1 || inputs[0].Process.Comment.ID != "PROCESS-001" {
		t.Fatalf("legacy status-only supersession must remain active and blocking: %+v", inputs)
	}
}

func TestBuildProcessEvidenceBindsIssueRationaleToExactExternalCarrier(t *testing.T) {
	const specURL = "https://issues.example/acme/widgets/issues/1#issuecomment-2"
	process := processClassArtifact(t, "PROCESS-001", "change-bearing", "SPEC-001", "done")
	process.Issue = 3
	process.Comment.Agent = "Coordinator"
	rationale := func(revision string, version int64, processID, specID string) model.Artifact {
		body, err := model.RenderCodeChangeRationaleBody(model.CodeChangeRationaleMarker{
			Process: processID, Spec: specID, SpecURL: specURL, ProviderKey: "code.example",
			ExternalRepository: "acme/widgets-code", ChangeID: "change-1", ReferenceVersion: version,
			SubjectRevision: revision, Agent: "Worker",
		}, "why")
		if err != nil {
			t.Fatal(err)
		}
		return model.Artifact{Issue: 3, URL: "https://issues.example/acme/widgets/issues/3#issuecomment-rationale",
			Comment: model.ParseTypedComment(body)}
	}
	consumption := externalEvidenceConsumption{ProviderKey: "code.example", ExternalRepository: "acme/widgets-code",
		ChangeID: "change-1", ReferenceVersion: 7, SubjectRevision: "head-current", EvidenceIDs: []string{"review-1"},
		Bindings: []externalEvidenceBinding{{ProcessID: "PROCESS-001", SpecID: "SPEC-001", EvidenceID: "review-1",
			Kind: codereview.EvidenceReview, SubjectRevision: "head-current", Trusted: true, Source: "native-authoritative-ledger"}}}
	base := []model.Artifact{{Issue: 1, URL: specURL, Comment: model.TypedComment{Type: "SPEC", ID: "SPEC-001", Status: "confirmed"}}, process}

	artifacts := append(append([]model.Artifact(nil), base...), rationale("head-current", 7, "PROCESS-001", "SPEC-001"))
	inputs := buildProcessEvidenceInputs(artifacts, "", nil, reviewSyncReport{}, &consumption)
	if len(inputs) != 1 || len(inputs[0].CodeChangeRationales) != 1 || !inputs[0].AuthorAgentsBySpec["SPEC-001"]["worker"] {
		t.Fatalf("exact rationale was not retained and credited: %+v", inputs)
	}
	report := gates.EvaluateProcessEvidence(inputs[0], gates.TargetFinal, gates.ModeAuthoritative)
	if !containsString(report.Satisfied, "exact-current provider rationale") || !report.CarrierRevision.Trusted {
		t.Fatalf("exact provider rationale did not satisfy gate: %+v", report)
	}

	for name, artifact := range map[string]model.Artifact{
		"stale revision": rationale("head-old", 7, "PROCESS-001", "SPEC-001"),
		"stale version":  rationale("head-current", 6, "PROCESS-001", "SPEC-001"),
		"wrong process":  rationale("head-current", 7, "PROCESS-999", "SPEC-001"),
		"wrong spec":     rationale("head-current", 7, "PROCESS-001", "SPEC-999"),
	} {
		t.Run(name, func(t *testing.T) {
			inputs := buildProcessEvidenceInputs(append(append([]model.Artifact(nil), base...), artifact), "", nil, reviewSyncReport{}, &consumption)
			if len(inputs) != 1 || inputs[0].AuthorAgentsBySpec["SPEC-001"]["worker"] {
				t.Fatalf("invalid rationale credited author: %+v", inputs)
			}
			report := gates.EvaluateProcessEvidence(inputs[0], gates.TargetFinal, gates.ModeAuthoritative)
			if containsString(report.Satisfied, "exact-current provider rationale") {
				t.Fatalf("invalid rationale rescued gate: %+v", report)
			}
		})
	}
}

func TestBuildProcessEvidenceBindsSelfHostedReviewToCurrentNativeLedgerReview(t *testing.T) {
	const specURL = "https://issues.example/acme/widgets/issues/1#issuecomment-2"
	reviewProcess := processClassArtifact(t, "PROCESS-002", "review", "SPEC-001", "done")
	reviewBody, err := model.EnsureTypedBody("REVIEW", "REVIEW-001",
		"Reviewed PROCESS-002 covering SPEC-001 with no blocking findings.",
		model.BodyOptions{Agent: "Independent Reviewer", Status: "done"})
	if err != nil {
		t.Fatal(err)
	}
	artifacts := []model.Artifact{
		{Issue: 1, URL: specURL, Comment: model.TypedComment{Type: "SPEC", ID: "SPEC-001", Status: "confirmed"}},
		reviewProcess,
		{Issue: 3, URL: "https://issues.example/acme/widgets/issues/3#issuecomment-review", Comment: model.ParseTypedComment(reviewBody)},
	}
	consumption := externalEvidenceConsumption{ProviderKey: "code.example", ExternalRepository: "acme/widgets-code",
		ChangeID: "change-1", ReferenceVersion: 7, SubjectRevision: "head-current", EvidenceIDs: []string{"review-2"},
		Bindings: []externalEvidenceBinding{{ProcessID: "PROCESS-002", SpecID: "SPEC-001", EvidenceID: "review-2",
			Kind: codereview.EvidenceReview, SubjectRevision: "head-current", Trusted: true, Source: "native-authoritative-ledger"}}}
	stamped, _, err := stampConsumedEvidence(artifacts[2].Comment.Body, consumption)
	if err != nil {
		t.Fatal(err)
	}
	artifacts[2].Comment = model.ParseTypedComment(stamped)
	inputs := buildProcessEvidenceInputs(artifacts, "", nil, reviewSyncReport{}, &consumption)
	if len(inputs) != 1 || len(inputs[0].Reviews) != 1 {
		t.Fatalf("self-hosted review input missing: %+v", inputs)
	}
	review := inputs[0].Reviews[0]
	if !review.Trusted || review.SubjectRevision != "head-current" || review.ReviewerAgent != "Independent Reviewer" ||
		!strings.HasPrefix(review.Source, "native-authoritative-ledger:") {
		t.Fatalf("self-hosted review was not bound to current native ledger evidence: %+v", review)
	}
	report := gates.EvaluateProcessEvidence(inputs[0], gates.TargetFinal, gates.ModeAuthoritative)
	if !report.CarrierRevision.Trusted || report.CarrierRevision.Revision != "head-current" ||
		!containsString(report.Satisfied, "review evidence") {
		t.Fatalf("self-hosted review did not satisfy exact-current carrier: %+v", report)
	}
	unstamped := append([]model.Artifact(nil), artifacts...)
	unstamped[2].Comment = model.ParseTypedComment(reviewBody)
	unstampedInputs := buildProcessEvidenceInputs(unstamped, "", nil, reviewSyncReport{}, &consumption)
	if len(unstampedInputs) != 1 || len(unstampedInputs[0].Reviews) != 1 || unstampedInputs[0].Reviews[0].Trusted {
		t.Fatalf("unstamped REVIEW borrowed native-ledger authority: %+v", unstampedInputs)
	}

	consumption.Bindings[0].Kind = codereview.EvidenceCheck
	inputs = buildProcessEvidenceInputs(artifacts, "", nil, reviewSyncReport{}, &consumption)
	if len(inputs) != 1 || len(inputs[0].Reviews) != 1 || inputs[0].Reviews[0].Trusted {
		t.Fatalf("a check binding must not become independent review authority: %+v", inputs)
	}
}

func TestBuildProcessEvidenceAcceptsFreshCompletionWithExplicitCoverage(t *testing.T) {
	now := time.Date(2026, 7, 17, 8, 0, 0, 0, time.UTC)
	artifacts, gate := externalReviewCompletionFixture(t, now, "Independent Reviewer")
	artifacts = append(artifacts, externalCodeRationaleArtifact(t, artifacts[1], "Worker", gate.Target, "SPEC-001", artifacts[0].URL))
	inputs := buildProcessEvidenceInputsWithExternalReview(artifacts, "", nil, reviewSyncReport{}, nil, &gate, now)
	implementation := processInputByID(t, inputs, "PROCESS-001")
	review := processInputByID(t, inputs, "PROCESS-002")
	if !implementation.AuthorAgentsBySpec["SPEC-001"]["worker"] || len(implementation.External) != 1 ||
		implementation.External[0].EvidenceKind != "review_completion" {
		t.Fatalf("completion did not back exact-current PROCESS rationale: %+v", implementation)
	}
	if len(review.Reviews) != 1 || !review.Reviews[0].Trusted || review.Reviews[0].SubjectRevision != "head-current" ||
		review.Reviews[0].Source != "external-review-completion" {
		t.Fatalf("completion REVIEW carrier missing: %+v", review.Reviews)
	}
	report := gates.EvaluateProcessEvidence(review, gates.TargetFinal, gates.ModeAuthoritative)
	if !report.CarrierRevision.Trusted || report.CarrierRevision.Revision != "head-current" ||
		!containsString(report.Satisfied, "review evidence") {
		t.Fatalf("fresh completion did not satisfy review PROCESS: %+v", report)
	}

	conflictedArtifacts, conflictedGate := externalReviewCompletionFixture(t, now, "Worker")
	conflictedArtifacts = append(conflictedArtifacts,
		externalCodeRationaleArtifact(t, conflictedArtifacts[1], "Worker", conflictedGate.Target, "SPEC-001", conflictedArtifacts[0].URL))
	conflicted := processInputByID(t, buildProcessEvidenceInputsWithExternalReview(conflictedArtifacts, "", nil,
		reviewSyncReport{}, nil, &conflictedGate, now), "PROCESS-002")
	conflictReport := gates.EvaluateProcessEvidence(conflicted, gates.TargetFinal, gates.ModeAuthoritative)
	if !hasDiagnosticCode(conflictReport.Diagnostics, gates.CodeProcessReviewAuthorConflict) ||
		containsString(conflictReport.Satisfied, "review evidence") {
		t.Fatalf("self-review was accepted: %+v", conflictReport)
	}
}

func TestExternalReviewCarrierRequiresExplicitExactCoverageLinks(t *testing.T) {
	now := time.Date(2026, 7, 17, 8, 0, 0, 0, time.UTC)
	tests := map[string][]string{
		"body ids without links": nil,
		"missing review process": {"implementation", "spec"},
		"missing implementation": {"review", "spec"},
		"missing spec":           {"review", "implementation"},
	}
	for name, selections := range tests {
		t.Run(name, func(t *testing.T) {
			artifacts, gate := externalReviewCompletionFixture(t, now, "Independent Reviewer")
			urls := map[string]string{"review": artifacts[2].URL, "implementation": artifacts[1].URL, "spec": artifacts[0].URL}
			body, err := model.EnsureTypedBody("REVIEW", "REVIEW-001",
				"Reviewed PROCESS-002 and implementation PROCESS-001 for SPEC-001.", model.BodyOptions{
					Agent: "Independent Reviewer", Status: "done", SubjectRevision: gate.Target.SubjectRevision})
			if err != nil {
				t.Fatal(err)
			}
			completion, found, err := parseExternalReviewCompletion(artifacts[3].Comment.Body)
			if err != nil || !found {
				t.Fatalf("completion found=%t err=%v", found, err)
			}
			body, _, err = stampExternalReviewCompletion(body, completion)
			if err != nil {
				t.Fatal(err)
			}
			for _, selection := range selections {
				body, _, err = model.AddRelatedCommentLink(body, urls[selection])
				if err != nil {
					t.Fatal(err)
				}
			}
			artifacts[3].Comment = model.ParseTypedComment(body)
			input := processInputByID(t, buildProcessEvidenceInputsWithExternalReview(artifacts, "", nil,
				reviewSyncReport{}, nil, &gate, now), "PROCESS-002")
			if len(input.Reviews) != 0 {
				t.Fatalf("invalid link carrier accepted: %+v", input.Reviews)
			}
		})
	}

	t.Run("stale spec link", func(t *testing.T) {
		artifacts, gate := externalReviewCompletionFixture(t, now, "Independent Reviewer")
		stale := model.Artifact{URL: "https://issues.example/spec-stale",
			Comment: model.TypedComment{Type: "SPEC", ID: "SPEC-099", Status: "superseded"}}
		body, _, err := model.AddRelatedCommentLink(artifacts[3].Comment.Body, stale.URL)
		if err != nil {
			t.Fatal(err)
		}
		artifacts[3].Comment = model.ParseTypedComment(body)
		artifacts = append(artifacts, stale)
		input := processInputByID(t, buildProcessEvidenceInputsWithExternalReview(artifacts, "", nil,
			reviewSyncReport{}, nil, &gate, now), "PROCESS-002")
		if len(input.Reviews) != 0 {
			t.Fatalf("stale SPEC link accepted: %+v", input.Reviews)
		}
	})

	t.Run("mixed spec coverage", func(t *testing.T) {
		artifacts, gate := externalReviewCompletionFixture(t, now, "Independent Reviewer")
		secondSpec := model.Artifact{URL: "https://issues.example/spec-2",
			Comment: model.TypedComment{Type: "SPEC", ID: "SPEC-002", Status: "confirmed"}}
		artifacts = append(artifacts, secondSpec)
		artifacts[1] = processClassArtifact(t, "PROCESS-001", "change-bearing", "SPEC-001\n- SPEC-002", "done")
		artifacts[1].URL, artifacts[1].Issue = "https://issues.example/process-1", 9
		artifacts[2] = processClassArtifact(t, "PROCESS-002", "review", "SPEC-001\n- SPEC-002", "done")
		artifacts[2].URL, artifacts[2].Issue = "https://issues.example/process-2", 9
		// The carrier remains linked only to SPEC-001, so it cannot cover the
		// mixed two-SPEC implementation/review edge.
		input := processInputByID(t, buildProcessEvidenceInputsWithExternalReview(artifacts, "", nil,
			reviewSyncReport{}, nil, &gate, now), "PROCESS-002")
		if len(input.Reviews) != 0 {
			t.Fatalf("partially linked multi-SPEC carrier accepted: %+v", input.Reviews)
		}
	})
}

func TestExternalReviewCompletionCarrierRejectsStaleIdentityAndRevision(t *testing.T) {
	now := time.Date(2026, 7, 17, 8, 0, 0, 0, time.UTC)
	tests := map[string]func(*model.Artifact){
		"change identity": func(review *model.Artifact) {
			completion, _, _ := parseExternalReviewCompletion(review.Comment.Body)
			completion.ChangeID = "change-old"
			body, _, err := stampExternalReviewCompletion(review.Comment.Body, completion)
			if err != nil {
				t.Fatal(err)
			}
			review.Comment = model.ParseTypedComment(body)
		},
		"reference version": func(review *model.Artifact) {
			completion, _, _ := parseExternalReviewCompletion(review.Comment.Body)
			completion.ReferenceVersion--
			body, _, err := stampExternalReviewCompletion(review.Comment.Body, completion)
			if err != nil {
				t.Fatal(err)
			}
			review.Comment = model.ParseTypedComment(body)
		},
		"subject revision": func(review *model.Artifact) {
			review.Comment = model.ParseTypedComment(strings.Replace(review.Comment.Body,
				"Subject Revision: head-current", "Subject Revision: head-old", 1))
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			artifacts, gate := externalReviewCompletionFixture(t, now, "Independent Reviewer")
			mutate(&artifacts[3])
			input := processInputByID(t, buildProcessEvidenceInputsWithExternalReview(artifacts, "", nil,
				reviewSyncReport{}, nil, &gate, now), "PROCESS-002")
			if len(input.Reviews) != 1 || input.Reviews[0].Trusted {
				t.Fatalf("stale completion identity became trusted: %+v", input.Reviews)
			}
		})
	}
}

func TestLegacyExternalReviewCarrierUsesActiveRecordsAndEarliestFreshness(t *testing.T) {
	now := time.Date(2026, 7, 17, 8, 0, 0, 0, time.UTC)
	tests := map[string]struct {
		times      []time.Time
		superseded bool
		noReview   bool
		trusted    bool
	}{
		"required policy compatibility": {times: []time.Time{now.Add(-30 * time.Minute)}, trusted: true},
		"earliest observation stale":    {times: []time.Time{now.Add(-30 * time.Minute), now.Add(-2 * time.Hour)}},
		"consumed record superseded":    {times: []time.Time{now.Add(-30 * time.Minute)}, superseded: true},
		"no consumed review":            {times: []time.Time{now.Add(-30 * time.Minute)}, noReview: true},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			artifacts, gate := legacyExternalReviewFixture(t, now, test.times, test.superseded, test.noReview)
			input := processInputByID(t, buildProcessEvidenceInputsWithExternalReview(artifacts, "", nil,
				reviewSyncReport{}, nil, &gate, now), "PROCESS-002")
			if len(input.Reviews) != 1 || input.Reviews[0].Trusted != test.trusted {
				t.Fatalf("legacy carrier trusted=%t want=%t reviews=%+v", len(input.Reviews) == 1 && input.Reviews[0].Trusted,
					test.trusted, input.Reviews)
			}
			if test.trusted && input.Reviews[0].Source != "native-authoritative-ledger:review-1" {
				t.Fatalf("legacy source=%q", input.Reviews[0].Source)
			}
		})
	}
}

func externalReviewCompletionFixture(t *testing.T, now time.Time, reviewer string) ([]model.Artifact, externalGateResult) {
	t.Helper()
	reference := codereview.Reference{ProviderKey: "code.example", ExternalRepository: "acme/widgets-code", ChangeID: "change-1"}
	target := coreevidence.NativeTarget{Reference: reference, ReferenceVersion: 7, SubjectRevision: "head-current"}
	gate := externalGateResult{Evaluation: coreevidence.Result{Passed: true}, Target: target,
		ReviewCompletionPolicy: ReviewCompletionPolicy{Required: true, Freshness: time.Hour},
		Snapshot: codereview.Snapshot{ProtocolVersion: codereview.ProtocolVersion, Reference: reference,
			SubjectRevision: target.SubjectRevision, CapturedAt: now},
		Consumption: externalEvidenceConsumption{ProviderKey: reference.ProviderKey,
			ExternalRepository: reference.ExternalRepository, ChangeID: reference.ChangeID,
			ReferenceVersion: target.ReferenceVersion, SubjectRevision: target.SubjectRevision}}
	spec := model.Artifact{Issue: 1, URL: "https://issues.example/spec-1",
		Comment: model.TypedComment{Type: "SPEC", ID: "SPEC-001", Status: "confirmed"}}
	implementation := processClassArtifact(t, "PROCESS-001", "change-bearing", "SPEC-001", "done")
	implementation.Issue, implementation.URL = 9, "https://issues.example/process-1"
	reviewProcess := processClassArtifact(t, "PROCESS-002", "review", "SPEC-001", "done")
	reviewProcess.Issue, reviewProcess.URL = 9, "https://issues.example/process-2"
	body, err := renderExternalReviewSyncCommentAt("REVIEW-001", reviewer,
		writerSession{}, "external review", gate, now.Add(-15*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	for _, url := range []string{reviewProcess.URL, implementation.URL, spec.URL} {
		body, _, err = model.AddRelatedCommentLink(body, url)
		if err != nil {
			t.Fatal(err)
		}
	}
	review := model.Artifact{Issue: 9, URL: "https://issues.example/review-1", Comment: model.ParseTypedComment(body)}
	return []model.Artifact{spec, implementation, reviewProcess, review}, gate
}

func externalCodeRationaleArtifact(t *testing.T, process model.Artifact, agent string,
	target coreevidence.NativeTarget, specID, specURL string) model.Artifact {
	t.Helper()
	body, err := model.RenderCodeChangeRationaleBody(model.CodeChangeRationaleMarker{
		Process: process.Comment.ID, Spec: specID, SpecURL: specURL, ProviderKey: target.Reference.ProviderKey,
		ExternalRepository: target.Reference.ExternalRepository, ChangeID: target.Reference.ChangeID,
		ReferenceVersion: target.ReferenceVersion, SubjectRevision: target.SubjectRevision, Agent: agent,
		AgentSessionID: "", AgentSessionSource: "",
	}, "implementation rationale")
	if err != nil {
		t.Fatal(err)
	}
	return model.Artifact{Issue: process.Issue, URL: "https://issues.example/rationale-1", Comment: model.ParseTypedComment(body)}
}

func legacyExternalReviewFixture(t *testing.T, now time.Time, observed []time.Time,
	superseded, noReview bool) ([]model.Artifact, externalGateResult) {
	t.Helper()
	artifacts, gate := externalReviewCompletionFixture(t, now, "Independent Reviewer")
	consumption := gate.Consumption
	for index, observedAt := range observed {
		id := fmt.Sprintf("review-%d", index+1)
		kind := codereview.EvidenceReview
		if noReview {
			kind = codereview.EvidenceCheck
		}
		record := codereview.EvidenceRecord{ID: id, Kind: kind, State: "resolved", Name: "review-check",
			SubjectRevision: gate.Target.SubjectRevision, ObservedAt: observedAt, Trusted: true,
			WriterIdentity: "bridge:test", PayloadDigest: "sha256:test"}
		if kind == codereview.EvidenceReview {
			record.Severity, record.FindingID, record.ProcessID, record.SpecID = "P2",
				fmt.Sprintf("FINDING-%03d", index+1), "PROCESS-001", "SPEC-001"
		}
		gate.Snapshot.Records = append(gate.Snapshot.Records, record)
		consumption.EvidenceIDs = append(consumption.EvidenceIDs, id)
		consumption.Bindings = append(consumption.Bindings, externalEvidenceBinding{ProcessID: "PROCESS-001",
			SpecID: "SPEC-001", EvidenceID: id, Kind: kind, SubjectRevision: gate.Target.SubjectRevision,
			Trusted: true, Source: "native-authoritative-ledger"})
	}
	if superseded {
		gate.Snapshot.Records = append(gate.Snapshot.Records, codereview.EvidenceRecord{ID: "review-successor",
			Kind: codereview.EvidenceReview, State: "resolved", SubjectRevision: gate.Target.SubjectRevision,
			Severity: "P2", FindingID: "FINDING-001", ProcessID: "PROCESS-001", SpecID: "SPEC-001",
			ObservedAt: observed[0].Add(time.Minute), Trusted: true, WriterIdentity: "bridge:test",
			PayloadDigest: "sha256:test-successor", SupersedesID: "review-1"})
	}
	body, err := model.EnsureTypedBody("REVIEW", "REVIEW-001", "Legacy synchronized external review.",
		model.BodyOptions{Agent: "Independent Reviewer", Status: "done", SubjectRevision: gate.Target.SubjectRevision})
	if err != nil {
		t.Fatal(err)
	}
	for _, url := range []string{artifacts[2].URL, artifacts[1].URL, artifacts[0].URL} {
		body, _, err = model.AddRelatedCommentLink(body, url)
		if err != nil {
			t.Fatal(err)
		}
	}
	body, _, err = stampConsumedEvidence(body, consumption)
	if err != nil {
		t.Fatal(err)
	}
	artifacts[3].Comment = model.ParseTypedComment(body)
	gate.Consumption = consumption
	gate.Evaluation.EvidenceIDs = append([]string(nil), consumption.EvidenceIDs...)
	return artifacts, gate
}

func processInputByID(t *testing.T, inputs []gates.ProcessEvidenceInput, id string) gates.ProcessEvidenceInput {
	t.Helper()
	for _, input := range inputs {
		if input.Process.Comment.ID == id {
			return input
		}
	}
	t.Fatalf("PROCESS input %s missing: %+v", id, inputs)
	return gates.ProcessEvidenceInput{}
}

func hasDiagnosticCode(diagnostics []gates.Diagnostic, code string) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == code {
			return true
		}
	}
	return false
}

func processClassArtifact(t *testing.T, id, class, spec, status string) model.Artifact {
	t.Helper()
	body, err := model.EnsureTypedBody("PROCESS", id, "## Process: n\n\n### Parent TASK\n\n- TASK-001\n\n### Execution Class\n\n- "+class+"\n\n### Covers\n\n- "+spec, model.BodyOptions{Status: status})
	if err != nil {
		t.Fatal(err)
	}
	return model.Artifact{URL: "https://example/" + strings.ToLower(id), Comment: model.ParseTypedComment(body)}
}

func externalProcessArtifact(t *testing.T, processID string) model.Artifact {
	t.Helper()
	specID := strings.Replace(processID, "PROCESS-", "SPEC-", 1)
	body, err := model.EnsureTypedBody("PROCESS", processID, "## Process: external\n\n### Execution Class\n\n- external\n\n### Covers\n\n- "+specID, model.BodyOptions{Status: "done"})
	if err != nil {
		t.Fatal(err)
	}
	return model.Artifact{URL: "https://example/" + strings.ToLower(processID), Comment: model.ParseTypedComment(body)}
}

func TestBuildProcessEvidenceUsesCollectorRevisionNotTypedText(t *testing.T) {
	const taskURL = "https://example/task"
	const prURL = "https://example/pr"
	processBody, err := model.EnsureTypedBody("PROCESS", "PROCESS-001", "## Process: verify\n\n### Execution Class\n\n- verification\n\n### Required Checks\n\n- unit tests\n\n### Covers\n\n- SPEC-001", model.BodyOptions{Status: "done", Links: map[string][]string{"Related Comments": {taskURL}, "PR": {prURL}}})
	if err != nil {
		t.Fatal(err)
	}
	verifyBody, err := model.EnsureTypedBody("VERIFY", "VERIFY-001", "Verified PROCESS-001 and SPEC-001 with tests. Claimed head-old in text.", model.BodyOptions{Status: "done"})
	if err != nil {
		t.Fatal(err)
	}
	artifacts := []model.Artifact{
		{URL: "https://example/spec", Comment: model.TypedComment{Type: "SPEC", ID: "SPEC-001", Status: "proposed"}},
		{URL: taskURL, Comment: model.TypedComment{Type: "TASK", ID: "TASK-001", Status: "done"}},
		{URL: "https://example/process", Comment: model.ParseTypedComment(processBody)},
		{URL: "https://example/verify", Comment: model.ParseTypedComment(verifyBody)},
	}
	review := reviewSyncReport{PassedChecks: []reviewCheck{{Name: "unit tests", SubjectRevision: "head-new", Trusted: true, Source: "github-check-run:7"}}}
	inputs := buildProcessEvidenceInputs(artifacts, prURL, nil, review, nil)
	if len(inputs) != 1 || len(inputs[0].Checks) != 1 {
		t.Fatalf("inputs = %+v", inputs)
	}
	report := gates.EvaluateProcessEvidence(inputs[0], gates.TargetFinal, gates.ModeAuthoritative)
	if report.CarrierRevision.Revision != "head-new" || !report.CarrierRevision.Trusted {
		t.Fatalf("typed text overrode collector revision: %+v", report.CarrierRevision)
	}
}

func TestBuildProcessEvidenceBindsZeroFindingReviewToAuthoritativeHead(t *testing.T) {
	const (
		prURL   = "https://github.com/o/r/pull/7"
		current = "0123456789abcdef0123456789abcdef01234567"
		stale   = "89abcdef0123456789abcdef0123456789abcdef"
	)
	report := reviewSyncReport{PR: 7, PRURL: prURL, SubjectRevision: current, RevisionSource: "github-pull-request-head:7"}
	tests := map[string]struct {
		revision string
		prLinks  []string
		trusted  bool
	}{
		"matching head":   {revision: current, prLinks: []string{prURL}, trusted: true},
		"stale head":      {revision: stale, prLinks: []string{prURL}},
		"different PR":    {revision: current, prLinks: []string{"https://github.com/o/r/pull/8"}},
		"ambiguous PR":    {revision: current, prLinks: []string{prURL, "https://github.com/o/r/pull/8"}},
		"missing carrier": {revision: current},
		"legacy review":   {prLinks: []string{prURL}},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			reviewBody, err := model.EnsureTypedBody("REVIEW", "REVIEW-001", "Reviewed PROCESS-001 and SPEC-001 with zero findings.", model.BodyOptions{
				Status: "done", SubjectRevision: test.revision, Links: map[string][]string{"PR": test.prLinks},
			})
			if err != nil {
				t.Fatal(err)
			}
			artifacts := []model.Artifact{
				{URL: "https://example/spec", Comment: model.TypedComment{Type: "SPEC", ID: "SPEC-001", Status: "proposed"}},
				processClassArtifact(t, "PROCESS-001", "review", "SPEC-001", "done"),
				{URL: "https://example/review", Comment: model.ParseTypedComment(reviewBody)},
			}
			inputs := buildProcessEvidenceInputs(artifacts, prURL, nil, report, nil)
			if len(inputs) != 1 || len(inputs[0].Reviews) != 1 {
				t.Fatalf("inputs = %+v", inputs)
			}
			got := gates.EvaluateProcessEvidence(inputs[0], gates.TargetFinal, gates.ModeAuthoritative).CarrierRevision
			if test.trusted {
				if !got.Known || !got.Trusted || got.Revision != current || got.Source != report.RevisionSource {
					t.Fatalf("matching zero-finding review carrier = %+v", got)
				}
			} else if got.Trusted || got.Known {
				t.Fatalf("unbound review became trusted: %+v", got)
			}
		})
	}
}

func TestBuildProcessEvidenceCurrentReviewSupersedesOldResolvedFindingRevision(t *testing.T) {
	const (
		prURL   = "https://github.com/o/r/pull/7"
		old     = "89abcdef0123456789abcdef0123456789abcdef"
		current = "0123456789abcdef0123456789abcdef01234567"
	)
	reviewBody, err := model.EnsureTypedBody("REVIEW", "REVIEW-001", "Reviewed PROCESS-001 and SPEC-001 at the current head.", model.BodyOptions{
		Status: "done", SubjectRevision: current, Links: map[string][]string{"PR": {prURL}},
	})
	if err != nil {
		t.Fatal(err)
	}
	artifacts := []model.Artifact{
		{URL: "https://example/spec", Comment: model.TypedComment{Type: "SPEC", ID: "SPEC-001", Status: "proposed"}},
		processClassArtifact(t, "PROCESS-001", "review", "SPEC-001", "done"),
		{URL: "https://example/review", Comment: model.ParseTypedComment(reviewBody)},
	}
	report := reviewSyncReport{PR: 7, PRURL: prURL, SubjectRevision: current, RevisionSource: "github-pull-request-head:7",
		ResolvedFindings: []reviewFinding{{Process: "PROCESS-001", Spec: "SPEC-001", URL: "https://example/finding", Agent: "Reviewer",
			SubjectRevision: old, RevisionSource: "github-pr-review-comment:3"}}}
	inputs := buildProcessEvidenceInputs(artifacts, prURL, nil, report, nil)
	if len(inputs) != 1 || len(inputs[0].Reviews) != 2 {
		t.Fatalf("review inputs did not preserve eligibility candidates: %+v", inputs)
	}
	got := gates.EvaluateProcessEvidence(inputs[0], gates.TargetFinal, gates.ModeAuthoritative).CarrierRevision
	if !got.Known || !got.Trusted || got.Revision != current || got.Source != report.RevisionSource {
		t.Fatalf("current review carrier = %+v", got)
	}
}

func TestBuildProcessEvidenceIndependentFindingSurvivesCurrentSelfReview(t *testing.T) {
	const (
		prURL   = "https://github.com/o/r/pull/7"
		current = "0123456789abcdef0123456789abcdef01234567"
		specURL = "https://example/spec"
	)
	reviewBody, err := model.EnsureTypedBody("REVIEW", "REVIEW-001", "Reviewed PROCESS-002 and SPEC-001 at the current head.", model.BodyOptions{
		Agent: "Worker", Status: "done", SubjectRevision: current, Links: map[string][]string{"PR": {prURL}},
	})
	if err != nil {
		t.Fatal(err)
	}
	artifacts := []model.Artifact{
		{URL: specURL, Comment: model.TypedComment{Type: "SPEC", ID: "SPEC-001", Status: "proposed"}},
		processClassArtifact(t, "PROCESS-001", "change-bearing", "SPEC-001", "done"),
		processClassArtifact(t, "PROCESS-002", "review", "SPEC-001", "done"),
		{URL: "https://example/self-review", Comment: model.ParseTypedComment(reviewBody)},
	}
	rationale, err := model.RenderRationaleBody("Worker", "PROCESS-001", "SPEC-001", specURL, "rationale", "internal/x.go", 12)
	if err != nil {
		t.Fatal(err)
	}
	report := reviewSyncReport{PR: 7, PRURL: prURL, SubjectRevision: current, RevisionSource: "github-pull-request-head:7",
		ResolvedFindings: []reviewFinding{{Process: "PROCESS-002", Spec: "SPEC-001", URL: "https://example/finding", Agent: "Independent Reviewer",
			SubjectRevision: current, RevisionSource: "github-pr-review-comment:3"}}}
	inputs := buildProcessEvidenceInputs(artifacts, prURL, []github.PullRequestReviewComment{{Body: rationale, Path: "internal/x.go", Line: 12}}, report, nil)
	var reviewInput *gates.ProcessEvidenceInput
	for i := range inputs {
		if inputs[i].Process.Comment.ID == "PROCESS-002" {
			reviewInput = &inputs[i]
			break
		}
	}
	if reviewInput == nil || len(reviewInput.Reviews) != 2 {
		t.Fatalf("review input candidates = %+v", reviewInput)
	}
	evaluated := gates.EvaluateProcessEvidence(*reviewInput, gates.TargetFinal, gates.ModeAuthoritative)
	if hasProcessDiagnostic(evaluated.Diagnostics, gates.CodeProcessReviewAuthorConflict) ||
		!containsProcessSatisfied(evaluated.Satisfied, "review evidence") || !evaluated.CarrierRevision.Trusted ||
		evaluated.CarrierRevision.Source != "github-pr-review-comment:3" {
		t.Fatalf("independent finding did not survive current self-review: %+v", evaluated)
	}
}

func TestBuildProcessEvidenceResolvedFindingRevisionRemainsCompatible(t *testing.T) {
	const revision = "0123456789abcdef0123456789abcdef01234567"
	reviewBody, err := model.EnsureTypedBody("REVIEW", "REVIEW-001", "Reviewed PROCESS-001 and SPEC-001.", model.BodyOptions{Status: "done"})
	if err != nil {
		t.Fatal(err)
	}
	artifacts := []model.Artifact{
		{URL: "https://example/spec", Comment: model.TypedComment{Type: "SPEC", ID: "SPEC-001", Status: "proposed"}},
		processClassArtifact(t, "PROCESS-001", "review", "SPEC-001", "done"),
		{URL: "https://example/review", Comment: model.ParseTypedComment(reviewBody)},
	}
	report := reviewSyncReport{ResolvedFindings: []reviewFinding{{
		Process: "PROCESS-001", Spec: "SPEC-001", URL: "https://example/finding", Agent: "Reviewer",
		SubjectRevision: revision, RevisionSource: "github-pr-review-comment:3",
	}}}
	inputs := buildProcessEvidenceInputs(artifacts, "https://github.com/o/r/pull/7", nil, report, nil)
	if len(inputs) != 1 || len(inputs[0].Reviews) != 2 {
		t.Fatalf("inputs = %+v", inputs)
	}
	got := gates.EvaluateProcessEvidence(inputs[0], gates.TargetFinal, gates.ModeAuthoritative).CarrierRevision
	if !got.Known || !got.Trusted || got.Revision != revision || got.Source != "github-pr-review-comment:3" {
		t.Fatalf("resolved-finding carrier changed: %+v", got)
	}
}

func hasProcessDiagnostic(diagnostics []gates.Diagnostic, code string) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == code {
			return true
		}
	}
	return false
}

func containsProcessSatisfied(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestBuildProcessEvidenceExternalSubstringCannotCreateBinding(t *testing.T) {
	processBody, err := model.EnsureTypedBody("PROCESS", "PROCESS-001", "## Process: external\n\n### Execution Class\n\n- external\n\n### Covers\n\n- SPEC-001\n\nProvider revision head-abc", model.BodyOptions{Status: "done"})
	if err != nil {
		t.Fatal(err)
	}
	artifacts := []model.Artifact{
		{URL: "https://example/spec", Comment: model.TypedComment{Type: "SPEC", ID: "SPEC-001", Status: "proposed"}},
		{URL: "https://example/process", Comment: model.ParseTypedComment(processBody)},
	}
	external := &externalEvidenceConsumption{ProviderKey: "code.example", ExternalRepository: "acme/widgets-code",
		ChangeID: "change-1", ReferenceVersion: 7, SubjectRevision: "head-abc", EvidenceIDs: []string{"check-1"}}
	inputs := buildProcessEvidenceInputs(artifacts, "", nil, reviewSyncReport{}, external)
	if len(inputs) != 1 || len(inputs[0].External) != 0 {
		t.Fatalf("PROCESS body substring created external trust: %+v", inputs)
	}
	if strings.Contains(processBody, "head-abc") == false {
		t.Fatal("fixture must exercise the old substring path")
	}
}

func TestArtifactReferencesAcceptExactIDsAndCanonicalLinks(t *testing.T) {
	process := model.Artifact{URL: "https://example/process-1", Comment: model.TypedComment{ID: "PROCESS-001"}}
	review := model.Artifact{Comment: model.TypedComment{Body: "Reviewed PROCESS-001; covered SPEC-001.", Links: map[string][]string{}}}
	if !artifactReferencesProcess(review, process) || !artifactReferencesSpec(review, "SPEC-001", "https://example/spec-1") {
		t.Fatal("exact typed IDs should be accepted")
	}
	linked := model.Artifact{Comment: model.TypedComment{Links: map[string][]string{"Related Comments": {process.URL, "https://example/spec-1"}}}}
	if !artifactReferencesProcess(linked, process) || !artifactReferencesSpec(linked, "SPEC-001", "https://example/spec-1") {
		t.Fatal("canonical related links should be accepted")
	}
}

func TestCanonicalEvidenceIndexMapsSharedReceiptWithoutMutatingInputs(t *testing.T) {
	for _, kind := range []CanonicalEvidenceKind{CanonicalEvidenceReview, CanonicalEvidenceVerification} {
		t.Run(string(kind), func(t *testing.T) {
			records := []CanonicalEvidenceRecord{
				canonicalRoleEvidence("PROCESS-001", "SPEC-001", "receipt-shared", kind),
				canonicalRoleEvidence("PROCESS-002", "SPEC-001", "receipt-shared", kind),
			}
			before := append([]CanonicalEvidenceRecord(nil), records...)
			index, err := BuildCanonicalEvidenceIndex(records, "head-current")
			if err != nil {
				t.Fatal(err)
			}
			if index.Len() != 2 || len(index.Records("PROCESS-001", "SPEC-001", kind)) != 1 ||
				len(index.Records("PROCESS-002", "SPEC-001", kind)) != 1 {
				t.Fatalf("shared receipt did not map to both active pairs: %+v", index)
			}
			if !reflect.DeepEqual(records, before) {
				t.Fatalf("index builder mutated caller records: before=%+v after=%+v", before, records)
			}
			copyOfRecords := index.Records("PROCESS-001", "SPEC-001", kind)
			copyOfRecords[0].EvidenceID = "mutated"
			if got := index.Records("PROCESS-001", "SPEC-001", kind)[0].EvidenceID; got != "receipt-shared" {
				t.Fatalf("Records exposed mutable index state: %q", got)
			}
		})
	}
}

func TestCanonicalEvidenceIndexRejectsStaleConflictingAndForgedEvidence(t *testing.T) {
	valid := canonicalRoleEvidence("PROCESS-001", "SPEC-001", "receipt-1", CanonicalEvidenceReview)
	tests := map[string]CanonicalEvidenceRecord{
		"stale revision": func() CanonicalEvidenceRecord {
			record := valid
			record.SubjectRevision = "head-old"
			return record
		}(),
		"digest mismatch": func() CanonicalEvidenceRecord {
			record := valid
			record.ReceiptDigest = "not-a-digest"
			return record
		}(),
		"forged prose": func() CanonicalEvidenceRecord {
			record := valid
			record.Source = "typed-body-prose"
			return record
		}(),
	}
	for name, record := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := BuildCanonicalEvidenceIndex([]CanonicalEvidenceRecord{record}, "head-current"); err == nil {
				t.Fatal("invalid evidence entered the canonical index")
			}
		})
	}
	conflict := valid
	conflict.ReceiptDigest = strings.Repeat("c", 64)
	if _, err := BuildCanonicalEvidenceIndex([]CanonicalEvidenceRecord{valid, conflict}, "head-current"); err == nil ||
		!strings.Contains(err.Error(), "conflicting receipt identity or digest") {
		t.Fatalf("digest conflict did not fail closed: %v", err)
	}
}

func TestCanonicalEvidenceIndexAcceptsGitHubAndSelfHostedProviderSubjects(t *testing.T) {
	provider := func(id, source string) CanonicalEvidenceRecord {
		return CanonicalEvidenceRecord{ProcessID: "PROCESS-001", SpecID: "SPEC-001", Kind: CanonicalEvidenceCheck,
			Authority: CanonicalEvidenceProviderOwned, EvidenceID: id, SubjectRevision: "head-current",
			Source: source, Trusted: true}
	}
	index, err := BuildCanonicalEvidenceIndex([]CanonicalEvidenceRecord{
		provider("github-check-1", "github-check-run:101"),
		provider("native-check-1", "native-evidence:check-1"),
	}, "head-current")
	if err != nil {
		t.Fatal(err)
	}
	if got := index.Records("PROCESS-001", "SPEC-001", CanonicalEvidenceCheck); len(got) != 2 ||
		got[0].EvidenceID != "github-check-1" || got[1].EvidenceID != "native-check-1" {
		t.Fatalf("provider-neutral subjects = %+v", got)
	}
}

func TestCanonicalEvidenceIndexHasFixedUpperBound(t *testing.T) {
	records := []CanonicalEvidenceRecord{
		canonicalRoleEvidence("PROCESS-001", "SPEC-001", "receipt-1", CanonicalEvidenceReview),
		canonicalRoleEvidence("PROCESS-002", "SPEC-001", "receipt-2", CanonicalEvidenceReview),
	}
	if _, err := buildCanonicalEvidenceIndex(records, "head-current", 1); err == nil || !strings.Contains(err.Error(), "bounded limit 1") {
		t.Fatalf("bounded index accepted excess entries: %v", err)
	}
}

func canonicalRoleEvidence(processID, specID, receiptID string, kind CanonicalEvidenceKind) CanonicalEvidenceRecord {
	source := "accepted-verification-receipt:self-reported-tests"
	if kind == CanonicalEvidenceReview {
		source = "accepted-review-receipt:self-reported"
	}
	return CanonicalEvidenceRecord{ProcessID: processID, SpecID: specID, Kind: kind,
		Authority: CanonicalEvidenceRoleOwned, EvidenceID: receiptID, ReceiptID: receiptID,
		ReceiptDigest: strings.Repeat("a", 64), AssignmentID: "assignment-1",
		AssignmentDigest: strings.Repeat("b", 64), SubjectRevision: "head-current",
		URL: "https://example/evidence/" + receiptID, Source: source, Trusted: true}
}
