package commands

import (
	"strings"
	"testing"

	"github.com/higress-group/issue-spec/internal/gates"
	"github.com/higress-group/issue-spec/internal/model"
)

func TestResolveStatusGateOnlyAcceptsPlanningTargets(t *testing.T) {
	for _, test := range []struct {
		raw               string
		design, implement int
		want              gates.Target
	}{
		{raw: "proposal", want: gates.TargetProposal},
		{raw: "design", design: 2, want: gates.TargetDesign},
		{raw: "implement", design: 2, implement: 3, want: gates.TargetImplement},
	} {
		got, err := resolveStatusGate(test.raw, test.design, test.implement)
		if err != nil || got != test.want {
			t.Fatalf("resolveStatusGate(%q) = %q, %v; want %q", test.raw, got, err, test.want)
		}
	}
	if _, err := resolveStatusGate("final", 2, 3); err == nil {
		t.Fatal("removed final gate was accepted")
	}
}

func TestPlanningStatusFlagsMalformedTypedComments(t *testing.T) {
	body, err := model.EnsureTypedBody("SPEC", "SPEC-001", "# SPEC-001\n\nhand-written", model.BodyOptions{Status: "confirmed"})
	if err != nil {
		t.Fatal(err)
	}
	summary := summarizeStatus("o/r", 1, 0, 0, []model.Artifact{{
		Issue: 1, URL: "https://github.com/o/r/issues/1#issuecomment-1", Comment: model.ParseTypedComment(body),
	}})
	if summary.OK || len(summary.Malformed) == 0 || !strings.Contains(strings.Join(summary.NextGates, "\n"), "malformed typed comments") {
		t.Fatalf("malformed planning artifact was not reported: %+v", summary)
	}
}

func TestPlanningStatusProjectionExcludesHistoricalFinalCarriers(t *testing.T) {
	artifacts := []model.Artifact{
		{Issue: 1, Comment: model.TypedComment{Type: "SPEC", ID: "SPEC-001"}},
		{Issue: 3, Comment: model.TypedComment{Type: "PROCESS", ID: "PROCESS-001"}},
		{Issue: 3, Comment: model.TypedComment{Type: "REVIEW", ID: "REVIEW-001"}},
		{Issue: 3, Comment: model.TypedComment{Type: "VERIFY", ID: "VERIFY-001"}},
	}
	projected := artifactsForImplementGate(artifacts, 3)
	if len(projected) != 2 || projected[0].Comment.Type != "SPEC" || projected[1].Comment.Type != "PROCESS" {
		t.Fatalf("planning projection retained historical carrier: %+v", projected)
	}
}
