# Recorded route identity

## Purpose

A historical request identifies the route that actually served it. Current defaults,
plan names, and surviving content cannot substitute for execution evidence.

## Requirements

### Requirement: Request summaries preserve admitted routing identity safely

Each ObservedRequestSummary MUST return routing-context state and, when proven,
plan ID/revision plus the safe display name frozen at admission. Execution facts,
a separate safe projection, or an exact run relation may establish that identity.
Conflicting evidence yields conflicted state with null references/name; absent or
unsupported historical evidence is explicitly unavailable. A proven route with no
historical name may expose the route while declaring the name unavailable. Current
Agent/default/session plan cannot fill a missing historical field.

The safe routing projection is retained separately from sensitive original facts
and receipt content. Deleting/expiring those carriers cannot remove safe admitted
identity from new records. Public Facts queries cannot expose the private projection
or raw receipts to reconstruct missing display data.

#### Scenario: Two plans serve requests in one session

- **WHEN** the same session contains requests served by different plan revisions
- **THEN** every row MUST retain its own recorded context, including the original
  safe name after a later rename.

#### Scenario: Content is removed or evidence disagrees

- **WHEN** sensitive receipt content is cleaned up, or receipt and run-link identity
  conflict
- **THEN** independent safe identity MUST remain available when consistent, while
  conflict remains explicit rather than resolved using today's default plan.

## Implementation references

- [Authorized relation and routing projection](../../../crates/observation/src/query_v2/relation.rs).
- [Observation query contract](../../../crates/observation/src/query_v2/README.md).
