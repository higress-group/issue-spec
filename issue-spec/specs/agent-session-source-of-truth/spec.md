# agent-session-source-of-truth

## Purpose

记录 issue-native workflow CLI 的长期行为契约。

Proposal Issues:
- https://github.com/higress-group/issue-spec/issues/20

## Requirements

### Requirement: Codex source-of-truth precedence

The CLI MUST detect when it is executing under Codex and MUST use the unique Codex session source, currently `CODEX_THREAD_ID`, as the resolved session id before considering any caller-supplied agent/session id parameter.

The CLI MUST preserve the caller-supplied logical role or logical agent label separately from the resolved session id when both are available.

#### Scenario: Codex session id overrides supplied id

- **WHEN** `CODEX_THREAD_ID=codex-session-123` is present and a caller supplies `--agent-id worker-a` or an equivalent agent/session parameter
- **THEN** the CLI MUST record `codex-session-123` as the resolved session id
- **THEN** the CLI MUST record the session source as `CODEX_THREAD_ID`
- **THEN** the CLI MUST NOT record `worker-a` as the resolved session id

#### Scenario: Codex session id is used for coordinator commands

- **WHEN** a coordinator runs an issue-spec command inside Codex with `CODEX_THREAD_ID=coordinator-session-456`
- **THEN** the CLI MUST use `coordinator-session-456` as the resolved session id for the artifact it writes
- **THEN** the artifact MUST remain auditable without relying on an invented coordinator/session label

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/20#issuecomment-4854703553

### Requirement: explicit fallback parameter outside Codex

The CLI SHALL use an explicit caller-provided agent/session id as the resolved session id when Codex identity sources are unavailable.

The CLI MUST make the selected source visible as an explicit parameter source rather than reporting it as a Codex environment source.

#### Scenario: non-Codex worker supplies a session id

- **WHEN** no Codex session source such as `CODEX_THREAD_ID` is present and a caller supplies `--agent-id worker-session-789` or an equivalent agent/session parameter
- **THEN** the CLI SHALL record `worker-session-789` as the resolved session id
- **THEN** the CLI MUST record the session source as an explicit caller-provided parameter

#### Scenario: non-Codex command lacks a supplied id

- **WHEN** no Codex session source is present and the caller does not supply an agent/session id
- **THEN** commands that write agent metadata MUST either record an explicit unknown or missing session state or fail only when strict metadata validation is enabled
- **THEN** the behavior MUST be documented in CLI help and generated workflow instructions

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/20#issuecomment-4854703562

### Requirement: coordinator dispatch responsibility in generated workflows

Generated workflow skills and prompts MUST state that the main or coordinator agent is responsible for assigning each subagent its subagent id when dispatching work.

Generated workflow skills and prompts MUST state that each subagent SHALL pass the assigned subagent id to issue-spec CLI commands that support agent/session metadata.

#### Scenario: coordinator dispatches a worker

- **WHEN** generated issue-spec workflow instructions dispatch a worker or review subagent
- **THEN** the coordinator MUST include the assigned subagent id in the task assignment
- **THEN** the assignment MUST state that the subagent passes that id to the CLI

#### Scenario: subagent records an artifact

- **WHEN** a subagent receives an assigned subagent id and runs an issue-spec command that writes a typed artifact
- **THEN** the subagent SHALL pass the assigned id through the supported CLI parameter
- **THEN** the CLI identity resolution rules MUST still decide whether Codex environment identity or the supplied id is the resolved session id

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/20#issuecomment-4854703570

### Requirement: metadata consistency across typed artifacts

Typed issue, typed comment, PR rationale, review, and verification artifacts MUST preserve logical agent role, resolved session id, and session source in a consistent machine-readable form.

The artifact format MUST allow humans and tools to distinguish the logical role assigned by the workflow from the runtime session id selected by CLI identity resolution.

#### Scenario: typed comment stores role and session metadata

- **WHEN** the CLI creates or updates a typed comment with logical role `review-worker` and resolved session id `codex-session-abc`
- **THEN** the comment MUST preserve `review-worker` as logical agent role metadata
- **THEN** the comment MUST preserve `codex-session-abc` as resolved session id metadata
- **THEN** the comment MUST preserve `CODEX_THREAD_ID` or the explicit parameter source as session source metadata

#### Scenario: PR artifact preserves the same metadata model

- **WHEN** the CLI writes PR rationale or review finding metadata
- **THEN** the PR artifact MUST preserve the same logical role, resolved session id, and source fields used by issue and comment artifacts
- **THEN** parsers and status output MUST report those fields consistently across artifact types

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/20#issuecomment-4854703592

### Requirement: verification and diagnostics for session metadata

Status, verify, auth, or diagnostic commands SHALL expose missing or mismatched session metadata when available artifact data makes the condition detectable.

Diagnostics MUST NOT block non-Codex workflows solely because Codex-specific environment sources are absent unless strict metadata validation is explicitly configured.

#### Scenario: verify reports missing metadata

- **WHEN** `issue-spec verify` or an equivalent status command reads a typed artifact that lacks resolved session id or session source metadata
- **THEN** the command SHALL report the missing metadata in human-readable output
- **THEN** JSON output SHALL include a machine-readable diagnostic for the missing metadata

#### Scenario: non-Codex workflow remains valid by default

- **WHEN** no Codex session source is present and artifacts use explicit caller-provided session ids
- **THEN** diagnostics MUST NOT report the absence of `CODEX_THREAD_ID` as an error by itself
- **THEN** diagnostics SHALL only fail the workflow when configured strict validation requires that failure

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/20#issuecomment-4854703552

### Requirement: typed comment model and rendering preserve role and session separately

The typed comment model MUST represent logical agent role separately from resolved agent session id and agent session source.

`BodyOptions` MUST provide independent fields for logical agent role, agent session id, and agent session source. `RenderHeader` MUST render `Agent`, `Agent Session ID`, and `Agent Session Source` as separate visible header fields when session metadata is available.

#### Scenario: renderer outputs separate fields

- **WHEN** a writer renders a SPEC comment with logical agent role `Proposal Coordinator`, agent session id `019f1d93-e396-7153-b1fe-f1e54202134e`, and source `CODEX_THREAD_ID`
- **THEN** the header MUST contain `Agent: Proposal Coordinator`
- **THEN** the header MUST contain `Agent Session ID: 019f1d93-e396-7153-b1fe-f1e54202134e`
- **THEN** the header MUST contain `Agent Session Source: CODEX_THREAD_ID`

#### Scenario: renderer does not overload Agent

- **WHEN** the resolved session id differs from the logical agent role
- **THEN** `RenderHeader` MUST NOT place the session id in the `Agent` field
- **THEN** the session id MUST be emitted only as session metadata

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/20#issuecomment-4854795652

### Requirement: parser and status JSON expose session metadata compatibly

`ParseTypedComment` MUST parse `Agent Session ID` and `Agent Session Source` when those fields are present.

Comment list, status, or related JSON output SHALL expose parsed session id and session source fields without breaking existing typed artifacts that lack those fields.

#### Scenario: parser reads new fields

- **WHEN** a typed comment body contains `Agent Session ID: session-123` and `Agent Session Source: CODEX_THREAD_ID`
- **THEN** `ParseTypedComment` MUST return `session-123` as the parsed agent session id
- **THEN** `ParseTypedComment` MUST return `CODEX_THREAD_ID` as the parsed agent session source

#### Scenario: old artifacts remain parseable

- **WHEN** a typed comment contains the existing marker, `Agent`, `Type`, `ID`, `Status`, `Scope`, and `Links` fields but no session metadata
- **THEN** `comment list --json` MUST still parse the artifact successfully
- **THEN** JSON output SHALL represent the missing session fields as empty, null, or omitted according to the chosen compatibility contract

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/20#issuecomment-4854795602

### Requirement: CLI writer commands accept explicit session parameter and resolve Codex precedence

CLI commands that write issue-spec artifacts MUST accept an explicit session parameter, such as `--agent-session`, for non-Codex and coordinator-dispatched workflows.

The session resolver MUST prefer `CODEX_THREAD_ID` when running under Codex and MUST fall back to the explicit session parameter when no Codex session source is available.

#### Scenario: Codex source wins over explicit parameter

- **WHEN** `CODEX_THREAD_ID=codex-session-123` is present and a writer command receives `--agent-session supplied-session-456`
- **THEN** the artifact MUST record `codex-session-123` as the agent session id
- **THEN** the artifact MUST record `CODEX_THREAD_ID` as the agent session source
- **THEN** the artifact MUST NOT record `supplied-session-456` as the resolved session id

#### Scenario: non-Codex parameter fallback

- **WHEN** no Codex session source is present and a writer command receives `--agent-session supplied-session-456`
- **THEN** the artifact SHALL record `supplied-session-456` as the agent session id
- **THEN** the artifact SHALL record the session source as the explicit CLI parameter

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/20#issuecomment-4854795623

### Requirement: generated workflow templates teach role versus session separation

Generated skills, prompts, and workflow templates MUST state that `Agent` is a logical role or workflow-assigned label and that session id/source are independent runtime metadata.

Generated coordinator instructions MUST require the coordinator to provide each subagent with its assigned subagent id, and generated subagent instructions MUST require the subagent to pass that id through the supported CLI parameter.

#### Scenario: generated coordinator dispatch instructions

- **WHEN** `issue-spec init` generates coordinator workflow instructions
- **THEN** those instructions MUST tell the coordinator to include a subagent id in each subagent assignment
- **THEN** those instructions MUST distinguish the subagent id from the logical `Agent` role shown in typed comment headers

#### Scenario: generated subagent writer instructions

- **WHEN** generated worker or review-agent instructions tell a subagent to write issue-spec artifacts
- **THEN** those instructions MUST tell the subagent to pass its assigned session or subagent id to the CLI
- **THEN** those instructions SHALL explain that Codex runtime identity may override the supplied id as the resolved session id

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/20#issuecomment-4854795594

### Requirement: diagnostics report missing or mismatched session metadata without default breakage

Status, verify, auth, or diagnostic commands SHOULD report missing or mismatched agent session metadata when the available artifact data makes the condition detectable.

Diagnostics MUST NOT fail valid non-Codex workflows by default solely because Codex-specific environment variables are absent. Strict failure behavior SHALL require explicit configuration.

#### Scenario: missing session metadata is reported

- **WHEN** a diagnostic command reads a typed artifact that has logical `Agent` metadata but lacks agent session id or source
- **THEN** the command SHOULD report a warning or diagnostic entry for the missing session metadata
- **THEN** JSON output SHALL include a machine-readable diagnostic when JSON output is requested

#### Scenario: strict validation controls failures

- **WHEN** strict session metadata validation is disabled and a non-Codex artifact uses explicit session metadata or lacks Codex-specific metadata
- **THEN** diagnostics MUST NOT fail solely because `CODEX_THREAD_ID` is absent
- **THEN** diagnostics SHALL fail only when strict validation is enabled and the configured rule is violated

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/20#issuecomment-4854795688

### Requirement: runner public session disclosure in coordinator-authored issue bodies

When runner metadata is available, coordinator-authored proposal, design, implement, handoff, and update issue bodies SHALL disclose the runner `public_session_id` that humans can use to resume the coordinator.

Coordinator-authored bodies SHALL distinguish `public_session_id` from artifact writer session metadata. `Agent Session ID`, `CODEX_THREAD_ID`, raw acpx record ids, and provider session ids MUST NOT be presented as `/resume` handles.

#### Scenario: coordinator updates an issue body during a runner session

- **WHEN** an issue-spec runner dispatches a coordinator with `runner.public_session_id=s-abc123`
- **WHEN** that coordinator creates or updates a proposal, design, implement, handoff, or update issue body
- **THEN** the body SHALL include `s-abc123` as the public runner session id
- **THEN** the body SHALL include a concrete `/resume s-abc123 <answer or next instruction>` template or equivalent resume guidance

#### Scenario: artifact writer metadata is present too

- **WHEN** the same coordinator-authored body or related typed artifact also contains `Agent Session ID` or `Agent Session Source`
- **THEN** that metadata SHALL be treated as artifact writer provenance
- **THEN** the body MUST NOT tell humans to use that artifact writer session id as the runner `/resume` id

#### Scenario: non-runner workflow

- **WHEN** no runner public session id is available
- **THEN** the coordinator-authored body MAY omit runner resume metadata
- **THEN** omission of `public_session_id` MUST NOT fail non-runner workflows by default

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/20#issuecomment-4883004527
