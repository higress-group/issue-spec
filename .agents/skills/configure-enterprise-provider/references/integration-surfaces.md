# Integration surfaces

Choose one authority for each surface before writing a wrapper.

| Surface | Authority | Stable extension point | Credential location |
|---|---|---|---|
| Issue workflow and typed artifacts | issue-spec Server or GitHub issue backend | Native API/profile | Profile token or service-account secret store |
| Code changes, reviews, checks, merge | Company code platform | `issue-spec.code-provider/v1` command bridge | Operator bridge environment/token file |
| Repository coordinates | issue-spec Source Binding | Self-hosted repository API and `issue-spec init` | No credential permitted |
| Runner clone and push | Company Git service | `issue-spec-git-credential-v1` or opt-in host SSH | Job lease or dedicated runner OS account |
| Browser identity | Company identity provider | OIDC | Protected auth provider file |
| Linked work-item visibility and status | issue-spec Server | Jira-like platform via Agent/Workflow CLI/API adapter or projection sidecar | Local wrapper or sidecar secret store |

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

Keep issue-spec Server authoritative for the issue workflow. Use either a
preferred Agent/Workflow CLI/API adapter or a centralized webhook/API
projection sidecar. Both designs use a stable association, idempotent writes,
and reconciliation; neither mirrors arbitrary comments bidirectionally.

A work-tracker adapter is separate from `issue-spec.code-provider/v1`. Do not
put it in the code-provider registry. Read
[work-tracker-adapter.md](work-tracker-adapter.md) for the implementation
sequence and failure handling.

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
