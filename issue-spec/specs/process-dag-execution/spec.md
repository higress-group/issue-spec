# process-dag-execution

## Purpose

Define the long-lived behavior contract for how the optional implement phase plans and executes a PROCESS DAG through bounded implementation assignments, compact targeted reads, exact-revision results, and generated guidance while preserving real non-coordinator execution, bounded context, invariant-shaped planning, resumable coordinator-managed workspaces, genuinely external or human-owned work, and strict separation from provider-owned review, checks, and merge authority.

Proposal Issues:
- https://github.com/higress-group/issue-spec/issues/144
- https://github.com/higress-group/issue-spec/issues/196
- https://github.com/higress-group/issue-spec/issues/247
- https://github.com/higress-group/issue-spec/issues/295
- https://github.com/higress-group/issue-spec/issues/299
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

The workflow MUST default a bounded implementation to one code writer and MUST permit exactly one code-writing child or subagent without TASK or PROCESS when the coordinator performs no concurrent code writes and no managed-coordination need exists. Read-only investigation or review children MUST NOT require PROCESS. Child use, file count, independent review, and merge evidence MUST NOT select PROCESS.

When a change explicitly selects TASK/PROCESS coordination, the implement coordinator MUST plan the PROCESS DAG before dispatch. Selection requires at least one concrete managed-coordination need: concurrent code writers, isolation protecting pre-existing work, enforced path ownership, restartable cross-session handoff, or dependency-ordered integration. PROCESS planning state MUST NOT become merge evidence or a final blocker.

#### Scenario: one delegated writer stays on the direct path

- **WHEN** a bounded change is delegated to exactly one code-writing child, the coordinator does not write concurrently, and no managed-coordination need exists
- **THEN** the child MAY implement without TASK, PROCESS, workspace lifecycle, or role receipt while ordinary Git and provider checks validate the result

#### Scenario: child use alone does not select PROCESS

- **WHEN** a child performs implementation, investigation, or review without concurrent write ownership, isolation, recovery, or integration needs
- **THEN** the workflow MUST NOT create PROCESS solely to record that delegation

#### Scenario: planning is selected by coordination risk

- **WHEN** parallel ownership, protection of pre-existing work, enforced path ownership, restartable handoff, or dependency-ordered integration requires PROCESS coordination
- **THEN** the coordinator plans the DAG before workers while merge-check remains independent of its lifecycle

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/405#issuecomment-5155764767

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

### Requirement: Agent-executed change-bearing PROCESS nodes require non-coordinator workers

When optional agent-executed change-bearing PROCESS work is selected, it MUST run through a real non-coordinator native child and the managed prepare-complete-integrate lifecycle; the coordinator MUST NOT fabricate identity or use independent mode as an inline escape, and no verification-only PROCESS class, role receipt, synchronization step, or readiness gate is implied.

#### Scenario: optional change-bearing node uses a real child

- **WHEN** a coordinator dispatches an agent-executed change-bearing PROCESS
- **THEN** a real non-coordinator worker owns implementation and the managed workspace result while review and merge authority remain provider-owned

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/405#issuecomment-5155764767

### Requirement: Compatible serial PROCESS nodes may reuse a worker while preserving bounded handoff

When optional serial PROCESS execution is selected, the same real worker MAY execute compatible successors while each node preserves dependency checks, write ownership, managed workspace result, and bounded handoff; evidence carriers, rationale, synchronization, review PROCESS, VERIFY, and final-gate state MUST NOT be required.

#### Scenario: serial reuse preserves only execution boundaries

- **WHEN** one worker continues from PROCESS-A to compatible dependent PROCESS-B
- **THEN** the coordinator preserves ownership, workspace integration, and the bounded handoff without creating or synchronizing merge evidence per PROCESS

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/405#issuecomment-5155764767

### Requirement: Coordinator retains only orchestration state during agent-executed implementation

During agent-executed implementation the coordinator MUST retain only planning, scheduling, workspace management, gate evaluation, integration, synchronization, blocker handling, and bounded handoff state. It MUST consume bounded worker outputs and issue-spec read results rather than implementing, testing, committing, writing legacy rationale evidence, or inlining full issue or pull request bodies or full diffs into its own context. Each implementation worker MUST own zero or more bounded line-rationale drafts for non-obvious decisions in its changed code, anchored by repository-relative path and stable symbol plus changed line with why/tradeoff/risk and containing no secret, raw payload, or credential. After the exact integrated change is pushed, the coordinator MUST validate and map those anchors, confirm continued applicability and sensitive-data absence, and MUST publish each valid unchanged worker-authored text as non-blocking provider-native inline discussion when safe; otherwise it MUST preserve `path:symbol/line` plus that text in the top-level fallback. It MUST return or explain dropping invalid drafts and MUST NOT compose or replace the workers' line rationale. The coordinator owns only provider publication and the ordinary top-level `### Implementation Rationale` summary/index without creating PROCESS/SPEC-bound rationale state.

#### Scenario: Coordinator integrates via bounded outputs

- **WHEN** workers complete coding PROCESS nodes
- **THEN** the coordinator SHALL integrate their results using bounded summaries, handoff evidence, zero or more worker-owned line-rationale drafts, and issue-spec read rather than re-reading full bodies or diffs inline

#### Scenario: Coordinator publishes but does not author worker rationale

- **WHEN** a worker returns a valuable stable-anchor rationale draft and the exact integrated head is pushed
- **THEN** the coordinator validates its changed-line anchor and publishes the worker-authored text inline or preserves it in the top-level fallback without substituting coordinator-written rationale

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

### Requirement: Generated role instructions shrink after CLI enforcement

Generated workflow instructions MUST remain role-bounded and concise by relying on executable CLI validation and the addressable instruction contract rather than repeating the complete workflow protocol in every agent context.

#### Scenario: Worker packets exclude coordinator policy

- **WHEN** the coordinator prepares an implementation assignment
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

### Requirement: implementation completion creates a canonical receipt without manual framing

The issue-spec CLI MUST provide an implementation-only completion operation that derives every mechanical receipt fact from the sealed assignment and authoritative worktree, executes exactly the sealed required tests, combines only closed implementation semantic input, and emits one atomically written self-validating receipt whose digest uses the shared canonical logical representation and is independent of output-file framing. Coordinator-owned acceptance MUST remain separate, MUST recompute all applicable invariants, and MUST NOT repair, reseal, or strengthen a rejected result. Review and verification role completion MUST return `deprecated_workflow` without mutation.

#### Scenario: implementation role completes with one command

- **WHEN** an implementation Worker has a valid active assignment, a clean managed worktree with one DCO result commit, owned changed paths, and passing sealed required tests
- **THEN** one role-owned completion command derives the assignment identity, generation, base and result revisions, changed paths, exact test evidence, and provenance, and emits a receipt accepted by independent workspace completion validation

#### Scenario: removed role types fail before receipt production

- **WHEN** a caller supplies a historical review or verification assignment to role completion
- **THEN** the CLI returns `deprecated_workflow` and emits no new receipt

#### Scenario: sealed tests cannot be replaced by the caller

- **WHEN** a caller attempts to omit, add, duplicate, replace, or claim the outcome of a required test or to supply an arbitrary command or revision
- **THEN** role completion fails closed and does not emit an acceptable success receipt

#### Scenario: canonical digest ignores JSON file framing

- **WHEN** the same valid receipt data is serialized with LF, CRLF, or trailing whitespace outside the top-level JSON value
- **THEN** parsing and shared canonical re-serialization produce the same logical receipt digest, while any semantic field change produces a different digest or validation failure

#### Scenario: output is atomic and self-validating

- **WHEN** role completion reports success
- **THEN** the final receipt has been atomically published, re-read, parsed, and verified by the same binary with a recomputed digest equal to the embedded digest

#### Scenario: failed execution emits no acceptable receipt

- **WHEN** a required test fails, the workspace is dirty, commit or ownership policy fails, assignment identity is stale, or revision binding cannot be resolved exactly
- **THEN** the command exits non-zero with bounded diagnostics and Coordinator acceptance cannot treat its output as successful evidence

#### Scenario: Coordinator validation does not repair role evidence

- **WHEN** workspace complete or read-only receipt inspection observes a missing field, bad digest, stale generation, mismatched revision, or altered test result
- **THEN** the validator reports the mismatch and remediation without editing, resealing, or replacing the role-owned receipt

#### Scenario: existing receipt contracts remain compatible

- **WHEN** an existing valid version-1 receipt is submitted through the current Coordinator acceptance path
- **THEN** it remains readable and acceptable under the same trust and exactness rules without historical receipt rewriting

#### Scenario: generated role guidance uses the bounded command

- **WHEN** Apply role instructions are generated after the implementation completion operation is available
- **THEN** the instructions invoke the bounded CLI operation, omit hand-written receipt schema and shell hashing recipes, and ask the role to return only the compact command result and human summary

#### Scenario: instruction compaction is regression tested

- **WHEN** representative generated Apply packets and handoffs are measured before and after adoption
- **THEN** fixture-based byte or token estimates demonstrate reduced normal role context and output while every receipt, provenance, revision, test, and acceptance invariant remains enforced by the CLI

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/396#issuecomment-5152395123

### Requirement: planning relationships have one canonical owner

Every active planning relationship MUST resolve to exactly one canonical owning typed artifact. Creating or updating the relationship MUST mutate only that owner and MUST NOT create or refresh a reverse backlink on the target artifact. Unsupported or ambiguous ownership MUST fail before any write. Historical REVIEW/VERIFY relationships are read-only navigation and cannot become authority.

#### Scenario: planning fan-out writes only its owner

- **WHEN** one TASK or PROCESS is related to multiple planning targets
- **THEN** the relationship operation writes the canonical planning owner at most once and performs zero target backlink writes

#### Scenario: known artifact pair resolves one owner

- **WHEN** a caller supplies either ordering of a supported typed artifact pair
- **THEN** the CLI deterministically selects the schema-defined owner or returns the exact corrected orientation without mutating either peer

#### Scenario: ambiguous relationship fails before mutation

- **WHEN** the artifact types or relationship kind do not identify exactly one canonical owner
- **THEN** the command rejects the request before updating any comment

#### Scenario: legacy backlink cannot become authority

- **WHEN** a historical target comment contains a reverse backlink that disagrees with the canonical owner
- **THEN** the canonical owner controls the relationship and the legacy backlink remains compatibility-only navigation data

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/392#issuecomment-5152236315

### Requirement: canonical relationship updates are bounded and conflict-safe

A canonical relationship owner update MUST merge a bounded target set through one idempotent mutation, preserve every previously accepted canonical target outside an explicit removal plan, detect stale observations, and verify the exact postcondition. A backend without atomic conditional comment updates MUST require an explicit guarded single-writer fallback and MUST NOT report success when concurrent or partial-write drift is observed.

#### Scenario: bounded planning fan-out is one owner mutation

- **WHEN** a caller adds several TASK or SPEC targets owned by one planning artifact
- **THEN** the CLI merges the complete bounded set and writes the owner once while preserving existing canonical targets

#### Scenario: stale owner observation fails explicitly

- **WHEN** another writer changes the canonical owner after the caller observed it
- **THEN** conditional mutation or postcondition validation returns a conflict instead of silently overwriting the concurrent relationship

#### Scenario: non-CAS fallback is guarded

- **WHEN** the selected backend cannot provide an atomic compare-and-swap update
- **THEN** the caller must provide the observed digest and explicitly acknowledge single-writer risk, after which the CLI re-reads and proves the exact merged postcondition

#### Scenario: lost response retry is idempotent

- **WHEN** the owner write succeeded but its response was lost
- **THEN** an exact retry recognizes the complete postcondition and returns success without duplicating or removing targets

#### Scenario: partial or conflicting postcondition blocks

- **WHEN** re-observation finds only part of the requested target set or an unplanned removal
- **THEN** the operation returns a structured reconcile result and does not claim that the relationship update completed

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/392#issuecomment-5152236480

### Requirement: reverse navigation is derived from canonical relationship owners

Compact reads, status, traceability diagnostics, generated workflow guidance, and human-facing navigation MUST derive reverse typed-artifact relationships from canonical owner references without persisting a second edge on target comments. Read projections MUST remain bounded and MUST keep historical symmetric backlinks readable but non-authoritative.

#### Scenario: reverse lookup finds canonical owners

- **WHEN** a reader asks which historical or active planning artifacts refer to a target artifact
- **THEN** the read model returns the matching canonical owners without requiring the target to contain backlinks

#### Scenario: reverse lookup performs no writes

- **WHEN** status, traceability, or a UI projection constructs reverse navigation
- **THEN** the operation performs zero comment mutations and does not cache derived edges as target backlinks

#### Scenario: stale legacy backlink does not affect readiness

- **WHEN** a legacy reverse backlink is missing, extra, partial, or stale while canonical owner references are valid
- **THEN** planning authoring uses the canonical relationships and may expose the legacy difference only as bounded compatibility information; merge readiness does not read either edge

#### Scenario: generated workflows use canonical orientation

- **WHEN** issue-spec generates coordinator, implementation, and recovery guidance
- **THEN** the guidance uses owner-oriented bounded relationship commands and never instructs callers to create required bidirectional backlinks

#### Scenario: reverse output remains bounded

- **WHEN** many canonical owners refer to the same artifact
- **THEN** compact reads cap returned identities and expose a detail action without expanding complete comment bodies or performing repository-wide unbounded output

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/392#issuecomment-5152236611
