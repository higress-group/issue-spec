---
name: issue-spec-propose
description: Create or continue proposal, SPEC, QUESTION, design, and TASK artifacts for an issue-spec change.
license: MIT
compatibility: Requires issue-spec CLI.
metadata:
  author: issue-spec
  version: "1.0"
  generatedBy: "issue-spec"
---

# Issue Spec Propose

Use when the user asks for /issue-spec:propose, issue-spec propose, creating a change proposal, drafting SPEC comments, or preparing design/tasks after questions converge.

## Steps

1. Validate the active workflow definition before creating artifacts:

       issue-spec workflow validate --repo higress-group/issue-spec --json

2. Create the proposal issue:

       issue-spec issue create proposal --repo higress-group/issue-spec --change <change-name> --body-file <proposal.md>

   Generated titles use the standardized `Proposal: <subject>`, `Design: <subject>`, and `Implement: <subject>` family. When --body-file is used, the subject comes from the first Markdown H1 when possible while the change name stays in issue-spec metadata. Use --title only for an explicit user-requested custom title; do not apply style-only issue update rewrites after creation. Historical issues with `issue-spec proposal: <change>`, `issue-spec design: <change>`, or `issue-spec implement: <change>` titles remain valid workflow artifacts.

3. If the proposal body needs revision after discussion, update it in place:

       issue-spec issue update --repo higress-group/issue-spec --issue <proposal-issue> --body-file <proposal.md> --summary "<what changed>"

4. Generate canonical SPEC bodies instead of hand-writing Markdown:

       issue-spec comment generate --type SPEC --id SPEC-001 --status confirmed --scope "<scope>" --input-file spec.json | issue-spec comment upsert --repo higress-group/issue-spec --issue <proposal-issue> --type SPEC --id SPEC-001 --body-file -

   The SPEC input JSON has requirement.title, requirement.text (use MUST/SHALL), and a scenarios array of title/when/then. comment upsert --type SPEC validates canonical discipline (## Requirement:, normative MUST/SHALL, at least one ### Scenario: with **WHEN**/**THEN** bullets) by default and rejects malformed bodies. Use --allow-noncanonical only as a write-time migration bypass; it does not create durable approval and status/verify/archive keep reporting the noncanonical state.
5. Add QUESTION comments for unresolved behavior with issue-spec question create and resolve blocking questions before design.
6. Create the design issue after SPEC/QUESTION convergence:

       issue-spec issue create design --repo higress-group/issue-spec --change <change-name> --proposal <proposal-issue-or-url> --body-file <design.md>

7. Generate TASK bodies with issue-spec comment generate --type TASK --id TASK-001 --input-file task.json, upsert them with issue-spec comment upsert --type TASK, and link every TASK to covered SPEC comments with issue-spec link. The TASK input JSON has title, summary, checklist, covers, and an execution_planning object (owned_areas, shared_touchpoints, dependencies, coupling, execution_mode, complexity) that renders the required ### Execution Planning section; comment upsert --type TASK rejects a TASK without it. Use the same comment generate command family for PROCESS, REVIEW, and VERIFY comments instead of inventing raw Markdown shapes; PROCESS input takes parent_task and handoff fields.
8. Create the implement issue once tasks are ready:

       issue-spec issue create implement --repo higress-group/issue-spec --change <change-name> --proposal <proposal-issue-or-url> --design <design-issue-or-url> --body-file <implement.md>

9. Run issue-spec verify-links and fix missing backlinks before implementation.
   This run covers SPEC↔TASK only; after PROCESS comments are created in
   issue-spec-apply (step 6), re-run verify-links to also catch PROCESS↔TASK gaps.

## Cross-Skill Boundary

Process creation, PROCESS↔TASK links, and PROCESS↔PR links live in
`issue-spec-apply`, not here. When you finish propose (TASKs complete),
hand off to apply before re-running `verify-links` for full coverage.

Link matrix (each direction has a designated owner; rows marked ✓ are gated by `verify-links`):
- ✓ SPEC ↔ TASK        (this skill, step 7)
- ✓ TASK ↔ PROCESS     (issue-spec-apply, step 6)
-   PROCESS ↔ SPEC     (issue-spec-apply, step 10, via pr rationale and review finding)
-   PROCESS ↔ PR       (issue-spec-apply, step 8, via pr link-process)

## Project Workflow

- Workflow Source: `builtin`
- Workflow Schema: `issue-spec`
- Workflow Diagnostics:

Project workflow templates are declarative only. Active proposal, design, implement, SPEC, TASK, PROCESS, QUESTION, REVIEW, and VERIFY artifacts remain in GitHub issue-native storage; durable specs are repository files created during archive.

