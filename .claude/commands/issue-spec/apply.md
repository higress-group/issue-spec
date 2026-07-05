---
name: "Issue Spec: Apply"
description: "Implement PROCESS comments for an issue-spec change and keep PR traceability synchronized."
category: "Workflow"
tags: ["workflow", "issue-spec"]
---

# Issue Spec Apply

Use when the user asks for /issue-spec:apply, issue-spec apply, or implementing PROCESS/TASK scopes from an issue-spec change.

## Steps

1. Read proposal/design/implement issue context and list typed comments with issue-spec comment list --json.
2. Confirm issue-spec auth status --json includes the expected GitHub backend. Local gh-authenticated sessions can use the native gh backend; keep ISSUE_SPEC_TOKEN="$(gh auth token)" only as an older-version or forced-rest compatibility path.
3. Plan the PROCESS DAG before dispatch: read each active TASK's ### Execution Planning metadata and derive PROCESS nodes from it. Render PROCESS bodies with issue-spec comment generate --type PROCESS --input-file process.json (fields include parent_task, owner, dependencies, write_ownership, handoff) instead of hand-writing Markdown; comment upsert --type PROCESS rejects a PROCESS without a ### Parent TASK.
   Keep Agent as the logical role. Pass assigned subagent/session ids with --agent-session; Codex CODEX_THREAD_ID remains the artifact writer session source of truth when present.
4. Default to serial PROCESS chains under one parent TASK, each completed node recording ### Handoff evidence for its successor. Split into parallel worker PROCESS nodes only when file/module write ownership is provably disjoint; parallelism is a gated optimization, not the default.
5. Add dedicated review PROCESS nodes for non-trivial changes. Review PROCESS nodes should own review scopes such as CLI/API behavior, workflow docs, tests, compatibility, or security-sensitive surfaces.
6. Link each PROCESS to its TASK comments with issue-spec link.
7. Implement the code changes for one PROCESS scope at a time, or integrate completed worker outputs by dependency order. The worker that owns a code scope owns its own commits; the coordinator does not author code artifacts on a worker's behalf unless it is the assigned worker.
8. Link every worker and review PROCESS to the PR with issue-spec pr link-process.
9. Add proposal/design/implement closing links to the implementation PR body:

       issue-spec pr link-issues --repo higress-group/issue-spec --pr <implementation-pr> --proposal <proposal-issue> --design <design-issue> --implement <implement-issue> --json

10. Add final PR rationale only after review/fix convergence, not as pre-review readiness evidence. Once all P0/P1 findings are resolved, the coordinator dispatches each owning worker to add rationale on the key code blocks that worker owns with issue-spec pr rationale (worker --agent and --agent-session), each linked to a SPEC comment.
11. Mark PROCESS comments done only after implementation/review work and focused verification evidence exist.

## Coordinator DAG Execution

1. Derive the DAG from TASK ### Execution Planning metadata; default to serial PROCESS chains under one parent TASK.
2. Build the ready set from PROCESS nodes whose dependencies are done.
3. Keep immediate blocking work local when the next step depends on it, and record ### Handoff evidence on each completed serial node before starting its successor.
4. Spawn or assign parallel worker agents only when their write ownership is provably disjoint, and give each worker an assigned id to pass via --agent-session.
5. Spawn or assign review agents for non-trivial PRs; run them in parallel only when their review scopes are disjoint. Route findings to the owner PROCESS or a dedicated repair PROCESS under the same serial/parallel gating.
6. Integrate completed outputs by dependency order and update PROCESS evidence (including ### Handoff for serial predecessors) before marking done.
7. The coordinator owns scheduling, gate evaluation, status synchronization, unresolved-blocker routing, and final rationale dispatch only. It does not author review findings, worker fix replies, review resolutions, or rationale on another agent's behalf unless explicitly assigned as that worker or review owner.

## Project Workflow

- Workflow Source: `builtin`
- Workflow Schema: `issue-spec`
- Workflow Diagnostics:

Project workflow templates are declarative only. Active proposal, design, implement, SPEC, TASK, PROCESS, QUESTION, REVIEW, and VERIFY artifacts remain in GitHub issue-native storage; durable specs are repository files created during archive.
