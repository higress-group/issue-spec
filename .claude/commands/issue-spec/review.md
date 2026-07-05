---
name: "Issue Spec: Review"
description: "Review an issue-spec implementation PR, create PR line findings, reply after fixes, and sync REVIEW comments."
category: "Workflow"
tags: ["workflow", "issue-spec"]
---

# Issue Spec Review

Use when the user asks for /issue-spec:review, issue-spec review, or a PR review gate for an issue-spec implementation.

## Steps

1. Run issue-spec review sync --repo higress-group/issue-spec --pr <number> --implement <issue> --id REVIEW-<n> --json to capture current rationale comments, findings, checks, and artifact writer session diagnostics. review sync owns the established "## Review Sync Summary" REVIEW body shape; do not hand-edit it. For separate manual review evidence, generate a REVIEW body with issue-spec comment generate --type REVIEW --input-file review.json.
2. For non-trivial PRs, spawn or assign dedicated review agents as review PROCESS owners. Multiple review agents can run in parallel when their review scopes are independent.
3. Give each review agent a concrete scope and expected output: actionable findings only, severity, file/line, linked SPEC, owner PROCESS, and suggested fix.
4. Each review agent authors its own actionable PR line findings directly with issue-spec review finding, using its own --agent identity and assigned --agent-session. Use P0/P1 for blockers and P2 for non-blocking follow-up. The coordinator does not create findings on a review agent's behalf.
5. Assign every finding to a PROCESS owner. If no findings are found, record that result in REVIEW or VERIFY evidence.
6. The worker that owns the affected code fixes it and replies on the original finding thread with issue-spec review reply using its own --agent and --agent-session. The review agent that opened the finding then re-checks the diff and owns the resolved reply or GitHub conversation resolution; a worker reply alone does not resolve a finding.
7. Re-run review sync. P0/P1 findings must be resolved by review-agent evidence before final verify/archive.

## Review DAG Policy

1. Every non-trivial PR should have at least one dedicated review PROCESS node before final verify.
2. Review parallelism is gated, not default: run multiple review agents in parallel only when their review scopes are independent, for example CLI/API behavior, workflow docs, tests, compatibility, or security-sensitive surfaces.
3. Each review agent authors its own findings with issue-spec review finding under its own agent identity; the coordinator schedules review agents and routes blockers but does not author findings on their behalf.
4. Route findings to the owner PROCESS or a dedicated repair PROCESS. Repair PROCESS nodes are DAG nodes too: they follow the same serial/parallel gating as implementation nodes and record ### Handoff evidence when part of a serial chain.
5. P0/P1 findings block final verify until the owning worker fixes them and replies on the thread, and the review agent that opened the finding re-checks and records the resolution or resolves the GitHub conversation.
6. If a review agent finds no issues, record that result in REVIEW or VERIFY evidence before marking the review PROCESS done.

## Project Workflow

- Workflow Source: `builtin`
- Workflow Schema: `issue-spec`
- Workflow Diagnostics:

Project workflow templates are declarative only. Active proposal, design, implement, SPEC, TASK, PROCESS, QUESTION, REVIEW, and VERIFY artifacts remain in GitHub issue-native storage; durable specs are repository files created during archive.
