# Requirements onboarding acceptance map

This map links the requirements journey to focused, isolated evidence. It does
not introduce a second acceptance framework.

| Specification | Evidence |
| --- | --- |
| SPEC-001 release contract | `.github/workflows/release.yml` packages all release assets and runs the native installer twice on hosted Linux, macOS, and Windows before publish; the release screenshot documents manifest, SHA-256, attestation, and `version --json`. |
| SPEC-002 secure setup | `internal/commands/requirements_acceptance_test.go` isolates HOME/config/keyring and proves preview-before-write, keyring-only token storage, status, and idempotence. |
| SPEC-003 requirements PAT | `web/tests/e2e/requirements-onboarding.spec.ts` uses the real requirements-mode PAT page with synthetic API responses and verifies the name-only default flow plus one-time redacted secret. |
| SPEC-004 skill delivery | The Go acceptance installs the canonical skill into isolated Codex and Claude targets and checks that compatibility and confirmation boundaries remain present. |
| SPEC-005 bounded workflow | The Go acceptance checks simple/standard drafting, explicit confirmation, returned browser URLs, and the read-only design handoff. |
| SPEC-006 bilingual journey | The two onboarding guides have identical step markers; Playwright creates English and Chinese screenshots from synthetic fixtures. `hack/requirements-acceptance/verify.sh` rejects missing, stale, unreferenced, or credential-like evidence. |
| SPEC-007 public contribution | `internal/server/api/github/issues/integration_test.go` proves authenticated outsiders can create ordinary and standard Proposal/SPEC/QUESTION discussions only under the existing public `contribute` capability; members/disabled remain denied without unrelated privilege expansion. |

Run the focused checks with:

```bash
make verify-requirements-acceptance
```

Regenerate documentation screenshots with:

```bash
make docs-self-hosted-screenshots
```
