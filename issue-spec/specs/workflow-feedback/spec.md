# Workflow Feedback

## Purpose

Make concrete CLI and workflow friction recoverable across tasks in a provider-neutral Issue discussion. Project guidance owns routing and publication authorization. The destination Issue repository owns evidence confirmation and priority policy.

## Requirements

### Requirement: count independently observed events by original identity

An ordinary Issue or comment may carry versioned occurrence records. Each record MUST include the source instance, source repository, stable original event ID, observation time, impact summary and evidence link. The Issue is the scope of counting. A retry, copied report or parallel report with the same normalized identity MUST contribute at most one confirmed occurrence. The built-in policy counts records attributed to identifiable Issue authors. A published trusted reporter list restricts confirmation to those provider user IDs. A trusted reporter may confirm an earlier untrusted or legacy report by recording the same evidenced event. Writable prose or JSON claims of authorization convey no trust.

#### Scenario: retry of a release deploy

- **WHEN** two catalog merges in separate releases have confirmed records and the second deploy is retried for the same release event
- **THEN** preview and replay return two confirmed occurrences, including when reports of the same event are concurrent

#### Scenario: contributor supplies an unconfirmed occurrence

- **WHEN** an authenticated contributor outside the configured reporter set supplies a third independent event
- **THEN** the Issue records the evidence and the confirmed count remains two until an authorized reporter confirms that event

### Requirement: use built-in defaults and publish custom policy through the destination's label authority

The CLI MUST support preview, recording, deduplication and counting with the built-in policy when the selected Issue repository has no published policy. The built-in policy suggests `priority/high` at three confirmed occurrences. A custom policy may specify only a positive occurrence threshold and inherits the other defaults. Publishing MUST require permission to manage repository label definitions. The definition of `issue-spec/feedback-policy` MUST bind a policy Issue number to the SHA-256 digest of the reviewed JSON body. A changed body MUST be validated and republished before it can govern new requests. An explicit `auto` mode with trusted reporter IDs permits the priority mutation. Both backends MUST support the default and custom policy independently of the code host.

#### Scenario: newly initialized repository

- **WHEN** an authorized caller records three distinct evidenced occurrences after ordinary init without publishing a policy
- **THEN** the CLI reports three confirmed occurrences and suggests `priority/high`

#### Scenario: custom threshold only

- **WHEN** a maintainer publishes a policy containing only the threshold
- **THEN** the CLI uses the custom threshold with suggestion mode and the built-in priority label

#### Scenario: policy publication and preview

- **WHEN** a maintainer previews a policy Issue
- **THEN** the CLI reports the policy it would publish with no provider mutation

#### Scenario: ordinary contributor changes an Issue body

- **WHEN** a changed policy body is visible but its digest differs from the published reference
- **THEN** feedback writes stop with a diagnostic until an authorized repository label owner publishes a valid policy

### Requirement: save evidence before permission-checked promotion

An explicit record request MUST preview the selected Issue, event identity, count and priority decision without writing when dry-run is selected. A real record request MUST save its evidence before attempting priority mutation. Automatic escalation requires a qualifying confirmed count, an open, unlocked Issue without a configured or built-in hold, an existing target label and unchanged published policy. Higher-priority and unrelated labels MUST be preserved. GitHub MUST enforce its effective issue label permission. On self-hosted, the feedback promotion operation MUST enforce the same repository permission as creating an Issue. It MUST verify the published policy reference, re-evaluate the persisted evidence and Issue state in the mutation transaction, and apply only the policy's target label. Ordinary issue label assignment continues to require triage.

A denied or unsuccessful label operation MUST preserve the recorded evidence and return the Issue identity, observed count and pending reason. The next request for the same occurrence MUST reconcile the existing record before retrying the label. An ambiguous comment or label response MUST be resolved by reading provider state before any repeated mutation.

#### Scenario: GitHub third occurrence with separate comment and label permissions

- **WHEN** an accepted third occurrence is recorded under automatic policy by a contributor who cannot assign labels
- **THEN** the Issue contains the evidence and returns a count of three with promotion pending
- **AND WHEN** an authorized triager retries the same event
- **THEN** the priority label is added and the recorded event identity and count are preserved

#### Scenario: self-hosted Issue creator promotes a qualifying Issue

- **WHEN** a caller with permission to create issues requests promotion against a published automatic policy and the stored evidence meets its threshold
- **THEN** the self-hosted server adds the configured priority label
- **AND WHEN** that caller requests promotion below the threshold or against a stale policy
- **THEN** the stored Issue labels remain intact

#### Scenario: maintainer hold or closed decision

- **WHEN** a repository maintainer has held the Issue or the Issue is closed
- **THEN** automatic promotion is ineligible even when the confirmed threshold is met

### Requirement: project instructions select the optional feedback procedure

The generated workflow MUST include a feedback reference for authorized tasks after ordinary initialization. Projects MAY refine triggering conditions, destinations and authorization through `guidance.shared`. The procedure MUST use existing open and closed discussion search and ordinary Issues and comments. The project can use the feedback procedure during relevant authorized work and report adoption and verification with the original Issue. Workflow update preview MUST retain its read-only behavior and project-owned instructions.

## Implementation references

- [Workflow feedback discussion](https://github.com/higress-group/issue-spec/issues/472#issuecomment-6915572763)
- [Release ordering evidence](https://github.com/higress-group/HiRoute/issues/495)
