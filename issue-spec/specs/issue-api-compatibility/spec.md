# issue-api-compatibility

## Purpose

Define the GitHub-compatible issue protocol, lossless marker persistence, canonical URL behavior and machine-actionable compatibility errors exposed by the self-hosted server.

Proposal Issues:
- https://github.com/higress-group/issue-spec/issues/160

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
