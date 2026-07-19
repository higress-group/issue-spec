# workflow-identity-and-sessions

## Purpose

Define the long-lived behavior contract for portable workflow identity and runner resume handles in issue-spec workflows.

This durable spec is organized by stable capability surfaces rather than by the original proposal's individual SPEC comments. Future changes that extend workflow identity, generated workflow guidance, or runner resume metadata should update the relevant module below instead of appending a one-to-one copy of new proposal requirements.

Proposal Issues:
- https://github.com/higress-group/issue-spec/issues/20
- https://github.com/higress-group/issue-spec/issues/99
- https://github.com/higress-group/issue-spec/issues/247

## Requirements

### Requirement: artifact identity is portable and runtime-session-free

`Agent` is the logical workflow role or assigned label used by issue-spec artifacts. Writer commands MUST NOT read coding-agent-specific runtime session variables, require a runtime session id, or emit new artifact writer session metadata. Correctness and role separation MUST depend on logical Agent, exact revision, typed links, and trusted provider evidence instead.

Legacy `Agent Session ID` and `Agent Session Source` fields remain readable as inert compatibility data. They MUST NOT affect validation, diagnostics, gate decisions, or runner resume behavior. A deprecated `--agent-session` argument MAY remain accepted as a no-op so existing command invocations do not fail.

#### Scenario: new artifact uses portable logical identity

- **WHEN** any supported coding agent writes an issue-spec artifact with `--agent Review Agent`
- **THEN** the artifact SHALL record `Agent: Review Agent`
- **THEN** the writer SHALL NOT require or emit a runtime-specific session id or source

#### Scenario: coding-agent environment does not change output

- **WHEN** `CODEX_THREAD_ID` or another coding-agent-specific session variable is present
- **THEN** artifact rendering and gate decisions SHALL be identical to a process where that variable is absent

#### Scenario: legacy session metadata remains readable but inert

- **WHEN** an existing typed comment, rationale, finding, or reply contains legacy `Agent Session ID` or `Agent Session Source` fields
- **THEN** the artifact SHALL remain parseable without a missing, partial, or invalid-session diagnostic
- **THEN** those fields SHALL NOT satisfy, strengthen, or invalidate role identity or evidence requirements

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/20#issuecomment-4854703592
- https://github.com/higress-group/issue-spec/issues/20#issuecomment-4854795652
- https://github.com/higress-group/issue-spec/issues/20#issuecomment-4854795602

### Requirement: change-bearing executor separation uses logical Agent identity

For each agent-executed change-bearing PROCESS, the PROCESS header `Agent` identifies the coordinator and each carrier-valid PR rationale `Agent` identifies a code author. After carrier validation, implementations MUST trim surrounding whitespace and compare those logical names case-insensitively. If an author equals the coordinator, final evaluation MUST emit the blocking diagnostic `process.executor.coordinator_conflict`. If any valid rationale carrier conflicts, the PROCESS remains blocked even when other valid rationale carriers identify non-coordinator authors.

Logical-name comparison is a fail-closed backstop and MUST NOT be presented as proof that a real native child executed the work. Runtime session metadata MUST NOT participate in executor-separation comparison. Other PROCESS execution classes remain unchanged.

#### Scenario: coordinator-authored change-bearing rationale is rejected

- **WHEN** a valid rationale for a change-bearing PROCESS names the same logical agent as the PROCESS coordinator after trimming and case-folding
- **THEN** final evaluation SHALL report `process.executor.coordinator_conflict`

#### Scenario: non-coordinator author passes identity comparison

- **WHEN** every valid rationale for a change-bearing PROCESS names a logical agent different from the coordinator
- **THEN** executor-separation evaluation SHALL emit no coordinator-conflict diagnostic

#### Scenario: one conflicting carrier blocks mixed authorship

- **WHEN** valid rationale carriers include both coordinator and non-coordinator logical authors
- **THEN** the PROCESS SHALL remain blocked by `process.executor.coordinator_conflict`

#### Scenario: runtime session metadata is irrelevant

- **WHEN** logical coordinator and author identities differ but session provenance is missing, equal, or different
- **THEN** executor-separation evaluation SHALL depend only on logical `Agent` identity

Source SPEC comment:
- https://github.com/higress-group/issue-spec/issues/247

### Requirement: generated workflow guidance stays coding-agent-neutral

Generated skills, prompts, and workflow templates MUST teach coordinators and subagents to use logical Agent roles and bounded role contracts without collecting or passing coding-agent-specific runtime session ids.

Generated guidance MUST require every agent-executed change-bearing PROCESS to run in a managed workspace through a real runtime-native child whose logical `Agent` differs from the coordinator. It MUST prohibit coordinator-inline implementation, use of `independent` as an inline escape, and logical-name fabrication. The same real worker MAY execute multiple compatible PROCESS nodes when each node keeps its own lifecycle, evidence, rationale, and handoff.

Generated guidance MUST preserve per-SPEC independent review as the blocking rule and SHOULD tell coordinators to assign at least one independent reviewer for every distinct change-bearing author. One reviewer MAY cover multiple authors; the guidance MUST NOT introduce a new one-reviewer-per-author blocking gate.

#### Scenario: coordinator dispatch instructions

- **WHEN** generated issue-spec workflow instructions dispatch a worker or review subagent
- **THEN** the coordinator instruction SHALL include the sealed assignment identity, exact revision, and logical `Agent` role
- **THEN** the instruction SHALL bind the role result to the assignment and receipt identity
- **THEN** the instruction SHALL NOT require or pass a runtime-specific session id to writer commands

#### Scenario: subagent writer instructions

- **WHEN** generated worker or review-agent instructions tell a subagent to write issue-spec artifacts
- **THEN** those instructions SHALL use the logical `Agent` role and sealed assignment/receipt identity
- **THEN** those instructions SHALL NOT collect, pass, or prioritize coding-agent-specific runtime session ids
- **THEN** artifact correctness SHALL remain independent of which supported coding agent executes the role

#### Scenario: generated change-bearing instructions require a real child

- **WHEN** generated issue-spec workflow instructions dispatch agent-executed change-bearing work
- **THEN** they SHALL require a managed real non-coordinator native child and SHALL reject coordinator-inline execution or fabricated identity

#### Scenario: generated review scheduling covers distinct authors softly

- **WHEN** generated coordinator instructions schedule review PROCESS nodes
- **THEN** they SHALL recommend independent reviewer coverage for every distinct change-bearing author, allow one reviewer to cover multiple authors, and keep per-SPEC coverage as the only blocking review relation

Generated skills, slash commands, and workflow templates MUST also teach the agent-owned review boundaries: review agents author findings directly, owning workers fix and reply on finding threads directly, review agents re-check and resolve their own findings, the coordinator orchestrates only, and final rationale is a post-review-convergence step owned by workers. These instructions MUST NOT tell the coordinator to author findings, fix replies, resolutions, or rationale on another agent's behalf, and MUST NOT place rationale as a pre-review step.

#### Scenario: generated review and fix instructions are agent-owned

- **WHEN** issue-spec workflow skills or slash commands are generated or refreshed
- **THEN** the review instructions SHALL tell review agents to author findings directly with their own logical agent identity
- **THEN** the fix instructions SHALL tell owning workers to fix the affected code and reply on the original finding thread directly
- **THEN** the resolution instructions SHALL tell review agents to re-check worker replies and resolve their own findings
- **THEN** the coordinator instructions SHALL limit coordinator responsibility to scheduling, gates, state synchronization, blocker routing, and final rationale dispatch
- **THEN** the generated instructions SHALL NOT instruct the coordinator to author findings, fix replies, resolutions, or rationale on another agent's behalf

#### Scenario: generated rationale instructions are post-convergence and worker-owned

- **WHEN** issue-spec workflow skills or slash commands are generated or refreshed
- **THEN** the apply/implement instructions SHALL place final PR rationale after review convergence, owned by the worker responsible for the final code block
- **THEN** the instructions SHALL NOT direct rationale to be authored before review as coordinator-owned work

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/20#issuecomment-4854703570
- https://github.com/higress-group/issue-spec/issues/20#issuecomment-4854795594
- https://github.com/higress-group/issue-spec/issues/99#issuecomment-4885052209
- https://github.com/higress-group/issue-spec/issues/99#issuecomment-4885052362
- https://github.com/higress-group/issue-spec/issues/99#issuecomment-4885052418
- https://github.com/higress-group/issue-spec/issues/99#issuecomment-4885052466
- https://github.com/higress-group/issue-spec/issues/247

### Requirement: review findings and worker fix replies carry distinct logical owners

Review findings and worker fix replies are authored by distinct logical roles, and each role SHALL write its own artifact with its own logical agent identity. A review PROCESS owner or review agent SHALL create each PR line finding directly for the review scope it owns using `issue-spec review finding`. The worker or process owner responsible for the affected code SHALL own the fix commit and SHALL reply to the original finding thread with `issue-spec review reply`. The coordinator MUST NOT be recorded as the logical author of a finding observed by a review agent, and MUST NOT write the substantive fix reply on behalf of a worker.

#### Scenario: review agent records a scoped finding

- **WHEN** a review agent detects a problem while reviewing an implementation PR within its assigned review PROCESS scope
- **THEN** the review agent SHALL create the PR line finding with `issue-spec review finding` using its own `--agent`
- **THEN** the finding artifact SHALL preserve the review agent owner, finding id, severity, PROCESS id, SPEC id, and SPEC URL
- **THEN** the coordinator SHALL NOT create the finding using coordinator ownership metadata on behalf of the review agent

#### Scenario: blocking finding is routed to the owning worker

- **WHEN** a P0 or P1 finding is associated with code or process scope owned by a worker PROCESS
- **THEN** the coordinator SHALL dispatch that worker or an explicitly assigned fix worker rather than write the fix reply itself
- **THEN** the worker SHALL make the required code or process change and reply to the original finding thread with `issue-spec review reply` using its own `--agent`
- **THEN** the reply artifact SHALL preserve the worker owner, finding id, PROCESS id, fix evidence, and reply status

#### Scenario: worker ownership is not known

- **WHEN** a finding cannot be mapped to a worker or process owner
- **THEN** the coordinator SHALL keep the finding unresolved and record the ownership gap as a blocker for scheduling
- **THEN** status and verify SHALL NOT treat the finding as ready for resolution until an owner is assigned

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/99#issuecomment-4885052209
- https://github.com/higress-group/issue-spec/issues/99#issuecomment-4885052257

### Requirement: review agents own re-check and resolution

The review agent that owns a finding SHALL own the re-check after a worker reply and SHALL own the resolved reply or GitHub conversation resolution. A worker reply alone MUST NOT mark a review finding resolved. The resolution owner SHALL be recoverable from synced output and SHALL be distinct from the worker fix-reply owner.

#### Scenario: worker reply satisfies the finding

- **WHEN** the owning worker replies that a finding has been fixed
- **THEN** the original review agent SHALL re-check the current PR diff and relevant evidence
- **THEN** if the fix satisfies the finding, the review agent SHALL record a resolved reply or resolve the corresponding GitHub review conversation using its own agent identity
- **THEN** the resolved state SHALL preserve the review agent owner, finding id, and resolution evidence
- **THEN** review sync, status, and verify output SHALL distinguish the review-agent resolution owner from the worker fix-reply owner

#### Scenario: worker reply does not satisfy the finding

- **WHEN** the review agent re-checks a worker reply and the issue remains present or incomplete
- **THEN** the review agent SHALL leave the finding unresolved and reply with the remaining problem and required next evidence
- **THEN** status and verify SHALL continue to treat the finding as blocking when its severity is P0 or P1

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/99#issuecomment-4885052317

### Requirement: coordinator owns orchestration and gates only

The coordinator SHALL own planning, scheduling, managed workspace preparation and integration, status synchronization, unresolved blocker routing, bounded handoff, and readiness gates. The coordinator SHALL NOT implement, test, commit, or write rationale for agent-executed change-bearing work and SHALL NOT be the logical owner of review findings, worker fix replies, review resolutions, or final code rationale. Existing verify blocking on open P0/P1 findings SHALL be preserved. Genuine external or human-owned independent work, other PROCESS execution classes, and the direct one-file path remain unchanged.

#### Scenario: unresolved blocking finding exists

- **WHEN** review sync, status, or verify observes an unresolved P0 or P1 finding without review-agent resolved evidence
- **THEN** the coordinator SHALL keep the workflow blocked for readiness and dispatch the owning worker or review agent required for the next action
- **THEN** status and verify SHALL report the unresolved blocker and its logical owner metadata

#### Scenario: all blocking findings are resolved

- **WHEN** every P0 and P1 finding has worker fix-reply evidence and review-agent resolved evidence
- **THEN** the coordinator SHALL allow the workflow to advance past review convergence
- **THEN** the coordinator SHALL dispatch final rationale work to the workers that own the relevant code or process blocks

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/99#issuecomment-4885052362
- https://github.com/higress-group/issue-spec/issues/247

### Requirement: final PR rationale is post-review-convergence and worker-owned

Final PR rationale comments SHALL be generated only after review and fix convergence, and each rationale SHALL be written by the worker or process owner responsible for the final code block using `issue-spec pr rationale` with its own logical agent identity. The coordinator MUST NOT write final rationale comments on behalf of workers.

#### Scenario: review and fix convergence is complete

- **WHEN** all review findings for the implementation PR are resolved or non-blocking by verified review-agent state
- **THEN** the coordinator SHALL dispatch each relevant worker to write final PR rationale comments for owned key code blocks
- **THEN** each worker SHALL use `issue-spec pr rationale` (or the successor rationale mechanism) with worker owner, PROCESS id, SPEC id, SPEC URL, file path, and line metadata

#### Scenario: implementation may still change during review

- **WHEN** unresolved review findings remain or worker fixes are still pending
- **THEN** final PR rationale SHALL NOT be required as pre-review readiness evidence
- **THEN** any rationale-like explanation created before convergence SHALL NOT satisfy the final rationale gate

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/99#issuecomment-4885052418

### Requirement: sync, status, and verify expose the logical owner for each review artifact

`issue-spec review finding`, `issue-spec review reply`, and `issue-spec pr rationale` accept and persist the logical agent owner and SHALL remain idempotent on re-run. In addition, `issue-spec review sync`, `issue-spec status`, and `issue-spec verify` SHALL expose the logical agent owner for each finding, fix reply, resolution, and rationale.

#### Scenario: ownership metadata is preserved on write

- **WHEN** `issue-spec review finding`, `issue-spec review reply`, or `issue-spec pr rationale` creates or updates an artifact with a logical `--agent` owner
- **THEN** the logical owner SHALL be stored with the artifact
- **THEN** re-running the same command SHALL remain idempotent without dropping or overwriting the recorded logical owner

#### Scenario: ownership is recoverable from synced output

- **WHEN** `issue-spec review sync`, `issue-spec comment list`, `issue-spec status`, or `issue-spec verify` reports on findings, fix replies, resolutions, and rationale
- **THEN** the output SHALL expose the logical agent owner for each of them
- **THEN** a human or coordinator SHALL be able to recover which review agent owns each finding, which worker owns each fix reply, which review agent resolved it, and which worker owns each rationale

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/99#issuecomment-4885052257
- https://github.com/higress-group/issue-spec/issues/99#issuecomment-4885052317
- https://github.com/higress-group/issue-spec/issues/99#issuecomment-4885052466

### Requirement: runner public session id is the public resume handle

In runner mode, `public_session_id` is the public, repository-scoped handle humans use with `/resume` to continue a coordinator session. Raw acpx record ids and provider session ids are not public runner resume handles.

Coordinator-authored proposal, design, implement, handoff, and update issue bodies or comments SHOULD disclose the available runner `public_session_id` and provide concrete `/resume <public-session-id> <answer or next instruction>` guidance when runner metadata is available.

#### Scenario: coordinator-authored issue body includes resume metadata

- **WHEN** an issue-spec runner dispatches a coordinator with `runner.public_session_id=s-abc123`
- **WHEN** that coordinator creates or updates a proposal, design, implement, handoff, or update issue body
- **THEN** the body SHALL include `s-abc123` as the public runner session id
- **THEN** the body SHALL include `/resume s-abc123 <answer or next instruction>` or equivalent resume guidance

#### Scenario: internal runtime ids are not resume handles

- **WHEN** a coordinator-authored body or related typed artifact also contains a raw acpx record id or provider session id
- **THEN** that metadata SHALL be treated as internal transport metadata
- **THEN** the body MUST NOT instruct humans to use those identifiers as the runner `/resume` id

#### Scenario: non-runner workflow omits public session metadata

- **WHEN** no runner public session id is available
- **THEN** coordinator-authored bodies MAY omit runner resume metadata
- **THEN** omission of `public_session_id` SHALL NOT fail non-runner workflows by default

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/20#issuecomment-4883004527
