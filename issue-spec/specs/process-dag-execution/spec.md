# process-dag-execution

## Purpose

Define the long-lived behavior contract for how the implement phase plans and executes the PROCESS DAG: capturing execution-planning metadata on TASK comments, defaulting to serial PROCESS chains with bounded handoff, gating parallel dispatch on proven decoupling, treating review and repair as first-class PROCESS nodes, and auditing execution-planning evidence at final verify.

Proposal Issues:
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

Review and repair work MUST be represented as first-class PROCESS nodes in the implementation DAG for non-trivial changes.

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

### Requirement: final verify audits execution-planning evidence

Final verification SHALL validate that execution planning artifacts are complete enough to audit the agent workflow.

For non-trivial changes, VERIFY evidence SHALL cover TASK completion, PROCESS rationale, PROCESS handoff or review evidence, resolved findings, and SPEC coverage.

The workflow SHOULD fail final verification when done PROCESS nodes lack rationale, TASK links, or required handoff/review evidence.

#### Scenario: traceable SPEC to PROCESS chain

- **WHEN** a done PROCESS belongs to a parent TASK
- **THEN** final verification SHALL be able to trace SPEC -> TASK -> PROCESS -> PR rationale or VERIFY evidence

#### Scenario: serial chain proves handoff

- **WHEN** a serial PROCESS chain was used
- **THEN** final verification SHALL confirm that handoff evidence exists or that the coordinator recorded why it was unnecessary

#### Scenario: done VERIFY summarizes evidence

- **WHEN** a non-trivial change reaches final verify
- **THEN** at least one done VERIFY comment SHALL summarize tests, review state, traceability, and SPEC coverage

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/32#issuecomment-4877853419
