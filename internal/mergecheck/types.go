// Package mergecheck contains the deterministic, I/O-free merge predicate.
// It deliberately accepts only normalized current provider facts. Historical
// attempts and workflow artifacts have no representation in this package.
package mergecheck

import (
	"fmt"
	"sort"

	"github.com/higress-group/issue-spec/internal/codereview"
)

const SchemaVersion = "issue-spec.merge-check/v1"

type ChangeScope struct {
	SimpleIssue    *int `json:"simple_issue,omitempty"`
	ProposalIssue  *int `json:"proposal_issue,omitempty"`
	DesignIssue    *int `json:"design_issue,omitempty"`
	ImplementIssue *int `json:"implement_issue,omitempty"`
}

func (s ChangeScope) Validate() error {
	simple, proposal := positive(s.SimpleIssue), positive(s.ProposalIssue)
	if simple == proposal {
		return fmt.Errorf("exactly one simple or proposal root is required")
	}
	if s.SimpleIssue != nil && *s.SimpleIssue <= 0 || s.ProposalIssue != nil && *s.ProposalIssue <= 0 ||
		s.DesignIssue != nil && *s.DesignIssue <= 0 || s.ImplementIssue != nil && *s.ImplementIssue <= 0 {
		return fmt.Errorf("scope issue numbers must be positive")
	}
	if simple && (s.DesignIssue != nil || s.ImplementIssue != nil) {
		return fmt.Errorf("design and implement are invalid with a simple root")
	}
	return nil
}

func positive(value *int) bool { return value != nil && *value > 0 }

// IssueNumbers returns the exact selected issue set in predecessor order.
func (s ChangeScope) IssueNumbers() []int {
	if s.Validate() != nil {
		return nil
	}
	if s.SimpleIssue != nil {
		return []int{*s.SimpleIssue}
	}
	result := []int{*s.ProposalIssue}
	if s.DesignIssue != nil {
		result = append(result, *s.DesignIssue)
	}
	if s.ImplementIssue != nil {
		result = append(result, *s.ImplementIssue)
	}
	return result
}

type CodeSubject struct {
	Reference codereview.Reference   `json:"reference"`
	Revision  string                 `json:"revision"`
	State     codereview.ChangeState `json:"state"`
}

type ProviderPolicyObservation struct {
	Code        string `json:"code"`
	Satisfied   bool   `json:"satisfied"`
	Diagnostics string `json:"diagnostics,omitempty"`
}

type Input struct {
	Scope        ChangeScope                  `json:"scope"`
	Subject      CodeSubject                  `json:"subject"`
	Required     []codereview.CheckIdentity   `json:"required"`
	Checks       []codereview.CheckConclusion `json:"checks"`
	Review       codereview.ReviewAuthority   `json:"review"`
	ProviderGate []ProviderPolicyObservation  `json:"provider_gate"`
}

type BlockerCode string

const (
	BlockerScopeInvalid           BlockerCode = "scope_invalid"
	BlockerSubjectInvalid         BlockerCode = "subject_invalid"
	BlockerChangeNotOpen          BlockerCode = "change_not_open"
	BlockerCheckConfiguration     BlockerCode = "check_configuration_invalid"
	BlockerCheckMissing           BlockerCode = "check_missing"
	BlockerCheckNotSuccessful     BlockerCode = "check_not_successful"
	BlockerReviewAuthorityInvalid BlockerCode = "review_authority_invalid"
	BlockerReviewFallbackRequired BlockerCode = "review_fallback_required"
	BlockerReviewApprovalCount    BlockerCode = "review_approval_count"
	BlockerReviewChangesRequested BlockerCode = "review_changes_requested"
	BlockerReviewIndependence     BlockerCode = "review_independence"
	BlockerReviewCodeOwner        BlockerCode = "review_code_owner"
	BlockerReviewConversation     BlockerCode = "review_conversation"
	BlockerReviewFinding          BlockerCode = "review_finding"
	BlockerProviderPolicy         BlockerCode = "provider_policy"
)

type Blocker struct {
	Code        BlockerCode `json:"code"`
	Subject     string      `json:"subject,omitempty"`
	Diagnostics string      `json:"diagnostics"`
	URL         string      `json:"url,omitempty"`
}

type Summary struct {
	RequiredChecks       int `json:"required_checks"`
	SuccessfulChecks     int `json:"successful_checks"`
	RequiredApprovals    int `json:"required_approvals"`
	CurrentApprovals     int `json:"current_approvals"`
	IndependentApprovals int `json:"independent_approvals"`
	VisibleP2Findings    int `json:"visible_p2_findings"`
}

type Decision struct {
	SchemaVersion  string    `json:"schema_version"`
	Ready          bool      `json:"ready"`
	ExpectedHead   string    `json:"expected_head"`
	SnapshotDigest string    `json:"snapshot_digest"`
	Blockers       []Blocker `json:"blockers"`
	Summary        Summary   `json:"summary"`
}

func sortBlockers(blockers []Blocker) {
	sort.Slice(blockers, func(i, j int) bool {
		left, right := blockers[i], blockers[j]
		return string(left.Code)+"\x00"+left.Subject+"\x00"+left.Diagnostics+"\x00"+left.URL <
			string(right.Code)+"\x00"+right.Subject+"\x00"+right.Diagnostics+"\x00"+right.URL
	})
}
