---
name: configure-enterprise-provider
description: Configure and implement provider-neutral enterprise integrations for self-hosted issue-spec, including private GitLab-like code hosts, Jira-like work trackers, OIDC, Source Bindings, issue-spec.code-provider/v1 command bridges, and Runner Git credentials. Use when an agent needs to assess integration surfaces, scaffold an operator-owned code wrapper, create a provider registry, map platform APIs to normalized evidence, configure a private repository, or validate a company-specific provider without publishing internal details.
---

# Configure Enterprise Provider

Build a least-privilege integration while keeping issue workflow authority,
code-host evidence, identity, and Git credentials as separate boundaries.

## Safety boundary

- Keep internal domains, repository names, identities, paths, credentials,
  deployment details, logs, and screenshots out of public repositories.
- Use `example.test` placeholders in committed documentation and tests.
- Keep executable paths and credential inputs only in operator-owned private
  configuration. Never put them in repository `issue-spec/config.yaml`.
- Start platform API assessment read-only. Advertise no merge capability until
  exact repository/change/head authority and the native token-bound merge
  mutation have both passed conformance tests.

## Workflow

1. Read [integration-surfaces.md](references/integration-surfaces.md). Produce
   an authority table for issues, code changes, identity, Git credentials, and
   work-item synchronization.
2. Decide whether the request needs:
   - an OIDC login provider;
   - a Source Binding;
   - a `code-provider-v1` bridge for code evidence or MR/PR mutations;
   - a `git-credential-v1` command or trusted host SSH for Runner clone/push;
   - a work-tracker adapter for a Jira-like platform.
3. Keep issue-spec Server authoritative for issues, typed artifacts, Change
   status, and Runner commands. Read
   [work-tracker-adapter.md](references/work-tracker-adapter.md) before
   implementing a Jira-like integration. Prefer an Agent/Workflow-driven
   CLI/API wrapper; use a webhook/API projection sidecar when centralized
   synchronization is required. A work-tracker adapter is not
   `issue-spec.code-provider/v1` and must never be placed in the code-provider
   registry.
4. Scaffold a code bridge when needed:

   ```bash
   python3 .agents/skills/configure-enterprise-provider/scripts/scaffold_provider.py \
     --provider-key code.example \
     --display-name "Example Code" \
     --remote-authority git.example.test \
     --provider-build-identity code-example@sha256:0123456789abcdef \
     --principal-mappings-file "$HOME/.config/issue-spec/principal-mappings.json" \
     --output "$HOME/.config/issue-spec/providers/code.example"
   ```

5. Treat the generated bridge as inert. The complete provider-native authority
   capability set, semantic generation, immutable provider build, and mapping
   identity are targets recorded in `implementation-plan.json`; the generated
   runtime and registry advertise no capabilities. Read
   [wrapper-mapping.md](references/wrapper-mapping.md), replace each required
   `not_implemented` branch with platform API calls, and add contract tests.
   Implement the reserved conformance marker locally in both actions so it
   returns the exact non-mutating acknowledgement without an upstream request.
   Only after both production mappings, their unhappy paths, and the runtime
   probes pass may all three required capabilities,
   `minimal-merge-authority/v1`, and the same immutable build identity be
   activated in both `provider_bridge.py` and `providers.json`. Preserve exact
   provider, repository, change, revision, check, actor, and authority-token
   identity.
6. Store the generated `providers.json` as a private operator file. Point both
   the server and relevant CLI process at it with
   `ISSUE_SPEC_CODE_PROVIDERS_FILE`, or use the self-hosted profile's trusted
   `operator_registry_file`. Keep the same provider description on both sides.
   Maintain `principal_mappings` and `principal_mapping_identity` only in this
   operator registry; never accept canonical principals from a repository,
   bridge response, CLI flag, login, email, or display name.
7. Configure the Source Binding with canonical credential-free HTTPS clone and
   web URLs. Run `issue-spec init --plan` before any remote or repository write.
8. Validate locally before deployment:

   ```bash
   python3 .agents/skills/configure-enterprise-provider/scripts/validate_provider.py \
     --registry "$HOME/.config/issue-spec/providers/code.example/providers.json" \
     --provider-key code.example
   ```

   For a complete provider, validation dispatches bounded `merge_snapshot` and
   `merge_change` probes with reserved coordinates and `mutation=forbidden`.
   The bridge must intercept them locally and echo the exact action/nonce with
   `mutation_performed=false`. Errors, normal action success, malformed output,
   or identity drift fail validation. This proves only runtime wire/action
   conformance; it does not prove the provider's native merge is atomic.

9. Exercise each advertised capability against a non-production repository at
   an exact revision. Verify timeouts, output bounds, secret redaction,
   idempotency, stale evidence, reference mismatch, and upstream failure.
10. For a work-tracker adapter, discover projects, item types, writable fields,
    and transitions before writing. Create or reuse its work item only after
    the proposal exists; keep one stable association through design and
    implementation; advance tracker status only after the matching issue-spec
    stage succeeds. Make retries idempotent, reconcile drift, and never roll
    back successful issue-spec work because tracker synchronization failed.
11. Report the architecture decision, generated files, operator configuration,
    validation evidence, remaining limitations, and rollback steps. Sanitize
    any result before writing to a public issue or PR.

## Required checks

- Provider registry is strict JSON, absolute, private, and contains no secret
  values.
- Bridge emits exactly one strict response and echoes protocol/request identity.
- Runtime capabilities, semantic generation, and immutable provider build match
  the operator description exactly.
- Both merge actions pass the reserved local conformance probe within the
  validator bound. The probe performs no upstream request or mutation, and a
  normal snapshot or merge result is rejected rather than treated as proof.
- A merge-capable bridge advertises all of `evidence.review-decision`,
  `evidence.authoritative-check-conclusion`, and
  `change.merge-conditional`; partial or legacy-only declarations fail closed.
- `merge_snapshot` is bound to the requested head, returns a closed author set,
  effective native policy, current reviewer decisions/findings/conversations,
  one provider-selected conclusion per stable check key/owner, and an opaque
  authority token.
- Every provider actor is covered by the operator-owned principal mapping;
  bridge-supplied canonical principals are never authority.
- `merge_change` atomically validates expected head and every native fact bound
  by the token. A bridge-side lock, double read, or expected-head-only merge is
  ineligible.
- Mutations are idempotent and cannot target another repository or change.
- Source Binding contains coordinates only, never credentials.
- issue-spec remains authoritative for Jira-like synchronization; the adapter
  uses stable associations, idempotent retries, reconciliation, and no comment
  loops.

## Product references

- User guide: `docs/self-hosting/enterprise-provider-integration.md`
- Code bridge wire contract: `docs/self-hosting/bridges/code-provider-v1.md`
- Runner credential contract: `docs/self-hosting/bridges/git-credential-v1.md`
- Authentication: `docs/self-hosting/authentication/README.md`
- Work-tracker adapter: [work-tracker-adapter.md](references/work-tracker-adapter.md)

Legacy `evidence.snapshot`, `change.create`, and `change.comment` bridges remain
available only for their pinned audit/navigation compatibility surfaces. They
must not be selected by current self-hosted init or described as merge-capable.
