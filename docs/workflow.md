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
    review_fallback:
      enabled: false
```

A bare display name is not an identity. The old `external_code.evidence` keys are rejected; they are not mapped into the new model.

## Preflight one immutable release set

Before Runner dispatch or merge, quiesce mutations and validate the same pinned CLI, Server, Runner, generated assets, and provider bridge:

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
  --canonical-principals operator-map@sha256:... \
  --review-mode provider_native \
  --reconciliation-mode post-merge-idempotent \
  --enforcement-mode provider-authority-token \
  --external-authority-mode disabled \
  --json
```

Preflight is read-only. It validates `issue-spec.code-provider/v1`, semantic generation `minimal-merge-authority/v1`, the immutable bridge build, `evidence.review-decision`, `evidence.authoritative-check-conclusion`, `change.merge-conditional`, generated-asset release/digest, configured key/owner pairs, canonical-principal mapping identity, review mode, reconciliation mode, and complete authority-token enforcement. Missing, unknown, or mixed identities fail before authority is consumed or anything mutates.

## Review and checks stay provider-owned

The provider returns one policy-complete exact-subject review snapshot. Reviewer independence compares trusted canonical principals against the complete opener, author, coauthor, and committer set. Current changes-requested decisions, unresolved required conversations, and open P0/P1 findings block. At least one qualifying approval must be independent.

For every configured check, the provider chooses exactly one current conclusion for the opaque key, owner, exact subject, and provider configuration generation. Historical attempts and same-name checks from another owner are audit data only. Only `success` passes.

Issue-native review fallback is disabled by default. It is eligible only when the provider explicitly requires it, repository policy enables it, identities map through an operator-owned canonical source, and conditional merge atomically validates the external-authority generation.

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

Legacy review synchronization/completion, verification submission/final verification, rationale-as-evidence, evidence-only PROCESS completion, finalization, closure verification, and Archive gates return `deprecated_workflow` before local, Issue, relationship, evidence, or provider mutation. Historical artifacts remain available through explicit audit reads only.

Upgrade and rollback are both complete-set switches: quiesce dispatch and merge, install the pinned binaries, bridge, generated assets, and configuration, validate preflight, then resume. Never run mixed generated assets or translate new facts into old REVIEW/VERIFY authority.
