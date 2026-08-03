# provider-integrations-and-evidence

## Purpose

Define operator-controlled provider contracts, navigation references, ephemeral exact-head merge authority, and fail-closed current-revision assertions at the provider merge boundary.

Proposal Issues:
- https://github.com/higress-group/issue-spec/issues/160
- https://github.com/higress-group/issue-spec/issues/271

## Requirements

### Requirement: Core owns neutral integration contracts while vendor behavior remains external

Core MUST keep provider-specific behavior behind issue-spec.code-provider/v1, MUST validate only the capabilities needed for the requested operation, and MUST allow delivery preparation through change creation, navigation, and ordinary discussion without requiring provider-native review normalization, authoritative checks, authority tokens, conditional merge, or a coordinated merge-capable release set.

#### Scenario: operation-scoped capability validation preserves useful providers

- **WHEN** a bridge supports the requested change or comment operation but no automatic merge operations
- **THEN** Core performs the supported operation and does not downgrade the repository to planning-only

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/417#issuecomment-5165960908

### Requirement: navigation links and merge authority remain separate

Provider change references, mutable metadata, audit snapshots, and ordinary discussions MUST remain navigation and human-review context, MUST preserve authenticated provenance and tenant visibility where applicable, and MUST NOT be interpreted as a machine merge decision or workflow gate.

#### Scenario: provider context stays useful without merge authority

- **WHEN** the workflow stores or reads a change reference, audit snapshot, or ordinary discussion
- **THEN** humans can navigate and review it while no active command treats it as permission to merge

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/417#issuecomment-5165960908

### Requirement: implementation rationale remains an ordinary provider discussion for human review

After pushing the exact reviewable head, the coordinator MUST validate valuable writer-owned changed-line rationale, publish it unchanged through safe provider-native inline discussion when supported, and publish or refresh one ordinary top-level Implementation Rationale summary before human handoff; these discussions MUST have no typed carrier, evidence field, gate, merge authority, quota, or requirement for PROCESS or SPEC.

#### Scenario: rationale improves review without blocking merge

- **WHEN** a writer makes a non-obvious decision on a changed line
- **THEN** the human reviewer receives its why, tradeoff, and risk while publication state is never converted into merge readiness

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/417#issuecomment-5165960908

### Requirement: delivery ends at an exact-head human merge handoff

The workflow MUST prepare and push one exact reviewable code subject, create or update its provider-native PR or MR, publish ordinary human-facing implementation rationale and validation context, and then hand control to a human; it MUST NOT compute authoritative merge readiness, normalize provider review or checks for merge authority, execute merge, require a merge-capable provider, or make post-merge reconciliation part of delivery completion.

#### Scenario: provider without conditional merge completes delivery preparation

- **WHEN** a configured provider can create a change and ordinary review discussions but cannot expose normalized native review or an atomic conditional-merge API
- **THEN** initialization, Runner implementation dispatch, PR or MR creation, rationale publication, and human handoff remain available without a planning-only or merge-capability blocker

#### Scenario: handoff reports engineering context without certifying mergeability

- **WHEN** the exact pushed head has implementation results, tests, risks, and worker-authored rationale ready for review
- **THEN** the coordinator presents those facts and the change reference to the human without issuing an authority token, ready decision, approval, or merge command

#### Scenario: human merge uses only native provider policy

- **WHEN** the human reviews current CI, approvals, conversations, branch rules, and other provider-native policy and chooses to merge
- **THEN** the code provider performs the merge and any native issue closing without an issue-spec merge transaction or post-merge lifecycle gate

#### Scenario: historical workflow and authority data remains inert

- **WHEN** legacy REVIEW, VERIFY, evidence, finalization, or minimal-merge-authority data remains readable
- **THEN** explicit audit reads may display it while active generated guidance and commands neither consume it nor recreate a compatibility path

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/417#issuecomment-5165960908
