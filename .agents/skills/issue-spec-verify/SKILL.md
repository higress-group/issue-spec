---
name: issue-spec-verify
description: Run final issue-spec verification across exact-current review, test, check, rationale, and traceability evidence.
license: MIT
compatibility: Requires issue-spec CLI.
metadata:
  author: issue-spec
  version: "1.0"
  generatedBy: "issue-spec"
---

# Issue Spec Verify

Coordinator: use issue-spec-workflow for final routing. In repository durable mode, materialize the projection on the implementation branch before dispatch and seal the built-in issue-spec/durable-spec check into the verification assignment. Forecast with status --gate final --summary --json, resolve its detail actions, then run authoritative issue-spec verify --summary --json and full --json before merge. Change-bearing nodes require backend-appropriate rationale and REVIEW completion evidence. Status forecast and final verify use the same authoritative validator. The validator owns exact identity, revision, freshness, and legacy compatibility.

## Verification Role Packet

1. Accept only the sealed verification assignment for the exact immutable subject revision, affected scenarios, required test commands/check selectors, and result schema. Do not load proposal/Design bodies, the complete DAG, link matrices, post-merge policy, or provider routing.
2. Inspect only the sealed exact detached subject. Outside it, write only `{"summary":"..."}`, then run `issue-spec role complete --assignment-file <sealed-packet.json> --decision-file <decision.json> --output <receipt.json> --agent <verifier-name> --json` from the snapshot. Do not collect or pass runtime session IDs.
3. The command runs every sealed test and derives check selectors without claiming provider outcomes, then atomically self-validates v1 evidence. Missing, failed, stale, or substituted evidence blocks output.
4. Return only the bounded result and verification summary. Failed/pending provider checks remain Coordinator acceptance blockers; verification never creates REVIEW, links, provider evidence, or acceptance state.

## Project Workflow

- Workflow Source: `builtin`
- Workflow Schema: `issue-spec`
- Workflow Config: `issue-spec/config.yaml`

Project workflow templates are declarative only. Active proposal, design, implement, SPEC, TASK, PROCESS, QUESTION, REVIEW, and VERIFY artifacts remain in the selected issue backend's issue-native storage; repository-mode durable specs are materialized and checked on the implementation branch.
