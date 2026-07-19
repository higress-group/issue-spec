---
name: issue-spec-review
description: Review an issue-spec implementation change, create line findings, reply after fixes, and sync REVIEW comments.
license: MIT
compatibility: Requires issue-spec CLI.
metadata:
  author: issue-spec
  version: "1.0"
  generatedBy: "issue-spec"
---

# Issue Spec Review

Use when the user asks for /issue-spec:review, issue-spec review, or an implementation review gate.

## Steps

1. The review agent runs issue-spec review sync --repo higress-group/issue-spec --implement <issue> --id REVIEW-<n> --agent <review-agent> --json under its own identity. On GitHub add --pr <number>; on a self-hosted profile omit --pr and add --revision <exact-head>. Sync authoritatively captures current rationale, findings, checks, per-PROCESS execution class and evidence diagnostics, then upserts one stable done REVIEW completion even with zero findings. The evidence gate reads the REVIEW writer's --agent as the reviewing identity for independence, so the reviewing agent (never the coordinator) MUST author it. review sync owns the established "## Review Sync Summary" REVIEW body and completion evidence: never hand-edit either, fabricate a finding, or substitute a generic approval framework. For separate manual review evidence, generate a REVIEW body with issue-spec comment generate --type REVIEW --input-file review.json and upsert it under the same review-agent identity.
2. For every active SPEC that has a valid change-bearing carrier, you MUST spawn or assign a dedicated review agent as a review PROCESS owner, and each review MUST be performed by a different agent than the worker that authored the code under review. The reviewing agent's --agent identity MUST name a real sub-agent actually spawned to perform the review; MUST NOT fabricate or reuse a name to bypass this. For each distinct change-bearing author Agent, the coordinator SHOULD provide at least one independent review assignment covering that author's PROCESS outputs and affected SPECs. One review agent MAY cover multiple authors when it authored none of their code; this is scheduling guidance, while final verification remains per SPEC and does not enforce a 1:1 relation. Multiple review agents can run in parallel when their review scopes are independent. A review PROCESS is complete only with a linked done REVIEW or resolved finding covering an active SPEC; it still needs TASK and implementation-change links.
3. Give each review agent a concrete scope and expected output: actionable findings only, severity, file/line, linked SPEC, owner PROCESS, and suggested fix.
4. Follow issue-spec-workflow for backend-appropriate finding routing. When that route uses issue-spec review finding, each review agent authors its own actionable line findings under its own --agent identity. Use P0/P1 for blockers and P2 for non-blocking follow-up. The coordinator does not create findings on a review agent's behalf.
5. Assign every finding to a PROCESS owner. After review/fix convergence, run the final review sync, then explicitly link that REVIEW bidirectionally to its review PROCESS, every covered change-bearing PROCESS, and every covered active SPEC before completing the review PROCESS. Repeat issue-spec link --repo higress-group/issue-spec --from REVIEW-<n> --from-issue <implement-issue> --to PROCESS-<n> --to-issue <implement-issue> for the review PROCESS and each implementation PROCESS, then run issue-spec link --repo higress-group/issue-spec --from REVIEW-<n> --from-issue <implement-issue> --to SPEC-<n> --to-issue <proposal-issue> for each SPEC. Run these commands after the final review sync so regeneration cannot replace them. Never rely on PROCESS/SPEC IDs in prose or auto-infer links.
6. Follow issue-spec-workflow for backend-appropriate reply and resolution routing. The worker that owns the affected code fixes it and replies on the original finding thread using its own --agent. The review agent that opened the finding then re-checks the diff and owns the resolved reply or code-review conversation resolution; a worker reply alone does not resolve a finding.
7. P0/P1 findings must be resolved by review-agent evidence before final verify/archive. Status and final verify validate the same backend-appropriate completion, explicit links, and reviewer independence; they do not refresh REVIEW.

## Review DAG Policy

1. Every active SPEC that has a valid change-bearing carrier MUST be covered by at least one independent review PROCESS node before final verify, owned by an agent that did not author the code under review. A single review PROCESS MAY cover a SPEC that several implementation PROCESS nodes or distinct worker Agents contribute to. Final verify remains per SPEC and fails closed (process.review.required) when a covered SPEC has no such review PROCESS; it does not require one unique reviewer per implementation Agent.
2. Review parallelism is gated, not default: run multiple review agents in parallel only when their review scopes are independent, for example CLI/API behavior, workflow docs, tests, compatibility, or security-sensitive surfaces.
3. Each review agent authors its own findings through the backend-appropriate route under its own agent identity; the coordinator schedules review agents and routes blockers but does not author findings on their behalf.
4. Route findings to the owner PROCESS or a dedicated repair PROCESS. Repair PROCESS nodes are DAG nodes too: they follow the same serial/parallel gating as implementation nodes and record ### Handoff evidence when part of a serial chain.
5. P0/P1 findings block final verify until the owning worker fixes them and replies on the thread, and the review agent that opened the finding re-checks and records or resolves the code-review conversation.
6. If a review agent finds no issues, use the final synced REVIEW completion as the evidence carrier. Run the explicit link flows above, then confirm with issue-spec comment list --repo higress-group/issue-spec --issue <implement-issue> --type REVIEW --json that Related Comments contains the review PROCESS URL, every covered change-bearing PROCESS URL, and every covered active SPEC URL before marking the review PROCESS done. A no-finding statement without all three link classes is incomplete review evidence.

## Project Workflow

- Workflow Source: `builtin`
- Workflow Schema: `issue-spec`
- Workflow Diagnostics:

Project workflow templates are declarative only. Active proposal, design, implement, SPEC, TASK, PROCESS, QUESTION, REVIEW, and VERIFY artifacts remain in the selected issue backend's issue-native storage; durable specs are repository files created during archive.
