# gh-cli-proxy

## Purpose

Document the long-lived behavior contract for reusing GitHub CLI authentication and proxying GitHub API operations through the gh backend while preserving REST fallback behavior.

Proposal Issues:
- https://github.com/higress-group/issue-spec/issues/9

## Requirements

### Requirement: reuse GitHub CLI authentication

When `gh` is installed and authenticated for the target host, issue-spec MUST be able to use that authentication without requiring an issue-spec-specific token.

#### Scenario: gh authenticated and no issue-spec token

- **WHEN** `gh auth status` succeeds for the target host and issue-spec has no `ISSUE_SPEC_TOKEN`, `GH_TOKEN`, `GITHUB_TOKEN`, keyring token, or config token
- **THEN** issue-spec MUST be able to authenticate GitHub operations through `gh`.

#### Scenario: auth status diagnostics

- **WHEN** issue-spec resolves authentication through `gh`
- **THEN** `issue-spec auth status --json` MUST report a source or backend that makes the `gh` usage visible.

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/9#issuecomment-4850077653

### Requirement: proxy GitHub API operations through gh

Issue-spec MUST support GitHub issue, comment, file, change-navigation, and PR creation operations through the gh backend with the same user-visible semantics as the REST backend, and MUST NOT require either backend to normalize review policy, compute merge readiness, issue authority tokens, or execute merge.

#### Scenario: gh and REST reach the same human handoff

- **WHEN** a workflow uses either GitHub backend for the same exact pull-request head
- **THEN** both expose the change and ordinary review context without creating workflow evidence or a merge decision

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/417#issuecomment-5165960908

### Requirement: debuggable backend selection

Issue-spec MUST make GitHub backend selection deterministic and debuggable.

#### Scenario: explicit override

- **WHEN** a user or CI job needs a specific GitHub backend
- **THEN** issue-spec SHOULD provide a way to force `rest` or `gh` behavior.

#### Scenario: missing gh

- **WHEN** `gh` is not installed or not authenticated
- **THEN** issue-spec MUST continue to support the existing direct REST token flow.

#### Scenario: command failures

- **WHEN** a proxied `gh` command fails
- **THEN** issue-spec MUST return an error that includes enough command/endpoint context for debugging without leaking tokens.

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/9#issuecomment-4850077678

### Requirement: generic external CLI backend abstraction

Issue-spec MUST define the CLI-backed GitHub integration through a backend abstraction that is generic enough to support external CLI backends beyond `gh`. The `gh` backend SHALL be implemented as one external CLI backend, not as a hard-coded special case embedded throughout command logic.

#### Scenario: adding another CLI backend

- **WHEN** issue-spec adds support for another provider CLI such as `glab`
- **THEN** the backend abstraction MUST allow that backend to implement provider operations through the same selection, execution, error handling, and diagnostics boundaries without rewriting planning, provider-authority, or merge orchestration.

#### Scenario: selecting the gh backend

- **WHEN** issue-spec selects the `gh` backend for GitHub operations
- **THEN** issue-spec SHALL identify it as a configured external CLI backend and MUST avoid assuming `gh` is the only possible CLI-backed mode.

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/9#issuecomment-4851163542
