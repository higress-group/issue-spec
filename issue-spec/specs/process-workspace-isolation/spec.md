# process-workspace-isolation

## Purpose

Define the long-lived behavior contract for safely executing a PROCESS DAG from
isolated Git workspaces. The coordinator retains one integration checkout;
selected managed PROCESS children receive leased writable worktrees,
inline independent nodes execute in that integration checkout, and review and
verification use exact snapshots. The contract covers explicit revision
binding, ownership-aware integration, crash-safe lifecycle reconciliation, and
the runner boundary that keeps one coordinator session distinct from its child
workspaces.

Proposal Issues:
- https://github.com/higress-group/issue-spec/issues/175
- https://github.com/higress-group/issue-spec/issues/272

## Requirements

### Requirement: PROCESS execution classes determine coordinator-to-child workspace isolation

When optional delegated change-bearing PROCESS execution is selected, the coordinator MUST isolate writable workers by worktree, preserve external or human self-managed workspaces, and avoid allocating writable workspaces to read-only orchestration; exact-head human review MUST remain outside REVIEW or VERIFY PROCESS workspaces.

A bounded delegated implementation with selected Design/TASK or an explicit user delegation request MUST dispatch exactly one real non-coordinator native child in the selected implementation checkout without requiring PROCESS or managed workspace only when no managed-coordination need applies. When managed PROCESS is selected, every change-bearing work package MUST instead receive its own real non-coordinator child owner and distinct safe packages MAY run concurrently. The coordinator makes no code changes on either path. Direct coordinator edits are limited to a narrow direct-PR fast path with no selected Design/TASK and no delegation request. Read-only children require no workspace allocation.

#### Scenario: direct child needs no managed workspace

- **WHEN** selected Design/TASK or an explicit user request uses the unmanaged bounded path and its preconditions hold
- **THEN** the child uses the selected implementation checkout without a PROCESS lease and the coordinator waits for its exact result without writing code on that path

#### Scenario: optional workers remain isolated

- **WHEN** two delegated implementation PROCESS nodes may execute concurrently
- **THEN** they receive distinct writable worktrees and ownership enforcement while the later human handoff reports the exact integrated head

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/417#issuecomment-5165960908

### Requirement: Coordinator integration is revision-bound and dependency-ordered

For optional delegated PROCESS implementation, the coordinator MUST integrate owned commits from explicit base revisions in dependency order and MUST require configured integration checks before accepting the code result, but MUST NOT require a review PROCESS, final rationale evidence, role receipt, or final-verification lifecycle to complete workspace integration. Direct single-writer delegation uses ordinary Git and provider checks without this managed integration lifecycle. The later ordinary provider-native `### Implementation Rationale` human-review discussion is a review handoff, not an integration prerequisite.

#### Scenario: safe integration does not create delivery acceptance

- **WHEN** a managed PROCESS worker returns an owned DCO commit and configured integration checks pass
- **THEN** the coordinator may complete integration while current provider checks and human review remain outside PROCESS lifecycle

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/417#issuecomment-5165960908

### Requirement: Workspace and commit scope enforce declared write ownership

Workspace dispatch and integration MUST enforce PROCESS write ownership on worker commit scope, MUST reject worker commits containing unrelated files or unowned shared outputs, and MUST preserve all pre-existing user changes and unowned worktrees. Workspace preparation MUST NOT reject a lease because another active PROCESS declares overlapping write ownership; the overlap MUST instead be reported as an advisory that names the other workspace and notes that the overlap may require merge-conflict resolution at integration time.

#### Scenario: Active write ownership overlap is advisory

- **WHEN** a proposed writable PROCESS overlaps an active PROCESS on a file, generated output, package chokepoint or shared configuration surface without an integration protocol
- **THEN** workspace preparation MUST allocate the concurrent writable worktree and MUST report an advisory naming the other workspace and noting that the overlap may require merge-conflict resolution at integration time

#### Scenario: Worker commit includes an unrelated file

- **WHEN** the diff of a worker commit contains a path outside its declared ownership and approved shared touchpoints
- **THEN** integration MUST fail with the unexpected paths and MUST NOT cherry-pick or mark the PROCESS done

#### Scenario: Several workers affect generated output

- **WHEN** multiple source PROCESS nodes would regenerate the same checked-in artifact
- **THEN** the plan MUST assign generation to one dedicated generator or integration PROCESS rather than accepting competing generated commits

#### Scenario: Main checkout contains user work

- **WHEN** the coordinator or lifecycle manager observes pre-existing user changes in another checkout
- **THEN** it MUST leave those changes untouched and MUST NOT clean, reset, stage or include them in a PROCESS commit

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/461#issuecomment-5489854487

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

### Requirement: Runner preserves the coordinator boundary for delegated PROCESS execution

For optional PROCESS execution, a Runner coordinator MUST remain at its session integration checkout, MUST use managed workspaces and real non-coordinator native children for every change-bearing node, MAY execute only non-change-bearing orchestration inline at the integration checkout, MUST preserve ownership and bounded handoff, and MUST NOT create REVIEW or VERIFY PROCESS workspaces, mandatory rationale evidence, or evidence lifecycle state.

#### Scenario: runner integration boundary survives workflow simplification

- **WHEN** a Runner executes optional delegated change-bearing work or inline non-change-bearing orchestration
- **THEN** its coordinator root, child worktrees, integration, ownership, and handoff remain isolated while provider review and the human merge decision stay outside PROCESS lifecycle

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/417#issuecomment-5165960908

### Requirement: Managed workspace preparation rejects ambiguous directory ownership before allocation

The CLI MUST validate every normalized bare PROCESS write-ownership path against the exact integrated base Git tree before creating a worker worktree, activating a portable lease, dispatching a worker, or mutating remote Workspace metadata. A bare path that resolves to a tracked Git tree MUST be rejected with an actionable `path/**` replacement, while a tracked blob and a path absent from the base tree MUST retain exact-path semantics and an explicit trailing `/**` declaration MUST retain recursive semantics. Classification MUST use Git object identity at the exact base revision and MUST NOT follow host filesystem symlinks or infer directory intent from filename shape. The CLI MUST NOT silently widen ownership or rewrite PROCESS artifacts. Workspace completion MUST remain authoritative and fail closed for every unowned committed path; when the mismatch is a descendant of a relevant bare declaration, its diagnostic MUST identify that declaration and suggest the recursive form without integrating the commit or marking the PROCESS done.

#### Scenario: Bare tracked directory is rejected before workspace mutation

- **WHEN** workspace prepare receives a bare ownership path that resolves to a Git tree at the exact integrated base revision
- **THEN** preparation MUST fail before worktree creation, lease activation, worker dispatch, or remote Workspace mutation, and the error MUST name the PROCESS, offending declaration, base revision, and exact `path/**` remediation

#### Scenario: Bare tracked file remains exact ownership

- **WHEN** workspace prepare receives a bare ownership path that resolves to a tracked blob at the exact integrated base revision
- **THEN** preparation MUST accept it as exact-file ownership and MUST NOT grant authority over descendants or sibling paths

#### Scenario: Missing base path is not guessed to be a directory

- **WHEN** workspace prepare receives a bare ownership path that is absent from the exact integrated base revision
- **THEN** preparation MUST preserve exact-path semantics and MUST NOT automatically append `/**` or otherwise widen the lease

#### Scenario: Explicit recursive ownership remains valid

- **WHEN** workspace prepare receives an ownership declaration ending in `/**`
- **THEN** preparation MUST preserve the existing recursive ownership behavior for descendants

#### Scenario: Completion still rejects an unowned descendant

- **WHEN** workspace complete observes a committed path outside ownership that is a descendant of a relevant bare declaration
- **THEN** completion MUST fail closed, MUST NOT integrate the commit or mark the PROCESS done, and MUST report the unexpected path with the corresponding `path/**` suggestion

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/272#issuecomment-5005441972

### Requirement: PROCESS ownership guidance distinguishes exact paths from recursive directories

Generated PROCESS planning guidance and maintained English and Chinese workspace documentation MUST describe a bare ownership path as exact and MUST use a trailing `/**` declaration whenever a directory subtree is intended to be writable. Examples MUST NOT imply that a bare directory path authorizes descendants. Existing PROCESS comments MUST remain readable without automatic migration, and users MUST be directed to explicitly correct the artifact or pass a corrected `--write-ownership path/**` value before workspace allocation.

#### Scenario: Directory write area is rendered recursively

- **WHEN** generated guidance or documentation demonstrates a PROCESS that owns a directory subtree
- **THEN** the example MUST render the ownership as `path/**` and explain that recursion is explicit

#### Scenario: Exact file ownership remains bare

- **WHEN** generated guidance or documentation demonstrates ownership of one file
- **THEN** the example MUST retain the bare file path and MUST NOT append `/**`

#### Scenario: Historical PROCESS remains readable

- **WHEN** the CLI reads an existing PROCESS comment containing a bare path
- **THEN** the artifact MUST remain parseable without migration, while preparation MAY reject it when the path resolves to a tracked directory and MUST provide the explicit correction

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/272#issuecomment-5005442423

### Requirement: Workspace ownership documentation separates execution leases from integration conflicts

Maintained workspace documentation MUST distinguish local execution leases, which protect physical execution resources (worktree path, writable branch, duplicate PROCESS dispatch, exclusive runtime resources) with preparation-time rejections, from Git integration conflicts, which are resolved at integration time. Documentation MUST describe shared manifests, lockfiles, and generated outputs as convergence artifacts handled by the designated integration step rather than as preparation blockers, and ownership overlap advisories MUST NOT instruct a reader to pause, stop, or abandon the workspace.

#### Scenario: Documentation names the two conflict classes

- **WHEN** a reader consults maintained workspace documentation about ownership conflicts
- **THEN** the documentation MUST state that declared path overlap across PROCESSes is advisory and that physical execution conflicts are the only preparation-time rejections

#### Scenario: Convergence artifacts are documented

- **WHEN** documentation describes shared manifests, lockfiles, or generated outputs that multiple changes may touch
- **THEN** it MUST describe them as convergence artifacts handled at integration time rather than as lease blockers

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/461#issuecomment-5489856118
