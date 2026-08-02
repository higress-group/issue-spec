package templates

import (
	"fmt"
	"strings"

	"github.com/higress-group/issue-spec/internal/workflow"
)

// IssueSpecProviderSkill renders one provider-neutral code-change workflow.
// Provider capabilities are policy and evidence contracts, not implied CLI
// commands. Vendor CLI commands and operator registration details never enter
// the checkout.
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
		fmt.Sprintf("- Semantic generation: `%s`", provider.SemanticGeneration),
		fmt.Sprintf("- Immutable bridge build: `%s`", provider.ProviderBuildIdentity),
		fmt.Sprintf("- Required merge capabilities: review-decision=%t authoritative-check-conclusion=%t merge-conditional=%t",
			provider.ReviewDecision, provider.AuthoritativeCheckConclusion, provider.MergeConditional),
	}
	lines = append(lines,
		"- The bridge must report `minimal-merge-authority/v1`, this immutable build, and the complete capability set before authority is read or anything mutates. Mixed releases and unknown fields fail closed.",
		"- Provider review decisions identify stable authenticated reviewers and trusted canonical principals at the exact subject. Bridge writer, logical Agent, login spelling, email, and display text are not reviewer identity.",
		"- Configured checks use opaque provider-native keys plus owner/integration identity. Core consumes exactly one provider-selected current conclusion and never chooses among attempts.",
		"- `merge-check` is read-only. `code-change merge --expected-head` recollects fresh authority and passes the provider-issued complete authority token to conditional merge.",
		"- Ordinary GitHub REST read-then-write cannot atomically protect same-head policy, review, conversation, finding, and check drift; it remains fail-closed unless an operator bridge proves complete provider-native enforcement.",
		"- Historical REVIEW, VERIFY, PROCESS evidence, rationale, receipts, coverage, finalization, and Archive data are audit-only and never become merge input.",
		"- After freshly observed provider merge, exact selected-Issue reconciliation is idempotent bookkeeping and may be retried independently.",
		"- Provider executables, arguments, environment, and credentials are operator-owned and must never be read from repository files.",
		"- Project/work-item tracker authority is independent and is not enabled by this code-provider selection.")
	return strings.Join(lines, "\n")
}

func providerWorkflowBody(repo string, provider workflow.ProviderPlan) string {
	steps := []string{
		"# Provider-bound Merge Workflow",
		"",
		fmt.Sprintf("This checkout uses `%s` for external code authority and `%s` for issue authority.", provider.ProviderKey, repo),
		"",
		ProviderWorkflowNotice(provider),
		"",
		"## Flow",
		"",
		"1. Read the active Source Binding and selected change scope. The binding supplies provider and external repository identity; never infer provider authority from the issue-server hostname.",
		"2. Run release preflight and require the exact CLI, Server, Runner, generated-asset, semantic-generation, immutable bridge-build, capability, configured-check, canonical-principal, review-mode, reconciliation, and conditional-merge identities. Stop on any mixed or missing value.",
	}
	if provider.ChangeCreate {
		steps = append(steps, "3. Create a change only through an approved operator tool when needed; `change.create` is a capability contract, not an implied issue-spec command.")
	} else {
		steps = append(steps, "3. Select a pre-existing provider change; `change.create` is unavailable.")
	}
	steps = append(steps, "4. Resolve or attach the exact provider change only for navigation and subject selection. Optional PROCESS links remain planning metadata and never enter merge-check.")
	steps = append(steps, "5. Obtain provider-native policy-complete review and provider-selected current required-check conclusions for the exact head. Do not synchronize them into typed workflow comments.")
	steps = append(steps, "6. Run `issue-spec merge-check --repo "+repo+" (--issue <n> | --proposal <n> [--design <n>] [--implement <n>]) --change-id <id> --head <exact-head> --json`. Treat output as a read-only current decision, never saved proof.")
	steps = append(steps, "7. Merge only with `issue-spec code-change merge` using the same selected scope and caller-observed `--expected-head`. The command recollects and reevaluates fresh authority and the provider atomically validates its complete token.")
	steps = append(steps, "8. Freshly observe merged state and retry idempotent selected-Issue reconciliation independently if bookkeeping fails.")
	steps = append(steps, "9. Configure a project/work-item tracker only through a separate explicit provider selection; this code-provider workflow grants no tracker authority.")
	return strings.Join(steps, "\n") + "\n"
}
