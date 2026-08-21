# runner-source-and-credentials

## Purpose

Define trusted source-repository resolution and the narrowly scoped issue and source credentials supplied to isolated runner jobs.

Proposal Issues:
- https://github.com/higress-group/issue-spec/issues/160

## Requirements

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

### Requirement: Sandboxed agents receive explicit repository-checked credentials

For self-hosted issue API access, the runner MUST reuse the operator-selected,
origin-bound profile PAT through a stable private read-only file shared by jobs
served by that process. The PAT MUST grant every explicitly configured
repository and include the minimum runner scopes; it MAY grant all repositories,
a selected set of repositories, or additional scopes. Source access MUST
continue to use an operator-owned, purpose-scoped credential helper or an
explicitly enabled host SSH boundary. Neither credential may be embedded in
repository state, prompts, clone URLs, or durable runner records.

#### Scenario: child issue access uses the profile PAT file

- **WHEN** a sandboxed coordinator starts
- **THEN** the runner MUST expose the selected self-hosted profile and stable private token-file path, reuse the configured profile PAT without a per-job delegation exchange, and pass child auth status before agent dispatch

#### Scenario: issue access remains stable across jobs and resume

- **WHEN** the runner dispatches successive `/new` or `/resume` jobs for any configured repository
- **THEN** each job MUST receive the same profile PAT file capability, job cleanup MUST NOT delete that file, and PAT rotation or revocation MUST remain an explicit operator action

#### Scenario: every configured repository is checked independently

- **WHEN** one process serves multiple repositories
- **THEN** startup and per-job preflight MUST verify that the PAT grants the current repository and that its identity has the required live repository role, without rejecting unrelated repository grants or additional scopes

#### Scenario: optional delegation remains outside the default runner path

- **WHEN** another integration needs a short-lived delegated issue credential
- **THEN** the server MAY issue and revoke that credential through its delegation API, but `runner serve` MUST NOT require or invoke that exchange for its normal job lifecycle

#### Scenario: source clone uses an operator credential helper

- **WHEN** the runner clones or writes the bound source repository
- **THEN** it MUST use an operator-owned credential helper or askpass lease, MUST NOT embed a token in the URL, and MUST not copy the credential into issue, workspace or agent prompt content

#### Scenario: credential failure cannot fall back to host secrets

- **WHEN** the profile PAT file or required source credential cannot be prepared
- **THEN** the job MUST fail before agent startup and MUST NOT inherit ISSUE_SPEC_TOKEN, GH_TOKEN, GITHUB_TOKEN or a host credential store as a fallback

#### Scenario: job-scoped source leases are revoked and audited

- **WHEN** a job completes, is canceled, fails or exceeds its lifetime
- **THEN** the runner MUST revoke revocable source leases, delete job-private source credential files, record the lease lifecycle without secret values, and preserve only the process-level profile PAT file for later authorized sessions

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/160#issuecomment-4932917391

### Requirement: GitHub polling runners bind configured repositories from authenticated metadata

A native GitHub polling runner MUST resolve every explicitly configured repository into a complete credential-free operator binding using repository metadata returned by the selected authenticated GitHub backend, MUST reject unavailable, incomplete, mismatched, or unsafe metadata during preflight before command intake, and MUST re-resolve the same trusted source for new dispatch and resume validation without deriving clone coordinates from issue content, webhook payloads, hostnames, or repository slug transformation.

#### Scenario: preflight validates each configured GitHub binding

- **WHEN** runner poll preflight evaluates an explicitly configured repository and the authenticated backend returns matching complete repository metadata
- **THEN** preflight succeeds only after validating a credential-free clone URL, canonical web URL, stable repository identity, and API-observed default branch as an operator binding

#### Scenario: invalid GitHub metadata fails before intake

- **WHEN** authenticated repository metadata is unavailable, incomplete, mismatched to the configured repository, ambiguous, or contains an unsafe URL
- **THEN** runner poll preflight fails with an actionable repository-binding diagnostic and does not begin accepting commands

#### Scenario: new GitHub sessions pin authenticated repository metadata

- **WHEN** a native GitHub polling runner dispatches a new command after successful preflight
- **THEN** dispatch re-resolves authenticated repository metadata, pins the complete operator snapshot before workspace creation, and never uses issue or comment content as a clone source

#### Scenario: GitHub resume detects live metadata drift

- **WHEN** a native GitHub polling runner resumes a session after the authenticated repository identity, clone URL, web URL, or default branch differs from the pinned snapshot
- **THEN** resume fails with the existing binding-drift diagnostic before touching the workspace

#### Scenario: self-hosted binding resolution stays fail closed

- **WHEN** runner serve resolves a self-hosted issue repository
- **THEN** it continues to require its authorized active server binding and does not fall back to GitHub polling metadata or slug-derived clone coordinates

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/377#issuecomment-5151525429

### Requirement: Organization runners enroll repositories from authoritative bindings

An organization-scoped self-hosted Runner MUST discover repositories through a concurrency-safe authenticated registry, MUST require live repository eligibility and one active credential-free Source Binding before command dispatch, and MUST never derive repository or clone authority from webhook payload content.

#### Scenario: new repository enrolls on its first valid command

- **WHEN** an organization delivery references a repository created after Runner startup and the Runner identity can authoritatively resolve the repository, required live permission, and active Source Binding
- **THEN** the registry admits the repository without process restart and the existing authoritative comment, authorization, credential, workspace, and job path handles the command

#### Scenario: concurrent first events share one trusted resolution

- **WHEN** multiple deliveries concurrently reference the same previously unseen repository
- **THEN** the Runner coalesces or safely serializes authoritative discovery, publishes one consistent UUID/name/binding entry, and preserves delivery and command idempotency

#### Scenario: ineligible repository never dispatches

- **WHEN** a delivery references a cross-organization, invisible, missing, ambiguous, permission-ineligible, PAT-excluded, or unbound repository
- **THEN** the Runner records a bounded terminal non-dispatch reason, creates no job or workspace, requests no Git credential, and does not use the payload to construct a clone target

#### Scenario: job boundaries recheck changing authority

- **WHEN** an enrolled repository later loses permission, leaves PAT scope, or changes or removes its active Source Binding
- **THEN** per-job capability and binding checks fail closed, existing sessions retain their pinned snapshot semantics, and resume never writes a different repository

#### Scenario: explicit repository mode remains compatible

- **WHEN** `runner serve` is configured with one or more explicit `--repo` values instead of organization mode
- **THEN** the Runner retains exact configured-scope resolution and rejects repositories outside that allow-list without enabling dynamic enrollment

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/456#issuecomment-5352636245
