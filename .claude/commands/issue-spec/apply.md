---
name: "Issue Spec: Apply"
description: "Implement directly or use an optional PROCESS when managed coordination is required."
category: "Workflow"
tags: ["workflow", "issue-spec"]
---

# Issue Spec Apply

Coordinator: default to a direct single-writer implementation. A single child or subagent may own that implementation without PROCESS while the coordinator performs no concurrent code writes. Use optional Implement and TASK planning when engineering risk makes them useful. Select PROCESS only for concurrent code writers, protection of pre-existing work through isolation, enforced path ownership, restartable cross-session handoff, or dependency-ordered integration. Using a child, changing several files, requesting independent review, or needing merge evidence is not sufficient. When Implement is selected, persist it, perform its first QUESTION pass, then finalize the selected implementation plan. Author PROCESS only if managed coordination was selected. Issue bodies and typed planning artifacts remain authoritative planning state.

## Direct Single-Writer Path

For a bounded change without those managed-coordination needs, the coordinator MAY implement directly or dispatch exactly one code-writing child or subagent in the selected implementation checkout. Keep one writer active, give a delegated child a bounded goal and focused verification, and wait for it before making coordinator code changes. Use ordinary Git and provider checks; do not manufacture PROCESS, workspace lifecycle, role receipt, handoff, a typed rationale carrier, or evidence state. Read-only investigation and review children remain available without PROCESS.

For both direct single-writer and managed PROCESS implementation, every actual code writer owns zero or more line-rationale drafts for non-obvious decisions in its changed code. On the direct path this is the actual single writer, whether Coordinator or child; under managed PROCESS each worker owns its drafts. A useful draft names repository-relative path, stable symbol plus changed-line anchor, and concise why/tradeoff/risk, with no secret, raw payload, or credential. Writers need no provider credentials and MUST NOT guess final diff positions. Obvious code needs no draft, quota, coverage target, or placeholder.

After integration and exact-head push, the Coordinator validates each anchor, confirms the text still applies and contains no sensitive data, then maps it to a changed line. Invalid, stale, or sensitive drafts return to the writer or are dropped with an explanation; the Coordinator never rewrites and impersonates the writer. Publish valid worker text as provider-native non-blocking inline discussion. Before human review, publish or refresh the ordinary top-level `### Implementation Rationale` with intent, decisions/tradeoffs, boundaries/risks, validation/results, exact head, planning links, and an inline-rationale index. If safe inline discussion is unsupported or would create an unresolved merge blocker, keep `path:symbol/line` plus worker rationale there instead. No Implement, TASK, PROCESS, or SPEC is required. Never use a rationale-evidence command, marker, ID, typed carrier, PROCESS/SPEC binding, evidence, or gate. On required write failure report the error, retain the body, and do not claim human-review handoff complete. Comments and status never enter `merge-check` or merge authority.

For every agent-executed change-bearing PROCESS, seal the implementation assignment and dispatch a real non-Coordinator worker with the packet below. Preserve exact base, ownership, DCO, tests, generators, dependency order, managed worktree isolation, and bounded handoff. These controls are implementation safety only: they do not create review, verification, rationale evidence, receipt, coverage, or finalization authority, and merge-check never reads their lifecycle.

## Implementation Role Packet

Relay this packet verbatim to the worker; the Coordinator MUST NOT execute it.

1. Accept only the sealed implementation assignment for the exact PROCESS, base revision, worktree, write ownership, focused tests, generators, result schema, and design_context. Do not load proposal bodies, the complete DAG, link matrices, post-merge policy, provider routing, or unrelated artifacts.
2. Require design_context.read_mode=complete-issue-body and conflict_policy=design-authoritative-stop. Read the complete Design with issue-spec read issue --repo higress-group/issue-spec --issue <design_context.source_url> without comments, timeline, history, or gates. Stop and report any conflict.
3. Work only in the assigned worktree and owned paths. Preserve the named invariant, decisions, must_preserve, must_not, and minimum_verification exactly. Do not collect or pass runtime-specific session IDs.
4. Implement the invariant, run assigned generators, finish exactly one DCO commit when required, and leave the tree clean. Collect zero or more line-rationale drafts only for non-obvious decisions: repository-relative path, stable symbol plus changed-line anchor, and concise why/tradeoff/risk without secret, raw payload, or credential. Do not guess a provider diff position or create filler. If cohesion fails, stop with stable-interface split options and acceptance consequences.
5. Outside the worktree, write only `{"decisions":[],"risks":[],"rationale_draft":"..."}`, rendering drafts in `rationale_draft` or leaving it empty when none are valuable. Run `issue-spec role complete --assignment-file <sealed-packet.json> --decision-file <decision.json> --output <receipt.json> --agent <worker-name> --json`; it derives Git facts, runs every sealed test, seals v1, publishes atomically, and self-validates. Provider access and final diff positions are not worker responsibilities.
6. An amendment invalidates the receipt and revision-sensitive evidence; rerun completion. Return the bounded result, decisions, risks, line-rationale drafts, generators, tests, and handoff. Leave Coordinator acceptance, integration, cleanup, review, anchor validation, publication, and top-level index to their owners; the Coordinator publishes worker-authored text but does not author it.

## Project Workflow

- Workflow Source: `builtin`
- Workflow Schema: `issue-spec`
- Workflow Config: `issue-spec/config.yaml`

Project workflow templates are declarative only. Active proposal, design, implement, SPEC, TASK, PROCESS, and QUESTION artifacts remain in the selected issue backend's issue-native storage; historical REVIEW and VERIFY artifacts are audit-only. Repository-mode durable specs are materialized and checked on the implementation branch.
