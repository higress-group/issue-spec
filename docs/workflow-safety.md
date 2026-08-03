# Workflow safety and failure behavior

Issue-spec planning gates answer only whether selected planning is internally
consistent. They never claim code-provider readiness or mergeability.

## Planning forecast

`issue-spec status --summary` reports blockers for the selected Issue,
Proposal, Design, Implement, SPEC, TASK, QUESTION, and optional PROCESS plan.
Valid `--gate` values are `proposal`, `design`, and `implement`.

Use the forecast before advancing a selected planning phase. Do not manufacture
planning artifacts merely to obtain a green status.

## Direct and managed implementation

Default to one code writer. A single bounded child may write without PROCESS
when the Coordinator performs no concurrent code writes.

Use managed PROCESS only for concurrent writers, isolation protecting existing
work, enforced path ownership, restartable cross-session handoff, or
dependency-ordered integration. Workspace prepare/complete/integrate preserve
base, ownership, clean commits, tests, and handoff without a role receipt. Those facts are
implementation safety only.

On conflict, ambiguous ownership, dirty or uncertain worktree state, failed
test, or revision drift, stop the affected integration and
retain recoverable work. Cleanup is explicit and owner-token authorized.

## Exact-head validation

Run selected project checks on the final implementation head. Push exactly that
head and create or select its PR/MR. A changed head invalidates earlier test and
rationale anchors and requires proportionate revalidation.

Provider operation failure is local:

- missing `change.create`: report a manual PR/MR creation fallback;
- missing `change.comment`: preserve rendered rationale for manual publication;
- missing `evidence.snapshot`: omit audit snapshot collection.

None creates a global planning-only state or blocks unrelated Runner dispatch.

## Human handoff

Before requesting human review, report exact head, PR/MR link, tests/results,
known risks and boundaries, rationale publication status, and provider operation
limitations. Stop before approval or merge.

The current provider UI is authoritative for CI, review, approvals,
conversations, ownership, branch policy, merge, and closing behavior. Issue-spec
does not persist a readiness receipt, normalize provider policy, or perform an
automatic post-merge transition.

## Retired paths

Legacy review/verify submission, rationale evidence, finalization, Archive, and
evidence-driven completion writers return `deprecated_workflow` before local,
Issue, relationship, provider, or evidence mutation. Historical objects remain
available for explicit audit reads where supported.
