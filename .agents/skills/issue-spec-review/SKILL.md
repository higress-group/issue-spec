---
name: issue-spec-review
description: Review an issue-spec implementation change, create line findings, reply after fixes, and sync REVIEW comments.
license: MIT
compatibility: Requires issue-spec CLI.
metadata:
  author: issue-spec
  version: "1.0"
  generatedBy: "issue-spec"
---

# Issue Spec Review

Coordinator: follow issue-spec-workflow to prepare the immutable review snapshot, dispatch a real independent reviewer, and route repairs to the invariant owner. On GitHub add --pr <number>; on a self-hosted profile omit --pr and add --revision <exact-head>. Sync authoritatively captures current rationale and emits one stable done REVIEW completion even with zero findings; submit and sync include the review PROCESS, every covered change-bearing PROCESS, and every covered active SPEC on the single REVIEW owner comment. Do not run peer mutation commands after publication. Compatibility warning: never "Run these commands after the final review sync": issue-spec link --repo higress-group/issue-spec --from REVIEW-<n> --from-issue <implement-issue> --to PROCESS-<n> or issue-spec link --repo higress-group/issue-spec --from REVIEW-<n> --from-issue <implement-issue> --to SPEC-<n>. Do not copy Coordinator lifecycle or provider policy into the review packet.

## Review Role Packet

1. Accept only the sealed review assignment for the exact subject revision, immutable snapshot/diff, code authors, owned invariant, affected scenarios, review scope, focused checks, result schema, and design_context.
2. Require design_context.read_mode=complete-issue-body and conflict_policy=design-authoritative-stop. Before inspecting code, read the complete Design with issue-spec read issue --repo higress-group/issue-spec --issue <design_context.source_url> without comments, timeline, history, or gates. Stop on conflict; do not collect or pass runtime-specific session IDs.
3. Review the invariant end to end at the exact detached revision. Own every finding and post-fix recheck; the author cannot review its own work. Do not expand into unrelated history, DAGs, links, policy, or provider routing.
4. Outside the snapshot, write only `{"verdict":"approve","findings":[]}` (or `changes-requested` with complete existing-model findings), then run `issue-spec role complete --assignment-file <sealed-packet.json> --decision-file <decision.json> --output <receipt.json> --agent <reviewer-name> --json` from the snapshot.
5. The command proves immutable Git identity, runs every sealed test, and atomically self-validates v1 evidence. Missing, failed, stale, or substituted evidence blocks output. Return only the bounded completion result and findings; Coordinator review submission/sync, links, and acceptance remain separate.

## Project Workflow

- Workflow Source: `builtin`
- Workflow Schema: `issue-spec`
- Workflow Config: `issue-spec/config.yaml`

Project workflow templates are declarative only. Active proposal, design, implement, SPEC, TASK, PROCESS, QUESTION, REVIEW, and VERIFY artifacts remain in the selected issue backend's issue-native storage; repository-mode durable specs are materialized and checked on the implementation branch.
