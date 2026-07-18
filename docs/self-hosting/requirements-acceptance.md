# Requirements onboarding acceptance map

This map links the requirements journey to focused, isolated evidence. It does
not introduce a second acceptance framework.

| Specification | Evidence |
| --- | --- |
| SPEC-001 release contract | `.github/workflows/release.yml` packages all release assets and runs the native installer twice on hosted Linux, macOS, and Windows before publish; the release screenshot documents manifest, SHA-256, attestation, and `version --json`. |
| SPEC-002 secure setup | `internal/commands/requirements_acceptance_test.go` executes setup and status against an isolated self-hosted API with a fake keyring/opener, proving preview-before-write and secret confinement. |
| SPEC-003 requirements PAT | `web/tests/e2e/requirements-onboarding.spec.ts` uses the real requirements-mode PAT page with synthetic API responses and verifies the name-only default flow plus one-time redacted secret. |
| SPEC-004 skill delivery | The Go acceptance installs both Codex and Claude targets, then decodes each managed manifest and verifies its schema, skill name, and canonical content ID. |
| SPEC-005 bounded workflow | The Go acceptance executes the supported CLI sequence for a simple Issue, standard Proposal, canonical SPEC and QUESTION, author title/body update, and ordinary discussion. Its server mutation log proves exact-plan confirmation, returned browser URLs, zero writes without confirmation or `contribute`, and an immediate zero-write design/engineering handoff. |
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
