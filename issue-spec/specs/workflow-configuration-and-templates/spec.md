# workflow-configuration-and-templates

## Purpose

Define the long-lived behavior contract for project workflow configuration, schema and template resolution, issue-native artifact generation, legacy OpenSpec compatibility, workflow diagnostics, and durable archive path selection.

This durable spec is organized by stable workflow capability surfaces rather than by the original proposal's individual SPEC comments. Future changes that extend project workflow configuration, generated workflow guidance, legacy OpenSpec reuse, validation diagnostics, or archive path compatibility should update the relevant module below.

Proposal Issues:
- https://github.com/higress-group/issue-spec/issues/23
- https://github.com/higress-group/issue-spec/issues/241

## Requirements

### Requirement: workflow configuration discovery is deterministic

The CLI MUST resolve repository-local issue-spec workflow configuration before compatible legacy OpenSpec configuration and built-in defaults. The selected workflow MAY explicitly set durable_specs.mode to repository; absence MUST remain mode none. The built-in fallback MUST include proposal, SPEC, QUESTION, design, TASK, PROCESS, REVIEW, and VERIFY behavior without an Archive artifact or readiness target.

#### Scenario: preferred issue-spec config wins

- **WHEN** both issue-spec/config.yaml and a compatible legacy OpenSpec config exist
- **THEN** the CLI SHALL select issue-spec/config.yaml and report that the legacy source was shadowed

#### Scenario: built-in workflow has no archive stage

- **WHEN** no project or compatible legacy workflow config exists
- **THEN** the CLI SHALL use the built-in issue-spec workflow without an Archive artifact or readiness target

#### Scenario: durable repository policy is explicit

- **WHEN** a repository requires same-PR durable contracts
- **THEN** its selected workflow MUST opt in with durable_specs.mode repository; absence remains lightweight mode none

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/308#issuecomment-5016452933

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

Active proposal, design, implement, SPEC, QUESTION, TASK, PROCESS, REVIEW, VERIFY, rationale, finding, and link state MUST remain issue-native. A repository-mode durable materializer MAY update declared durable spec files on the implementation branch before review, but MUST NOT create repository-local active change directories or a post-merge Archive change. Merge publishes implementation and durable contract atomically.

#### Scenario: file-oriented active outputs remain issue-native

- **WHEN** a project or compatible legacy schema declares proposal, task, review, verify, or change-oriented file outputs
- **THEN** the CLI SHALL map active state to issue-native storage and MUST NOT create issue-spec/changes or openspec/changes directories

#### Scenario: durable files change only in implementation

- **WHEN** confirmed SPEC operations authorize repository durable changes
- **THEN** the dedicated materializer SHALL update only declared durable spec paths on the implementation branch before final review

#### Scenario: merge needs no archive change

- **WHEN** the implementation and exact durable projection pass final review and verification
- **THEN** one merge SHALL publish both and no second Archive pull request is required

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/308#issuecomment-5016452933

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

`issue-spec init` SHALL generate repository-scoped skills and slash commands from the resolved project workflow configuration when one is available. It MUST NOT write user-global Codex prompts unless the caller explicitly requests global prompt installation.

Generated assets SHALL include the resolved workflow source, schema, config path, template directory, non-info diagnostics, workflow context, workflow rules, and artifact instructions where available. Repository artifacts and explicitly installed user-global prompts SHALL use the same rendered command source. Generated guidance SHALL preserve issue-native storage rules and the default issue-spec path family.

When no project workflow exists, generated skills and slash commands SHALL use the built-in issue-spec workflow.

When the caller explicitly selects `--tools none`, init MUST remain workflow-neutral: it MUST NOT read, validate, create, or modify workflow-selection files or generated workflow artifacts. It MUST preserve existing issue-spec and legacy OpenSpec workflow configuration byte-for-byte while still initializing issue-spec-owned runtime metadata.

#### Scenario: init uses project workflow guidance

- **WHEN** `issue-spec init --tools codex,claude --delivery both` runs in a repository with a valid project workflow schema
- **THEN** generated Codex skills, Claude skills, and Claude slash commands SHALL include the resolved workflow guidance
- **THEN** user-global Codex prompts SHALL remain unchanged
- **THEN** generated assets SHALL state that active artifacts remain issue-native.

#### Scenario: explicit global prompt installation is isolated and previewable

- **WHEN** init explicitly enables global prompt installation with an optional destination directory
- **THEN** every planned user-global prompt path SHALL be reported as an absolute path and installed only in the selected directory
- **THEN** a global prompt dry-run SHALL report the same paths without writing them
- **THEN** installed prompts SHALL contain the same resolved workflow guidance as repository command artifacts.

#### Scenario: Fresh repository with language and provider options

- **WHEN** initialization runs with `--tools none` while a language and external-code provider are selected
- **THEN** the CLI writes runtime metadata under `.issue-spec`, records provider identity and capabilities, reports the language as not applied, and creates no `issue-spec/config.yaml`, `.agents`, `.claude`, `.codex`, repository command, or global-prompt artifact

#### Scenario: Repository already uses OpenSpec

- **WHEN** a repository containing only `openspec/config.yaml` is initialized with `--tools none`
- **THEN** the OpenSpec file remains byte-for-byte unchanged and subsequent issue-spec workflow discovery still selects legacy OpenSpec compatibility mode

#### Scenario: Existing issue-spec workflow remains operator-owned

- **WHEN** a repository already contains `issue-spec/config.yaml` and initialization runs with `--tools none`
- **THEN** the CLI leaves that file byte-for-byte unchanged and does not validate or merge language or provider workflow settings into it

#### Scenario: Runtime metadata remains available

- **WHEN** workflow-neutral initialization completes successfully
- **THEN** `.issue-spec/config.json` still records the selected profile, repository, server or realm identity, source binding, provider identity, and provider capabilities needed by runtime operations

#### Scenario: Global prompt installation conflicts with tools none

- **WHEN** a caller combines `--tools none` with an explicit global-prompt installation option
- **THEN** the CLI rejects the arguments before local or remote mutation with an actionable error

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/23#issuecomment-4861704448
- https://github.com/higress-group/issue-spec/issues/23#issuecomment-4861704749
- https://github.com/higress-group/issue-spec/issues/241#issuecomment-4990799840

Source issue:
- https://github.com/higress-group/issue-spec/issues/189

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

### Requirement: project-defined business verification remains declarative

Projects MUST define business verification through workflow context, rules.verify, verifier artifact instructions and templates, exact required tests, and provider-owned required checks. The core CLI MUST preserve selector identity, exact subject revision, outcome, and assurance without executing project prose, adding a project plugin runtime, or upgrading natural-language conclusions into provider authority.

#### Scenario: custom verification guidance is sealed

- **WHEN** a project declares business verification rules in its selected workflow
- **THEN** the verifier assignment SHALL carry bounded applicable guidance and exact affected scenarios without adding binary semantics

#### Scenario: deterministic rules use required evidence

- **WHEN** a business rule needs mechanical enforcement
- **THEN** the project MUST express it as an exact required test or provider-owned check whose identity, revision, and outcome are validated

#### Scenario: prose retains role-owned assurance

- **WHEN** a verifier records a natural-language project conclusion
- **THEN** the conclusion MUST retain its ordinary role-owned assurance and MUST NOT impersonate provider-owned evidence

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/308#issuecomment-5016452732

### Requirement: repository HTML review authoring policy is strict and default-enabled

The selected workflow configuration MAY contain a top-level html_review mapping with the single required boolean enabled. The CLI MUST resolve an absent mapping as enabled for backward compatibility, MUST require explicit false to opt out, and MUST reject malformed or unknown configuration before writing workflow assets or creating an issue. The resolved policy MUST control generated workflow skills, commands, prompts, the managed human-review reference resource, and built-in Proposal, Design, and Implement issue-body guidance. Disabling HTML review MUST NOT weaken typed artifact authoring, QUESTION discovery, PROCESS planning, independent code review, final verification, or the HTML preview runtime itself.

#### Scenario: missing HTML review setting preserves compatibility

- **WHEN** the selected workflow configuration omits html_review
- **THEN** workflow generation and built-in issue bodies retain the enabled HTML review authoring checkpoints and reference resource

#### Scenario: disabled HTML review omits authoring load

- **WHEN** the selected workflow configures html_review.enabled to false
- **THEN** generated workflow assets and built-in phase bodies omit HTML review projection instructions, references, and sections, while stale exact managed reference files are pruned and typed workflow review obligations remain

#### Scenario: invalid HTML review configuration fails closed

- **WHEN** html_review is a scalar, omits enabled, gives enabled a non-boolean value, or contains an unknown field
- **THEN** workflow resolution reports invalid_config and does not write generated assets or create a phase issue

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/384#issuecomment-5151712448
