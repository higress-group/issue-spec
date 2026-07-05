# runner-state-management

## Purpose

Define the long-lived behavior contract for the issue-spec runner's control-plane
state file (`state.json`): what it may persist, how it stays bounded as the runner
runs and processes more comments, how terminal records are retained without breaking
idempotency, and which compact metadata must survive compaction for resume and
reconciliation.

This durable spec is organized by stable capability surfaces rather than by the
original proposal's individual SPEC comments. Future changes that touch runner state
schema, ACPX metadata persistence, retention/compaction, sensitive-data handling, or
resume/reconciliation metadata should update the relevant module below instead of
appending a one-to-one copy of new proposal requirements.

Proposal Issues:
- https://github.com/higress-group/issue-spec/issues/75

Related history (audit trail, not part of the durable contract):
- Design: https://github.com/higress-group/issue-spec/issues/80
- Implement: https://github.com/higress-group/issue-spec/issues/84
- Implementation PR: https://github.com/higress-group/issue-spec/pull/95

## Requirements

### Requirement: runner state.json is a bounded control-plane store

Runner state persistence MUST be bounded by explicit size and count policies so
`state.json` remains a control-plane state file rather than an append-only history
store. State size MUST NOT grow linearly and without limit as the number of processed
jobs, sessions, cancellations, or status writebacks increases.

The runner MUST retain active queued, dispatched, running, and interrupted work needed
for current operation. Terminal records MUST be governed by retention limits or compact
tombstones.

#### Scenario: state does not grow linearly with completed work

- **WHEN** a runner processes many completed or failed jobs and sessions over a long
  runtime
- **THEN** `state.json` SHALL remain bounded by the configured retention and compaction
  limits
- **THEN** state size SHALL NOT grow without limit for every processed record

#### Scenario: active work is protected from compaction

- **WHEN** a job or public session is queued, dispatched, running, or interrupted
- **THEN** compaction SHALL preserve the state required to continue, reconcile, cancel,
  or report that active work
- **THEN** compaction SHALL NOT tombstone or prune non-terminal records

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/75#issuecomment-4882854655

### Requirement: ACPX metadata persisted in state is compact

Runner state MUST NOT persist unbounded ACPX raw metadata, conversation history, tool
output, command output, or flattened `messages.*` fields directly in `state.json`.

The runner SHALL persist only bounded, control-plane ACPX metadata: stable record id,
true/provider session id, last turn id, refreshed time, and the working directory
required for resume. ACPX history retained for troubleshooting SHALL live outside
`state.json` in the runner's separate bounded diagnostics subsystem, not embedded in
state records.

#### Scenario: large ACPX history is not embedded in state

- **WHEN** ACPX metadata contains long message history, tool results, stdout/stderr-like
  values, or command text
- **THEN** `state.json` SHALL store only the compact typed ACPX fields
- **THEN** `state.json` SHALL NOT embed the raw history, tool output, or flattened
  `messages.*` fields in job or public session records

#### Scenario: raw ACPX payload is not duplicated across records

- **WHEN** a terminal job and its public session both reference the same ACPX session
- **THEN** the runner SHALL NOT store duplicate raw ACPX payloads in both records

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/75#issuecomment-4882855819

### Requirement: terminal records use retention limits and idempotency-safe tombstones

Runner state MUST define retention behavior for terminal jobs, terminal public sessions,
completed cancellations, status writebacks, and idempotency indexes.

Retention MUST be governed by a configurable policy (for example a time-to-live and a
per-category count cap). After retention expires, the runner SHALL either remove full
terminal records or replace them with compact tombstones. A tombstone MUST remain a real,
resolvable record that preserves the fields duplicate suppression and recovery read; a
tombstone MUST NOT be reduced to a bare idempotency index entry pointing at a missing
record. When a record is pruned, its idempotency index entry MUST be removed atomically,
and dangling index entries MUST be dropped so lookups return a clean miss rather than an
error.

#### Scenario: duplicate command arrives while the record is still retained

- **WHEN** a command with an existing idempotency key is observed while its record is
  still retained (as a full record or a tombstone)
- **THEN** the runner SHALL recognize it as a duplicate
- **THEN** the runner SHALL NOT enqueue a second job for the same command

#### Scenario: old terminal detail expires

- **WHEN** a terminal job, session, cancellation, or writeback is older than the
  configured retention policy or exceeds the per-category count cap
- **THEN** compaction SHALL prune verbose details while preserving any required compact
  idempotency or writeback tombstone
- **THEN** the record and its idempotency index entry SHALL be removed together

#### Scenario: idempotency index never dangles

- **WHEN** a terminal record is pruned from state
- **THEN** no idempotency index entry SHALL continue to point at the removed record
- **THEN** a subsequent duplicate lookup SHALL return a clean miss and SHALL NOT error

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/75#issuecomment-4882857412

### Requirement: compaction is automatic, safe, and observable

The runner MUST keep state bounded without requiring operator intervention: compaction
SHALL run automatically as part of normal state persistence. Compaction MUST be safe for
existing oversized state files, migrating them without manual edits by dropping or
bounding unbounded payloads while preserving the identifiers required for current
operation.

Compaction MUST be able to report what it changed, grouped by record category (for
example records tombstoned, records pruned, and dangling indexes dropped), so its effect
is observable. The runner SHOULD expose this report for operator preview.

#### Scenario: existing state contains large `acpx.raw`

- **WHEN** an existing `state.json` contains large `jobs[].acpx.raw` or
  `public_sessions[].acpx.raw` payloads
- **THEN** loading and re-saving that state SHALL remove or bound those payloads while
  preserving the identifiers required for current runner operation
- **THEN** the working directory previously carried in `acpx.raw` SHALL be preserved on
  the compact typed field so resume continuity survives migration
- **THEN** no manual edit of the operator's state file SHALL be required

#### Scenario: compaction is observable

- **WHEN** compaction runs
- **THEN** it SHALL be able to report the affected record categories and counts without
  requiring the raw payloads it removed

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/75#issuecomment-4882858585

### Requirement: state persistence excludes sensitive and unbounded raw values

Runner state MUST NOT persist tokens, auth material, secret-like environment values, or
unredacted sensitive command output as part of state compaction, metadata preservation,
or any retained reference.

Because raw ACPX history and command/tool output are not persisted in `state.json`, the
control-plane state file MUST NOT become a channel for host-local secrets. Any diagnostic
data retained for troubleshooting SHALL live in the runner's private, bounded diagnostics
subsystem and SHALL NOT be disclosed through public GitHub comments.

#### Scenario: ACPX metadata includes secret-like values

- **WHEN** ACPX metadata or command output contains token-like strings, auth config
  content, or secret-like environment values
- **THEN** state persistence and compaction SHALL NOT write those values to `state.json`

#### Scenario: sensitive data is not published

- **WHEN** troubleshooting data is retained outside state
- **THEN** it SHALL remain private to local operators
- **THEN** it SHALL NOT be exposed through GitHub status writebacks or comments

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/75#issuecomment-4882859568

### Requirement: compaction preserves resume and reconciliation metadata

State compaction MUST preserve the compact fields required for `/resume`, cancellation,
restart reconciliation, and status writeback recovery.

At minimum, the runner SHALL preserve public session id, stable ACPX record id, last turn
id when available, status writeback identity and comment id, workspace routing metadata,
and the working directory (`cwd`) required to resume safely. Status writeback records MUST
retain the fields needed to edit the same status comment while their job is active, and
MAY be reduced to a compact form once the job is terminal, keeping the comment id for
idempotent re-answer.

#### Scenario: compacted session is resumed

- **WHEN** a public session has been compacted and a later `/resume` command targets that
  session
- **THEN** the runner SHALL retain enough compact metadata to locate the session, resolve
  the workspace, call ACPX with the stable record id, and continue
- **THEN** resume SHALL NOT require the removed raw history

#### Scenario: runner restarts during active work

- **WHEN** the runner restarts with queued, dispatched, running, or interrupted jobs in
  state
- **THEN** reconciliation SHALL still have the compact job and session identifiers needed
  to refresh ACPX state and write an accurate status update

#### Scenario: status writeback recovery survives compaction

- **WHEN** a status writeback's job is still active
- **THEN** compaction SHALL keep the writeback fields needed to edit the same status
  comment
- **WHEN** the writeback's job has reached a terminal state
- **THEN** compaction MAY reduce the writeback to a compact form that still preserves its
  idempotency key and comment id

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/75#issuecomment-4882860225
