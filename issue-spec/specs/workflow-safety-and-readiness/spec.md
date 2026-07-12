# workflow-safety-and-readiness

## Purpose

Define the long-lived behavior contract for this capability.

Proposal Issues:
- https://github.com/higress-group/issue-spec/issues/166

## Requirements

### Requirement: Status, preflight and verify share one explainable gate evaluator

The CLI MUST evaluate locally knowable workflow readiness through one shared gate model used by status, preflight and final verify, and MUST report stable blocker codes, affected artifact identities and actionable remediation without weakening final fail-closed verification.

#### Scenario: unfinished artifacts are visible before final verify

- **WHEN** a required TASK or PROCESS is not done, a SPEC lacks done VERIFY coverage, or a required rationale is absent
- **THEN** status for the requested gate MUST be non-ready and identify every blocking artifact and the command family that can resolve it

#### Scenario: final verify remains authoritative

- **WHEN** remote checks, review findings or revision-bound evidence can change after preflight
- **THEN** preflight MUST label the result as a point-in-time forecast and final verify MUST re-evaluate all local and remote gates fail closed

#### Scenario: machine-readable diagnostics are stable

- **WHEN** an automation invokes status or preflight with JSON output
- **THEN** each blocker MUST include a stable code, gate, artifact URL when available, current state, expected state and remediation metadata

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/166#issuecomment-4951036229

### Requirement: Typed artifacts support atomic transitions and resumable reconciliation

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

The workflow runner MUST preflight authentication source, token safety, network reachability and required repository operations before dispatching delegated review or implementation work, and MUST provide agents only short-lived credentials scoped to the approved host, repository and operation set.

#### Scenario: failed preflight consumes no worker execution

- **WHEN** the token is absent, unsafe, expired, unreadable or lacks a required issue or pull-request operation
- **THEN** dispatch MUST stop before workspace preparation or worker allocation and return the concrete failed probe without exposing credential material

#### Scenario: successful preflight returns a capability matrix

- **WHEN** the requested agent operations are available
- **THEN** the runner MUST record a redacted result containing token source class, host, repository, permitted operations, expiry and network status

#### Scenario: credentials cannot escape delegated scope

- **WHEN** an agent attempts a different host, repository or mutation class
- **THEN** the credential broker MUST deny the operation and audit the attempted scope expansion

#### Scenario: GitHub strict mode requires a short-lived issuer

- **WHEN** a GitHub delegated workflow requests strict scoped execution
- **THEN** the runner MUST obtain a repository and operation scoped credential from an operator-configured issuer or fail before dispatch, and MUST NOT treat a mirrored long-lived host gh credential as compliant

#### Scenario: legacy GitHub credentials are migration-only

- **WHEN** an existing configuration explicitly enables legacy host gh credential mirroring
- **THEN** preflight MUST report the non-compliant source class and migration guidance and MUST NOT satisfy a strict delegated-work gate

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/166#issuecomment-4951036603

### Requirement: PR rationale policy is proportional to PROCESS execution class

The workflow MUST classify PROCESS artifacts by execution responsibility and MUST require inline code rationale only for processes that own code changes, while preserving durable PR, SPEC, review and verification traceability for every other process class.

#### Scenario: code-owning process requires an inline rationale

- **WHEN** a change-bearing PROCESS modifies a reviewable line in the implementation PR
- **THEN** final verify MUST require a rationale linked to that PROCESS, an active SPEC and a valid changed line

#### Scenario: review and verification use their native evidence

- **WHEN** a PROCESS performs review, security analysis, E2E verification or orchestration without owning a code line
- **THEN** final verify MUST accept the linked REVIEW, VERIFY, check, handoff or external evidence record and MUST NOT require an arbitrary inline code comment

#### Scenario: legacy processes remain safe

- **WHEN** an existing PROCESS has no explicit execution class
- **THEN** verification MUST apply a documented conservative default and expose a migration diagnostic without invalidating already accepted historical archives

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/166#issuecomment-4951036789
