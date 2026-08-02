# Integrate company code and work platforms

Self-hosted issue-spec can keep change-spec workflow state on issue-spec Server
while source code, merge requests, reviews, and CI remain on a company-operated
code platform. A work tracker can link to or summarize the same change without
becoming an accidental second authority.

This guide covers provider selection, operator configuration, code-provider
wrapper implementation, Source Bindings, Runner Git credentials, OIDC, and
Jira-like work-item synchronization. All examples use fictional coordinates.

## 1. Separate the integration surfaces

Do not implement one privileged “company provider” that owns everything.

| Surface | Recommended authority | Integration |
|---|---|---|
| Issues, typed comments, Change views | issue-spec Server | Native API and self-hosted CLI profile |
| Source, PR/MR, review, CI, merge | Company code platform | `issue-spec.code-provider/v1` bridge |
| Clone and push credentials | Company Git service | `issue-spec-git-credential-v1` or trusted host SSH |
| Employee login | Company identity provider | OIDC |
| Linked work-item visibility and status | issue-spec Server | Jira-like tracker via Agent/Workflow adapter or API/webhook projection sidecar |

```text
browser ------ OIDC ------> issue-spec Server <------ runner webhook
                                  ^                         |
                                  | native API              | pinned Source Binding
work-tracker adapter or sidecar --+                         v
                                                    Git credential bridge
issue-spec Server / CLI ---- code-provider bridge ----> company code platform
```

The stable operator command protocol covers **code providers**. A work-tracker
adapter is a separate CLI/API integration: it is not
`issue-spec.code-provider/v1` and must not be placed in the code-provider
registry. See [Work trackers](#6-connect-a-jira-like-work-tracker) before
designing that integration.

## 2. Inventory the company platform

Record these decisions before writing code:

- stable repository identity and canonical credential-free clone/web URLs;
- PR/MR identity scope: global ID or repository-local IID;
- immutable head and merge revision fields;
- review, discussion, pipeline, job, and merge APIs;
- webhook signature, delivery ID, retry, and ordering behavior;
- service-account scopes and whether short-lived Git credentials can be minted;
- API rate limits, pagination, eventual consistency, and idempotency support;
- which system owns issue text, typed artifacts, code evidence, and work-item
  status.

Assess the complete merge-authority contract before enabling onboarding. A
provider remains pinned and ineligible until it can return policy-complete
provider-native review/check authority for one exact head and perform a native
conditional merge that atomically validates that authority. Legacy read-only
audit or MR navigation can remain useful, but must not be advertised as merge
capability.

## 3. Implement a code-provider bridge

The bridge is an operator-owned executable. issue-spec invokes it directly,
sends one strict JSON request on stdin, and accepts one strict JSON response on
stdout. It receives only explicitly configured arguments and environment.

The complete wire format is defined by the
[`issue-spec.code-provider/v1` contract](bridges/code-provider-v1.md).

### Scaffold the wrapper

The repository includes a skill and provider-neutral Python scaffold:

```bash
python3 .agents/skills/configure-enterprise-provider/scripts/scaffold_provider.py \
  --provider-key code.example \
  --display-name "Example Code" \
  --remote-authority git.example.test \
  --provider-build-identity code-example@sha256:0123456789abcdef \
  --principal-mappings-file "$HOME/.config/issue-spec/principal-mappings.json" \
  --output "$HOME/.config/issue-spec/providers/code.example"
```

The private mapping file has this strict shape. Populate it from an
operator-owned identity directory, not repository or bridge output:

```json
{
  "principal_mapping_identity": "employee-directory@sha256:0123456789abcdef",
  "principal_mappings": [
    {
      "provider": "code.example",
      "stable_id": "provider-user-42",
      "principal": {"realm": "employees", "stable_id": "person-42"}
    }
  ]
}
```

The command creates:

- `provider_bridge.py`: strict protocol envelope and safe error handling;
- `providers.json`: private operator registration and public-safe provider
  description;
- `implementation-plan.json`: the requested target capabilities and activation
  checklist.

The scaffold deliberately returns `not_implemented` for `merge_snapshot` and
`merge_change`. Its runtime and `providers.json` therefore start with no active
capabilities. `implementation-plan.json` records the complete three-capability
target, semantic generation, immutable build, and mapping identity. Implement
and contract-test both actions first. Then activate all three capabilities and
the same generation/build in both `provider_bridge.py` and `providers.json` as
one immutable release; never activate a partial set.

### Map provider objects to neutral facts

| Neutral coordinate | Typical platform value |
|---|---|
| `provider_key` | Operator registration key |
| `external_repository` | Stable project ID or canonical namespace |
| `change_id` | PR/MR ID with documented repository scope |
| `expected_subject_revision` / `expected_head` | Exact head commit SHA |
| `canonical_url` | Credential-free canonical HTTPS browser URL |

External-reference metadata has the same visibility as its reference. Metadata
on a `repository` reference is repository-readable and becomes public when the
repository is public; a `maintainers` reference is hidden in full from other
callers. Store only non-secret workflow coordinates there. Keep tokens,
cookies, authorization headers, and provider credentials in the operator
bridge or delegated credential channel.

For `merge_snapshot`, fetch one coherent provider generation for the requested
head; never substitute latest HEAD. Return a closed opener/author/coauthor/
committer set, effective approval/CODEOWNER/stale/conversation policy, current
per-reviewer decisions, provider-owned findings and resolutions, unresolved
conversation IDs, and exactly one provider-selected conclusion for every
requested stable check key/owner/configuration. Return an opaque authority
token binding all those native facts.

Return only stable source actor identities. issue-spec replaces every canonical
principal from the operator-owned `principal_mappings`; login, email, display
name, logical Agent, comment writer, and bridge identity never substitute for
that mapping.

For `merge_change`, require the exact reference, caller-required head, and the
fresh token. The platform operation must atomically validate head and every
review/check/policy fact bound by the token while merging. A bridge-side lock,
double read, or expected-head-only API is not eligible. `change.create` and
`change.comment` may remain optional navigation mutations, but cannot replace
the three-capability merge contract.

## 4. Register and configure the provider

Keep the registry outside repositories, owned by the service operator, with
mode `0600` or stricter:

```json
{
  "version": 1,
  "providers": {
    "code.example": {
      "path": "/opt/issue-spec/providers/code-example/provider-bridge",
      "args": ["serve-stdio"],
      "environment": [
        "CODE_EXAMPLE_API_URL=https://git.example.test/api",
        "CODE_EXAMPLE_TOKEN_FILE=/run/secrets/code-example-token"
      ],
      "timeout": "30s",
      "max_output_bytes": 1048576,
      "principal_mapping_identity": "employee-directory@sha256:0123456789abcdef",
      "principal_mappings": [
        {
          "provider": "code.example",
          "stable_id": "provider-user-42",
          "principal": {"realm": "employees", "stable_id": "person-42"}
        }
      ],
      "description": {
        "provider_key": "code.example",
        "display_name": "Example Code",
        "remote_authorities": ["git.example.test"],
        "code_change_label": "Merge request",
        "semantic_generation": "minimal-merge-authority/v1",
        "provider_build_identity": "code-example@sha256:0123456789abcdef",
        "capabilities": [
          "evidence.review-decision",
          "evidence.authoritative-check-conclusion",
          "change.merge-conditional"
        ]
      }
    }
  }
}
```

Point the server and every CLI process that performs provider-backed work at
the same trusted registry:

```bash
export ISSUE_SPEC_CODE_PROVIDERS_FILE=/etc/issue-spec/code-providers.json
```

The self-hosted profile can instead carry an absolute
`operator_registry_file`. The environment variable takes precedence. Never
store executable paths, environment, or credential sources in repository
`issue-spec/config.yaml`.

An origin-bound `auth login` for the same self-hosted realm retains this
operator-owned setting. After any profile reconfiguration, still run `auth
status` and a provider-backed plan before relying on the registry. A different
realm must be configured independently; never copy a provider registry across
realms without an explicit operator review.

Restart the server and confirm `/api/v1/meta` advertises only the public-safe
provider description. It must not expose the executable, environment, token
file, or credential.

### Validate the capabilities handshake

```bash
python3 .agents/skills/configure-enterprise-provider/scripts/validate_provider.py \
  --registry /etc/issue-spec/code-providers.json \
  --provider-key code.example
```

The validator checks private file mode, strict registry shape, executable
location, bounded capabilities response, protocol identity, the complete
three-capability set, semantic generation, immutable build agreement, and the
operator mapping identity. Empty generated scaffolds validate as inert and are
not merge-capable. Legacy-only or partial declarations fail with focused
diagnostics. Exercise `merge_snapshot` and the platform's protected merge in a
non-production repository before activation; capability validation alone is
not proof of atomic platform behavior.

## 5. Bind an issue-spec repository to its source

A Source Binding contains coordinates, not credentials. Preview onboarding
before allowing remote or local writes:

```bash
issue-spec --profile team init \
  --repo acme/payments-spec \
  --server-org acme \
  --server-repo payments-spec \
  --bind-source \
  --provider code.example \
  --external-repo platform/payments \
  --source-clone-url https://git.example.test/platform/payments.git \
  --source-web-url https://git.example.test/platform/payments \
  --default-branch main \
  --tools codex,claude \
  --delivery skills \
  --plan
```

Review the resolved provider, remote authority, server repository, external
repository, clone URL, web URL, and default branch. Then repeat without
`--plan`, adding `--yes` only for an approved non-interactive mutation.

The clone and web URL authorities must exactly match an authority advertised by
the selected provider. Treat a code platform's display or redirect URL as
metadata, not as a Source Binding coordinate: derive the binding URLs from the
selected canonical Git remote or an operator-maintained canonical-host mapping.
Do not silently substitute an alias just because it resolves in a browser.

Generated repository workflow configuration may select `code.example` and
stable provider-native required check keys/owners, but cannot replace the
operator registration, principal mapping, generation, build, or executable.

### Attach an existing provider change

Create the PR/MR with the approved code-host workflow first. Then use the
self-hosted profile to validate and associate that existing provider change:

```bash
issue-spec --profile team code-change attach \
  --repo acme/payments-spec \
  --implement 3 \
  --change-id 42 \
  --revision abc123 \
  --json
```

The active Source Binding fixes the provider and external repository; callers
supply only the provider-owned change ID and exact revision. This operation
does not call `change.create` and does not ingest evidence. A refresh of the
same relationship must supply `--refresh --expected-version <version>`.

Link a PROCESS only after the Implement Issue has exactly one active
`code_change` reference:

```bash
issue-spec --profile team code-change link-process \
  --repo acme/payments-spec \
  --implement 3 \
  --process PROCESS-001 \
  --expected-version 5 \
  --json
```

If the server returns `ambiguous_active_references`, inspect the reference IDs
returned by the conflict and the current native reference list. Delete only the
unwanted active reference through the authenticated native references API or
server UI, then retry. Operators and agents must not choose a winner by order
or overwrite all references. Review, merge, and closure continue through the
selected provider bridge or trusted code-host skill, not GitHub PR endpoints.

## 6. Connect a Jira-like work tracker

Keep issue-spec Server authoritative for issue bodies, typed comments,
permissions, Change status, and Runner commands. The tracker is a linked view
of the same change, not a second issue authority.

A work-tracker adapter is not `issue-spec.code-provider/v1`; never place its
executable, configuration, or credentials in the code-provider registry. Keep
credentials in the approved local wrapper, workflow runner, or sidecar secret
store.

### Preferred: Agent/Workflow-driven CLI/API adapter

Use a small wrapper around the company's work-item CLI or API when an Agent or
workflow can synchronize near the matching issue-spec stage.

1. Discover projects, work-item types, writable fields, statuses, and valid
   transitions before writing. Do not hard-code mutable platform labels.
2. After a proposal exists, find or create one external work item from a stable
   association key and persist a unique mapping. Store reciprocal canonical
   HTTPS links.
3. Reuse that association through design and implementation; do not create a
   separate work item for every stage.
4. Advance tracker status only after the corresponding issue-spec stage has
   succeeded. Use the platform's idempotency key or a local operation ledger.
5. When synchronization fails, retain the successful issue-spec stage and save
   a retryable operation. Do not roll back issue-spec merely because the
   tracker update failed.
6. Reconcile mappings and expected status periodically to recover from failed
   retries, delayed events, and manual tracker edits.

The wrapper can provide local operations such as `discover`, `find-or-create`,
`link`, `transition`, and `reconcile`. These names describe its workflow
contract; they are not an issue-spec provider ABI.

### Centralized: webhook/API projection sidecar

Use a sidecar when one service must synchronize multiple repositories or when
event-driven projection is required.

1. Consume signed issue-spec webhooks, or poll the native API with a
   checkpoint.
2. Maintain an inbox/outbox ledger keyed by delivery and association IDs.
3. Apply conditional, idempotent tracker updates with an origin marker to
   prevent event loops.
4. Project stable summaries and reciprocal links only. Keep typed artifacts in
   issue-spec instead of copying them into free-form tracker comments.
5. Reconcile on a schedule because either platform can lose events or be
   updated manually.

## 7. Configure identity and Runner Git access

Use [OIDC](authentication/v1/oidc.md) for employee browser login. OIDC identity
does not grant organization or repository roles; configure issue-spec
authorization separately.

For Runner clone and push, prefer a short-lived credential command implementing
[`issue-spec-git-credential-v1`](bridges/git-credential-v1.md). It should mint a
lease for the exact pinned Source Binding and revoke it on job completion.

Trusted deployments may opt into host SSH with a dedicated runner OS account,
sandboxing enabled, and `.ssh` mounted read-only. Host SSH has broader authority
than a job-scoped credential and should not be the default.

## 8. Acceptance and operations

Validate in a non-production repository:

- server and CLI load the same provider description;
- Source Binding authority and canonical URLs are accepted;
- capability generation/build and the operator principal mapping agree in CLI
  and Server;
- each advertised action succeeds and every unadvertised action fails closed;
- `merge_snapshot` rejects another provider, repository, change, revision, or
  check key/owner and returns only one provider-selected current conclusion;
- unmapped or conflicting authors/reviewers fail closed;
- current changes-requested, stale/dismissed decisions, unresolved P0/P1
  findings, conversations, and pending/failed checks behave as expected;
- `merge_change` rejects head, policy, review, finding, conversation, check, or
  authority-token drift at the native merge boundary;
- retries cannot create duplicate changes or comments;
- 401, 403, 404, 429, timeout, cancellation, and 5xx responses are mapped to
  safe stable errors;
- stdout/stderr bounds, secret redaction, credential rotation, and rollback are
  exercised;
- work-tracker synchronization keeps issue-spec authoritative, has a stable
  association, and cannot create a synchronization loop;
- repeated create, link, and transition requests are idempotent, while a failed
  tracker update remains retryable without rolling back issue-spec.

### Legacy Aone compatibility boundary

The existing operator-owned Aone bridge is pinned to its prior immutable
release and legacy `evidence.snapshot`, `change.create`, and `change.comment`
surfaces. Those values remain audit/navigation compatibility only. They fail
current self-hosted init before local or remote mutation and must not be made to
look compatible by changing registry declarations.

Aone becomes eligible only after sanitized stable platform APIs and conformance
tests prove the full current contract: exact authors and per-reviewer decisions,
effective native policy and discussions/findings, stable provider-selected
check key/owner conclusions, operator principal mapping coverage, and an atomic
expected-head merge that validates the complete fresh authority token. Until
then, keep the whole Aone release set pinned; do not parse verbose CLI output or
advertise partial current capabilities.

Keep detailed company configuration and evidence in approved internal systems.
Public documentation, issues, and PRs should contain only provider-neutral
examples and sanitized pass/fail summaries.

## Agent-assisted configuration

Invoke the repository skill when adapting a new platform:

```text
Use $configure-enterprise-provider to assess our code and work-item APIs,
scaffold the bridge, create a private provider registry, and produce a
non-production validation plan.
```

The skill is located at
`.agents/skills/configure-enterprise-provider/SKILL.md`.
