---
name: "Issue Spec: Archive"
description: "Create the post-merge durable spec archive PR for an issue-spec change."
category: "Workflow"
tags: ["workflow", "issue-spec"]
---

# Issue Spec Archive

Use only after implementation merge and authoritative final verification.

1. Confirm the implementation change merged and required closing links existed. Archive may read an existing required REVIEW completion when implementation merge policy requires it. Archive never creates, updates, or refreshes REVIEW or adds archive-specific review state.
2. Choose --capability as a stable long-lived domain, not the change name. Inspect issue-spec/specs/<capability>/spec.md and related issue-spec/specs/*/spec.md; reuse an existing openspec/specs/<capability>/spec.md only through the documented compatibility path.
3. Create the separate durable-spec PR with issue-spec archive durable-spec, passing proposal, Design, Implement, implementation change, --create-pr, and --close-issues.
4. Review the generated draft. Merge related requirements into coherent capability modules, preserve prior requirements and Source SPEC links, and keep only long-lived behavior. Do not copy PROCESS, review-finding, or verification-log history.
5. Keep closed change issues as audit history.

## Project Workflow

- Workflow Source: `builtin`
- Workflow Schema: `issue-spec`
- Workflow Diagnostics:

Project workflow templates are declarative only. Active proposal, design, implement, SPEC, TASK, PROCESS, QUESTION, REVIEW, and VERIFY artifacts remain in the selected issue backend's issue-native storage; durable specs are repository files created during archive.
