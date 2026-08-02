package relationships

import (
	"errors"
	"strings"
	"testing"

	"github.com/higress-group/issue-spec/internal/model"
)

func TestPlanMutationChangesOnlyOwnerAndPreservesUnrelatedBytes(t *testing.T) {
	spec := relationshipArtifact(t, 1, "SPEC", "SPEC-001", "## Specification\n\nsemantic", nil)
	task := relationshipArtifact(t, 3, "TASK", "TASK-001",
		"## Task\n\n### Covers\n\n- SPEC-001\n\n### Unrelated\n\nkeep exactly", []string{"https://outside.example/keep"})
	owner, target := mustRef(t, task), mustRef(t, spec)
	frozen, err := PlanMutation([]model.Artifact{spec, task}, owner, []model.ArtifactRef{target}, nil,
		task.Comment.Body, 7, DefaultMutationTargetLimit)
	if err != nil {
		t.Fatal(err)
	}
	if frozen.RepresentationVersion != 7 || frozen.BeforeDigest == frozen.AfterDigest {
		t.Fatalf("frozen=%+v", frozen)
	}
	if !strings.Contains(frozen.DesiredBody, "- Related Comments: "+target.URL+", https://outside.example/keep") ||
		!strings.Contains(frozen.DesiredBody, "### Unrelated\n\nkeep exactly") {
		t.Fatalf("desired body changed or lost unrelated bytes:\n%s", frozen.DesiredBody)
	}
	replayed, changed, err := ApplyMutation(frozen.DesiredBody, frozen.Mutation)
	if err != nil || changed || replayed != frozen.DesiredBody {
		t.Fatalf("idempotent replay changed bytes: changed=%v err=%v", changed, err)
	}
	if spec.Comment.Body != relationshipArtifact(t, 1, "SPEC", "SPEC-001", "## Specification\n\nsemantic", nil).Comment.Body {
		t.Fatal("pure mutation altered peer artifact")
	}
}

func TestMutationRemovalUpdatesOwnedSemanticSectionAndRejectsAppendOnlyCarrier(t *testing.T) {
	spec1 := relationshipArtifact(t, 1, "SPEC", "SPEC-001", "## Specification", nil)
	spec2 := relationshipArtifact(t, 1, "SPEC", "SPEC-002", "## Specification", nil)
	task := relationshipArtifact(t, 3, "TASK", "TASK-001", "## Task\n\n### Covers\n\n- SPEC-001\n- SPEC-002",
		[]string{spec1.URL, spec2.URL})
	owner, target := mustRef(t, task), mustRef(t, spec1)
	frozen, err := PlanMutation([]model.Artifact{spec1, spec2, task}, owner, nil, []model.ArtifactRef{target},
		task.Comment.Body, 0, DefaultMutationTargetLimit)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(frozen.DesiredBody, "- SPEC-001") || strings.Contains(frozen.DesiredBody, spec1.URL) ||
		!strings.Contains(frozen.DesiredBody, "- SPEC-002") || !strings.Contains(frozen.DesiredBody, spec2.URL) {
		t.Fatalf("semantic removal did not remain owner-consistent:\n%s", frozen.DesiredBody)
	}

	review := relationshipArtifact(t, 3, "REVIEW", "REVIEW-001", "## Review\n\n### Covers\n\n- SPEC-001", []string{spec1.URL})
	_, err = PlanMutation([]model.Artifact{spec1, review}, mustRef(t, review), nil, []model.ArtifactRef{target},
		review.Comment.Body, 0, DefaultMutationTargetLimit)
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("append-only review removal error=%v", err)
	}
}

func TestMutationFailsClosedOnOverlapAndTargetBound(t *testing.T) {
	spec := relationshipArtifact(t, 1, "SPEC", "SPEC-001", "## Specification", nil)
	task := relationshipArtifact(t, 3, "TASK", "TASK-001", "## Task\n\n### Covers\n\n- SPEC-001", nil)
	owner, target := mustRef(t, task), mustRef(t, spec)
	if _, err := PlanMutation([]model.Artifact{spec, task}, owner, []model.ArtifactRef{target}, []model.ArtifactRef{target},
		task.Comment.Body, 0, DefaultMutationTargetLimit); !errors.Is(err, ErrInvalid) {
		t.Fatalf("overlap error=%v", err)
	}
	if _, err := PlanMutation([]model.Artifact{spec, task}, owner, []model.ArtifactRef{target}, nil,
		task.Comment.Body, 0, -1); !errors.Is(err, ErrBound) {
		t.Fatalf("bound error=%v", err)
	}
}
