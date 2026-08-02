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

- `evidence.review-decision`: return current provider-native reviewer decisions,
  a closed exact-subject author set, effective approval/CODEOWNER/stale/
  conversation policy, and provider-owned findings.
- `evidence.authoritative-check-conclusion`: return exactly one
  provider-selected current conclusion for every requested stable check
  key/owner and configuration generation.
- `change.merge-conditional`: atomically validate expected head and every
  review/check/policy fact bound by the fresh opaque authority token while
  performing the native protected merge.

All three are required with semantic generation `minimal-merge-authority/v1`
and one immutable provider build identity. Do not advertise any of them until
the complete set and its unhappy paths are tested. Capability discovery must
not require a repository token when the wrapper can answer from static
configuration. `evidence.snapshot`, `change.create`, and `change.comment` are
legacy audit/navigation surfaces and never satisfy merge-authority preflight.

## Merge-authority snapshot

`merge_snapshot` receives the exact reference, expected subject revision, and
the complete requested check identity set. Observe one coherent native
generation; never substitute current HEAD for the requested head and never let
Core choose a CI retry by time or list order.

| Authority fact | Provider source | Important checks |
|---|---|---|
| Exact-subject authors | opener plus commit authors/coauthors/committers | closed set, stable source actor IDs, services included |
| Review policy and decisions | native approval policy and current reviews | full count/CODEOWNER/stale/conversation rules, current reviewer identity and verdict |
| Findings and conversations | native discussions/findings | stable owner, P0/P1/P2, reviewer-owned resolution, unresolved conversation IDs |
| Current checks | native required-check/ruleset APIs | exact key/owner/configuration, one provider-selected attempt, exact subject |
| Authority token | native policy generation or protected merge token | binds head, policy, decisions, findings, conversations, and all required checks |

Return source actors without trusted cross-domain identity. Core overwrites
their canonical principal using `principal_mappings` from the operator registry
and rejects unmapped, duplicate, or conflicting actors. Never infer identity
from login spelling, email, display text, logical Agent, or bridge process.

## Conditional merge

`merge_change` requires the same exact reference, caller-required
`expected_head`, and the opaque token returned by a fresh `merge_snapshot`.
Call only a platform primitive that atomically rejects head, policy, decision,
finding, conversation, or required-check drift. A local mutex, double read,
saved snapshot, expected-head-only API, or read-then-unprotected merge cannot
implement `change.merge-conditional`.

## Errors

Return stable lowercase codes such as `unauthorized`, `forbidden`,
`not_found`, `revision_mismatch`, `rate_limited`, `upstream_unavailable`, or
`not_implemented`. Keep messages safe and short. Never return upstream bodies,
headers, tokens, cookies, or filesystem paths.

## Contract test matrix

- capabilities: complete, empty/inert, partial, legacy-only, duplicate, unknown,
  generation/build mismatch, and description mismatch;
- envelope: wrong protocol/request ID, duplicate/unknown fields, trailing JSON;
- mapping: absent identity, duplicate source actor, conflicting principal,
  unmapped author/reviewer/finding owner, and bridge-supplied conflict;
- merge snapshot: wrong provider/repository/change/revision, incomplete author
  set/policy, duplicate reviewer, unresolved P0/P1, conversation, wrong token;
- checks: missing/duplicate key-owner, pending, failed, success, renamed,
  configuration drift, and provider-selected retry;
- merge: expected-head race, same-head policy/review/check drift, stale token,
  unknown native result, and exact response identity;
- operations: timeout, cancellation, output overflow, upstream 401/403/404/429/5xx;
- security: no inherited ambient token, no shell invocation, no secret on stderr.
