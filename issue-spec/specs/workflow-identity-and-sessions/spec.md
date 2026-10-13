# workflow-identity-and-sessions

## Purpose

Define the long-lived behavior contract for portable workflow identity and runner resume handles in issue-spec workflows.

This durable spec is organized by stable capability surfaces rather than by the original proposal's individual SPEC comments. Future changes that extend workflow identity, generated workflow guidance, or runner resume metadata should update the relevant module below instead of appending a one-to-one copy of new proposal requirements.

Proposal Issues:
- https://github.com/higress-group/issue-spec/issues/20
- https://github.com/higress-group/issue-spec/issues/99
- https://github.com/higress-group/issue-spec/issues/247

## Requirements

### Requirement: artifact identity is portable and runtime-session-free

`Agent` is the logical workflow role or assigned label used by issue-spec artifacts. Writer commands MUST NOT read coding-agent-specific runtime session variables, require a runtime session id, or emit new artifact writer session metadata. Correctness and role separation MUST depend on logical Agent, exact revision, typed links, and trusted provider evidence instead.

Legacy `Agent Session ID` and `Agent Session Source` fields remain readable as inert compatibility data. They MUST NOT affect validation, diagnostics, gate decisions, or runner resume behavior. A deprecated `--agent-session` argument MAY remain accepted as a no-op so existing command invocations do not fail.

#### Scenario: new artifact uses portable logical identity

- **WHEN** any supported coding agent writes an issue-spec artifact with `--agent Review Agent`
- **THEN** the artifact SHALL record `Agent: Review Agent`
- **THEN** the writer SHALL NOT require or emit a runtime-specific session id or source

#### Scenario: coding-agent environment does not change output

- **WHEN** `CODEX_THREAD_ID` or another coding-agent-specific session variable is present
- **THEN** artifact rendering and gate decisions SHALL be identical to a process where that variable is absent

#### Scenario: legacy session metadata remains readable but inert

- **WHEN** an existing typed comment, rationale, finding, or reply contains legacy `Agent Session ID` or `Agent Session Source` fields
- **THEN** the artifact SHALL remain parseable without a missing, partial, or invalid-session diagnostic
- **THEN** those fields SHALL NOT satisfy, strengthen, or invalidate role identity or evidence requirements

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/20#issuecomment-4854703592
- https://github.com/higress-group/issue-spec/issues/20#issuecomment-4854795652
- https://github.com/higress-group/issue-spec/issues/20#issuecomment-4854795602

### Requirement: generated workflow guidance permits direct ownership

The current agent MUST be able to perform authorized planning, implementation and self-tests in the same session. Selecting Design, TASK or PROCESS MUST NOT require another writer or prohibit direct implementation. Delegation MUST require actual user or applicable project authorization. Projects MAY define a coordinator-only role and stage review through project-owned guidance. Optional managed PROCESS remains for concrete concurrency, isolation, ownership, recovery or dependency-integration needs, never file count alone.

#### Scenario: selected Design is implemented by its current owner

- **WHEN** the user authorizes implementation of a selected Design in the current session
- **THEN** generated guidance permits that agent to implement and self-test without creating another agent, PROCESS or worktree solely for role separation

#### Scenario: project coordinator policy stays project-owned

- **WHEN** a project requires a coordinator to dispatch separate owners
- **THEN** its project guidance defines that role without changing the shared issue-spec skill bodies

### Requirement: review and publication follow the requested workflow

Generated guidance MUST preserve revision-bound validation and actual writer/reviewer identity. It MUST distinguish self-tests from independent review and MUST NOT automatically dispatch a reviewer, create a PR/MR, publish rationale, push, approve or merge. Requested review follows project guidance, identifies concrete failures, routes repairs to their responsible owner and checks affected regressions. Publication requires actual authorization and provider policy; failures retain the rendered body for recovery. Historical REVIEW/VERIFY, role receipts, rationale evidence, finalization and Archive carriers remain audit-only.

#### Scenario: project reviews once at a stage boundary

- **WHEN** the project assigns implementation to a Proposal owner and formal stage review to its coordinator
- **THEN** the owner returns the exact candidate, self-test results and gaps for stage review without spawning a reviewer or self-approving

#### Scenario: a repaired candidate has new evidence

- **WHEN** a review finding is repaired
- **THEN** the affected checks run against the changed candidate, and reused evidence retains its original revision

### Requirement: bounded reads preserve normative intent

Generated guidance MUST read the complete selected Design and applicable effective answers before implementation, then expand selected TASKs, required SPECs and dependencies from compact indexes. It MUST route plain QUESTION decisions through resolution and choice-model decisions through ANSWER without manufacturing choices or decision authority.

#### Scenario: compact discovery leads to full selected requirements

- **WHEN** a TASK or required relation is omitted from a compact preview
- **THEN** the agent explicitly expands it before relying on that requirement

### Requirement: runner public session id is the public resume handle

In runner mode, `public_session_id` is the public, repository-scoped handle humans use with `/resume` to continue a coordinator session. Raw acpx record ids and provider session ids are not public runner resume handles.

Coordinator-authored proposal, design, implement, handoff, and update issue bodies or comments SHOULD disclose the available runner `public_session_id` and provide concrete `/resume <public-session-id> <answer or next instruction>` guidance when runner metadata is available.

#### Scenario: coordinator-authored issue body includes resume metadata

- **WHEN** an issue-spec runner dispatches a coordinator with `runner.public_session_id=s-abc123`
- **WHEN** that coordinator creates or updates a proposal, design, implement, handoff, or update issue body
- **THEN** the body SHALL include `s-abc123` as the public runner session id
- **THEN** the body SHALL include `/resume s-abc123 <answer or next instruction>` or equivalent resume guidance

#### Scenario: internal runtime ids are not resume handles

- **WHEN** a coordinator-authored body or related typed artifact also contains a raw acpx record id or provider session id
- **THEN** that metadata SHALL be treated as internal transport metadata
- **THEN** the body MUST NOT instruct humans to use those identifiers as the runner `/resume` id

#### Scenario: non-runner workflow omits public session metadata

- **WHEN** no runner public session id is available
- **THEN** coordinator-authored bodies MAY omit runner resume metadata
- **THEN** omission of `public_session_id` SHALL NOT fail non-runner workflows by default

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/20#issuecomment-4883004527
