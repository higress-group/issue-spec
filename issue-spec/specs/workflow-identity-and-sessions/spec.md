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

### Requirement: generated workflow guidance stays coding-agent-neutral

Generated guidance MUST select execution mode before assigning writers. Once Design or TASK is selected or the user requests delegation, it MUST prohibit coordinator code changes on delegated and managed paths. Without managed PROCESS, it MUST require exactly one real non-coordinator worker for the bounded implementation and MUST NOT require Implement or PROCESS merely for delegation. With managed PROCESS, it MUST assign exactly one real non-coordinator owner to every change-bearing work package/PROCESS and MAY permit concurrent workers for distinct safe packages. It MUST limit direct coordinator edits to a narrow direct-PR fast path with no selected Design/TASK and no delegation request. It MUST state that child use and file count alone do not select PROCESS, and file count does not select the coordinator exception. It MUST select optional managed PROCESS only for concrete concurrent-write, isolation, ownership-enforcement, restart-recovery, or dependency-integration needs. It MUST make each worker own one package's code, focused tests, exact result commit, decisions, risks, and zero or more stable-anchor line-rationale drafts for non-obvious decisions; it MUST assign dispatch/wait, exact-commit inspection, integration, proportionate final validation, anchor validation, and provider publication to the coordinator. It MUST default to an ordinary provider-native `### Implementation Rationale` summary/index, report project checks, exact head, PR or MR link, risks, and limitations, stop before human approval or merge, keep coding-agent session identity out of authority, MAY describe real-child workspace safety for selected PROCESS execution, and MUST NOT instruct rationale quotas, writer-owned provider positions, REVIEW/VERIFY PROCESS, role receipts, review sync, verify submit, mandatory rationale evidence, coverage, finalization, readiness, merge execution, or post-merge reconciliation.

#### Scenario: one generated model

- **WHEN** built-in skills and prompts are generated after the cutover
- **THEN** they present only the exact-head human handoff model and no legacy compatibility stages

#### Scenario: one child does not manufacture planning state

- **WHEN** generated guidance delegates a bounded implementation with selected Design/TASK to one code-writing child without a managed-coordination need
- **THEN** it keeps one non-coordinator writer active and creates no Implement, PROCESS, workspace lease, role receipt, or merge evidence merely for the delegation

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/417#issuecomment-5165960908

### Requirement: generated guidance converges reviewer findings by severity

Before human handoff, generated workflow guidance MUST dispatch one real read-only reviewer independent of every actual code writer against the exact base and current exact head, without a write path or provider credentials. The reviewer MUST classify actionable findings as P0, P1, or P2 and provide stable changed-line anchors. The coordinator MUST route every P0/P1 unchanged to the original writer that owns the affected code. That writer MUST repair the finding, run focused tests, and return a new exact commit; after integration and push, the same reviewer MUST recheck the new exact head. The workflow MUST repeat this loop automatically until the reviewer reports zero P0/P1.

The workflow MUST retain only still-applicable P2 findings from the final reviewed head and MUST publish each unchanged as a provider-native non-blocking line comment when an approved provider tool supports safe line coordinates. Otherwise it MUST use the provider-neutral ordinary `change.comment` operation and preserve `path:symbol/line`. P2 MUST NOT enter the repair loop or pause completion. When P2 publication is unavailable or fails, the workflow MUST report the rendered comment body and continue. Finding convergence MUST NOT require PROCESS without a separate managed-coordination need and MUST NOT create typed REVIEW/VERIFY, finding evidence, receipts, readiness gates, provider approval, or merge authority.

#### Scenario: P1 automatically returns to its original writer

- **WHEN** the independent reviewer reports a P1 against the current exact head
- **THEN** the coordinator routes the unchanged finding to the original writer that owns the affected code
- **THEN** that writer repairs and tests a new exact commit
- **THEN** the same reviewer rechecks the new exact head
- **THEN** the workflow repeats until no P0/P1 remains

#### Scenario: P2 is visible and non-blocking

- **WHEN** the final reviewed exact head has a still-applicable P2
- **THEN** the workflow publishes the unchanged finding as a non-blocking provider comment
- **THEN** the workflow continues to human handoff without waiting for a repair or human confirmation

#### Scenario: provider has no safe line-comment coordinates

- **WHEN** the selected code provider cannot safely publish a non-blocking line comment
- **THEN** the workflow publishes an ordinary change-level `change.comment` that preserves `path:symbol/line`
- **THEN** publication failure is reported with the rendered body and does not pause completion

Source issue:
- https://github.com/higress-group/issue-spec/issues/429

### Requirement: coordinator owns orchestration and gates only

The coordinator MUST own only selected planning, scheduling, workspace preparation and integration, exact-head anchor/applicability/sensitive-data validation and provider publication of worker-authored line rationale, the ordinary provider-native `### Implementation Rationale` summary/index, project validation summary, PR or MR handoff, verbatim P0/P1 writer-reviewer routing, non-blocking P2 provider publication, blocker routing, and bounded handoff. It MUST return or explain dropping invalid, stale, or sensitive drafts and MUST NOT rewrite the actual code writers' line rationale or reviewer findings, impersonate workers or reviewers, own review sync, verify, legacy rationale evidence, finding resolution, evidence publication, compute merge readiness, approve, or merge.

#### Scenario: coordinator routes but does not create provider policy

- **WHEN** a provider finding or required check blocks the exact subject during human review
- **THEN** the coordinator may route implementation or reviewer work and refresh the exact-head handoff without authoring a merge decision

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/417#issuecomment-5165960908

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
