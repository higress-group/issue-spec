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
- issue-spec CLI processes that run init, read-only merge checks, or protected
  provider merge against that provider.

A current merge-capable provider is one indivisible contract: semantic
generation `minimal-merge-authority/v1`, an immutable provider build identity,
`evidence.review-decision`, `evidence.authoritative-check-conclusion`,
`change.merge-conditional`, provider-native `merge_snapshot` and
`merge_change`, and an operator-owned canonical-principal mapping. Partial sets
are planning-only and fail init before mutation. Legacy `evidence.snapshot`,
`change.create`, and `change.comment` remain audit/navigation compatibility
surfaces and do not make a provider merge-capable.

The operator validator also requires both runtime actions to recognize the
reserved `issue-spec.code-provider-conformance/v1` marker. A bridge intercepts
that marker before provider-coordinate lookup, makes no upstream request, and
echoes an action- and nonce-bound `mutation_performed=false` acknowledgement.
This closes declaration-only activation; it is wire/action conformance, not
evidence that the platform's production merge primitive is atomic.

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
- semantic generation, immutable provider build, mapping identity, and mapping
  coverage owner;
- stable provider-native reviewer, author, finding, conversation, and check
  key/owner identities;
- the native primitive that atomically validates expected head and the complete
  authority token;
- credential issuer, scope, lifetime, storage, and revocation;
- retry, idempotency, reconciliation, and loop prevention;
- test environment and rollback path.
