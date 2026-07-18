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

The server MUST store mutable provider-namespaced external references separately from immutable revision-bound evidence, MUST authorize and audit their writers, and MUST provide idempotent retrieval and ingestion semantics that prevent URL-only records from being treated as proof.

#### Scenario: external references use a non-null provider identity

- **WHEN** a bridge records a work item, source repository, code change, build or archive link
- **THEN** the server MUST require provider key, relation kind, external repository/object identity and canonical URL and MUST upsert idempotently without NULL-based uniqueness gaps

#### Scenario: evidence is immutable and revision bound

- **WHEN** a designated writer reports review, check, merge or archive state
- **THEN** the server MUST append an immutable row containing normalized state, subject revision, observed time, payload digest, provenance and writer identity, using a superseding row for later observations

#### Scenario: untrusted writers cannot publish gate evidence

- **WHEN** a caller without repository evidence-writer authorization or evidence:write scope submits evidence
- **THEN** the server MUST reject the write and audit the attempt while ordinary authorized users MAY still manage non-authoritative external references according to repository policy

#### Scenario: authenticated writers can inspect only their own designation

- **WHEN** an authenticated repository reader queries its Evidence Writer status through the native evidence API
- **THEN** the server SHALL return only that identity's active assignment for the exact repository, SHALL NOT grant or mutate an assignment, and SHALL keep the result independent from PAT scopes while evidence publication continues to re-evaluate all authorization gates

#### Scenario: reads respect tenant and field visibility

- **WHEN** the SPA, board or CLI lists references or evidence
- **THEN** the server MUST filter by repository visibility, caller permission and record visibility, return the normalized payload and provenance of a repository-visible evidence row to repository readers, omit a maintainers-visible row in full from non-maintainers, and MUST not leak credentials or hidden provider URLs or metadata

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/160#issuecomment-4926919280

### Requirement: Core review, verify and archive validate neutral external evidence fail closed

For a self-hosted issue profile whose code change lives externally, issue-spec MUST evaluate trusted structured evidence for the exact code revision, review findings, required checks, merge state and archive state, MUST compute the gate result itself, and MUST fail closed rather than accepting an arbitrary approval flag or unverified VERIFY text.

#### Scenario: verify passes only for the verified revision

- **WHEN** trusted evidence for the active code-change reference reports the current head revision, no open P0/P1 findings and all required checks passed and a done VERIFY records that revision
- **THEN** core verify MAY pass its external-code gate and MUST record the evidence identifiers and revision used

#### Scenario: missing or stale evidence blocks

- **WHEN** evidence is absent, expired, written by an untrusted identity, tied to another provider/change/revision, has pending or failed checks, or has open blocking review
- **THEN** review or verify MUST fail with an actionable reason and MUST NOT be bypassed by omitting a PR flag

#### Scenario: archive validates implementation merge and durable-spec merge

- **WHEN** an external bridge reports implementation or durable-spec change state
- **THEN** archive MUST require trusted merged evidence for the expected revision and change reference before closing proposal/design/implement issues, while GitHub mode retains its existing closure-block path

#### Scenario: line-level discussions remain externally linked but summarized canonically

- **WHEN** a bridge reports external review threads
- **THEN** issue-spec MUST preserve canonical PROCESS/SPEC/finding linkage and blocking severity/state in neutral evidence while the external platform remains the owner of line-level discussion content

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/160#issuecomment-4927054247

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
- **THEN** the server requires an active designated evidence writer with exact repository authority, derives immutable writer provenance, uses deterministic ingest identity to make replay idempotent, preserves supersession history, and rejects untrusted approval booleans or identity changes

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

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/160#issuecomment-4949554236

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
