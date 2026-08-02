# pr-issue-closure

## Purpose

Define the long-lived behavior contract for how issue-spec makes an implementation
PR's issue-closure links tamper-proof. GitHub auto-close depends on `Closes #N`
keywords stored in a managed block inside the mutable PR body, where a later
full-body edit can silently erase or reduce the block. This capability owns three
coupled surfaces: the completeness contract for that managed closure block (it must
cover exactly the declared set of expected issues — one or more of proposal, design,
and implement — and be verified before merge, not discovered after); a CLI
verification check (`pr verify-closure`) that reuses the same closure-block routine
as the post-merge archive path so pre-merge and post-merge verdicts stay consistent,
and that is callable by both an agent and a CI gate; and workflow guidance that orders
`pr link-issues` as the final PR-body write and makes the clobber failure mode
explicit.

This durable spec is organized by stable capability surfaces. Future changes that
adjust closure-block completeness rules, the verification check, or the ordering
guidance should update the relevant requirement module below (matched by requirement
title, newest wins) instead of appending a one-to-one copy of new proposal
requirements.

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
