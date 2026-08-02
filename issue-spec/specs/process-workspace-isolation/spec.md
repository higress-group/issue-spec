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
- https://github.com/higress-group/issue-spec/issues/272

## Requirements

### Requirement: PROCESS execution classes determine coordinator-to-child workspace isolation

When optional delegated change-bearing PROCESS execution is selected, the coordinator MUST isolate writable workers by worktree, preserve external or human self-managed workspaces, and avoid allocating writable workspaces to read-only orchestration; review decisions and merge-check MUST consume the provider exact subject without REVIEW or VERIFY PROCESS workspaces.

#### Scenario: optional workers remain isolated

- **WHEN** two delegated implementation PROCESS nodes may execute concurrently
- **THEN** they receive distinct writable worktrees and ownership enforcement while review and merge use provider exact-head reads

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/405#issuecomment-5155764767

### Requirement: Coordinator integration is revision-bound and dependency-ordered

For optional delegated implementation, the coordinator MUST integrate owned commits from explicit base revisions in dependency order and MUST require configured integration checks before accepting the code result, but MUST NOT require a review PROCESS, final rationale, role receipt, or final-verification lifecycle to complete workspace integration.

#### Scenario: safe integration does not create merge authority

- **WHEN** a delegated worker returns an owned DCO commit and configured integration checks pass
- **THEN** the coordinator may complete integration while provider checks and review independently determine merge readiness

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/405#issuecomment-5155764767

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

For optional PROCESS execution, a Runner coordinator MUST remain at its session integration checkout, MUST use managed workspaces and native children for delegated nodes, MAY execute genuinely inline independent nodes at the integration checkout, MUST preserve ownership and bounded handoff, and MUST NOT create REVIEW or VERIFY PROCESS workspaces, mandatory rationale, or evidence lifecycle state.

#### Scenario: runner integration boundary survives workflow simplification

- **WHEN** a Runner executes optional delegated or inline implementation work
- **THEN** its coordinator root, child worktrees, integration, ownership, and handoff remain isolated while provider review and merge-check run outside PROCESS lifecycle

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/405#issuecomment-5155764767

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
