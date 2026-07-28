# provider-integrations-and-evidence

## Purpose

Define operator-controlled provider contracts, external references, immutable revision-bound evidence, fail-closed synchronization, and authoritative current-revision assertions at terminal workflow gates.

Proposal Issues:
- https://github.com/higress-group/issue-spec/issues/160
- https://github.com/higress-group/issue-spec/issues/271

## Requirements

### Requirement: Core owns neutral integration contracts while vendor behavior remains external

The project MUST provide versioned provider-neutral contracts for source bindings, code evidence and external mutations required before agent startup or at workflow gates, MUST NOT compile vendor-specific adapters into the server or CLI, and MUST restrict executable adapter configuration to trusted operator scope.

#### Scenario: GitHub and self-hosted modes implement the same neutral boundaries differently

- **WHEN** the CLI requests issue data, source resolution or code evidence
- **THEN** GitHub mode MAY satisfy the boundaries with one adapter while self-hosted mode MUST use its issue backend plus configured neutral bindings/evidence without changing workflow semantics

#### Scenario: repository content cannot select an executable

- **WHEN** a project workflow names an external provider
- **THEN** it MAY reference an operator-registered provider key and evidence policy but MUST NOT supply an arbitrary executable path, arguments or credential source

#### Scenario: vendor adapters remain replaceable

- **WHEN** an operator installs a bridge for a code host, CI system or requirement tracker
- **THEN** the adapter MUST communicate through the documented versioned contract and neutral server APIs without requiring vendor fields in core tables or durable specs

#### Scenario: missing capabilities fail before partial mutation

- **WHEN** a selected provider lacks an operation required by review, verify or archive
- **THEN** capability discovery MUST fail the command with an actionable error before any partial workflow evidence is written

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/160#issuecomment-4926529483

### Requirement: External links and trusted evidence have separate lifecycle and provenance

The Server MUST store mutable provider-namespaced external references separately from immutable revision-bound evidence, MUST authorize and audit external-reference writers, and MUST provide idempotent retrieval and ingestion semantics that prevent URL-only records from being treated as proof; for trusted evidence publication, the self-hosted Server MUST require an active supported credential carrying explicit `evidence:write`, its applicable repository cap, and the authenticated identity's live `write`-or-higher permission for the exact repository, MUST NOT require, infer, or consult a separate Evidence Writer assignment, and MUST preserve immutable authenticated writer provenance, audit, tenant isolation, idempotency, visibility, and exact-revision validation.

#### Scenario: external references use a non-null provider identity

- **WHEN** a bridge records a work item, source repository, code change, build or archive link
- **THEN** the server MUST require provider key, relation kind, external repository/object identity and canonical URL and MUST upsert idempotently without NULL-based uniqueness gaps

#### Scenario: Scoped repository writer publishes without designation

- **WHEN** an active PAT carries `evidence:write`, allows the exact repository, and authenticates an active identity with live repository `write` permission, but no Active Evidence Writer assignment exists
- **THEN** the Server accepts an otherwise valid evidence append and records the authenticated writer identity without consulting designation state

#### Scenario: Repository write does not replace evidence credential scope

- **WHEN** an owner, administrator, maintainer, or writer has live repository write authority but the active credential lacks `evidence:write`
- **THEN** the Server rejects evidence publication with the credential-scope reason and audits the denial

#### Scenario: Evidence scope does not replace repository authority

- **WHEN** a credential carries `evidence:write` but its repository cap excludes the target, its identity is inactive, or its live repository permission is below `write`
- **THEN** the Server rejects publication with the precise repository-cap, identity, or permission reason and audits the denial

#### Scenario: Legacy assignments are non-authoritative

- **WHEN** an existing Evidence Writer assignment is active or inactive during the compatibility window
- **THEN** the assignment neither grants publication to an otherwise unauthorized caller nor denies an otherwise authorized caller

#### Scenario: Runner preflight uses the same authorization model

- **WHEN** Runner preflight validates a self-hosted profile and repository
- **THEN** it validates credential identity, required scopes, repository cap and live permission without calling or reporting an Evidence Writer designation check

#### Scenario: Evidence trust properties remain unchanged

- **WHEN** an authorized caller publishes a provider observation
- **THEN** the Server still enforces normalized evidence kind and state, exact subject revision, deterministic ingest identity, immutable provenance, visibility, supersession, idempotency and audit requirements

#### Scenario: reads respect tenant and field visibility

- **WHEN** the SPA, board or CLI lists references or evidence
- **THEN** the server MUST filter by repository visibility, caller permission and record visibility, return the normalized payload and provenance of a repository-visible evidence row to repository readers, omit a maintainers-visible row in full from non-maintainers, and MUST not leak credentials or hidden provider URLs or metadata

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/343#issuecomment-5099614411

### Requirement: Core review, verify and archive validate neutral external evidence fail closed

For externally hosted code changes, issue-spec MUST validate trusted structured evidence for the exact active subject revision, independent review findings, sealed required tests and provider checks, and authoritative merge state. Final verification MUST fail closed on missing, stale, untrusted, conflicting, or revision-mismatched evidence. Durable projection MUST appear only as an ordinary required test/check result, and post-merge issue closure MUST require exact merged evidence and active binding without restoring Archive review or verification semantics.

#### Scenario: verify passes only for exact trusted evidence

- **WHEN** the active external subject has exact-current accepted review and verification, no unresolved blocking findings, and every sealed test/check passes
- **THEN** core final verification MAY pass and MUST retain the evidence identities and revision used

#### Scenario: stale or untrusted evidence blocks

- **WHEN** external evidence is absent, expired, untrusted, conflicting, pending, failed, or bound to another subject revision
- **THEN** review or verification MUST fail closed and MUST NOT accept free-form approval prose

#### Scenario: durable result stays ordinary evidence

- **WHEN** repository durable checking is required
- **THEN** the exact checker outcome MUST be recorded through the ordinary required test/check contract and core final MUST NOT parse durable files or intent

#### Scenario: post-merge closure uses merged binding only

- **WHEN** a self-hosted implementation change has authoritative merged evidence for the exact active binding
- **THEN** idempotent closure MAY close only the bound lifecycle issues without re-running review, verification, or durable checks

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/308#issuecomment-5016452933

### Requirement: Provider-neutral evidence synchronization before workflow gates

The issue-spec CLI and runner MUST provide a provider-neutral evidence synchronization stage that resolves the active external code-change reference, selects an operator-registered adapter by `provider_key`, requests an `evidence.snapshot` for one exact subject revision, validates and immutably persists the returned neutral records through the self-hosted evidence authority, and only then evaluates review or verify gates from the persisted ledger; repository workflow configuration MAY select synchronization policy and required evidence but MUST NOT supply provider executables, credentials, or vendor-specific approval decisions.

#### Scenario: Explicit evidence synchronization uses the active code-change reference

- **WHEN** an operator or workflow invokes evidence synchronization for an implement issue and exact revision
- **THEN** issue-spec resolves exactly one active `code_change` reference, verifies the requested revision matches its pinned head revision, resolves the registered provider adapter, requires `evidence.snapshot`, and rejects an absent, ambiguous, mismatched, or incapable provider before evidence is written

#### Scenario: Verify can run workflow-directed synchronization before its gate

- **WHEN** the repository workflow enables pre-gate synchronization and `issue-spec verify` or the equivalent runner stage reaches external evidence validation
- **THEN** the synchronization stage runs first using the workflow's provider and evidence policy, persists the accepted snapshot, and verify evaluates the resulting exact-revision ledger records instead of requiring a separate vendor-specific command sequence

#### Scenario: Adapters return facts rather than gate decisions

- **WHEN** a GitHub, Aone, or other registered adapter snapshots a code change
- **THEN** it emits only protocol-versioned neutral `change`, `review`, `check`, `merge`, or `archive` records with stable identity, normalized state, observation time, exact revision, canonical URL where applicable, and payload digest, and issue-spec core independently decides whether the gate passes

#### Scenario: Evidence persistence is trusted and idempotent

- **WHEN** the synchronization stage writes a validated snapshot to the self-hosted server
- **THEN** the server requires an active supported credential with explicit `evidence:write`, exact repository access, and live `write`-or-higher permission, derives immutable writer provenance from the authenticated identity, uses deterministic ingest identity to make replay idempotent, preserves supersession history, and rejects untrusted approval booleans or identity changes

#### Scenario: Revision movement during synchronization fails closed

- **WHEN** the external change head or active reference changes while a snapshot is being collected or persisted
- **THEN** the stage does not mix records across revisions, does not advance the reference implicitly, and fails with an actionable revision-mismatch result that requires a new synchronization attempt for the new head

#### Scenario: Missing or failed synchronization never becomes successful verification

- **WHEN** the adapter is unavailable, its output is malformed, required checks are missing, pending, failed, stale, or tied to another reference or revision, or persistence is unauthorized
- **THEN** the synchronization or verify command fails closed and MUST NOT substitute a typed VERIFY comment, skill execution transcript, cached vendor response, or adapter-provided approved flag for trusted evidence

#### Scenario: Repository workflow cannot install executable authority

- **WHEN** a checked-out repository configures pre-gate evidence synchronization
- **THEN** it may select a registered provider key, required evidence kinds, required check names, freshness, and synchronization timing, while executable paths, arguments, environment, credentials, writer grants, and network authority remain operator-controlled outside the repository

#### Scenario: Runner and interactive CLI share one synchronization contract

- **WHEN** evidence synchronization is triggered interactively, by runner polling, or by a trusted webhook-driven runner job
- **THEN** all modes use the same provider-neutral snapshot validation, exact-revision persistence, idempotency, authorization, audit, and gate-evaluation semantics and produce equivalent evidence identities

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/343#issuecomment-5099614411

### Requirement: Authoritative final gates assert the current code-change revision

At authoritative final gates, issue-spec MUST resolve the current code-change revision through the existing mode-specific authority: GitHub-native mode MUST read the live pull-request HEAD, while self-hosted external-Provider mode MUST synchronously request the active code_change revision through the existing evidence.snapshot path regardless of optional sync_before policy and accept it only when the operator-trusted Provider asserts that the requested revision is still current. For a self-hosted code_change snapshot, the Provider MUST read current HEAD before collecting facts and read it again after collection, and MUST return success only when both observations equal the requested revision; movement before or during collection MUST return a stable revision-mismatch failure instead of a snapshot. In both modes, verify and status --gate final MUST fail closed unless the authoritative revision agrees with the eligible REVIEW completion and VERIFY input, including active reference identity and version where applicable. The assertion SHALL remain read-only, preserve historical artifacts, avoid mandatory Provider calls for non-terminal authoring and synchronization, expose the asserted revision for human pre-merge comparison, and MUST NOT perform merge or introduce a new Provider capability, evidence kind, Server API, or persisted-data model.

#### Scenario: GitHub-native final gate reads the live pull-request HEAD

- **WHEN** GitHub-native verify or status --gate final reads a pull request whose live HEAD, eligible review state, and VERIFY input all identify revision A
- **THEN** issue-spec uses the existing GitHub pull-request authority without a Code Provider bridge and may pass the final gate for revision A under the remaining policy

#### Scenario: GitHub-native HEAD movement invalidates old final evidence

- **WHEN** the GitHub pull-request HEAD advances from revision A to revision B after review or verification evidence for A was recorded
- **THEN** verify and status --gate final treat the evidence for A as stale and do not report current final readiness

#### Scenario: Self-hosted Provider confirms the pinned revision is current

- **WHEN** a self-hosted Implement Issue has one active code_change at reference version 3 and revision A, the Provider reads HEAD A both before and after collecting the exact-revision facts, and the eligible external REVIEW completion and VERIFY input are bound to the same identity, version, and revision
- **THEN** issue-spec validates and persists the snapshot through the existing native evidence authority, confirms the active reference did not move, and may pass verify or status --gate final under the remaining policy

#### Scenario: Self-hosted Provider rejects a retrievable historical revision

- **WHEN** the Provider current HEAD has advanced to revision B while the Server active code_change and stored REVIEW and VERIFY state still consistently identify revision A
- **THEN** the Provider reports a stable revision mismatch for the requested historical revision A and verify and status --gate final fail closed without silently refreshing the active reference

#### Scenario: Provider HEAD movement during snapshot collection fails closed

- **WHEN** the Provider reads current HEAD A for requested revision A, the code change advances to revision B while facts are collected, and the Provider's post-collection HEAD read observes B
- **THEN** the Provider returns a stable revision mismatch instead of a successful snapshot, issue-spec persists no partial Provider facts from that attempt, and verify and status --gate final do not report current readiness

#### Scenario: Provider authority failure cannot become a final green result

- **WHEN** the self-hosted Provider is unavailable, lacks evidence.snapshot, returns malformed or mismatched identity, or the active reference moves during synchronization
- **THEN** the current final invocation returns a non-success result with actionable retry or refresh guidance and does not accept stored Server state as proof of current HEAD

#### Scenario: Explicit refresh starts a new exact-revision evidence cycle

- **WHEN** an operator observes revision movement, explicitly refreshes the same active code_change from revision A to revision B, and reruns review synchronization and final verification
- **THEN** evidence bound to the former reference version or revision A remains historical but is ineligible, and only matching evidence for the refreshed reference version and revision B may satisfy the final gate

#### Scenario: Failure preserves history and unchanged retry is idempotent

- **WHEN** a final assertion fails or the same unchanged current revision is asserted again
- **THEN** issue-spec preserves earlier REVIEW, VERIFY, and Provider evidence without deletion or downgrade and repeats the exact successful synchronization without duplicating authoritative evidence

#### Scenario: Non-terminal work and human merge remain outside enforcement

- **WHEN** proposal, design, task, apply, local authoring, or another non-terminal synchronization operation continues, or a human prepares to merge after a successful final assertion
- **THEN** non-terminal work does not require a live Provider round trip, final output identifies the asserted revision for immediate comparison with the code host, and issue-spec does not execute or proxy merge

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/271#issuecomment-5009746561
