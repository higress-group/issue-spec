---
name: "Issue Spec: Review"
description: "Review an issue-spec implementation PR, create PR line findings, reply after fixes, and sync REVIEW comments."
category: "Workflow"
tags: ["workflow", "issue-spec"]
---

# Issue Spec Review

Use when the user asks for /issue-spec:review, issue-spec review, or a PR review gate for an issue-spec implementation.

## Steps

1. Run issue-spec review sync --repo higress-group/issue-spec --pr <number> --implement <issue> --id REVIEW-<n> --json to capture current rationale comments, findings, checks, per-PROCESS execution class and evidence diagnostics. review sync owns the established "## Review Sync Summary" REVIEW body shape; do not hand-edit it. For separate manual review evidence, generate a REVIEW body with issue-spec comment generate --type REVIEW --input-file review.json.
2. For non-trivial PRs, you MUST spawn or assign dedicated review agents as review PROCESS owners, and each review MUST be performed by a different agent than the one that authored the code under review. The reviewing agent's --agent identity MUST name a real sub-agent actually spawned to perform the review; MUST NOT fabricate or reuse a name to bypass this. Multiple review agents can run in parallel when their review scopes are independent. A review PROCESS is complete only with a linked done REVIEW or resolved finding covering an active SPEC; it still needs TASK and PR links.
3. Give each review agent a concrete scope and expected output: actionable findings only, severity, file/line, linked SPEC, owner PROCESS, and suggested fix.
4. Each review agent authors its own actionable PR line findings directly with issue-spec review finding, using its own --agent identity and assigned --agent-session. Use P0/P1 for blockers and P2 for non-blocking follow-up. The coordinator does not create findings on a review agent's behalf.
5. Assign every finding to a PROCESS owner. If no findings are found, record that result in the synced REVIEW, then link that REVIEW bidirectionally to both its review PROCESS and every covered active SPEC before completing the PROCESS. Use issue-spec link --repo higress-group/issue-spec --from REVIEW-<n> --from-issue <implement-issue> --to PROCESS-<n> --to-issue <implement-issue>, then issue-spec link --repo higress-group/issue-spec --from REVIEW-<n> --from-issue <implement-issue> --to SPEC-<n> --to-issue <proposal-issue> for each covered SPEC. Run these commands after the final review sync so sync cannot replace the evidence links.
6. The worker that owns the affected code fixes it and replies on the original finding thread with issue-spec review reply using its own --agent and --agent-session. The review agent that opened the finding then re-checks the diff and owns the resolved reply or GitHub conversation resolution; a worker reply alone does not resolve a finding.
7. Re-run review sync. P0/P1 findings must be resolved by review-agent evidence before final verify/archive.

## Review DAG Policy

1. Every non-trivial PR MUST have at least one dedicated review PROCESS node before final verify, owned by an agent that did not author the code under review.
2. Review parallelism is gated, not default: run multiple review agents in parallel only when their review scopes are independent, for example CLI/API behavior, workflow docs, tests, compatibility, or security-sensitive surfaces.
3. Each review agent authors its own findings with issue-spec review finding under its own agent identity; the coordinator schedules review agents and routes blockers but does not author findings on their behalf.
4. Route findings to the owner PROCESS or a dedicated repair PROCESS. Repair PROCESS nodes are DAG nodes too: they follow the same serial/parallel gating as implementation nodes and record ### Handoff evidence when part of a serial chain.
5. P0/P1 findings block final verify until the owning worker fixes them and replies on the thread, and the review agent that opened the finding re-checks and records the resolution or resolves the GitHub conversation.
6. If a review agent finds no issues, use the final synced REVIEW as the evidence carrier. Run the two issue-spec link flows above, then confirm with issue-spec comment list --repo higress-group/issue-spec --issue <implement-issue> --type REVIEW --json that Related Comments contains the review PROCESS URL and each covered active SPEC URL before marking the review PROCESS done. A no-finding statement without both link classes is incomplete review evidence.

## Project Workflow

- Workflow Source: `builtin`
- Workflow Schema: `issue-spec`
- Workflow Diagnostics:

Project workflow templates are declarative only. Active proposal, design, implement, SPEC, TASK, PROCESS, QUESTION, REVIEW, and VERIFY artifacts remain in GitHub issue-native storage; durable specs are repository files created during archive.
