---
name: "Issue Spec: Verify"
description: "Run final issue-spec verification across traceability, questions, review findings, PR rationale, PR checks, and durable spec draft."
category: "Workflow"
tags: ["workflow", "issue-spec"]
---

# Issue Spec Verify

Use when the user asks for /issue-spec:verify, issue-spec verify, or final readiness evidence before merge/archive.

## Steps

1. Run issue-spec status --repo higress-group/issue-spec --proposal <issue> --design <issue> --implement <issue> --gate final --json and resolve every locally knowable blocker. This is a forecast, not a substitute for authoritative final verify.
2. Run focused project tests and record evidence in VERIFY comments. Generate VERIFY bodies with issue-spec comment generate --type VERIFY --input-file verify.json instead of hand-writing Markdown, and reference the covered SPEC IDs so final verify can confirm coverage. A verification PROCESS needs a linked done VERIFY or required passing check with test evidence; inspect every per-PROCESS evidence report rather than requiring rationale from non-change-bearing classes.
3. Run issue-spec verify-links --repo higress-group/issue-spec --proposal <issue> --design <issue> --implement <issue> --json.
4. Render a durable spec draft:

       issue-spec archive durable-spec --repo higress-group/issue-spec --proposal <issue> --capability <capability> --output /tmp/<capability>-spec.md --json

5. Run final verify:

       issue-spec verify --repo higress-group/issue-spec --proposal <issue> --design <issue> --implement <issue> --pr <pr> --durable-spec /tmp/<capability>-spec.md --json

6. Final verify must fail if blocking questions, missing links, missing class-specific PROCESS evidence, open P0/P1 findings, failed or pending PR checks, or durable spec omissions exist. Change-bearing PROCESS nodes require matching GitHub inline rationale or, on self-hosted profiles, an exact-current append-only code-change rationale paired with trusted consumed native-ledger PROCESS/SPEC evidence. Review, verification, orchestration, and external nodes use their proportional evidence carriers.

## Project Workflow

- Workflow Source: `builtin`
- Workflow Schema: `issue-spec`
- Workflow Diagnostics:

Project workflow templates are declarative only. Active proposal, design, implement, SPEC, TASK, PROCESS, QUESTION, REVIEW, and VERIFY artifacts remain in the selected issue backend's issue-native storage; durable specs are repository files created during archive.
