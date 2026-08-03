---
name: configure-enterprise-provider
description: Configure provider-neutral enterprise integrations for self-hosted issue-spec, including private code hosts, work trackers, OIDC, Source Bindings, issue-spec.code-provider/v1 operation bridges, and Runner Git credentials.
---

# Configure Enterprise Provider

Build a least-privilege integration that prepares an exact PR/MR for human
review. The code host and human own checks, approval, and merge; issue-spec does
not model or execute that authority.

## Safety boundary

- Keep internal domains, repository names, identities, paths, credentials,
  deployment details, logs, and screenshots out of public repositories.
- Use `example.test` placeholders in committed documentation and tests.
- Keep executable paths and credentials only in operator-owned private
  configuration, never repository `issue-spec/config.yaml`.
- Advertise only operations that have real handlers and non-production contract
  tests. Missing operations limit that action; they do not make the repository
  planning-only and do not block unrelated Runner work.

## Workflow

1. Read [integration-surfaces.md](references/integration-surfaces.md) and record
   one owner for issues, code changes, Git transport, and work-item projection.
2. Select only the integrations actually needed:
   - OIDC for login;
   - Source Binding for canonical repository coordinates;
   - `issue-spec.code-provider/v1` for PR/MR creation, ordinary discussion, or
     optional evidence snapshots;
   - `git-credential-v1` or trusted host SSH for Runner clone/push;
   - a separate adapter for a Jira-like work tracker.
3. Keep issue-spec Server authoritative for issues, typed planning artifacts,
   Change status, and Runner commands. A work-tracker adapter is never placed in
   the code-provider registry. Read
   [work-tracker-adapter.md](references/work-tracker-adapter.md) before adding it.
4. Scaffold only required provider operations:

   ```bash
   python3 .agents/skills/configure-enterprise-provider/scripts/scaffold_provider.py \
     --provider-key code.example \
     --display-name "Example Code" \
     --remote-authority git.example.test \
     --capability change.create \
     --capability change.comment \
     --output "$HOME/.config/issue-spec/providers/code.example"
   ```

5. Treat the scaffold as inert. Read
   [wrapper-mapping.md](references/wrapper-mapping.md), implement and
   contract-test each selected handler, then copy only implemented capabilities
   into both `provider_bridge.py` and `providers.json`.
6. Store `providers.json` as a private operator file. Point Server and relevant
   CLI processes to it with `ISSUE_SPEC_CODE_PROVIDERS_FILE`, or use the
   self-hosted profile's `operator_registry_file`. Keep the same description on
   both sides.
7. Configure Source Binding with credential-free canonical HTTPS clone and web
   URLs. Run `issue-spec init --plan` before any remote or repository write.
8. Validate registry safety and the exact runtime capability handshake:

   ```bash
   python3 .agents/skills/configure-enterprise-provider/scripts/validate_provider.py \
     --registry "$HOME/.config/issue-spec/providers/code.example/providers.json" \
     --provider-key code.example
   ```

   Validation deliberately performs no snapshot, comment, create, review, or
   merge action. Exercise every advertised operation separately against a
   non-production repository at an exact revision.
9. Verify timeouts, output bounds, secret redaction, idempotency, stale-head and
   reference mismatch rejection, canonical URLs, and upstream failure behavior.
10. Report architecture, files, operator configuration, validation, remaining
    limitations, rollback, and the exact PR/MR handoff boundary. Sanitize public
    output.

## Required checks

- Registry is strict JSON, absolute, private, and contains no secret values.
- Bridge emits one strict response and echoes protocol/request identity.
- Runtime and operator-described capabilities match exactly.
- `change.create` returns the created change reference and canonical URL for the
  pushed head; retries cannot create unrelated duplicate changes.
- `change.comment` targets the exact existing change and head and remains an
  ordinary non-blocking human review aid.
- Optional `evidence.snapshot` is exact-head audit/navigation data only; it is
  not delivery acceptance or merge authority.
- Source Binding contains coordinates only, never credentials.
- Runner Git credentials are isolated from provider API credentials.
- The workflow stops after reporting exact head, change link, tests, rationale,
  risks, and limitations. Approval and merge stay in the provider UI.

## Product references

- User guide: `docs/self-hosting/enterprise-provider-integration.md`
- Code bridge wire contract: `docs/self-hosting/bridges/code-provider-v1.md`
- Runner credential contract: `docs/self-hosting/bridges/git-credential-v1.md`
- Authentication: `docs/self-hosting/authentication/README.md`
- Work-tracker adapter: [work-tracker-adapter.md](references/work-tracker-adapter.md)
