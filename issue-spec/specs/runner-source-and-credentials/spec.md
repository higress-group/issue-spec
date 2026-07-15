# runner-source-and-credentials

## Purpose

Define trusted source-repository resolution and short-lived purpose-scoped credentials supplied to isolated runner jobs.

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

### Requirement: Sandboxed agents receive only short-lived purpose-scoped credentials

The runner MUST broker short-lived repository, job, audience and purpose scoped credentials for issue API and source access, MUST keep long-lived human and service credentials outside the sandbox, and MUST revoke or expire every lease when the job or session ends.

#### Scenario: child issue access uses a delegated token file

- **WHEN** a sandboxed coordinator starts
- **THEN** the runner MUST mint or obtain a short-lived issue token with only required repository scopes, write it to a session-private read-only file, expose the selected profile and token-file path, and pass child auth status before agent dispatch

#### Scenario: an uninterrupted agent job runs for multiple days

- **WHEN** a `/new` or `/resume` job remains active beyond a minute-scale credential lifetime
- **THEN** its delegated issue token MUST remain usable for up to the seven-day default job lifetime, remain bound to the repository and job, and be revoked immediately when the job reaches a terminal state

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
