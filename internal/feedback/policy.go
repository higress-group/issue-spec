package feedback

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/model"
)

const (
	policyLabel = "issue-spec/feedback-policy"
	holdLabel   = "issue-spec/feedback-hold"
	modeSuggest = "suggest"
	modeAuto    = "auto"
)

type Backend interface {
	github.IssueBackend
	github.IssueLabelBackend
}

type Policy struct {
	Threshold            int      `json:"threshold"`
	PriorityLabel        string   `json:"priority_label"`
	Mode                 string   `json:"mode"`
	TrustedReporterIDs   []string `json:"trusted_reporter_ids"`
	HigherPriorityLabels []string `json:"higher_priority_labels,omitempty"`
	HoldLabels           []string `json:"hold_labels,omitempty"`
}

type PolicyResult struct {
	Repository  string `json:"repository"`
	PolicyIssue int    `json:"policy_issue"`
	PolicyRef   string `json:"policy_ref"`
	DryRun      bool   `json:"dry_run"`
	Policy      Policy `json:"policy"`
}

// Configure publishes a repository label definition to bind the reviewed policy
// to this Issue repository. The provider authorizes changes to the definition.
func Configure(ctx context.Context, backend Backend, repo string, number int, dryRun bool) (PolicyResult, error) {
	policy, ref, err := readPolicyIssue(ctx, backend, repo, number)
	if err != nil {
		return PolicyResult{}, err
	}
	result := PolicyResult{Repository: repo, PolicyIssue: number, PolicyRef: ref, DryRun: dryRun, Policy: policy}
	if dryRun {
		return result, nil
	}
	label, err := backend.GetRepositoryLabel(ctx, repo, policyLabel)
	if errors.Is(err, github.ErrRepositoryLabelNotFound) {
		if _, err = backend.CreateLabel(ctx, repo, policyLabel, "536D8B", ref); err == nil {
			label, err = backend.GetRepositoryLabel(ctx, repo, policyLabel)
		}
	}
	if err != nil {
		return PolicyResult{}, fmt.Errorf("read or create feedback policy label: %w", err)
	}
	if label.Description != ref {
		label, err = backend.UpdateRepositoryLabelDescription(ctx, repo, policyLabel, ref)
		if err != nil {
			return PolicyResult{}, fmt.Errorf("publish feedback policy: %w", err)
		}
	}
	if label.Description != ref {
		return PolicyResult{}, fmt.Errorf("the backend did not publish the requested feedback policy")
	}
	return result, nil
}

func loadPolicy(ctx context.Context, backend Backend, repo string) (Policy, int, string, error) {
	label, err := backend.GetRepositoryLabel(ctx, repo, policyLabel)
	if errors.Is(err, github.ErrRepositoryLabelNotFound) {
		return Policy{}, 0, "", fmt.Errorf("feedback is disabled for %s until its maintainers configure a policy", repo)
	}
	if err != nil {
		return Policy{}, 0, "", fmt.Errorf("read feedback policy label: %w", err)
	}
	parts := strings.Split(label.Description, ":")
	if len(parts) != 3 || parts[0] != "v1" {
		return Policy{}, 0, "", fmt.Errorf("invalid feedback policy reference on %s", repo)
	}
	number, err := strconv.Atoi(parts[1])
	if err != nil || number <= 0 {
		return Policy{}, 0, "", fmt.Errorf("invalid feedback policy Issue number on %s", repo)
	}
	policy, ref, err := readPolicyIssue(ctx, backend, repo, number)
	if err != nil {
		return Policy{}, 0, "", err
	}
	if label.Description != ref {
		return Policy{}, 0, "", fmt.Errorf("feedback policy Issue #%d differs from the published revision", number)
	}
	return policy, number, ref, nil
}

func readPolicyIssue(ctx context.Context, backend github.IssueBackend, repo string, number int) (Policy, string, error) {
	issue, err := backend.GetIssue(ctx, repo, number)
	if err != nil {
		return Policy{}, "", fmt.Errorf("read feedback policy Issue #%d: %w", number, err)
	}
	if issue.PullRequest != nil {
		return Policy{}, "", fmt.Errorf("feedback policy must refer to an Issue")
	}
	source, _ := model.StripTranslationSuffix(issue.Body)
	source = strings.TrimSpace(source)
	if strings.HasPrefix(source, "```json\n") && strings.HasSuffix(source, "\n```") {
		source = strings.TrimSuffix(strings.TrimPrefix(source, "```json\n"), "\n```")
	}
	policy, err := parsePolicy(source)
	if err != nil {
		return Policy{}, "", fmt.Errorf("feedback policy Issue #%d: %w", number, err)
	}
	digest := sha256.Sum256([]byte(source))
	return policy, fmt.Sprintf("v1:%d:%s", number, hex.EncodeToString(digest[:])), nil
}

func parsePolicy(source string) (Policy, error) {
	var policy Policy
	if err := decodeJSON(source, &policy); err != nil {
		return Policy{}, err
	}
	if policy.Mode == "" {
		policy.Mode = modeSuggest
	}
	if policy.Mode != modeSuggest && policy.Mode != modeAuto {
		return Policy{}, fmt.Errorf("mode must be suggest or auto")
	}
	policy.PriorityLabel = strings.TrimSpace(policy.PriorityLabel)
	if policy.Threshold <= 0 || policy.PriorityLabel == "" || strings.EqualFold(policy.PriorityLabel, policyLabel) ||
		strings.EqualFold(policy.PriorityLabel, holdLabel) || strings.EqualFold(policy.PriorityLabel, "wontfix") {
		return Policy{}, fmt.Errorf("a positive threshold and a priority label are required")
	}
	if len(policy.TrustedReporterIDs) == 0 {
		return Policy{}, fmt.Errorf("at least one trusted_reporter_ids entry is required")
	}
	for _, reporter := range policy.TrustedReporterIDs {
		id, err := strconv.ParseInt(reporter, 10, 64)
		if err != nil || id <= 0 || reporter != strconv.FormatInt(id, 10) {
			return Policy{}, fmt.Errorf("trusted_reporter_ids entries must be positive provider user IDs")
		}
	}
	for _, labels := range [][]string{policy.HoldLabels, policy.HigherPriorityLabels} {
		for _, label := range labels {
			if label == "" || strings.TrimSpace(label) != label || strings.EqualFold(label, policy.PriorityLabel) {
				return Policy{}, fmt.Errorf("hold and higher-priority labels must differ from the escalation label")
			}
		}
	}
	return policy, nil
}

func (p Policy) trusts(author *github.User) bool {
	return author != nil && author.ID > 0 && slices.Contains(p.TrustedReporterIDs, strconv.FormatInt(author.ID, 10))
}
