# workflow-safety-and-readiness

## Purpose

Define the long-lived safety and readiness contract for issue-native workflow execution: one explainable gate policy across forecast and authoritative verification, conflict-aware typed-artifact mutation and resumable reconciliation, fail-closed delegated-operation capability checks, and evidence requirements proportional to each PROCESS execution class.

Proposal Issues:
- https://github.com/higress-group/issue-spec/issues/166
- https://github.com/higress-group/issue-spec/issues/247

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

### Requirement: Review PROCESS evidence MUST be authored by an agent independent of the code author

The workflow MUST treat code review as mandatory for every active SPEC that has a valid change-bearing carrier, regardless of change size, and MUST require that a review PROCESS be authored by a different agent than the code author of the SPEC under review, judged by the `--agent` identity recorded on the review evidence. Agent-executed change-bearing work of every size MUST be implemented by a real non-coordinator native child; coordinator-inline implementation is prohibited. This requirement does not change non-change-bearing PROCESS execution or the direct one-file delivery path. The final gate MUST fail closed with a distinct diagnostic when a review PROCESS's reviewer `--agent` name matches a code author of the same SPEC. Author and reviewer identities MUST be joined per SPEC so that a reviewer who authored a *different* SPEC is not falsely flagged. This name-based check is a machine backstop for the prompt contract, not full provenance enforcement.

#### Scenario: self-review by the same agent name is blocked

- **WHEN** a review PROCESS covering an active SPEC records a reviewer `--agent` name that also authored a change-bearing rationale for that SPEC
- **THEN** the final gate MUST emit `process.review.author_conflict` and MUST NOT count that review as satisfied for the conflicted SPEC

#### Scenario: an independent reviewer of the same SPEC satisfies the node

- **WHEN** the SPEC under review is covered by a review PROCESS whose reviewer `--agent` name differs from every code author of that SPEC
- **THEN** the final gate MUST accept the review evidence, and a clean review of one SPEC MUST NOT rescue another SPEC that still has only a conflicted review

#### Scenario: authoring a different SPEC is not a conflict

- **WHEN** a reviewer `--agent` name matches a code author of a SPEC other than the one under review
- **THEN** the final gate MUST NOT flag the review as an author conflict for the SPEC under review

Source changes:
- https://github.com/higress-group/issue-spec/pull/232
- https://github.com/higress-group/issue-spec/issues/247
