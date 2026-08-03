# profile-onboarding-and-workflow-generation

## Purpose

Define profile-driven server onboarding, stable instance authority, repository registration, source binding and provider-capability-aware workflow generation.

Proposal Issues:
- https://github.com/higress-group/issue-spec/issues/160

## Requirements

### Requirement: Profile-driven server onboarding and provider-aware workflow initialization

The CLI MUST initialize a repository from its selected profile and code provider, MUST generate operation-scoped workflow guidance for issue planning, implementation, change creation, ordinary discussion, rationale, and human handoff, and MUST NOT require merge-authority capabilities, operator merge preflight, canonical-principal mapping, configured check identities, or merge-capable readiness.

#### Scenario: enterprise provider onboards for human merge

- **WHEN** an enterprise provider supports repository identity, change creation, and ordinary comments but the human merges in its native UI
- **THEN** init completes the usable workflow and reports unsupported optional operations only when they are explicitly requested

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/417#issuecomment-5165960908
