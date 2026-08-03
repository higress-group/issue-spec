package workflow

import (
	"fmt"
	"sort"
	"strings"

	"github.com/higress-group/issue-spec/internal/codereview"
)

// ProviderPlan is the provider-neutral capability contract captured during
// init. Repository-owned workflow files may select the provider key and gate
// policy, but never the executable or credentials behind this description.
type ProviderPlan struct {
	ProviderKey                  string                    `json:"provider_key"`
	DisplayName                  string                    `json:"display_name"`
	CodeChangeLabel              string                    `json:"code_change_label"`
	SemanticGeneration           string                    `json:"semantic_generation,omitempty"`
	ProviderBuildIdentity        string                    `json:"provider_build_identity,omitempty"`
	Capabilities                 []codereview.Capability   `json:"capabilities"`
	RecommendedEvidence          []codereview.EvidenceKind `json:"recommended_evidence,omitempty"`
	ChangeCreate                 bool                      `json:"change_create"`
	ChangeComment                bool                      `json:"change_comment"`
	EvidenceSnapshot             bool                      `json:"evidence_snapshot"`
	ReviewDecision               bool                      `json:"review_decision"`
	AuthoritativeCheckConclusion bool                      `json:"authoritative_check_conclusion"`
	MergeConditional             bool                      `json:"merge_conditional"`
}

func NewProviderPlan(description codereview.ProviderDescription, capabilities codereview.Capabilities) (ProviderPlan, error) {
	normalized, err := description.Normalized(description.ProviderKey)
	if err != nil {
		return ProviderPlan{}, err
	}
	if err := capabilities.Validate(); err != nil {
		return ProviderPlan{}, err
	}
	described := append([]codereview.Capability(nil), normalized.Capabilities...)
	discovered := append([]codereview.Capability(nil), capabilities.Values...)
	sort.Slice(described, func(i, j int) bool { return described[i] < described[j] })
	sort.Slice(discovered, func(i, j int) bool { return discovered[i] < discovered[j] })
	if strings.Join(capabilityStrings(described), "\x00") != strings.Join(capabilityStrings(discovered), "\x00") {
		return ProviderPlan{}, fmt.Errorf("provider %q advertised capabilities do not match the registered bridge", normalized.ProviderKey)
	}
	if normalized.SemanticGeneration != capabilities.SemanticGeneration ||
		normalized.ProviderBuildIdentity != capabilities.ProviderBuildIdentity {
		return ProviderPlan{}, fmt.Errorf("provider %q generation or immutable build does not match the registered bridge", normalized.ProviderKey)
	}
	plan := ProviderPlan{ProviderKey: normalized.ProviderKey, DisplayName: normalized.DisplayName,
		CodeChangeLabel: normalized.CodeChangeLabel, SemanticGeneration: capabilities.SemanticGeneration,
		ProviderBuildIdentity: capabilities.ProviderBuildIdentity, Capabilities: discovered,
		RecommendedEvidence: append([]codereview.EvidenceKind(nil), normalized.RecommendedEvidence...)}
	for _, capability := range discovered {
		switch capability {
		case codereview.CapabilityChangeCreate:
			plan.ChangeCreate = true
		case codereview.CapabilityChangeComment:
			plan.ChangeComment = true
		case codereview.CapabilityEvidenceSnapshot:
			plan.EvidenceSnapshot = true
		case codereview.CapabilityReviewDecision:
			plan.ReviewDecision = true
		case codereview.CapabilityAuthoritativeCheckConclusion:
			plan.AuthoritativeCheckConclusion = true
		case codereview.CapabilityMergeConditional:
			plan.MergeConditional = true
		}
	}
	return plan, nil
}

func capabilityStrings(values []codereview.Capability) []string {
	result := make([]string, len(values))
	for i := range values {
		result[i] = string(values[i])
	}
	return result
}
