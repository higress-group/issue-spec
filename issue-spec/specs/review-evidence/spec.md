# review-evidence

## Purpose

Define the long-lived behavior contract for this capability.

Proposal Issues:
- https://github.com/higress-group/issue-spec/issues/393

## Requirements

### Requirement: accepted REVIEW tests remain exact final evidence

The CLI MUST preserve every accepted role-owned REVIEW required-test result as canonical final evidence and MUST select it only when the receipt, active review assignment, stable selector, resolved revision, exact command, passing outcome, generation, subject, provenance, and assurance agree.

#### Scenario: accepted exact-current review test reaches the final index

- **WHEN** an independent reviewer submits a passing receipt that exactly covers the sealed review required tests at the immutable current subject
- **THEN** final evaluation selects both the independent REVIEW completion and one canonical test record for each assigned review test

#### Scenario: bound review selector preserves declarative and executed identity

- **WHEN** an accepted review test uses a supported subject-revision binding
- **THEN** the final test record preserves the assigned selector and binding, exact resolved revision, and deterministically expanded executed command

#### Scenario: active review assignment remains the authority across projection

- **WHEN** accepted REVIEW evidence is projected to the review PROCESS and multiple explicitly covered change-bearing PROCESS targets
- **THEN** every projected test record retains the issuing review PROCESS as AssignmentProcessID and selects only the active assignment generation

#### Scenario: missing failed stale or tampered review tests fail closed

- **WHEN** a required review test is absent, failed, skipped, stale, extra, or differs in selector, resolved revision, command, receipt, assignment, generation, subject, provenance, or assurance
- **THEN** the evidence is ineligible and final evaluation reports a blocking required-test diagnostic

#### Scenario: duplicate or conflicting active review test identity is rejected

- **WHEN** more than one accepted record claims the same active review assignment and stable test identity with conflicting receipt or execution identity
- **THEN** canonical indexing fails closed instead of choosing by timestamp or comment order

#### Scenario: historical review carriers do not fabricate test coverage

- **WHEN** a historical accepted-REVIEW carrier has valid completion identity but predates canonical persisted test records
- **THEN** the carrier remains readable for compatible REVIEW completion while providing no passing final required-test evidence

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/393#issuecomment-5152273309
