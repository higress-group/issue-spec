package templates

import (
	"fmt"
	"strings"

	"github.com/higress-group/issue-spec/internal/workflow"
)

// IssueSpecProviderSkill renders one provider-neutral code-change workflow.
// Capabilities describe supported operations, not merge policy or implied CLI
// commands. Vendor commands and operator registration details never enter the
// checkout.
func IssueSpecProviderSkill(repo string, provider workflow.ProviderPlan) RenderedSkill {
	repo = valueOr(strings.TrimSpace(repo), "owner/repo")
	description := fmt.Sprintf("Use issue-spec with the registered %s code provider to prepare a code change for human review and merge.", provider.DisplayName)
	body := providerWorkflowBody(repo, provider)
	return RenderedSkill{Name: "issue-spec-code-provider", Content: renderSkill("issue-spec-code-provider", description, body)}
}

func ProviderWorkflowNotice(provider workflow.ProviderPlan) string {
	lines := []string{
		"## External Code Provider",
		"",
		fmt.Sprintf("- Provider: `%s` (%s)", provider.ProviderKey, provider.DisplayName),
		fmt.Sprintf("- Code-change term: %s", provider.CodeChangeLabel),
		fmt.Sprintf("- Supported operations: change-create=%t change-comment=%t audit-snapshot=%t",
			provider.ChangeCreate, provider.ChangeComment, provider.EvidenceSnapshot),
	}
	lines = append(lines,
		"- Capabilities are validated only for the requested operation. Missing automatic merge support never makes this checkout planning-only or disables implementation.",
		"- The provider and human reviewer own CI, review policy, conversations, branch rules, approval, and merge. Issue-spec does not compute or execute a merge decision.",
		"- Historical REVIEW, VERIFY, evidence, finalization, Archive, and merge-authority data are explicit audit-only history and never become handoff input.",
		"- For requested review/publication, follow issue-spec-workflow/references/implementation-review.md and configured project review guidance. No automatic reviewer, PR/MR, push or comment is implied.",
		"- Provider executables, arguments, environment, and credentials are operator-owned and must never be read from repository files.",
		"- Project/work-item tracker authority is independent and is not enabled by this code-provider selection.")
	return strings.Join(lines, "\n")
}

func providerWorkflowBody(repo string, provider workflow.ProviderPlan) string {
	steps := []string{
		"# Provider-bound Human Handoff Workflow",
		"",
		fmt.Sprintf("This checkout uses `%s` for external code changes and `%s` for issue planning.", provider.ProviderKey, repo),
		"",
		ProviderWorkflowNotice(provider),
		"",
		"## Flow",
		"",
		"1. Read the active Source Binding and selected issue or optional planning scope. The binding supplies provider and external repository identity; never infer it from the issue-server hostname.",
		"2. Implement and validate one exact reviewable head. Use optional PROCESS/workspace coordination only when execution risk requires it; neither planning nor execution state is delivery acceptance.",
	}
	if provider.ChangeCreate {
		steps = append(steps, "3. Create the provider change through an approved operator tool when needed; `change.create` is a capability contract, not an implied issue-spec command.")
	} else {
		steps = append(steps, "3. Select or manually create a provider change; registered `change.create` is unavailable.")
	}
	steps = append(steps, "4. Resolve or attach the exact provider change for navigation and human review. Optional PROCESS links remain planning metadata.")
	steps = append(steps, "5. Follow requested project review and publication guidance, report the exact candidate, tests/results, risks and remaining review needs. Approval and merge require separate authorization and provider policy.")

	return strings.Join(steps, "\n") + "\n"
}
