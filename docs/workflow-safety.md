# Workflow planning and PROCESS safety

This guide is the operator and agent contract for planning forecasts, safe
typed-comment mutation, delegated operation preflight, and optional PROCESS
implementation safety.

## Forecast the gate before doing work

Use the gate that matches the next action:

```bash
issue-spec status --repo owner/repo \
  --proposal 10 --design 11 --implement 12 \
  --gate implement --json
```

Valid gates are `proposal`, `design`, and `implement`. `status` is a
point-in-time planning forecast with stable diagnostics and remediation. It is
never a merge-readiness decision; use the read-only `merge-check` for current
provider authority.

## Mutate one artifact safely

Use `comment transition` for status, PROCESS handoff, PR link, and related-link
changes. Do not regenerate the whole comment for those mutations.

```bash
issue-spec comment transition --repo owner/repo --issue 12 \
  --id PROCESS-003 --to done --expected-version 7 \
  --handoff-file handoff.md --pr https://github.com/owner/repo/pull/20 --json
```

Conditional backends compare the observed representation version and return
the expected/current identities on conflict. A backend without comment CAS
fails before writing. Its compatibility mode must be acknowledged explicitly:

```bash
issue-spec comment transition --repo owner/repo --issue 12 \
  --id PROCESS-003 --to done \
  --expected-digest <observed-sha256> --allow-nonatomic --json
```

Accepted compatibility results report `atomic: false`, the
`non-atomic-single-writer` guarantee, and before/after body digests.

## Prove delegated operations before allocation

Request only the operations the worker needs and run the probe before creating
a workspace, leasing credentials, or allocating a worker:

```bash
issue-spec doctor agent --repo owner/repo \
  --operation issue.read --operation artifact.write \
  --operation git.clone --operation git.push --json
```

Other stable operations include `issue.comment.write`, `pr.read`,
`pr.review.write`, `pr.update`, `checks.read`, and
`external.change.comment`. Reports are redacted and describe the source class,
expiry knowledge, network result, and per-operation decision. A self-hosted
runner may use the private PAT from its origin-bound profile when the token
grants the requested repository and includes the minimum runner scopes; that file may
report unknown expiry. Strict GitHub delegated execution still requires an
operator-owned short-lived issuer scoped to the exact host, repository, job,
purpose, and operations. Mirrored host-gh credentials are reported as
`legacy_long_lived`; they are migration-only and never satisfy strict policy.

## Choose a PROCESS execution class

Generate every optional PROCESS with one explicit `execution_class`. The class
controls implementation workspace and handoff safety only; no class, status,
link, or evidence carrier contributes to merge readiness.

| Class | Implementation behavior |
| --- | --- |
| `change-bearing` | Writable owned branch with exact-base, one-commit/DCO, ownership, and focused-test validation |
| `orchestration` | Lifecycle bookkeeping without a checkout |
| `external` | Operator-owned external execution without a local checkout |

A legacy PROCESS without the section remains readable but projects to
`change-bearing` with a migration warning. An unknown, empty, duplicate, or
multi-valued class blocks delegated execution safety, not provider merge
authority. Historical `review` and `verification` values remain readable for
audit, but generation and workspace preparation reject them before mutation;
provider-native review and configured checks replace those execution classes.

For self-hosted and GitHub code hosts, run the read-only `merge-check` only
after provider-native policy-complete review and current configured checks
exist at the exact subject. Merge through `code-change merge --expected-head`,
which recollects fresh authority and passes the provider-issued complete token
to a conditional merge. Reconcile the selected Issue set only after freshly
observed merge. Legacy REVIEW/VERIFY/rationale/Archive artifacts are audit-only.

## Keep PROCESS workspaces exact and recoverable

Use only the exact PROCESS id selected by the coordinator from the typed DAG, never a PROCESS inferred from prompt prose or runner command grammar. The runner launches exactly one ACPX coordinator and keeps its cwd and primary sandbox workspace at the public session clone; it does not launch or resume an ACPX session per PROCESS. The workspace lifecycle has six commands: `prepare`, `inspect`, `complete`, `integrate`, `reconcile`, and `cleanup`. Keep their repository, issue, PROCESS, roots, and owner token stable. Runner mode supplies trusted session-local defaults through `ISSUE_SPEC_PROCESS_INTEGRATION_ROOT` and `ISSUE_SPEC_PROCESS_WORKSPACE_ROOT`; standalone coordinators pass explicit roots.

`change-bearing` gets a writable owned branch. `orchestration` gets no checkout,
and `external` uses mode `none`. Historical `review` and `verification`
workspaces cannot be prepared or completed. PROCESS completion never becomes a
merge gate.

After `prepare`, the coordinator delegates through the current agent runtime's native child/subagent facility with the exact worktree path as cwd, branch, write ownership, PROCESS id, parent TASK, and bounded predecessor handoff. The child is not an ACPX session. In runner mode it shares the coordinator's outer sandbox, which exposes the session clone and only that session's PROCESS pool; `--unsafe-no-sandbox` provides no filesystem isolation. The child authors a result commit, runs focused tests, and returns handoff evidence. The coordinator validates that output and runs `complete` and `integrate` from the unchanged session clone before status transition.

After runner resume or restart, the top-level runner recovers only the ACPX/session job. From the unchanged session clone, the coordinator owns the PROCESS lifecycle and runs `inspect` or `reconcile` on the exact lease before `complete` and `integrate`. Missing, mismatched, dirty, or needs-reconcile state blocks the coordinator. It invokes owner-token cleanup only after an explicit integration or retention decision.

Top-level runner session-clone retention calls `git worktree list` and fails closed by retaining the clone when runner metadata is dirty or uncertain, a linked worktree exists, or git worktree inspection fails. It does not own, persist, or retry child PROCESS cleanup. `workflow workspace cleanup` is always an explicit owner-token-authorized destructive operation and can remove an unintegrated change-bearing worktree. It does not decide or enforce integration/retention eligibility for its caller; invoke it only after making that decision.
