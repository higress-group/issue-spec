# comment-events-and-webhooks

## Purpose

Define transactional issue and comment events, runner delivery credentials, GitHub-compatible notification policies, retry, suppression and replay behavior.

Proposal Issues:
- https://github.com/higress-group/issue-spec/issues/160

## Requirements

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

### Requirement: Organization-scoped Runner webhooks are operable

The self-hosted product MUST let an authorized organization integration manager create and operate an organization-scoped `issue-spec.v1` Runner subscription, and `runner serve` MUST bind its configured credential to that authoritative organization subscription before accepting dynamic repository enrollment.

#### Scenario: organization manager creates a Runner subscription

- **WHEN** an organization integration manager opens the organization Runner webhook surface and submits a valid Runner receiver configuration
- **THEN** the SPA omits `repository_id`, the Server creates an organization-scoped `issue-spec.v1` bearer subscription, and the show-once secret and subscription ID are displayed without weakening repository-scoped webhook behavior

#### Scenario: organization Runner verifies subscription authority

- **WHEN** `runner serve` starts in organization mode with a subscription ID and secret
- **THEN** startup verifies that the active subscription belongs to the configured organization, uses `issue-spec.v1` bearer delivery, selects the supported comment events, and does not accept a repository-scoped or cross-organization subscription as organization authority

#### Scenario: organization webhook lifecycle remains manageable

- **WHEN** an authorized organization integration manager lists, pauses, resumes, rotates, inspects, or revokes the organization subscription
- **THEN** the existing optimistic version, secret rotation, suppression, audit, retry, redaction, and terminal revocation contracts remain in force at organization scope

#### Scenario: repository webhook management remains isolated

- **WHEN** an operator continues to use a repository-scoped webhook or a caller lacks organization integration-management authority
- **THEN** existing repository routes remain compatible and unauthorized organization webhook configuration is concealed or denied without revealing destinations or secret metadata

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/456#issuecomment-5352635785
