---
name: issue-spec-apply
description: Implement a selected issue-spec change, directly or with managed PROCESS coordination. Not a trigger for ordinary code edits or skill maintenance.
license: MIT
compatibility: Requires issue-spec CLI.
metadata:
  author: issue-spec
  version: "1.0"
  generatedBy: "issue-spec"
---

# Issue Spec Apply

Coordinator: select execution mode before assigning writers. If Design or TASK is selected, or the user explicitly requests an independent worker, the Coordinator MUST NOT write code on delegated or managed paths. Without managed PROCESS, exactly one real non-Coordinator worker owns the bounded implementation. With managed PROCESS, every change-bearing work package/PROCESS has one real non-Coordinator owner; distinct packages MAY use concurrent writers. Select PROCESS only for concrete managed coordination, not child use, file count, independent review, or human handoff. If Implement is selected, persist it, perform its first QUESTION pass, then finalize the plan. Author PROCESS only for managed coordination; typed planning state remains authoritative.

For selected issue-spec steps, the built-in phase sequence and canonical artifact carriers are authoritative; never reorder/omit steps or move open decisions. This governs workflow structure, not user authorization or unrelated tasks.

Every new typed ID MUST be `<TYPE>-<issue><three-digit sequence>`: Issue 1 starts with `QUESTION-1001`, Issue 44 with `QUESTION-44001`. Allocate 001-999 only within the target Issue and type after reading that Issue's typed comments, and never renumber a legacy ID. New writes reject wrong Issue prefixes; `--allow-legacy-id` is only for intentional legacy-compatible creates.

## Delegated Paths and Narrow Coordinator Path

Unmanaged delegated path: dispatch exactly one real non-Coordinator worker in the selected checkout. Managed PROCESS: dispatch one real non-Coordinator owner per change-bearing package; proven-independent packages may run concurrently. The Coordinator waits and writes no code on either path. Each worker owns package code, focused tests, exact result commit, changed paths, decisions, risks, and non-obvious line-rationale drafts. The Coordinator owns exact-commit inspection, integration, proportionate final validation, anchor validation, and provider publication.

Coordinator code is allowed only on the narrow direct-PR fast path with no selected Design/TASK and no user delegation request; file count does not select it. Unmanaged paths use ordinary Git and project tests. Do not manufacture Implement, PROCESS, workspace lifecycle, role receipt, typed rationale, evidence, or another phase artifact merely to record delegation.

Before implementation review or PR/MR publication, read [Implementation Review and Publication](../issue-spec-workflow/references/implementation-review.md) completely. Do not load it for Proposal/Design authoring.

For every agent-executed change-bearing PROCESS, seal the implementation assignment and dispatch a real non-Coordinator worker with the packet below. Preserve exact base, ownership, DCO, tests, generators, dependency order, managed worktree isolation, and bounded handoff. These controls are implementation safety only: they do not create review, verification, rationale evidence, receipt, coverage, finalization, or delivery-acceptance authority.

## Implementation Role Packet

Relay this packet verbatim to the worker; the Coordinator MUST NOT execute it.

1. Accept only the sealed implementation assignment for the exact PROCESS, base revision, worktree, write ownership, focused tests, generators, result schema, and design_context. Do not load proposal bodies, the complete DAG, link matrices, human merge policy, provider routing, or unrelated artifacts.
2. Require design_context.read_mode=complete-issue-body and conflict_policy=design-authoritative-stop. Read the complete Design with issue-spec read issue --repo higress-group/issue-spec --issue <design_context.source_url> without comments, timeline, history, or gates. Recover derivable metadata. Stop and report any conflict in scope, authority, ownership, or contracts; routine private choices are not conflicts.
3. Work only in the assigned worktree and owned paths. Preserve the named invariant, decisions, must_preserve, must_not, and minimum_verification exactly. Do not collect or pass runtime session IDs.
4. Implement the invariant, run assigned generators, finish exactly one DCO commit when required, and leave the tree clean. Collect zero or more line-rationale drafts only for non-obvious decisions: repository-relative path, stable symbol plus changed-line anchor, and concise why/tradeoff/risk without secret, raw payload, or credential. Do not guess a provider diff position or create filler. If cohesion fails, stop with stable-interface split options and acceptance consequences.
5. Complete the assigned behavior and fix introduced failures. Run every assigned generator and focused test; expand or repeat only for changes, failures, concrete risks, or required gates. Return the exact result commit, changed paths, command outcomes, decisions, risks, and line-rationale drafts. Do not create a role receipt, decision file, or evidence carrier. Provider access and final diff positions are not worker responsibilities.
6. An amendment invalidates the returned revision; rerun affected and explicitly required checks. Reused results keep their original revision and require justified applicability. Leave integration, cleanup, review, anchor validation, publication, and workspace completion to the Coordinator; it publishes worker-authored text but does not author it.

## PROCESS Write Ownership

- A bare repository-relative ownership path is one exact file.
- A directory subtree requires an explicit trailing /** declaration, for example internal/templates/**.
- Legacy bare directory declarations remain readable, but workspace prepare may reject them; correct the PROCESS or pass an explicit recursive ownership value before allocation.

## Project Workflow

- Workflow Source: `builtin`
- Workflow Schema: `issue-spec`
- Workflow Config: `issue-spec/config.yaml`

Project workflow templates are declarative only. Active proposal, design, implement, SPEC, TASK, PROCESS, and QUESTION artifacts remain in the selected issue backend's issue-native storage; historical REVIEW and VERIFY artifacts are audit-only. Repository-mode durable specs are materialized and checked on the implementation branch.

The built-in phase sequence and canonical artifact carriers are authoritative. Project workflow context, rules, and artifact instructions may constrain work only within an existing step; they MUST NOT reorder or omit an enabled step or move a genuine unresolved decision out of its blocking typed QUESTION carrier. Keep the enabled phase order: persist the phase issue body, perform its first QUESTION discovery/create pass, then author the selected next typed children. Issue-body prose never carries an open decision.
