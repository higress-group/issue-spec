# provider-integrations-and-evidence

## Purpose

Define operator-controlled provider contracts, navigation references, ephemeral exact-head merge authority, and fail-closed current-revision assertions at the provider merge boundary.

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

### Requirement: navigation links and merge authority remain separate

The Server and CLI MUST keep navigation references and historical evidence separate from the provider's ephemeral exact-head review/check snapshot, MUST obtain every canonical principal from an operator-owned mapping source, and MUST NOT treat URL, ingest writer identity, legacy PROCESS/SPEC fields, mutable workflow carriers, or caller assertions as proof. No fallback attestation or external-authority generation is produced.

#### Scenario: historical evidence remains audit-only

- **WHEN** legacy evidence or navigation metadata remains stored after cutover
- **THEN** explicit audit reads preserve it while merge-check and conditional merge consume only freshly mapped provider-native authority

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

- **WHEN** the provider head or same-head policy, review, finding, conversation, or required-check authority changes after merge-check, or the provider cannot prove complete conditional merge
- **THEN** provider merge or preflight fails closed through complete authority-token validation and issue-spec does not repair evidence or create a merge lifecycle

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/405#issuecomment-5155764772

### Requirement: implementation rationale remains an ordinary provider discussion for human review

Once an exact code change is reviewable, the workflow MUST publish or refresh one ordinary top-level provider discussion headed `### Implementation Rationale` before requesting human review, for both direct single-writer and managed PROCESS implementation. The discussion MUST summarize intent, important decisions and tradeoffs, boundaries and non-goals, known risks, validation and current results, exact review subject/head, and selected Issue, Proposal, or Design links without requiring an Implement, TASK, PROCESS, SPEC, typed carrier, machine marker, rationale ID, PROCESS/SPEC binding, evidence field, or gate. It MUST remain mutable human-review UX that merge-check and conditional merge ignore completely.

#### Scenario: direct and managed implementation have the same review handoff

- **WHEN** either a direct single writer or selected managed PROCESS work produces a reviewable exact change
- **THEN** the coordinator publishes or refreshes the same ordinary provider-native `### Implementation Rationale` discussion before requesting human review without manufacturing planning or evidence state

#### Scenario: discussion publication failure is visible but non-authoritative

- **WHEN** the provider-native discussion cannot be published or refreshed
- **THEN** the coordinator reports the provider error, retains the rendered body for retry or manual posting, and does not claim the human-review handoff is complete
- **THEN** merge-check and conditional merge neither consume the failure nor reinterpret it as a legacy delivery gate or authority result

Source Design amendment:
- https://github.com/higress-group/issue-spec/issues/407

### Requirement: checks consume one current authoritative conclusion across attempts

The workflow MUST allow CI retries and reruns, MUST consume only the provider-normalized current authoritative conclusion for each configured provider-native opaque check key, owner/integration identity, exact current subject, and configuration generation, and MUST keep display names and historical attempts non-conflicting and non-authoritative. A check that cannot be represented by the selected provider makes that provider ineligible; Core MUST NOT substitute an external attestation.

#### Scenario: reruns replace current authority without erasing history

- **WHEN** a required check has multiple attempts for the exact subject
- **THEN** the provider selects one current authoritative conclusion for the exact key and owner, while older attempts and same-name checks from another owner neither satisfy nor conflict with merge-check

#### Scenario: missing provider-native check support fails closed

- **WHEN** a configured required check cannot be returned and conditionally enforced by the selected provider
- **THEN** preflight fails before authority collection or mutation and does not accept a caller or issue-server attestation

#### Scenario: merge evaluation never executes checks

- **WHEN** a required conclusion is missing, pending, failed, stale, or bound to another subject or configuration
- **THEN** merge-check reports that fact with zero writes and does not rerun, copy, or relabel the check

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/405#issuecomment-5155764758

### Requirement: v1 review decisions are policy-complete exact-subject authority

The v1 workflow MUST represent provider-native review with the exact subject, stable authenticated source reviewer identity distinct from bridge identity, operator-trusted canonical principals comparable across reviewer and author identity domains, a closed opener/commit-author/coauthor/committer set, verdict and dismissal state, finding ownership and reviewer-owned resolution, and configured provider review-policy state; it MUST require reviewer independence and the configured reviewer count, CODEOWNERS or equivalent, stale-dismissal, and conversation-resolution rules, while PROCESS/SPEC linkage is deprecated and non-authoritative and no issue-native fallback is eligible.

#### Scenario: provider-native review satisfies configured policy

- **WHEN** the exact current subject has review decisions whose mapped canonical principals, verdicts, findings, and conversation state satisfy the configured provider policy and include a principal outside the complete author set
- **THEN** merge-check accepts the provider decisions without REVIEW PROCESS, sync, coverage projection, or a copied receipt

#### Scenario: provider without policy-complete review is unsupported

- **WHEN** a provider cannot return policy-complete provider-native review with stable actors and a closed author set
- **THEN** preflight fails before authority collection or mutation and Core does not synthesize decisions from issue comments

#### Scenario: stale dismissed or self-authored review blocks

- **WHEN** a decision targets another head, is dismissed or stale, has an unmapped or ambiguous principal, matches any opener/author/coauthor/committer principal, or leaves configured findings or conversations unresolved
- **THEN** merge-check fails closed without consulting logical Agent, PROCESS, SPEC, rationale, receipt, or comment order

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/405#issuecomment-5155764761

### Requirement: v1 capability cutover removes legacy authority as one pinned release set

The workflow MUST keep the issue-spec.code-provider/v1 protocol and built-in issue-spec schema while supporting a bounded simple-Issue path or a selected Proposal/Design/Implement path and requiring evidence.review-decision, provider-native keyed evidence.authoritative-check-conclusion when checks are configured, and complete authority-token change.merge-conditional at the merge boundary; new and old components MUST fail fast on missing or unknown capabilities, any non-atomic provider, or unmapped principals, legacy fields MUST be read-only and non-authoritative, deprecated evidence writers MUST return deprecated_workflow with zero writes, and CLI, Server, Runner, bridge, and generated skills MUST switch and roll back as one pinned immutable release set with no dual gate or legacy adapter.

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
