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
