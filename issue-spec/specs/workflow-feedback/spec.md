# Workflow Feedback

## Purpose

Make concrete CLI and workflow friction recoverable across tasks in a provider-neutral Issue discussion. Project guidance owns routing and publication authorization. The destination Issue repository owns evidence confirmation and priority policy.

## Requirements

### Requirement: count independently observed events by original identity

An ordinary Issue or comment may carry versioned occurrence records. Each record MUST include the source instance, source repository, stable original event ID, observation time, impact summary and evidence link. The Issue is the scope of counting. A retry, copied report or parallel report with the same normalized identity MUST contribute at most one confirmed occurrence. Only records authored by a provider user ID trusted by the published destination policy contribute to the confirmed count. A trusted reporter may confirm an earlier untrusted or legacy report by recording the same evidenced event. Writable prose or JSON claims of authorization convey no trust.

#### Scenario: retry of a release deploy

- **WHEN** two catalog merges in separate releases have confirmed records and the second deploy is retried for the same release event
- **THEN** preview and replay return two confirmed occurrences, including when reports of the same event are concurrent

#### Scenario: contributor supplies an unconfirmed occurrence

- **WHEN** an authenticated contributor outside the configured reporter set supplies a third independent event
- **THEN** the Issue records the evidence and the confirmed count remains two until an authorized reporter confirms that event

### Requirement: publish policy through the destination's label authority

The CLI MUST load the policy from the selected Issue repository. Publishing MUST require the backend's permission to manage repository label definitions. The definition of `issue-spec/feedback-policy` MUST bind a policy Issue number to the SHA-256 digest of the reviewed JSON body. A missing definition disables the feedback command. A changed body MUST be validated and republished before it can govern new requests. Policy sets a positive occurrence threshold, a target label and trusted provider user IDs. Suggestion mode MUST be the default. An explicit `auto` mode permits the separate issue label mutation. The same policy operations MUST work on GitHub and self-hosted independently of the code host.

#### Scenario: policy publication and preview

- **WHEN** a maintainer previews a policy Issue
- **THEN** the CLI reports the policy it would publish with no provider mutation

#### Scenario: ordinary contributor changes an Issue body

- **WHEN** a changed policy body is visible but its digest differs from the published reference
- **THEN** feedback writes stop with a diagnostic until an authorized repository label owner publishes a valid policy

### Requirement: save evidence before permission-checked promotion

An explicit record request MUST preview the selected Issue, event identity, count and priority decision without writing when dry-run is selected. A real record request MUST save its evidence before attempting priority mutation. Automatic escalation requires a qualifying confirmed count, an open, unlocked Issue without a configured or built-in hold, an existing target label, unchanged published policy and effective backend permission to add the target label. Higher-priority and unrelated labels MUST be preserved. On self-hosted, contribution governs ordinary comment creation and triage governs issue label assignment.

A denied or unsuccessful label operation MUST preserve the recorded evidence and return the Issue identity, observed count and pending reason. The next request for the same occurrence MUST reconcile the existing record before retrying the label. An ambiguous comment or label response MUST be resolved by reading provider state before any repeated mutation.

#### Scenario: third occurrence with separate comment and label permissions

- **WHEN** an accepted third occurrence is recorded under automatic policy by a contributor who cannot assign labels
- **THEN** the Issue contains the evidence and returns a count of three with promotion pending
- **AND WHEN** an authorized triager retries the same event
- **THEN** the priority label is added and the recorded event identity and count are preserved

#### Scenario: maintainer hold or closed decision

- **WHEN** a repository maintainer has held the Issue or the Issue is closed
- **THEN** automatic promotion is ineligible even when the confirmed threshold is met

### Requirement: project instructions select the optional feedback procedure

The generated workflow MUST offer a shared feedback reference that projects can select through `guidance.shared` with their own destination and authorization rules. The procedure MUST use existing open and closed discussion search and ordinary Issues and comments. The project can use the feedback procedure during relevant authorized work and report adoption and verification with the original Issue. Workflow update preview MUST retain its read-only behavior and project-owned instructions.

## Implementation references

- [Workflow feedback discussion](https://github.com/higress-group/issue-spec/issues/472#issuecomment-6915572763)
- [Release ordering evidence](https://github.com/higress-group/HiRoute/issues/495)
