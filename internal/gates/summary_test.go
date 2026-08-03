package gates

import "testing"

func TestProjectCompactSummaryUsesPlanningDecision(t *testing.T) {
	report := Report{Ready: false, Target: TargetImplement, Mode: ModeForecast, PointInTime: true,
		Diagnostics: []Diagnostic{{Code: CodeTaskRequired, Blocking: true, Gate: TargetImplement,
			Artifact:    ArtifactRef{Type: "TASK", ID: "TASK-001"},
			Remediation: Remediation{CommandFamily: "comment generate", Arguments: []string{"--id", "TASK-001"}}}}}
	summary := ProjectCompactSummary(report, nil, nil, Remediation{CommandFamily: "status"})
	if summary.OK || summary.Gate.Target != TargetImplement || len(summary.Blockers) != 1 || summary.Blockers[0].Count != 1 {
		t.Fatalf("unexpected compact planning summary: %+v", summary)
	}
}
