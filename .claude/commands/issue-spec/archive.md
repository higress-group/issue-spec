---
name: "Issue Spec: Archive"
description: "Create the post-merge durable spec archive PR for an issue-spec change."
category: "Workflow"
tags: ["workflow", "issue-spec"]
---

# Issue Spec Archive

Use when the user asks for /issue-spec:archive, issue-spec archive, or creating the post-merge durable spec PR.

## Steps

1. Confirm the implementation PR is merged and had issue-spec closing links before merge.
2. Choose the --capability value as a stable long-lived capability or domain directory, not the original change/proposal name. Prefer names that can host related future durable specs, for example workflow-identity-and-sessions instead of agent-session-source-of-truth.
3. Inspect existing durable specs before creating or finalizing the archive PR. Read `issue-spec/specs/<capability>/spec.md` when it exists, and scan related `issue-spec/specs/*/spec.md` files when the new behavior may belong with an existing capability. If `openspec/specs/<capability>/spec.md` already exists, issue-spec may select that legacy path for update and report the compatibility choice. Decide whether to update, merge, or reorganize existing durable requirements instead of adding a duplicate or narrowly named spec.
4. Create the durable spec PR and idempotently close any still-open PR-associated active issues:

       issue-spec archive durable-spec --repo higress-group/issue-spec --proposal <proposal-issue> --design <design-issue> --implement <implement-issue> --pr <implementation-pr> --capability <capability> --create-pr --branch issue-spec/durable-spec-<capability> --close-issues --json

5. Review and edit the generated durable spec draft before handoff or merge. Reconcile it with any existing related durable specs, regroup related source SPEC content into durable capability modules instead of preserving one-to-one source SPEC sections, and keep Source SPEC links for traceability.
6. Keep only long-lived behavior. Do not copy process records, review findings, or verification logs into durable specs.
7. Keep the closed proposal/design/implement issues as audit history.

## Project Workflow

- Workflow Source: `builtin`
- Workflow Schema: `issue-spec`
- Workflow Diagnostics:

Project workflow templates are declarative only. Active proposal, design, implement, SPEC, TASK, PROCESS, QUESTION, REVIEW, and VERIFY artifacts remain in GitHub issue-native storage; durable specs are repository files created during archive.
