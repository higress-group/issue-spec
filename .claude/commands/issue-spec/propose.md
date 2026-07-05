---
name: "Issue Spec: Propose"
description: "Create or continue proposal, SPEC, QUESTION, design, and TASK artifacts for an issue-spec change."
category: "Workflow"
tags: ["workflow", "issue-spec"]
---

# Issue Spec Propose

Use when the user asks for /issue-spec:propose, issue-spec propose, creating a change proposal, drafting SPEC comments, or preparing design/tasks after questions converge.

## Steps

1. Create the proposal issue:

       issue-spec issue create proposal --repo higress-group/issue-spec --change <change-name> --body-file <proposal.md>

2. If the proposal body needs revision after discussion, update it in place:

       issue-spec issue update --repo higress-group/issue-spec --issue <proposal-issue> --body-file <proposal.md> --summary "<what changed>"

3. Generate canonical SPEC bodies instead of hand-writing Markdown:

       issue-spec comment generate --type SPEC --id SPEC-001 --status confirmed --scope "<scope>" --input-file spec.json | issue-spec comment upsert --repo higress-group/issue-spec --issue <proposal-issue> --type SPEC --id SPEC-001 --body-file -

   The SPEC input JSON has requirement.title, requirement.text (use MUST/SHALL), and a scenarios array of title/when/then. comment upsert --type SPEC validates canonical discipline (## Requirement:, normative MUST/SHALL, at least one ### Scenario: with **WHEN**/**THEN** bullets) by default and rejects malformed bodies. Use --allow-noncanonical only as a write-time migration bypass; it does not create durable approval and status/verify/archive keep reporting the noncanonical state.
4. Add QUESTION comments for unresolved behavior with issue-spec question create and resolve blocking questions before design.
5. Create the design issue after SPEC/QUESTION convergence:

       issue-spec issue create design --repo higress-group/issue-spec --change <change-name> --proposal <proposal-issue-or-url> --body-file <design.md>

6. Generate TASK bodies with issue-spec comment generate --type TASK --id TASK-001 --input-file task.json, upsert them with issue-spec comment upsert --type TASK, and link every TASK to covered SPEC comments with issue-spec link. Use the same comment generate command family for PROCESS, REVIEW, and VERIFY comments instead of inventing raw Markdown shapes.
7. Create the implement issue once tasks are ready:

       issue-spec issue create implement --repo higress-group/issue-spec --change <change-name> --proposal <proposal-issue-or-url> --design <design-issue-or-url> --body-file <implement.md>

8. Run issue-spec verify-links and fix missing backlinks before implementation.
