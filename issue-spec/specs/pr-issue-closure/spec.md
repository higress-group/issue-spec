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

### Requirement: Implementation PR carries a complete issue-closure block verified before merge

An implementation PR MUST carry a managed issue-closure block that covers every linked issue the change is expected to close (each of proposal, design, and implement that exists for that change), and the completeness of that block MUST be verified before the PR is merged rather than discovered afterward. Because GitHub auto-close relies on `Closes #N` keywords in the PR body, and the managed block lives in the mutable body where a later full-body edit can silently erase or reduce it, the workflow MUST NOT treat a single successful `pr link-issues` run as sufficient: the block MUST be re-checked at a pre-merge gate so a missing, tampered, or incomplete block fails loudly while it can still be fixed. The gate MUST verify exactly the declared set of expected issues, so it stays correct for changes that legitimately have fewer than three artifacts.

#### Scenario: complete block passes the pre-merge gate

- **WHEN** an implementation PR body contains the managed closure block with a `Closes #N` line for each of the linked proposal, design, and implement issues
- **THEN** the pre-merge closure verification SHALL pass

#### Scenario: missing or tampered block fails before merge

- **WHEN** the managed closure block is absent, its markers are broken, or its contents were replaced by a later full-body edit
- **THEN** the pre-merge closure verification SHALL fail and block the merge until the block is restored

#### Scenario: subset coverage fails before merge

- **WHEN** the block is present but only covers a subset of the expected issues (for example only the implement issue, omitting proposal and design)
- **THEN** the pre-merge closure verification SHALL fail and report the missing issues

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/155#issuecomment-4905760149

### Requirement: A CLI check verifies the PR closure block against the expected issue set

issue-spec MUST expose a CLI verification check that reads an implementation PR body and validates its managed issue-closure block against the declared set of expected issues (one or more of proposal/design/implement), exiting non-zero when the block is missing, incomplete, tampered, or does not exactly match the declared set, and exiting zero when it matches. The check MUST accept the same optional-flag (at least one required) semantics that `pr link-issues` uses to write the block, so a subset block written for a change lacking an artifact can still be verified. The check MUST reuse the existing closure-block verification logic (the same routine used by the post-merge archive path) so pre-merge and post-merge verdicts stay consistent, and it MUST be exercisable both by an agent during the workflow and by a repository CI gate.

#### Scenario: check passes on a complete block

- **WHEN** the check runs against a PR whose body holds the managed block covering all expected issues
- **THEN** it SHALL exit zero and report the block as complete

#### Scenario: check fails non-zero on a missing or incomplete block

- **WHEN** the check runs against a PR whose body is missing the block or covers only a subset of the expected issues
- **THEN** it SHALL exit non-zero and name the missing or unexpected closure links

#### Scenario: check is wired into a pre-merge gate

- **WHEN** the workflow reaches the point where an implementation PR is ready to merge
- **THEN** the closure check SHALL be part of the pre-merge verification surface so the gate runs without a separate manual step

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/155#issuecomment-4905760523

### Requirement: Workflow guidance makes link-issues the final PR-body write

The generated workflow guidance (skills and the runtime coordinator prompt) MUST state that `pr link-issues` is the last write to an implementation PR body, and that any subsequent body edit MUST preserve the managed closure block or re-run `pr link-issues` afterward. The guidance MUST make the failure mode explicit — that a later full-body edit silently erases the block and causes GitHub to close only the issues still named in the body — so agents order their PR-body writes to avoid clobbering the block.

#### Scenario: guidance orders link-issues last

- **WHEN** an agent follows the generated apply/workflow guidance to prepare an implementation PR
- **THEN** the guidance SHALL instruct that `pr link-issues` runs after all other PR-body writes are complete

#### Scenario: later body edit must preserve or restore the block

- **WHEN** the guidance describes editing the PR body after `pr link-issues` has run
- **THEN** it SHALL require preserving the managed block verbatim or re-running `pr link-issues` to restore it

#### Scenario: generated artifacts reflect the ordering rule

- **WHEN** the skills and coordinator prompt are regenerated for this change
- **THEN** the ordering-and-preservation rule SHALL appear in the committed generated artifacts, not only in source templates

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/155#issuecomment-4905760907
