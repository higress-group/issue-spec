package gates

import (
	"testing"

	"github.com/higress-group/issue-spec/internal/model"
)

func TestEvaluateRejectsRemovedFinalTarget(t *testing.T) {
	if _, err := Evaluate(Snapshot{Target: Target("final"), Mode: ModeForecast}); err == nil {
		t.Fatal("removed final target was accepted")
	}
}

func TestImplementPlanningAllowsOmittedOptionalTaskAndProcess(t *testing.T) {
	body, err := model.EnsureTypedBody("SPEC", "SPEC-001", "## Requirement: x\n\nx MUST work.\n\n### Scenario: x\n\n- **WHEN** x\n- **THEN** x", model.BodyOptions{Status: "confirmed"})
	if err != nil {
		t.Fatal(err)
	}
	report, err := Evaluate(Snapshot{Target: TargetImplement, Mode: ModeForecast,
		Artifacts: []model.Artifact{{Comment: model.ParseTypedComment(body)}}})
	if err != nil {
		t.Fatal(err)
	}
	if !report.Ready || len(report.Diagnostics) != 0 {
		t.Fatalf("unexpected implement planning report: %+v", report)
	}
}

func TestPlanningEvaluationIgnoresHistoricalReviewAndVerify(t *testing.T) {
	body, err := model.EnsureTypedBody("SPEC", "SPEC-001", "## Requirement: x\n\nx MUST work.\n\n### Scenario: x\n\n- **WHEN** x\n- **THEN** x", model.BodyOptions{Status: "confirmed"})
	if err != nil {
		t.Fatal(err)
	}
	report, err := Evaluate(Snapshot{Target: TargetProposal, Mode: ModeForecast,
		Artifacts: []model.Artifact{
			{Comment: model.ParseTypedComment(body)},
			{Comment: model.TypedComment{Type: "REVIEW", ID: "REVIEW-001", Errors: []string{"historical malformed review"}}},
			{Comment: model.TypedComment{Type: "VERIFY", ID: "VERIFY-001", Errors: []string{"historical malformed verification"}}},
		},
		Canonical: CanonicalFacts{Observed: true, Diagnostics: []model.CanonicalDiagnostic{
			{Type: "REVIEW", ID: "REVIEW-001", Message: "historical malformed review"},
		}},
	})
	if err != nil || !report.Ready || len(report.Diagnostics) != 0 {
		t.Fatalf("historical carriers affected planning: report=%+v err=%v", report, err)
	}
}
