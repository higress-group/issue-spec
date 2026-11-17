package templates

import _ "embed"

//go:embed assets/workflow-feedback.md
var workflowFeedbackReference string

func feedbackSkill() RenderedSkill {
	const name = "issue-spec-feedback"
	const description = "Capture reproducible friction from any project skill, CLI command, or workflow. Use when instructions, references, or scripts cause a concrete problem during work."
	const body = `# Issue Spec Feedback

When a skill's instructions, references, or scripts cause a reproducible problem, record the skill name, repository-relative path, applicable revision, observed behavior, and impact on the task. Direct the report to the repository that owns the cause.

Use applicable project rules and guidance.shared entries from the selected workflow config when available. Follow the [feedback guide](../issue-spec-workflow/references/workflow-feedback.md) for finding the matching Issue, recording evidence, and handling retries. Issue writes use the task's authorization and the target backend's permissions.`
	return RenderedSkill{Name: name, Content: renderSkill(name, description, body)}
}
