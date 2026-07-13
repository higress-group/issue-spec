# Work-tracker adapter

Keep issue-spec Server authoritative for issue bodies, typed artifacts, Change
status, permissions, and Runner commands. A Jira-like platform is a linked
work-tracker view of the same change.

This adapter is not `issue-spec.code-provider/v1`; do not add it to the
code-provider registry. Keep its executable configuration and secrets local to
the approved wrapper, workflow runner, or sidecar deployment.

## Choose a pattern

### Agent/Workflow-driven CLI/API adapter (preferred)

Use a small wrapper around the company work-item CLI or API when a workflow or
Runner can perform the synchronization near the corresponding issue-spec stage.

1. Discover projects, work-item types, writable fields, transitions, and
   assignee or status values before the first write. Do not hard-code mutable
   platform labels.
2. After the proposal exists, find or create one external work item using a
   stable association key. Persist the mapping with a uniqueness constraint.
3. Store reciprocal canonical HTTPS links. Reuse the same mapping through
   design and implementation rather than creating stage-specific work items.
4. Advance tracker status only after the matching issue-spec stage succeeds.
   Use the platform's idempotency key or a local operation ledger.
5. If tracker synchronization fails, preserve the successful issue-spec stage
   and record a retryable operation. Never roll it back solely for tracker
   failure.
6. Reconcile mappings and expected status periodically to recover from failed
   retries, manual edits, or delayed platform events.

The wrapper may expose conceptual operations such as `discover`,
`find-or-create`, `link`, `transition`, and `reconcile`. These are a local
workflow contract, not an issue-spec provider ABI.

### Centralized webhook/API projection sidecar

Use a sidecar when one service must synchronize many repositories or when
webhook-driven status projection is required.

1. Consume signed issue-spec events or poll the native API with a checkpoint.
2. Maintain an inbox/outbox ledger keyed by delivery and association IDs.
3. Apply conditional, idempotent tracker updates with an origin marker to
   prevent event loops.
4. Project stable summaries and reciprocal links; keep typed artifacts in
   issue-spec rather than copying them into free-form tracker comments.
5. Reconcile on a schedule because either system can lose events or be changed
   manually.

## Minimum validation

- Verify discovery against a non-production tracker project.
- Repeat create, link, and transition requests to prove no duplicate work item
  or status update is created.
- Inject a tracker failure after an issue-spec stage succeeds and prove the
  retry is queued without rolling back issue-spec.
- Verify a manual tracker edit is detected and handled by the declared
  reconciliation policy.
- Confirm logs, public issues, and pull requests contain no internal URLs,
  identifiers, credentials, or payloads.
