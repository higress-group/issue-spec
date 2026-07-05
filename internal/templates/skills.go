package templates

import (
	"fmt"
	"strings"
)

const IssueSpecGeneratedBy = "issue-spec"

type WorkflowTemplate struct {
	Name        string
	Description string
	CommandID   string
	CommandName string
	Body        string
}

type RenderedSkill struct {
	Name    string
	Content string
}

type CommandContent struct {
	ID          string
	Name        string
	Description string
	Category    string
	Tags        []string
	Body        string
}

func IssueSpecSkills(repo string) []RenderedSkill {
	workflows := issueSpecWorkflows(repo)
	out := make([]RenderedSkill, 0, len(workflows)+1)
	for _, tmpl := range workflows {
		out = append(out, RenderedSkill{Name: tmpl.Name, Content: renderSkill(tmpl.Name, tmpl.Description, tmpl.Body)})
	}
	out = append(out, githubCLISkill())
	return out
}

func IssueSpecCommandContents(repo string) []CommandContent {
	workflows := issueSpecWorkflows(repo)
	out := make([]CommandContent, 0, len(workflows))
	for _, tmpl := range workflows {
		if strings.TrimSpace(tmpl.CommandID) == "" {
			continue
		}
		out = append(out, CommandContent{
			ID:          tmpl.CommandID,
			Name:        tmpl.CommandName,
			Description: tmpl.Description,
			Category:    "Workflow",
			Tags:        []string{"workflow", "issue-spec"},
			Body:        tmpl.Body,
		})
	}
	return out
}

func issueSpecWorkflows(repo string) []WorkflowTemplate {
	repo = valueOr(strings.TrimSpace(repo), "owner/repo")
	workflows := []WorkflowTemplate{
		{
			Name:        "issue-spec-workflow",
			Description: "Use issue-spec to run an issue-native OpenSpec-style workflow with GitHub issues, typed comments, PR review comments, final verification, and durable spec archive PRs.",
			Body: `# Issue Spec Workflow

Use this skill for issue-native OpenSpec work. Active change artifacts live in GitHub issues and issue comments; durable specs are repository files created after implementation merge.

## Start

1. Run issue-spec auth status --json and confirm the active auth source and GitHub backend.
2. Run issue-spec status --repo {{repo}} --proposal <issue> --design <issue> --implement <issue> --json when issues already exist.
3. For new work, create proposal, design, and implement issues with issue-spec issue create and pass --body-file with concrete markdown content.
4. When an issue body changes, update it in place with issue-spec issue update --body-file and include --summary for the human-readable audit trail.
5. Store requirements, tasks, process ownership, review, and verify evidence as typed comments.

## GitHub Backend

- Local agents may rely on native GitHub CLI support: when no ISSUE_SPEC_TOKEN, GH_TOKEN, GITHUB_TOKEN, keyring token, or issue-spec config token is present and gh auth status --active succeeds for the target host, issue-spec auto-selects the gh backend.
- Explicit env or stored issue-spec tokens keep the rest backend under auto selection. Set ISSUE_SPEC_GITHUB_BACKEND=rest or ISSUE_SPEC_GITHUB_BACKEND=gh only when a workflow needs deterministic backend selection.
- The gh backend proxies GitHub API operations through gh api and uses gh --hostname for Enterprise hosts. It does not replace local git commands.
- ISSUE_SPEC_API_URL applies to the rest backend. Forced gh mode should be used only with hosts that gh can address.
- Use ISSUE_SPEC_TOKEN="$(gh auth token)" only for older issue-spec versions or when deliberately forcing rest while sourcing the token from gh.

## Rules

- Create SPEC comments before design; each SPEC must be testable and include WHEN/THEN scenarios.
- Do not leave active proposal/design/implement issue bodies as TBD placeholders.
- Resolve blocking QUESTION comments before design/tasks, or explicitly record accepted assumptions.
- Link SPEC <-> TASK and TASK <-> PROCESS with issue-spec link.
- Link every PROCESS to the implementation PR with issue-spec pr link-process.
- Treat Agent as the logical role or workflow-assigned label. Treat Agent Session ID and Agent Session Source as artifact writer provenance, not runner resume metadata.
- When dispatching subagents, assign each subagent an explicit subagent/session id and tell it to pass that value with --agent-session to issue-spec writer commands. In Codex, CODEX_THREAD_ID may override that value as the resolved artifact writer session id; outside Codex, --agent-session is the explicit fallback and missing session metadata is non-strict by default.
- In runner mode, runner.public_session_id is the public /resume handle. Coordinator-authored proposal, design, implement, handoff, and update issue bodies or comments should include runner.public_session_id and /resume <public-session-id> <answer or next instruction> when available. Do not present Agent Session ID, CODEX_THREAD_ID, acpx record ids, or provider session ids as /resume handles.
- For non-trivial changes, include review PROCESS nodes in the DAG; review agents are scheduled like worker agents and can run in parallel when their review scopes are independent.
- Small changes may stay coordinator-only, but record the serial execution decision in the implement or VERIFY evidence.
- Before human review, add PR rationale comments with issue-spec pr rationale for every active PROCESS.
- Use issue-spec review finding for PR line findings and issue-spec review reply to close the original thread.
- Run issue-spec review sync and issue-spec verify before declaring ready.
- After the implementation PR merges, create the separate durable spec PR with issue-spec archive durable-spec --create-pr. Use an abstract long-lived --capability directory, inspect existing related durable specs, and regroup the generated draft by stable capability modules before merge.

## Coordinator DAG Execution

1. Treat PROCESS comments as DAG nodes with explicit owner, dependencies, write or review scope, PR link, and evidence.
2. Select ready PROCESS nodes whose dependencies are done and whose scopes do not overlap.
3. Dispatch independent worker PROCESS nodes in parallel when their file/module ownership is disjoint; include each worker's assigned subagent/session id and require it to pass that id with --agent-session on supported issue-spec writer commands.
4. Dispatch independent review PROCESS nodes in parallel for non-trivial PRs after PR rationale exists.
5. Integrate completed worker outputs by dependency order; route P0/P1 review findings back to the owner PROCESS.
6. Mark PROCESS nodes done only after their implementation or review evidence is recorded and blocking findings are resolved.
`,
		},
		{
			Name:        "issue-spec-propose",
			Description: "Create or continue proposal, SPEC, QUESTION, design, and TASK artifacts for an issue-spec change.",
			CommandID:   "propose",
			CommandName: "Issue Spec: Propose",
			Body: `# Issue Spec Propose

Use when the user asks for /issue-spec:propose, issue-spec propose, creating a change proposal, drafting SPEC comments, or preparing design/tasks after questions converge.

## Steps

1. Create the proposal issue:

       issue-spec issue create proposal --repo {{repo}} --change <change-name> --body-file <proposal.md>

2. If the proposal body needs revision after discussion, update it in place:

       issue-spec issue update --repo {{repo}} --issue <proposal-issue> --body-file <proposal.md> --summary "<what changed>"

3. Add SPEC comments with issue-spec comment upsert --type SPEC. SPEC comments must use MUST/SHALL and WHEN/THEN scenarios.
4. Add QUESTION comments for unresolved behavior with issue-spec question create and resolve blocking questions before design.
5. Create the design issue after SPEC/QUESTION convergence:

       issue-spec issue create design --repo {{repo}} --change <change-name> --proposal <proposal-issue-or-url> --body-file <design.md>

6. Add TASK comments with issue-spec comment upsert --type TASK and link every TASK to covered SPEC comments with issue-spec link.
7. Create the implement issue once tasks are ready:

       issue-spec issue create implement --repo {{repo}} --change <change-name> --proposal <proposal-issue-or-url> --design <design-issue-or-url> --body-file <implement.md>

8. Run issue-spec verify-links and fix missing backlinks before implementation.
`,
		},
		{
			Name:        "issue-spec-apply",
			Description: "Implement PROCESS comments for an issue-spec change and keep PR traceability synchronized.",
			CommandID:   "apply",
			CommandName: "Issue Spec: Apply",
			Body: `# Issue Spec Apply

Use when the user asks for /issue-spec:apply, issue-spec apply, or implementing PROCESS/TASK scopes from an issue-spec change.

## Steps

1. Read proposal/design/implement issue context and list typed comments with issue-spec comment list --json.
2. Confirm issue-spec auth status --json includes the expected GitHub backend. Local gh-authenticated sessions can use the native gh backend; keep ISSUE_SPEC_TOKEN="$(gh auth token)" only as an older-version or forced-rest compatibility path.
3. Create or update PROCESS comments with owner agent, scope, dependencies, write ownership, and status.
   Keep Agent as the logical role. Pass assigned subagent/session ids with --agent-session; Codex CODEX_THREAD_ID remains the artifact writer session source of truth when present.
4. Split non-trivial work into independent worker PROCESS nodes when file/module ownership does not overlap; execute independent workers in parallel when available.
5. Add dedicated review PROCESS nodes for non-trivial changes. Review PROCESS nodes should own review scopes such as CLI/API behavior, workflow docs, tests, compatibility, or security-sensitive surfaces.
6. Link each PROCESS to its TASK comments with issue-spec link.
7. Implement the code changes for one PROCESS scope at a time, or integrate completed worker outputs by dependency order.
8. Link every worker and review PROCESS to the PR with issue-spec pr link-process.
9. Add PR rationale comments on key changed lines with issue-spec pr rationale, each linked to a SPEC comment.
10. Mark PROCESS comments done only after implementation/review work and focused verification evidence exist.

## Coordinator DAG Execution

1. Build the ready set from PROCESS nodes whose dependencies are done.
2. Keep immediate blocking work local when the next step depends on it.
3. Spawn or assign independent worker agents only when their write ownership is disjoint, and give each worker an assigned id to pass via --agent-session.
4. Spawn or assign independent review agents only when their review scopes are disjoint.
5. Integrate completed outputs by dependency order and update PROCESS evidence before marking done.
`,
		},
		{
			Name:        "issue-spec-review",
			Description: "Review an issue-spec implementation PR, create PR line findings, reply after fixes, and sync REVIEW comments.",
			CommandID:   "review",
			CommandName: "Issue Spec: Review",
			Body: `# Issue Spec Review

Use when the user asks for /issue-spec:review, issue-spec review, or a PR review gate for an issue-spec implementation.

## Steps

1. Run issue-spec review sync --repo {{repo}} --pr <number> --implement <issue> --id REVIEW-<n> --json to capture current rationale comments, findings, checks, and artifact writer session diagnostics.
2. For non-trivial PRs, spawn or assign dedicated review agents as review PROCESS owners. Multiple review agents can run in parallel when their review scopes are independent.
3. Give each review agent a concrete scope and expected output: actionable findings only, severity, file/line, linked SPEC, owner PROCESS, and suggested fix.
4. Create actionable PR line findings with issue-spec review finding. Use P0/P1 for blockers and P2 for non-blocking follow-up. Pass the review agent's assigned id with --agent-session.
5. Assign every finding to a PROCESS owner. If no findings are found, record that result in REVIEW or VERIFY evidence.
6. After the worker fixes a finding, reply to the original thread with issue-spec review reply --status resolved.
7. Re-run review sync. P0/P1 findings must be resolved before final verify/archive.

## Review DAG Policy

1. Every non-trivial PR should have at least one dedicated review PROCESS node before final verify.
2. Use multiple review agents in parallel when scopes are independent, for example CLI/API behavior, workflow docs, tests, compatibility, or security-sensitive surfaces.
3. A review agent reports findings only; the coordinator converts actionable line findings into issue-spec review finding comments.
4. P0/P1 findings block final verify until the owner PROCESS fixes them and issue-spec review reply records the resolution on the original thread.
5. If a review agent finds no issues, record that result in REVIEW or VERIFY evidence before marking the review PROCESS done.
`,
		},
		{
			Name:        "issue-spec-verify",
			Description: "Run final issue-spec verification across traceability, questions, review findings, PR rationale, PR checks, and durable spec draft.",
			CommandID:   "verify",
			CommandName: "Issue Spec: Verify",
			Body: `# Issue Spec Verify

Use when the user asks for /issue-spec:verify, issue-spec verify, or final readiness evidence before merge/archive.

## Steps

1. Run focused project tests and record evidence in VERIFY comments.
2. Run issue-spec verify-links --repo {{repo}} --proposal <issue> --design <issue> --implement <issue> --json.
3. Render a durable spec draft:

       issue-spec archive durable-spec --repo {{repo}} --proposal <issue> --capability <capability> --output /tmp/<capability>-spec.md --json

4. Run final verify:

       issue-spec verify --repo {{repo}} --proposal <issue> --design <issue> --implement <issue> --pr <pr> --durable-spec /tmp/<capability>-spec.md --json

5. Final verify must fail if blocking questions, missing links, missing PROCESS rationale, open P0/P1 findings, failed or pending PR checks, or durable spec omissions exist.
`,
		},
		{
			Name:        "issue-spec-archive",
			Description: "Create the post-merge durable spec archive PR for an issue-spec change.",
			CommandID:   "archive",
			CommandName: "Issue Spec: Archive",
			Body: `# Issue Spec Archive

Use when the user asks for /issue-spec:archive, issue-spec archive, or creating the post-merge durable spec PR.

## Steps

1. Confirm the implementation PR is merged.
2. Choose the --capability value as a stable long-lived capability or domain directory, not the original change/proposal name. Prefer names that can host related future durable specs, for example workflow-identity-and-sessions instead of agent-session-source-of-truth.
3. Inspect existing durable specs before creating or finalizing the archive PR. Read ` + "`issue-spec/specs/<capability>/spec.md`" + ` when it exists, and scan related ` + "`issue-spec/specs/*/spec.md`" + ` files when the new behavior may belong with an existing capability. Decide whether to update, merge, or reorganize existing durable requirements instead of adding a duplicate or narrowly named spec.
4. Create the durable spec PR:

       issue-spec archive durable-spec --repo {{repo}} --proposal <issue> --capability <capability> --create-pr --branch issue-spec/durable-spec-<capability> --json

5. Review and edit the generated durable spec draft before handoff or merge. Reconcile it with any existing related durable specs, regroup related source SPEC content into durable capability modules instead of preserving one-to-one source SPEC sections, and keep Source SPEC links for traceability.
6. Keep only long-lived behavior. Do not copy process records, review findings, or verification logs into durable specs.
7. After durable spec PR merge, keep proposal/design/implement issues as audit history unless the project policy says to close them.
`,
		},
	}

	for i := range workflows {
		workflows[i].Body = strings.ReplaceAll(workflows[i].Body, "{{repo}}", repo)
	}
	return workflows
}

func renderSkill(name, description, body string) string {
	return renderSkillWithCompatibility(name, description, "Requires issue-spec CLI.", body)
}

func renderSkillWithCompatibility(name, description, compatibility, body string) string {
	return fmt.Sprintf(`---
name: %s
description: %s
license: MIT
compatibility: %s
metadata:
  author: issue-spec
  version: "1.0"
  generatedBy: "%s"
---

%s`, name, description, compatibility, IssueSpecGeneratedBy, strings.TrimSpace(body)+"\n")
}

func githubCLISkill() RenderedSkill {
	const name = "issue-spec-github"
	const description = "Use GitHub CLI for GitHub issues, pull requests, CI runs, and API queries that issue-spec does not wrap."
	const body = `# GitHub CLI

Use the ` + "`gh`" + ` CLI to interact with GitHub repositories, issues, pull requests, CI, and API endpoints.

## When To Use

- Checking PR status, reviews, mergeability, or CI checks.
- Creating, viewing, updating, closing, or commenting on GitHub issues.
- Listing or inspecting pull requests, workflow runs, releases, labels, or repository metadata.
- Calling GitHub API endpoints with ` + "`gh api`" + ` when issue-spec does not provide a dedicated command.

## When Not To Use

- Local git operations such as commit, branch, fetch, merge, or push. Use ` + "`git`" + ` directly.
- Non-GitHub repositories. Use the matching provider CLI instead.
- Complex code review across local diffs. Read the repository files directly and use issue-spec review commands for traceable findings.

## Setup

` + "```bash" + `
gh auth login
gh auth status
` + "```" + `

## Common Commands

` + "```bash" + `
gh issue list --repo owner/repo --state open
gh issue view 42 --repo owner/repo --json number,title,state,url,body
gh issue comment 42 --repo owner/repo --body "Comment body"

gh pr list --repo owner/repo
gh pr view 17 --repo owner/repo --json number,title,state,headRefName,baseRefName,url
gh pr checks 17 --repo owner/repo

gh run list --repo owner/repo --limit 10
gh run view <run-id> --repo owner/repo --log-failed

gh api repos/owner/repo/labels --jq '.[].name'
` + "```" + `

## Notes

- Always pass ` + "`--repo owner/repo`" + ` when the current directory is not definitely inside the target repository.
- Use GitHub URLs directly when convenient, for example ` + "`gh pr view https://github.com/owner/repo/pull/17`" + `.
- Prefer structured output with ` + "`--json`" + ` and ` + "`--jq`" + ` when another command or agent step consumes the result.
- issue-spec owns the proposal, design, implement, typed comment, review, verify, and archive workflow state. Use ` + "`gh`" + ` for adjacent GitHub operations that are outside issue-spec's command surface.
`
	return RenderedSkill{Name: name, Content: renderSkillWithCompatibility(name, description, "Requires GitHub CLI (gh).", body)}
}
