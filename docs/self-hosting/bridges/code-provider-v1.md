# Code provider bridge protocol v1

Self-hosted issue-spec keeps issue data and code-host facts behind separate
trust boundaries. The issue server is authoritative for issues and typed
workflow artifacts. An operator-installed bridge supplies one normalized,
exact-subject merge-authority generation and performs the one conditional
merge that atomically validates that generation. Provider snapshots and
authority tokens stay ephemeral. A bridge cannot replace policy-complete
facts with an `approved` or `mergeable` boolean.

For platform assessment, wrapper scaffolding, operator registration, Source
Binding, and work-tracker boundaries, see
[Integrate company code and work platforms](../enterprise-provider-integration.md).

## Trust and registration

The operator registers a provider key, immutable provider build identity, and
implementation when starting the CLI/server process. Repository configuration
may select only that key and stable check/review inputs. It cannot define the
executable or credentials:

```yaml
external_code:
  provider_key: code.example
  merge:
    required_checks:
      - source: provider
        provider: code.example
        key: app:42/context:unit
        owner: app:42
        display_name: unit
```

Repository configuration containing an executable, arguments, environment, or
credential source is rejected. Display names are diagnostic only. A check is
identified by its opaque provider key and owner/integration plus its mandatory
provider configuration generation.

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
      "max_output_bytes": 1048576,
      "principal_mapping_identity": "employee-directory@sha256:0123456789abcdef",
      "principal_mappings": [
        {
          "provider": "code.example",
          "stable_id": "provider-user-42",
          "principal": {"realm": "employees", "stable_id": "person-42"}
        }
      ]
    }
  }
}
```

This process-owned file is never discovered from the repository, and workflow
configuration cannot replace any registered executable or credential input.
`principal_mappings` is the operator-owned mapping from provider source actors
to the canonical principals used for reviewer independence. Its immutable
`principal_mapping_identity` identifies the exact mapping release. A non-empty
mapping requires that identity, and the identity without mappings is invalid.
Repository content, bridge responses, and CLI flags cannot add or override a
mapping.

## Process boundary

An operator may register an in-process implementation of
`codereview.MergeAuthorityProvider`, or construct
`codereview.CommandProvider` from trusted process configuration. Legacy audit
readers may continue to implement `codereview.Provider`; that interface is not
merge authority. The command implementation:

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

The protocol string remains v1. Merge-authority bridges must advertise the
closed semantic generation `minimal-merge-authority/v1`, an immutable provider
build identity, and all three merge-authority capabilities:

- `evidence.review-decision`
- `evidence.authoritative-check-conclusion`
- `change.merge-conditional`

Missing or partial capabilities, another semantic generation, an absent build
identity, duplicate/unknown values, or a build that changes after discovery
fails before usable authority or mutation. The legacy `evidence.snapshot`,
`change.create`, and `change.comment` values remain decodable only for their
non-authoritative compatibility surfaces.

```json
{
  "protocol": "issue-spec.code-provider/v1",
  "request_id": "c6234dde-0dc0-4f03-a727-c26bdbeda9b6",
  "capabilities": {
    "protocol_version": "issue-spec.code-provider/v1",
    "semantic_generation": "minimal-merge-authority/v1",
    "provider_build_identity": "code-example@sha256:0123456789abcdef",
    "values": [
      "evidence.review-decision",
      "evidence.authoritative-check-conclusion",
      "change.merge-conditional"
    ]
  }
}
```

### Merge-authority snapshot

The `merge_snapshot` action takes a complete provider reference, an exact
expected subject revision, and configured provider-native check identities:

```json
{
  "reference": {
    "provider_key": "code.example",
    "external_repository": "acme/widgets",
    "change_id": "42"
  },
  "expected_subject_revision": "abc123",
  "required_checks": [
    {
      "provider": "code.example",
      "key": "app:42/context:unit",
      "owner": "app:42",
      "display_name": "unit"
    }
  ]
}
```

The response field is `merge_snapshot`. It repeats protocol generation, build,
reference, and exact subject; includes current change state and capture time;
returns a closed exact-subject author set, effective review policy, current
per-reviewer decisions, findings and unresolved conversations; returns exactly
one provider-selected current conclusion for every requested check; and carries
an opaque `authority_token`.

Actors preserve source audit identity as `(provider, stable_id)`. Independence
uses only their trusted `canonical_principal` `(realm, stable_id)`. Every opener,
commit author, coauthor, and committer belongs to the closed author set,
including services. Login, email, display, logical agent, comment writer, and
bridge process identity are never heuristic substitutes. Missing, ambiguous,
or conflicting mappings fail closed.

Review mode is always `provider_native`; any other value is rejected. The
policy contains approval count and explicit CODEOWNER, stale-dismissal, and
conversation-resolution rules. Verdicts are `approved`, `changes_requested`,
`dismissed`, or `stale`. Findings have P0/P1/P2 severity and open/resolved/
dismissed state; resolution or dismissal must be owned by the finding reviewer.
Provider mode carries only current decisions, never a supersession history.

Check conclusions are `pending`, `success`, `failure`, `cancelled`, or
`skipped`; only `success` passes. Each conclusion repeats the exact subject,
opaque check key, owner, provider-selected current attempt, and configuration
generation. Duplicate, missing, unrequested, owner-mismatched, or wrong-subject
conclusions invalidate the whole snapshot. Core never selects a run by time or
list order.

The bridge must observe one coherent provider generation. Head movement or an
inability to bind the effective policy, review, findings, conversations, and
all required conclusions returns an error and no usable token. The snapshot
and token are never persisted as REVIEW, VERIFY, a receipt, or a merge plan.

### Conditional merge

The `merge_change` action is distinct from legacy `mutate`. Its request repeats
the exact reference, caller-required `expected_head`, and fresh opaque
`authority_token`. Its response field is `merge` and returns the
same reference/head plus provider merge ID, merged revision, and canonical URL.

At the write boundary the provider must atomically validate expected head and
every fact covered by the token: effective policy, decisions, findings,
conversations, and required conclusions. Expected-head-only or read-then-merge
implementations must not advertise `change.merge-conditional`.

### Validator conformance probe

A complete operator registration is not accepted from declarations alone. The
public validator dispatches both `merge_snapshot` and `merge_change` with an
optional marker reserved for runtime wire/action conformance:

```json
{
  "conformance_probe": {
    "schema_version": "issue-spec.code-provider-conformance/v1",
    "action": "merge_change",
    "nonce": "validator-generated-nonce",
    "mutation": "forbidden"
  }
}
```

The request's `external_repository`, `change_id`, revision, check key/owner,
and authority token are all the exact sentinel
`__issue_spec_conformance_probe__:<nonce>`. Those values are protocol-reserved,
not provider coordinates. A bridge MUST recognize the marker before coordinate
resolution, platform lookup, or any upstream request. It MUST NOT forward the
sentinel or translate the probe into a provider dry-run.

After the production action and its unhappy paths have been implemented and
tested, the local probe branch returns the normal action envelope
(`merge_snapshot` or `merge`) containing only:

```json
{
  "conformance_probe": {
    "schema_version": "issue-spec.code-provider-conformance/v1",
    "action": "merge_change",
    "nonce": "validator-generated-nonce",
    "mutation_performed": false
  }
}
```

Each action receives a separate nonce and at most five seconds. The validator
requires exact protocol, request, action, and nonce identity. It rejects every
error (including `unsupported_action` and `not_implemented`), malformed or
extra output, `mutation_performed=true`, and a normal snapshot or merge result.
The acknowledgement proves bounded runtime dispatch and local non-mutation; it
does not prove that the platform-native production merge is atomic. Activation
still requires operator-reviewed native API implementation and non-production
platform tests.

### Unsupported-provider boundary

There is no issue-native fallback, external check attestation, or external
authority generation. A provider that cannot supply every configured fact in
one policy-complete exact-head snapshot and atomically validate the returned
token is ineligible for merge authority. Legacy Aone capabilities and ordinary
GitHub REST read-then-write therefore remain explicit fail-closed cases.

### Legacy evidence snapshot (audit only)

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

Historical readers reject protocol/reference/revision mismatches and malformed
records. These records remain readable for audit but are never imported by the
merge-authority snapshot or conditional merge path.
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

Historical `review` records may carry canonical `finding_id`, `process_id`, and
`spec_id` fields (for example `FINDING-030`, `PROCESS-020`, and `SPEC-010`).
Audit readers validate their shapes but never project them into REVIEW,
PROCESS, readiness, or merge authority. The external platform continues to own
the line-level body; the neutral record preserves only audit identity and an
optional HTTPS `canonical_url`.

### Legacy mutation

Mutation actions are `create_change` and `comment`. Requests and responses use
the same neutral `provider_key`, `external_repository`, and `change_id`
reference. A `comment` response must repeat the entire request reference;
`create_change` alone may introduce a new `change_id`. Capability discovery
runs first. Every comment request has a non-empty `head_revision`; returned
canonical URLs and external IDs are navigation/traceability values, never
trusted workflow evidence. The removed code-author rationale workflow cannot
invoke this surface: `metadata.kind=rationale` is deprecated and rejected
before provider mutation.

A human-facing implementation explanation is a different surface. Before
requesting review, an operator tool may publish or refresh one ordinary
top-level provider discussion headed `### Implementation Rationale`, using a
provider-native UI/CLI or the general non-authoritative comment capability when
available. It carries no `metadata.kind=rationale`, machine marker, rationale
ID, typed carrier, PROCESS/SPEC binding, evidence field, or gate. Providers and
Core MUST NOT return it in the merge-authority snapshot or make its publication
status part of conditional merge. Failure remains a visible review-handoff
problem with a retryable/manual body, not an authority result.

## Versioning and compatibility

Unknown protocol versions, semantic generations, capabilities, fields,
actions, enums, evidence kinds, and mutation kinds fail closed. This is an
intentional closed semantic-generation cutover inside
`issue-spec.code-provider/v1`, not v2 and not a dual gate. New Core rejects old
bridges before authority; old Core rejects the new values/fields. CLI, Server,
Runner, generated assets, and bridge must be deployed as one pinned release
set. GitHub mode uses the same strict normalized types in process, and remains
fail-closed when its native merge primitive cannot atomically validate the
complete provider token.
