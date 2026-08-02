# workflow-safety-and-readiness

## Purpose

Define the long-lived safety and readiness contract for issue-native workflow execution: one explainable gate policy across forecast and authoritative verification, conflict-aware typed-artifact mutation and resumable reconciliation, fail-closed delegated-operation capability checks, and evidence requirements proportional to each PROCESS execution class.

Proposal Issues:
- https://github.com/higress-group/issue-spec/issues/166
- https://github.com/higress-group/issue-spec/issues/247

## Requirements

### Requirement: Typed artifacts support conflict-aware transitions and resumable reconciliation

The CLI MUST provide optimistic, link-preserving typed-artifact transitions and an idempotent reconciliation mode that applies a declared set of creates, updates, links and state changes without requiring callers to rewrite complete comment bodies.

#### Scenario: state transition preserves authored content

- **WHEN** a caller moves a TASK or PROCESS to a valid next state and optionally appends handoff evidence or a PR link
- **THEN** the CLI MUST update only the declared structured fields while preserving the marker, logical body, author metadata and all unrelated links

#### Scenario: concurrent mutation fails safely

- **WHEN** the remote artifact changed after the caller read its representation and the backend advertises conditional comment mutation
- **THEN** the transition MUST fail with a conflict and current representation identity instead of silently overwriting another writer

#### Scenario: unsupported conditional mutation is explicit

- **WHEN** a strict transition or reconcile plan targets a backend such as GitHub that does not provide conditional PATCH for issue comments
- **THEN** the command MUST fail before mutation unless the caller explicitly accepts non-atomic single-writer mode, and any accepted result MUST report that atomicity was not provided

#### Scenario: batch reconciliation resumes idempotently

- **WHEN** a network or rate-limit failure interrupts a multi-artifact plan
- **THEN** a rerun MUST report created, updated, unchanged, conflicted and pending operations and MUST continue without duplicate artifacts or lost backlinks

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/166#issuecomment-4951036404

### Requirement: Delegated work starts only after scoped agent capability preflight

The workflow runner MUST preflight authentication source, token safety, network reachability and required repository operations before dispatching delegated review or implementation work. A self-hosted runner MAY satisfy the gate with its origin-bound private profile PAT when that credential grants the requested repository and includes the required scopes; unrelated repository grants and additional scopes MUST NOT invalidate that credential. Strict GitHub delegated mode MUST still use short-lived credentials scoped to the approved host, repository and operation set; explicitly enabled legacy host credentials remain migration-only and MUST NOT satisfy that gate.

#### Scenario: failed preflight consumes no worker execution

- **WHEN** the token is absent, unsafe, expired, unreadable or lacks a required issue or pull-request operation
- **THEN** dispatch MUST stop before workspace preparation or worker allocation and return the concrete failed probe without exposing credential material

#### Scenario: successful preflight returns a capability matrix

- **WHEN** the requested agent operations are available
- **THEN** the runner MUST record a redacted result containing token source class, host, repository, permitted operations, expiry knowledge and network status; a private self-hosted profile file MAY report unknown expiry

#### Scenario: credential mode preserves its declared boundary

- **WHEN** an agent runs through a self-hosted profile PAT or a strict delegated credential
- **THEN** the runner MUST dispatch self-hosted jobs only for explicitly configured repositories and revalidate the current target, while the strict delegated broker MUST deny and audit attempts outside its approved host, repository or mutation class

#### Scenario: GitHub strict mode requires a short-lived issuer

- **WHEN** a GitHub delegated workflow requests strict scoped execution
- **THEN** the runner MUST obtain a repository and operation scoped credential from an operator-configured issuer or fail before dispatch, and MUST NOT treat a mirrored long-lived host gh credential as compliant

#### Scenario: legacy GitHub credentials are migration-only

- **WHEN** an existing configuration explicitly enables legacy host gh credential mirroring
- **THEN** preflight MUST report the non-compliant source class and migration guidance and MUST NOT satisfy a strict delegated-work gate

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/166#issuecomment-4951036603
