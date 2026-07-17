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
	}
	if provider.ChangeCreate {
		lines = append(lines, "- `change.create`: available; an operator-provided code-host skill or bridge may create an external change.")
	} else {
		lines = append(lines, "- `change.create`: unavailable; associate a pre-existing external change before implementation gates.")
	}
	if provider.ChangeComment {
		lines = append(lines, "- `change.comment`: available; external finding and reply write-back may use the neutral comment operation.")
	} else {
		lines = append(lines, "- `change.comment`: unavailable; keep external discussion on the provider and synchronize only supported evidence.")
	}
	if provider.EvidenceSnapshot {
		lines = append(lines, "- `evidence.snapshot`: available; synchronize exact-revision evidence before verification gates. Runner dispatch synchronization remains an explicit `external_code.evidence.sync_before` project-policy opt-in.")
	} else {
		lines = append(lines, "- `evidence.snapshot`: unavailable; no generated step may claim automatic pre-gate synchronization.")
	}
	lines = append(lines,
		"- Capabilities are policy and evidence contracts, not implied issue-spec CLI commands. Use only an approved operator-provided code-host skill or bridge for mutations.",
		"- On a self-hosted profile, `code-change attach` validates and associates an existing provider change at an exact revision; it never creates that change or ingests evidence.",
		"- After attach, `code-change link-process` links one PROCESS to the unique active code change with representation-version CAS.",
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
		"1. Read the active Source Binding and code-change references from issue-spec; the binding supplies provider and external repository identity. Do not infer provider authority from the issue server hostname.",
	}
	if provider.ChangeCreate {
		steps = append(steps, "2. When a new external change is required, create it with an operator-provided trusted code-host skill or bridge. `change.create` is a capability contract, not an implied issue-spec CLI command; stop and request operator setup when no approved skill or bridge is available.")
	} else {
		steps = append(steps, "2. Create the external change outside issue-spec; `change.create` is not available.")
	}
	steps = append(steps, "3. Validate and attach that existing change at its exact provider revision with `issue-spec --profile <self-hosted-profile> code-change attach --repo "+repo+" --implement <issue> --change-id <id> --revision <revision>`. Attach does not create the change or ingest review/CI evidence. Refresh only the same active change and provide `--refresh --expected-version <version>` together.")
	steps = append(steps, "4. Link each PROCESS with `issue-spec --profile <self-hosted-profile> code-change link-process --repo "+repo+" --implement <issue> --process PROCESS-001 --expected-version <comment-version>`. The command requires exactly one active `code_change`; the same URL is a no-op and a different URL conflicts.")
	steps = append(steps, "5. If active references are ambiguous, inspect the Implement Issue references, explicitly delete only the unwanted active reference through the self-hosted native references API or UI, then retry. Never guess or silently overwrite.")
	if provider.ChangeComment {
		steps = append(steps, "6. Use neutral `change.comment` for supported finding/reply write-back, preserving canonical FINDING/PROCESS/SPEC linkage.")
	} else {
		steps = append(steps, "6. Do not request external finding/reply write-back because `change.comment` is not available.")
	}
	if provider.EvidenceSnapshot {
		steps = append(steps, "7. Before verification gates, synchronize a provider snapshot for the exact active head revision, then evaluate only server-accepted evidence IDs. Add `runner` to `external_code.evidence.sync_before` only when every dispatch is expected to have an active external change.")
	} else {
		steps = append(steps, "7. Verify only against already-authoritative server evidence; automatic provider snapshot synchronization is unavailable.")
	}
	steps = append(steps, "8. Keep review, merge, and closure on the selected code provider; do not substitute GitHub PR endpoints for a self-hosted workflow.")
	steps = append(steps, "9. Configure a project/work-item tracker only through a separate explicit provider selection; this code-provider workflow grants no tracker authority.")
	return strings.Join(steps, "\n") + "\n"
}
