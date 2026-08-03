# pr-issue-closure

## Purpose

Define the long-lived behavior contract for closing the exact selected Issue set
only after a fresh provider observation proves that the code change merged. Mutable
GitHub `Closes #N` blocks may remain useful navigation, but they are never pre-merge
authority and no closure-verification or archive gate is produced.

Future changes to selected-set reconciliation should update the requirement below
rather than restoring mutable closing-link or final-evidence authority.

Proposal Issues:
- https://github.com/higress-group/issue-spec/issues/155

Related history (audit trail, not part of the durable contract):
- Design: https://github.com/higress-group/issue-spec/issues/156
- Implement: https://github.com/higress-group/issue-spec/issues/157
- Implementation PR: https://github.com/higress-group/issue-spec/pull/158

## Requirements

### Requirement: Post-merge Issue closure is provider-native or human-owned

issue-spec MUST NOT poll or ingest a merge result in order to close Issues, reconcile a selected Issue set, create closure evidence, or advance a post-merge workflow lifecycle. Provider-native closing links MAY remain as human-readable delivery metadata, and users MAY close Issues explicitly through the normal Issue backend.

#### Scenario: merged change causes no issue-spec reconciliation write

- **WHEN** a human merges the handed-off PR or MR in the code provider
- **THEN** issue-spec performs no automatic close, archive, evidence, relationship, or board-lifecycle mutation

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/417#issuecomment-5165960908
