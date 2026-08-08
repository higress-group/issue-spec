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

The runner-scoped runtime HOME and per-job scratch MUST be managed physical storage under the same storage owner lock and reconciliation engine that owns session runtimes and PROCESS pools, and reconciliation of these resources MUST stay idempotent across crashes between filesystem and metadata transitions. Storage diagnostics MUST distinguish protected identity/config bytes, rebuildable cache bytes, job scratch bytes, and unknown bytes for the runtime HOME. Storage pressure and cleanup MAY evict rebuildable cache directories and stale job scratch, but MUST NOT remove authentication or configuration state, ACPX index and session mappings, live agent state, active workspaces, or anything required to `/resume` an existing session. Known-expired resources MUST NOT receive a fresh orphan grace period, and an older runner binary reading the newer storage metadata MUST NOT corrupt or silently delete the runtime HOME or its records.

#### Scenario: storage report classifies runtime HOME bytes

- **WHEN** an operator runs the storage reconciliation report
- **THEN** the report SHALL show the runtime HOME's protected identity/config bytes, rebuildable cache bytes, job scratch bytes, and unknown bytes as distinct categories without exposing file contents or credentials

#### Scenario: cache cleanup preserves resume

- **WHEN** low disk space or an explicit cleanup evicts rebuildable caches from the runtime HOME
- **THEN** eviction SHALL be limited to cache-eligible and stale scratch paths and every existing public session SHALL remain resumable afterwards

#### Scenario: old binary rollback is safe

- **WHEN** an older runner binary that predates the shared runtime HOME runs against the upgraded root
- **THEN** the older binary SHALL NOT delete or corrupt the shared runtime HOME, its metadata records, or the storage sidecar's ownership evidence

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/439#issuecomment-5226066600

### Requirement: Legacy per-session runtime homes migrate with resume validation

Upgrading a runner root that contains legacy per-session runtime HOME directories MUST preserve exact `/resume` compatibility for existing sessions. Migration MUST run only while holding exclusive storage owner ownership with the old runner stopped, MUST back up control-plane state and the storage sidecar first, MUST create the runner-scoped runtime HOME with private permissions, MUST import verified ACPX and agent session state while keeping every existing ACPX record ID and agent session identity unchanged, and MUST NOT copy rebuildable cache directories from every legacy session. Legacy runtime homes MUST be preserved until a real resume through the new runtime HOME succeeds, and only then MAY the redundant legacy data be retired through the existing storage reconciler without granting known-expired resources a fresh orphan grace period. When legacy session homes contain conflicting global agent configuration or indexes, migration MUST stop with actionable diagnostics instead of silently choosing one. Migration, retry, and rollback MUST be idempotent, and rolling back to the previous binary MUST NOT corrupt or lose the sidecar or the new runtime HOME metadata.

#### Scenario: migrated sessions resume with original identity

- **WHEN** an operator migrates a root with existing per-session runtime homes and then resumes a migrated session
- **THEN** the session SHALL reconnect to its original ACPX record ID and agent session through the shared runtime HOME, and legacy homes SHALL be retired only after that validation succeeds

#### Scenario: conflicting legacy configuration fails closed

- **WHEN** two legacy session homes carry conflicting global agent configuration or index files
- **THEN** migration SHALL stop with a diagnostic naming the conflicting sessions and paths and SHALL NOT pick one silently

#### Scenario: migration retries are idempotent

- **WHEN** migration is interrupted and re-run, or re-run after it already completed
- **THEN** the repeated run SHALL converge to the same completed state without duplicating imports, corrupting state, or restarting orphan grace for known-expired resources

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/439#issuecomment-5226066701
