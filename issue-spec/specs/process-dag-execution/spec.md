# process-dag-execution

## Purpose

Define the long-lived behavior contract for this capability.

Proposal Issues:
- https://github.com/higress-group/issue-spec/issues/144
- https://github.com/higress-group/issue-spec/issues/196
- https://github.com/higress-group/issue-spec/issues/247
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
- https://github.com/higress-group/issue-spec/issues/247

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
- https://github.com/higress-group/issue-spec/issues/247

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
- https://github.com/higress-group/issue-spec/issues/247

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
- https://github.com/higress-group/issue-spec/issues/247

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
- https://github.com/higress-group/issue-spec/issues/247

### Requirement: Delegate agent-executed change-bearing PROCESS nodes to non-coordinator workers

Every agent-executed change-bearing PROCESS MUST be dispatched to a real runtime-native child or sub-agent. The coordinator MUST NOT implement such a node inline and MUST NOT use workspace_management: independent as an inline escape hatch. A worker MAY execute multiple compatible change-bearing or code-repair PROCESS nodes while preserving each node's state, dependencies, bounded handoff, and evidence. Trusted external or human execution and non-change-bearing execution classes SHALL retain their existing execution policies.

#### Scenario: Coordinator cannot execute a change-bearing node inline

- **WHEN** the coordinator selects an agent-executed change-bearing PROCESS whose dependencies are ready
- **THEN** it SHALL dispatch the node to a real non-coordinator worker instead of implementing, testing, or committing the node itself

#### Scenario: One worker may execute multiple compatible nodes

- **WHEN** several serial or otherwise compatible change-bearing or code-repair PROCESS nodes can be owned safely by the same worker
- **THEN** the coordinator MAY dispatch them to that worker without creating a fresh worker for every node, while retaining separate PROCESS state and evidence

#### Scenario: Unsupported child capability fails closed

- **WHEN** the active runtime cannot dispatch a native child or sub-agent for an agent-executed change-bearing PROCESS
- **THEN** the workflow MUST report an actionable unsupported-capability failure and MUST NOT fall back to coordinator-inline implementation

#### Scenario: Other execution classes remain unchanged

- **WHEN** a PROCESS is verification, orchestration, external, or trusted human-owned work rather than agent-executed change-bearing work
- **THEN** this requirement SHALL NOT add a new native-child or executor-identity gate beyond that class's existing policy

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/247#issuecomment-4992293971

### Requirement: Reject coordinator-authored change-bearing evidence using existing logical Agent identities

Final verification MUST reuse the existing logical Agent identity model and the name normalization established by process.review.author_conflict. The PROCESS typed-comment Agent SHALL identify the coordinator that planned and scheduled the node, and the valid change-bearing rationale carrier Agent SHALL identify the code author. When those normalized names match, final verification MUST fail closed with process.executor.coordinator_conflict. Agent Session ID and Agent Session Source MUST remain diagnostic-only and MUST NOT be compared for executor independence.

#### Scenario: Coordinator-authored rationale is rejected

- **WHEN** a valid rationale carrier for a change-bearing PROCESS has the same normalized Agent name as the PROCESS coordinator Agent
- **THEN** final verification MUST emit the blocking process.executor.coordinator_conflict diagnostic

#### Scenario: Independent worker rationale passes coordinator separation

- **WHEN** a valid change-bearing rationale carrier is authored by a logical Agent whose normalized name differs from the coordinator Agent
- **THEN** the coordinator-separation gate SHALL NOT block that carrier on Agent-name conflict

#### Scenario: Missing session metadata does not determine independence

- **WHEN** a valid worker carrier has missing or runtime-incompatible Agent Session metadata
- **THEN** the coordinator-separation gate MUST evaluate logical Agent names and MUST NOT fail solely because session metadata is absent

#### Scenario: Generated guidance prohibits fabricated worker names

- **WHEN** workflow prompts or skills explain the logical-name backstop
- **THEN** they MUST state that a different Agent name is not sufficient without a real dispatched worker and MUST prohibit fabricated or relabeled identities used only to pass the gate

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/247#issuecomment-4992294654

### Requirement: Guide independent review coverage for every distinct change-bearing code author

Generated coordinator, apply, and workflow guidance MUST instruct the coordinator that, for each distinct Agent authoring one or more change-bearing PROCESS nodes, it SHOULD assign at least one independent review agent whose scope covers that author's PROCESS outputs and affected SPECs. One review agent MAY cover multiple implementation authors when it authored none of the code in its assigned scope. This is scheduling guidance: final verification SHALL continue enforcing review presence and author independence per SPEC and MUST NOT require a one-to-one mapping between implementation and review agents.

#### Scenario: Review planning covers each development author

- **WHEN** multiple distinct worker Agents author change-bearing PROCESS outputs
- **THEN** generated guidance SHALL tell the coordinator to assign independent review coverage for every author and include the relevant PROCESS outputs and affected SPECs in the review scopes

#### Scenario: One independent reviewer may cover multiple authors

- **WHEN** one review agent authored none of the code produced by several implementation workers and can review their combined scope
- **THEN** the coordinator MAY assign that reviewer to all of those workers without creating one unique reviewer per implementation Agent

#### Scenario: Final verification remains per SPEC

- **WHEN** review coverage is evaluated at final verification
- **THEN** the gate MUST require independent review for every change-bearing SPEC and MUST NOT add a blocking implementation-Agent-to-reviewer pairing requirement

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/247#issuecomment-4992295279
