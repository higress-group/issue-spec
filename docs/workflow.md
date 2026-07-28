# issue-spec workflow guide

**English | [简体中文](workflow.zh-CN.md)**

This guide collects the detailed workflow walkthrough, the coordination model,
and the GitHub-mode setup that the [README](../README.md) intentionally keeps
short.

## See it in action

```text
You: /issue-spec:propose add-dark-mode
AI:  Created proposal issue #101
     Added SPEC comments for theme behavior and persistence
     Added QUESTION comments for unresolved UX decisions

Human: answers QUESTION-101001 on Issue #101's page (or in a comment).
AI:    Recorded the ANSWER and updated the relevant SPEC comments.

You: /issue-spec:apply
AI:  Created design issue #102 and implement issue #103
     Split work into PROCESS nodes:
     - PROCESS-103001: theme state and storage
     - PROCESS-103002: UI toggle
     - PROCESS-103003: tests and verification
     Linked SPEC <-> TASK <-> PROCESS

Worker: opens PR #120
AI:     Added PR rationale comments on changed lines, each linked to SPEC and PROCESS.

You: /issue-spec:review
AI:  Synced PR review comments, checks, and findings into REVIEW comments.
     P1 finding assigned to PROCESS-103002.

Worker: fixes the finding
AI:     Replied to the original PR review thread and marked the finding resolved.

You: /issue-spec:verify
AI:  Traceability OK
     Blocking questions: 0
     P0/P1 findings: 0
     PR checks: passing
     Durable spec draft covers all SPEC comments

You: /issue-spec:archive
AI:  After implementation merge, opened a separate durable-spec PR.
```

## Workflow model

Each substantial change uses three issue classes.

| Issue | Purpose | Typed comments |
| --- | --- | --- |
| Proposal | what and why | `SPEC`, `QUESTION` |
| Design | how and acceptance strategy | `TASK`, `QUESTION` |
| Implement | multi-agent execution, review, verify | `PROCESS`, `QUESTION`, `REVIEW`, `VERIFY` |

Traceability is bidirectional:

```text
SPEC <-> TASK <-> PROCESS <-> PR rationale
                   |
                   +-> REVIEW findings and replies
                   +-> VERIFY evidence
```

Before the implementation PR merges, `pr link-issues` writes closing links into
the implementation PR body so the provider closes the PR-associated proposal,
design, and implement issues at merge time. After merge,
`archive durable-spec --create-pr --close-issues` opens a separate PR that
writes the long-lived behavior contract into the repository and idempotently
closes any still-open active issues.

Use `--capability` as a stable capability directory rather than the original
change name. Before finalizing the archive PR, inspect existing related durable
specs and treat the generated durable spec as a draft to merge, revise, or
regroup by durable functional modules while preserving Source SPEC links for
traceability.

## Active change state lives in issues

Active change artifacts are stored in issues instead of repository files:

- proposal issue: proposal body plus `SPEC` and `QUESTION` comments
- design issue: design body plus `TASK` and `QUESTION` comments
- implement issue: implementation DAG plus `PROCESS`, `REVIEW`, and `VERIFY` comments

Issue bodies are the current editable proposal/design/implementation artifacts,
not placeholder shells. Use `--body-file` when creating them and
`issue-spec issue update --body-file --summary` when discussion changes the
body, so humans can review the latest content and the audit trail in the same
issue.

Generated issue titles use the human-readable `Proposal: <subject>`,
`Design: <subject>`, and `Implement: <subject>` family. With `--body-file`, the
subject is derived from the first Markdown H1 when possible, while the change
name remains preserved in the issue marker and metadata. Use
`issue create --title` only for an explicit user-requested custom title.

This keeps the repository focused on current code and durable specs: draft,
superseded, or abandoned change specs never show up in `grep`, code search, or
an agent's later repository reads. Draft change history remains reviewable in
the issue tracker, with comment threads, edits, links, and human approval
points.

Human-in-the-loop decisions are first-class:

- blocking questions are `QUESTION` comments with structured choice models
- each confirmed choice is an immutable, append-only `ANSWER` comment
- accepted assumptions are recorded in issue history
- review findings are PR line comments with owners and linked specs
- verification evidence is stored in `VERIFY` comments

## Native multi-agent DAG coordination

`issue-spec` treats implementation and review as a native multi-agent workflow.
Work is split into small `TASK` and `PROCESS` units, linked back to the
relevant `SPEC` comments, PR work, and review evidence.

The goal is to keep each model invocation inside its effective reasoning zone:
narrow scope, clear context, explicit ownership, focused tests, and small
review surfaces.

The implement issue records the DAG:

- worker owner and review agent owner
- branch/worktree or PR node
- dependencies
- owned files and scope
- linked TASK/SPEC comments
- status, blockers, and verification evidence

For non-trivial changes, the DAG should include dedicated review PROCESS nodes,
not only implementation PROCESS nodes. A coordinator may run multiple review
agents in parallel when their review scopes are independent, such as CLI/API
behavior, workflow documentation, tests, compatibility, or security-sensitive
surfaces. Small changes may be implemented and reviewed by the coordinator
directly, but the implement or verify record should state that the task was
intentionally kept serial.

Coordinator execution follows a ready-node loop:

- select PROCESS nodes whose dependencies are done and whose write/review scopes do not overlap
- dispatch independent worker or review agents in parallel when that reduces context size without creating integration risk
- integrate completed worker outputs by dependency order and add PR rationale for the changed lines
- route P0/P1 review findings back to the owner PROCESS before final verification
- mark review PROCESS nodes done only after their review evidence is recorded and blocking findings are resolved

The CLI does not act as a scheduler that launches agents automatically. It
provides the shared state, links, and gates that let a coordinator safely split
work across multiple agents without losing traceability.

## PR-native review flow

Review and verification connect directly to PR review comments:

- `pr rationale` records why a worker changed a specific PR diff line and links it to a `SPEC` and `PROCESS`
- `review finding` creates actionable PR line findings with severity, owner process, and linked spec context
- `review reply` lets the worker close the original review thread after a fix
- `review sync` summarizes rationale comments, findings, resolved findings, PR checks, and review status back into `REVIEW` comments

This gives humans a better review experience: findings are attached to the
exact code line, while issue comments preserve assignment, workflow state, and
spec context.

Final verification checks unresolved blocking questions, traceability, P0/P1
findings, PR rationale coverage, PR checks, and durable spec coverage before
archive.

## Safe workflow gates and proportional evidence

Use `status --gate proposal|design|implement|final|archive --json` to forecast
the next boundary, `comment transition` with an observed version or digest for
a single safe mutation, and `workflow reconcile --plan ... --checkpoint ...
--json` for resumable dependency-ordered batches. Run `doctor agent
--operation ... --json` before delegated workspace or worker allocation.
PROCESS nodes declare one of five execution classes so change-bearing, review,
verification, orchestration, and external work use truthful evidence carriers
instead of all requiring arbitrary line rationale.

See [Workflow safety, reconciliation, and PROCESS evidence](workflow-safety.md)
for the commands, atomicity boundary, strict credential policy, resume
behavior, and complete evidence matrix.

## Finding related changes

Before proposing a change, search the issue backend for the historical trail
it should build on:

```bash
issue-spec search issues --repo owner/repo --query "schema allowlist" \
  --source change --stage design --state all --limit 10
```

`--source change` groups matches by their related change (self-hosted backend),
and `--stage proposal|design|implement` narrows results to one phase. On the
self-hosted Web UI the same search groups issue and comment matches under each
related change.

## Agent skills and slash commands

`issue-spec init` generates agent workflow artifacts for a project:

```bash
issue-spec init --repo owner/repo --tools codex,claude --delivery both
```

- Skills are written once to `.agents/skills/issue-spec-*`, including the generated `issue-spec-github` support skill for adjacent GitHub CLI operations that issue-spec does not wrap directly.
- When Claude is selected, `.claude/skills` is a relative symlink to `../.agents/skills`, so Codex and Claude consume the same repository skill files.
- Init safely migrates an existing `.claude/skills` directory only when it contains issue-spec-managed skills or byte-identical copies of canonical skills. It stops without replacing user-owned conflicts.
- Claude slash commands are written to `.claude/commands/issue-spec/*.md`, invoked like `/issue-spec:propose`.
- Codex slash prompts are written to `${CODEX_HOME:-~/.codex}/prompts/issue-spec-*.md` for compatibility with Codex custom prompts. Codex custom prompts are deprecated by current Codex docs; prefer skills for shared workflows.
- `--delivery skills` writes only skills; `--delivery commands` writes only slash commands.

If `--tools` is omitted, init detects existing `.agents` or `.claude`
directories and refreshes those workflows. Use `--tools none` to initialize
only `.issue-spec/config.json` and optional labels.

## GitHub mode setup

`issue-spec` runs the same workflow directly against github.com or GitHub
Enterprise. It expects GitHub CLI to be installed and authenticated on the
current machine, and uses the same account and host that `gh auth status`
reports:

```bash
gh auth login
gh auth status
issue-spec auth status --json
```

For GitHub Enterprise, log in with GitHub CLI first, then pass the same host to
issue-spec commands:

```bash
gh auth login --hostname ghe.example.com
issue-spec auth status --hostname ghe.example.com --json
```

`issue-spec auth status`, `init`, and normal workflow commands do not print
token values. `issue-spec auth token --plain` prints the current `gh` token
only when explicitly requested.

`archive durable-spec --create-pr` still uses local `git` for fetch, worktree,
commit, and push. GitHub API reads and PR creation use the same authenticated
`gh` account.

On GitHub, every typed artifact stays readable Markdown. The two self-hosted
review surfaces that GitHub does not execute are the sandboxed `html-preview`
review projections and the interactive QUESTION answer panel; on GitHub their
source remains reviewable as plain Markdown and answers are recorded through
the CLI instead.

## Relationship to OpenSpec

`issue-spec` is inspired by [OpenSpec](https://github.com/Fission-AI/OpenSpec)
and keeps its spec-first authoring habits: proposal -> specs -> design ->
tasks -> review -> verify -> archive, with durable specs archived in the
repository. The main adaptation is where active state lives (issues and typed
comments instead of working files) and how humans review it (rendered review
projections, PR line findings, and structured QUESTION/ANSWER decisions).
