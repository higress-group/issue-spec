# multi-tenant-identity-and-access

## Purpose

Define tenant isolation, repository administration, external identity, sessions, scoped credentials, organization admission, account avatars and trusted internal transport.

Proposal Issues:
- https://github.com/higress-group/issue-spec/issues/160

## Requirements

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

#### Scenario: a site-wide PAT preserves the subject's live authorization

- **WHEN** a personal or managed PAT is created with all repositories across the site
- **THEN** the PAT MUST omit a repository allowlist, MUST remain bounded by the subject's live organization memberships, repository roles and token scopes, and MUST NOT create or elevate organization membership or repository authority

#### Scenario: air-gapped recovery does not depend on GitHub

- **WHEN** the configured identity provider is unavailable in an air-gapped deployment
- **THEN** an audited operator-only recovery command MUST be able to mint a short-lived one-time administrative credential without enabling an unauthenticated network backdoor

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/160#issuecomment-4926527893

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
