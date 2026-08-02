package relationships

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/higress-group/issue-spec/internal/model"
)

func TestRegistryIsClosedOrientedAndExplicitlyExcludesOtherAuthorities(t *testing.T) {
	want := []OwnerRule{
		{TaskCoversSpec, "TASK", "SPEC", "section:covers", RelatedCommentsField, true},
		{ProcessParentTask, "PROCESS", "TASK", "section:parent-task", RelatedCommentsField, true},
		{ProcessDependsProcess, "PROCESS", "PROCESS", "section:dependencies", RelatedCommentsField, true},
		{ReviewCoversProcess, "REVIEW", "PROCESS", "accepted-review-or-explicit-sync", RelatedCommentsField, true},
		{ReviewCoversSpec, "REVIEW", "SPEC", "accepted-review-or-explicit-sync", RelatedCommentsField, true},
		{VerifyCoversProcess, "VERIFY", "PROCESS", "accepted-verification-assignment", RelatedCommentsField, true},
		{VerifyCoversSpec, "VERIFY", "SPEC", "section:covered-specs-or-accepted-assignment", RelatedCommentsField, true},
		{ProcessCodeSubject, "PROCESS", "CODE_SUBJECT", "provider-code-subject-binding", "PR", false},
		{ProcessSupersededBy, "PROCESS", "PROCESS", "carrier:superseded-by", RelatedCommentsField, false},
	}
	if got := Registry(); !reflect.DeepEqual(got, want) {
		t.Fatalf("registry=\n%+v\nwant=\n%+v", got, want)
	}
	copyOfRules := Registry()
	copyOfRules[0].OwnerType = "SPEC"
	if Registry()[0].OwnerType != "TASK" {
		t.Fatal("registry caller mutated closed table")
	}

	left := model.ArtifactRef{Issue: 2, Type: "TASK", ID: "TASK-001", URL: "https://example.test/tasks/1"}
	right := model.ArtifactRef{Issue: 1, Type: "SPEC", ID: "SPEC-001", URL: "https://example.test/specs/1"}
	for _, pair := range [][2]model.ArtifactRef{{left, right}, {right, left}} {
		owner, target, err := Normalize(TaskCoversSpec, pair[0], pair[1])
		if err != nil || owner.ID != left.ID || target.ID != right.ID {
			t.Fatalf("pair=%+v owner=%+v target=%+v err=%v", pair, owner, target, err)
		}
	}
	process := model.ArtifactRef{Issue: 3, Type: "PROCESS", ID: "PROCESS-001", URL: "https://example.test/processes/1"}
	otherProcess := model.ArtifactRef{Issue: 3, Type: "PROCESS", ID: "PROCESS-002", URL: "https://example.test/processes/2"}
	if _, _, err := Normalize(ProcessDependsProcess, process, otherProcess); !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("same-type relationship inferred without semantics: %v", err)
	}
	if _, _, err := Normalize(ProcessCodeSubject, process, right); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("dedicated code subject entered generic typed normalization: %v", err)
	}

	wantExclusions := []string{"proposal-design-implement-predecessors", "question-answer-resolution",
		"review-findings-and-replies", "code-change-rationale", "provider-checks", "arbitrary-free-form-urls"}
	gotExclusions := Exclusions()
	if len(gotExclusions) != len(wantExclusions) {
		t.Fatalf("exclusions=%+v", gotExclusions)
	}
	for index, exclusion := range gotExclusions {
		if exclusion.Name != wantExclusions[index] || exclusion.Authority == "" {
			t.Fatalf("exclusion[%d]=%+v", index, exclusion)
		}
	}
}

func TestResolveUsesExactArtifactsAndSemanticOwnerInEitherPairOrder(t *testing.T) {
	spec := relationshipArtifact(t, 1, "SPEC", "SPEC-001", "## Specification", nil)
	task := relationshipArtifact(t, 2, "TASK", "TASK-001", "## Task\n\n### Covers\n\n- SPEC-001", nil)
	prerequisite := relationshipArtifact(t, 3, "PROCESS", "PROCESS-001",
		"## Process\n\n### Parent TASK\n\n- TASK-001\n\n### Dependencies\n\n- N/A", nil)
	dependent := relationshipArtifact(t, 3, "PROCESS", "PROCESS-002",
		"## Process\n\n### Parent TASK\n\n- TASK-001\n\n### Dependencies\n\n- PROCESS-001", nil)
	reviewProcess := relationshipArtifact(t, 3, "PROCESS", "PROCESS-003",
		"## Process\n\n### Parent TASK\n\n- TASK-001\n\n### Covers\n\n- SPEC-001", nil)
	review := relationshipArtifact(t, 3, "REVIEW", "REVIEW-001", "## Review\n\n"+
		"<!-- issue-spec:accepted-review-receipt version=2 -->\n{\"receipt_id\":\"receipt-review-1\",\"receipt_digest\":\""+
		strings.Repeat("a", 64)+"\",\"assignment_process_id\":\"PROCESS-003\",\"assignment_generation\":1}\n"+
		"<!-- /issue-spec:accepted-review-receipt -->", nil)
	verifyBody := "## Verify\n\n### Covered SPECs\n\n- SPEC-001\n\n" +
		"<!-- issue-spec:accepted-verification-receipt version=1 -->\n{\"receipt_id\":\"receipt-verify-1\",\"receipt_digest\":\"" +
		strings.Repeat("b", 64) + "\",\"assignment_generation\":1}\n<!-- /issue-spec:accepted-verification-receipt -->"
	verify := relationshipArtifact(t, 3, "VERIFY", "VERIFY-001", verifyBody,
		[]string{reviewProcess.URL})
	superseded := relationshipArtifact(t, 3, "PROCESS", "PROCESS-004",
		"## Process\n\n### Parent TASK\n\n- TASK-001", nil)
	var err error
	superseded.Comment.Body, _, err = model.StampSupersededBy(superseded.Comment.Body, superseded.Comment.ID,
		model.SupersededBy{ProcessID: prerequisite.Comment.ID, URL: prerequisite.URL})
	if err != nil {
		t.Fatal(err)
	}
	superseded.Comment = model.ParseTypedComment(superseded.Comment.Body)
	artifacts := []model.Artifact{spec, task, prerequisite, dependent, reviewProcess, review, verify, superseded}

	assert := func(kind Kind, left, right model.Artifact, ownerID, targetID string) {
		t.Helper()
		leftRef, _ := left.Ref()
		rightRef, _ := right.Ref()
		for _, pair := range [][2]model.ArtifactRef{{leftRef, rightRef}, {rightRef, leftRef}} {
			rule, owner, target, resolveErr := Resolve(artifacts, pair[0], pair[1])
			if resolveErr != nil || rule.Kind != kind || owner.ID != ownerID || target.ID != targetID {
				t.Fatalf("kind=%s pair=%s/%s rule=%+v owner=%+v target=%+v err=%v",
					kind, pair[0].ID, pair[1].ID, rule, owner, target, resolveErr)
			}
		}
	}
	assert(TaskCoversSpec, task, spec, task.Comment.ID, spec.Comment.ID)
	assert(ProcessParentTask, dependent, task, dependent.Comment.ID, task.Comment.ID)
	assert(ProcessDependsProcess, dependent, prerequisite, dependent.Comment.ID, prerequisite.Comment.ID)
	assert(ReviewCoversProcess, review, reviewProcess, review.Comment.ID, reviewProcess.Comment.ID)
	assert(ReviewCoversSpec, review, spec, review.Comment.ID, spec.Comment.ID)
	assert(VerifyCoversProcess, verify, reviewProcess, verify.Comment.ID, reviewProcess.Comment.ID)
	assert(VerifyCoversSpec, verify, spec, verify.Comment.ID, spec.Comment.ID)
	assert(ProcessSupersededBy, superseded, prerequisite, superseded.Comment.ID, prerequisite.Comment.ID)
}

func TestBuildIndexAcceptsProposalDesignImplementRoleTopology(t *testing.T) {
	spec := relationshipArtifact(t, 1, "SPEC", "SPEC-001", "## Specification", nil)
	task := relationshipArtifact(t, 2, "TASK", "TASK-001", "## Task\n\n### Covers\n\n- SPEC-001", []string{spec.URL})
	process := relationshipArtifact(t, 3, "PROCESS", "PROCESS-001",
		"## Process\n\n### Parent TASK\n\n- TASK-001", []string{task.URL})
	review := relationshipArtifact(t, 3, "REVIEW", "REVIEW-001",
		"## Review\n\n### Covered PROCESSes\n\n- PROCESS-001\n\n### Covered SPECs\n\n- SPEC-001",
		[]string{process.URL, spec.URL})
	verify := relationshipArtifact(t, 3, "VERIFY", "VERIFY-001",
		"## Verify\n\n### Covered PROCESSes\n\n- PROCESS-001\n\n### Covered SPECs\n\n- SPEC-001",
		[]string{process.URL, spec.URL})

	index, err := BuildIndex([]model.Artifact{verify, task, spec, review, process})
	if err != nil {
		t.Fatal(err)
	}
	want := map[Kind]bool{
		TaskCoversSpec: true, ProcessParentTask: true, ReviewCoversProcess: true,
		ReviewCoversSpec: true, VerifyCoversProcess: true, VerifyCoversSpec: true,
	}
	for _, edge := range index.Edges {
		delete(want, edge.Kind)
	}
	if len(index.Edges) != 6 || len(want) != 0 {
		t.Fatalf("three-issue canonical edges=%+v missing=%v", index.Edges, want)
	}
}

func TestBuildIndexRejectsTypedArtifactsOnWrongPhaseIssue(t *testing.T) {
	spec := relationshipArtifact(t, 1, "SPEC", "SPEC-001", "## Specification", nil)
	task := relationshipArtifact(t, 2, "TASK", "TASK-001", "## Task", nil)
	process := relationshipArtifact(t, 3, "PROCESS", "PROCESS-001", "## Process", nil)
	for name, artifacts := range map[string][]model.Artifact{
		"TASK in Implement": {spec, relationshipArtifact(t, 3, "TASK", "TASK-001", "## Task", nil), process},
		"PROCESS in Design": {spec, task, relationshipArtifact(t, 2, "PROCESS", "PROCESS-001", "## Process", nil)},
		"SPEC in Design":    {relationshipArtifact(t, 2, "SPEC", "SPEC-001", "## Specification", nil), task, process},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := BuildIndex(artifacts); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "artifacts share issue") {
				t.Fatalf("wrong phase role error=%v", err)
			}
		})
	}
}

func TestResolveAndBuildIndexRejectExactIdentityAmbiguity(t *testing.T) {
	spec := relationshipArtifact(t, 1, "SPEC", "SPEC-001", "## Specification", nil)
	task := relationshipArtifact(t, 2, "TASK", "TASK-001", "## Task\n\n### Covers\n\n- SPEC-001", []string{spec.URL})
	ref, _ := task.Ref()
	wrongURL := ref
	wrongURL.URL = "https://example.test/tasks/other"
	if _, _, _, err := Resolve([]model.Artifact{spec, task}, wrongURL, mustRef(t, spec)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unresolved endpoint URL error=%v", err)
	}

	duplicateID := spec
	duplicateID.Issue, duplicateID.URL = 2, "https://example.test/issues/2#issuecomment-999"
	urlCollision := relationshipArtifact(t, 1, "SPEC", "SPEC-002", "## Specification", nil)
	urlCollision.URL = spec.URL
	wrongIssueURL := task
	wrongIssueURL.URL = "https://example.test/issues/4#issuecomment-1"
	otherDesignIssue := relationshipArtifact(t, 4, "TASK", "TASK-009", "## Task", nil)
	otherImplementIssue := relationshipArtifact(t, 4, "PROCESS", "PROCESS-009", "## Process", nil)
	repeatedLink := relationshipArtifact(t, 2, "TASK", "TASK-001", "## Task\n\n### Covers\n\n- SPEC-001",
		[]string{spec.URL, spec.URL})
	for name, input := range map[string][]model.Artifact{
		"duplicate typed id": {spec, duplicateID, task},
		"duplicate url":      {spec, urlCollision, task},
		"url wrong issue":    {spec, wrongIssueURL},
		"multiple designs":   {spec, task, otherDesignIssue},
		"multiple implements": {spec, task,
			relationshipArtifact(t, 3, "PROCESS", "PROCESS-008", "## Process", nil), otherImplementIssue},
		"duplicate link": {spec, repeatedLink},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := BuildIndex(input); !errors.Is(err, ErrAmbiguous) && !errors.Is(err, ErrInvalid) {
				t.Fatalf("input was not rejected with stable identity error: %v", err)
			}
		})
	}

	question := relationshipArtifact(t, 1, "QUESTION", "QUESTION-001", "## Question", nil)
	question.Comment.Links[RelatedCommentsField] = []string{task.URL}
	index, err := BuildIndex([]model.Artifact{spec, task, question})
	if err != nil {
		t.Fatal(err)
	}
	if len(index.Classifications) != 2 || index.Totals.Unknown != 1 {
		t.Fatalf("unsupported pair was not classified unknown: %+v", index)
	}
	if _, _, _, err := Resolve([]model.Artifact{spec, task, question}, mustRef(t, question), mustRef(t, task)); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("unsupported pair resolve error=%v", err)
	}
}

func TestBuildIndexClassifiesCanonicalBacklinkStaleAndUnknownWithoutPromotingBacklinks(t *testing.T) {
	spec1 := relationshipArtifact(t, 1, "SPEC", "SPEC-001", "## Specification", nil)
	spec2 := relationshipArtifact(t, 1, "SPEC", "SPEC-002", "## Specification", nil)
	task1 := relationshipArtifact(t, 2, "TASK", "TASK-001", "## Task\n\n### Covers\n\n- SPEC-001",
		[]string{spec1.APIURL + "/", "https://outside.example/unknown/"})
	task2 := relationshipArtifact(t, 2, "TASK", "TASK-002", "## Task\n\n### Covers\n\n- SPEC-001", nil)
	spec1.Comment.Links[RelatedCommentsField] = []string{task1.URL}
	spec2.Comment.Links[RelatedCommentsField] = []string{task2.URL}

	index, err := BuildIndex([]model.Artifact{task2, spec2, task1, spec1})
	if err != nil {
		t.Fatal(err)
	}
	if len(index.Edges) != 1 || index.Edges[0].Kind != TaskCoversSpec || index.Edges[0].Owner.ID != task1.Comment.ID ||
		index.Edges[0].Target.ID != spec1.Comment.ID {
		t.Fatalf("canonical edges=%+v", index.Edges)
	}
	wantClassifications := map[Classification]int{Canonical: 1, LegacyBacklink: 1, LegacyOrStale: 1, Unknown: 1}
	for _, edge := range index.Classifications {
		wantClassifications[edge.Classification]--
	}
	for classification, remaining := range wantClassifications {
		if remaining != 0 {
			t.Fatalf("classification %s remaining=%d all=%+v", classification, remaining, index.Classifications)
		}
	}
	if len(index.Forward) != 1 || len(index.Reverse) != 1 || index.Reverse[0].Artifact.ID != spec1.Comment.ID {
		t.Fatalf("forward=%+v reverse=%+v", index.Forward, index.Reverse)
	}
	if index.Totals.PhysicalLinks != 4 || index.Totals.Canonical != 1 || index.Totals.LegacyBacklink != 1 ||
		index.Totals.LegacyOrStale != 1 || index.Totals.Unknown != 1 {
		t.Fatalf("totals=%+v", index.Totals)
	}
	raw, err := json.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"assignment_role", "assignment_process_id", "receipt", "selector", "provenance", "assurance"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("relationship index leaked evidence authority field %q: %s", forbidden, raw)
		}
	}
}

func TestBuildIndexIsDeterministicAndCapsEachAdjacency(t *testing.T) {
	const count = 12
	var ids, links []string
	artifacts := make([]model.Artifact, 0, count+1)
	for index := count; index >= 1; index-- {
		id := fmt.Sprintf("SPEC-%03d", index)
		spec := relationshipArtifact(t, 1, "SPEC", id, "## Specification", nil)
		ids, links, artifacts = append(ids, id), append(links, spec.URL), append(artifacts, spec)
	}
	task := relationshipArtifact(t, 2, "TASK", "TASK-001", "## Task\n\n### Covers\n\n- "+strings.Join(ids, "\n- "), links)
	artifacts = append(artifacts, task)
	first, err := BuildIndex(artifacts, BuildOptions{IdentityLimit: 2})
	if err != nil {
		t.Fatal(err)
	}
	slices.Reverse(artifacts)
	for index := range artifacts {
		if artifacts[index].Comment.ID != task.Comment.ID {
			continue
		}
		values := append([]string(nil), artifacts[index].Comment.Links[RelatedCommentsField]...)
		slices.Reverse(values)
		artifacts[index].Comment.Links = map[string][]string{RelatedCommentsField: values}
	}
	second, err := BuildIndex(artifacts, BuildOptions{IdentityLimit: 2})
	if err != nil {
		t.Fatal(err)
	}
	left, _ := json.Marshal(first)
	right, _ := json.Marshal(second)
	if string(left) != string(right) {
		t.Fatalf("index depends on input order:\n%s\n%s", left, right)
	}
	if len(first.Forward) != 1 || first.Forward[0].Total != count || len(first.Forward[0].Edges) != 2 ||
		!first.Forward[0].Truncated || !first.Truncated {
		t.Fatalf("forward cap=%+v index truncated=%t", first.Forward, first.Truncated)
	}
	if first.Forward[0].Detail.CommandFamily != "relationship-detail" ||
		!reflect.DeepEqual(first.Forward[0].Detail.Arguments,
			[]string{"--direction", "forward", "--kind", string(TaskCoversSpec), "--artifact", "TASK-001"}) {
		t.Fatalf("detail=%+v", first.Forward[0].Detail)
	}
	if len(first.Reverse) != count {
		t.Fatalf("reverse identities=%d want %d", len(first.Reverse), count)
	}
	if _, err := BuildIndex(artifacts, BuildOptions{ArtifactLimit: count}); !errors.Is(err, ErrBound) {
		t.Fatalf("artifact cap error=%v", err)
	}
	if _, err := BuildIndex(artifacts, BuildOptions{PhysicalLinkLimit: count - 1}); !errors.Is(err, ErrBound) {
		t.Fatalf("physical cap error=%v", err)
	}
}

func relationshipArtifact(t *testing.T, issue int, kind, id, logical string, links []string) model.Artifact {
	t.Helper()
	body, err := model.EnsureTypedBody(kind, id, logical, model.BodyOptions{Agent: "Worker", Status: "done",
		Links: map[string][]string{RelatedCommentsField: links}})
	if err != nil {
		t.Fatal(err)
	}
	return model.Artifact{Issue: issue, URL: fmt.Sprintf("https://example.test/issues/%d#issuecomment-%s", issue, id),
		APIURL: fmt.Sprintf("https://api.example.test/issues/%d/comments/%s", issue, id), Comment: model.ParseTypedComment(body)}
}

func mustRef(t *testing.T, artifact model.Artifact) model.ArtifactRef {
	t.Helper()
	ref, err := artifact.Ref()
	if err != nil {
		t.Fatal(err)
	}
	return ref
}
