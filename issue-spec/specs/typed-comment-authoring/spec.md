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

Typed-comment generation, upsert, workflow validation, and phase status MUST reject or report malformed selected active planning artifacts before their owning planning command consumes them; merge-check and conditional merge MUST NOT parse, reconstruct, or be blocked by malformed historical REVIEW, VERIFY, receipt, rationale, finalization, or Archive data, while explicit audit reads MAY diagnose that inert history.

#### Scenario: selected planning authoring remains canonical

- **WHEN** a caller creates or updates a selected SPEC, TASK, or PROCESS planning artifact
- **THEN** the owning authoring or phase-status command rejects malformed canonical structure with actionable diagnostics

#### Scenario: legacy typed evidence is inert at merge

- **WHEN** a malformed historical REVIEW or VERIFY comment remains on the change
- **THEN** merge-check ignores it and uses only provider authority and configured safe fallbacks

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/405#issuecomment-5155764767

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

Generated guidance MUST use canonical generators and validators for selected SPEC, TASK, PROCESS, and QUESTION planning artifacts, MUST direct final readiness to merge-check, and MUST NOT instruct agents to generate REVIEW, VERIFY, rationale, receipt, or finalization comments.

#### Scenario: typed planning remains canonical without final verification artifacts

- **WHEN** generated guidance authors optional planning state or prepares merge readiness
- **THEN** it validates selected planning comments and invokes merge-check instead of generating review or verification Markdown

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/405#issuecomment-5155764767
