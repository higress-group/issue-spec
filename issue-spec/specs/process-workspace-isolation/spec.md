# process-workspace-isolation

## Purpose

Define the long-lived behavior contract for safely executing a PROCESS DAG from
isolated Git workspaces. The coordinator retains one integration checkout;
delegated change-bearing native children receive leased writable worktrees,
inline independent nodes execute in that integration checkout, and review and
verification use exact snapshots. The contract covers explicit revision
binding, ownership-aware integration, crash-safe lifecycle reconciliation, and
the runner boundary that keeps one coordinator session distinct from its child
workspaces.

Proposal Issues:
- https://github.com/higress-group/issue-spec/issues/175

## Requirements

### Requirement: PROCESS execution classes determine coordinator-to-child workspace isolation

The implement coordinator MUST retain its integration checkout as the coordinator execution root, MUST allocate a unique writable Git worktree and branch to every delegated managed change-bearing PROCESS executed by a native child agent, MUST execute an inline independent change-bearing PROCESS directly in the integration checkout without allocating a PROCESS worktree, MUST give review and verification child agents an immutable revision snapshot, and MUST avoid allocating a writable checkout to orchestration-only work that does not need repository files.

#### Scenario: Parallel change-bearing child agents are physically separated

- **WHEN** two delegated managed change-bearing PROCESS nodes are eligible to run in parallel from the same integrated base revision
- **THEN** the coordinator MUST assign different writable worktree paths and different branches to their native child agents while retaining the disjoint write-ownership gate

#### Scenario: Serial successor receives a fresh child workspace

- **WHEN** delegated managed PROCESS-B depends on completed change-bearing PROCESS-A
- **THEN** the coordinator MUST prepare a new PROCESS-B worktree from the revision where PROCESS-A was integrated and MUST NOT reuse PROCESS-A's dirty or worker-local checkout

#### Scenario: Inline serial successor retains a PROCESS boundary

- **WHEN** inline independent PROCESS-B depends on completed PROCESS-A
- **THEN** the coordinator MUST execute PROCESS-B in the integration checkout without preparing or reusing a child worktree and MUST retain distinct PROCESS state plus the bounded handoff from PROCESS-A

#### Scenario: Review child evaluates an immutable revision

- **WHEN** a review or verification PROCESS begins
- **THEN** the coordinator MUST assign its native child agent a detached or otherwise immutable snapshot of the exact integration or pull-request revision under evaluation

#### Scenario: Coordinator does not consume a worker workspace

- **WHEN** the coordinator prepares or dispatches a delegated managed PROCESS in the change-bearing, review, or verification class
- **THEN** the coordinator MUST keep its own cwd and integration checkout unchanged and MUST pass the allocated workspace identity to the native child-agent execution instead

#### Scenario: Orchestration needs no writable checkout

- **WHEN** a PROCESS only schedules native child agents, updates gates, or links issue-native artifacts
- **THEN** the coordinator MAY execute it without allocating a writable Git worktree

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/175#issuecomment-4951798104

### Requirement: Coordinator integration is revision-bound and dependency-ordered

The coordinator MUST own the integration checkout, MUST prepare each delegated managed worker from an explicit base revision, MUST integrate delegated worker commits in PROCESS dependency order, and MUST permit a delegated managed change-bearing PROCESS to become done only after its commit is integrated and required integration checks pass. For an inline independent change-bearing PROCESS, the coordinator MAY author the commit directly in the integration checkout, but MUST preserve the PROCESS's declared write ownership, rationale, test evidence, serial handoff boundary, and mandatory independent review obligations before final verification.

#### Scenario: Worker returns a bounded commit

- **WHEN** a delegated managed change-bearing worker completes its assigned implementation
- **THEN** it SHALL return a signed-off commit SHA, test evidence and handoff summary and SHALL NOT require the coordinator to recover changes from a shared dirty directory

#### Scenario: Local commit is not yet done evidence

- **WHEN** a delegated managed worker has committed successfully but the coordinator has not integrated that commit
- **THEN** the PROCESS MUST remain non-done and status MUST distinguish worker-complete from integrated

#### Scenario: Inline independent completion has no integration phase

- **WHEN** the coordinator completes an inline independent change-bearing PROCESS in the integration checkout
- **THEN** the coordinator MUST NOT run workspace complete or integrate for that PROCESS and MUST record its commit, tests, rationale and, when it is a serial predecessor, its bounded handoff before routing its active SPEC to an independent review agent

#### Scenario: Dependency changed after dispatch planning

- **WHEN** the intended base revision no longer contains the integrated results required by the PROCESS dependencies
- **THEN** workspace preparation or integration MUST fail as stale and require a new base or explicit reconciliation

#### Scenario: Parallel commits conflict during integration

- **WHEN** two independently valid worker commits cannot be applied cleanly to the integration branch
- **THEN** the coordinator MUST keep the affected PROCESS nodes non-done and route the conflict to a declared integration or repair PROCESS

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/175#issuecomment-4951798445

### Requirement: Workspace and commit scope enforce declared write ownership

Workspace dispatch and integration MUST enforce PROCESS write ownership, MUST reject worker commits containing unrelated files or unowned shared outputs, and MUST preserve all pre-existing user changes and unowned worktrees.

#### Scenario: Active write ownership overlaps

- **WHEN** a proposed writable PROCESS overlaps an active PROCESS on a file, generated output, package chokepoint or shared configuration surface without an integration protocol
- **THEN** dispatch MUST serialize the nodes or require an explicit integration owner instead of allocating concurrent writable worktrees

#### Scenario: Worker commit includes an unrelated file

- **WHEN** the diff of a worker commit contains a path outside its declared ownership and approved shared touchpoints
- **THEN** integration MUST fail with the unexpected paths and MUST NOT cherry-pick or mark the PROCESS done

#### Scenario: Several workers affect generated output

- **WHEN** multiple source PROCESS nodes would regenerate the same checked-in artifact
- **THEN** the plan MUST assign generation to one dedicated generator or integration PROCESS rather than accepting competing generated commits

#### Scenario: Main checkout contains user work

- **WHEN** the coordinator or lifecycle manager observes pre-existing user changes in another checkout
- **THEN** it MUST leave those changes untouched and MUST NOT clean, reset, stage or include them in a PROCESS commit

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/175#issuecomment-4951798769

### Requirement: Workspace lifecycle is observable, recoverable and session-resource-aware

Issue-spec MUST track portable workspace lifecycle metadata for each coordinator-allocated PROCESS, MUST reconcile interrupted preparation, integration and cleanup without deleting unowned resources, MUST give a runner-managed coordinator access only to its integration checkout and its session-scoped PROCESS workspace pool, and MUST namespace non-filesystem runtime resources that Git worktrees do not isolate.

#### Scenario: Workspace metadata is reported

- **WHEN** a PROCESS workspace is prepared or inspected by the coordinator
- **THEN** JSON diagnostics MUST expose a portable workspace id, execution class, base SHA, branch or detached revision, lifecycle state and result commit without persisting a machine-specific absolute path in remote durable artifacts

#### Scenario: Coordinator restarts during integration

- **WHEN** the coordinator stops after a child-agent commit exists but before integration state is finalized
- **THEN** a reconcile operation MUST re-observe the integration branch and PROCESS metadata and resume idempotently without duplicating commits or losing the worker result

#### Scenario: Cleanup sees an unowned worktree

- **WHEN** a branch or worktree is not associated with the active PROCESS workspace lease
- **THEN** automated cleanup MUST skip it and report the ownership mismatch instead of deleting it

#### Scenario: Runner exposes a session-scoped workspace pool

- **WHEN** a coordinator ACPX session is prepared in runner mode
- **THEN** the runner MUST keep the coordinator cwd at the session integration checkout and MUST expose only that checkout and that session's managed PROCESS workspace root to the coordinator sandbox

#### Scenario: Parallel child tests use external runtime resources

- **WHEN** parallel PROCESS child agents require ports, temporary directories, databases, containers, caches or sandbox names
- **THEN** coordinator child-agent dispatch MUST assign resource namespaces or serialize the conflicting resources and diagnostics MUST distinguish these conflicts from Git write ownership

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/175#issuecomment-4951799179

### Requirement: Runner preserves the coordinator boundary for delegated and inline PROCESS execution

A runner-managed ACPX coordinator MUST remain bound to its session integration checkout for its full session, MUST use the same issue-spec workspace lifecycle commands as a non-runner coordinator for delegated managed PROCESS nodes, MUST delegate those managed nodes through the agent runtime's native child-agent mechanism, MAY execute inline independent nodes directly in the session integration checkout without prepare/child/complete/integrate, and MUST NOT create a nested ACPX session or rebind the coordinator ACPX cwd or sandbox root to a PROCESS worktree.

#### Scenario: Runner coordinator starts and resumes at the integration root

- **WHEN** the runner creates or resumes the coordinator ACPX session
- **THEN** the ACPX cwd MUST remain the session clone that owns coordinator integration regardless of which PROCESS the coordinator plans or delegates

#### Scenario: Coordinator delegates a change-bearing PROCESS

- **WHEN** the runner-managed coordinator selects an eligible delegated managed change-bearing PROCESS
- **THEN** the coordinator MUST prepare its managed worktree through issue-spec and MUST pass the exact worktree identity to a native child agent without starting or resuming another ACPX coordinator record

#### Scenario: Native child returns a bounded handoff

- **WHEN** the native child agent finishes its assigned delegated managed PROCESS
- **THEN** it MUST return the result commit, tests and handoff through the agent runtime collaboration channel and the coordinator MUST run complete and integrate before the PROCESS becomes done

#### Scenario: Runner coordinator executes an inline independent PROCESS

- **WHEN** the runner-managed coordinator selects an eligible inline independent change-bearing PROCESS
- **THEN** the coordinator MUST implement, test and commit it in the session integration checkout, MUST NOT prepare a PROCESS worktree or dispatch a coding child or run complete/integrate, MUST preserve its distinct PROCESS state and, when it is a serial predecessor, its bounded handoff, MUST record change-bearing rationale, and MUST route every active SPEC with a valid change-bearing carrier to an independent review PROCESS owned by an agent other than the code author

#### Scenario: Exact PROCESS targeting cannot mutate coordinator execution

- **WHEN** a runner command, stored job or recovery path carries an exact PROCESS identity
- **THEN** the runner MUST reject or ignore any interpretation that would replace the top-level coordinator ACPX cwd or sandbox workspace with the PROCESS worktree

#### Scenario: Native children share a session security boundary

- **WHEN** multiple delegated managed native child agents run under one coordinator ACPX sandbox
- **THEN** issue-spec MUST guarantee distinct Git worktrees, branches, leases and commit-scope enforcement but MUST NOT claim a per-child operating-system sandbox unless the underlying agent runtime explicitly provides one

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/175#issuecomment-4956889860
