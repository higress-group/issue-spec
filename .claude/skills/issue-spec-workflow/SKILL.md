---
name: issue-spec-workflow
description: Use issue-spec to run an issue-native OpenSpec-style workflow with GitHub issues, typed comments, PR review comments, final verification, and durable spec archive PRs.
license: MIT
compatibility: Requires issue-spec CLI.
metadata:
  author: issue-spec
  version: "1.0"
  generatedBy: "issue-spec"
---

# Issue Spec Workflow

Use this skill for issue-native OpenSpec work. Active change artifacts live in GitHub issues and issue comments; durable specs are repository files created after implementation merge.

## Start

1. Run issue-spec auth status --json and confirm the active auth source and GitHub backend.
2. Run issue-spec status --repo higress-group/issue-spec --proposal <issue> --design <issue> --implement <issue> --gate <proposal|design|implement|final|archive> --json when issues already exist. Treat status as a point-in-time forecast; final verify re-observes authoritative remote facts.
3. For new work, create proposal, design, and implement issues with issue-spec issue create and pass --body-file with concrete markdown content.
4. When an issue body changes, update it in place with issue-spec issue update --body-file and include --summary for the human-readable audit trail.
5. Store requirements, tasks, process ownership, review, and verify evidence as typed comments.

## Project Workflow Config

- Run issue-spec workflow validate --repo higress-group/issue-spec --json before relying on project templates or legacy OpenSpec workflow definitions.
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
- Every PROCESS must declare execution_class when generated: change-bearing, review, verification, orchestration, or external. Legacy missing classes project conservatively to change-bearing; unknown classes block. All classes require TASK, PR, and active SPEC traceability. The class carrier is respectively matching path/line rationale, done REVIEW or resolved finding, done VERIFY or required passing check with test evidence, non-empty coordination handoff, or consumed exact-revision provider evidence.
- The coordinator selects the exact PROCESS from the typed DAG, never from runner command grammar or prompt prose. Runner commands remain /new <instruction> and /resume <public-session-id> <instruction>; --process is not accepted by runner command intake. The runner launches exactly one ACPX coordinator and keeps its cwd and primary sandbox workspace at the public session clone for new, resume, cancellation, and restart reconciliation. Never start a nested ACPX worker or rebind the coordinator to a PROCESS worktree.
- The coordinator owns every PROCESS workspace lifecycle operation and runs issue-spec workflow workspace prepare, inspect, complete, integrate, reconcile, and cleanup with the same --repo, --issue, --process, roots, and owner token. In runner mode, ISSUE_SPEC_PROCESS_INTEGRATION_ROOT and ISSUE_SPEC_PROCESS_WORKSPACE_ROOT provide trusted session-local defaults; standalone coordinators pass explicit --integration-root and --workspace-root. change-bearing uses a writable owned branch; review and verification use detached immutable workflow snapshots and fail closed when dirty; orchestration uses bookkeeping with no checkout. external uses mode none; completing it and passing the final gate require consumed provider-neutral exact-revision evidence.
- After prepare, delegate through the current agent runtime's native child/subagent facility and pass the exact worktree path, branch, write ownership, PROCESS id, and bounded TASK/handoff context. The child runs with that worktree as its cwd, authors its own result commit, runs focused tests, and returns commit/test/handoff evidence. A native child is not an ACPX session: issue-spec does not launch nested ACPX or claim a separate per-child OS sandbox. In runner mode children share the coordinator's outer sandbox, which exposes the session clone and only that session's PROCESS pool; unsafe-no-sandbox has no filesystem isolation.
- The coordinator validates the child handoff, then runs complete and integrate and updates PROCESS status/handoff/PR/SPEC links. After runner restart or resume, the top-level runner recovers only the ACPX/session job; from the unchanged session clone the coordinator inspects or reconciles the exact PROCESS lease, then completes and integrates it. The coordinator invokes owner-token cleanup only after an explicit integration or retention decision. Top-level runner session-clone retention cleanup consults git worktree list and fails closed by retaining the clone when runner metadata is dirty or uncertain, a linked worktree exists, or git worktree inspection fails; it does not own, persist, or retry child PROCESS cleanup. workflow workspace cleanup is always an explicit owner-token-authorized destructive operation that can remove unintegrated work and does not decide or enforce integration/retention eligibility for its caller.
- Move an existing typed artifact with issue-spec comment transition --id <id> --to <status> instead of regenerating its body. Conditional backends use --expected-version; a backend without CAS fails closed unless --allow-nonatomic is explicit together with --expected-digest, and the result must report atomic: false. Use --handoff-file, --pr, and --related for the only declared mutations.
- Apply multi-artifact desired state with issue-spec workflow reconcile --plan <plan.json> --checkpoint <checkpoint.json> --json. Plans are versioned and dependency ordered; keep the checkpoint and rerun the same plan after pending transport/rate-limit failures so remote re-observation can repair lost responses and partial backlinks.
- Before allocating a delegated worker, run issue-spec doctor agent --repo higress-group/issue-spec --operation <operation> --json for every required provider-neutral operation (issue.read, artifact.write, pr.read, pr.review.write, checks.read, git.clone, git.push, or external.change.comment). Strict runner work requires an operator-owned short-lived issuer; legacy_long_lived mirrored gh credentials are compatibility-only and never satisfy strict operation policy.
- Link every PROCESS to the implementation PR with issue-spec pr link-process.
- Before implementation PR merge, add GitHub closing links to the implementation PR body with issue-spec pr link-issues so GitHub closes the proposal/design/implement issues when the PR merges. issue-spec pr link-issues MUST be the final write to the implementation PR body: the managed closure block lives in the mutable body, so any later full-body edit silently erases it and GitHub then closes only the issues still named in the body (the observed symptom is the proposal and design issues staying open while only the implement issue closes). Any later body edit MUST preserve the managed closure block verbatim, or re-run issue-spec pr link-issues afterward to restore it.
- Gate merge on the closure block: before merging the implementation PR, run issue-spec pr verify-closure --repo owner/repo --pr N --proposal N --design N --implement N (exit 0 = block complete/valid; exit 1 = block missing/incomplete/tampered).
- Treat Agent as the logical role or workflow-assigned label. Treat Agent Session ID and Agent Session Source as artifact writer provenance, not runner resume metadata.
- When dispatching subagents, assign each subagent an explicit subagent/session id and tell it to pass that value with --agent-session to issue-spec writer commands. In Codex, CODEX_THREAD_ID may override that value as the resolved artifact writer session id; outside Codex, --agent-session is the explicit fallback and missing session metadata is non-strict by default.
- In runner mode, runner.public_session_id is the public /resume handle. Coordinator-authored proposal, design, implement, handoff, and update issue bodies or comments should include runner.public_session_id and /resume <public-session-id> <answer or next instruction> when available. Do not present Agent Session ID, CODEX_THREAD_ID, acpx record ids, or provider session ids as /resume handles.
- For non-trivial changes, include review PROCESS nodes in the DAG; review agents are scheduled like worker agents and can run in parallel when their review scopes are independent.
- Small changes may stay coordinator-only, but record the serial execution decision in the implement or VERIFY evidence.
- After review/fix convergence, add issue-spec pr rationale only for change-bearing PROCESS nodes; other classes satisfy their class-specific carrier and do not invent arbitrary code-line rationale.
- Use issue-spec review finding for PR line findings and issue-spec review reply to close the original thread.
- Run issue-spec review sync and issue-spec verify before declaring ready.
- After the implementation PR merges, create the separate durable spec PR with issue-spec archive durable-spec --create-pr --close-issues, passing the proposal, design, implement, and implementation PR numbers so archive also idempotently closes any still-open active issues. Use an abstract long-lived --capability directory as an umbrella capability that accumulates related current and future changes, inspect existing related durable specs, and regroup the generated draft by stable capability modules before merge. Archive now accumulates new requirements into an existing capability spec by requirement title (newest wins), so re-archiving into an umbrella capability preserves prior requirements instead of overwriting them.

## Coordinator DAG Execution

1. Plan the PROCESS DAG before dispatch: read every active TASK's ### Execution Planning metadata (coupling class, recommended execution mode, owned areas) and derive PROCESS nodes from it.
2. Delegate by default for context isolation: delegation exists to keep the coordinator context bounded and avoid mid-task compaction, so dispatch each non-trivial coding node to its own worker sub-agent, serial or parallel, and do not implement non-trivial code inline. Escape hatch: trivial single-file or pure-orchestration work MAY be inlined.
3. Treat PROCESS comments as DAG nodes with explicit owner, parent TASK, dependencies, write or review scope, PR link, and evidence.
4. Select ready PROCESS nodes whose dependencies are done and dispatch each to its assigned worker; include each worker's assigned subagent/session id and require it to pass that id with --agent-session on supported issue-spec writer commands. Parallelism is a separate, gated optimization, not the trigger for delegation: run independent nodes concurrently only when their write ownership is provably disjoint.
5. Default to serial PROCESS chains under one parent TASK; serial chains still delegate, running each node in its own worker connected by a bounded ### Handoff. Each completed serial PROCESS records ### Handoff evidence (the contract/state its successor consumes) before the next node starts, seeding the successor worker with the parent TASK context plus the predecessor ### Handoff rather than the coordinator's accumulated context; record a reason when a handoff is unnecessary.
6. Prepare each ready PROCESS with the workspace CLI while the coordinator remains in its integration checkout/session clone, then pass the returned exact worktree path, branch, ownership, and PROCESS id to a runtime-native child. Do not create an ACPX child session or move the coordinator cwd.
7. Dispatch review PROCESS nodes for non-trivial PRs after PR rationale exists; run review nodes in parallel only when their review scopes are independent. Route P0/P1 findings to the owner PROCESS or a dedicated repair PROCESS that follows the same serial/parallel gating.
8. Validate each child result commit and focused-test handoff, then complete and integrate outputs by dependency order from the coordinator checkout.
9. Mark PROCESS nodes done only after implementation or review evidence and, for serial predecessors, ### Handoff evidence are recorded and blocking findings are resolved.
10. Gate merge on the closure block: issue-spec pr link-issues must be the final write to the implementation PR body, since any later full-body edit silently erases the managed closure block and GitHub then closes only the issues still named in the body (proposal and design stay open, only implement closes). Run issue-spec pr verify-closure --repo higress-group/issue-spec --pr <implementation-pr> --proposal <proposal-issue> --design <design-issue> --implement <implement-issue> before merge; exit 1 means restore the block by re-running pr link-issues.

## Cross-Skill Boundary

The issue-spec workflow is composed of cooperating skills. Each owns a slice
of the link matrix; a single skill never covers the full graph.

Link matrix (each direction has a designated owner; rows marked ✓ are gated by `verify-links`):
- ✓ SPEC ↔ TASK        (issue-spec-propose, step 7)
- ✓ TASK ↔ PROCESS     (issue-spec-apply, step 6)
-   PROCESS ↔ SPEC     (issue-spec-apply, step 10, via pr rationale and review finding)
-   PROCESS ↔ PR       (issue-spec-apply, step 8, via pr link-process)

`verify-links` covers SPEC↔TASK and TASK↔PROCESS only; the other two directions
are created by their owner steps but not auto-checked.

## Project Workflow

- Workflow Source: `builtin`
- Workflow Schema: `issue-spec`
- Workflow Diagnostics:

Project workflow templates are declarative only. Active proposal, design, implement, SPEC, TASK, PROCESS, QUESTION, REVIEW, and VERIFY artifacts remain in GitHub issue-native storage; durable specs are repository files created during archive.

