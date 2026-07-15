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
ingestion still requires a designated repository writer, `evidence:write`, an
exact repository cap, and live repository permission.

### Assign an evidence writer

The assignment belongs to a Server user, not to a PAT. Personal Runner accounts
and service accounts use the same flow. Resolve the Runner user with its own PAT,
then use a separate repository-operator credential to activate the assignment:

```bash
SERVER_URL=https://issues.example.test
ORG_ID=00000000-0000-4000-8000-000000000001
REPO_ID=00000000-0000-4000-8000-000000000002

read -rsp "Runner PAT: " RUNNER_PAT
printf '\n'
RUNNER_USER_ID="$(
  curl -fsS \
    -H "Authorization: Bearer ${RUNNER_PAT}" \
    "${SERVER_URL}/api/v1/context" | jq -er '.user.id'
)"

read -rsp "Repository operator PAT: " OPERATOR_PAT
printf '\n'
curl -fsS --request PUT \
  -H "Authorization: Bearer ${OPERATOR_PAT}" \
  -H 'Content-Type: application/json' \
  --data '{"active":true}' \
  "${SERVER_URL}/api/v1/orgs/${ORG_ID}/repos/${REPO_ID}/evidence/writers/${RUNNER_USER_ID}"

unset RUNNER_PAT OPERATOR_PAT
```

The operator credential must belong to an identity allowed to manage repository
integrations. A short-lived, exact-repository `admin:repo` PAT is sufficient
when that identity has the required repository authority. Do not add
`admin:repo` to the Runner PAT. To retire the writer, repeat the `PUT` with
`{"active":false}`. PAT rotation for the same user preserves the assignment;
changing Runner identity requires a new assignment and deactivating the old one.
Runner preflight reads only the authenticated identity's own active status and
cannot create or change an assignment.

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
credentials independently from runner delegated tokens. Bridge stderr is
diagnostic only and must never contain secrets.

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
observation time, validity window, payload digest, designated-writer identity,
trust decision, and optional supersession link. Kinds are `change`, `review`,
`check`, `merge`, and `archive`.

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
runs first. A returned canonical URL and
external ID are navigation/traceability values and must subsequently be backed
by trusted evidence before verify or archive can pass.

## Versioning and compatibility

Unknown protocol versions, capabilities, fields, evidence kinds, and mutation
kinds fail closed. Additive protocol work requires a new advertised version;
operators may register multiple provider keys during migration. GitHub mode may
implement the same interfaces in process and retains its existing PR/check
behavior, while self-hosted issue profiles never infer a code host from the
issue origin.
