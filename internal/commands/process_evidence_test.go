package commands

import (
	"strings"
	"testing"

	"github.com/higress-group/issue-spec/internal/codereview"
	"github.com/higress-group/issue-spec/internal/gates"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/model"
)

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
	consumption := externalEvidenceConsumption{SubjectRevision: "head-new", EvidenceIDs: []string{"review-2", "review-1"}, Bindings: []externalEvidenceBinding{
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
			consumption := externalEvidenceConsumption{SubjectRevision: "head-new", EvidenceIDs: test.ids, Bindings: []externalEvidenceBinding{valid, test.binding}}
			inputs := buildProcessEvidenceInputs(artifacts, "", nil, reviewSyncReport{}, &consumption)
			if len(inputs) != 2 || len(inputs[0].External) != 0 || len(inputs[1].External) != 0 {
				t.Fatalf("invalid mixed binding retained carrier: %+v", inputs)
			}
		})
	}
}

func TestBuildProcessEvidenceValidatesAuthorRationaleBeforeCrediting(t *testing.T) {
	const specURL = "https://example/proposal#issuecomment-spec"
	artifacts := []model.Artifact{
		{URL: specURL, Comment: model.TypedComment{Type: "SPEC", ID: "SPEC-001", Status: "proposed"}},
	}
	valid, err := model.RenderRationaleBody("coordinator", "PROCESS-001", "SPEC-001", specURL, "rationale", "real.go", 12)
	if err != nil {
		t.Fatal(err)
	}
	// Forged marker: claims agent "ghost" authored SPEC-001 but its marker path/line
	// do not match the hosting comment, so it must not credit an author identity.
	forged, err := model.RenderRationaleBody("ghost", "PROCESS-001", "SPEC-001", specURL, "rationale", "forged.go", 99)
	if err != nil {
		t.Fatal(err)
	}
	comments := []github.PullRequestReviewComment{
		{Body: valid, Path: "real.go", Line: 12},
		{Body: forged, Path: "real.go", Line: 12},
	}
	inputs := buildProcessEvidenceInputs(artifacts, "", comments, reviewSyncReport{}, nil)
	if len(inputs) != 0 {
		t.Fatalf("no PROCESS artifacts present, unexpected inputs: %+v", inputs)
	}
	// Re-run with a review PROCESS so AuthorAgentsBySpec is observable on the input.
	processBody, err := model.EnsureTypedBody("PROCESS", "PROCESS-001", "## Process: review\n\n### Parent TASK\n\n- TASK-001\n\n### Execution Class\n\n- review\n\n### Covers\n\n- SPEC-001", model.BodyOptions{Status: "done"})
	if err != nil {
		t.Fatal(err)
	}
	artifacts = append(artifacts, model.Artifact{URL: "https://example/process", Comment: model.ParseTypedComment(processBody)})
	inputs = buildProcessEvidenceInputs(artifacts, "", comments, reviewSyncReport{}, nil)
	if len(inputs) != 1 {
		t.Fatalf("inputs = %+v", inputs)
	}
	authors := inputs[0].AuthorAgentsBySpec["SPEC-001"]
	if !authors["coordinator"] {
		t.Fatalf("validated rationale author must be credited: %+v", authors)
	}
	if authors["ghost"] {
		t.Fatalf("forged path/line rationale must not credit an author: %+v", authors)
	}
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

func TestBuildProcessEvidenceExternalSubstringCannotCreateBinding(t *testing.T) {
	processBody, err := model.EnsureTypedBody("PROCESS", "PROCESS-001", "## Process: external\n\n### Execution Class\n\n- external\n\n### Covers\n\n- SPEC-001\n\nProvider revision head-abc", model.BodyOptions{Status: "done"})
	if err != nil {
		t.Fatal(err)
	}
	artifacts := []model.Artifact{
		{URL: "https://example/spec", Comment: model.TypedComment{Type: "SPEC", ID: "SPEC-001", Status: "proposed"}},
		{URL: "https://example/process", Comment: model.ParseTypedComment(processBody)},
	}
	external := &externalEvidenceConsumption{SubjectRevision: "head-abc", EvidenceIDs: []string{"check-1"}}
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
