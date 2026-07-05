---
name: issue-spec-verify
description: Run final issue-spec verification across traceability, questions, review findings, PR rationale, PR checks, and durable spec draft.
license: MIT
compatibility: Requires issue-spec CLI.
metadata:
  author: issue-spec
  version: "1.0"
  generatedBy: "issue-spec"
---

# Issue Spec Verify

Use when the user asks for /issue-spec:verify, issue-spec verify, or final readiness evidence before merge/archive.

## Steps

1. Run focused project tests and record evidence in VERIFY comments. Generate VERIFY bodies with issue-spec comment generate --type VERIFY --input-file verify.json instead of hand-writing Markdown, and reference the covered SPEC IDs so final verify can confirm coverage.
2. Run issue-spec verify-links --repo higress-group/issue-spec --proposal <issue> --design <issue> --implement <issue> --json.
3. Render a durable spec draft:

       issue-spec archive durable-spec --repo higress-group/issue-spec --proposal <issue> --capability <capability> --output /tmp/<capability>-spec.md --json

4. Run final verify:

       issue-spec verify --repo higress-group/issue-spec --proposal <issue> --design <issue> --implement <issue> --pr <pr> --durable-spec /tmp/<capability>-spec.md --json

5. Final verify must fail if blocking questions, missing links, missing PROCESS rationale, open P0/P1 findings, failed or pending PR checks, or durable spec omissions exist.
