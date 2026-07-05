# workflow-configuration-and-templates

## Purpose

Define the long-lived behavior contract for project workflow configuration, schema and template resolution, issue-native artifact generation, legacy OpenSpec compatibility, workflow diagnostics, and durable archive path selection.

This durable spec is organized by stable workflow capability surfaces rather than by the original proposal's individual SPEC comments. Future changes that extend project workflow configuration, generated workflow guidance, legacy OpenSpec reuse, validation diagnostics, or archive path compatibility should update the relevant module below.

Proposal Issues:
- https://github.com/higress-group/issue-spec/issues/23

## Requirements

### Requirement: workflow configuration discovery is deterministic

The CLI MUST discover project workflow configuration from repository-local configuration before falling back to built-in defaults.

`issue-spec/config.yaml` is the preferred project workflow config. When it exists, issue-spec SHALL parse it as the active project workflow config and SHALL use its selected schema, context, rules, and per-artifact guidance for workflow-aware commands.

When `issue-spec/config.yaml` is absent and a compatible legacy OpenSpec workflow config exists at `openspec/config.yaml`, issue-spec SHALL be able to reuse that OpenSpec workflow definition as a legacy workflow source while keeping active workflow artifacts issue-native.

When neither preferred nor compatible legacy config exists, issue-spec SHALL use a built-in workflow equivalent to the default issue-spec proposal, SPEC, QUESTION, design, TASK, PROCESS, review, verify, and archive behavior.

#### Scenario: preferred issue-spec config wins

- **WHEN** both `issue-spec/config.yaml` and `openspec/config.yaml` exist
- **THEN** issue-spec SHALL use `issue-spec/config.yaml`
- **THEN** it SHALL emit diagnostics explaining that the legacy config was ignored.

#### Scenario: built-in workflow fallback

- **WHEN** neither `issue-spec/config.yaml` nor a compatible legacy workflow config exists
- **THEN** issue-spec SHALL use the built-in issue-spec workflow
- **THEN** workflow commands SHALL continue to work without repository setup.

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/23#issuecomment-4861703801
- https://github.com/higress-group/issue-spec/issues/23#issuecomment-4861704151
- https://github.com/higress-group/issue-spec/issues/23#issuecomment-4861728628

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

Custom project workflows and reused OpenSpec workflows MUST NOT reintroduce repository-local active change artifact directories.

Active proposal, design, implement, SPEC, QUESTION, TASK, PROCESS, REVIEW, VERIFY, PR rationale, PR review finding, and issue-spec link state SHALL remain in GitHub issue bodies, typed comments, PR review comments, and issue-spec links. Schema outputs such as `proposal.md`, `specs/**/*.md`, `tasks.md`, `review.md`, or `verify.md` are storage hints that issue-spec maps to supported issue-native storage.

Only durable archive output SHALL write repository spec files.

#### Scenario: file-oriented active outputs become issue-native storage

- **WHEN** a project or legacy schema artifact declares a file-oriented output such as `specs/**/*.md`, `proposal.md`, `tasks.md`, `review.md`, or `verify.md`
- **THEN** issue-spec SHALL map that artifact to supported issue-native storage such as issue bodies, typed comments, PR rationale, PR review comments, or issue-spec links
- **THEN** it SHALL NOT create or update `openspec/changes/<change>/...` or `issue-spec/changes/<change>/...` for active workflow state.

#### Scenario: apply tracking maps to typed task and process state

- **WHEN** a legacy OpenSpec schema declares `apply.tracks: tasks.md`
- **THEN** issue-spec SHALL interpret that tracking requirement as TASK, PROCESS, and issue-spec link state
- **THEN** it SHALL NOT require or update a local `tasks.md` active artifact.

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/23#issuecomment-4861704293
- https://github.com/higress-group/issue-spec/issues/23#issuecomment-4861728628

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

`issue-spec init` SHALL generate skills, slash commands, and prompts from the resolved project workflow configuration when one is available.

Generated assets SHALL include the resolved workflow source, schema, config path, template directory, non-info diagnostics, workflow context, workflow rules, and artifact instructions where available. Generated guidance SHALL preserve issue-native storage rules and the default issue-spec path family.

When no project workflow exists, generated skills and slash commands SHALL use the built-in issue-spec workflow.

#### Scenario: init uses project workflow guidance

- **WHEN** `issue-spec init --tools codex,claude --delivery both` runs in a repository with a valid project workflow schema
- **THEN** generated Codex skills, Claude skills, Claude slash commands, and Codex prompts SHALL include the resolved workflow guidance
- **THEN** generated assets SHALL state that active artifacts remain issue-native.

#### Scenario: tools none does not require workflow resolution

- **WHEN** `issue-spec init --tools none` runs
- **THEN** issue-spec SHALL skip workflow asset generation
- **THEN** invalid workflow config SHALL NOT fail the command solely because no workflow assets were requested.

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/23#issuecomment-4861704448
- https://github.com/higress-group/issue-spec/issues/23#issuecomment-4861704749

### Requirement: workflow diagnostics expose resolution and compatibility decisions

The CLI SHOULD provide diagnostics that explain workflow config, schema, template, artifact mapping, legacy compatibility, and archive path resolution decisions.

Human-readable and JSON output for workflow validation and selection commands SHALL identify the active workflow source, selected schema, resolved artifact storage mappings, validation errors, warnings, and compatibility-mode decisions.

#### Scenario: user validates active workflow

- **WHEN** a user runs `issue-spec workflow validate --repo <repo> --json`
- **THEN** issue-spec SHALL validate project config syntax, schema structure, artifact dependencies, template existence, supported artifact mappings, canonical safety, and archive path defaults
- **THEN** JSON output SHALL include diagnostics with stable severity, code, message, and relevant path or artifact context.

#### Scenario: user asks which workflow is active

- **WHEN** a user runs `issue-spec workflow which --repo <repo>`
- **THEN** issue-spec SHALL show whether the active schema came from project, legacy OpenSpec, user/global, or built-in sources
- **THEN** it SHALL report shadowed schemas and legacy compatibility choices when applicable.

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/23#issuecomment-4861703801
- https://github.com/higress-group/issue-spec/issues/23#issuecomment-4861703981
- https://github.com/higress-group/issue-spec/issues/23#issuecomment-4861704151
- https://github.com/higress-group/issue-spec/issues/23#issuecomment-4861704607
- https://github.com/higress-group/issue-spec/issues/23#issuecomment-4861705034
- https://github.com/higress-group/issue-spec/issues/23#issuecomment-4861728628

### Requirement: durable archive paths prefer issue-spec/specs while preserving legacy compatibility

The CLI SHALL default new durable archive output to `issue-spec/specs/<capability>/spec.md`.

Repositories that already store durable specs under `openspec/specs/<capability>/spec.md` remain compatible. When a matching legacy durable spec exists, issue-spec MAY select that legacy path for update and SHALL report that compatibility choice. Explicit user-supplied legacy durable paths under `openspec/specs/<capability>/spec.md` SHALL remain valid when they pass normal repository path validation.

New durable specs in repositories without matching legacy durable specs SHALL be created under `issue-spec/specs`.

#### Scenario: new durable spec uses issue-spec/specs

- **WHEN** `issue-spec archive durable-spec --repo <repo> --proposal <n> --capability <capability>` creates a durable spec and no matching legacy durable spec exists
- **THEN** the default output path SHALL be `issue-spec/specs/<capability>/spec.md`
- **THEN** generated PR descriptions, command output, and verification examples SHALL reference that path.

#### Scenario: existing legacy durable spec remains updateable

- **WHEN** archive runs for a capability and `openspec/specs/<capability>/spec.md` already exists
- **THEN** issue-spec SHALL support updating that legacy durable spec
- **THEN** it SHALL report that legacy compatibility selected the archive path.

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/23#issuecomment-4861704749
- https://github.com/higress-group/issue-spec/issues/23#issuecomment-4861704910
- https://github.com/higress-group/issue-spec/issues/23#issuecomment-4861728628
