package templates

import (
	"fmt"
	"strings"

	"github.com/higress-group/issue-spec/internal/workflow"
)

// IssueSpecProviderSkill renders one provider-neutral code-change workflow.
// The output intentionally names only neutral issue-spec operations; vendor
// CLI commands and operator registration details never enter the checkout.
func IssueSpecProviderSkill(repo string, provider workflow.ProviderPlan) RenderedSkill {
	repo = valueOr(strings.TrimSpace(repo), "owner/repo")
	description := fmt.Sprintf("Use issue-spec with the registered %s code provider for neutral code-change, review, and evidence workflows.", provider.DisplayName)
	body := providerWorkflowBody(repo, provider)
	return RenderedSkill{Name: "issue-spec-code-provider", Content: renderSkill("issue-spec-code-provider", description, body)}
}

func ProviderWorkflowNotice(provider workflow.ProviderPlan) string {
	lines := []string{
		"## External Code Provider",
		"",
		fmt.Sprintf("- Provider: `%s` (%s)", provider.ProviderKey, provider.DisplayName),
		fmt.Sprintf("- Code-change term: %s", provider.CodeChangeLabel),
	}
	if provider.ChangeCreate {
		lines = append(lines, "- `change.create`: available; issue-spec may request a provider-neutral external change creation.")
	} else {
		lines = append(lines, "- `change.create`: unavailable; associate a pre-existing external change before implementation gates.")
	}
	if provider.ChangeComment {
		lines = append(lines, "- `change.comment`: available; external finding and reply write-back may use the neutral comment operation.")
	} else {
		lines = append(lines, "- `change.comment`: unavailable; keep external discussion on the provider and synchronize only supported evidence.")
	}
	if provider.EvidenceSnapshot {
		lines = append(lines, "- `evidence.snapshot`: available; synchronize exact-revision evidence before verify and runner gates.")
	} else {
		lines = append(lines, "- `evidence.snapshot`: unavailable; no generated step may claim automatic pre-gate synchronization.")
	}
	lines = append(lines,
		"- Provider executables, arguments, environment, and credentials are operator-owned and must never be read from repository files.",
		"- Project/work-item tracker authority is independent and is not enabled by this code-provider selection.")
	return strings.Join(lines, "\n")
}

func providerWorkflowBody(repo string, provider workflow.ProviderPlan) string {
	steps := []string{
		"# Provider-neutral Code Workflow",
		"",
		fmt.Sprintf("This checkout uses `%s` for external code authority and `%s` for issue authority.", provider.ProviderKey, repo),
		"",
		ProviderWorkflowNotice(provider),
		"",
		"## Flow",
		"",
		"1. Read the active source binding and code-change reference from issue-spec; do not infer provider authority from the issue server hostname.",
	}
	if provider.ChangeCreate {
		steps = append(steps, "2. When a new external change is required, use the issue-spec operation that requests neutral `change.create`; core discovers capability before mutation.")
	} else {
		steps = append(steps, "2. Create the external change outside issue-spec, then associate its stable provider/repository/change identity; `change.create` is not available.")
	}
	if provider.ChangeComment {
		steps = append(steps, "3. Use neutral `change.comment` for supported finding/reply write-back, preserving canonical FINDING/PROCESS/SPEC linkage.")
	} else {
		steps = append(steps, "3. Do not request external finding/reply write-back because `change.comment` is not available.")
	}
	if provider.EvidenceSnapshot {
		steps = append(steps, "4. Before verify and runner gates, synchronize a provider snapshot for the exact active head revision, then evaluate only server-accepted evidence IDs.")
	} else {
		steps = append(steps, "4. Verify only against already-authoritative server evidence; automatic provider snapshot synchronization is unavailable.")
	}
	steps = append(steps, "5. Configure a project/work-item tracker only through a separate explicit provider selection; this code-provider workflow grants no tracker authority.")
	return strings.Join(steps, "\n") + "\n"
}
