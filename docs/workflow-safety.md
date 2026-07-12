# Workflow safety, reconciliation, and PROCESS evidence

This guide is the operator and agent contract for readiness forecasts, safe
typed-comment mutation, resumable batches, delegated operation preflight, and
proportional PROCESS evidence.

## Forecast the gate before doing work

Use the gate that matches the next action:

```bash
issue-spec status --repo owner/repo \
  --proposal 10 --design 11 --implement 12 \
  --gate final --json
```

Valid gates are `proposal`, `design`, `implement`, `final`, and `archive`.
`status` is a point-in-time forecast with stable diagnostics and remediation;
`verify` remains authoritative and re-observes remote checks, findings, and
evidence before declaring final readiness.

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

## Reconcile a dependency-ordered plan

Use a version-1 JSON plan for create/upsert, transition, and bidirectional link
operations. Validate and retain one checkpoint beside the plan:

```bash
issue-spec workflow reconcile --plan reconcile.json \
  --checkpoint .issue-spec/reconcile-checkpoint.json --json
```

The engine validates the complete schema, logical IDs, transition legality,
dependencies, DAG, and plan digest before the first write. It atomically saves
the checkpoint after each successful operation and always re-observes remote
state on resume. A rerun therefore recognizes a lost create response, repairs
only a missing backlink direction, and leaves rate-limited dependent work
pending. Duplicate logical markers and checkpoint/plan digest mismatches fail
closed. Conditional mutation is the default; non-atomic operations require the
plan-level `allow_nonatomic` acknowledgement.

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
expiry knowledge, network result, and per-operation decision. Strict runner
execution requires an operator-owned short-lived issuer scoped to the exact
host, repository, job, purpose, and operations. Mirrored host-gh credentials
are reported as `legacy_long_lived`; they are migration-only and never satisfy
strict policy.

## Choose a PROCESS execution class

Generate every PROCESS with one explicit `execution_class`. All five classes
still require a TASK link, the required PR link, and active SPEC coverage.

| Class | Required evidence carrier |
| --- | --- |
| `change-bearing` | Inline rationale whose marker path/line matches the actual PR comment and an active SPEC |
| `review` | Linked done REVIEW or resolved finding for an active SPEC |
| `verification` | Linked done VERIFY, or required passing check, with test evidence for an active SPEC |
| `orchestration` | Non-empty coordination handoff plus active SPEC coverage |
| `external` | Consumed provider evidence at the exact subject revision with stable evidence IDs |

A legacy PROCESS without the section remains readable but projects to
`change-bearing` with a migration warning. An unknown, empty, duplicate, or
multi-valued class blocks verification. `review sync` and final `verify` expose
required, satisfied, and missing evidence per PROCESS; do not invent code-line
rationale for non-change-bearing work.
