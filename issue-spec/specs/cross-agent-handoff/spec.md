# cross-agent-handoff

## Purpose

Durable GitHub artifacts must be sufficient for a fresh agent with no shared session context to reconstruct both the state and the understanding of the work. This capability has two complementary halves: the write-side self-contained authoring contract (proposal #121), captured by the requirements below, ensures artifacts contain the environment-independent background, assumptions, decisions, and rejected alternatives a context-less reader needs; the read-side remote-authoritative reconstruction contract (#11/#116) ensures a fresh agent reconstructs current state from those artifacts. This actor-to-actor resume of understanding is distinct from the process-dag ### Handoff serial-chain completion evidence and from /resume session handles.

Proposal Issues:
- https://github.com/higress-group/issue-spec/issues/121

## Requirements

### Requirement: generated guidance states the self-contained authoring invariant

Generated issue-spec coordinator guidance and workflow skills MUST state a self-contained authoring invariant instructing agents that proposal, design, SPEC, and TASK artifacts SHALL be written for a reader who shares no local session context, capturing environment-independent background, assumptions, decisions, and rejected alternatives with rationale.

#### Scenario: coordinator guidance asserts the invariant

- **WHEN** issue-spec generates coordinator guidance or workflow skills for authoring proposal, design, SPEC, or TASK artifacts
- **THEN** the generated guidance SHALL instruct the agent to externalize environment-independent background, assumptions, decisions, and rationale so a reader with no shared session context can resume the work

#### Scenario: invariant stays distinct from serial handoff evidence and resume handles

- **WHEN** generated guidance references the self-contained authoring invariant
- **THEN** it MUST keep the invariant distinct from the existing `### Handoff` PROCESS serial-chain evidence section and from `/resume` session handles

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/121#issuecomment-4888785064

### Requirement: authored proposal and design artifacts capture environment-independent background

When an agent authors or updates a proposal or design issue body, it MUST populate the background and context sections with environment-independent knowledge — problem framing, discovered domain facts and constraints, assumptions in force, and decisions with rejected alternatives and rationale — and MUST NOT leave placeholder text in required background sections; the requirement SHALL be scoped to environment-independent knowledge and MUST NOT require local paths or session-specific state.

#### Scenario: authored proposal externalizes background and rationale

- **WHEN** a proposal or design body is authored or updated for a change
- **THEN** the body SHALL contain the environment-independent background, assumptions, and rationale a context-less reader needs to resume, rather than placeholder text in required sections

#### Scenario: environment-specific detail is not required

- **WHEN** authoring guidance directs an agent to record background
- **THEN** the guidance MUST scope the requirement to environment-independent knowledge and MUST NOT require local filesystem paths or session-specific state to be recorded

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/121#issuecomment-4888785167

### Requirement: templates prompt for self-contained context instead of bare placeholders

issue-spec proposal and design issue templates MUST replace bare `TBD` placeholders in background and context-oriented sections with author-facing prompts that SHALL direct the author to supply environment-independent, self-contained content for a reader with no shared session context.

#### Scenario: generated proposal template prompts for self-contained background

- **WHEN** issue-spec generates a new proposal or design issue body
- **THEN** the background and context-oriented sections SHALL contain author-facing prompts describing the self-contained, environment-independent content expected rather than a bare `TBD`

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/121#issuecomment-4888785732

### Requirement: authoring-completeness diagnostics stay advisory and non-blocking

issue-spec status and verify MAY emit an advisory diagnostic when a required background or context section is empty or still contains placeholder text such as a literal `TBD`, but such a diagnostic MUST NOT fail a stage gate or block the workflow on subjective judgments of depth or richness.

#### Scenario: placeholder background produces an advisory diagnostic

- **WHEN** status or verify evaluates a proposal or design whose required background or context section is empty or contains literal placeholder text such as `TBD`
- **THEN** it SHALL report an advisory diagnostic that identifies the section and the affected issue

#### Scenario: advisory diagnostic does not block gates

- **WHEN** the only outstanding signal for a change is an advisory authoring-completeness diagnostic
- **THEN** status and verify MUST NOT fail the corresponding stage gate or block the workflow on that diagnostic alone

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/121#issuecomment-4888785840
