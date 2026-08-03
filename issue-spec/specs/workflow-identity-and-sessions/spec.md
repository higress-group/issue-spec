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

Generated guidance MUST default bounded implementation to one code writer, MUST permit one code-writing child or subagent without PROCESS, and MUST state that child use alone does not select PROCESS. It MUST select optional managed PROCESS only for concrete concurrent-write, isolation, ownership-enforcement, restart-recovery, or dependency-integration needs. It MUST make the actual code writer own zero or more stable-anchor line-rationale drafts for non-obvious decisions, assign exact-head anchor validation and provider publication to the coordinator, default to an ordinary provider-native `### Implementation Rationale` summary/index, report project checks, exact head, PR or MR link, risks, and limitations, stop before human approval or merge, keep coding-agent session identity out of authority, MAY describe real-child workspace safety for selected PROCESS execution, and MUST NOT instruct rationale quotas, writer-owned provider positions, REVIEW/VERIFY PROCESS, role receipts, review sync, verify submit, mandatory rationale evidence, coverage, finalization, readiness, merge execution, or post-merge reconciliation.

#### Scenario: one generated model

- **WHEN** built-in skills and prompts are generated after the cutover
- **THEN** they present only the exact-head human handoff model and no legacy compatibility stages

#### Scenario: one child does not manufacture planning state

- **WHEN** generated guidance delegates a bounded implementation to one code-writing child without a managed-coordination need
- **THEN** it keeps one writer active and creates no TASK, PROCESS, workspace lease, role receipt, or merge evidence for the delegation

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/417#issuecomment-5165960908

### Requirement: coordinator owns orchestration and gates only

The coordinator MUST own only selected planning, scheduling, workspace preparation and integration, exact-head anchor/applicability/sensitive-data validation and provider publication of worker-authored line rationale, the ordinary provider-native `### Implementation Rationale` summary/index, project validation summary, PR or MR handoff, blocker routing, and bounded handoff. It MUST return or explain dropping invalid, stale, or sensitive drafts and MUST NOT rewrite the actual code writers' line rationale, impersonate workers or reviewers, own review sync, verify, legacy rationale evidence, finding resolution, evidence publication, compute merge readiness, approve, or merge.

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
