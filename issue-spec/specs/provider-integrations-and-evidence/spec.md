# provider-integrations-and-evidence

## Purpose

Define operator-controlled provider contracts, external references, immutable revision-bound evidence, fail-closed synchronization, and authoritative current-revision assertions at terminal workflow gates.

Proposal Issues:
- https://github.com/higress-group/issue-spec/issues/160
- https://github.com/higress-group/issue-spec/issues/271

## Requirements

### Requirement: Core owns neutral integration contracts while vendor behavior remains external

Core MUST keep vendor behavior behind issue-spec.code-provider/v1, MUST strictly validate the new required review-decision, authoritative-check, and conditional-merge capabilities, MUST fail before authority consumption or mutation on every missing or unknown capability, and MUST permit this deliberate v1 breaking subtraction without a second protocol or legacy gate.

#### Scenario: v1 capability marks the semantic generation

- **WHEN** Core and a bridge negotiate the unchanged v1 protocol string
- **THEN** both must recognize the complete required capability set or fail closed without partial workflow behavior

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/405#issuecomment-5155764767

### Requirement: External links and trusted evidence have separate lifecycle and provenance

The Server MUST keep navigation references separate from immutable exact-subject external-check and fallback-review attestations, MUST preserve authenticated executor or reviewer provenance, trusted canonical-principal mapping, tenant visibility, idempotency, concurrent-fork detection, external-authority generation, and audit, and MUST NOT treat URL, ingest writer identity, legacy PROCESS/SPEC fields, or mutable workflow carriers as proof.

#### Scenario: trusted fallback preserves the deciding identity

- **WHEN** an authorized external executor or fallback reviewer publishes an exact-subject attestation
- **THEN** the Server records the authenticated deciding identity and assurance while navigation metadata and legacy linkage remain non-authoritative

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/405#issuecomment-5155764761

### Requirement: merge readiness is read-only and merge is exact-head conditional

The workflow MUST compute merge readiness with zero writes from only the selected simple Issue or Proposal contract, exact current code subject, current authoritative required-check conclusions, configured provider review policy and reviewer-owned findings, and required durable/provider policies; it MUST ignore optional or historical TEST, VERIFY, FinalReceipt, PROCESS, rationale, coverage, finalization, and pre-merge closure state, and the provider-native merge MUST atomically validate the complete authority token/generation plus expected head without a generic issue-spec merge state machine, while exact issue closure is reconciled idempotently after observed merge.

#### Scenario: necessary exact-head facts are sufficient

- **WHEN** the exact current subject satisfies configured provider-enforced checks, review policy, finding resolution, durable, DCO, and CLA policy
- **THEN** merge-check returns ready with the expected head and performs zero provider writes regardless of optional or historical workflow artifacts

#### Scenario: a necessary authority fails closed

- **WHEN** a required conclusion is missing, failed, stale, untrusted, policy-incomplete, or bound to another subject
- **THEN** merge-check returns a focused blocker with zero writes and no legacy evidence fallback

#### Scenario: head movement cannot race merge

- **WHEN** the provider head or same-head policy, review, finding, conversation, required-check, or supported external-authority generation changes after merge-check, or the provider cannot prove complete conditional merge
- **THEN** provider merge or preflight fails closed through authority-token/generation validation and issue-spec does not repair evidence or create a merge lifecycle

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/405#issuecomment-5155764772

### Requirement: checks consume one current authoritative conclusion across attempts

The workflow MUST allow CI retries and reruns, MUST consume only the provider-normalized current authoritative conclusion for each configured provider-native opaque check key, owner/integration identity, exact current subject, and configuration generation, MUST keep display name and historical attempts non-conflicting and non-authoritative, and MUST use a trusted exact-subject external attestation only when the configured check cannot run at the code provider and conditional merge can atomically validate its external-authority generation.

#### Scenario: reruns replace current authority without erasing history

- **WHEN** a required check has multiple attempts for the exact subject
- **THEN** the provider selects one current authoritative conclusion for the exact key and owner, while older attempts and same-name checks from another owner neither satisfy nor conflict with merge-check

#### Scenario: external fallback preserves executor assurance

- **WHEN** a configured required check cannot run through the code provider and its trusted executor completes it
- **THEN** one immutable attestation records exact subject, stable key, executor, protocol or command, relevant environment, conclusion, and bounded diagnostics, and preflight rejects the source unless conditional merge atomically validates its external generation

#### Scenario: merge evaluation never executes checks

- **WHEN** a required conclusion is missing, pending, failed, stale, or bound to another subject or configuration
- **THEN** merge-check reports that fact with zero writes and does not rerun, copy, or relabel the check

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/405#issuecomment-5155764758

### Requirement: v1 review decisions are policy-complete exact-subject authority

The v1 workflow MUST represent review with the exact subject, stable authenticated source reviewer identity distinct from ingest writer identity, operator-trusted canonical principals comparable across reviewer and author identity domains, a closed opener/commit-author/coauthor/committer set, verdict and dismissal state, finding ownership and reviewer-owned resolution, and configured provider review-policy state; it MUST require reviewer independence and the configured reviewer count, CODEOWNERS or equivalent, stale-dismissal, and conversation-resolution rules, while PROCESS/SPEC linkage is deprecated and non-authoritative and fallback is eligible only when conditional merge atomically validates its external-authority generation.

#### Scenario: provider-native review satisfies configured policy

- **WHEN** the exact current subject has review decisions whose mapped canonical principals, verdicts, findings, and conversation state satisfy the configured provider policy and include a principal outside the complete author set
- **THEN** merge-check accepts the provider decisions without REVIEW PROCESS, sync, coverage projection, or a copied receipt

#### Scenario: provider without review uses per-reviewer exact-subject fallbacks

- **WHEN** the mandatory v1 review-decision capability explicitly reports that the provider lacks a review primitive
- **THEN** Core may accept one immutable issue-native decision per authenticated reviewer and exact subject only after trusted canonical-principal mapping and proof that conditional merge atomically validates the external-authority generation; it aggregates current decisions under the configured policy, including reviewer counts greater than one, or preflight rejects fallback

#### Scenario: stale dismissed or self-authored review blocks

- **WHEN** a decision targets another head, is dismissed or stale, has an unmapped or ambiguous principal, matches any opener/author/coauthor/committer principal, or leaves configured findings or conversations unresolved
- **THEN** merge-check fails closed without consulting logical Agent, PROCESS, SPEC, rationale, receipt, or comment order

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/405#issuecomment-5155764761

### Requirement: v1 capability cutover removes legacy authority as one pinned release set

The workflow MUST keep the issue-spec.code-provider/v1 protocol and built-in issue-spec schema while supporting a bounded simple-Issue path or a selected Proposal/Design/Implement path and requiring evidence.review-decision, provider-native keyed evidence.authoritative-check-conclusion when checks are configured, and complete authority-token/generation change.merge-conditional at the merge boundary; new and old components MUST fail fast on missing or unknown capabilities, unsupported fallback atomicity, or unmapped principals, legacy fields MUST be read-only and non-authoritative, deprecated evidence writers MUST return deprecated_workflow with zero writes, and CLI, Server, Runner, bridge, and generated skills MUST switch and roll back as one pinned immutable release set with no dual gate or legacy adapter.

#### Scenario: mixed releases fail before authority is consumed

- **WHEN** new Core sees an old bridge without required capabilities or old Core sees a new capability
- **THEN** preflight fails closed before a green merge decision or workflow mutation and does not fall back to legacy evidence, bare-name checks, heuristic identity, or expected-head-only merge

#### Scenario: upgrade uses an immutable coordinated set

- **WHEN** an operator adopts the breaking v1 subtraction
- **THEN** the operator quiesces dispatch and merge, switches pinned CLI, Server, Runner, bridge, and generated skills, validates capabilities and release identities, and resumes only after preflight succeeds

#### Scenario: historical and optional planning remain non-authoritative

- **WHEN** legacy REVIEW, VERIFY, PROCESS, rationale, receipt, finalization, TASK, workspace, or handoff data remains readable
- **THEN** merge-check ignores it and deprecated writers perform zero writes while optional implementation workspaces retain their execution-safety rules

#### Scenario: rollback is a complete-set switch

- **WHEN** an operator must restore the previous release
- **THEN** the operator quiesces work, restores the pinned prior binaries, bridge, configuration, generated skills, and required backup as one set, and re-obtains facts rather than reinterpreting new evidence

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/405#issuecomment-5155764767
