# issue-api-compatibility

## Purpose

Define the long-lived behavior contract for this capability.

Proposal Issues:
- https://github.com/higress-group/issue-spec/issues/160
- https://github.com/higress-group/issue-spec/issues/241

## Requirements

### Requirement: GitHub-compatible issue REST surface with an explicit compatibility boundary

The server MUST expose the documented GitHub-compatible issue API required by the issue-spec CLI, issue UI, and runner recovery path, MUST preserve the response fields and protocol metadata consumed by existing clients, and MUST treat pull-request and notifications capabilities as explicitly separate backend capabilities rather than pretending the existing runner is unchanged.

#### Scenario: issue, comment, label and reaction operations use compatible shapes

- **WHEN** a client creates, lists, gets, updates, or closes issues; lists, gets, creates, or updates comments; manages labels or issue-label assignments; or lists, adds, or removes comment reactions
- **THEN** the server returns GitHub-compatible status codes and JSON fields including stable ids, user.login, timestamps, issue_url, html_url, url, labels, reactions, body, title and state

#### Scenario: runner recovery endpoints are complete

- **WHEN** runner serve reconciles a delivered comment or an operator performs a repository-comments recovery scan
- **THEN** single-comment GET, per-issue comments, repository-wide comments, collaborator permission and repository subscription endpoints MUST be available without requiring a notifications endpoint

#### Scenario: pagination and conditional requests preserve cursor semantics

- **WHEN** a caller supplies per_page/page, since, If-None-Match or If-Modified-Since on a supported list or issue resource
- **THEN** the server returns deterministic ordering, same-origin RFC5988 Link headers, representation-correct ETag and Last-Modified values, rate-limit metadata and a bodyless 304 when unchanged

#### Scenario: generated URLs use configured canonical origins

- **WHEN** the server emits API URLs, web URLs or Link cursors behind a reverse proxy
- **THEN** it MUST derive them only from configured public origins and trusted-proxy policy, and the client MUST refuse to forward Authorization to a cross-origin cursor or redirect

#### Scenario: unsupported code-host operations are capability errors

- **WHEN** a self-hosted issue profile invokes a direct pull, review, commit-status or check-run operation
- **THEN** the command MUST fail with a stable actionable unsupported-capability error and MUST NOT create partial workflow state

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/160#issuecomment-4926527181

### Requirement: Issue-spec markers round-trip byte-for-byte while projections stay derived

The server MUST preserve raw issue and comment bodies exactly as written, including issue-spec HTML markers, and MUST maintain any searchable artifact or typed-comment projection as derived data that cannot alter, reorder, sanitize or replace the raw source of record.

#### Scenario: markers survive create, read and update

- **WHEN** issue-spec writes and later reads a body containing issue or typed-comment markers and arbitrary UTF-8 content
- **THEN** the REST response MUST return the body bytes unchanged except for an explicit client-requested body replacement

#### Scenario: projection failure never destroys raw content

- **WHEN** the server cannot parse a malformed or future-version marker
- **THEN** it MUST retain the raw body, record a projection anomaly and allow a compatible reader to recover the original content

#### Scenario: rendering is separate from storage

- **WHEN** the SPA hides a marker or sanitizes rendered Markdown
- **THEN** the raw REST and edit paths MUST continue returning the unchanged marker and body

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/160#issuecomment-4927060840

### Requirement: Errors are compatible, non-leaking and machine actionable

The GitHub-compatible surface MUST return the status codes and error JSON required by existing client idempotency and authorization behavior, while native APIs MUST return stable problem codes and request identifiers, and neither surface may leak invisible tenant resources or credential values.

#### Scenario: duplicate label remains idempotent

- **WHEN** a caller creates an existing repository label
- **THEN** the compatibility API MUST return a GitHub-shaped 422 already_exists error that the current client treats as already present

#### Scenario: invisible and missing resources are concealed

- **WHEN** a caller requests a missing resource or one outside its visibility scope
- **THEN** the compatibility surface MUST return a GitHub-shaped 404 without distinguishing the cases

#### Scenario: authentication, authorization, validation and throttling are distinguishable

- **WHEN** a visible request has invalid credentials, insufficient permission, invalid input or exceeds an enforced rate limit
- **THEN** the API MUST return the documented 401, 403, 422 or 429 contract with redacted details and Retry-After where applicable

#### Scenario: native errors carry stable diagnostic identity

- **WHEN** a native admin, evidence, board or webhook call fails
- **THEN** it MUST return application/problem+json with a stable code and request_id suitable for audit and support without exposing tokens or cross-tenant identifiers

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/160#issuecomment-4927189589

### Requirement: Workflow-neutral initialization with --tools none

When `issue-spec init` is invoked with explicit `--tools none`, the CLI MUST initialize issue-spec-owned runtime metadata without reading, validating, creating, or modifying workflow-selection files or generated workflow artifacts. It MUST leave existing OpenSpec configuration unchanged and allow subsequent issue-spec workflow discovery to keep selecting legacy OpenSpec compatibility mode when no pre-existing issue-spec workflow file exists.

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

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/241#issuecomment-4990799840

### Requirement: Minimal provider-neutral issue discovery

The `issue-spec` CLI MUST provide a repository-scoped JSON issue listing operation that returns ordinary issues through the selected backend, defaults to open issues, supports open, closed, or all lifecycle states, exhausts backend pagination, excludes pull requests, and exposes stable issue number, title, state, URL, and complete body fields.

#### Scenario: Default to open issues

- **WHEN** the caller omits `--state`
- **THEN** the command lists open ordinary issues and reports the effective open-state filter

#### Scenario: Discover all issues across pages

- **WHEN** a repository has more issues than one backend response page and the caller requests `--state all --json`
- **THEN** the CLI follows pagination to completion and returns every ordinary issue supplied by the stable backend pages

#### Scenario: Exclude pull requests from GitHub issue results

- **WHEN** the GitHub Issues API returns pull requests alongside ordinary issues
- **THEN** the CLI omits pull-request entries from the issue result

#### Scenario: Return lossless stable fields

- **WHEN** an ordinary issue is included in JSON output
- **THEN** its number, title, normalized state, human-facing URL, and complete backend body are present

#### Scenario: Empty repository result

- **WHEN** no visible ordinary issues match the requested state
- **THEN** the CLI succeeds with an empty issue array instead of an error or null collection

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/241#issuecomment-4990829767

### Requirement: Opt-in original typed-comment bodies

The `issue-spec comment list --json` command MUST offer an `--include-body` opt-in that adds each typed artifact's complete original backend Markdown as a top-level `body` field while preserving the existing default JSON contract for callers that do not request bodies.

#### Scenario: Request original comment bodies

- **WHEN** the caller lists typed comments with `--json --include-body`
- **THEN** each returned artifact includes a top-level body field containing the complete body returned by the backend without reconstruction

#### Scenario: Require JSON output for body inclusion

- **WHEN** the caller passes `--include-body` without `--json`
- **THEN** the CLI rejects the arguments with an actionable error

#### Scenario: Preserve default compatibility

- **WHEN** an existing caller uses `comment list --json` without `--include-body`
- **THEN** the output retains its existing fields and does not add a body field

#### Scenario: Combine type filtering and bodies

- **WHEN** the caller requests a typed-comment filter together with body inclusion
- **THEN** only matching typed comments are returned and every returned artifact contains parsed metadata plus its original body

#### Scenario: No matching comments

- **WHEN** no typed comments match the query
- **THEN** the CLI succeeds with an empty comment array rather than a null collection

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/241#issuecomment-4990831137

### Requirement: Preserve direct issue lineage during body updates

When an issue-spec issue body is replaced, the CLI MUST preserve the complete stored issue marker and the issue's direct predecessor metadata exactly once, and MUST reject different replacement metadata before mutation. A design's direct predecessor is its Proposal Issue; an implement's direct predecessor is its Design Issue.

#### Scenario: Restore omitted design lineage

- **WHEN** a replacement design body omits its marker and Proposal Issue line
- **THEN** the updated body contains the complete stored design marker and original Proposal Issue line exactly once

#### Scenario: Restore omitted implement lineage

- **WHEN** a replacement implement body omits its marker and Design Issue line
- **THEN** the updated body contains the complete stored implement marker and original Design Issue line exactly once without adding an indirect Proposal Issue requirement

#### Scenario: Normalize identical duplicate metadata

- **WHEN** the replacement repeats the exact stored marker or direct predecessor more than once
- **THEN** the updated body contains one copy of each stored metadata line

#### Scenario: Reject a different issue marker

- **WHEN** the replacement contains an issue marker that differs from the complete stored marker, including class, change, or version metadata
- **THEN** the CLI returns an actionable error and leaves the remote issue unchanged

#### Scenario: Reject a different direct predecessor

- **WHEN** the replacement names a Proposal Issue or Design Issue value different from the exact stored direct predecessor value
- **THEN** the CLI returns an actionable error and leaves the remote issue unchanged without attempting semantic reference normalization

#### Scenario: Do not repair malformed stored lineage

- **WHEN** the stored issue has missing or conflicting reserved lineage metadata
- **THEN** the CLI fails before mutation and reports that historical metadata must be repaired explicitly

#### Scenario: Repeat the same update

- **WHEN** the same body replacement is applied more than once
- **THEN** the stored marker and direct predecessor remain singular and unchanged

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/241#issuecomment-4990831775

### Requirement: Provider-neutral idempotent issue close and reopen

The `issue-spec` CLI MUST provide dedicated provider-neutral `issue close` and `issue reopen` operations through the selected backend. Each operation MUST read the current state, avoid a PATCH when already in the target state, and return deterministic machine-readable state and changed fields.

#### Scenario: Close an open issue

- **WHEN** the caller closes an open issue
- **THEN** the backend issue becomes closed and the CLI reports state closed with changed true

#### Scenario: Close an already closed issue

- **WHEN** the caller closes an already closed issue
- **THEN** the operation succeeds without issuing a PATCH and reports state closed with changed false

#### Scenario: Reopen a closed issue

- **WHEN** the caller reopens a closed issue
- **THEN** the backend issue becomes open and the CLI reports state open with changed true

#### Scenario: Reopen an already open issue

- **WHEN** the caller reopens an already open issue
- **THEN** the operation succeeds without issuing a PATCH and reports state open with changed false

#### Scenario: Honor selected backend and authorization

- **WHEN** a close or reopen command runs under a GitHub or self-hosted profile
- **THEN** the CLI uses only that profile's selected issue backend and returns actionable authorization or not-found errors without fallback

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/241#issuecomment-4990832627
