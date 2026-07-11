# Code provider bridge protocol v1

Self-hosted issue-spec keeps issue data and code-host facts behind separate
trust boundaries. The issue server is authoritative for issues and typed
workflow artifacts. An operator-installed bridge reports normalized,
revision-bound code evidence or performs an explicitly requested external
mutation. Core evaluates every gate itself; a bridge cannot return an
`approved` boolean.

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

### Mutation

Mutation actions are `create_change` and `comment`. Requests and responses use
the same neutral `provider_key`, `external_repository`, and `change_id`
reference. Capability discovery runs first. A returned canonical URL and
external ID are navigation/traceability values and must subsequently be backed
by trusted evidence before verify or archive can pass.

## Versioning and compatibility

Unknown protocol versions, capabilities, fields, evidence kinds, and mutation
kinds fail closed. Additive protocol work requires a new advertised version;
operators may register multiple provider keys during migration. GitHub mode may
implement the same interfaces in process and retains its existing PR/check
behavior, while self-hosted issue profiles never infer a code host from the
issue origin.
