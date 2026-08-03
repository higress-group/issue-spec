# Code provider wrapper mapping

Map only provider operations required by the repository. The command receives
one `issue-spec.code-provider/v1` JSON request on stdin and emits one strict JSON
response on stdout. It is executed directly without a shell.

## Capabilities

The `capabilities` action returns `protocol_version` and a duplicate-free
`values` array containing any implemented subset of:

- `change.create`
- `change.comment`
- `evidence.snapshot`

Runtime values and the private registry description must match exactly. Keep a
new scaffold empty until each selected handler and its unhappy paths are tested.

## Create change

The `mutate` action with `kind=create_change` receives provider key, external
repository, empty change ID, title, optional body/base revision, exact pushed
head revision, and bounded metadata. Return the new complete reference,
canonical HTTPS URL, and stable external ID. Reject repository or head mismatch
before mutation. Make retries idempotent by an operator-defined stable key.

## Comment

The `mutate` action with `kind=comment` receives a complete existing reference,
body, and exact head. Return the same reference, canonical URL, and comment
external ID. Use it only for ordinary human review context such as line
rationale or the top-level Implementation Rationale; it creates no evidence or
acceptance state.

Core does not define portable diff coordinates in the comment request. Treat
ordinary top-level publication as the base contract. Inline comments require an
operator-approved provider-native review tool or a documented provider-specific
metadata extension whose exact-head behavior is tested independently.

If safe line discussion is unavailable, put `path:symbol/line` and the original
writer rationale in the top-level discussion. A failure to publish stays
visible and retryable but does not authorize or reject merge.

## Snapshot

The `snapshot` action receives a complete reference and subject revision. Return
only facts for that exact revision with stable IDs, timestamps, canonical URLs,
and payload digests. This optional surface supports audit and navigation. Do not
turn provider review, check, or merge state into issue-spec delivery authority.

## Contract tests

Cover:

- strict request/response shape, duplicate fields, protocol and request identity;
- capability empty/subset/full, duplicate, unknown, and description mismatch;
- repository, change, and head mismatch;
- create idempotency and canonical URL safety;
- comment exact-reference preservation and non-blocking behavior;
- snapshot wrong-revision, duplicate fact, stale observation, and oversized output;
- timeout, malformed output, upstream rejection, and secret redaction.

Registry validation proves local file/process safety and the capability
handshake only. It never performs provider mutations and does not replace these
non-production operation tests.
