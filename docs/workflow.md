# Minimal provider-bound workflow

Issue-spec has one delivery model: select a bounded Issue contract, satisfy current provider checks and provider review at one exact code subject, inspect readiness without writes, merge through a provider-issued complete authority token, then reconcile Issue closure after the provider reports the change merged.

Planning artifacts are optional. They help people make decisions and isolate implementation, but their status, links, receipts, rationale, and history never change merge readiness.

## Select the contract by risk

- Use a simple Issue for a bounded change whose product and design risk does not require a Proposal. File count is not the criterion.
- Use a Proposal when requirements need explicit confirmation. Add a Design only for design risk and an Implement/TASK/PROCESS plan only for coordination, delegation, or workspace-isolation risk.
- Exactly one simple Issue or Proposal is the root. Design and Implement may appear only with a Proposal.

SPEC, QUESTION, TASK, and PROCESS comments remain canonical issue-native planning artifacts. Repository durable projection materializes confirmed behavior in `issue-spec/specs/**` on the implementation branch. Durable-spec, DCO, CLA, security, and business policy are ordinary configured checks.

## Configure stable merge inputs

Repository configuration selects an operator-registered provider and stable provider-native check identities:

```yaml
external_code:
  provider_key: code.example
  merge:
    required_checks:
      - source: provider
        provider: code.example
        key: app:42/context:unit
        owner: app:42
        display_name: unit
```

A bare display name is not an identity. The old `external_code.evidence` keys are rejected; they are not mapped into the new model.

## Preflight one immutable release set

As an operator-controlled deployment procedure before enabling Runner dispatch or merge, quiesce mutations and validate the same pinned CLI, Server, Runner, generated assets, and provider bridge:

```sh
issue-spec workflow preflight \
  --repo owner/repo \
  --release-set 2.0.0 \
  --server-release 2.0.0 \
  --runner-release 2.0.0 \
  --generated-manifest .agents/skills/issue-spec-workflow/release.json \
  --generated-digest sha256:... \
  --provider-generation minimal-merge-authority/v1 \
  --provider-build code-example@sha256:... \
  --json
```

Preflight is read-only and reports `purpose=operator-controlled-deployment-readiness`. The operator supplies freshly observed Server/Runner identities; the command compares them with the local CLI, generated manifest, provider generation/build/capabilities, configured key/owner pairs, canonical-principal mapping source, fixed `post-merge-idempotent` reconciliation, and fixed `provider-authority-token` enforcement. It is a trusted cutover check, not a persisted receipt or merge authority, and merge commands do not consume its output. Authority-bearing commands independently revalidate provider generation, build, capabilities, principal mapping, exact subject, and a fresh authority token before collection or mutation.

An unavailable merge-authority provider does not block `init`, Proposal, Design, or direct manual development. Init reports `workflow_readiness.mode=planning-only` and `merge_capable=false` and generates no provider-authority Skill. The operator must keep Runner dispatch quiesced; `merge-check` and conditional merge independently fail closed. Even a capability-complete provider is reported as `operator-preflight-required`, `provider_authority_capable=true`, and `merge_capable=false` at init time because init does not establish principal mapping, configured check identity, or deployment readiness.

## Review and checks stay provider-owned

Before requesting human review of a reviewable exact change, publish or refresh
one ordinary top-level provider discussion headed `### Implementation
Rationale`. Do this for both direct single-writer and managed PROCESS
implementation; no Implement, TASK, PROCESS, or SPEC is required. Summarize the
intent, key decisions and tradeoffs, boundaries and non-goals, known risks,
validation and current results, exact review subject/head, and selected
Issue/Proposal/Design links.

Use the provider-native discussion surface, not the deprecated `code-change
rationale` evidence command. Add no machine marker, rationale ID, typed
carrier, PROCESS/SPEC binding, evidence field, or gate. If the write fails,
report the provider error, retain the rendered body for retry or manual posting,
and do not claim the human-review handoff is complete. The comment and its
publication status remain mutable review UX only; `merge-check` and conditional
merge never consume them.

The provider returns one policy-complete exact-subject review snapshot. Reviewer independence compares trusted canonical principals against the complete opener, author, coauthor, and committer set. Current changes-requested decisions, unresolved required conversations, and open P0/P1 findings block. At least one qualifying approval must be independent.

For every configured check, the provider chooses exactly one current conclusion for the opaque key, owner, exact subject, and provider configuration generation. Historical attempts and same-name checks from another owner are audit data only. Only `success` passes.

There is no issue-native review fallback or external authority generation. A provider that cannot return the complete review/check snapshot and atomically validate its authority token is not merge-capable and fails closed.

## Read-only readiness

For a simple Issue:

```sh
issue-spec merge-check --repo owner/repo --issue 41 --pr 87 --json
```

For an optional phase plan:

```sh
issue-spec merge-check --repo owner/repo \
  --proposal 41 --design 42 --implement 43 \
  --change-id change-87 --head <exact-head> --json
```

`merge-check` performs zero writes. It does not execute checks or read TASK/PROCESS lifecycle, REVIEW/VERIFY comments, role receipts, rationale, relationship coverage, finalization, Archive state, or pre-merge closing links. Its decision and snapshot digest are diagnostic output, not reusable proof.

Candidate CLI dogfood must remain on this read-only command until the exact-head check set and provider-native review authority are proven.

## Conditional merge and reconciliation

```sh
issue-spec code-change merge --repo owner/repo \
  --proposal 41 --design 42 --implement 43 \
  --change-id change-87 --expected-head <exact-head> --json
```

The merge command recollects a fresh snapshot, reevaluates the same pure predicate, and asks the provider to atomically validate the opaque authority token plus expected head. Expected-head CAS alone is insufficient because review, policy, findings, conversations, and checks can drift at the same head. Ordinary GitHub REST read-then-write is non-atomic and remains fail-closed unless an operator bridge proves complete provider-native enforcement.

After freshly observing merged state, issue-spec reconciles exactly the selected Issue set idempotently. Reconciliation failure cannot undo or make the code merge ambiguous; retry bookkeeping separately.

## Deprecated boundary

Legacy review synchronization/completion, verification submission/final verification, rationale-as-evidence, evidence-only PROCESS completion, finalization, closure verification, and Archive gates return `deprecated_workflow` before local, Issue, relationship, evidence, or provider mutation. Historical artifacts remain available through explicit audit reads only. This retirement does not include the ordinary human-facing provider discussion above.

Upgrade and rollback are both complete-set switches: quiesce dispatch and merge, install the pinned binaries, bridge, generated assets, and configuration, validate preflight, then resume. Never run mixed generated assets or translate new facts into old REVIEW/VERIFY authority.
