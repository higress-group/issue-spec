# typed-comment-authoring

## Purpose

Define the long-lived behavior contract for canonical typed-comment authoring and validation.

Proposal Issues:
- https://github.com/higress-group/issue-spec/issues/12

## Requirements

### Requirement: generate canonical SPEC comments from structured fields

The CLI MUST provide a command or template flow that generates canonical SPEC comment Markdown from structured fields instead of requiring agents to hand-write raw SPEC bodies.

#### Scenario: structured fields render a canonical SPEC body

- **WHEN** a caller provides structured fields for requirement title, normative requirement text, scenario title, WHEN condition, and THEN outcome
- **THEN** the CLI MUST render a SPEC body with `## Requirement:`, normative MUST or SHALL language, one or more `### Scenario:` sections, and bullet lines containing `**WHEN**` and `**THEN**`.

#### Scenario: generated body is ready for upsert

- **WHEN** the CLI generates a canonical SPEC body from structured fields
- **THEN** the generated body SHALL be accepted by `issue-spec comment upsert --type SPEC` without manual Markdown edits.

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/12#issuecomment-4850688258

### Requirement: validate canonical SPEC discipline on upsert by default

`issue-spec comment upsert --type SPEC` MUST validate canonical SPEC body discipline by default before creating or updating the remote typed comment.

#### Scenario: valid canonical SPEC is accepted

- **WHEN** `comment upsert --type SPEC` receives a body with a `## Requirement:` heading, normative MUST or SHALL text, and at least one `### Scenario:` section containing `**WHEN**` and `**THEN**` bullets
- **THEN** the CLI SHALL create or update the SPEC comment normally.

#### Scenario: malformed SPEC is rejected

- **WHEN** `comment upsert --type SPEC` receives a body that uses an ad hoc heading such as `# SPEC-001` or omits required WHEN/THEN scenario bullets
- **THEN** the CLI MUST reject the upsert by default with diagnostics that identify the missing canonical elements.

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/12#issuecomment-4850689153

### Requirement: catch malformed typed comments before archive

Typed-comment generation, upsert, workflow validation, and phase status MUST reject or report malformed typed artifacts before their owning lifecycle command consumes them. Final verification MUST validate malformed identity and representation data only when that data is part of the minimal exact-current evidence snapshot, and MUST NOT reconstruct broad Archive-readiness linting. Legacy Archive compatibility MAY diagnose malformed historical input but MUST NOT restore Archive as a readiness gate.

#### Scenario: authoring rejects malformed canonical artifacts

- **WHEN** a caller creates or updates a malformed SPEC, TASK, PROCESS, REVIEW, or VERIFY artifact
- **THEN** the owning authoring or phase-status command MUST fail with actionable canonical diagnostics

#### Scenario: final verification stays minimal

- **WHEN** a malformed historical or unrelated typed comment is outside the active exact-current evidence set
- **THEN** final verification MUST NOT add a broad Archive-style history audit

#### Scenario: legacy archive input remains diagnostic only

- **WHEN** already-started legacy Archive work reads malformed historical input during the compatibility window
- **THEN** the CLI SHALL report deprecation and focused validation without creating an Archive readiness target

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/308#issuecomment-5016452507

### Requirement: provide an explicit noncanonical migration escape hatch

The CLI MUST provide an explicit migration escape hatch, such as `--allow-noncanonical`, for cases where existing typed comments cannot be made canonical immediately.

#### Scenario: noncanonical upsert requires an explicit flag

- **WHEN** a caller attempts to upsert a noncanonical SPEC body without the migration escape hatch
- **THEN** the CLI MUST reject the upsert by default and explain that `--allow-noncanonical` or canonical regeneration is required.

#### Scenario: escape hatch preserves migration visibility

- **WHEN** a caller uses the migration escape hatch to create or update a noncanonical typed comment
- **THEN** the CLI SHALL make the noncanonical state visible in command output and later validation/status/verify diagnostics.

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/12#issuecomment-4850690808

### Requirement: generated skills direct agents to typed comment generators and validators

Generated issue-spec skills and command guidance MUST instruct agents to use CLI typed-comment generators and validators instead of hand-writing raw SPEC, TASK, PROCESS, REVIEW, or VERIFY Markdown.

#### Scenario: proposal guidance creates SPEC comments through the CLI

- **WHEN** issue-spec generates or updates coordinator guidance for proposal work
- **THEN** the guidance SHALL direct agents to generate and validate SPEC comments through the CLI before calling `comment upsert --type SPEC`.

#### Scenario: non-SPEC typed comment guidance avoids raw Markdown drift

- **WHEN** issue-spec generates or updates guidance for design, implementation, review, or verification work
- **THEN** the guidance MUST direct agents to use available CLI templates or validators for TASK, PROCESS, REVIEW, and VERIFY comments rather than inventing raw Markdown shapes.

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/12#issuecomment-4850691991
