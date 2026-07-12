# issue-management-server

## Purpose

Define the long-lived behavior contract for this capability.

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

### Requirement: Organization-owned repositories with complete tenant isolation and administration

The server MUST support multiple organizations whose repositories, memberships, issue data, boards, source bindings, evidence and webhook configuration are isolated on every read and write, and MUST provide a bootstrap and administration path that makes a fresh deployment usable without direct database mutation.

#### Scenario: the first administrator can provision the system exactly once

- **WHEN** a fresh deployment has no site administrator and an operator presents the configured one-time bootstrap secret
- **THEN** exactly one concurrent claim MUST create or promote the first administrator, consume the secret and write an immutable audit record

#### Scenario: administrators manage organization-owned repositories

- **WHEN** an authorized administrator creates an organization, repository, membership or collaborator assignment
- **THEN** the server MUST persist the lifecycle change through an authenticated native API and MUST scope the repository to exactly one owning organization

#### Scenario: visibility and role gate reads before aggregation

- **WHEN** a caller lists issues, reads comments, opens a board, reads references/evidence or enumerates repositories
- **THEN** public, internal and private visibility plus effective permission and token scope MUST be applied before returning rows, counts or external URLs, and an invisible compatibility resource MUST return 404

#### Scenario: permission levels have explicit mutation semantics

- **WHEN** a caller attempts issue triage, content writes, label/reference writes, runner commands, source-binding/webhook policy changes or repository administration
- **THEN** the server MUST enforce the documented read, triage, write, maintain and admin operation matrix capped by the caller token scopes

#### Scenario: cross-tenant identifiers cannot escape scope

- **WHEN** a caller guesses an issue, comment, label, binding, evidence or subscription identifier belonging to another organization
- **THEN** composite storage constraints and scoped store APIs MUST prevent data access or mutation and MUST not reveal the other tenant's existence

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/160#issuecomment-4926527500

### Requirement: Provider-correct identity with secure browser sessions and scoped API tokens

The server MUST implement generic OIDC and GitHub OAuth as separate authentication adapters, MUST map provider identities to a stable local user and login, and MUST separate browser sessions, user PATs, service credentials and delegated runner tokens with appropriate lifetime, scope, revocation and audit controls.

#### Scenario: generic OIDC validates the complete login transaction

- **WHEN** a user authenticates through a configured OIDC provider
- **THEN** the server MUST use authorization code flow with PKCE, state and nonce and MUST validate issuer, audience, signature, expiry and the unique provider-issuer-subject identity

#### Scenario: GitHub login uses the OAuth adapter

- **WHEN** an operator enables GitHub as an interactive identity source
- **THEN** the server MUST use the GitHub OAuth flow, resolve the stable numeric GitHub user identity, and MUST NOT model the GitHub Actions OIDC issuer as an interactive user provider

#### Scenario: local identity remains stable across provider display changes

- **WHEN** a provider username or email changes or two providers expose the same email
- **THEN** issue/comment authorship and runner authorization MUST continue using the same local user id/login and identities MUST be linked only by an explicit authorized action

#### Scenario: browser mutations resist session and CSRF attacks

- **WHEN** the SPA authenticates with a session cookie and performs a mutation
- **THEN** the server MUST enforce secure HttpOnly SameSite cookies, idle and absolute expiry, rotation, Origin validation and a CSRF token, while bearer-token requests remain cookie-independent

#### Scenario: PAT lifecycle is bounded and visible

- **WHEN** a user creates, rotates, expires or revokes a PAT
- **THEN** the plaintext MUST be shown only once, only a protected digest MUST be stored, repository selection and scopes MUST cap current permissions, and revocation or account disablement MUST take effect immediately

#### Scenario: air-gapped recovery does not depend on GitHub

- **WHEN** the configured identity provider is unavailable in an air-gapped deployment
- **THEN** an audited operator-only recovery command MUST be able to mint a short-lived one-time administrative credential without enabling an unauthenticated network backdoor

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/160#issuecomment-4926527893

### Requirement: REST-backed issue SPA with secure Markdown and usable self-host administration

The server MUST ship an embedded React SPA that uses the authenticated APIs for issue management and the minimum bootstrap, identity, PAT, organization and repository administration needed to operate the self-hosted product, while preserving raw workflow bodies and preventing active Markdown or browser-session attacks.

#### Scenario: users browse and mutate issues through REST

- **WHEN** an authorized user opens a repository issue list or detail view
- **THEN** the SPA MUST support pagination, issue creation/edit/state changes, ordered comments, comment creation, label assignment and reaction operations with loading, empty and error states

#### Scenario: Markdown is faithful but not executable

- **WHEN** an issue or comment contains GitHub-Flavored Markdown, fenced code, raw HTML, unsafe URL schemes or event-handler attributes
- **THEN** the SPA MUST render supported GFM and syntax highlighting while sanitizing active content under a restrictive content security policy

#### Scenario: typed markers are hidden visually and preserved raw

- **WHEN** a body includes an issue-spec issue or typed-comment HTML marker
- **THEN** the rendered view MUST hide the marker while the REST body and raw editing path preserve its bytes unchanged

#### Scenario: administrators can reach an operable initial state

- **WHEN** the first administrator signs in after bootstrap
- **THEN** the SPA MUST expose PAT lifecycle and organization, repository, membership, visibility, source-binding and webhook navigation permitted by the native APIs

#### Scenario: the product remains issue-only

- **WHEN** a user navigates the SPA
- **THEN** the UI MUST NOT expose source browsing, branch, diff or pull-request hosting surfaces and MUST represent external code objects only as neutral links and evidence

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/160#issuecomment-4926528283

### Requirement: One workflow board card per issue-spec change

The server MUST project proposal, design and implement artifact issues sharing a repository and change key into one permission-filtered change card, MUST derive current stage and lifecycle deterministically, and MUST report marker, label and link anomalies instead of displaying the same change as three independent stage cards.

#### Scenario: three artifact issues become one card

- **WHEN** proposal, design and implement issues exist for the same repository and change marker
- **THEN** the board MUST return exactly one card with three artifact slots and current_stage implement

#### Scenario: blocked and completed are lifecycle states rather than duplicate columns

- **WHEN** a change has an unresolved blocking QUESTION or later has successful final VERIFY plus accepted archive/closure evidence
- **THEN** the card MUST report blocked or completed lifecycle independently from its highest artifact stage

#### Scenario: invalid artifact structure is visible

- **WHEN** a change has duplicate artifact types, a marker/label mismatch, missing links or an implement artifact without its expected predecessor
- **THEN** the server MUST keep a single card where possible, attach stable anomaly codes and MUST NOT silently duplicate or drop the change

#### Scenario: organization boards do not leak hidden repositories

- **WHEN** a user requests an organization-level board
- **THEN** the server MUST filter repositories and restricted external data by caller permission before aggregating cards, counts or progress

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/160#issuecomment-4926528729

### Requirement: Versioned at-least-once comment events replace self-hosted notifications polling

The server MUST record issue/comment events in a transactional outbox, MUST deliver a versioned immutable event snapshot to scoped runner subscriptions with at-least-once semantics, and the runner MUST durably de-duplicate, reconcile and authorize the comment before using the existing command parser and job path.

#### Scenario: comment mutation and event creation are atomic

- **WHEN** an issue comment is created or edited
- **THEN** the body mutation and outbox event MUST commit in the same database transaction or both roll back

#### Scenario: the event carries a complete revision rather than a trusted parsed command

- **WHEN** the server emits a comment created or edited event
- **THEN** the envelope MUST include schema version, event/delivery identity, action, repository, issue, comment id, raw body, body hash, author, created_at and updated_at, and MUST NOT make a server-parsed command authoritative

#### Scenario: runner reconciles stale or replayed events

- **WHEN** runner serve receives a valid delivery
- **THEN** it MUST persist de-duplication before acknowledging, may refetch the single comment, MUST compare revision/hash, MUST recheck current permission, and MUST invoke the existing command parser only for the accepted immutable revision

#### Scenario: delivery is observable and recoverable

- **WHEN** delivery receives a retryable status or the worker crashes
- **THEN** the same delivery id MUST retry with bounded backoff, attempts MUST be audited, exhausted deliveries MUST enter a dead-letter state and an authorized operator MUST be able to redeliver

#### Scenario: self-hosted runner does not require notifications

- **WHEN** a runner uses a self-hosted issue profile
- **THEN** runner serve MUST be the command-intake transport and MUST NOT call a GitHub-style notifications endpoint, while GitHub polling mode remains available for GitHub profiles

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/160#issuecomment-4926529148

### Requirement: Core owns neutral integration contracts while vendor behavior remains external

The project MUST provide versioned provider-neutral contracts for source bindings, code evidence and external mutations required before agent startup or at workflow gates, MUST NOT compile vendor-specific adapters into the server or CLI, and MUST restrict executable adapter configuration to trusted operator scope.

#### Scenario: GitHub and self-hosted modes implement the same neutral boundaries differently

- **WHEN** the CLI requests issue data, source resolution or code evidence
- **THEN** GitHub mode MAY satisfy the boundaries with one adapter while self-hosted mode MUST use its issue backend plus configured neutral bindings/evidence without changing workflow semantics

#### Scenario: repository content cannot select an executable

- **WHEN** a project workflow names an external provider
- **THEN** it MAY reference an operator-registered provider key and evidence policy but MUST NOT supply an arbitrary executable path, arguments or credential source

#### Scenario: vendor adapters remain replaceable

- **WHEN** an operator installs a bridge for a code host, CI system or requirement tracker
- **THEN** the adapter MUST communicate through the documented versioned contract and neutral server APIs without requiring vendor fields in core tables or durable specs

#### Scenario: missing capabilities fail before partial mutation

- **WHEN** a selected provider lacks an operation required by review, verify or archive
- **THEN** capability discovery MUST fail the command with an actionable error before any partial workflow evidence is written

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/160#issuecomment-4926529483

### Requirement: External links and trusted evidence have separate lifecycle and provenance

The server MUST store mutable provider-namespaced external references separately from immutable revision-bound evidence, MUST authorize and audit their writers, and MUST provide idempotent retrieval and ingestion semantics that prevent URL-only records from being treated as proof.

#### Scenario: external references use a non-null provider identity

- **WHEN** a bridge records a work item, source repository, code change, build or archive link
- **THEN** the server MUST require provider key, relation kind, external repository/object identity and canonical URL and MUST upsert idempotently without NULL-based uniqueness gaps

#### Scenario: evidence is immutable and revision bound

- **WHEN** a designated writer reports review, check, merge or archive state
- **THEN** the server MUST append an immutable row containing normalized state, subject revision, observed time, payload digest, provenance and writer identity, using a superseding row for later observations

#### Scenario: untrusted writers cannot publish gate evidence

- **WHEN** a caller without repository evidence-writer authorization or evidence:write scope submits evidence
- **THEN** the server MUST reject the write and audit the attempt while ordinary authorized users MAY still manage non-authoritative external references according to repository policy

#### Scenario: reads respect tenant and field visibility

- **WHEN** the SPA, board or CLI lists references or evidence
- **THEN** the server MUST filter by repository visibility, caller permission and record visibility and MUST not leak hidden provider URLs or metadata

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/160#issuecomment-4926919280

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

### Requirement: Core review, verify and archive validate neutral external evidence fail closed

For a self-hosted issue profile whose code change lives externally, issue-spec MUST evaluate trusted structured evidence for the exact code revision, review findings, required checks, merge state and archive state, MUST compute the gate result itself, and MUST fail closed rather than accepting an arbitrary approval flag or unverified VERIFY text.

#### Scenario: verify passes only for the verified revision

- **WHEN** trusted evidence for the active code-change reference reports the current head revision, no open P0/P1 findings and all required checks passed and a done VERIFY records that revision
- **THEN** core verify MAY pass its external-code gate and MUST record the evidence identifiers and revision used

#### Scenario: missing or stale evidence blocks

- **WHEN** evidence is absent, expired, written by an untrusted identity, tied to another provider/change/revision, has pending or failed checks, or has open blocking review
- **THEN** review or verify MUST fail with an actionable reason and MUST NOT be bypassed by omitting a PR flag

#### Scenario: archive validates implementation merge and durable-spec merge

- **WHEN** an external bridge reports implementation or durable-spec change state
- **THEN** archive MUST require trusted merged evidence for the expected revision and change reference before closing proposal/design/implement issues, while GitHub mode retains its existing closure-block path

#### Scenario: line-level discussions remain externally linked but summarized canonically

- **WHEN** a bridge reports external review threads
- **THEN** issue-spec MUST preserve canonical PROCESS/SPEC/finding linkage and blocking severity/state in neutral evidence while the external platform remains the owner of line-level discussion content

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/160#issuecomment-4927054247

### Requirement: Webhook delivery uses scoped rotatable bearer secrets over operator-approved transport

Each org or repository webhook subscription MUST authenticate delivery with its own rotatable bearer secret, MUST protect the secret at rest and in logs, MUST require secure transport in production, and MUST enforce destination and replay controls appropriate for a multi-tenant self-hosted service.

#### Scenario: runner selects the secret by trusted subscription identity

- **WHEN** runner serve receives a delivery
- **THEN** it MUST select the configured subscription secret independently of the body, compare the bearer value in constant time, validate the delivery timestamp/window and reject an unknown, missing, expired or mismatched credential before persistence

#### Scenario: secret rotation has a bounded overlap

- **WHEN** an administrator rotates a subscription secret
- **THEN** the server and runner MUST support current/previous versions for a configured overlap, return plaintext only at creation/rotation, and reject the old version after expiry or revocation

#### Scenario: transport and destination policy prevent credential disclosure

- **WHEN** a subscription is created or delivery resolves/connects to its target
- **THEN** production MUST require HTTPS, redirects MUST be disabled, and loopback, link-local, metadata and non-allowlisted private destinations or DNS rebinding MUST be rejected according to operator network policy

#### Scenario: secrets are recoverable only with the deployment key ring

- **WHEN** the delivery worker loads or backs up a subscription secret
- **THEN** the database MUST contain only encrypted secret material with key identifiers, logs/API responses MUST be redacted, and operations documentation MUST treat database and encryption-key backup as one recovery unit

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/160#issuecomment-4927054509

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

### Requirement: Every self-hosted capability has an executable end-to-end acceptance case

The project MUST provide a reproducible conformance and security harness that boots the production server, database, identity fixtures, runner serve process, external source repository and neutral evidence stub, drives the production CLI and browser surfaces, and fails the final gate unless every active SPEC has a passing executable test.

#### Scenario: issue workflow and protocol run without external GitHub

- **WHEN** the harness starts an isolated server and points a production CLI profile at it
- **THEN** proposal/design/implement creation, typed upsert/link/status/verify-links, labels/reactions, marker fidelity, pagination, ETag/304, rate headers and compatible errors MUST pass without calling github.com

#### Scenario: identity, tenant and browser security are exercised

- **WHEN** the harness drives generic OIDC/GitHub OAuth test adapters, bootstrap, sessions, PATs, permissions and the SPA
- **THEN** CSRF/XSS, token lifecycle, cross-org reads/writes, admin flows and change-board filtering MUST match their SPECs

#### Scenario: real runner sandbox proves the decoupled runtime

- **WHEN** a delivered comment triggers runner serve against a temporary external git repository
- **THEN** the runner MUST resolve and clone the binding, use short-lived credentials, start the sandboxed agent, pass child auth status and perform comment/reaction/status writeback without exposing a long-lived token

#### Scenario: evidence and archive gates fail and pass for the right revision

- **WHEN** the harness supplies valid and invalid review/check/merge/archive evidence
- **THEN** valid trusted evidence for the exact revision MUST pass and stale, untrusted, mismatched, pending or failed evidence MUST block deterministically

#### Scenario: packaging and operations gates are reproducible

- **WHEN** CI runs the final matrix target
- **THEN** embedded frontend build, migrations, health/readiness, delivery retry/HA behavior and existing GitHub-mode regression suites MUST pass and the matrix MUST report no uncovered active SPEC

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/160#issuecomment-4927466268

### Requirement: Runner resolves source repositories from trusted versioned bindings

The runner MUST treat issue repository identity and source repository location as separate concepts, MUST resolve a credential-free clone binding from trusted operator or server configuration before agent startup, and MUST never derive a clone URL from the issue server hostname or untrusted issue content.

#### Scenario: new sessions pin an explicit binding

- **WHEN** runner dispatches a new command for an issue repository
- **THEN** it MUST resolve operator mapping first, then an authorized active server binding, persist binding id/version/provider/clone metadata in session state, and fail when no binding exists

#### Scenario: resume fails closed after binding drift

- **WHEN** a public session is resumed after its source provider, external repository, clone URL or binding version changed
- **THEN** the runner MUST reject resume with a binding-changed diagnostic and require a new session rather than cloning or writing a different repository

#### Scenario: untrusted content cannot choose a clone target

- **WHEN** an issue body, comment, external URL or webhook payload contains a clone URL or source repository instruction
- **THEN** the resolver MUST ignore it and MUST accept source metadata only from operator configuration or a maintain-authorized versioned binding

#### Scenario: source credentials are outside the binding

- **WHEN** the server returns or displays a source binding
- **THEN** the clone URL MUST contain no embedded credential and the server MUST NOT store a vendor access token in the binding record

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/160#issuecomment-4932916831

### Requirement: Sandboxed agents receive only short-lived purpose-scoped credentials

The runner MUST broker short-lived repository, job, audience and purpose scoped credentials for issue API and source access, MUST keep long-lived human and service credentials outside the sandbox, and MUST revoke or expire every lease when the job or session ends.

#### Scenario: child issue access uses a delegated token file

- **WHEN** a sandboxed coordinator starts
- **THEN** the runner MUST mint or obtain a short-lived issue token with only required repository scopes, write it to a session-private read-only file, expose the selected profile and token-file path, and pass child auth status before agent dispatch

#### Scenario: source clone uses an operator credential helper

- **WHEN** the runner clones or writes the bound source repository
- **THEN** it MUST use an operator-owned credential helper or askpass lease, MUST NOT embed a token in the URL, and MUST not copy the credential into issue, workspace or agent prompt content

#### Scenario: broker failure cannot fall back to host secrets

- **WHEN** the credential broker cannot issue or refresh a required lease
- **THEN** the job MUST fail before agent startup and MUST NOT inherit ISSUE_SPEC_TOKEN, GH_TOKEN, GITHUB_TOKEN or a host credential store as a fallback

#### Scenario: leases are revoked and audited

- **WHEN** a job completes, is canceled, fails or exceeds its lifetime
- **THEN** the runner MUST revoke revocable leases, delete private token files, record the lease lifecycle without secret values and prevent reuse by later sessions

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/160#issuecomment-4932917391

### Requirement: Self-hosted API credentials are bound to a named instance and origin

The CLI and runner MUST represent a self-hosted server as an origin-bound profile with stable instance identity, MUST store and select credentials in that profile realm, and MUST never send GitHub environment or stored credentials to a custom API origin.

#### Scenario: custom profiles reject implicit GitHub tokens

- **WHEN** ISSUE_SPEC_API_URL or a named self-hosted profile selects a non-GitHub API origin and only GH_TOKEN or GITHUB_TOKEN is available
- **THEN** authentication MUST fail with instructions to provide an explicit issue-spec token or origin-bound login and MUST send no request carrying the GitHub token

#### Scenario: profile login and status expose the selected realm

- **WHEN** a user runs auth login, status, token or logout for a profile
- **THEN** the command MUST report profile name, server instance id, canonical API origin, backend kind and redacted credential source and MUST operate only on that realm

#### Scenario: profiles do not share credentials

- **WHEN** two servers use the same DNS hostname, repository name or user login but have different instance IDs or API origins
- **THEN** their persisted credentials MUST remain separate and a credential MUST not be automatically migrated or reused across them

#### Scenario: redirect and cursor origin changes strip authorization

- **WHEN** a custom server returns a redirect or Link cursor targeting another origin
- **THEN** the client MUST reject or follow without Authorization according to the documented safe policy and MUST surface a protocol-security diagnostic

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/160#issuecomment-4932917899

### Requirement: The server is packaged and observable as a recoverable production service

The project MUST ship a reproducible single-server artifact with embedded frontend assets, PostgreSQL migration and compatibility controls, health/readiness, structured audit and operational telemetry, safe secret configuration, and documented backup, restore, upgrade and worker high-availability procedures.

#### Scenario: build and startup are reproducible

- **WHEN** an operator builds the release or starts the documented container/compose setup
- **THEN** the pinned frontend build MUST be embedded in the Go server, configuration validation and migration locking MUST run deterministically, and startup MUST fail before readiness on invalid config or incompatible schema

#### Scenario: health and readiness reflect real dependencies

- **WHEN** an orchestrator probes the service
- **THEN** liveness MUST report process health, readiness MUST require database connectivity and required migration state, and delivery workers MUST stop accepting work during graceful shutdown

#### Scenario: operators can diagnose without exposing secrets

- **WHEN** the service emits logs, metrics, request diagnostics or audit records
- **THEN** it MUST include request/delivery identifiers and useful state while redacting PATs, sessions, delegated tokens, webhook secrets and OAuth material

#### Scenario: database and encryption keys form one recoverable unit

- **WHEN** an operator backs up, restores or upgrades the deployment
- **THEN** documentation and smoke tests MUST cover database plus encryption-key backup, migration rollback compatibility, webhook worker leases, and restoration of encrypted subscription secrets

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/160#issuecomment-4932918429

### Requirement: Provider-neutral external code-change relationships in the web UI

The self-hosted server and SPA MUST project every caller-visible active `code_change` external reference as a first-class relationship on the owning issue and its derived change view, using the persisted provider key, external repository identity, external object identity, canonical URL, lifecycle state, and only metadata authorized for that caller; the UI MUST derive provider-specific presentation without making issue-spec core depend on vendor-specific object types or parsing free-form issue bodies.

#### Scenario: Issue detail exposes the persisted code-change relationship

- **WHEN** an authorized caller opens an issue that has a repository-visible active `code_change` reference
- **THEN** the issue detail shows the provider, external repository, external object identifier, lifecycle state, and a safe clickable canonical link sourced from the reference API rather than from issue-body text

#### Scenario: Change projection exposes delivery association

- **WHEN** a projected change contains an implement artifact whose issue has a caller-visible active `code_change` reference
- **THEN** the change detail includes that relationship and the change card provides a compact delivery indicator without removing the proposal, design, task, process, or diagnostic information

#### Scenario: Provider-specific labels remain presentation-only

- **WHEN** the UI renders code-change references from GitHub, Aone, or an unrecognized registered provider key
- **THEN** it labels GitHub objects as pull requests, Aone objects as merge requests, and unknown providers as code changes while preserving one provider-neutral API shape and canonical-link behavior

#### Scenario: Reference visibility and metadata permissions are preserved

- **WHEN** a caller can read the issue or public repository but lacks permission to read a maintainer-only reference or restricted reference metadata
- **THEN** the relationship projection omits or redacts exactly the same data as the reference API and does not disclose hidden identifiers, revisions, provider payloads, credentials, or authorization material

#### Scenario: Source binding mismatch is not presented as healthy delivery

- **WHEN** an active code-change reference identifies a different provider key or external repository from the repository's active source binding
- **THEN** the change UI reports a structural delivery diagnostic and does not present the mismatched reference as a healthy authoritative PR or MR association

#### Scenario: Canonical external links remain safe

- **WHEN** the relationship is rendered as an external link
- **THEN** the UI uses only the server-validated canonical URL, opens it with safe external-link protections, and never constructs a provider URL from untrusted issue text or embeds provider credentials

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/160#issuecomment-4949546738

### Requirement: Provider-neutral evidence synchronization before workflow gates

The issue-spec CLI and runner MUST provide a provider-neutral evidence synchronization stage that resolves the active external code-change reference, selects an operator-registered adapter by `provider_key`, requests an `evidence.snapshot` for one exact subject revision, validates and immutably persists the returned neutral records through the self-hosted evidence authority, and only then evaluates review or verify gates from the persisted ledger; repository workflow configuration MAY select synchronization policy and required evidence but MUST NOT supply provider executables, credentials, or vendor-specific approval decisions.

#### Scenario: Explicit evidence synchronization uses the active code-change reference

- **WHEN** an operator or workflow invokes evidence synchronization for an implement issue and exact revision
- **THEN** issue-spec resolves exactly one active `code_change` reference, verifies the requested revision matches its pinned head revision, resolves the registered provider adapter, requires `evidence.snapshot`, and rejects an absent, ambiguous, mismatched, or incapable provider before evidence is written

#### Scenario: Verify can run workflow-directed synchronization before its gate

- **WHEN** the repository workflow enables pre-gate synchronization and `issue-spec verify` or the equivalent runner stage reaches external evidence validation
- **THEN** the synchronization stage runs first using the workflow's provider and evidence policy, persists the accepted snapshot, and verify evaluates the resulting exact-revision ledger records instead of requiring a separate vendor-specific command sequence

#### Scenario: Adapters return facts rather than gate decisions

- **WHEN** a GitHub, Aone, or other registered adapter snapshots a code change
- **THEN** it emits only protocol-versioned neutral `change`, `review`, `check`, `merge`, or `archive` records with stable identity, normalized state, observation time, exact revision, canonical URL where applicable, and payload digest, and issue-spec core independently decides whether the gate passes

#### Scenario: Evidence persistence is trusted and idempotent

- **WHEN** the synchronization stage writes a validated snapshot to the self-hosted server
- **THEN** the server requires an active designated evidence writer with exact repository authority, derives immutable writer provenance, uses deterministic ingest identity to make replay idempotent, preserves supersession history, and rejects untrusted approval booleans or identity changes

#### Scenario: Revision movement during synchronization fails closed

- **WHEN** the external change head or active reference changes while a snapshot is being collected or persisted
- **THEN** the stage does not mix records across revisions, does not advance the reference implicitly, and fails with an actionable revision-mismatch result that requires a new synchronization attempt for the new head

#### Scenario: Missing or failed synchronization never becomes successful verification

- **WHEN** the adapter is unavailable, its output is malformed, required checks are missing, pending, failed, stale, or tied to another reference or revision, or persistence is unauthorized
- **THEN** the synchronization or verify command fails closed and MUST NOT substitute a typed VERIFY comment, skill execution transcript, cached vendor response, or adapter-provided approved flag for trusted evidence

#### Scenario: Repository workflow cannot install executable authority

- **WHEN** a checked-out repository configures pre-gate evidence synchronization
- **THEN** it may select a registered provider key, required evidence kinds, required check names, freshness, and synchronization timing, while executable paths, arguments, environment, credentials, writer grants, and network authority remain operator-controlled outside the repository

#### Scenario: Runner and interactive CLI share one synchronization contract

- **WHEN** evidence synchronization is triggered interactively, by runner polling, or by a trusted webhook-driven runner job
- **THEN** all modes use the same provider-neutral snapshot validation, exact-revision persistence, idempotency, authorization, audit, and gate-evaluation semantics and produce equivalent evidence identities

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/160#issuecomment-4949554236

### Requirement: Profile-driven server onboarding and provider-aware workflow initialization

The issue-spec CLI MUST allow an origin-bound profile to reference operator-owned provider registries and onboarding policy, and `issue-spec init` using that profile MUST idempotently resolve or register the target issue repository, establish a credential-free source binding when policy permits, discover the selected registered provider's neutral description and capabilities, and generate matching provider-neutral workflow artifacts without storing provider executables, credentials, or vendor-specific authority in the project checkout.

#### Scenario: Profile binds the server realm and operator registry

- **WHEN** a user selects a named self-hosted profile for init, CLI, or runner operations
- **THEN** the profile determines the canonical API and web origins, immutable server instance identity, CA trust, isolated credential realm, operator registry reference, and onboarding policy while credentials remain outside the persisted profile and the repository cannot override any of those authorities

#### Scenario: One profile may serve repositories using different providers

- **WHEN** repositories on the same issue-spec server use Aone, GitHub, GitLab, or another registered code provider
- **THEN** the profile references a registry containing multiple provider keys and each repository workflow or source binding selects its own key rather than the profile forcing one global default provider

#### Scenario: Init resolves an existing server repository idempotently

- **WHEN** the selected profile can reach the server and the requested organization/repository already exists and is visible to the caller
- **THEN** init reuses the unique existing repository, verifies the caller's effective access and compatible identity, performs no destructive update, and generates local configuration that records the selected profile and issue repository

#### Scenario: Init registers a missing repository under profile policy

- **WHEN** the target repository is absent, profile or explicit CLI policy permits create-if-missing registration, and the credential has organization-scoped repository-create authority
- **THEN** init creates exactly one audited repository with safe defaults such as private visibility and member contribution, handles concurrent creation idempotently, and does not require unrelated organization-administration authority when a dedicated repository-create grant is sufficient

#### Scenario: Unauthorized or ambiguous registration fails before local success

- **WHEN** the organization is missing or ambiguous, the credential lacks repository-create authority, the profile origin or server identity is invalid, or an existing repository conflicts with the requested identity
- **THEN** init fails closed with an actionable result, does not report successful initialization, does not create labels or bindings in another repository, and does not silently fall back to GitHub or a different profile

#### Scenario: Source provider and repository are discovered safely

- **WHEN** source-binding automation is enabled and the local checkout has canonical git remotes
- **THEN** init matches the remote authority and repository identity against operator-owned provider descriptions, selects a provider automatically only when the match is unique, accepts an explicit provider key as a disambiguation, and never accepts a repository-supplied executable path, credential, or unregistered provider

#### Scenario: Source binding creation is credential-free and conflict-safe

- **WHEN** init has selected one registered provider and external source repository
- **THEN** it creates or reuses an active source binding containing only the provider key, stable external repository identity, validated credential-free clone and web URLs, and default branch; an incompatible active binding produces a conflict instead of being replaced automatically

#### Scenario: Provider description drives generic workflow generation

- **WHEN** init resolves a registered provider for the repository
- **THEN** it reads only operator-trusted provider-neutral metadata and capabilities such as display name, supported remote authorities, code-change label, `change.create`, `change.comment`, `evidence.snapshot`, and recommended evidence names, then generates one generic workflow whose available steps and preconditions match those capabilities without embedding vendor CLI commands

#### Scenario: Generated workflow reflects missing and available capabilities

- **WHEN** a provider advertises only a subset of the neutral contract
- **THEN** generated skills require a pre-existing external change when `change.create` is absent, enable external finding and reply flows only when `change.comment` is present, configure pre-gate synchronization only when `evidence.snapshot` is present, and fail closed rather than describing unsupported operations as available

#### Scenario: Project tracker integration remains independently selectable

- **WHEN** a source provider also has a project or work-item system such as Aone
- **THEN** code-provider discovery does not implicitly grant or configure project-tracker authority, and init generates tracker workflow only when a separately registered or explicitly selected project provider is available

#### Scenario: Profile registry selection has deterministic precedence

- **WHEN** both process-level operator configuration and a profile registry reference are present
- **THEN** an explicit operator environment override takes precedence over the profile reference, a missing or malformed selected registry fails provider operations closed, and repository workflow configuration never participates in registry path resolution

#### Scenario: Partial onboarding is auditable and safely resumable

- **WHEN** repository creation succeeds but source binding, provider discovery, label creation, or local workflow generation later fails
- **THEN** init reports the exact completed and pending stages, preserves server audit history, performs no destructive rollback of shared remote state, and a repeated invocation resumes idempotently without creating duplicate repositories, bindings, or labels

#### Scenario: Interactive and automated initialization make remote mutation explicit

- **WHEN** profile policy would create a remote repository or source binding
- **THEN** interactive init presents the planned server, organization, repository, provider, and visibility before mutation unless the profile or CLI explicitly enables unattended registration, and non-interactive use requires an explicit create-if-missing policy rather than inferring authority from project files

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/160#issuecomment-4949612765

### Requirement: GitHub-compatible notification webhooks with content-aware delivery policy

The self-hosted server MUST let an authorized organization or repository administrator configure a webhook delivery format independently from event selection, preserve the existing `issue-spec.v1` runner contract, and provide a `github.v3` notification format that maps supported issue mutations to GitHub-compatible event headers, actions, and payload objects; notification subscriptions MUST evaluate provider-neutral, server-authoritative issue, comment, actor, and projection attributes before delivery, MUST protect signing and destination credentials as secrets, and MUST retain transactional outbox, retry, replay, authorization, audit, and SSRF guarantees.

#### Scenario: Issue creation is delivered as a GitHub issues event

- **WHEN** a subscribed repository creates an issue whose authoritative projected kind and action match a `github.v3` notification policy
- **THEN** the server sends one JSON request with `X-GitHub-Event: issues`, a stable `X-GitHub-Delivery`, a `GitHub-Hookshot/` user agent, action `opened`, and GitHub-compatible `issue`, `repository`, `organization`, and `sender` objects whose canonical URLs point to the self-hosted server

#### Scenario: Human non-typed comments can be selected for notification

- **WHEN** an authenticated human creates or edits a comment that has no valid typed-comment projection and the subscription selects the corresponding comment action and human-untyped comment class
- **THEN** the server sends a GitHub-compatible `issue_comment` event with the matching `created` or `edited` action and authorized `issue`, `comment`, `repository`, `organization`, and `sender` data

#### Scenario: Typed workflow comments can be excluded without parsing notification authority from raw text

- **WHEN** a comment mutation has an active valid typed-comment projection and the subscription excludes typed workflow comments
- **THEN** the server emits no outbound request for that subscription, records a non-secret observable suppression outcome, and bases the classification on the transactional projection and authenticated actor provenance rather than a username convention or an untrusted body substring

#### Scenario: Key workflow issue kinds can be selected independently

- **WHEN** an administrator limits issue notifications to proposal, design, implement, or another supported projected workflow artifact kind
- **THEN** only issue actions whose authoritative projection matches the configured kinds are delivered, while ordinary issues and malformed, unsupported, or absent markers do not become key workflow issues through free-form text matching

#### Scenario: GitHub-compatible and runner delivery semantics remain isolated

- **WHEN** the same repository has both an `issue-spec.v1` runner subscription and a `github.v3` notification subscription
- **THEN** the runner request retains its schema-versioned issue-spec envelope, `X-Issue-Spec-*` headers, and bearer authentication, while the notification request uses GitHub event names, actions, headers, payload objects, and optional HMAC signing without sending the runner bearer credential

#### Scenario: Webhook signing follows GitHub semantics

- **WHEN** a `github.v3` subscription has an active signing secret version
- **THEN** the server computes `X-Hub-Signature-256` as `sha256=` plus the HMAC-SHA256 digest over the exact transmitted UTF-8 body, never returns the secret after creation or rotation, and binds retries and manual replay to an auditable secret version

#### Scenario: Destination query credentials are accepted without becoming public configuration

- **WHEN** an administrator supplies a GitHub-style payload URL containing a credential-bearing query such as a robot access token
- **THEN** the server validates the canonical HTTPS origin, host, port, and path under the existing network policy, separates and encrypts the complete query credential material, returns and audits only a redacted destination, reconstructs the exact query only at send time, follows no redirects, and never exposes it in browser state, API responses, delivery errors, metrics, logs, or webhook payloads

#### Scenario: Content policy is explicit, authorized, and safely editable

- **WHEN** an administrator creates or updates a notification subscription in the API or SPA
- **THEN** the configuration explicitly identifies delivery format, supported issue and comment actions, projected issue kinds, allowed comment classes, actor classes, signing mode, retry policy, and optimistic version, rejects unsupported combinations, and remains invisible and immutable to callers without integration-management permission even for a public repository

#### Scenario: Transactional retries and replay preserve compatible event identity

- **WHEN** a matching mutation commits, a receiver times out or fails, or an administrator manually redelivers a recorded notification
- **THEN** delivery remains downstream of the committed transactional outbox, retries never roll back the issue mutation, the GitHub-compatible delivery and payload describe the same immutable event revision, attempts are visible in the delivery ledger, and terminal failure enters the existing dead-letter and replay lifecycle

#### Scenario: Browser and receiver contract tests prove filtering and compatibility

- **WHEN** the notification webhook acceptance suite runs against a local capture receiver and an opt-in live GitHub-compatible robot endpoint
- **THEN** browser E2E configures a `github.v3` route, creates a selected key issue, a typed comment, and a human non-typed comment, verifies the receiver gets exactly the selected GitHub-compatible issue and human-comment events with no secret leakage, verifies typed-comment suppression and replay in the UI, and keeps live endpoint credentials outside source, fixtures, screenshots, and test output

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/160#issuecomment-4949730701

### Requirement: Interactive identity profiles, safe account avatars, and trusted-internal HTTP production mode

The self-hosted server MUST support interactive GitHub OAuth and standards-based OIDC login against operator-configured providers, normalize verified external profile metadata without using mutable profile fields as identity authority, and expose a safe same-origin account avatar across compatible APIs and the SPA; production MUST remain HTTPS-only by default but MUST provide an explicit operator-controlled trusted-internal HTTP mode that permits private-address or internal-DNS canonical origins and makes browser session, CSRF, callback, observability, and acceptance behavior internally consistent without weakening OAuth state, PKCE, identity, authorization, audit, or secret-handling guarantees.

#### Scenario: GitHub OAuth login works without public inbound reachability

- **WHEN** an operator registers a GitHub OAuth App whose callback is the server's canonical private-IP or internal-DNS `/api/v1/auth/{provider}/callback`, employees can reach that callback in their browser, and the server can reach the configured GitHub authorization, token, and user endpoints
- **THEN** the login page exposes the GitHub provider, the browser completes the authorization-code redirect, the server exchanges the code and revalidates the stable GitHub numeric user identity, and no public route from GitHub infrastructure to the server is required

#### Scenario: Trusted-internal HTTP requires explicit production opt-in

- **WHEN** production is configured with HTTP `API_PUBLIC_URL` or `WEB_PUBLIC_URL`
- **THEN** startup fails closed unless the operator explicitly selects trusted-internal HTTP mode; when selected, both canonical origins must form a coherent browser-reachable HTTP deployment with absolute root origins and no userinfo, path, query, fragment, or request-header-derived callback authority

#### Scenario: Default production transport policy remains HTTPS-only

- **WHEN** trusted-internal HTTP mode is absent, false, malformed, or conflicts with an HTTPS or mixed-scheme public-origin configuration
- **THEN** the server retains the existing production HTTPS requirement and refuses to silently infer insecure transport from a private IP, internal hostname, reverse-proxy header, development setting, or repository configuration

#### Scenario: Browser cookies are usable and bounded in trusted-internal HTTP mode

- **WHEN** a GitHub OAuth or OIDC callback establishes or rotates a browser session over an explicitly accepted HTTP canonical origin
- **THEN** the server emits session and CSRF cookies without the unusable `Secure` attribute while preserving opaque high-entropy values, session `HttpOnly`, the existing SameSite policies, path and lifetime bounds, fixation-safe replacement, idle and absolute expiry, CSRF validation, rotation, revocation, and audit semantics

#### Scenario: OAuth protections do not depend on TLS mode

- **WHEN** interactive login begins or completes in HTTPS production, trusted-internal HTTP production, development, or test
- **THEN** the server uses one-time state, PKCE S256, single-use bounded login transactions, canonical configured callback URLs, safe local return paths, exact provider and issuer binding, fresh user-profile lookup, generic authentication errors, and secret redaction in every mode

#### Scenario: Transport posture is observable without exposing secrets

- **WHEN** trusted-internal HTTP mode is enabled
- **THEN** startup logs, server metadata, operational diagnostics, and the authenticated administration UI clearly report the effective transport posture and canonical origins without exposing OAuth client secrets, authorization codes, access tokens, cookies, external claims, or provider credentials

#### Scenario: GitHub profile metadata includes the account avatar

- **WHEN** a GitHub OAuth callback successfully reads the authenticated GitHub `/user` representation
- **THEN** the adapter normalizes stable numeric subject, login, display name, authorized email metadata, and `avatar_url`, preserves the provider representation for audit-safe profile refresh, and never treats login, email, display name, or avatar URL as the stable account key

#### Scenario: Generic OIDC can supply equivalent profile imagery

- **WHEN** a verified OIDC ID token or configured user-info response contains a valid `picture` claim
- **THEN** the adapter normalizes it into the same provider-neutral avatar field used by GitHub OAuth while retaining issuer, subject, audience, nonce, signature, and configured-claim validation as the identity authority

#### Scenario: Profile refresh and linked identities select avatars deterministically

- **WHEN** an existing user signs in again with changed provider profile metadata or links more than one external identity
- **THEN** the server refreshes claims and permitted display metadata for the exact provider identity, keeps the local login and stable user ID immutable, selects the account's profile-source identity by a deterministic explicit rule, and does not let an unrelated linked provider silently oscillate or overwrite the selected avatar

#### Scenario: External avatar retrieval cannot become an SSRF or tracking channel

- **WHEN** the selected provider supplies an external avatar URL
- **THEN** the server validates it against operator-controlled provider origins and bounded network policy, denies private, loopback, link-local, metadata, unsafe redirect, oversized, timeout, and invalid media responses, caches or proxies only an accepted image through a same-origin stable endpoint, and does not expose the raw provider URL or claims to browsers

#### Scenario: Compatible APIs and the SPA present one safe avatar identity

- **WHEN** an authorized account, issue, comment, change, membership, or user candidate representation includes a visible user
- **THEN** the native context and GitHub-compatible user representations expose the same canonical same-origin `avatar_url`, and account, navigation, issue, comment, change, and administration surfaces render it with accessible text, lazy loading, fixed layout, and an initials fallback without weakening the existing self-only image CSP

#### Scenario: Avatar absence or provider failure does not block login

- **WHEN** a provider omits an avatar, the URL is rejected, the image is unavailable, or cached content expires during a provider outage
- **THEN** identity resolution and session creation remain successful, APIs return an empty or fallback avatar representation without leaking internal errors, the UI displays deterministic initials, and the failure remains observable to operators without exposing user credentials or claims

#### Scenario: Browser acceptance covers HTTP callback and avatar behavior

- **WHEN** the authentication acceptance suite runs against an operator-controlled GitHub OAuth or protocol-conformance fixture
- **THEN** it proves default production HTTP refusal, explicit trusted-internal HTTP startup, browser login and callback over a private-address HTTP origin, usable session and CSRF cookies, avatar synchronization and refresh, same-origin rendering with initials fallback, malicious avatar rejection, logout and session rotation, and the absence of OAuth secrets or tokens from source, screenshots, browser-visible state, logs, and test output

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/160#issuecomment-4949894099

### Requirement: GitHub organization membership admission at interactive login

The self-hosted server MUST make GitHub OAuth admission an explicit operator policy distinct from GitHub authentication and local authorization, and a provider configured for organization-restricted admission MUST verify the freshly authenticated user's active membership in an approved GitHub organization before creating or updating the local identity, provisioning a user, or issuing a browser session; the check MUST use the transient user OAuth credential only for the configured membership query, fail closed on indeterminate results, and preserve stable-subject, PKCE, state, secret-redaction, audit, and least-privilege guarantees.

#### Scenario: Production GitHub admission intent is explicit

- **WHEN** an operator enables a `github-oauth` provider in production
- **THEN** the provider configuration must explicitly select organization-restricted admission with one or more approved organizations or deliberately select unrestricted admission, and startup rejects an omitted, malformed, duplicated, or unsupported admission policy rather than silently accepting every GitHub account

#### Scenario: Active approved organization membership admits the user

- **WHEN** GitHub returns the authenticated user's membership for a configured approved organization with state `active` and the organization identity matches the operator's normalized configuration
- **THEN** the login transaction may continue to stable numeric subject resolution, identity profile refresh or just-in-time local user provisioning, local authorization, session replacement, and the configured safe return path

#### Scenario: Pending, absent, or unapproved membership is denied

- **WHEN** the authenticated user is only pending, is an outside collaborator without qualifying membership, belongs only to another organization, has left every approved organization, or GitHub returns not found for all configured organizations
- **THEN** the server denies login before local provisioning or session issuance, preserves any pre-existing account and sessions unchanged, returns a generic non-enumerating authentication result, and records a redacted admission denial suitable for operator diagnosis

#### Scenario: Membership lookup failure fails closed

- **WHEN** the GitHub membership endpoint is unavailable, rate limited, blocked by organization policy, denied by SSO, returns malformed data, exceeds response or time bounds, or the OAuth grant lacks the required visibility
- **THEN** the server does not treat the user as a member, does not fall back to email domain, public profile text, cached membership, repository access, or OAuth App approval, and exposes an actionable operator diagnostic without leaking the access token or organization-private data

#### Scenario: Membership scope is requested only when required

- **WHEN** a GitHub provider uses organization-restricted admission
- **THEN** the authorization request includes the minimum membership-read scope needed to evaluate private membership in addition to basic identity scopes, the quickstart explains organization approval and SSO implications, and an unrestricted provider does not gain organization scope merely because the server supports restricted admission

#### Scenario: Organization configuration remains stable across display renames

- **WHEN** an operator configures an approved organization by login or updates a provider after an organization rename
- **THEN** the server resolves and compares stable GitHub organization identity where available, stores only non-secret normalized admission metadata, rejects ambiguous or changed identity rather than trusting display text, and requires an explicit operator update when the configured organization identity changes

#### Scenario: Transient OAuth credentials are not retained for admission

- **WHEN** the server finishes the `/user` and organization-membership checks during one GitHub OAuth callback
- **THEN** it discards the GitHub user access token after the callback, never persists or returns it, never includes it in local identity claims or audit metadata, and persists only the minimum normalized user profile and admission evidence needed to explain the decision

#### Scenario: Admission policy composes with local repository authorization

- **WHEN** a GitHub organization member is admitted to the server
- **THEN** membership proves only eligibility to obtain a local account and does not grant site-admin, organization, repository, integration, runner, evidence, or private-resource authority beyond explicit local role and membership assignments

#### Scenario: Login acceptance covers membership transitions and failures

- **WHEN** the GitHub OAuth conformance and browser E2E suites exercise organization-restricted admission
- **THEN** they prove active-member success, pending and non-member denial before provisioning, organization rename or identity mismatch, missing scope, SSO or app restriction, rate limit and upstream failure, unrestricted explicit mode, redacted diagnostics, transient-token disposal, and successful local authorization after admission

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/160#issuecomment-4949942453

### Requirement: External authentication integration guide and GitHub OAuth App quickstart

The self-hosted server MUST ship an authoritative, versioned operator guide for external interactive authentication that explains the provider-neutral trust and lifecycle model, documents every supported `AUTH_PROVIDERS_FILE` field and canonical callback rule, and provides a complete step-by-step GitHub OAuth App quickstart plus an OIDC integration path; examples MUST be safe to copy, contain no real credentials, stay synchronized with executable configuration validation and browser behavior, and cover HTTPS and explicitly accepted trusted-internal HTTP deployments, organization admission, profile avatars, verification, rotation, and troubleshooting.

#### Scenario: Guide explains the external identity model before configuration

- **WHEN** an operator opens the external authentication documentation
- **THEN** it distinguishes interactive GitHub OAuth from GitHub Actions OIDC and generic enterprise OIDC, explains that provider issuer and immutable subject identify the external identity while the local user and RBAC model remain authoritative, and states that login eligibility, provider authentication, local authorization, source bindings, and code-provider credentials are separate concerns

#### Scenario: Configuration reference is complete and secret-safe

- **WHEN** an operator needs to configure or review an authentication provider
- **THEN** the guide documents stable provider UUID, safe name, kind, issuer, client ID, client secret, scopes, optional GitHub Enterprise endpoints, admission policy, avatar policy, provider-file ownership and `0600` mode, size and JSON constraints, canonical API and web origins, callback derivation, environment variables, startup validation, and the rule that secrets never belong in commands, source control, screenshots, logs, or API responses

#### Scenario: GitHub OAuth App quickstart is step-by-step and runnable

- **WHEN** an operator follows the GitHub quickstart from a fresh server
- **THEN** the documented sequence chooses the employee-visible canonical origin, creates a GitHub OAuth App, sets its homepage and exact `/api/v1/auth/{provider}/callback`, generates and preserves a provider UUID, writes a minimal provider JSON with placeholders, chooses identity and optional organization-membership scopes, mounts the secret file, sets `API_PUBLIC_URL`, `WEB_PUBLIC_URL`, and `AUTH_PROVIDERS_FILE`, starts or restarts the server, verifies `/api/v1/auth/providers`, signs in through the SPA, verifies `/user` and the account avatar, and confirms local authorization remains separate

#### Scenario: Quickstart covers private-IP and internal-DNS HTTP callbacks

- **WHEN** the deployment has no certificate or public inbound endpoint and intentionally uses trusted-internal HTTP mode
- **THEN** the guide explains that the employee browser, not GitHub infrastructure, follows the callback; lists browser-to-GitHub, browser-to-internal-server, and server-to-GitHub reachability requirements; shows private-IP and internal-DNS examples; enables the explicit HTTP production posture; warns about plaintext session risk and compensating network controls; and verifies usable session and CSRF cookies without suggesting development mode as a production workaround

#### Scenario: Organization-restricted GitHub admission is documented end to end

- **WHEN** the server is intended only for members of approved GitHub organizations
- **THEN** the quickstart shows the admission configuration and minimum membership-read scope, explains active versus pending membership, organization OAuth App approval and SSO behavior, describes explicit unrestricted mode, and includes a verification step proving a member succeeds while a non-member is denied before local provisioning

#### Scenario: Enterprise OIDC integration has an equivalent operational path

- **WHEN** an operator selects a standards-based enterprise OIDC provider
- **THEN** the guide covers issuer discovery, client registration, exact callback, client secret, openid/profile/email and optional group scopes, issuer-subject binding, nonce and PKCE behavior, preferred username, name, email and picture claims, group or provider admission, certificate and egress prerequisites, provider rotation, and login verification without presenting GitHub-specific concepts as generic OIDC requirements

#### Scenario: Secret and provider rotation preserves identity continuity

- **WHEN** an operator rotates a client secret, changes scopes, updates approved organizations, or moves between provider endpoints
- **THEN** the guide tells the operator which identifiers must remain stable, when a change creates a new identity realm, how to stage and verify the provider file before restart, how to preserve or explicitly link existing users, how to roll back safely, and how to revoke the prior OAuth or OIDC credential without printing secret values

#### Scenario: Troubleshooting maps symptoms to safe checks

- **WHEN** provider discovery, redirect, token exchange, user lookup, membership admission, avatar retrieval, session cookie, CSRF, or return-path behavior fails
- **THEN** the guide provides grep-friendly error classes and non-secret checks for callback mismatch, wrong public origin, malformed provider file, missing scope, SSO or organization restriction, unavailable egress, trusted-internal HTTP not enabled, unusable cookie attributes, provider identity mismatch, and rejected avatar URLs without recommending secret dumps or raw callback query logging

#### Scenario: Documentation examples are continuously verified

- **WHEN** the repository test and documentation validation suites run
- **THEN** all provider JSON examples parse under the production loader, generated callback URLs match route composition, links are checked, secret placeholders are detected, HTTPS and trusted-internal HTTP quickstarts run against controlled GitHub OAuth and OIDC fixtures, browser smoke tests confirm the documented login and avatar flow, and drift between CLI/server behavior and the guide fails CI

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/160#issuecomment-4949941981
