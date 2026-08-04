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
		"- Before handoff, a real read-only reviewer independent of every code writer checks the exact base and current exact head without write access or provider credentials. Every P0/P1 returns unchanged to the original writer that owns the affected code; after repair, focused tests, integration, and push, the same reviewer rechecks the new head. This repeats automatically until zero P0/P1 remain.",
		"- Only still-applicable P2 findings from the final reviewed head are published unchanged. Prefer a provider-native non-blocking line comment when an approved tool supports safe line coordinates; otherwise use the provider-neutral ordinary `change.comment` operation and preserve `path:symbol/line`. P2 never enters the repair loop or pauses completion. Unavailable or failed publication is reported with the rendered body while the workflow continues.",
		"- Finding convergence creates no typed REVIEW/VERIFY, finding evidence, receipt, readiness gate, or reviewer merge authority. Review and repair routing require no PROCESS unless a separate managed-coordination need exists.",
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
	steps = append(steps, "5. Dispatch the independent read-only reviewer against the exact base and pushed head. Route every P0/P1 unchanged to its original writer, integrate and push the tested repair, and have the same reviewer recheck. Repeat automatically until zero P0/P1 remain.")
	steps = append(steps, "6. Publish each final-head P2 unchanged as a safe provider-native non-blocking line comment when supported; otherwise publish an ordinary change-level `change.comment` preserving `path:symbol/line`. Never pause for P2 or put it in the repair loop. If publication is unavailable or fails, retain and report the rendered body while continuing.")
	steps = append(steps, "7. Validate writer rationale anchors against the final pushed exact head, continued applicability, and sensitive-data absence. Publish valuable unchanged text through a safe provider-native inline discussion and publish or refresh the ordinary top-level `### Implementation Rationale` summary/index. When safe inline discussion is unavailable, keep `path:symbol/line` plus worker rationale in the top-level discussion. Publish no filler and create no evidence carrier or gate.")
	steps = append(steps, "8. Report the exact head, change link, tests run and results, known risks, boundaries, P2 publication status, and rationale status to the human reviewer.")
	steps = append(steps, "9. Stop. The human reviews current provider-native CI, approvals, conversations, and branch policy and decides whether to merge in the provider UI.")
	steps = append(steps, "10. Configure a project/work-item tracker only through a separate explicit provider selection; this code-provider workflow grants no tracker authority.")
	return strings.Join(steps, "\n") + "\n"
}
