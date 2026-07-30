# Code provider bridge protocol v1

Self-hosted issue-spec keeps issue data and code-host facts behind separate
trust boundaries. The issue server is authoritative for issues and typed
workflow artifacts. An operator-installed bridge reports normalized,
revision-bound code evidence or performs an explicitly requested external
mutation. Core evaluates every gate itself; a bridge cannot return an
`approved` boolean.

For platform assessment, wrapper scaffolding, operator registration, Source
Binding, and work-tracker boundaries, see
[Integrate company code and work platforms](../enterprise-provider-integration.md).

## Trust and registration

The operator registers a provider key and its implementation when starting the
CLI/server process. Repository `issue-spec/config.yaml` may select only that
key and an evidence policy:

```yaml
external_code:
  provider_key: code.example
  evidence:
    required: [review, check, merge]
    required_checks: [unit, dco]
    freshness:
      review: 24h
      check: 1h
```

Repository configuration containing an executable, arguments, environment, or
credential source is rejected. Provider keys do not grant authority: evidence
ingestion still requires an active supported credential with explicit
`evidence:write`, access to the exact repository, and an authenticated active
identity with live `write`-or-higher repository permission. Repository roles
and `repo`, `admin:repo`, or `issues:write` scopes do not replace the evidence
scope, and the evidence scope does not replace repository permission.

### Deprecated Evidence Writer compatibility API

The native assignment routes and `repository_evidence_writers` rows remain
available for one release window so mixed-version deployments can upgrade and
roll back safely:

```text
GET /api/v1/orgs/{org}/repos/{repo}/evidence/writers/me
PUT /api/v1/orgs/{org}/repos/{repo}/evidence/writers/{user}
```

These routes are deprecated and their state is non-authoritative: active rows do
not grant evidence publication, while inactive or absent rows do not deny it.
New Runner versions do not call them. Upgrade Runner before or with Server; an
older Runner still checks the legacy row during client-side preflight, so retain
rows until all Runners are upgraded. Rolling back to an older Server binary
restores the former assignment gate, and any rows removed after upgrade must be
recreated before rollback.

For the CLI, command bridges are registered by pointing
`ISSUE_SPEC_CODE_PROVIDERS_FILE` at a clean absolute private regular file
(POSIX mode `0600` or stricter and one hard link, or an equivalent private
Windows ACL; at most 1 MiB). The file is strict
JSON; unknown/duplicate fields and unsupported versions fail closed:

```json
{
  "version": 1,
  "providers": {
    "code.example": {
      "path": "/opt/issue-spec/bin/code-example-bridge",
      "args": ["serve-stdio"],
      "environment": ["CODE_EXAMPLE_TOKEN_FILE=/run/secrets/code-example"],
      "timeout": "30s",
      "max_output_bytes": 1048576
    }
  }
}
```

This process-owned file is never discovered from the repository, and workflow
configuration cannot replace any registered executable or credential input.

## Process boundary

An operator may register an in-process implementation of
`codereview.Provider`, or construct `codereview.CommandProvider` from trusted
process configuration. The command implementation:

- is a clean absolute executable path and is invoked directly without a shell;
- receives only the operator-configured arguments and environment;
- has a timeout no greater than two minutes;
- has independent bounded stdout and stderr (1 KiB through 4 MiB);
- receives at most 1 MiB of request JSON;
- must emit exactly one strict JSON response with no unknown fields;
- is killed on cancellation or timeout.

The operator should run bridges as a dedicated low-privilege identity, provide
the narrowest credential scopes, isolate their network access, and rotate
credentials independently from the Runner's issue API credential. Bridge
stderr is diagnostic only and must never contain secrets.

## Envelope

Every stdin request uses protocol `issue-spec.code-provider/v1`:

```json
{
  "protocol": "issue-spec.code-provider/v1",
  "request_id": "c6234dde-0dc0-4f03-a727-c26bdbeda9b6",
  "action": "capabilities",
  "payload": null
}
```

The bridge copies `protocol` and `request_id` exactly into the response. A
failure uses a stable bridge-owned code and a safe message:

```json
{
  "protocol": "issue-spec.code-provider/v1",
  "request_id": "c6234dde-0dc0-4f03-a727-c26bdbeda9b6",
  "error": {"code": "upstream_unavailable", "message": "code host unavailable"}
}
```

### Capabilities

Core always discovers capabilities before a mutation. Supported v1 values are
`evidence.snapshot`, `change.create`, and `change.comment`. Missing capability
fails before any workflow comment, evidence, or external mutation is written.

```json
{
  "protocol": "issue-spec.code-provider/v1",
  "request_id": "c6234dde-0dc0-4f03-a727-c26bdbeda9b6",
  "capabilities": {
    "protocol_version": "issue-spec.code-provider/v1",
    "values": ["evidence.snapshot", "change.comment"]
  }
}
```

### Evidence snapshot

A snapshot request contains a provider-namespaced repository/change reference
and the exact expected revision. The response repeats both and returns
immutable normalized records. Each record has its own state, revision,
observation time, validity window, payload digest, authenticated-writer identity,
trust decision, and optional supersession link. Kinds are `change`, `review`,
`check`, `merge`, and `archive`.

For every evidence snapshot request, the bridge MUST read the change's current
HEAD before collecting facts and MUST read it again after fact collection. It
may return a successful snapshot only when both observations equal the
requested revision. If the request names a historical revision, or HEAD moves
while facts are collected, the bridge returns an error with the stable code
`revision_mismatch` and no snapshot. Core therefore has no provider facts to
persist from a failed current-HEAD assertion.

Core rejects protocol/reference/revision mismatches, missing or malformed
records, untrusted writers, expired/stale observations, open P0/P1 findings,
pending/failed required checks, and non-merged merge/archive evidence. The
evidence identifiers and exact revision used are retained in the gate result.
URLs remain navigation metadata; they are never proof by themselves.
Persisted source-binding web URLs and external-reference canonical URLs are
canonical HTTPS coordinates only: userinfo, query strings (including a bare
`?`), fragments, control characters, default-port aliases, and dot-segment or
otherwise non-canonical forms are rejected. Credentials belong in the
operator bridge or delegated credential channel, never in a persisted URL.
External-reference metadata follows the reference visibility: metadata on a
`repository` reference is repository-readable (and public for a public
repository), while a `maintainers` reference is hidden in full from other
callers. Treat metadata as non-secret workflow coordinates, never as a place
for tokens, cookies, authorization headers, or provider credentials.

Persisted evidence follows the same row-level visibility rule. A `repository`
evidence row exposes its normalized payload and provenance to repository
readers, while a `maintainers` row is omitted entirely for non-maintainers.
Wrappers must therefore keep credentials, request headers, cookies, and raw
provider responses out of repository-visible evidence.

Every `review` record additionally carries canonical `finding_id`,
`process_id`, and `spec_id` fields (for example `FINDING-030`, `PROCESS-020`,
and `SPEC-010`). Core validates their type-specific shapes together with the
neutral severity and state, and writes the consumed linkage into the canonical
REVIEW artifact. The external platform continues to own the line-level body;
the neutral record preserves the workflow identity and optional HTTPS
`canonical_url` only.

### Mutation

Mutation actions are `create_change` and `comment`. Requests and responses use
the same neutral `provider_key`, `external_repository`, and `change_id`
reference. A `comment` response must repeat the entire request reference;
`create_change` alone may introduce a new `change_id`. Capability discovery
runs first. Every comment request has a non-empty `head_revision`. Before
writing a rationale comment, the bridge MUST require that the change's current
HEAD equals that exact revision and return a stable `revision_mismatch` failure
without writing when it does not. A returned canonical URL and external ID are
navigation/traceability values; they are not trusted workflow evidence.

Code-author rationale uses the existing `comment` mutation with
`metadata.kind=rationale`. Its metadata contains the stable `rationale_id`,
canonical `process`, `spec`, `reference_version`, `subject_revision`, and
logical `agent`. The body is a stable human-readable projection and does not
contain Issue comment identity, publication state, external receipts,
credentials, or runtime session identity.

For rationale, exact replay of the same `rationale_id`, body, entire reference,
and `head_revision` MUST return the original external ID and canonical URL
without creating another comment. Reuse of a `rationale_id` with a conflicting
body, reference, or head MUST fail. This is an idempotency rule of the existing
`change.comment` capability, not a new protocol field, capability, mutation
kind, or evidence kind. Core durably records a pending Issue carrier before the
first call and may replay after a lost provider or Issue acknowledgement.
Bridges that advertise `change.comment` must implement this contract; providers
without that capability use issue-spec's explicit issue-only rationale fallback.

## Versioning and compatibility

Unknown protocol versions, capabilities, fields, evidence kinds, and mutation
kinds fail closed. Additive protocol work requires a new advertised version;
operators may register multiple provider keys during migration. GitHub mode may
implement the same interfaces in process and retains its existing PR/check
behavior, while self-hosted issue profiles never infer a code host from the
issue origin.
