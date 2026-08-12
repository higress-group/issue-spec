# runner-shared-runtime-home

## Purpose

Define the long-lived behavior contract for this capability.

Proposal Issues:
- https://github.com/higress-group/issue-spec/issues/439

## Requirements

### Requirement: Runner sessions share one runner-scoped isolated runtime HOME

The runner MUST give every public session dispatched for one runner scope the same persistent isolated runtime HOME, where the scope identity is the hostname, backend profile realm, canonical repository identity, and runner identity. The runtime HOME MUST never be the operator's real HOME, MUST be created runner-owned with private (0700) permissions strictly confined below the resolved runner workspace root, and MUST fail closed when the resolved path escapes the root, crosses a symlink, or collides with another root entry. Repository content MUST NOT be able to select or redirect the runtime HOME path. Different repositories, runner identities, or backend profile realms MUST NOT share a writable runtime HOME. Session routing MUST continue to use only the exact stored public session ID, ACPX record ID, ACP session ID, and repository binding from durable runner state; the runner MUST NOT infer the most recent or active conversation from runtime HOME contents.

#### Scenario: concurrent sessions share one runtime HOME

- **WHEN** two public sessions of the same runner scope dispatch jobs, concurrently or sequentially
- **THEN** both jobs SHALL receive the same runner-scoped runtime HOME while each dispatch keeps its own exact public session ID, ACPX record ID, and managed workspace, and alternating `/resume` operations before and after a runner restart SHALL reconnect each session to its original agent identity

#### Scenario: different scope cannot reuse the runtime HOME

- **WHEN** a job is dispatched for a different repository, runner identity, or backend profile realm
- **THEN** the runner SHALL derive a distinct runtime HOME for that scope and SHALL NOT bind another scope's runtime HOME into the sandbox

#### Scenario: unsafe path resolution fails closed

- **WHEN** the derived runtime HOME path escapes the workspace root, resolves through a symlink, or overlaps a protected root entry
- **THEN** dispatch SHALL fail with an actionable diagnostic instead of creating or using the unsafe path

#### Scenario: operator HOME stays untouched

- **WHEN** any runner job executes in the default sandbox
- **THEN** the operator's real HOME SHALL remain unavailable for general writes and SHALL never be selected as the shared runtime HOME

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/439#issuecomment-5226066432

### Requirement: Each dispatched job receives unique disposable scratch directories

Every dispatched runner job MUST receive its own private scratch directories for `TMPDIR`, `GOTMPDIR`, `XDG_DATA_HOME`, and `XDG_STATE_HOME`, unique per runner job ID and never shared between jobs. Job scratch MUST be removed after the job reaches a terminal state, and scratch left behind by a crashed runner MUST be recovered by a conservative, idempotent reconciliation that never deletes scratch belonging to an active job. Job-private credential material MUST follow the job scratch lifecycle and MUST NOT persist in the shared runtime HOME.

#### Scenario: scratch is unique and writable

- **WHEN** two jobs run concurrently in the same runner scope
- **THEN** each job SHALL observe its own sandbox-writable `TMPDIR`, `GOTMPDIR`, `XDG_DATA_HOME`, and `XDG_STATE_HOME` paths that the other job does not receive

#### Scenario: scratch is removed at terminal completion

- **WHEN** a job completes, fails, or is cancelled
- **THEN** the runner SHALL remove that job's scratch directories without touching the shared runtime HOME or another job's scratch

#### Scenario: crash recovery is conservative and idempotent

- **WHEN** the runner restarts after a crash that left job scratch directories behind
- **THEN** reconciliation SHALL remove only scratch whose owning job is terminal or unknown to durable state, SHALL keep scratch of active jobs, and repeating the pass SHALL be a no-op

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/439#issuecomment-5226066531

### Requirement: Shared runtime HOME storage is owner-locked, accounted, and cache-only evictable

The runner-scoped runtime HOME and per-job scratch MUST be managed physical storage tracked as records in the existing runner storage sidecar under the same owner lock and reconciliation engine that owns session runtimes and PROCESS pools; no parallel lifecycle engine or separate metadata authority is introduced. Reconciliation of these resources MUST stay idempotent across crashes between filesystem and metadata transitions. Storage diagnostics MUST distinguish protected identity/config bytes, rebuildable cache bytes, job scratch bytes, and unknown bytes for the runtime HOME. Storage pressure and cleanup MAY evict rebuildable cache directories and stale job scratch, but MUST NOT remove authentication or configuration state, ACPX index and session mappings, live agent state, active workspaces, or anything required to `/resume` an existing session on the current layout. Known-expired resources MUST NOT receive a fresh orphan grace period.

#### Scenario: storage report classifies runtime HOME bytes

- **WHEN** an operator runs the storage reconciliation report
- **THEN** the report SHALL show the runtime HOME's protected identity/config bytes, rebuildable cache bytes, job scratch bytes, and unknown bytes as distinct categories without exposing file contents or credentials

#### Scenario: cache cleanup preserves resume

- **WHEN** low disk space or an explicit cleanup evicts rebuildable caches from the runtime HOME
- **THEN** eviction SHALL be limited to cache-eligible and stale scratch paths and every existing public session on the current layout SHALL remain resumable afterwards

#### Scenario: single storage authority

- **WHEN** the runner registers, accounts, reconciles, or evicts shared runtime HOME or job scratch resources
- **THEN** those operations SHALL use the existing storage sidecar records and owner lock and SHALL NOT create a second lifecycle engine or metadata file with independent deletion authority

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/439#issuecomment-5226066600

### Requirement: Shared runtime HOME is a clean cutover for new runner roots

The runner-scoped isolated runtime HOME MUST be enabled as a clean operational cutover for newly started runner roots and deployments. The runner MUST NOT migrate, import, or resume legacy per-session runtime HOME state, and operator documentation MUST provide an explicit cutover procedure: stop the old runner, preserve or archive the old root if desired, start the new binary against a fresh runner root, verify new-session concurrency and cache reuse, then remove or archive the old root separately. The new binary MUST NOT modify, import, or delete an old runner root; archiving or deleting it is always a separate explicit operator action. Sessions created before the cutover are not resumable afterwards and are drained or archived by the operator.

#### Scenario: fresh root initializes the shared layout

- **WHEN** a new binary starts against a fresh runner root and dispatches new sessions
- **THEN** the runner SHALL create the runner-scoped isolated runtime HOME with private permissions and SHALL serve new sessions and `/resume` entirely within the shared layout, including across runner restarts

#### Scenario: old root is untouched

- **WHEN** the new binary runs against a fresh root while an old runner root still exists elsewhere on the host
- **THEN** the new binary SHALL NOT read for import, modify, or delete the old root, and removing or archiving it SHALL remain a separate explicit operator action

#### Scenario: cutover is documented

- **WHEN** an operator adopts the shared runtime HOME
- **THEN** runner documentation SHALL describe the breaking cutover, the drain-or-archive expectation for pre-cutover sessions, and the step-by-step cutover procedure

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/439#issuecomment-5226324113
