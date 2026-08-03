# Code provider command protocol v1

`issue-spec.code-provider/v1` is a small operator-owned command bridge for a
private code host. It exists to prepare and explain an exact PR/MR for human
review. It does not model provider policy, approve a change, or merge it.

## Boundary

The operator registry owns the executable path, arguments, environment,
timeouts, output bounds, remote authorities, and public provider description.
Repository workflow configuration contains only the provider key.

The command is executed directly without a shell. It reads one JSON request
from stdin and writes one strict JSON response to stdout. Unknown fields,
duplicate JSON keys, unsupported protocol/action/capability, wrong request
identity, malformed output, timeout, or output overflow fail the requested
operation before issue-spec records success.

Example private registry entry:

```json
{
  "version": 1,
  "providers": {
    "code.example": {
      "path": "/opt/issue-spec/providers/code-example",
      "args": [],
      "environment": [
        "CODE_EXAMPLE_API_URL=https://git.example.test/api",
        "CODE_EXAMPLE_TOKEN_FILE=/run/secrets/code-example-token"
      ],
      "timeout": "30s",
      "max_output_bytes": 1048576,
      "description": {
        "display_name": "Example Code",
        "remote_authorities": ["git.example.test"],
        "code_change_label": "Merge request",
        "capabilities": ["change.create", "change.comment"],
        "recommended_evidence": []
      }
    }
  }
}
```

The registry is private operator configuration. Environment entries may carry
secret file paths, not secret values. Source Binding remains credential-free.

## Envelope

Every request contains:

```json
{
  "protocol": "issue-spec.code-provider/v1",
  "request_id": "opaque-request-id",
  "action": "capabilities",
  "payload": null
}
```

Every response echoes `protocol` and `request_id` and contains exactly one of
`capabilities`, `snapshot`, `mutation`, or `error`.

```json
{
  "protocol": "issue-spec.code-provider/v1",
  "request_id": "opaque-request-id",
  "error": {
    "code": "not_implemented",
    "message": "comment mapping is not implemented"
  }
}
```

Error messages must be bounded and secret-free.

## Capabilities

The `capabilities` action has no payload and returns any implemented subset:

- `change.create`
- `change.comment`
- `evidence.snapshot`

```json
{
  "protocol": "issue-spec.code-provider/v1",
  "request_id": "opaque-request-id",
  "capabilities": {
    "protocol_version": "issue-spec.code-provider/v1",
    "values": ["change.comment", "change.create"]
  }
}
```

Runtime values and the operator description must match exactly. A missing
capability rejects only the requested operation. It does not create a global
readiness mode and does not block planning, implementation, Runner dispatch, or
Git operations that do not require that capability.

## Reference

Every change operation is scoped by:

```json
{
  "provider_key": "code.example",
  "external_repository": "group/project",
  "change_id": "42"
}
```

Provider key, repository, change, and head are untrusted request coordinates
until the operator bridge validates them against its own configuration and the
platform response.

## Create or comment

Both use `action=mutate`.

Create request payload:

```json
{
  "kind": "create_change",
  "reference": {
    "provider_key": "code.example",
    "external_repository": "group/project",
    "change_id": ""
  },
  "title": "Explain exact-head handoff",
  "body": "Human-facing summary",
  "base_revision": "main",
  "head_revision": "0123456789abcdef",
  "metadata": {}
}
```

Comment request payload:

```json
{
  "kind": "comment",
  "reference": {
    "provider_key": "code.example",
    "external_repository": "group/project",
    "change_id": "42"
  },
  "body": "Implementation rationale",
  "head_revision": "0123456789abcdef"
}
```

A successful response returns:

```json
{
  "protocol": "issue-spec.code-provider/v1",
  "request_id": "opaque-request-id",
  "mutation": {
    "reference": {
      "provider_key": "code.example",
      "external_repository": "group/project",
      "change_id": "42"
    },
    "canonical_url": "https://git.example.test/group/project/changes/42",
    "external_id": "42"
  }
}
```

Only `create_change` may introduce a change ID. `comment` must echo the exact
input reference. Create retries must be idempotent; comment retries must not
target another change. Canonical URLs are HTTPS and contain no credentials.

Provider-native comments carry human review context such as writer-owned line
rationale and a top-level `### Implementation Rationale` index. They are not
typed evidence, readiness, or delivery acceptance. Core does not standardize
diff path/line/side coordinates in `MutationRequest`; `change.comment` guarantees
only an ordinary comment. Use an operator-approved provider-native review tool
or a documented bridge-specific metadata extension for inline publication. If
safe inline discussion is unsupported, preserve `path:symbol/line` and the
original rationale in the top-level discussion.

## Optional snapshot

`action=snapshot` is an audit/navigation compatibility surface:

```json
{
  "reference": {
    "provider_key": "code.example",
    "external_repository": "group/project",
    "change_id": "42"
  },
  "subject_revision": "0123456789abcdef"
}
```

The response repeats protocol version, reference, subject revision, capture
time, and bounded provider facts for that exact revision. Facts have stable IDs,
state, observation time, canonical URL, and payload digest. They are untrusted
until core validates their shape. Snapshot content never authorizes approval or
merge.

## Validation and handoff

`validate_provider.py` validates the private registry, executable, bounds, and
capability handshake without performing provider mutations. Separately test
every advertised operation in a non-production repository, including exact-head
binding, idempotency, stale coordinates, malformed upstream data, timeout,
output overflow, and secret redaction.

The workflow ends after an exact reviewable head is pushed, a PR/MR is created
or selected, rationale is published when available, and tests, risks,
limitations, exact head, and change link are reported. Current provider CI,
approval, policy, and merge remain in the provider UI under human control.
