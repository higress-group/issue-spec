# Human-handoff workflow

Issue-spec has one delivery boundary: implement and validate one exact code
head, create or select its provider-native PR/MR, explain the change for human
review, report the handoff, and stop. The human and code provider own current
CI, review, approval, and merge.

Planning artifacts are optional. Use them when they improve a real decision or
managed execution; their status is never delivery acceptance.

## Choose the smallest planning path

Use a simple Issue for a bounded single-writer change. Add Proposal, Design,
Implement, SPEC, TASK, or PROCESS only when product decisions, durable contract
changes, or concrete coordination risk justify them.

A child/subagent is an execution choice, not a PROCESS trigger. Select execution
mode before assigning writers. Once Design or TASK is selected, or the user
explicitly asks for an independent worker, the Coordinator writes no code on
delegated or managed paths. Without managed PROCESS, exactly one real
non-Coordinator worker owns the bounded implementation. With managed PROCESS,
each change-bearing work package has one real non-Coordinator owner and distinct
packages may run concurrently. Select managed PROCESS only for:

- concurrent code writers;
- isolation that protects pre-existing work;
- enforced path ownership;
- restartable cross-session handoff;
- dependency-ordered integration.

Read-only investigation and review children never require PROCESS.

Coordinator code edits are limited to a narrow direct-PR fast path with no
selected Design/TASK and no user delegation request. File count never selects
this exception.

## Plan and implement

When planning is selected:

1. Persist the phase Issue body.
2. Record genuine unresolved decisions as typed QUESTION artifacts.
3. Upsert the statusless human review projection when enabled.
4. Create only selected SPEC/TASK/PROCESS artifacts.
5. Materialize confirmed durable-spec changes on the implementation branch.

Managed PROCESS preserves exact base, ownership, worktree isolation, DCO,
generators, tests, dependency order, and bounded handoff. These are execution
safety controls, not review or merge gates.

Each implementation worker owns one package's code, focused tests, exact result
commit, decisions, risks, and non-obvious line-rationale drafts. The Coordinator
owns dispatch/wait, exact-commit inspection, integration, proportionate final
validation, anchor validation, and provider publication. Workers receive no
provider credentials.

## Validate the exact head

Run tests and repository checks selected for the implementation. Push one exact
reviewable head. Create or select the provider-native PR/MR using an advertised
`change.create` operation, GitHub tooling, or an explicit manual fallback.

Optional `evidence.snapshot` data remains audit/navigation context. Issue-spec
does not normalize current provider policy or reproduce its merge decision.

## Converge reviewer findings

Before human handoff, dispatch one real read-only reviewer independent of every
code writer. Give it the exact base and current exact head, but no write path or
provider credentials. The reviewer returns actionable P0, P1, or P2 findings
with stable changed-line anchors.

Route each P0/P1 unchanged to the original writer that owns the affected code.
That writer repairs it, runs focused tests, and returns a new exact commit.
Integrate and push the repair, then have the same reviewer recheck it. Repeat
automatically until no P0/P1 remains. This routing needs no PROCESS unless a
separate managed-coordination need already exists.

Keep only still-applicable P2 findings from the final reviewed head. Publish
each unchanged as a provider-native non-blocking line comment when safe line
coordinates are supported. Otherwise publish an ordinary change-level
`change.comment` preserving `path:symbol/line`. P2 never enters the repair loop
and never pauses completion. If publication is unavailable or fails, report the
rendered body and continue.

This loop creates no typed REVIEW/VERIFY, finding evidence, receipt, readiness
gate, provider approval, or merge authority.

## Explain non-obvious code

Each actual code writer owns zero or more line-rationale drafts for non-obvious
decisions in changed code. A useful draft contains repository-relative path,
stable symbol plus changed-line anchor, and concise why/tradeoff/risk, with no
secret, raw payload, credential, guessed diff position, filler, or quota.

After exact-head integration and push, the Coordinator validates anchors,
continued applicability, and sensitive-data absence. Publish valid unchanged
writer text as non-blocking provider-native line discussion. Invalid, stale, or
sensitive drafts return to the writer or are dropped with an explanation; the
Coordinator never rewrites them while claiming writer authorship.

Maintain an ordinary top-level `### Implementation Rationale` discussion with:

- intent;
- decisions and tradeoffs;
- boundaries and known risks;
- tests and results;
- exact head and planning links;
- an index of line rationale.

If safe line discussion is unsupported or would create an unresolved provider
review blocker, preserve `path:symbol/line` plus writer text in that top-level
discussion. A requested publication failure remains visible and retryable.
Rationale comments are human context, not typed evidence or acceptance.

## Hand off and stop

Report:

- exact pushed head;
- PR/MR link;
- tests and results;
- rationale publication status;
- known risks, boundaries, and unsupported provider operations;
- planning artifacts when used.

Then stop before approval or merge. Do not create a readiness receipt, provider
policy model, merge command, or automatic post-merge reconciliation. The human
uses the current provider UI to inspect CI, approvals, conversations, ownership,
and branch policy and decides whether to merge.

## Provider capability model

`issue-spec.code-provider/v1` exposes only operation-scoped capabilities:

- `change.create`
- `change.comment`
- `evidence.snapshot`

Any implemented subset is valid. Missing capabilities constrain only those
actions and never switch the repository into a global planning-only mode.

## Historical workflow data

Historical REVIEW, VERIFY, rationale evidence, finalization, receipt, Archive,
and provider evidence remain readable for audit where supported. Retired writer
commands return `deprecated_workflow` before mutation. They are not part of
current delivery.
