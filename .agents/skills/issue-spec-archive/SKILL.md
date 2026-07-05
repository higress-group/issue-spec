---
name: issue-spec-archive
description: Create the post-merge durable spec archive PR for an issue-spec change.
license: MIT
compatibility: Requires issue-spec CLI.
metadata:
  author: issue-spec
  version: "1.0"
  generatedBy: "issue-spec"
---

# Issue Spec Archive

Use when the user asks for /issue-spec:archive, issue-spec archive, or creating the post-merge durable spec PR.

## Steps

1. Confirm the implementation PR is merged.
2. Choose `<capability>` as a stable long-lived capability or domain directory, not the original change/proposal name. Prefer names that can host related future durable specs, for example `workflow-identity-and-sessions` instead of `agent-session-source-of-truth`.
3. Inspect existing durable specs before creating or finalizing the archive PR. Read `issue-spec/specs/<capability>/spec.md` when it exists, and scan related `issue-spec/specs/*/spec.md` files when the new behavior may belong with an existing capability. Decide whether to update, merge, or reorganize existing durable requirements instead of adding a duplicate or narrowly named spec.
4. Create the durable spec PR:

       issue-spec archive durable-spec --repo higress-group/issue-spec --proposal <issue> --capability <capability> --create-pr --branch issue-spec/durable-spec-<capability> --json

5. Review and edit the generated durable spec draft before handoff or merge. Reconcile it with any existing related durable specs, regroup related source SPEC content into durable capability modules instead of preserving one-to-one source SPEC sections, and keep Source SPEC links for traceability.
6. Keep only long-lived behavior. Do not copy process records, review findings, or verification logs into durable specs.
7. After durable spec PR merge, keep proposal/design/implement issues as audit history unless the project policy says to close them.
