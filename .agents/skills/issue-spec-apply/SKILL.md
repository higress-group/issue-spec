---
name: issue-spec-apply
description: Implement directly or use an optional PROCESS when managed coordination is required.
license: MIT
compatibility: Requires issue-spec CLI.
metadata:
  author: issue-spec
  version: "1.0"
  generatedBy: "issue-spec"
---

# Issue Spec Apply

Coordinator: default to a direct single-writer implementation. A single child or subagent may own that implementation without PROCESS while the coordinator performs no concurrent code writes. Use optional Implement and TASK planning when engineering risk makes them useful. Select PROCESS only for concurrent code writers, protection of pre-existing work through isolation, enforced path ownership, restartable cross-session handoff, or dependency-ordered integration. Using a child, changing several files, requesting independent review, or needing merge evidence is not sufficient. When Implement is selected, persist it, perform its first QUESTION pass, then finalize the selected implementation plan. Author PROCESS only if managed coordination was selected. Issue bodies and typed planning artifacts remain authoritative planning state.

## Direct Single-Writer Path

For a bounded change without those managed-coordination needs, the coordinator MAY implement directly or dispatch exactly one code-writing child or subagent in the selected implementation checkout. Keep one writer active, give a delegated child a bounded goal and focused verification, and wait for it before making coordinator code changes. Use ordinary Git and provider checks; do not manufacture PROCESS, workspace lifecycle, role receipt, handoff, a typed rationale carrier, or evidence state. Read-only investigation and review children remain available without PROCESS.

After either the direct path or selected managed PROCESS work produces a reviewable exact change, the Coordinator MUST publish or refresh one ordinary top-level provider discussion headed `### Implementation Rationale` before human review; no Implement, TASK, PROCESS, or SPEC is required. Summarize intent, decisions/tradeoffs, boundaries/risks, validation/results, exact subject/head, and selected planning links. Use the provider-native discussion surface, never a rationale-evidence command, marker, ID, typed carrier, PROCESS/SPEC binding, evidence, or gate. On failure report the provider error, retain the body, and do not claim human-review handoff complete. The comment and its status never enter `merge-check` or merge authority.

For every agent-executed change-bearing PROCESS, seal the implementation assignment and dispatch a real non-Coordinator worker with the packet below. Preserve exact base, ownership, DCO, tests, generators, dependency order, managed worktree isolation, and bounded handoff. These controls are implementation safety only: they do not create review, verification, rationale evidence, receipt, coverage, or finalization authority, and merge-check never reads their lifecycle.

## Implementation Role Packet

This packet is addressed to the dispatched worker subagent. Relay it verbatim with the sealed assignment; do not execute it in the coordinator context.

1. Accept only the sealed implementation assignment for the exact PROCESS, base revision, worktree, write ownership, focused tests, generators, result schema, and design_context. Do not load proposal bodies, the complete DAG, link matrices, post-merge policy, provider routing, or unrelated artifacts.
2. Before code changes, require design_context.read_mode=complete-issue-body and conflict_policy=design-authoritative-stop. Read the complete Design with issue-spec read issue --repo higress-group/issue-spec --issue <design_context.source_url> without comments, timeline, history, or gates. Stop and report any conflict; do not reinterpret or summarize the packet.
3. Work only in the assigned worktree and owned paths. Preserve the named invariant, decisions, must_preserve, must_not, and minimum_verification exactly. Do not collect or pass runtime-specific session IDs.
4. Implement the invariant, run assigned generators, finish exactly one DCO commit when required, and leave the tree clean. If the work cannot remain one bounded end-to-end invariant, stop with stable-interface split options and acceptance consequences.
5. Outside the worktree, write only `{"decisions":[],"risks":[],"rationale_draft":"..."}`, then run `issue-spec role complete --assignment-file <sealed-packet.json> --decision-file <decision.json> --output <receipt.json> --agent <worker-name> --json` from the assigned worktree. The command derives Git facts, runs every sealed test, seals v1, publishes atomically, and self-validates; never supply or hand-author those facts.
6. An amendment invalidates the receipt and all revision-sensitive evidence; rerun completion. Return only its bounded result plus decisions, risks, generator outputs, and handoff. Leave Coordinator acceptance, integration, cleanup, independent review, publishing, and the human-facing rationale discussion to their owners.

## PROCESS Write Ownership

- A bare repository-relative ownership path is one exact file.
- A directory subtree requires an explicit trailing /** declaration, for example internal/templates/**.
- Legacy bare directory declarations remain readable, but workspace prepare may reject them; correct the PROCESS or pass an explicit recursive ownership value before allocation.

## Project Workflow

- Workflow Source: `builtin`
- Workflow Schema: `issue-spec`
- Workflow Config: `issue-spec/config.yaml`

Project workflow templates are declarative only. Active proposal, design, implement, SPEC, TASK, PROCESS, and QUESTION artifacts remain in the selected issue backend's issue-native storage; historical REVIEW and VERIFY artifacts are audit-only. Repository-mode durable specs are materialized and checked on the implementation branch.
