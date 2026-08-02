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

### Requirement: selected issue closure is idempotent post-merge bookkeeping

After freshly observing provider merge, the workflow MUST reconcile the exact selected issue set idempotently on every provider, MUST NOT make mutable closing links or split issue storage pre-merge authority, and MUST NOT introduce a distributed lock, receipt, or merge state machine.

#### Scenario: closure follows observed merge

- **WHEN** the provider reports the selected code change merged
- **THEN** the closure reconciler closes exactly the selected simple Issue or Proposal phase set idempotently and a retry repairs bookkeeping without changing merge authority

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/405#issuecomment-5155764772
