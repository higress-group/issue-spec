package workflow

import (
	"testing"

	"github.com/higress-group/issue-spec/internal/codereview"
)

func TestNewProviderPlanBuildsCapabilityMatrix(t *testing.T) {
	description := codereview.ProviderDescription{ProviderKey: "aone", DisplayName: "Aone Code",
		CodeChangeLabel: "Merge request", Capabilities: []codereview.Capability{
			codereview.CapabilityEvidenceSnapshot, codereview.CapabilityChangeComment},
		RecommendedEvidence: []codereview.EvidenceKind{codereview.EvidenceReview, codereview.EvidenceCheck}}
	plan, err := NewProviderPlan(description, codereview.Capabilities{ProtocolVersion: codereview.ProtocolVersion,
		Values: []codereview.Capability{codereview.CapabilityChangeComment, codereview.CapabilityEvidenceSnapshot}})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ChangeCreate || !plan.ChangeComment || !plan.EvidenceSnapshot || plan.CodeChangeLabel != "Merge request" {
		t.Fatalf("unexpected capability matrix: %+v", plan)
	}
}

func TestNewProviderPlanRejectsAdvertisedRuntimeMismatch(t *testing.T) {
	description := codereview.ProviderDescription{ProviderKey: "aone",
		Capabilities: []codereview.Capability{codereview.CapabilityEvidenceSnapshot}}
	_, err := NewProviderPlan(description, codereview.Capabilities{ProtocolVersion: codereview.ProtocolVersion,
		Values: []codereview.Capability{codereview.CapabilityChangeCreate}})
	if err == nil {
		t.Fatal("expected advertised/runtime mismatch to fail closed")
	}
}

func TestNewProviderPlanCapabilityMatrix(t *testing.T) {
	capabilities := []codereview.Capability{codereview.CapabilityChangeCreate,
		codereview.CapabilityChangeComment, codereview.CapabilityEvidenceSnapshot}
	for mask := 0; mask < 8; mask++ {
		var selected []codereview.Capability
		for index, capability := range capabilities {
			if mask&(1<<index) != 0 {
				selected = append(selected, capability)
			}
		}
		plan, err := NewProviderPlan(codereview.ProviderDescription{ProviderKey: "matrix",
			Capabilities: append([]codereview.Capability(nil), selected...)}, codereview.Capabilities{
			ProtocolVersion: codereview.ProtocolVersion, Values: append([]codereview.Capability(nil), selected...)})
		if err != nil {
			t.Fatalf("mask %03b: %v", mask, err)
		}
		if plan.ChangeCreate != (mask&1 != 0) || plan.ChangeComment != (mask&2 != 0) ||
			plan.EvidenceSnapshot != (mask&4 != 0) {
			t.Fatalf("mask %03b produced %+v", mask, plan)
		}
	}
}
