---
name: issue-spec-apply
description: Implement an optional PROCESS while preserving bounded workspace and handoff safety.
license: MIT
compatibility: Requires issue-spec CLI.
metadata:
  author: issue-spec
  version: "1.0"
  generatedBy: "issue-spec"
---

# Issue Spec Apply

Coordinator: use Implement, TASK, and PROCESS only when coordination, isolation, or delegation risk selects them. Persist the Implement issue, perform its first QUESTION pass, then complete PROCESS planning. Issue bodies and typed planning artifacts remain authoritative planning state.

For every agent-executed change-bearing PROCESS, seal the implementation assignment and dispatch a real non-Coordinator worker with the packet below. Preserve exact base, ownership, DCO, tests, generators, dependency order, managed worktree isolation, and bounded handoff. These controls are implementation safety only: they do not create review, verification, rationale, receipt, coverage, or finalization authority, and merge-check never reads their lifecycle.

## Implementation Role Packet

This packet is addressed to the dispatched worker subagent. Relay it verbatim with the sealed assignment; do not execute it in the coordinator context.

1. Accept only the sealed implementation assignment for the exact PROCESS, base revision, worktree, write ownership, focused tests, generators, result schema, and design_context. Do not load proposal bodies, the complete DAG, link matrices, post-merge policy, provider routing, or unrelated artifacts.
2. Before code changes, require design_context.read_mode=complete-issue-body and conflict_policy=design-authoritative-stop. Read the complete Design with issue-spec read issue --repo higress-group/issue-spec --issue <design_context.source_url> without comments, timeline, history, or gates. Stop and report any conflict; do not reinterpret or summarize the packet.
3. Work only in the assigned worktree and owned paths. Preserve the named invariant, decisions, must_preserve, must_not, and minimum_verification exactly. Do not collect or pass runtime-specific session IDs.
4. Implement the invariant, run assigned generators, finish exactly one DCO commit when required, and leave the tree clean. If the work cannot remain one bounded end-to-end invariant, stop with stable-interface split options and acceptance consequences.
5. Outside the worktree, write only `{"decisions":[],"risks":[],"rationale_draft":"..."}`, then run `issue-spec role complete --assignment-file <sealed-packet.json> --decision-file <decision.json> --output <receipt.json> --agent <worker-name> --json` from the assigned worktree. The command derives Git facts, runs every sealed test, seals v1, publishes atomically, and self-validates; never supply or hand-author those facts.
6. An amendment invalidates the receipt and all revision-sensitive evidence; rerun completion. Return only its bounded result plus decisions, risks, generator outputs, and handoff. Leave Coordinator acceptance, integration, cleanup, independent review, publishing, and rationale to their owners.

## PROCESS Write Ownership

- A bare repository-relative ownership path is one exact file.
- A directory subtree requires an explicit trailing /** declaration, for example internal/templates/**.
- Legacy bare directory declarations remain readable, but workspace prepare may reject them; correct the PROCESS or pass an explicit recursive ownership value before allocation.

## Project Workflow

- Workflow Source: `builtin`
- Workflow Schema: `issue-spec`
- Workflow Config: `issue-spec/config.yaml`

Project workflow templates are declarative only. Active proposal, design, implement, SPEC, TASK, PROCESS, QUESTION, REVIEW, and VERIFY artifacts remain in the selected issue backend's issue-native storage; repository-mode durable specs are materialized and checked on the implementation branch.
