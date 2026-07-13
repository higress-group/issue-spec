---
name: "Issue Spec: Apply"
description: "Implement PROCESS comments for an issue-spec change and keep PR traceability synchronized."
category: "Workflow"
tags: ["workflow", "issue-spec"]
---

# Issue Spec Apply

Use when the user asks for /issue-spec:apply, issue-spec apply, or implementing PROCESS/TASK scopes from an issue-spec change.

## Prerequisite

This skill assumes `issue-spec-propose` has been completed: proposal, design,
implement issues exist with SPEC, TASK typed comments and SPEC↔TASK bidirectional
links. Run `issue-spec verify-links` after propose as a smoke check.

## Steps

1. Read proposal/design/implement issue context and list typed comments with issue-spec comment list --json. Run issue-spec status --repo higress-group/issue-spec --proposal <n> --design <n> --implement <n> --gate implement --json before dispatch and use --gate final as the pre-verify forecast.
2. Confirm issue-spec auth status --json includes the expected GitHub backend. Local gh-authenticated sessions can use the native gh backend; keep ISSUE_SPEC_TOKEN="$(gh auth token)" only as an older-version or forced-rest compatibility path. Before workspace or worker allocation, run issue-spec doctor agent --repo higress-group/issue-spec --operation <required-operation> --json; strict delegated work requires an operator-owned short-lived issuer and never silently downgrades to legacy_long_lived host credentials.
3. Plan the PROCESS DAG before dispatch, then dispatch each non-trivial coding node to a worker sub-agent; do not implement non-trivial code inline in the coordinator context. Delegation exists to keep the coordinator context bounded and avoid mid-task compaction; the coordinator plans and integrates while workers hold the bulk coding context. Context-isolation delegation is the default for every non-trivial node, serial or parallel. Escape hatch: trivial single-file or pure-orchestration work MAY be inlined. Read each active TASK's ### Execution Planning metadata and derive PROCESS nodes from it. Render PROCESS bodies with issue-spec comment generate --type PROCESS --input-file process.json (fields include parent_task, execution_class, owner, dependencies, write_ownership, handoff) instead of hand-writing Markdown; comment upsert --type PROCESS rejects a PROCESS without a ### Parent TASK. Select change-bearing, review, verification, orchestration, or external according to responsibility; a missing legacy class remains conservatively change-bearing.
   Keep Agent as the logical role. Pass assigned subagent/session ids with --agent-session; Codex CODEX_THREAD_ID remains the artifact writer session source of truth when present.
4. Default to serial PROCESS chains under one parent TASK. Serial chains still delegate: run each node in its own worker connected by a bounded ### Handoff, and seed a successor worker with the parent TASK context plus the predecessor ### Handoff, never the coordinator's accumulated context. Parallelism is a separate, gated optimization, not the trigger for delegation: split into parallel worker PROCESS nodes only when file/module write ownership is provably disjoint.
5. Add dedicated review PROCESS nodes for non-trivial changes. Review PROCESS nodes should own review scopes such as CLI/API behavior, workflow docs, tests, compatibility, or security-sensitive surfaces.
6. Link each PROCESS to its TASK comments with issue-spec link.
7. Keep the coordinator in its integration checkout. In runner mode, the single ACPX coordinator stays in the public session clone for /new, /resume, cancellation, and restart reconciliation; runner command intake never accepts a PROCESS selector. Never launch nested ACPX or rebind coordinator cwd/sandbox to a PROCESS worktree. The coordinator owns every PROCESS workspace lifecycle operation: select the exact PROCESS from the typed DAG and run issue-spec workflow workspace prepare, inspect, complete, integrate, reconcile, and cleanup with stable --repo, --issue, --process, roots, and owner token. Runner-provided ISSUE_SPEC_PROCESS_INTEGRATION_ROOT and ISSUE_SPEC_PROCESS_WORKSPACE_ROOT are trusted defaults; standalone use passes explicit --integration-root and --workspace-root. Review and verification use detached immutable workflow snapshots and fail closed when dirty, but issue-spec does not claim per-child OS immutability. external uses mode none and requires consumed provider-neutral exact-revision evidence for completion and the final gate.
8. After prepare, use the current agent runtime's native child/subagent facility, not ACPX, to run the worker. Give it the exact worktree path as cwd plus branch, write ownership, PROCESS id, parent TASK, and predecessor handoff. The worker owns one result commit, focused tests, and a bounded handoff. Native children share the runner coordinator's outer sandbox; there is no nested ACPX session or separate per-child OS sandbox. After the child returns, the coordinator validates the commit/tests/handoff, runs workspace complete and integrate from its unchanged checkout, and updates status/links/handoff. After runner restart or resume, the top-level runner recovers only the ACPX/session job; the coordinator inspects or reconciles the exact PROCESS lease from the unchanged session clone before complete and integrate. The coordinator invokes owner-token cleanup only after an explicit integration or retention decision. Top-level runner session-clone retention cleanup consults git worktree list and fails closed by retaining the clone when runner metadata is dirty or uncertain, a linked worktree exists, or git worktree inspection fails; it does not own, persist, or retry child PROCESS cleanup. workflow workspace cleanup is destructive and does not decide or enforce integration/retention eligibility for its caller.
9. Link every worker and review PROCESS to the PR with issue-spec pr link-process.
   Use issue-spec comment transition for status/handoff/PR/related-link changes. Pass an observed --expected-version on conditional backends; for non-CAS compatibility, explicitly pass both --allow-nonatomic and --expected-digest and verify the result says atomic: false. For a dependency-ordered batch, use issue-spec workflow reconcile --plan <plan.json> --checkpoint <checkpoint.json> --json and resume the same digest/checkpoint after pending failures.
10. Add proposal/design/implement closing links to the implementation PR body, and make this the final write to that PR body:

       issue-spec pr link-issues --repo higress-group/issue-spec --pr <implementation-pr> --proposal <proposal-issue> --design <design-issue> --implement <implement-issue> --json

   The managed closure block lives in the mutable PR body, so any later full-body edit silently erases it and GitHub then closes only the issues still named in the body (the observed symptom is the proposal and design issues staying open while only the implement issue closes). Run link-issues last, or if a later body edit is unavoidable preserve the managed closure block verbatim or re-run issue-spec pr link-issues afterward to restore it. Before merge, gate on the block with issue-spec pr verify-closure --repo higress-group/issue-spec --pr <implementation-pr> --proposal <proposal-issue> --design <design-issue> --implement <implement-issue> (exit 0 = block complete/valid; exit 1 = block missing/incomplete/tampered, so restore it before merging).
11. Add final PR rationale only after review/fix convergence and only for change-bearing PROCESS nodes. Once all P0/P1 findings are resolved, the coordinator dispatches each owning worker to add rationale on the key code blocks that worker owns with issue-spec pr rationale (worker --agent and --agent-session), each linked to a SPEC comment. review requires a linked done REVIEW or resolved finding; verification requires a linked done VERIFY or required passing check with test evidence; orchestration requires a non-empty coordination handoff; external requires consumed exact-revision provider evidence. Every class still requires TASK, PR, and active SPEC traceability.
12. Mark PROCESS comments done only after implementation/review work and focused verification evidence exist.

## Coordinator DAG Execution

1. Derive the DAG from TASK ### Execution Planning metadata; default to serial PROCESS chains under one parent TASK.
2. Build the ready set from PROCESS nodes whose dependencies are done.
3. Delegate by default for context isolation: delegation exists to keep the coordinator context bounded and avoid mid-task compaction, so dispatch each non-trivial coding node to its own worker sub-agent rather than implementing it inline. Escape hatch: trivial single-file or pure-orchestration work MAY be inlined.
4. Serial chains still delegate: run each serial node in its own worker connected by a bounded ### Handoff, and seed a successor worker with the parent TASK context plus the predecessor ### Handoff, never the coordinator's accumulated context. Record ### Handoff evidence on each completed serial node before starting its successor.
5. Parallelism is a separate, gated optimization, not the trigger for delegation: run independent worker nodes concurrently only when their write ownership is provably disjoint. Give each worker an assigned id to pass via --agent-session.
6. For each ready PROCESS, prepare its workspace from the unchanged coordinator checkout and dispatch a runtime-native child with the exact worktree cwd/branch/ownership; never dispatch another ACPX session.
7. Spawn or assign review agents for non-trivial PRs; run them in parallel only when their review scopes are disjoint. Route findings to the owner PROCESS or a dedicated repair PROCESS under the same serial/parallel gating.
8. Validate child result commit/tests/handoff, then run complete and integrate by dependency order and update PROCESS evidence (including ### Handoff for serial predecessors) before marking done.
9. The coordinator owns scheduling, workspace lifecycle, gate evaluation, status synchronization, unresolved-blocker routing, and final rationale dispatch only, and stays lean by consuming bounded worker outputs and issue-spec read results rather than full issue/PR bodies or full diffs. It stays in the integration checkout/session clone and does not author review findings, worker fix replies, review resolutions, or rationale on another agent's behalf unless explicitly assigned as that worker or review owner.
10. Gate merge on the closure block: keep issue-spec pr link-issues the final write to the implementation PR body, because a later full-body edit silently erases the managed closure block and GitHub then closes only the issues still named in the body (proposal and design stay open, only implement closes). Before merge run issue-spec pr verify-closure --repo higress-group/issue-spec --pr <implementation-pr> --proposal <proposal-issue> --design <design-issue> --implement <implement-issue>; on exit 1 re-run pr link-issues to restore the block.

## Project Workflow

- Workflow Source: `builtin`
- Workflow Schema: `issue-spec`
- Workflow Diagnostics:

Project workflow templates are declarative only. Active proposal, design, implement, SPEC, TASK, PROCESS, QUESTION, REVIEW, and VERIFY artifacts remain in GitHub issue-native storage; durable specs are repository files created during archive.
