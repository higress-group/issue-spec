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
   an authority table for issues, code changes, identity, and Git credentials.
2. Decide whether the request needs:
   - an OIDC login provider;
   - a Source Binding;
   - a `code-provider-v1` bridge for code evidence or MR/PR mutations;
   - a `git-credential-v1` command or trusted host SSH for Runner clone/push;
   - an external work-item projection for a Jira-like platform.
3. Do not invent a Jira provider configuration. The current stable command
   bridge covers code providers, not arbitrary issue backends. When a Jira-like
   system must remain visible, keep issue-spec Server authoritative and design
   an API/webhook sidecar that links or summarizes work items. If the external
   system must be the issue authority, record that core work is required.
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

5. Read [wrapper-mapping.md](references/wrapper-mapping.md), then replace every
   `not_implemented` branch with platform API calls. Advertise only completed
   actions. Preserve exact provider, repository, change, and revision identity.
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
10. Report the architecture decision, generated files, operator configuration,
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
- Jira-like synchronization has one declared authority and no comment loops.

## Product references

- User guide: `docs/self-hosting/enterprise-provider-integration.md`
- Code bridge wire contract: `docs/self-hosting/bridges/code-provider-v1.md`
- Runner credential contract: `docs/self-hosting/bridges/git-credential-v1.md`
- Authentication: `docs/self-hosting/authentication/README.md`
