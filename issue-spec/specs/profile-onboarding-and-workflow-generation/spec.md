# profile-onboarding-and-workflow-generation

## Purpose

Define profile-driven server onboarding, stable instance authority, repository registration, source binding and provider-capability-aware workflow generation.

Proposal Issues:
- https://github.com/higress-group/issue-spec/issues/160

## Requirements

### Requirement: Profile-driven server onboarding and provider-aware workflow initialization

Initialization MUST discover the operator-registered v1 provider and mandatory minimal-workflow capabilities, MUST generate only the intent-checks-review-merge model and optional execution aids, MUST record pinned provider and release identity, and MUST fail before generated assets or remote mutation when required capability or release identity is absent, unknown, or mixed.

#### Scenario: init rejects an old bridge

- **WHEN** the selected provider omits evidence.review-decision or a configured check or merge capability
- **THEN** initialization reports the exact incompatibility and generates no legacy or dual-mode workflow

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/405#issuecomment-5155764767
