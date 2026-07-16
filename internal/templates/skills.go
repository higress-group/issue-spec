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
2. Run issue-spec status --repo {{repo}} --proposal <issue> --design <issue> --implement <issue> --gate <proposal|design|implement|final|archive> --json when issues already exist. Treat status as a point-in-time forecast; final verify re-observes authoritative remote facts.
3. For new work, create proposal, design, and implement issues with issue-spec issue create and pass --body-file with concrete markdown content.
4. When an issue body changes, update it in place with issue-spec issue update --body-file and include --summary for the human-readable audit trail.
5. Store requirements, tasks, process ownership, review, and verify evidence as typed comments.

## Project Workflow Config

- Run issue-spec workflow validate --repo {{repo}} --json before relying on project templates or legacy OpenSpec workflow definitions.
- issue-spec/config.yaml is the preferred project workflow config. If it is absent, openspec/config.yaml can be reused as a legacy workflow definition source.
- Project schemas live under issue-spec/schemas/<schema>/schema.yaml with templates in templates/*.md. Legacy OpenSpec schemas are read from openspec/schemas/<schema>/schema.yaml only in compatibility mode.
- Active workflow artifacts remain issue-native even when a legacy OpenSpec schema declares file-oriented outputs such as proposal.md, specs/**/*.md, tasks.md, review.md, or verify.md.
- Template rendering cannot weaken typed comment wrapping or canonical SPEC validation.

## GitHub Backend

- Local agents may rely on native GitHub CLI support: when no ISSUE_SPEC_TOKEN, GH_TOKEN, GITHUB_TOKEN, keyring token, or issue-spec config token is present and gh auth status --active succeeds for the target host, issue-spec auto-selects the gh backend.
- Explicit env or stored issue-spec tokens keep the rest backend under auto selection. Set ISSUE_SPEC_GITHUB_BACKEND=rest or ISSUE_SPEC_GITHUB_BACKEND=gh only when a workflow needs deterministic backend selection.
- The gh backend proxies GitHub API operations through gh api and uses gh --hostname for Enterprise hosts. It does not replace local git commands.
- ISSUE_SPEC_API_URL applies to the rest backend. Forced gh mode should be used only with hosts that gh can address.
- Use ISSUE_SPEC_TOKEN="$(gh auth token)" only for older issue-spec versions or when deliberately forcing rest while sourcing the token from gh.

## Rules

- Use issue-spec comment generate to render canonical typed comment bodies (SPEC, TASK, PROCESS, REVIEW, VERIFY) from structured JSON instead of hand-writing Markdown; comment upsert --type SPEC validates and rejects noncanonical SPEC bodies by default, with --allow-noncanonical as a write-time migration bypass only.
- Create SPEC comments before design; each SPEC must be testable and include WHEN/THEN scenarios.
- Self-contained authoring: write proposal, design, SPEC, and TASK artifacts for a reader with no shared session context. Externalize environment-independent background, assumptions, decisions, and rejected alternatives, and replace the template placeholder prompts (the issue-spec:fill sentinel) with real content instead of leaving TBD. This actor-to-actor resume of understanding is distinct from the ### Handoff PROCESS serial-chain evidence section and from the /resume session handle.
- Resolve blocking QUESTION comments before design/tasks, or explicitly record accepted assumptions.
- Link SPEC <-> TASK and TASK <-> PROCESS with issue-spec link.
- Each design TASK must carry an ### Execution Planning section (rendered by comment generate --type TASK): owned modules/write areas, shared touchpoints, dependency/interface assumptions, coupling class, recommended execution mode, and complexity/split guidance. comment upsert --type TASK rejects a TASK that omits it.
- Every PROCESS must record its ### Parent TASK; comment upsert --type PROCESS rejects a PROCESS without one. Serial PROCESS chains under one parent TASK are the default decomposition; each completed serial node records ### Handoff evidence for its successor. Parallelism is a gated optimization enabled only when write ownership is disjoint, not the default.
- Every PROCESS must declare execution_class when generated: change-bearing, review, verification, orchestration, or external. Legacy missing classes project conservatively to change-bearing; unknown classes block. The class carrier is respectively matching path/line rationale, done REVIEW or resolved finding, done VERIFY or required passing check with test evidence, non-empty coordination handoff, or consumed exact-revision provider evidence. New PROCESS comments also declare workspace_management: use managed when the coordinator delegates the node to an issue-spec-allocated worktree (workspace prepare -> child -> complete -> integrate), and independent when the coordinator implements the node inline in its own integration checkout or an external/human executor owns the worktree. independent changes workspace allocation only: it does not change the workflow's existing review or verification policy. Regardless of managed or independent, every active SPEC that has a valid change-bearing carrier MUST be covered by at least one independent review PROCESS whose reviewing agent differs from the code author.
- The coordinator selects the exact PROCESS from the typed DAG, never from command grammar or prompt prose. When runner context is supplied, use the single runner-managed coordinator session and keep its cwd and primary sandbox workspace at the supplied session checkout across new work, resume, cancellation, and restart reconciliation; otherwise operate standalone from the unchanged integration checkout. Never start a nested coordinator session or rebind the coordinator to a PROCESS worktree.
- The coordinator owns every PROCESS workspace lifecycle operation for managed execution and runs issue-spec workflow workspace prepare, inspect, complete, integrate, reconcile, and cleanup with the same --repo, --issue, --process, roots, and owner token. When runner context is supplied, ISSUE_SPEC_PROCESS_INTEGRATION_ROOT and ISSUE_SPEC_PROCESS_WORKSPACE_ROOT provide trusted session-local defaults; otherwise standalone coordinators pass explicit --integration-root and --workspace-root. change-bearing uses a writable owned branch; review and verification use detached immutable workflow snapshots and fail closed when dirty; orchestration uses bookkeeping with no checkout. external uses mode none; completing it and passing the final gate require consumed provider-neutral exact-revision evidence.
- For a delegated (managed) PROCESS, after prepare, delegate through the current runtime native child/subagent facility and pass the exact worktree path, branch, write ownership, PROCESS id, and bounded TASK/handoff context. The child runs with that worktree as its cwd, authors its own result commit, runs focused tests, and returns commit/test/handoff evidence. A runtime-native child is not a coordinator session: issue-spec does not launch a nested coordinator session or claim a separate per-child OS sandbox. When runner context is supplied, children share the runner-managed coordinator session's outer sandbox, which exposes the session checkout and only that session's PROCESS pool; unsafe-no-sandbox has no filesystem isolation.
- For that delegated output, the coordinator validates the child handoff, then runs complete and integrate and updates PROCESS status/handoff/PR/SPEC links. Inline (independent) nodes have no child output and MUST NOT run complete or integrate. When runner context is supplied for restart or resume, the runtime recovers only the runner-managed coordinator session; from the unchanged session checkout the coordinator inspects or reconciles the exact managed PROCESS lease, then completes and integrates it. Otherwise standalone execution remains in the unchanged integration checkout. The coordinator invokes owner-token cleanup only after an explicit integration or retention decision. Runner-managed session retention cleanup consults git worktree list and fails closed by retaining the session checkout when runtime metadata is dirty or uncertain, a linked worktree exists, or git worktree inspection fails; it does not own, persist, or retry child PROCESS cleanup. workflow workspace cleanup is always an explicit owner-token-authorized destructive operation that can remove unintegrated work and does not decide or enforce integration/retention eligibility for its caller.
- Move an existing typed artifact with issue-spec comment transition --id <id> --to <status> instead of regenerating its body. Conditional backends use --expected-version; a backend without CAS fails closed unless --allow-nonatomic is explicit together with --expected-digest, and the result must report atomic: false. Use --handoff-file, --pr, and --related for the only declared mutations.
- Apply multi-artifact desired state with issue-spec workflow reconcile --plan <plan.json> --checkpoint <checkpoint.json> --json. Plans are versioned and dependency ordered; keep the checkpoint and rerun the same plan after pending transport/rate-limit failures so remote re-observation can repair lost responses and partial backlinks.
- Before allocating a delegated worker, run issue-spec doctor agent --repo {{repo}} --operation <operation> --json for every required provider-neutral operation (issue.read, artifact.write, pr.read, pr.review.write, checks.read, git.clone, git.push, or external.change.comment). Strict delegated work requires an operator-owned short-lived issuer; legacy_long_lived mirrored gh credentials are compatibility-only and never satisfy strict operation policy.
- Link every PROCESS to the implementation PR with issue-spec pr link-process.
- Before implementation PR merge, add GitHub closing links to the implementation PR body with issue-spec pr link-issues so GitHub closes the proposal/design/implement issues when the PR merges. issue-spec pr link-issues MUST be the final write to the implementation PR body: the managed closure block lives in the mutable body, so any later full-body edit silently erases it and GitHub then closes only the issues still named in the body (the observed symptom is the proposal and design issues staying open while only the implement issue closes). Any later body edit MUST preserve the managed closure block verbatim, or re-run issue-spec pr link-issues afterward to restore it.
- Gate merge on the closure block: before merging the implementation PR, run issue-spec pr verify-closure --repo owner/repo --pr N --proposal N --design N --implement N (exit 0 = block complete/valid; exit 1 = block missing/incomplete/tampered).
- Treat Agent as the logical role or workflow-assigned label. Treat Agent Session ID and Agent Session Source as artifact writer provenance, not runner resume metadata.
- When dispatching subagents, assign each subagent an explicit subagent/session id and tell it to pass that value with --agent-session to issue-spec writer commands. In Codex, CODEX_THREAD_ID may override that value as the resolved artifact writer session id; outside Codex, --agent-session is the explicit fallback and missing session metadata is non-strict by default.
- When runner context supplies runner.public_session_id, it is the public /resume handle. Coordinator-authored proposal, design, implement, handoff, and update issue bodies or comments should include runner.public_session_id and /resume <public-session-id> <answer or next instruction> when available. Do not present Agent Session ID, CODEX_THREAD_ID, coordinator record ids, or provider session ids as /resume handles.
- Every active SPEC that has a valid change-bearing carrier MUST be covered by at least one independent review PROCESS, and the review MUST be performed by a different agent than the code author; the code author (coordinator or worker) MUST NOT review its own code. This applies to changes of every size: small changes MAY be implemented inline by the coordinator, but still MUST be reviewed by a different agent. Independence is judged by the reviewing agent's --agent identity, which MUST name a real sub-agent actually spawned to perform the review and MUST NOT be a fabricated or reused name used only to pass this check. A single independent review PROCESS MAY cover a SPEC that several implementation PROCESS nodes contribute to; the requirement is per covered SPEC, not one review per implementation PROCESS. Review agents are scheduled like worker agents and can run in parallel when their review scopes are independent.
- After review/fix convergence, add issue-spec pr rationale only for change-bearing PROCESS nodes; other classes satisfy their class-specific carrier and do not invent arbitrary code-line rationale.
- Use issue-spec review finding for PR line findings and issue-spec review reply to close the original thread.
- Run issue-spec review sync and issue-spec verify before declaring ready.
- After the implementation PR merges, create the separate durable spec PR with issue-spec archive durable-spec --create-pr --close-issues, passing the proposal, design, implement, and implementation PR numbers so archive also idempotently closes any still-open active issues. Use an abstract long-lived --capability directory as an umbrella capability that accumulates related current and future changes, inspect existing related durable specs, and regroup the generated draft by stable capability modules before merge. Archive now accumulates new requirements into an existing capability spec by requirement title (newest wins), so re-archiving into an umbrella capability preserves prior requirements instead of overwriting them.

## Coordinator DAG Execution

1. Plan the PROCESS DAG before dispatch: read every active TASK's ### Execution Planning metadata (coupling class, recommended execution mode, owned areas) and derive PROCESS nodes from it.
2. Coding MAY be inline or delegated, and the two paths differ in workspace mechanics. Delegated: declare workspace_management: managed and run workspace prepare -> runtime-native child -> complete -> integrate. Inline: declare workspace_management: independent, implement/test/commit in the coordinator's own integration checkout, and do NOT run prepare/child/complete/integrate for that node. Both paths record change-bearing rationale on the PR, and a serial predecessor still records a bounded ### Handoff (for audit and so the node can later be switched to delegated). Delegation is a recommended technique for keeping the coordinator context bounded and avoiding mid-task compaction on large nodes, not a requirement; inline coding is allowed for any node because independent review is mandatory regardless. After the code becomes reviewable, both paths MUST schedule an independent review PROCESS.
3. Treat PROCESS comments as DAG nodes with explicit owner, parent TASK, dependencies, write or review scope, PR link, and evidence. Declaring a PROCESS node is a plan artifact only; its assigned worker or review sub-agent is spawned lazily at dispatch when that node becomes ready (its dependencies are done), never pre-created to look compliant. Declaring a node MUST NOT instantiate an idle agent that has no work yet -- a review node's agent is spawned only after the code under review exists, not at implement kickoff.
4. Select ready PROCESS nodes whose dependencies are done. Execute an inline (independent) coding node in the coordinator's integration checkout; dispatch only delegated (managed) coding nodes and review nodes to their assigned worker or review agent. Include each dispatched agent's assigned subagent/session id and require it to pass that id with --agent-session on supported issue-spec writer commands. Parallelism is a separate, gated optimization, not the trigger for delegation: run independent nodes concurrently only when their write ownership is provably disjoint.
5. Default to serial PROCESS chains under one parent TASK. When a serial node is delegated, run each node in its own worker connected by a bounded ### Handoff and seed the successor worker with the parent TASK context plus the predecessor ### Handoff rather than the coordinator's accumulated context. When a serial node is inline, the coordinator executes it in the integration checkout but still preserves distinct per-PROCESS state and the same bounded ### Handoff boundary. Each completed serial PROCESS records ### Handoff evidence (the contract/state its successor consumes) before the next node starts; record a reason when a handoff is unnecessary.
6. For each delegated (managed) ready PROCESS, prepare its workspace with the workspace CLI while the coordinator remains in its unchanged integration checkout, using the runner-managed session checkout when runner context is supplied and otherwise the standalone checkout. Pass the returned exact worktree path, branch, ownership, and PROCESS id to a current runtime native child. Do not create another coordinator session or move the coordinator cwd. For an inline (independent) node, skip prepare/child/complete/integrate: the coordinator implements, tests, and commits the node in its own integration checkout, then records rationale and (for a serial predecessor) a bounded ### Handoff.
7. Dispatch an independent review PROCESS for every active SPEC that has a valid change-bearing carrier once reviewable implementation code exists on the PR, whether the code was authored inline or by a worker; do not wait for PR rationale, which is added only after review/fix convergence (adding rationale first would deadlock, since review gates rationale and rationale would gate review). Each review node is owned by an agent that did not author the code under review; run review nodes in parallel only when their review scopes are independent. Route P0/P1 findings to the owner PROCESS or a dedicated repair PROCESS that follows the same serial/parallel gating.
8. For each delegated (managed) output, validate the child result commit and focused-test handoff, then complete and integrate it by dependency order from the coordinator checkout. Inline (independent) nodes skip this child-output lifecycle.
9. Mark PROCESS nodes done only after implementation or review evidence and, for serial predecessors, ### Handoff evidence are recorded and blocking findings are resolved.
10. Gate merge on the closure block: issue-spec pr link-issues must be the final write to the implementation PR body, since any later full-body edit silently erases the managed closure block and GitHub then closes only the issues still named in the body (proposal and design stay open, only implement closes). Run issue-spec pr verify-closure --repo {{repo}} --pr <implementation-pr> --proposal <proposal-issue> --design <design-issue> --implement <implement-issue> before merge; exit 1 means restore the block by re-running pr link-issues.

## Cross-Skill Boundary

The issue-spec workflow is composed of cooperating skills. Each owns a slice
of the link matrix; a single skill never covers the full graph.

Link matrix (each direction has a designated owner; rows marked ✓ are gated by ` + "`verify-links`" + `):
- ✓ SPEC ↔ TASK        (issue-spec-propose, step 7)
- ✓ TASK ↔ PROCESS     (issue-spec-apply, step 6)
-   PROCESS ↔ SPEC     (issue-spec-apply, step 10, via pr rationale and review finding)
-   PROCESS ↔ PR       (issue-spec-apply, step 8, via pr link-process)

` + "`verify-links`" + ` covers SPEC↔TASK and TASK↔PROCESS only; the other two directions
are created by their owner steps but not auto-checked.
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

1. Validate the active workflow definition before creating artifacts:

       issue-spec workflow validate --repo {{repo}} --json

2. Create the proposal issue:

       issue-spec issue create proposal --repo {{repo}} --change <change-name> --body-file <proposal.md>

   Generated titles use the standardized ` + "`Proposal: <subject>`" + `, ` + "`Design: <subject>`" + `, and ` + "`Implement: <subject>`" + ` family. When --body-file is used, the subject comes from the first Markdown H1 when possible while the change name stays in issue-spec metadata. Use --title only for an explicit user-requested custom title; do not apply style-only issue update rewrites after creation. Historical issues with ` + "`issue-spec proposal: <change>`" + `, ` + "`issue-spec design: <change>`" + `, or ` + "`issue-spec implement: <change>`" + ` titles remain valid workflow artifacts.

3. If the proposal body needs revision after discussion, update it in place:

       issue-spec issue update --repo {{repo}} --issue <proposal-issue> --body-file <proposal.md> --summary "<what changed>"

4. Generate canonical SPEC bodies instead of hand-writing Markdown:

       issue-spec comment generate --type SPEC --id SPEC-001 --status confirmed --scope "<scope>" --input-file spec.json | issue-spec comment upsert --repo {{repo}} --issue <proposal-issue> --type SPEC --id SPEC-001 --body-file -

   The SPEC input JSON has requirement.title, requirement.text (use MUST/SHALL), and a scenarios array of title/when/then. comment upsert --type SPEC validates canonical discipline (## Requirement:, normative MUST/SHALL, at least one ### Scenario: with **WHEN**/**THEN** bullets) by default and rejects malformed bodies. Use --allow-noncanonical only as a write-time migration bypass; it does not create durable approval and status/verify/archive keep reporting the noncanonical state.
5. Add QUESTION comments for unresolved behavior with issue-spec question create and resolve blocking questions before design.
6. Create the design issue after SPEC/QUESTION convergence:

       issue-spec issue create design --repo {{repo}} --change <change-name> --proposal <proposal-issue-or-url> --body-file <design.md>

7. Generate TASK bodies with issue-spec comment generate --type TASK --id TASK-001 --input-file task.json and upsert them with issue-spec comment upsert --type TASK. To create the durable SPEC<->TASK links in one step, pass --covers-issue <proposal-issue> to comment upsert: it resolves the SPEC IDs listed in the TASK's ### Covers section to peer comment URLs, writes them onto the TASK's Related Comments, and backlinks each SPEC to the TASK. Order no longer matters and re-running comment upsert preserves existing Related Comments (it never silently drops links); issue-spec link remains available for ad-hoc or cross-issue links. The TASK input JSON has title, summary, checklist, covers (SPEC IDs), and an execution_planning object (owned_areas, shared_touchpoints, dependencies, coupling, execution_mode, complexity) that renders the required ### Execution Planning section; comment upsert --type TASK rejects a TASK without it. Use the same comment generate command family for PROCESS, REVIEW, and VERIFY comments instead of inventing raw Markdown shapes; PROCESS input takes parent_task and handoff fields.
8. Create the implement issue once tasks are ready:

       issue-spec issue create implement --repo {{repo}} --change <change-name> --proposal <proposal-issue-or-url> --design <design-issue-or-url> --body-file <implement.md>

9. Run issue-spec verify-links and fix missing backlinks before implementation.
   This run covers SPEC↔TASK only; after PROCESS comments are created in
   issue-spec-apply (step 6), re-run verify-links to also catch PROCESS↔TASK gaps.

## Cross-Skill Boundary

Process creation, PROCESS↔TASK links, and PROCESS↔PR links live in
` + "`issue-spec-apply`" + `, not here. When you finish propose (TASKs complete),
hand off to apply before re-running ` + "`verify-links`" + ` for full coverage.

Link matrix (each direction has a designated owner; rows marked ✓ are gated by ` + "`verify-links`" + `):
- ✓ SPEC ↔ TASK        (this skill, step 7)
- ✓ TASK ↔ PROCESS     (issue-spec-apply, step 6)
-   PROCESS ↔ SPEC     (issue-spec-apply, step 10, via pr rationale and review finding)
-   PROCESS ↔ PR       (issue-spec-apply, step 8, via pr link-process)
`,
		},
		{
			Name:        "issue-spec-apply",
			Description: "Implement PROCESS comments for an issue-spec change and keep PR traceability synchronized.",
			CommandID:   "apply",
			CommandName: "Issue Spec: Apply",
			Body: `# Issue Spec Apply

Use when the user asks for /issue-spec:apply, issue-spec apply, or implementing PROCESS/TASK scopes from an issue-spec change.

## Prerequisite

This skill assumes ` + "`issue-spec-propose`" + ` has been completed: proposal, design,
implement issues exist with SPEC, TASK typed comments and SPEC↔TASK bidirectional
links. Run ` + "`issue-spec verify-links`" + ` after propose as a smoke check.

## Steps

1. Read proposal/design/implement issue context and list typed comments with issue-spec comment list --json. Run issue-spec status --repo {{repo}} --proposal <n> --design <n> --implement <n> --gate implement --json before dispatch and use --gate final as the pre-verify forecast.
2. Confirm issue-spec auth status --json includes the expected GitHub backend. Local gh-authenticated sessions can use the native gh backend; keep ISSUE_SPEC_TOKEN="$(gh auth token)" only as an older-version or forced-rest compatibility path. Before workspace or worker allocation, run issue-spec doctor agent --repo {{repo}} --operation <required-operation> --json; strict delegated work requires an operator-owned short-lived issuer and never silently downgrades to legacy_long_lived host credentials.
3. Plan the PROCESS DAG before dispatch. The coordinator MAY implement coding nodes inline or delegate them to a worker sub-agent, and the two paths differ in workspace mechanics. Delegated node: declare workspace_management: managed and run workspace prepare -> runtime-native child -> complete -> integrate; the coordinator plans and integrates while the worker holds the bulk coding context. Inline node: declare workspace_management: independent, implement/test/commit in the coordinator's own integration checkout, and do NOT run prepare/child/complete/integrate for it. Both paths record change-bearing rationale, and a serial predecessor still records a bounded ### Handoff (for audit and so the node can later be switched to delegated). Delegation is a recommended technique for keeping the coordinator context bounded and avoiding mid-task compaction on large or context-heavy nodes, not a requirement; inline coding is allowed for any node because independent review (step 5) is MANDATORY regardless: whoever wrote the code MUST NOT be the one who reviews it. Read each active TASK's ### Execution Planning metadata and derive PROCESS nodes from it. Render PROCESS bodies with issue-spec comment generate --type PROCESS --input-file process.json (fields include parent_task, execution_class, workspace_management, owner, dependencies, write_ownership, handoff) instead of hand-writing Markdown; comment upsert --type PROCESS rejects a PROCESS without a ### Parent TASK. A missing legacy declaration remains conservatively managed.
   Keep Agent as the logical role. Pass assigned subagent/session ids with --agent-session; Codex CODEX_THREAD_ID remains the artifact writer session source of truth when present.
4. Default to serial PROCESS chains under one parent TASK. When a serial coding node is delegated, run it in its own worker connected by a bounded ### Handoff, and seed a successor worker with the parent TASK context plus the predecessor ### Handoff, never the coordinator's accumulated context. Inline serial nodes are executed by the coordinator in its own checkout, but still keep per-PROCESS state and record the same bounded ### Handoff boundary. Parallelism is a separate, gated optimization: split into parallel worker PROCESS nodes only when file/module write ownership is provably disjoint.
5. Every active SPEC that has a valid change-bearing carrier MUST be covered by at least one independent review PROCESS, and the review MUST be performed by a different agent than the one that authored the code under review. This applies to changes of every size, whether the code was authored inline or by a worker. The code author -- coordinator or worker -- MUST NOT review its own code by writing the REVIEW itself; self-review does not count, because the author cannot see their own blind spots. Independence is judged by the reviewing agent's --agent identity: it MUST name a real sub-agent that was actually spawned to perform the review, and MUST NOT be a fabricated or reused name passed only to satisfy this check. A single independent review PROCESS MAY cover a SPEC that several implementation PROCESS nodes contribute to; the requirement is per covered SPEC, not one review per implementation PROCESS. Review PROCESS nodes should own review scopes such as CLI/API behavior, workflow docs, tests, compatibility, or security-sensitive surfaces.
6. Link each PROCESS to its TASK comments with issue-spec link.
7. Keep the coordinator in its unchanged integration checkout. When runner context is supplied, use the single runner-managed coordinator session and keep its cwd and primary sandbox workspace at the supplied session checkout across new work, resume, cancellation, and restart reconciliation; otherwise operate standalone with explicit --integration-root and --workspace-root. Select the exact PROCESS from the typed DAG, never from command grammar or prompt prose. Never launch a nested coordinator session or rebind coordinator cwd/sandbox to a PROCESS worktree. For managed (delegated) execution, the coordinator owns every PROCESS workspace lifecycle operation: run issue-spec workflow workspace prepare, inspect, complete, integrate, reconcile, and cleanup with stable --repo, --issue, --process, roots, and owner token. Inline (independent) execution skips prepare/child/complete/integrate and is authored directly in the coordinator's integration checkout; independent execution is not runner-dispatchable. Either way, every active SPEC with a valid change-bearing carrier still needs an independent review PROCESS by a different agent than the author. When runner context is supplied, ISSUE_SPEC_PROCESS_INTEGRATION_ROOT and ISSUE_SPEC_PROCESS_WORKSPACE_ROOT are trusted defaults. Review and verification use detached immutable workflow snapshots and fail closed when dirty, but issue-spec does not claim per-child OS immutability. external uses mode none and requires consumed provider-neutral exact-revision evidence for completion and the final gate.
8. For a delegated (managed) node, after prepare, use the current runtime native child/subagent facility to run the worker. Give it the exact worktree path as cwd plus branch, write ownership, PROCESS id, parent TASK, and predecessor handoff. The worker owns one result commit, focused tests, and a bounded handoff. A runtime-native child is not a coordinator session. When runner context is supplied, children share the runner-managed coordinator session's outer sandbox; there is no nested coordinator session or separate per-child OS sandbox. After the delegated child returns, the coordinator validates the commit/tests/handoff, runs workspace complete and integrate from its unchanged checkout, and updates status/links/handoff. Inline (independent) nodes have no child result and skip this entire lifecycle. When runner context is supplied for restart or resume, the runtime recovers only the runner-managed coordinator session and the coordinator inspects or reconciles the exact managed PROCESS lease from the unchanged session checkout before complete and integrate; otherwise standalone execution remains in the unchanged integration checkout. The coordinator invokes owner-token cleanup only after an explicit integration or retention decision. Runner-managed session retention cleanup consults git worktree list and fails closed by retaining the session checkout when runtime metadata is dirty or uncertain, a linked worktree exists, or git worktree inspection fails; it does not own, persist, or retry child PROCESS cleanup. workflow workspace cleanup is destructive and does not decide or enforce integration/retention eligibility for its caller.
9. Link every worker and review PROCESS to the PR with issue-spec pr link-process.
   Use issue-spec comment transition for status/handoff/PR/related-link changes. Pass an observed --expected-version on conditional backends; for non-CAS compatibility, explicitly pass both --allow-nonatomic and --expected-digest and verify the result says atomic: false. For a dependency-ordered batch, use issue-spec workflow reconcile --plan <plan.json> --checkpoint <checkpoint.json> --json and resume the same digest/checkpoint after pending failures.
10. Add proposal/design/implement closing links to the implementation PR body, and make this the final write to that PR body:

       issue-spec pr link-issues --repo {{repo}} --pr <implementation-pr> --proposal <proposal-issue> --design <design-issue> --implement <implement-issue> --json

   The managed closure block lives in the mutable PR body, so any later full-body edit silently erases it and GitHub then closes only the issues still named in the body (the observed symptom is the proposal and design issues staying open while only the implement issue closes). Run link-issues last, or if a later body edit is unavoidable preserve the managed closure block verbatim or re-run issue-spec pr link-issues afterward to restore it. Before merge, gate on the block with issue-spec pr verify-closure --repo {{repo}} --pr <implementation-pr> --proposal <proposal-issue> --design <design-issue> --implement <implement-issue> (exit 0 = block complete/valid; exit 1 = block missing/incomplete/tampered, so restore it before merging).
11. Add final PR rationale only after review/fix convergence and only for change-bearing PROCESS nodes. Once all P0/P1 findings are resolved, each code author adds rationale on the key code blocks it owns with issue-spec pr rationale, linked to a SPEC comment: for delegated code, the coordinator dispatches the owning worker with that worker's --agent and --agent-session; for inline code, the coordinator records rationale under its own author identity without fabricating a worker. review requires a linked done REVIEW or resolved finding; verification requires a linked done VERIFY or required passing check with test evidence; orchestration requires a non-empty coordination handoff; external requires consumed exact-revision provider evidence. Every class still requires TASK, PR, and active SPEC traceability.
12. Mark PROCESS comments done only after implementation/review work and focused verification evidence exist.

## Coordinator DAG Execution

1. Derive the DAG from TASK ### Execution Planning metadata; default to serial PROCESS chains under one parent TASK.
2. Build the ready set from PROCESS nodes whose dependencies are done. Declaring a PROCESS node is a plan artifact only; its assigned worker or review sub-agent is spawned lazily when that node enters the ready set, never pre-created to satisfy the plan. Declaring a node MUST NOT instantiate an idle agent that has no work yet -- a review node's agent is spawned only after the code under review exists.
3. Coding MAY be inline or delegated. Delegated: workspace_management: managed, run prepare -> child -> complete -> integrate. Inline: workspace_management: independent, the coordinator implements/tests/commits in its own integration checkout and skips prepare/child/complete/integrate. Both record change-bearing rationale, and both schedule an independent review PROCESS once the code is reviewable. Delegation is a recommended technique for context isolation, not a requirement.
4. When serial coding nodes are delegated, run each in its own worker connected by a bounded ### Handoff, and seed a successor worker with the parent TASK context plus the predecessor ### Handoff, never the coordinator's accumulated context. Record ### Handoff evidence on each completed serial node before starting its successor.
5. Parallelism is a separate, gated optimization, not the trigger for delegation: run independent worker nodes concurrently only when their write ownership is provably disjoint. Give each worker an assigned id to pass via --agent-session.
6. For each delegated ready PROCESS, prepare its workspace from the unchanged coordinator checkout and dispatch a current runtime native child with the exact worktree cwd/branch/ownership; never dispatch another coordinator session. For an inline ready PROCESS, skip prepare/child/complete/integrate and author the node directly in the coordinator's integration checkout.
7. Every active SPEC with a valid change-bearing carrier MUST be reviewed by a review agent that is not the author of the code under review, whether authored inline or by a worker; spawn or assign it, and run multiple review agents in parallel only when their review scopes are disjoint. A single review PROCESS MAY cover a SPEC that several implementation PROCESS nodes contribute to. The reviewing agent's --agent identity MUST name a real sub-agent actually spawned to perform the review; the coordinator MUST NOT self-author review findings for code it or a worker wrote, nor fabricate an agent name to bypass this. Route findings to the owner PROCESS or a dedicated repair PROCESS under the same serial/parallel gating.
8. For each delegated (managed) output, validate the child result commit/tests/handoff, then run complete and integrate by dependency order and update PROCESS evidence (including ### Handoff for serial predecessors) before marking done. Inline (independent) nodes skip the child-output lifecycle and retain their own per-PROCESS handoff boundary.
9. For delegated work, the coordinator owns scheduling, workspace lifecycle, gate evaluation, status synchronization, unresolved-blocker routing, and final rationale dispatch only, and stays lean by consuming bounded worker outputs and issue-spec read results rather than full issue/PR bodies or full diffs. For inline work, it additionally owns the current node's bounded implementation, test, commit, handoff, and author-rationale context. It stays in the integration checkout/session clone and does not author review findings, worker fix replies, review resolutions, or rationale on another agent's behalf unless explicitly assigned as that worker or review owner.
10. Gate merge on the closure block: keep issue-spec pr link-issues the final write to the implementation PR body, because a later full-body edit silently erases the managed closure block and GitHub then closes only the issues still named in the body (proposal and design stay open, only implement closes). Before merge run issue-spec pr verify-closure --repo {{repo}} --pr <implementation-pr> --proposal <proposal-issue> --design <design-issue> --implement <implement-issue>; on exit 1 re-run pr link-issues to restore the block.
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

1. The review agent runs issue-spec review sync --repo {{repo}} --pr <number> --implement <issue> --id REVIEW-<n> --agent <review-agent> --agent-session <id> --json to capture current rationale comments, findings, checks, per-PROCESS execution class and evidence diagnostics. The evidence gate reads the REVIEW writer's --agent as the reviewing identity for the independence check, so the reviewing agent (never the coordinator on its behalf) MUST author the REVIEW under its own --agent/--agent-session; a REVIEW written without --agent defaults to the coordinator identity and can be falsely flagged as a self-review. review sync owns the established "## Review Sync Summary" REVIEW body shape; do not hand-edit it. For separate manual review evidence, generate a REVIEW body with issue-spec comment generate --type REVIEW --input-file review.json and upsert it under the same review-agent identity.
2. For every active SPEC that has a valid change-bearing carrier, you MUST spawn or assign a dedicated review agent as a review PROCESS owner, and each review MUST be performed by a different agent than the one that authored the code under review. This holds for changes of every size, whether the code was authored inline by the coordinator or by a worker. The reviewing agent's --agent identity MUST name a real sub-agent actually spawned to perform the review; MUST NOT fabricate or reuse a name to bypass this. A single review PROCESS MAY cover a SPEC that several implementation PROCESS nodes contribute to. Multiple review agents can run in parallel when their review scopes are independent. A review PROCESS is complete only with a linked done REVIEW or resolved finding covering an active SPEC; it still needs TASK and PR links.
3. Give each review agent a concrete scope and expected output: actionable findings only, severity, file/line, linked SPEC, owner PROCESS, and suggested fix.
4. Each review agent authors its own actionable PR line findings directly with issue-spec review finding, using its own --agent identity and assigned --agent-session. Use P0/P1 for blockers and P2 for non-blocking follow-up. The coordinator does not create findings on a review agent's behalf.
5. Assign every finding to a PROCESS owner. If no findings are found, record that result in the synced REVIEW, then link that REVIEW bidirectionally to both its review PROCESS and every covered active SPEC before completing the PROCESS. Use issue-spec link --repo {{repo}} --from REVIEW-<n> --from-issue <implement-issue> --to PROCESS-<n> --to-issue <implement-issue>, then issue-spec link --repo {{repo}} --from REVIEW-<n> --from-issue <implement-issue> --to SPEC-<n> --to-issue <proposal-issue> for each covered SPEC. Run these commands after the final review sync so sync cannot replace the evidence links.
6. The worker that owns the affected code fixes it and replies on the original finding thread with issue-spec review reply using its own --agent and --agent-session. The review agent that opened the finding then re-checks the diff and owns the resolved reply or GitHub conversation resolution; a worker reply alone does not resolve a finding.
7. Re-run review sync. P0/P1 findings must be resolved by review-agent evidence before final verify/archive.

## Review DAG Policy

1. Every active SPEC that has a valid change-bearing carrier MUST be covered by at least one independent review PROCESS node before final verify, owned by an agent that did not author the code under review. This applies to changes of every size and whether the code was authored inline or by a worker; a single review PROCESS MAY cover a SPEC that several implementation PROCESS nodes contribute to. Final verify fails closed (process.review.required) when a covered SPEC has no such review PROCESS.
2. Review parallelism is gated, not default: run multiple review agents in parallel only when their review scopes are independent, for example CLI/API behavior, workflow docs, tests, compatibility, or security-sensitive surfaces.
3. Each review agent authors its own findings with issue-spec review finding under its own agent identity; the coordinator schedules review agents and routes blockers but does not author findings on their behalf.
4. Route findings to the owner PROCESS or a dedicated repair PROCESS. Repair PROCESS nodes are DAG nodes too: they follow the same serial/parallel gating as implementation nodes and record ### Handoff evidence when part of a serial chain.
5. P0/P1 findings block final verify until the owning worker fixes them and replies on the thread, and the review agent that opened the finding re-checks and records the resolution or resolves the GitHub conversation.
6. If a review agent finds no issues, use the final synced REVIEW as the evidence carrier. Run the two issue-spec link flows above, then confirm with issue-spec comment list --repo {{repo}} --issue <implement-issue> --type REVIEW --json that Related Comments contains the review PROCESS URL and each covered active SPEC URL before marking the review PROCESS done. A no-finding statement without both link classes is incomplete review evidence.
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

1. Run issue-spec status --repo {{repo}} --proposal <issue> --design <issue> --implement <issue> --gate final --json and resolve every locally knowable blocker. This is a forecast, not a substitute for authoritative final verify.
2. Run focused project tests and record evidence in VERIFY comments. Generate VERIFY bodies with issue-spec comment generate --type VERIFY --input-file verify.json instead of hand-writing Markdown, and reference the covered SPEC IDs so final verify can confirm coverage. A verification PROCESS needs a linked done VERIFY or required passing check with test evidence; inspect every per-PROCESS evidence report rather than requiring rationale from non-change-bearing classes.
3. Run issue-spec verify-links --repo {{repo}} --proposal <issue> --design <issue> --implement <issue> --json.
4. Render a durable spec draft:

       issue-spec archive durable-spec --repo {{repo}} --proposal <issue> --capability <capability> --output /tmp/<capability>-spec.md --json

5. Run final verify:

       issue-spec verify --repo {{repo}} --proposal <issue> --design <issue> --implement <issue> --pr <pr> --durable-spec /tmp/<capability>-spec.md --json

6. Final verify must fail if blocking questions, missing links, missing class-specific PROCESS evidence, open P0/P1 findings, failed or pending PR checks, or durable spec omissions exist. Only change-bearing PROCESS nodes require matching inline rationale; review, verification, orchestration, and external nodes use their proportional evidence carriers.
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

1. Confirm the implementation PR is merged and had issue-spec closing links before merge.
2. Choose the --capability value as a stable long-lived capability or domain directory, not the original change/proposal name. Treat it as an umbrella capability: a single spec that accumulates related current and future changes. Prefer names that can host related future durable specs, for example workflow-identity-and-sessions instead of agent-session-source-of-truth.
3. Inspect existing durable specs before creating or finalizing the archive PR. Read ` + "`issue-spec/specs/<capability>/spec.md`" + ` when it exists, and scan related ` + "`issue-spec/specs/*/spec.md`" + ` files when the new behavior may belong with an existing capability. If ` + "`openspec/specs/<capability>/spec.md`" + ` already exists, issue-spec may select that legacy path for update and report the compatibility choice. Decide whether to update, merge, or reorganize existing durable requirements instead of adding a duplicate or narrowly named spec.
4. Create the durable spec PR and idempotently close any still-open PR-associated active issues:

       issue-spec archive durable-spec --repo {{repo}} --proposal <proposal-issue> --design <design-issue> --implement <implement-issue> --pr <implementation-pr> --capability <capability> --create-pr --branch issue-spec/durable-spec-<capability> --close-issues --json

5. Review and edit the generated durable spec draft before handoff or merge. When re-archiving into an existing umbrella capability, archive now accumulates the new proposal's requirements into the existing spec by requirement title (newest wins) rather than overwriting prior requirements, so verify the merged result. Reconcile it with any existing related durable specs, regroup related source SPEC content into durable capability modules instead of preserving one-to-one source SPEC sections, and keep Source SPEC links for traceability.
6. Keep only long-lived behavior. Do not copy process records, review findings, or verification logs into durable specs.
7. Keep the closed proposal/design/implement issues as audit history.
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
