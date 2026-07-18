# Requirements onboarding acceptance map

This map links the requirements journey to focused, isolated evidence. It does
not introduce a second acceptance framework.

| Specification | Evidence |
| --- | --- |
| SPEC-001 release contract | `.github/workflows/release.yml` packages all release assets and runs the native installer twice on hosted Linux, macOS, and Windows before publish; release tests prove curl-only downloads plus manifest, SHA-256, and `version --json` verification. |
| SPEC-002 origin-bound setup security | `internal/commands/requirements_test.go` proves credential-free realm discovery, realm-mismatch failure, keyring fail-closed behavior, terminal-input refusal, private context permissions, and secret confinement. |
| SPEC-003 global server setup | `internal/commands/requirements_acceptance_test.go` proves preview-before-write and one global profile/server context with no repository, agent, or skill-directory state; the Playwright PAT fixture verifies the name-only default flow and one-time redacted secret. |
| SPEC-004 skill delivery | Release packaging builds one canonical `issue-spec-requirements.zip`; content tests verify its schema, deterministic bytes, canonical content ID, compatibility manifest, and absence of server, repository, agent-path, or credential state. |
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
