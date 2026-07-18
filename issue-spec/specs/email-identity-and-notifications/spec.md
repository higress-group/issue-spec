# email-identity-and-notifications

## Purpose

Define the long-lived behavior contract for this capability.

Proposal Issues:
- https://github.com/higress-group/issue-spec/issues/268

## Requirements

### Requirement: First-session onboarding and verified notification email

The server MUST require newly provisioned human accounts to submit a preferred name and notification email during their first browser session when operator mail capability is enabled, MUST bind at most one case-insensitively unique notification address only after explicit single-use verification, MUST keep provider email metadata separate from verified local profile state, and MUST preserve existing account access and identity continuity.

#### Scenario: New human account submits onboarding without waiting for mail delivery

- **WHEN** a human account provisioned after rollout opens its first authenticated browser session while operator mail capability is enabled
- **THEN** the SPA requires a non-empty preferred name and syntactically valid email submission, persists pending verification, and allows normal use after submission while email notification remains disabled until confirmation

#### Scenario: Existing account remains compatible

- **WHEN** an account created before the migration signs in without a verified notification email
- **THEN** the account remains usable without a forced onboarding dialog and can request, replace, resend, or remove email binding from its account page when operator mail capability is enabled

#### Scenario: Disabled mail capability preserves current behavior

- **WHEN** an operator does not configure the mail capability
- **THEN** the server remains usable, does not present email onboarding or binding controls, reports the capability unavailable, and may prompt still-incomplete post-rollout human accounts if the operator enables it later

#### Scenario: Verification proves control without scanner-triggered binding

- **WHEN** a user follows a current verification message
- **THEN** the same-origin page requires an explicit confirmation action, accepts only the protected single-use unexpired challenge, binds the address to that account, and rejects expired, consumed, superseded, conflicting, or disabled-account challenges without disclosing another identity

#### Scenario: Confirmed replacement preserves the current address until ready

- **WHEN** a user requests a different notification address
- **THEN** the prior verified address remains active until the replacement is confirmed and the successful confirmation atomically supersedes it

#### Scenario: Provider refresh cannot overwrite verification

- **WHEN** a later external login supplies a missing, changed, or equal provider email claim
- **THEN** the server may retain it only as untrusted provider metadata and does not create, replace, merge, authorize, or verify the local notification binding from that claim

#### Scenario: Verification and mail secrets remain redacted

- **WHEN** verification is requested, delivered, retried, confirmed, audited, logged, diagnosed, or represented through an API
- **THEN** plaintext verification tokens, mail credentials, and private mail content are excluded from stored token fields, logs, audit metadata, metrics, screenshots, fixtures, and reusable responses; complete addresses are limited to the owning user's private profile and never appear in public or cross-user APIs, logs, diagnostics, or errors

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/268#issuecomment-5004640335

### Requirement: Authenticated site-wide mention discovery with stable targets

The server and SPA MUST provide bounded authenticated mention discovery across all active human accounts on the current instance, MUST match preferred names and immutable logins while returning only minimal public profile fields, MUST store canonical @login text and resolve it server-side to stable user identity, and MUST NOT expose email, membership, repository-permission, disabled-account, or service-account data through the search surface.

#### Scenario: Authenticated user searches the whole site

- **WHEN** an authenticated issue commenter types an @ prefix and a preferred-name or login fragment
- **THEN** the SPA performs a debounced bounded query and shows matching active human accounts from the whole instance with avatar, preferred name, and @login even when a candidate cannot read the current repository

#### Scenario: Duplicate or mutable names do not change the target

- **WHEN** multiple users share a preferred name or a selected user later changes that name
- **THEN** the candidate login disambiguates the selection, raw Markdown contains the immutable @login, and the persisted mention continues to identify the same local user

#### Scenario: Directory fields and callers are bounded

- **WHEN** an unauthenticated caller, service account, disabled account, broad query, or repeated requester attempts to use mention discovery
- **THEN** the server requires an authenticated human session, excludes non-active-human results, enforces bounded prefix results and rate limits, and returns no email, organization membership, repository authority, or hidden account facts

#### Scenario: Server parses only mention-capable Markdown

- **WHEN** a comment contains @-shaped text in prose, code spans, fenced or indented code, URLs, or email-address text
- **THEN** the server resolves only valid canonical prose mentions, ignores excluded contexts, de-duplicates stable user targets, and does not trust client-supplied mention metadata

#### Scenario: Compatible clients receive the same semantics

- **WHEN** the SPA, CLI, PAT client, or delegated compatible API creates or edits a comment containing canonical mentions
- **THEN** the authoritative server-side parser and identity resolution produce the same mention records without changing the raw comment body or existing compatible response shape

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/268#issuecomment-5004640846

### Requirement: Permission-gated reliable mention email delivery

The server MUST create idempotent durable email work for newly introduced comment mentions, MUST deliver only to active recipients with a verified notification address and live read authority for the repository, MUST keep SMTP failure independent of comment commit, and MUST bound, retry, suppress, and diagnose delivery without exposing credentials, addresses, tokens, or private issue content.

#### Scenario: Eligible new mention creates one notification

- **WHEN** a committed new comment mentions the same eligible user one or more times
- **THEN** the comment transaction records one stable recipient and one idempotent email delivery containing only bounded actor, repository, issue, excerpt, and canonical comment-link data

#### Scenario: Site-wide discovery does not bypass repository access

- **WHEN** a comment canonically mentions an active site user who cannot read the current repository
- **THEN** the raw mention remains valid and renderable but the server creates no deliverable private-content email for that recipient

#### Scenario: Edits notify only newly introduced recipients

- **WHEN** a comment author edits a comment, repeats a mention, removes a mention, mentions themselves, or re-adds an already notified recipient
- **THEN** the server sends only to eligible recipients newly introduced by that revision, never sends to the acting author, and does not duplicate prior delivery

#### Scenario: Live access is rechecked before send

- **WHEN** a queued recipient becomes disabled, loses repository read access, removes the verified address, or the repository becomes invisible before SMTP delivery
- **THEN** the worker suppresses the delivery without rendering or sending private issue content and records only a redacted reason

#### Scenario: SMTP failure does not roll back comments

- **WHEN** mail configuration is unavailable, authentication fails, the relay times out, or delivery returns a retryable or terminal error
- **THEN** the committed comment remains successful, the worker applies fixed bounded retry and terminal state, verification mail can be resent manually, and operational output distinguishes relay acceptance from final mailbox delivery

#### Scenario: Mail configuration is secret-safe and minimal

- **WHEN** an operator enables the mail capability
- **THEN** the server uses verified TLS, secret-file or platform-secret credential injection, bounded timeouts and concurrency, no plaintext downgrade, no IMAP dependency, and no provider-specific credentials in source, commands, logs, APIs, or documentation examples

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/268#issuecomment-5004641396

### Requirement: Repository subscription and change milestone email

The server MUST allow an eligible active human user to opt in to or out of one repository-level email subscription, MUST notify current eligible subscribers exactly once logically for a new ordinary issue or for the first observation of a supported change milestone, MUST avoid duplicate generic and milestone mail for the same artifact creation, and MUST revalidate verified address, subscription, account state, and repository read authority before delivery.

#### Scenario: Eligible user manages one repository subscription

- **WHEN** an active human user with a verified notification address and current repository read permission subscribes to or unsubscribes from a repository
- **THEN** the server atomically creates or removes that user's single explicit repository subscription, excludes service accounts, applies the change only to later events, and does not infer or replay notification state from membership, authorship, mentions, or history

#### Scenario: Ordinary issue creation notifies current subscribers

- **WHEN** a user creates an ordinary issue in a repository with eligible subscribers
- **THEN** the issue commit creates at most one logical delivery per current subscriber other than the actor, and the plain-text email identifies the actor and repository and includes the issue number, title, normalized issue content up to 64 KiB, an explicit truncation marker when needed, and a canonical link

#### Scenario: Artifact issue creation sends one specialized milestone message

- **WHEN** a newly created issue projects as a valid proposal, design, or implement artifact
- **THEN** each current eligible subscriber other than the actor receives at most one logical change-milestone delivery identifying the change, new stage, triggering artifact, bounded content, and link, and the same issue creation does not also create a generic issue-created delivery

#### Scenario: Completed change sends one terminal milestone

- **WHEN** a relevant mutation causes the authoritative change projection to transition from a non-completed lifecycle to completed
- **THEN** each current eligible subscriber other than the actor receives at most one logical completed-milestone delivery with the change identity and canonical link

#### Scenario: Milestones and subscriptions do not replay history

- **WHEN** a milestone is reprocessed, a change regresses or re-enters a stage, or a user subscribes after an issue or milestone occurred
- **THEN** repository-wide milestone uniqueness and recipient delivery idempotency prevent duplicate or retroactive notification

#### Scenario: Final send revalidates eligibility

- **WHEN** a queued repository notification is claimed after the recipient unsubscribed, lost repository read permission, removed the verified address, became disabled, or became a service account
- **THEN** the worker suppresses the delivery with a redacted reason and does not disclose or send repository content

#### Scenario: Unselected repository activity remains silent

- **WHEN** an issue is edited, including conversion of an ordinary issue into an artifact, commented on, labeled, closed, or reopened, or a change becomes blocked or merely closed without completing
- **THEN** the first release creates no repository-subscription email for that activity

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/268#issuecomment-5004824390

### Requirement: Operator-constrained notification email domains

When an operator configures one or more notification email domain suffixes, the server MUST accept onboarding, binding, replacement, resend, and confirmation only for addresses whose normalized domain equals an allowed suffix or is a dot-delimited subdomain of it, MUST compare domains case-insensitively without naive string-suffix matches, and MUST suppress all notification delivery to previously verified addresses that no longer satisfy the current policy. When no suffix is configured, the existing syntactically valid verified-email behavior MUST remain unchanged.

#### Scenario: Allowed corporate address proceeds

- **WHEN** the configured suffix is corp.example.test and a user submits an address at corp.example.test or a dot-delimited subdomain
- **THEN** the normal verification lifecycle proceeds and notification delivery remains eligible after confirmation

#### Scenario: Lookalike and unrelated domains are rejected

- **WHEN** a submitted address uses an unrelated domain, a prefix lookalike such as evilcorp.example.test, or a trailing-domain lookalike such as corp.example.test.evil.example
- **THEN** the server rejects the request before queueing verification mail and returns a stable domain-policy validation error

#### Scenario: Policy change suppresses an existing address

- **WHEN** an operator enables or narrows the configured suffixes after a user previously verified a nonconforming address
- **THEN** mention, repository, milestone, and verification resend or confirmation delivery is suppressed until the user verifies an allowed replacement

#### Scenario: Unconfigured policy preserves compatibility

- **WHEN** the SMTP capability is enabled without any allowed notification email suffix
- **THEN** all syntactically valid addresses continue to use the existing verification and notification behavior

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/268#issuecomment-5009667349

### Requirement: Repository subscription control on the issue list

The repository issue list MUST expose the existing repository-wide email subscription control to authenticated users with repository read access when repository email subscriptions are enabled, MUST use the same subscription state and mutation API as repository settings, MUST guide users without a verified notification email to account settings, and MUST NOT show the repository subscription control on individual issue details or represent the action as an issue-only subscription.

#### Scenario: Authenticated reader toggles the repository subscription

- **WHEN** an authenticated reader opens the issue list for a repository with email subscriptions enabled and a verified notification email
- **THEN** the list header shows the current repository subscription state and toggling it updates the same repository-wide subscription used by repository settings

#### Scenario: Reader needs a verified email

- **WHEN** an authenticated reader opens the issue list without a verified notification email
- **THEN** the subscription control links to account settings instead of creating a subscription

#### Scenario: Individual and public issue views remain unambiguous

- **WHEN** a user opens an individual issue, or an unauthenticated visitor opens a publicly readable issue list
- **THEN** the page does not expose a repository subscription mutation control

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/268#issuecomment-5009979521

### Requirement: Issue-first repository navigation

The authenticated application navigation MUST place Issues first, MUST replace the separate Overview and direct first-organization Repositories entries with one Repositories entry backed by the organization-selection landing page, MUST retain canonical organization and repository routes, and MUST display navigation context for the selected organization instead of assuming the first visible organization.

#### Scenario: Navigation starts with issues

- **WHEN** an authenticated user opens the desktop sidebar or mobile navigation
- **THEN** Issues is the first workspace destination and Repositories appears once

#### Scenario: Repositories starts with organization selection

- **WHEN** the user chooses Repositories
- **THEN** the landing page lists visible organizations and selecting one opens its existing repository list

#### Scenario: Existing repository links remain compatible

- **WHEN** the user follows an existing canonical organization or repository URL
- **THEN** the route continues to resolve and the shell identifies the organization from the current path

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/268#issuecomment-5009979689

### Requirement: Issue-first product presentation

The leading README workspace screenshot MUST show the repository issue list, including the repository subscription entry when available, and the Chinese Changes workspace headline MUST use the concise existing phrase “慎终如始，则无败事” instead of the longer change-specific phrase.

#### Scenario: Reader sees the primary product workflow

- **WHEN** a reader opens the English or Chinese README
- **THEN** the first product screenshot shows the Issue list rather than the overview dashboard

#### Scenario: Changes headline stays concise

- **WHEN** a Chinese user opens the Changes workspace
- **THEN** its headline is “慎终如始，则无败事”

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/268#issuecomment-5010424793
