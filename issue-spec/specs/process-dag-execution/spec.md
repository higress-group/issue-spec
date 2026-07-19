# process-dag-execution

## Purpose

Define the long-lived behavior contract for how the implement phase plans and executes the PROCESS DAG through bounded role assignments, compact targeted reads, exact-revision results, independent review and verification, issue-native evidence projection, and generated guidance while preserving real non-coordinator execution, bounded context, invariant-shaped planning, resumable coordinator-managed workspaces, genuinely external or human-owned independent workspaces, and auditable final gates.

Proposal Issues:
- https://github.com/higress-group/issue-spec/issues/144
- https://github.com/higress-group/issue-spec/issues/196
- https://github.com/higress-group/issue-spec/issues/247
- https://github.com/higress-group/issue-spec/issues/295
- https://github.com/higress-group/issue-spec/issues/32

## Requirements

### Requirement: TASK comments capture execution-planning metadata

Design-stage TASK comments MUST capture execution-planning metadata, not only functional scope.

Each non-trivial TASK SHALL document expected write ownership, shared touchpoints, dependency assumptions, coupling level, recommended execution mode, and complexity/split guidance.

The metadata SHALL be advisory during design but SHALL be consumed by the implement coordinator when creating PROCESS comments.

#### Scenario: non-trivial TASK identifies ownership and touchpoints

- **WHEN** a design TASK is created for a non-trivial change
- **THEN** it SHALL identify owned modules or files and any shared touchpoints that may create cross-agent conflicts

#### Scenario: cross-cutting TASK is flagged non-parallel

- **WHEN** a TASK is cross-cutting or touches shared chokepoints
- **THEN** it SHALL be marked as high-coupling, serial-only, coordinator-owned, or needs-process-planning rather than parallel-safe

#### Scenario: coordinator reads TASK metadata before dispatch

- **WHEN** the implement coordinator reads design TASKs
- **THEN** it SHALL have enough metadata to avoid assuming one TASK can be safely assigned to one parallel agent

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/32#issuecomment-4877851917

### Requirement: implement phase plans a PROCESS DAG before dispatch

The implement phase MUST include an explicit PROCESS planning step before spawning worker agents.

The coordinator SHALL derive PROCESS nodes from TASK complexity, coupling, write ownership, and dependency metadata. It SHALL NOT mechanically map every TASK to exactly one PROCESS or every PROCESS to immediate parallel execution.

The PROCESS plan SHALL define parent TASK relationships, dependencies, write ownership, execution mode, and integration ownership where needed.

#### Scenario: coordinator builds a PROCESS DAG before workers

- **WHEN** implementation begins
- **THEN** the coordinator SHALL inspect all active TASK metadata and create a PROCESS DAG before dispatching workers

#### Scenario: simple TASK maps to one PROCESS

- **WHEN** a TASK is simple and low-coupling
- **THEN** the coordinator MAY create one PROCESS for that TASK

#### Scenario: complex TASK splits into bounded PROCESS nodes

- **WHEN** a TASK is complex or has a large context surface
- **THEN** the coordinator SHALL split it into multiple bounded PROCESS nodes while preserving the parent TASK relationship

#### Scenario: shared chokepoint is serialized

- **WHEN** multiple PROCESS nodes touch a shared chokepoint
- **THEN** the plan SHALL serialize them or assign a coordinator/integration owner instead of running them as independent parallel workers

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/32#issuecomment-4877852231

### Requirement: serial PROCESS nodes hand off bounded context

Multiple PROCESS nodes under the same parent TASK SHOULD default to serial execution unless independence is explicitly proven.

A completed serial PROCESS SHALL produce a bounded handoff summary containing changed files, decisions made, assumptions established, tests run, risks, and next-step instructions.

The next PROCESS SHALL receive the parent TASK context plus the previous handoff. It SHALL NOT require the full previous agent transcript to continue correctly.

#### Scenario: split TASK defaults to a serial chain

- **WHEN** a complex parent TASK is split to control per-agent context size
- **THEN** the resulting PROCESS nodes SHALL be ordered as a serial chain unless their write sets and interfaces are independent

#### Scenario: dependent predecessor produces a handoff

- **WHEN** PROCESS-B depends on PROCESS-A under the same TASK
- **THEN** PROCESS-A SHALL finish with a handoff summary suitable for PROCESS-B

#### Scenario: successor starts from bounded context

- **WHEN** PROCESS-B starts
- **THEN** it SHALL receive the parent TASK context, relevant SPEC/TASK links, and PROCESS-A handoff before making changes

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/32#issuecomment-4877852507

### Requirement: parallel dispatch is a gated exception, not the default

Parallel agent execution SHALL be allowed only for PROCESS nodes that are sufficiently decoupled.

The coordinator SHALL verify that parallel PROCESS nodes have non-overlapping write ownership, stable interface contracts, independent test surfaces, and no immediate dependency on each other's implementation decisions.

The workflow SHALL treat parallelism as an optimization after context control and conflict avoidance, not as the primary reason for splitting work.

The existing workflow guidance that defaults to parallel worker dispatch SHALL be revised so that serial execution is the default and parallel dispatch is the explicitly gated exception.

#### Scenario: overlapping writes must not run in parallel

- **WHEN** two PROCESS nodes write to the same file, package-level abstraction, generated artifact, or shared configuration surface
- **THEN** they SHALL NOT run in parallel unless a coordinator-owned integration protocol is explicitly recorded

#### Scenario: disjoint modules may run in parallel

- **WHEN** two parent TASKs own separate modules and interact only through stable pre-existing interfaces
- **THEN** their PROCESS nodes MAY run in parallel

#### Scenario: uncertainty defaults to serial

- **WHEN** uncertainty exists about overlap or interface stability
- **THEN** the coordinator SHALL choose serial execution or create an integration PROCESS rather than optimistic parallel dispatch

#### Scenario: workflow guidance is rewritten serial-first

- **WHEN** the implement or apply workflow guidance is updated for this change
- **THEN** any text that dispatches independent workers in parallel by default SHALL be rewritten to make serial-under-shared-parent-TASK the default and parallel dispatch conditional on the decoupling checks in this SPEC

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/32#issuecomment-4877852810

### Requirement: review and repair are first-class PROCESS nodes

Review and repair work MUST be represented as first-class PROCESS nodes in the implementation DAG for every active SPEC that has a valid change-bearing carrier, regardless of change size.

Review findings SHALL be assigned to owner PROCESS nodes or to dedicated repair PROCESS nodes based on coupling and write ownership.

Repair PROCESS scheduling SHALL follow the same serial/parallel constraints as initial implementation work.

#### Scenario: shared-chokepoint findings get one repair owner

- **WHEN** review finds issues in a shared chokepoint touched by multiple workers
- **THEN** the coordinator SHALL create or assign a repair PROCESS with explicit ownership instead of asking multiple agents to patch the same area concurrently

#### Scenario: low-coupling finding fixed by its owner

- **WHEN** a review finding maps cleanly to one low-coupling owner PROCESS
- **THEN** that PROCESS or a direct follow-up repair PROCESS MAY fix it independently

#### Scenario: no open findings before final verify

- **WHEN** all findings are resolved
- **THEN** review sync SHALL show no open actionable or blocking findings before final verify

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/32#issuecomment-4877853100

### Requirement: Change-bearing work requires a present, independent review PROCESS

Every active SPEC that has satisfied change-bearing carrier evidence (a matching inline rationale on the PR) MUST also be covered by a satisfied review PROCESS whose reviewing agent is independent of the code author. Because the independent-review check only runs when a review PROCESS exists, final verify MUST fail closed when a change-bearing SPEC has no review PROCESS covering it, so that omitting the review node cannot silently bypass the independence contract. The reviewing agent's identity is judged by its `--agent` name, and a review PROCESS whose reviewer name matches a code author of the same SPEC MUST NOT satisfy this requirement for that SPEC.

#### Scenario: coded SPEC without any review PROCESS fails closed

- **WHEN** an active SPEC has a satisfied change-bearing carrier but no satisfied review PROCESS covers it at final verify
- **THEN** the gate MUST emit a blocking diagnostic requiring an independent review PROCESS for that SPEC

#### Scenario: independent review PROCESS satisfies the requirement

- **WHEN** a change-bearing SPEC is covered by a review PROCESS whose reviewer `--agent` differs from every code author of that SPEC
- **THEN** the presence and independence requirements are both satisfied and the gate MUST NOT block on this basis

#### Scenario: self-authored review does not satisfy presence

- **WHEN** the only review PROCESS covering a change-bearing SPEC has a reviewer `--agent` equal to a code author of that SPEC
- **THEN** the gate MUST treat the SPEC as lacking independent review and MUST block final verify

Source change: https://github.com/higress-group/issue-spec/pull/232

### Requirement: final verify audits execution-planning evidence

Final verification SHALL validate that execution planning artifacts are complete enough to audit the agent workflow.

For non-trivial changes, VERIFY evidence SHALL cover TASK completion, class-specific PROCESS evidence, resolved findings, and SPEC coverage. Change-bearing PROCESS nodes SHALL use matching inline PR rationale, while review, verification, orchestration, and external PROCESS nodes SHALL use their native REVIEW, VERIFY, check, handoff, or immutable external-revision carriers.

The workflow SHOULD fail final verification when done PROCESS nodes lack their required class-specific carrier, TASK links, active SPEC coverage, or required handoff/review evidence.

#### Scenario: traceable SPEC to PROCESS chain

- **WHEN** a done PROCESS belongs to a parent TASK
- **THEN** final verification SHALL be able to trace SPEC -> TASK -> PROCESS -> its required PR rationale, REVIEW, VERIFY, check, handoff, or immutable external evidence

#### Scenario: serial chain proves handoff

- **WHEN** a serial PROCESS chain was used
- **THEN** final verification SHALL confirm that handoff evidence exists or that the coordinator recorded why it was unnecessary

#### Scenario: done VERIFY summarizes evidence

- **WHEN** a non-trivial change reaches final verify
- **THEN** at least one done VERIFY comment SHALL summarize tests, review state, traceability, and SPEC coverage

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/32#issuecomment-4877853419
- https://github.com/higress-group/issue-spec/issues/166#issuecomment-4951036789

### Requirement: Agent-executed change-bearing PROCESS nodes require non-coordinator workers

Every agent-executed change-bearing PROCESS MUST run in a real runtime-native child or sub-agent whose logical `Agent` differs from the coordinator. The PROCESS MUST use the coordinator-managed `workflow workspace prepare` -> child/worker -> `complete` -> `integrate` lifecycle. The coordinator MUST NOT implement, test, or commit the node inline and MUST NOT select `independent` as an escape from that lifecycle. Merely choosing another logical agent name, relabeling the coordinator, or fabricating child identity MUST NOT satisfy the requirement.

The coordinator retains planning, scheduling, workspace management, integration, synchronization, gate evaluation, blocker handling, and bounded handoff duties. Genuine external or human-owned independently managed work remains supported. Verification-only and orchestration-only PROCESS classes, session diagnostics, parallelism eligibility, and the direct one-file delivery path remain unchanged.

#### Scenario: Ready change-bearing node is dispatched

- **WHEN** an agent-executed change-bearing PROCESS becomes ready
- **THEN** the coordinator SHALL prepare its managed workspace and dispatch a real non-coordinator native child to implement, test, and commit it

#### Scenario: Missing native child support fails closed

- **WHEN** the runtime cannot create a real native child for an agent-executed change-bearing PROCESS
- **THEN** the coordinator SHALL report the blocker instead of implementing inline or fabricating a distinct logical identity

#### Scenario: Other execution paths remain unchanged

- **WHEN** work is genuinely external or human-owned, verification-only, orchestration-only, or eligible for the direct one-file path
- **THEN** its existing execution and delivery behavior SHALL remain unchanged

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/144#issuecomment-4904042881
- https://github.com/higress-group/issue-spec/pull/232
- https://github.com/higress-group/issue-spec/issues/247#issuecomment-4992293971
- https://github.com/higress-group/issue-spec/issues/247#issuecomment-4992294654

### Requirement: Compatible serial PROCESS nodes may reuse a worker while preserving bounded handoff

The same real worker MAY execute multiple compatible serial change-bearing or repair PROCESS nodes. A fresh worker per node is not required, but each node MUST retain its own PROCESS state, dependency checks, write ownership, managed workspace result, evidence, rationale, and bounded `### Handoff`. A successor MUST be seeded with the parent TASK context plus its predecessor's handoff and MUST NOT require the predecessor's full transcript.

#### Scenario: Same worker continues to a compatible successor

- **WHEN** PROCESS-B depends on PROCESS-A, their ownership and dependencies are compatible, and the coordinator keeps the same real worker assigned
- **THEN** the worker MAY continue with PROCESS-B after PROCESS-A completes while preserving both PROCESS lifecycles and the bounded handoff between them

#### Scenario: Reuse does not collapse PROCESS boundaries

- **WHEN** one worker executes more than one PROCESS node
- **THEN** each node SHALL still be independently prepared, completed, integrated, evidenced, and synchronized

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/144#issuecomment-4904043156
- https://github.com/higress-group/issue-spec/issues/247#issuecomment-4992293971

### Requirement: Coordinator prompt carries a phase-aware worker and review contract

The generated coordinator prompt MUST express the DAG-execution contract at fidelity matching the apply workflow guidance. On an implement turn it MUST direct the coordinator to plan the PROCESS DAG before execution, require agent-executed change-bearing nodes to use managed real non-coordinator native children, prohibit coordinator-inline execution and fabricated identities, allow one worker to execute multiple compatible nodes without collapsing per-PROCESS boundaries, and keep parallel execution as a separately gated optimization. It MUST preserve mandatory per-SPEC independent review and SHOULD direct the coordinator to assign at least one independent reviewer for every distinct change-bearing author. One reviewer MAY cover multiple authors, and this author-oriented guidance MUST NOT introduce a new one-reviewer-per-author blocking gate.

#### Scenario: Implement-turn prompt mandates plan then worker dispatch

- **WHEN** the coordinator prompt is rendered for an implement-phase command
- **THEN** it SHALL direct the coordinator to build the PROCESS DAG first and dispatch every agent-executed change-bearing node to a real non-coordinator managed worker

#### Scenario: Prompt rejects identity fabrication

- **WHEN** the coordinator prompt describes executor separation
- **THEN** it SHALL state that a different logical name without a real native child is insufficient

#### Scenario: Parallelism remains a separate gate

- **WHEN** the coordinator prompt describes worker dispatch
- **THEN** it SHALL allow serial worker reuse while requiring separate workers for nodes actually dispatched in parallel

#### Scenario: Prompt guides reviewer coverage without a new gate

- **WHEN** the coordinator prompt describes review scheduling
- **THEN** it SHALL recommend independent reviewer coverage for each distinct author, allow one reviewer to cover multiple authors, and retain per-SPEC independent review as the blocking rule

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/144#issuecomment-4904043410
- https://github.com/higress-group/issue-spec/issues/247#issuecomment-4992293971
- https://github.com/higress-group/issue-spec/issues/247#issuecomment-4992294654
- https://github.com/higress-group/issue-spec/issues/247#issuecomment-4992295279

### Requirement: Coordinator retains only orchestration state during agent-executed implementation

During agent-executed implementation the coordinator MUST retain only planning, scheduling, workspace management, gate evaluation, integration, synchronization, blocker handling, and bounded handoff state. It MUST consume bounded worker outputs and issue-spec read results rather than implementing, testing, committing, writing rationale, or inlining full issue or pull request bodies or full diffs into its own context.

#### Scenario: Coordinator integrates via bounded outputs

- **WHEN** workers complete coding PROCESS nodes
- **THEN** the coordinator SHALL integrate their results using bounded summaries, handoff evidence, and issue-spec read rather than re-reading full bodies or diffs inline

#### Scenario: Coordinator does not accumulate implementation detail

- **WHEN** the coordinator advances the DAG across multiple nodes
- **THEN** its retained context SHALL be limited to scheduling, gate status, integration ownership, and handoff references

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/144#issuecomment-4904043650
- https://github.com/higress-group/issue-spec/issues/247#issuecomment-4992293971

### Requirement: Explicit workspace-management declaration

Every newly generated PROCESS comment MUST render exactly one `### Workspace Management` section with either `managed` or `independent`. The generator MUST default to `managed`. The parser MUST reject empty, duplicate, multi-value, or unknown declarations while treating a missing declaration as legacy-compatible.

#### Scenario: Generated process defaults to managed

- **WHEN** a PROCESS is generated without a workspace-management input
- **THEN** its logical body contains exactly `- managed` under `### Workspace Management`

#### Scenario: Invalid declaration is rejected

- **WHEN** a PROCESS contains an unknown or duplicate workspace-management declaration
- **THEN** canonical validation reports an error

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/196#issuecomment-4964217002

### Requirement: Independent workspace final-gate behavior

A done PROCESS genuinely declared `independent` for external or human-owned self-managed change-bearing work MUST NOT require portable Workspace metadata at the final gate. Agent-executed change-bearing nodes MUST use `managed` and MUST NOT select `independent` for coordinator-inline execution. A `managed` or legacy undeclared change-bearing PROCESS MUST retain the existing portable Workspace requirement. `workflow workspace prepare` MUST reject an explicitly independent PROCESS so runner and managed child execution cannot claim it. Independent mode MUST NOT weaken existing PROCESS PR, rationale, review, verification, or traceability checks.

#### Scenario: Independent process is not blocked for absent lease

- **WHEN** a final-gate snapshot contains a done explicit independent change-bearing PROCESS without a Workspace section
- **THEN** workspace evaluation emits no missing-Workspace blocker for that PROCESS

#### Scenario: Managed process remains protected

- **WHEN** a final-gate snapshot contains a done managed change-bearing PROCESS without a Workspace section
- **THEN** workspace evaluation reports the existing required-Workspace blocker

#### Scenario: Managed allocation rejects independent work

- **WHEN** workflow workspace prepare targets an explicit independent PROCESS
- **THEN** the command rejects the request before creating a workspace lease

#### Scenario: Coordinator cannot select independent for inline implementation

- **WHEN** the coordinator owns an agent-executed change-bearing PROCESS
- **THEN** it SHALL use a managed non-coordinator worker rather than declaring the PROCESS independent and implementing it inline

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/196#issuecomment-4964218213
- https://github.com/higress-group/issue-spec/issues/247#issuecomment-4992293971

### Requirement: Role-bounded implementation with validated worker results

For agent-executed change-bearing work, the issue-spec workflow MUST keep the main agent as coordinator, MUST dispatch a real non-coordinator worker, and MUST let that worker implement and test from a digest-covered bounded assignment while lifecycle bookkeeping and Git-result validation remain coordinator-owned. A Coordinator-imported result MAY be used to validate the assignment and Git result, but caller-provided identity and provenance fields MUST remain informational and MUST NOT create accepted implementation-receipt authority.

#### Scenario: Worker receives a bounded assignment

- **WHEN** the coordinator dispatches a ready change-bearing PROCESS
- **THEN** the worker receives only the correlation id, objective, relevant acceptance scenarios, exact worktree and base revision, write ownership, dependencies, predecessor handoff, commit and generator requirements, focused tests, and result schema

#### Scenario: Implementation assignment carries authoritative design context

- **WHEN** the coordinator issues a change-bearing implementation assignment
- **THEN** the digest-covered packet includes the exact canonical Design Issue URL, a targeted complete-body read instruction, and the coordinator-authored PROCESS design covering one named invariant, applicable decisions, implementation direction, must-preserve constraints, must-not constraints, and minimum verification; the CLI preserves those values without interpreting Design prose, and the worker stops if the PROCESS design conflicts with the authoritative Design

#### Scenario: Legacy stored assignment remains readable but cannot be reissued

- **WHEN** the CLI opens registry or PROCESS workspace state created before design_context became required
- **THEN** historical inspection and recovery can read the legacy assignment without declaring the registry corrupt, while any new issuance, redispatch, assignment-file acceptance, or role submission still requires a complete canonical design_context

#### Scenario: Assignment stays outside the code result

- **WHEN** the CLI emits or persists an implementation assignment
- **THEN** it returns the packet on stdout or stores it in coordinator-managed sidecar state outside the Git worktree so it cannot become part of the worker commit

#### Scenario: Coordinator validates a worker result before integration

- **WHEN** a worker returns a structured result bound to its assignment
- **THEN** the CLI validates assignment id, digest and generation, active workspace, base and result revisions, changed paths, single-commit and DCO policy, write ownership, required generated outputs, and required tests before integration, while imported writer, subject, session, route, and assurance labels remain informational and create no accepted implementation-receipt marker

#### Scenario: Coordinator cannot substitute for a worker

- **WHEN** a coordinator imports a result file, changes an agent or session label, or supplies equivalent prose
- **THEN** the CLI MUST NOT represent that input as runtime-attested or accepted implementation-receipt authority, and the existing managed-worker, rationale, executor-separation, exact-revision, and final-verification gates remain authoritative

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/295#issuecomment-5010953633

### Requirement: Minimal independent-review context with honest provenance

The issue-spec workflow MUST give an independent reviewer a bounded exact-revision assignment and MUST let that reviewer publish findings or a verdict through a narrow role-specific submit path before the coordinator finalizes links or lifecycle bookkeeping. Version-1 reviewer provenance is self-reported compatibility evidence, not runtime attestation, and the CLI MUST NOT present an agent name or session label as proof of the executing process.

#### Scenario: Reviewer inspects a bounded exact-revision packet

- **WHEN** integrated code becomes ready for independent review
- **THEN** a reviewer different from every covered code author receives only the immutable revision or diff, affected SPEC scenarios, review scope, author identity, relevant tests, and finding or verdict schema

#### Scenario: Review assignment carries authoritative design context

- **WHEN** the coordinator issues an independent review assignment
- **THEN** the digest-covered packet includes the exact canonical Design Issue URL, a targeted complete-body read instruction, and the same PROCESS design invariant, applicable decisions, direction, constraints, and minimum verification that governed implementation; the reviewer stops and reports any conflict with the authoritative Design

#### Scenario: Review result is bound to the reviewer and revision

- **WHEN** the reviewer returns findings or an explicit no-finding verdict
- **THEN** the role-specific submit path validates assignment identity, exact reviewed revision, non-Coordinator ownership, and independence from exact-diff authors, then persists immutable receipt identity and self-reported provenance before the coordinator projects deterministic links or other lifecycle state

#### Scenario: Unattested migration path remains honest

- **WHEN** the runtime cannot validate an imported reviewer receipt with equivalent assurance
- **THEN** the compatible reviewer-owned narrow submit path remains available, its provenance stays explicitly self-reported, runtime-attested coordinator import remains unavailable, and the caller cannot select a stronger assurance label

#### Scenario: Stale or self-authored review remains blocked

- **WHEN** review evidence targets another revision or the reviewer authored covered code
- **THEN** the existing exact-revision and independent-review gates reject it

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/295#issuecomment-5010953748

### Requirement: Compact status and verification with unchanged gates

The issue-spec CLI MUST offer additive compact status and verification views that group repeated diagnostics and bound affected identifiers while using the same authoritative evaluator, snapshot, decision, and exit status as full detail.

#### Scenario: Repeated diagnostics are grouped

- **WHEN** many PROCESS nodes fail the same gate diagnostic
- **THEN** compact output reports the stable diagnostic code once with a count, bounded affected ids, truncation state, and a recommended next action for that blocker group

#### Scenario: Successful output remains bounded

- **WHEN** a requested gate passes
- **THEN** compact output returns the decision, target, authoritative revision or evidence identity, and checked categories without expanding satisfied evidence for every PROCESS

#### Scenario: Exact detail remains addressable

- **WHEN** a blocker requires diagnosis
- **THEN** compact output provides an explicit detail command that retrieves the relevant complete evidence without forcing unrelated artifact bodies into context

#### Scenario: Compatibility output remains available

- **WHEN** a human or existing automation requests the current full report
- **THEN** the CLI returns complete per-PROCESS evidence and the compact and full decisions and exit statuses are identical for the same snapshot

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/295#issuecomment-5010953893

### Requirement: Precise active-artifact reads with explicit history

The issue-spec CLI MUST provide precise single-artifact and filtered active/history reads, including a CLI-computed exact representation digest, so callers and agents receive current state without unrelated or superseded bodies while full audit history remains explicitly available. Provider adapters MAY use direct lookup, cache, or an internal timeline scan according to backend capability; the bounded contract applies to returned caller context rather than requiring every provider to support server-side artifact-id filtering.

#### Scenario: One artifact can be read directly

- **WHEN** an agent requests a typed artifact by issue and stable id
- **THEN** the CLI returns its identity, status, URL, exact representation digest, relevant links, and revision or workspace binding without returning every comment body to the caller

#### Scenario: Active machine reads omit superseded bodies

- **WHEN** an agent requests the current typed artifacts for a phase
- **THEN** superseded artifacts are represented only by bounded metadata such as id, status, URL, digest, and superseded-by unless body or history detail is explicitly requested

#### Scenario: Existing reads are not silently broken

- **WHEN** precise and active read modes are introduced
- **THEN** existing full-list behavior remains available through a documented compatibility or detail path and canonicalization is owned by the CLI rather than caller-side text pipelines

#### Scenario: Human-readable artifacts remain intact

- **WHEN** agent-facing reads are compacted
- **THEN** Proposal, Design, durable SPEC/TASK content, and explicit audit history remain readable to humans

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/295#issuecomment-5010954045

### Requirement: Deterministic coordinator projection with compact durable evidence

The issue-spec CLI MUST compile lifecycle state, deterministic issue relationships, and compact durable evidence from explicit already-accepted role receipt identities through retry-safe operations, without accepting receipt payloads through projection, inferring authority from prose, or persisting duplicated and immediately stale evaluator forecasts.

#### Scenario: Accepted carriers project deterministic issue relationships

- **WHEN** an already-accepted PROCESS, REVIEW, or VERIFY carrier explicitly identifies immutable receipt identity, generation, coverage, and current targets
- **THEN** the CLI idempotently projects only authorized typed lifecycle, current-pointer, and issue-link relationships, reports the applied mutation plan, and leaves provider rationale, findings, and checks on their existing role-owned commands and carriers

#### Scenario: Implementation projection requires accepted issue-native authority

- **WHEN** workspace completion validated a Coordinator-imported implementation result but the PROCESS contains no accepted implementation-receipt marker
- **THEN** implementation projection fails closed and MUST NOT treat workspace assignment, result commit, caller JSON, or imported provenance labels as a substitute for accepted issue-native authority

#### Scenario: Role-level retry preserves the state machine

- **WHEN** projection is retried after a partial or non-atomic provider mutation
- **THEN** the CLI observes current state, resumes from a stable checkpoint, applies existing transition and concurrency checks, and returns fresh final state

#### Scenario: Invalid relationship types fail early

- **WHEN** a caller supplies a provider discussion or other URL that is not valid for the requested typed relationship
- **THEN** the write is rejected with a structured diagnostic instead of storing a link that only fails final verification

#### Scenario: Typed evidence stores only durable authority

- **WHEN** PROCESS, REVIEW, or VERIFY evidence is projected
- **THEN** it records authoritative state, subject and result revisions, provenance, structured tests or checks, findings or verdict, and handoff without duplicating a second status field or a recomputable full PROCESS gate forecast

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/295#issuecomment-5011082197

### Requirement: Generated role instructions shrink after CLI enforcement

Generated workflow instructions MUST remain role-bounded and concise by relying on executable CLI validation and the addressable instruction contract rather than repeating the complete workflow protocol in every agent context.

#### Scenario: Worker and reviewer packets exclude coordinator policy

- **WHEN** the coordinator prepares an implementation or review assignment
- **THEN** the packet excludes phase bodies, full PROCESS graphs, link matrices, closure and archive policy, provider routing, and typed-comment authoring instructions

#### Scenario: Coordinator plans PROCESS nodes by design invariant

- **WHEN** the coordinator derives a PROCESS DAG from confirmed TASK execution planning
- **THEN** each PROCESS completely owns one independently verifiable Design invariant and its major entry points, and it is split only when the resulting nodes have independent acceptance criteria and can be reviewed correctly in isolation; file overlap and parallelism remain secondary to invariant cohesion

#### Scenario: Ambiguous PROCESS boundaries block for human direction

- **WHEN** preserving an end-to-end invariant conflicts with keeping one role agent's context and working set bounded and the coordinator cannot establish a stable independently testable split
- **THEN** Implement planning stops as blocked with the competing boundary options and their acceptance consequences, and requires human direction instead of silently choosing an oversized or fragmented PROCESS

#### Scenario: Coordinator instructions focus on actions and stops

- **WHEN** workflow and phase skills are generated
- **THEN** they contain concise role actions, recovery entry points, and mandatory stop conditions while detailed stable rules are enforced by CLI validators or requested through the instruction API

#### Scenario: Compaction follows enforceability

- **WHEN** a rule has not yet been moved into an executable validator or bounded contract
- **THEN** instruction shortening does not remove that safety boundary merely to meet a token target

#### Scenario: Size regressions are bounded

- **WHEN** generated coordinator skills, assignments, results, or compact reports change
- **THEN** regression tests enforce explicit size budgets while preserving all authoritative gate outcomes

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/295#issuecomment-5011082327
