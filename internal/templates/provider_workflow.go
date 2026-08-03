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
		"- The actual code writer owns line-rationale drafts for non-obvious decisions: repository-relative path, stable symbol plus changed-line anchor, why/tradeoff/risk, and no secret, raw payload, or credential. Writers need no provider access, never guess final diff positions, and produce no filler, quota, or coverage comments.",
		"- After pushing the exact head, the Coordinator validates anchors, continued applicability, and sensitive-data absence, then publishes unchanged worker rationale as provider-native non-blocking inline discussions through an approved native review tool. The generic `change.comment` operation guarantees ordinary comments but does not standardize diff coordinates. Invalid, stale, or sensitive drafts return to the writer or are dropped with explanation, never Coordinator-rewritten under worker authorship. The ordinary top-level provider discussion `### Implementation Rationale` summarizes and indexes valid comments.",
		"- If non-blocking inline discussion is unsupported or an inline discussion would become an unresolved merge blocker, keep `path:symbol/line` plus the worker rationale in the top-level discussion instead. These are mutable review UX only: no typed carrier, marker, rationale ID, PROCESS/SPEC binding, evidence field, gate, or merge input.",
		"- A failed requested rationale write must be reported and the rendered body retained for retry or manual posting. It does not create an issue-spec merge blocker because issue-spec never decides merge.",
		"- Handoff reports the exact head, change link, tests, risks, and rationale to the human, then stops before approval or merge.",
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
	steps = append(steps, "5. Validate writer anchors against the pushed exact head, continued applicability, and sensitive-data absence. Publish valuable unchanged text through a safe provider-native inline discussion and publish or refresh the ordinary top-level `### Implementation Rationale` summary/index. When safe inline discussion is unavailable, keep `path:symbol/line` plus worker rationale in the top-level discussion. Publish no filler and create no evidence carrier or gate.")
	steps = append(steps, "6. Report the exact head, change link, tests run and results, known risks, boundaries, and rationale status to the human reviewer.")
	steps = append(steps, "7. Stop. The human reviews current provider-native CI, approvals, conversations, and branch policy and decides whether to merge in the provider UI.")
	steps = append(steps, "8. Configure a project/work-item tracker only through a separate explicit provider selection; this code-provider workflow grants no tracker authority.")
	return strings.Join(steps, "\n") + "\n"
}
