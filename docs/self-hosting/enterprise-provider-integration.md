# Enterprise provider integration

Use this pattern when issue-spec Server owns issue-native planning while source,
PRs/MRs, CI, review, approval, and merge remain on a company code platform.

The design goal is a clean handoff, not a second code-host policy engine:
issue-spec prepares one exact reviewable head and provider change; the human and
provider UI decide whether to merge it.

## Authority map

| Surface | Owner | Integration |
|---|---|---|
| Issues, typed planning, Change status, Runner commands | issue-spec Server | native API/OIDC |
| Repository coordinates | operator | Source Binding |
| Clone and push | Git transport | trusted SSH or `git-credential-v1` |
| PR/MR create and ordinary discussion | company code platform | optional `issue-spec.code-provider/v1` operations |
| CI, review, approval, merge | company code platform and human | provider UI/native policy |
| Work-item projection | company tracker | separate idempotent adapter |

No integration should duplicate another surface's authority. In particular,
provider review/check state is not copied into an issue-spec readiness gate.

## Assess the platform

Begin read-only. Confirm:

- stable repository and change identifiers;
- canonical credential-free clone and web URLs;
- exact head/base revision fields;
- create-change and discussion APIs actually needed by the workflow;
- token scope, timeout, rate limit, and output bounds;
- an idempotency strategy for PR/MR creation;
- a non-production repository for contract tests.

The provider bridge supports independent operation capabilities:

- `change.create`
- `change.comment`
- `evidence.snapshot` (optional audit/navigation)

Advertise any implemented subset. Missing `change.create` means the human or an
approved external tool creates the PR/MR; it does not disable planning or
implementation. Missing `change.comment` means rationale is returned as a body
for manual publication. Missing `evidence.snapshot` removes only provider audit
snapshot collection.

## Scaffold an operator bridge

```bash
python3 .agents/skills/configure-enterprise-provider/scripts/scaffold_provider.py \
  --provider-key code.example \
  --display-name "Example Code" \
  --remote-authority git.example.test \
  --capability change.create \
  --capability change.comment \
  --output "$HOME/.config/issue-spec/providers/code.example"
```

This creates:

- `provider_bridge.py`: strict inert command wrapper;
- `providers.json`: private operator registry with no active capabilities;
- `implementation-plan.json`: selected operation handlers and activation steps.

Implement and contract-test each selected handler, then move only implemented
values into both runtime `CAPABILITIES` and registry
`description.capabilities`. Do not add capabilities for review decisions,
policy normalization, approval, or merge.

Validate configuration and handshake:

```bash
python3 .agents/skills/configure-enterprise-provider/scripts/validate_provider.py \
  --registry "$HOME/.config/issue-spec/providers/code.example/providers.json" \
  --provider-key code.example
```

The validator intentionally performs no provider operation. Exercise create,
comment, and snapshot independently against a non-production repository.

## Register privately

Keep `providers.json`, executable paths, token file paths, and API environment
outside the repository. Use:

```bash
export ISSUE_SPEC_CODE_PROVIDERS_FILE="$HOME/.config/issue-spec/providers/code.example/providers.json"
```

or the self-hosted profile's trusted `operator_registry_file`. Server and CLI
processes that use provider operations need the same registry description.

Repository `issue-spec/config.yaml` names only the provider key:

```yaml
workflow:
  external_code:
    provider_key: code.example
```

## Source Binding and Git

Source Binding stores canonical repository coordinates and provider key, never
credentials. Its clone and web URL authorities must match an operator-advertised
remote authority.

Runner Git authentication is separate from provider API authentication. Use a
bounded `git-credential-v1` command or trusted read-only host SSH configuration.
Do not pass code-host API tokens through repository workflow content or Agent
prompts.

Run `issue-spec init --plan` before any local or remote write. Initialization
generates provider-aware Skills for the operations the provider advertises. It
does not classify a provider as planning-only or merge-capable.

## Human review context

The actual code writer returns zero or more rationale drafts for non-obvious
changed lines. After the exact head is integrated and pushed, the Coordinator
validates anchors and sensitive-data absence, publishes safe unchanged text as
non-blocking inline discussion, and maintains a top-level
`### Implementation Rationale` summary/index.

If inline comments are unsafe or unsupported, the top-level discussion keeps
`path:symbol/line` with the writer's text. Publication failures remain visible
and retryable. Rationale is review context, never evidence or approval.

## Work-tracker projection

A Jira-like tracker adapter is separate from the code-provider registry. Keep
issue-spec Server authoritative, use one stable association, make retries
idempotent, reconcile drift, prevent comment loops, and never roll back
successful issue-spec work because tracker synchronization failed.

## Acceptance checklist

- Registry is strict, absolute, private, and secret-free.
- Runtime capabilities match the operator description exactly.
- Every advertised operation has non-production happy/unhappy-path tests.
- PR/MR creation is bound to the intended repository and exact pushed head.
- Comment operations preserve exact change identity and are non-blocking.
- Optional snapshots reject wrong head and remain audit-only.
- Source Binding is credential-free; Runner Git and provider API credentials are
  separate.
- Workflow reports exact head, change link, tests, rationale, risks, and
  limitations, then stops before approval or merge.

## Rollback

Remove a capability from both runtime and registry to disable that operation.
Remove the provider key from repository workflow configuration to stop provider
dispatch. Revoke the provider API token separately from Runner Git credentials.
Existing provider PR/MR and comments remain on the code platform for human audit.
