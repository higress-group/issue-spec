# workflow-identity-and-sessions

## Purpose

Define the long-lived behavior contract for workflow identity, artifact writer provenance, session metadata, and runner resume handles in issue-spec workflows.

This durable spec is organized by stable capability surfaces rather than by the original proposal's individual SPEC comments. Future changes that extend workflow identity, agent provenance, generated workflow guidance, session diagnostics, or runner resume metadata should update the relevant module below instead of appending a one-to-one copy of new proposal requirements.

Proposal Issues:
- https://github.com/higress-group/issue-spec/issues/20

## Requirements

### Requirement: artifact identity model separates logical role from writer provenance

Issue-spec artifacts that record agent metadata MUST preserve the logical workflow role separately from artifact writer session provenance.

`Agent` is the logical role or workflow-assigned label. `Agent Session ID` and `Agent Session Source` are artifact writer provenance fields. Implementations MUST NOT overload `Agent` with runtime session ids.

Typed issue comments, typed comment JSON, PR rationale comments, review findings, finding replies, review sync artifacts, and verification artifacts MUST use a consistent metadata model for logical agent, artifact writer session id, and artifact writer session source.

#### Scenario: visible artifact metadata is distinct

- **WHEN** a writer renders an artifact with logical agent role `Review Agent`, artifact writer session id `codex-session-123`, and source `CODEX_THREAD_ID`
- **THEN** the rendered metadata SHALL contain `Agent: Review Agent`
- **THEN** the rendered metadata SHALL contain `Agent Session ID: codex-session-123`
- **THEN** the rendered metadata SHALL contain `Agent Session Source: CODEX_THREAD_ID`
- **THEN** the rendered metadata SHALL NOT place `codex-session-123` in the `Agent` field

#### Scenario: machine-readable artifact metadata is compatible

- **WHEN** a typed issue comment or PR artifact contains `Agent Session ID` and `Agent Session Source`
- **THEN** parsers and JSON output SHALL expose those values as additive optional fields
- **THEN** the existing logical `agent` field SHALL remain the logical role
- **THEN** existing artifacts without session provenance SHALL remain parseable and valid by default

#### Scenario: partial or future metadata is preserved

- **WHEN** an artifact contains only one of `Agent Session ID` or `Agent Session Source`
- **THEN** parsers SHALL preserve the present value for diagnostics
- **THEN** tooling SHALL NOT silently invent the missing value
- **WHEN** an artifact contains unknown future header fields
- **THEN** those fields SHALL NOT prevent parsing of core type, id, status, scope, links, and known session provenance fields

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/20#issuecomment-4854703592
- https://github.com/higress-group/issue-spec/issues/20#issuecomment-4854795652
- https://github.com/higress-group/issue-spec/issues/20#issuecomment-4854795602

### Requirement: writer commands resolve artifact session provenance once and stamp artifacts consistently

CLI commands that write issue-spec artifacts with agent metadata MUST resolve artifact writer session provenance once per command invocation and apply that resolved provenance consistently to newly rendered and pre-rendered artifact bodies.

Writer commands SHOULD accept an explicit session parameter such as `--agent-session` for non-Codex and coordinator-dispatched workflows. The resolver MUST prefer Codex runtime identity, currently `CODEX_THREAD_ID`, when that environment source is present and non-empty. When no Codex source is available, the resolver SHALL use the explicit session parameter as the artifact writer session id when supplied.

#### Scenario: Codex identity has precedence

- **WHEN** `CODEX_THREAD_ID=codex-session-123` is present
- **WHEN** a writer command receives `--agent-session supplied-session-456`
- **THEN** the artifact SHALL record `codex-session-123` as the artifact writer session id
- **THEN** the artifact SHALL record `CODEX_THREAD_ID` as the artifact writer session source
- **THEN** the artifact SHALL NOT record `supplied-session-456` as the resolved artifact writer session id

#### Scenario: explicit non-Codex fallback is visible

- **WHEN** no Codex session source is present
- **WHEN** a writer command receives `--agent-session supplied-session-456`
- **THEN** the artifact SHALL record `supplied-session-456` as the artifact writer session id
- **THEN** the artifact SHALL record the source as an explicit caller-provided parameter source

#### Scenario: pre-rendered bodies cannot bypass writer-owned provenance

- **WHEN** a writer command receives a body that already contains an issue-spec typed header
- **THEN** the command SHALL stamp or reconcile the resolved artifact writer session id and source after body normalization
- **THEN** conflicting pre-rendered session provenance SHALL be replaced by the resolved writer-owned provenance

#### Scenario: missing session input remains non-strict by default

- **WHEN** no Codex source is present
- **WHEN** the caller does not supply an explicit artifact writer session id
- **THEN** writer commands MAY omit session provenance or record an explicit missing state
- **THEN** default non-Codex workflows SHALL NOT fail solely because `CODEX_THREAD_ID` is absent

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/20#issuecomment-4854703553
- https://github.com/higress-group/issue-spec/issues/20#issuecomment-4854703562
- https://github.com/higress-group/issue-spec/issues/20#issuecomment-4854795623

### Requirement: diagnostics report session provenance problems without breaking legacy workflows by default

Artifact-reading commands SHOULD expose detectable missing, partial, invalid, or internally inconsistent artifact writer session provenance in both human-readable and JSON output.

Diagnostics MUST be warning-oriented by default for legacy and non-Codex workflows. Strict failure behavior, if supported, SHALL be explicitly enabled. Diagnostics MUST NOT compare historical artifact session ids to the current process `CODEX_THREAD_ID`; artifacts from older sessions are valid.

#### Scenario: issue artifact diagnostics

- **WHEN** `status`, `verify`, or an equivalent artifact-reading command reads a typed issue artifact that has logical `Agent` metadata but lacks `Agent Session ID` or `Agent Session Source`
- **THEN** the command SHOULD report a diagnostic for missing or partial artifact writer provenance
- **THEN** JSON output SHALL include a machine-readable diagnostic entry when JSON output is requested

#### Scenario: PR artifact diagnostics

- **WHEN** `review sync`, `verify --pr`, or an equivalent PR-aware command reads PR rationale, review finding, or finding reply artifacts
- **THEN** the command SHOULD parse and report logical agent, artifact writer session id, and artifact writer session source where available
- **THEN** missing or partial PR artifact provenance SHOULD be reported without making legacy PR comments invalid by default

#### Scenario: substantive review summaries ignore metadata

- **WHEN** review finding summaries are extracted from PR review comments
- **THEN** metadata lines such as `Agent`, `Agent Session ID`, and `Agent Session Source` SHALL NOT be selected as the substantive finding summary

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/20#issuecomment-4854703552
- https://github.com/higress-group/issue-spec/issues/20#issuecomment-4854795688

### Requirement: generated workflow guidance teaches dispatch ids and artifact writer provenance

Generated skills, prompts, and workflow templates MUST teach coordinators and subagents how logical roles, assigned subagent ids, and artifact writer session provenance differ.

Coordinators SHOULD assign each worker or review subagent an explicit subagent/session id when dispatching work. Subagents SHOULD pass that assigned id through supported issue-spec writer command parameters. Codex runtime identity may still override the supplied id as the resolved artifact writer session provenance.

#### Scenario: coordinator dispatch instructions

- **WHEN** generated issue-spec workflow instructions dispatch a worker or review subagent
- **THEN** the coordinator instruction SHALL include an assigned subagent/session id
- **THEN** the instruction SHALL say that the subagent passes that id to supported writer commands
- **THEN** the instruction SHALL distinguish the assigned id from the visible `Agent` logical role

#### Scenario: subagent writer instructions

- **WHEN** generated worker or review-agent instructions tell a subagent to write issue-spec artifacts
- **THEN** those instructions SHALL tell the subagent to pass its assigned session or subagent id through the supported CLI parameter
- **THEN** those instructions SHALL explain that Codex runtime identity may override the supplied id as artifact writer provenance
- **THEN** those instructions SHALL preserve default non-strict behavior when neither Codex identity nor explicit session id is available

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/20#issuecomment-4854703570
- https://github.com/higress-group/issue-spec/issues/20#issuecomment-4854795594

### Requirement: runner public session id is the public resume handle

In runner mode, `public_session_id` is the public, repository-scoped handle humans use with `/resume` to continue a coordinator session. Artifact writer provenance fields, Codex thread ids, raw acpx record ids, and provider session ids are not public runner resume handles.

Coordinator-authored proposal, design, implement, handoff, and update issue bodies or comments SHOULD disclose the available runner `public_session_id` and provide concrete `/resume <public-session-id> <answer or next instruction>` guidance when runner metadata is available.

#### Scenario: coordinator-authored issue body includes resume metadata

- **WHEN** an issue-spec runner dispatches a coordinator with `runner.public_session_id=s-abc123`
- **WHEN** that coordinator creates or updates a proposal, design, implement, handoff, or update issue body
- **THEN** the body SHALL include `s-abc123` as the public runner session id
- **THEN** the body SHALL include `/resume s-abc123 <answer or next instruction>` or equivalent resume guidance

#### Scenario: artifact writer provenance is not a resume handle

- **WHEN** a coordinator-authored body or related typed artifact also contains `Agent Session ID`, `Agent Session Source`, `CODEX_THREAD_ID`, raw acpx record id, or provider session id metadata
- **THEN** that metadata SHALL be treated as provenance or internal transport metadata
- **THEN** the body MUST NOT instruct humans to use those identifiers as the runner `/resume` id

#### Scenario: non-runner workflow omits public session metadata

- **WHEN** no runner public session id is available
- **THEN** coordinator-authored bodies MAY omit runner resume metadata
- **THEN** omission of `public_session_id` SHALL NOT fail non-runner workflows by default

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/20#issuecomment-4883004527
