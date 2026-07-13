# Code wrapper mapping

Map provider APIs to the neutral contract; do not leak vendor response shapes
through the bridge.

## Coordinates

| issue-spec field | Provider source | Rule |
|---|---|---|
| `provider_key` | Operator registration | Lowercase stable key; never infer from request URLs |
| `external_repository` | Project ID or canonical namespace | Stable across display-name changes when possible |
| `change_id` | PR/MR immutable ID or repository-local IID | Preserve the provider's documented identity semantics |
| `subject_revision` | Head commit SHA | Fetch and compare exactly; never substitute latest head |
| `canonical_url` | Browser HTTPS URL | No credential, query, fragment, userinfo, or redirect alias |

## Capabilities

- `evidence.snapshot`: read change, review, check, merge, and archive facts for
  one exact head revision.
- `change.create`: create a PR/MR from the requested base and head revisions.
- `change.comment`: add a comment to the exact existing change.

Do not advertise a capability until its unhappy paths are tested. Capability
discovery must not require a repository token when the wrapper can answer from
static configuration.

## Evidence facts

Use stable fact IDs derived from immutable provider event or object IDs. Use a
SHA-256 digest of a canonical normalized payload, not of presentation text.

| Kind | Typical provider objects | Important checks |
|---|---|---|
| `change` | PR/MR record | Head revision, base revision, open/closed state |
| `review` | Review or line discussion | Canonical FINDING/PROCESS/SPEC IDs, neutral severity and state |
| `check` | Pipeline/job/check run | Exact head SHA, stable name, pending/success/failure state |
| `merge` | Merge event/change record | Merged state and immutable merge revision |
| `archive` | Durable-spec PR/MR | Exact archive revision and merged state |

Never synthesize canonical review linkage from free-form reviewer prose. If the
provider cannot retain issue-spec linkage metadata, omit review facts and do
not recommend `review` evidence.

`observed_at` is the provider observation time. Set `valid_until` when the fact
is cached or otherwise time-bounded. Return a new fact that supersedes an old
one instead of silently changing the old fact's identity.

## Mutations

For `create_change`, require a title and head revision and reject an existing
`change_id`. Return the new change reference, external ID, and canonical URL.

For `comment`, require a complete reference and non-empty body. The response
must echo the same reference. Use a request-derived idempotency key when the
provider supports one; otherwise persist a local mutation ledger.

## Errors

Return stable lowercase codes such as `unauthorized`, `forbidden`,
`not_found`, `revision_mismatch`, `rate_limited`, `upstream_unavailable`, or
`not_implemented`. Keep messages safe and short. Never return upstream bodies,
headers, tokens, cookies, or filesystem paths.

## Contract test matrix

- capabilities: supported, empty, duplicate, unknown, and description mismatch;
- envelope: wrong protocol/request ID, duplicate/unknown fields, trailing JSON;
- snapshot: wrong provider/repository/change/revision, stale fact, duplicate ID;
- review: missing linkage, invalid severity/state, supersession;
- checks: pending, failed, success, renamed, and retry;
- mutation: unsupported capability, wrong reference, duplicate delivery;
- operations: timeout, cancellation, output overflow, upstream 401/403/404/429/5xx;
- security: no inherited ambient token, no shell invocation, no secret on stderr.
