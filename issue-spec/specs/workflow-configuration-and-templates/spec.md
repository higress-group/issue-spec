# workflow-configuration-and-templates

## Purpose

Define the long-lived behavior contract for project workflow configuration, schema and template resolution, issue-native artifact generation, legacy OpenSpec compatibility, workflow diagnostics, and durable-spec path selection.

This durable spec is organized by stable workflow capability surfaces rather than by the original proposal's individual SPEC comments. Future changes that extend project workflow configuration, generated workflow guidance, legacy OpenSpec reuse, validation diagnostics, or durable-spec path compatibility should update the relevant module below.

Proposal Issues:
- https://github.com/higress-group/issue-spec/issues/23
- https://github.com/higress-group/issue-spec/issues/241

## Requirements

### Requirement: workflow configuration discovery is deterministic

The CLI MUST resolve repository-local issue-spec workflow configuration before compatible legacy configuration and built-in defaults, MUST preserve explicit durable_specs mode selection, and the built-in fallback MUST provide a bounded simple-Issue path plus optional Proposal, SPEC, QUESTION, Design, TASK, and PROCESS planning with provider review, read-only merge-check, and conditional merge, without required REVIEW, VERIFY, final verification, or Archive behavior.

#### Scenario: built-in workflow uses the minimal authority model

- **WHEN** no project or compatible legacy workflow configuration exists
- **THEN** the CLI selects the built-in issue-spec workflow with optional planning and provider merge authority but no REVIEW, VERIFY, final-verification, or Archive stage

#### Scenario: repository configuration still wins deterministically

- **WHEN** both issue-spec/config.yaml and compatible legacy configuration exist
- **THEN** the CLI selects issue-spec/config.yaml and reports the legacy source shadowed before generating assets

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/405#issuecomment-5155764767

### Requirement: schema and template resolution is safe and explainable

The CLI MUST resolve workflow schemas and artifact templates deterministically from project-local, user/global, legacy, and built-in sources.

Project schemas live under `issue-spec/schemas/<schema>/schema.yaml` with templates in `templates/*.md`. Legacy OpenSpec schemas MAY be read from `openspec/schemas/<schema>/schema.yaml` only in compatibility mode. Template paths MUST resolve within the selected schema template directory.

If a selected schema cannot be found, has unsupported artifact types, references missing dependencies, creates a dependency cycle, references missing templates, or uses unsafe template paths, issue-spec SHALL reject the workflow before creating or updating GitHub workflow state or generated local workflow assets.

#### Scenario: project schema shadows lower-priority schemas

- **WHEN** the selected schema name exists in `issue-spec/schemas/<schema>/schema.yaml`
- **THEN** issue-spec SHALL use that project-local schema
- **THEN** lower-priority user/global, legacy, or built-in schemas with the same name SHALL be treated as shadowed.

#### Scenario: artifact template is resolved inside schema templates

- **WHEN** a resolved artifact declares `template: proposal.md`
- **THEN** issue-spec SHALL resolve the template relative to the selected schema's template directory
- **THEN** the generated issue body, typed comment, skill, slash command, or prompt SHALL use that resolved template when the artifact is scaffolded.

#### Scenario: unsafe schema cannot write artifacts

- **WHEN** a schema artifact template path is absolute, escapes the template directory, or points to a missing file
- **THEN** issue-spec SHALL fail validation with the affected artifact id and path
- **THEN** it SHALL NOT write GitHub issues, comments, PR comments, or generated workflow assets from that template.

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/23#issuecomment-4861703981
- https://github.com/higress-group/issue-spec/issues/23#issuecomment-4861704151
- https://github.com/higress-group/issue-spec/issues/23#issuecomment-4861704607
- https://github.com/higress-group/issue-spec/issues/23#issuecomment-4861705034
- https://github.com/higress-group/issue-spec/issues/23#issuecomment-4861728628

### Requirement: active workflow artifacts remain issue-native

Selected Proposal, Design, SPEC, QUESTION, TASK, PROCESS, and handoff artifacts MUST remain issue-native optional planning state, repository durable materialization MAY update only declared durable paths on the implementation branch, and provider checks, review decisions, findings, conversations, and merge readiness MUST remain provider authority rather than being projected into REVIEW, VERIFY, rationale evidence, receipt, or finalization artifacts. An ordinary provider-native `### Implementation Rationale` discussion MAY explain the implementation to human reviewers but MUST NOT be treated as one of those artifacts or authority inputs.

#### Scenario: issue-native planning does not become merge evidence

- **WHEN** a change uses optional planning artifacts and repository durable projection
- **THEN** the artifacts remain readable and durable files merge with code while merge-check ignores their lifecycle and consumes provider authority directly

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/405#issuecomment-5155764767

### Requirement: project templates cannot weaken issue-spec validation

Project templates MAY customize generated issue bodies, typed comment bodies, skills, slash commands, and prompts, but they MUST remain declarative and subordinate to issue-spec's canonical validation rules.

Template rendering MUST NOT weaken typed comment wrapping, canonical SPEC validation, issue body markers, artifact writer metadata, issue-native storage rules, or repository path safety. Rendered SPEC comments MUST still satisfy canonical SPEC discipline before they can be written or used as archive source material.

#### Scenario: custom SPEC template output is canonical

- **WHEN** a project workflow template renders a SPEC typed comment body
- **THEN** issue-spec SHALL wrap it as a typed SPEC comment with the requested id and metadata
- **THEN** issue-spec SHALL validate the rendered body for `## Requirement:`, normative MUST or SHALL language, at least one `### Scenario:`, and `**WHEN**`/`**THEN**` scenario bullets before accepting it.

#### Scenario: unsupported template behavior is diagnostic

- **WHEN** a reused template or schema instruction depends on unsupported local active-change files, OpenSpec-only commands, or unsupported artifact fields
- **THEN** issue-spec SHALL warn or fail with clear diagnostics before writing GitHub workflow state
- **THEN** it SHALL NOT silently follow instructions that would break issue-native active storage.

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/23#issuecomment-4861704607
- https://github.com/higress-group/issue-spec/issues/23#issuecomment-4861705034
- https://github.com/higress-group/issue-spec/issues/23#issuecomment-4861728628

### Requirement: init-generated workflow assets reflect the resolved workflow

Generated assets MUST reflect the selected schema, minimal merge authorities, optional planning aids, required provider capabilities, and pinned release identity, and MUST NOT generate legacy evidence stages or dual-mode guidance.

#### Scenario: generated assets bind the cutover

- **WHEN** init resolves the built-in issue-spec schema and a compatible v1 provider
- **THEN** all generated tools describe the same capability-gated minimal workflow and exact release set

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/405#issuecomment-5155764767

### Requirement: workflow diagnostics expose resolution and compatibility decisions

Workflow validation and selection diagnostics MUST explain active config/schema/template resolution, issue-native storage mappings, durable_specs mode, canonical or legacy-existing durable path choices, and bounded compatibility decisions. Legacy Archive input MUST emit stable machine-readable deprecation and replacement guidance without adding an Archive readiness target or generated route.

#### Scenario: workflow validation reports durable policy

- **WHEN** a user validates a selected workflow
- **THEN** JSON diagnostics SHALL identify the workflow source, schema, storage mappings, durable_specs mode, validation errors, and compatibility decisions

#### Scenario: legacy archive schema is deprecated only

- **WHEN** a legacy custom schema still declares an archive artifact during the compatibility window
- **THEN** the CLI SHALL report a stable deprecation and replacement command without restoring Archive readiness

#### Scenario: legacy durable path choice is visible

- **WHEN** an already-existing openspec/specs target is selected
- **THEN** the CLI SHALL report the exact compatibility path decision

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/308#issuecomment-5016452933

### Requirement: durable archive paths prefer issue-spec/specs while preserving legacy compatibility

Repository durable-spec operations MUST target issue-spec/specs/<capability>/spec.md by default. An already-existing openspec/specs/<capability>/spec.md target MAY remain updateable as explicit compatibility, but new legacy paths MUST NOT be created. The durable-spec preview/apply/check protocol MUST use exact declared paths and MUST NOT rely on post-merge Archive path inference.

#### Scenario: new durable target uses issue-spec specs

- **WHEN** an authorized ADDED operation targets a capability without an existing legacy durable file
- **THEN** the operation MUST use issue-spec/specs/<capability>/spec.md

#### Scenario: existing legacy durable target remains explicit

- **WHEN** openspec/specs/<capability>/spec.md already exists and the confirmed operation names that exact path
- **THEN** preview MAY accept it and SHALL report the compatibility choice

#### Scenario: archive path inference is absent

- **WHEN** durable materialization runs for an implementation change
- **THEN** the CLI MUST consume exact confirmed operation paths and MUST NOT create or select a post-merge Archive target

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/308#issuecomment-5016452933

### Requirement: repository HTML review authoring policy is strict and default-enabled

The selected workflow configuration MAY control HTML review authoring with a strict enabled boolean and MUST fail before writes on malformed or unknown policy; disabling HTML review MUST NOT weaken selected typed planning authoring, QUESTION discovery, optional PROCESS safety, provider-native independent review, or read-only merge-check, and MUST NOT preserve typed REVIEW, VERIFY, or final-verification obligations.

#### Scenario: disabled HTML review removes only presentation authoring

- **WHEN** html_review.enabled is false
- **THEN** generated assets omit HTML review projections and stale managed references while preserving provider review and merge-check without typed REVIEW or VERIFY duties

#### Scenario: invalid HTML review policy fails before generation

- **WHEN** the html_review mapping is malformed or contains unknown fields
- **THEN** workflow resolution reports invalid_config and writes no generated asset or phase issue

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/405#issuecomment-5155764767

### Requirement: projects declare required check identities without verifier artifacts

Projects MUST express mechanical business policy as configured required check identities and MUST NOT create verifier assignments, VERIFY artifacts, role receipts, or executable natural-language conclusions.

#### Scenario: project policy selects an objective check

- **WHEN** a business rule needs deterministic merge enforcement
- **THEN** configuration names the provider or trusted external check whose current exact-subject conclusion merge-check consumes

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/405#issuecomment-5155764758

### Requirement: deprecated evidence writers fail with zero mutation

Legacy review sync, verify submit, evidence-only PROCESS completion, rationale-gate publication, and evidence-carrier finalization commands MUST return deprecated_workflow with focused replacement guidance and MUST perform zero local, issue, code-provider, or relationship writes. This deprecation MUST NOT prohibit an ordinary provider-native `### Implementation Rationale` discussion that contains no legacy carrier, identity, relationship, evidence, or gate semantics.

#### Scenario: deprecated command cannot preserve old authority

- **WHEN** a caller invokes a removed evidence writer after cutover
- **THEN** the command fails before mutation and directs the caller to provider checks, review, or merge-check as applicable

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/405#issuecomment-5155764767
