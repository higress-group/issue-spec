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

1. Read proposal/design/implement issue context and list typed comments with issue-spec comment list --json.
2. Confirm issue-spec auth status --json includes the expected GitHub backend. Local gh-authenticated sessions can use the native gh backend; keep ISSUE_SPEC_TOKEN="$(gh auth token)" only as an older-version or forced-rest compatibility path.
3. Plan the PROCESS DAG before dispatch, then dispatch each non-trivial coding node to a worker sub-agent; do not implement non-trivial code inline in the coordinator context. Delegation exists to keep the coordinator context bounded and avoid mid-task compaction; the coordinator plans and integrates while workers hold the bulk coding context. Context-isolation delegation is the default for every non-trivial node, serial or parallel. Escape hatch: trivial single-file or pure-orchestration work MAY be inlined. Read each active TASK's ### Execution Planning metadata and derive PROCESS nodes from it. Render PROCESS bodies with issue-spec comment generate --type PROCESS --input-file process.json (fields include parent_task, owner, dependencies, write_ownership, handoff) instead of hand-writing Markdown; comment upsert --type PROCESS rejects a PROCESS without a ### Parent TASK.
   Keep Agent as the logical role. Pass assigned subagent/session ids with --agent-session; Codex CODEX_THREAD_ID remains the artifact writer session source of truth when present.
4. Default to serial PROCESS chains under one parent TASK. Serial chains still delegate: run each node in its own worker connected by a bounded ### Handoff, and seed a successor worker with the parent TASK context plus the predecessor ### Handoff, never the coordinator's accumulated context. Parallelism is a separate, gated optimization, not the trigger for delegation: split into parallel worker PROCESS nodes only when file/module write ownership is provably disjoint.
5. Add dedicated review PROCESS nodes for non-trivial changes. Review PROCESS nodes should own review scopes such as CLI/API behavior, workflow docs, tests, compatibility, or security-sensitive surfaces.
6. Link each PROCESS to its TASK comments with issue-spec link.
7. Implement the code changes for one PROCESS scope at a time, or integrate completed worker outputs by dependency order. The worker that owns a code scope owns its own commits; the coordinator does not author code artifacts on a worker's behalf unless it is the assigned worker.
8. Link every worker and review PROCESS to the PR with issue-spec pr link-process.
9. Add proposal/design/implement closing links to the implementation PR body, and make this the final write to that PR body:

       issue-spec pr link-issues --repo higress-group/issue-spec --pr <implementation-pr> --proposal <proposal-issue> --design <design-issue> --implement <implement-issue> --json

   The managed closure block lives in the mutable PR body, so any later full-body edit silently erases it and GitHub then closes only the issues still named in the body (the observed symptom is the proposal and design issues staying open while only the implement issue closes). Run link-issues last, or if a later body edit is unavoidable preserve the managed closure block verbatim or re-run issue-spec pr link-issues afterward to restore it. Before merge, gate on the block with issue-spec pr verify-closure --repo higress-group/issue-spec --pr <implementation-pr> --proposal <proposal-issue> --design <design-issue> --implement <implement-issue> (exit 0 = block complete/valid; exit 1 = block missing/incomplete/tampered, so restore it before merging).
10. Add final PR rationale only after review/fix convergence, not as pre-review readiness evidence. Once all P0/P1 findings are resolved, the coordinator dispatches each owning worker to add rationale on the key code blocks that worker owns with issue-spec pr rationale (worker --agent and --agent-session), each linked to a SPEC comment.
11. Mark PROCESS comments done only after implementation/review work and focused verification evidence exist.

## Coordinator DAG Execution

1. Derive the DAG from TASK ### Execution Planning metadata; default to serial PROCESS chains under one parent TASK.
2. Build the ready set from PROCESS nodes whose dependencies are done.
3. Delegate by default for context isolation: delegation exists to keep the coordinator context bounded and avoid mid-task compaction, so dispatch each non-trivial coding node to its own worker sub-agent rather than implementing it inline. Escape hatch: trivial single-file or pure-orchestration work MAY be inlined.
4. Serial chains still delegate: run each serial node in its own worker connected by a bounded ### Handoff, and seed a successor worker with the parent TASK context plus the predecessor ### Handoff, never the coordinator's accumulated context. Record ### Handoff evidence on each completed serial node before starting its successor.
5. Parallelism is a separate, gated optimization, not the trigger for delegation: run independent worker nodes concurrently only when their write ownership is provably disjoint. Give each worker an assigned id to pass via --agent-session.
6. Spawn or assign review agents for non-trivial PRs; run them in parallel only when their review scopes are disjoint. Route findings to the owner PROCESS or a dedicated repair PROCESS under the same serial/parallel gating.
7. Integrate completed outputs by dependency order and update PROCESS evidence (including ### Handoff for serial predecessors) before marking done.
8. The coordinator owns scheduling, gate evaluation, status synchronization, unresolved-blocker routing, and final rationale dispatch only, and stays lean by consuming bounded worker outputs and issue-spec read results rather than full issue/PR bodies or full diffs. It does not author review findings, worker fix replies, review resolutions, or rationale on another agent's behalf unless explicitly assigned as that worker or review owner.
9. Gate merge on the closure block: keep issue-spec pr link-issues the final write to the implementation PR body, because a later full-body edit silently erases the managed closure block and GitHub then closes only the issues still named in the body (proposal and design stay open, only implement closes). Before merge run issue-spec pr verify-closure --repo higress-group/issue-spec --pr <implementation-pr> --proposal <proposal-issue> --design <design-issue> --implement <implement-issue>; on exit 1 re-run pr link-issues to restore the block.

## Project Workflow

- Workflow Source: `builtin`
- Workflow Schema: `issue-spec`
- Workflow Diagnostics:

Project workflow templates are declarative only. Active proposal, design, implement, SPEC, TASK, PROCESS, QUESTION, REVIEW, and VERIFY artifacts remain in GitHub issue-native storage; durable specs are repository files created during archive.
