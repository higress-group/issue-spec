# Integration surfaces

Choose one authority for each surface before writing a wrapper.

| Surface | Authority | Stable extension point | Credential location |
|---|---|---|---|
| Issue workflow and typed artifacts | issue-spec Server or GitHub issue backend | Native API/profile | Profile token or service-account secret store |
| Code changes, reviews, checks, merge | Company code platform | `issue-spec.code-provider/v1` command bridge | Operator bridge environment/token file |
| Repository coordinates | issue-spec Source Binding | Self-hosted repository API and `issue-spec init` | No credential permitted |
| Runner clone and push | Company Git service | `issue-spec-git-credential-v1` or opt-in host SSH | Job lease or dedicated runner OS account |
| Browser identity | Company identity provider | OIDC | Protected auth provider file |
| External work-item visibility | Jira-like platform | API/webhook projection sidecar | Sidecar secret store |

## Code platform

Use the command bridge when the company code host is not natively implemented.
Register the executable only in trusted process configuration. The repository
may select a provider key but cannot choose a binary, arguments, environment,
or credentials.

The same registry description should be available to:

- issue-spec Server, so `/api/v1/meta` advertises the provider and Source
  Binding validation recognizes its remote authority;
- issue-spec CLI processes that run init, review, verify, or archive against
  that provider.

Start with `evidence.snapshot`. Add `change.create` and `change.comment`
independently. A provider may be useful with only one capability.

## Jira-like work tracker

The current public command bridge is not a generic IssueBackend plugin ABI.
There is no `ISSUE_SPEC_ISSUE_PROVIDERS_FILE` or Jira wrapper configuration.

Prefer one of these designs:

1. **issue-spec authoritative, work item linked**: create or associate one
   external work item, store reciprocal HTTPS links, and project high-level
   state such as proposed, implementing, reviewing, verified, or archived.
2. **issue-spec authoritative, event summary**: consume issue-spec webhooks and
   post idempotent status summaries to the external platform. Keep typed
   comments only in issue-spec.
3. **external tracker authoritative**: implement and maintain an in-process Go
   IssueBackend adapter as product code. Treat this as core development, not
   operator configuration, until a stable issue-provider protocol exists.

For the first two designs, use a dedicated service account, a mapping table
with unique constraints, signed webhook verification, an outbox/inbox ledger,
and loop-prevention markers. Never mirror arbitrary comments bidirectionally.

## Identity

Use OIDC for employee browser login when available. Authentication establishes
identity only; organization membership, repository roles, service accounts,
and evidence-writer authority remain issue-spec authorization decisions.

## Git credentials

Prefer short-lived, job- and Source-Binding-scoped HTTPS credentials through
the Runner credential bridge. Use host SSH only for a trusted dedicated runner
account, with sandboxing enabled and `.ssh` mounted read-only.

## Decision output

Record:

- authority per surface;
- external stable identifiers and canonical URLs;
- advertised capabilities and intentionally unsupported operations;
- credential issuer, scope, lifetime, storage, and revocation;
- retry, idempotency, reconciliation, and loop prevention;
- test environment and rollback path.
