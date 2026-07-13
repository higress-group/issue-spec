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
- Start with read-only evidence. Add mutation capabilities only after exact
  repository, change, and revision checks are tested.

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
     --capability evidence.snapshot \
     --recommended-evidence change \
     --recommended-evidence check \
     --output "$HOME/.config/issue-spec/providers/code.example"
   ```

5. Treat the generated bridge as inert. The requested capabilities and
   evidence are targets recorded in `implementation-plan.json`; the generated
   runtime and registry advertise none. Read
   [wrapper-mapping.md](references/wrapper-mapping.md), replace each required
   `not_implemented` branch with platform API calls, and add contract tests.
   Only then copy completed capabilities into both `provider_bridge.py` and
   `providers.json`. Preserve exact provider, repository, change, and revision
   identity.
6. Store the generated `providers.json` as a private operator file. Point both
   the server and relevant CLI process at it with
   `ISSUE_SPEC_CODE_PROVIDERS_FILE`, or use the self-hosted profile's trusted
   `operator_registry_file`. Keep the same provider description on both sides.
7. Configure the Source Binding with canonical credential-free HTTPS clone and
   web URLs. Run `issue-spec init --plan` before any remote or repository write.
8. Validate locally before deployment:

   ```bash
   python3 .agents/skills/configure-enterprise-provider/scripts/validate_provider.py \
     --registry "$HOME/.config/issue-spec/providers/code.example/providers.json" \
     --provider-key code.example
   ```

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
- Runtime capabilities match the operator description.
- Snapshot facts are bound to the requested head revision and use stable IDs.
- Review evidence uses real canonical FINDING/PROCESS/SPEC linkage; otherwise
  the bridge does not advertise review evidence.
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
