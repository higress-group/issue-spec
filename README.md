# issue-spec

**English | [简体中文](README.zh-CN.md)**

`issue-spec` is a GitHub issue-backed, OpenSpec-style workflow CLI for agentic software development.

It keeps the OpenSpec habit of proposal -> specs -> design -> tasks -> review -> verify -> archive, but moves active change state out of the code repository and into GitHub issues, typed comments, and PR review threads.

Our philosophy:

```text
-> OpenSpec habits, GitHub-native state
-> active changes in issues, durable specs in the repo
-> human decisions in comment threads, not hidden local files
-> small agent DAGs, not giant implementation prompts
-> line-level review findings linked back to specs
```

## Self-host issue-spec for your team

Run an operator-controlled issue-spec workspace on your own infrastructure.
The self-hosted server combines organization and repository authorization,
Issue and Change views, service accounts, provider-neutral code evidence,
runners, and notification webhooks with durable PostgreSQL state.

[![Self-hosted issue-spec workspace](docs/self-hosting/assets/self-hosted-dashboard.png)](docs/self-hosting/README.md)

It supports private-network deployment and GitHub OAuth or OIDC sign-in, while
keeping source code, pull requests or merge requests, reviews, and CI on the
code provider your team already uses.

**[Explore the self-hosted server, architecture, access model, deployment, and operations →](docs/self-hosting/README.md)**

**[Connect private code hosts and work trackers safely →](docs/self-hosting/enterprise-provider-integration.md)**

### From transparent change specs to handoff-ready agent execution

Proposal, design, TASK, PROCESS, review, and verification evidence already live
transparently in the issue timeline. Runner takes the next step: an authorized
maintainer triggers an agent with an ordinary issue comment, while status,
public session, results, and continuation instructions stay in that same
timeline.

```text
Alice:  /new Implement the current design, test it, and open a PR
Runner: started · public session s_demo_42
        ...status, PROCESS, and code evidence continue in the issue...
Bob:    /resume s_demo_42 Apply the review decision to error handling
```

A public session belongs to authorized repository maintainers, not to one
person's private conversation. Another maintainer can read the complete issue
context and use `/resume` to take over the same agent session and workspace.
The handoff requires no copied local chat and remains connected to the change
spec, review, and verification trail.

[![Trigger an agent from an issue comment and continue it as another maintainer](docs/self-hosting/assets/self-hosted-runner-command.png)](docs/self-hosting/runner.md)

**[Explore self-hosted Runner setup, comment triggers, multi-maintainer handoff, and operations →](docs/self-hosting/runner.md)**

## See it in action

```text
You: /issue-spec:propose add-dark-mode
AI:  Created proposal issue #101
     Added SPEC comments for theme behavior and persistence
     Added QUESTION comments for unresolved UX decisions

Human: Keep system preference as the default, but allow manual override.
AI:    Resolved QUESTION-001 and updated the relevant SPEC comments.

You: /issue-spec:apply
AI:  Created design issue #102 and implement issue #103
     Split work into PROCESS nodes:
     - PROCESS-001: theme state and storage
     - PROCESS-002: UI toggle
     - PROCESS-003: tests and verification
     Linked SPEC <-> TASK <-> PROCESS

Worker: opens PR #120
AI:     Added PR rationale comments on changed lines, each linked to SPEC and PROCESS.

You: /issue-spec:review
AI:  Synced PR review comments, checks, and findings into REVIEW comments.
     P1 finding assigned to PROCESS-002.

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

## Quick Start

Install the CLI:

```bash
go install github.com/higress-group/issue-spec/cmd/issue-spec@latest
```

Authenticate with GitHub CLI on the current machine. `issue-spec` reuses that `gh` session for GitHub operations:

```bash
gh auth login
gh auth status
issue-spec auth status --json
```

Initialize a repository:

```bash
issue-spec init --repo owner/repo --tools codex,claude --delivery both
```

Initialization ensures the issue-spec workflow labels by default. Use `--skip-labels` only when the repository labels are managed separately.

Then use the generated skills or slash-command style workflows from your agent:

```text
/issue-spec:propose "your idea"
/issue-spec:apply
/issue-spec:review
/issue-spec:verify
/issue-spec:archive
```

## GitHub Authentication

`issue-spec` expects GitHub CLI to be installed and authenticated on the current machine. It uses the same account and host that `gh auth status` reports:

```bash
gh auth status
issue-spec auth status --json
```

For GitHub Enterprise, log in with GitHub CLI first, then pass the same host to issue-spec commands:

```bash
gh auth login --hostname ghe.example.com
issue-spec auth status --hostname ghe.example.com --json
```

`issue-spec auth status`, `init`, and normal workflow commands do not print token values. `issue-spec auth token --plain` prints the current `gh` token only when explicitly requested.

`archive durable-spec --create-pr` still uses local `git` for fetch, worktree, commit, and push. GitHub API reads and PR creation use the same authenticated `gh` account.

## Runner: Comment-Triggered Workflows

`issue-spec runner` watches authorized issue command comments and dispatches
Codex or Claude through acpx in managed repository workspaces. `/new` creates a
public session, and any authorized maintainer with repository write permission
can continue it with `/resume <public-session-id> ...`, enabling agent handoff
across people.

```bash
issue-spec runner preflight --repo owner/repo --runner "$(gh api user --jq .login)"
issue-spec runner poll --repo owner/repo --runner "$(gh api user --jq .login)" --agent codex
```

See the **[GitHub runner operations guide](docs/runner.md)** for command intake,
authorization, notification identities, sandboxing, concurrency, workspaces,
recovery, and all runner options. A self-hosted server uses webhook-driven
`runner serve`; see the **[self-hosted Runner guide](docs/self-hosting/runner.md)**.

## Why issue-spec

### Active specs stay out of the code repository

OpenSpec active changes are usually repository files under `openspec/changes/<change>/...`. That works well for local spec-driven development, but it also means draft, superseded, or abandoned change specs can be found by `grep`, `rg`, code search, or an agent reading the repository later.

`issue-spec` keeps active change artifacts in GitHub issues instead:

- proposal issue: proposal body plus `SPEC` and `QUESTION` comments
- design issue: design body plus `TASK` and `QUESTION` comments
- implement issue: implementation DAG plus `PROCESS`, `REVIEW`, and `VERIFY` comments

Issue bodies are the current editable proposal/design/implementation artifacts, not placeholder shells. Use `--body-file` when creating them and `issue-spec issue update --body-file --summary` when discussion changes the body, so humans can review the latest content and the audit trail in the same GitHub issue.

Generated issue titles use the human-readable `Proposal: <subject>`, `Design: <subject>`, and `Implement: <subject>` family. With `--body-file`, the subject is derived from the first Markdown H1 when possible, while the change name remains preserved in the issue marker and metadata. Use `issue create --title` only for an explicit user-requested custom title. Older issues titled `issue-spec proposal: <change>`, `issue-spec design: <change>`, or `issue-spec implement: <change>` remain valid workflow artifacts and do not need retitling.

This keeps the repository focused on current code and durable specs. Draft change history remains reviewable in GitHub, with comment threads, edits, links, and human approval points.

Human-in-the-loop decisions are first-class:

- blocking questions are `QUESTION` comments
- accepted assumptions are recorded in issue history
- review findings are PR line comments with owners and linked specs
- verification evidence is stored in `VERIFY` comments

### Native multi-agent DAG coordination

`issue-spec` treats implementation and review as a native multi-agent workflow. Work is split into small `TASK` and `PROCESS` units, linked back to the relevant `SPEC` comments, PR work, and review evidence.

The goal is to keep each model invocation inside its effective reasoning zone: narrow scope, clear context, explicit ownership, focused tests, and small review surfaces.

The implement issue records the DAG:

- worker owner and review agent owner
- branch/worktree or PR node
- dependencies
- owned files and scope
- linked TASK/SPEC comments
- status, blockers, and verification evidence

Coding may be done inline by the coordinator or delegated to a worker sub-agent; delegation is the recommended default for context isolation, not a hard requirement. A coordinator-authored inline node uses `workspace_management: independent` and skips prepare/child/complete/integrate, but `independent` remains the general self-managed mode for external or human executors too and does not force those executors into the coordinator checkout. Review is a MUST for any change-bearing work, and it must be performed by a different agent than the code author: the DAG must include dedicated review PROCESS nodes, and a review PROCESS whose reviewer `--agent` name matches a code author of the same SPEC fails the final gate (`process.review.author_conflict`). The coordinator may author code inline and keep a chain serial, but it must not review its own code — route the review through an independent reviewing agent instead. A coordinator may run multiple review agents in parallel when their review scopes are independent, such as CLI/API behavior, workflow documentation, tests, compatibility, or security-sensitive surfaces. Final PR rationale is written by the code author only after independent review and all fixes converge.

Coordinator execution follows a ready-node loop:

- select PROCESS nodes whose dependencies are done and whose write/review scopes do not overlap
- execute coordinator-inline nodes in the integration checkout, or dispatch delegated managed workers when that reduces context size without creating integration risk
- validate and integrate delegated worker outputs by dependency order; collect commit, test, and applicable handoff evidence for every path
- dispatch independent review agents only after the code is reviewable
- route P0/P1 review findings back to the owner PROCESS and converge all fixes
- only after review/fix convergence, have each code author add final PR rationale for its changed lines
- mark review PROCESS nodes done only after their review evidence is recorded and blocking findings are resolved

The CLI does not act as a scheduler that launches agents automatically. It provides the shared state, links, and gates that let a coordinator safely split work across multiple agents without losing traceability.

### PR-native review flow

OpenSpec already encourages review and verification as workflow phases. `issue-spec` connects that discipline directly to GitHub PR review comments:

- `pr rationale` records why a worker changed a specific PR diff line and links it to a `SPEC` and `PROCESS`
- `review finding` creates actionable PR line findings with severity, owner process, and linked spec context
- `review reply` lets the worker close the original review thread after a fix
- `review sync` summarizes rationale comments, findings, resolved findings, PR checks, and review status back into `REVIEW` comments

This gives humans a better review experience: findings are attached to the exact code line, while issue comments preserve assignment, workflow state, and spec context.

Final verification checks unresolved blocking questions, traceability, P0/P1 findings, PR rationale coverage, PR checks, and durable spec coverage before archive.

### Safe workflow gates and proportional evidence

Use `status --gate proposal|design|implement|final|archive --json` to forecast the next boundary, `comment transition` with an observed version or digest for a single safe mutation, and `workflow reconcile --plan ... --checkpoint ... --json` for resumable dependency-ordered batches. Run `doctor agent --operation ... --json` before delegated workspace or worker allocation. PROCESS nodes now declare one of five execution classes so change-bearing, review, verification, orchestration, and external work use truthful evidence carriers instead of all requiring arbitrary line rationale.

Delegated workspaces are selected by an exact PROCESS id and managed with `workflow workspace prepare|inspect|complete|integrate|reconcile|cleanup`. Runner commands address a public session, not a PROCESS: the runner keeps exactly one ACPX coordinator in the session clone, the coordinator selects a ready PROCESS from the typed DAG, and a runtime-native child works in the exact prepared worktree without starting nested ACPX. The coordinator owns the PROCESS lifecycle, validates the child's commit, tests, and handoff, and completes or integrates from the unchanged session clone. After resume or restart the top-level runner recovers only the ACPX/session job; the coordinator inspects or reconciles the exact PROCESS lease. Session-clone retention consults `git worktree list` and fails closed by retaining the clone when runner metadata is dirty or uncertain, a linked worktree exists, or git worktree inspection fails; it does not clean child PROCESS workspaces. Review and verification use detached workflow snapshots with dirty-state rejection, but native children share the coordinator's outer sandbox rather than receiving a separate OS sandbox. Workspace cleanup is an owner-token-authorized destructive command that does not decide integration or retention eligibility for its caller. See the [reference](docs/reference.md#process-workspaces) and [runner guide](docs/runner.md).

See [Workflow safety, reconciliation, and PROCESS evidence](docs/workflow-safety.md) for the commands, atomicity boundary, strict credential policy, resume behavior, and complete evidence matrix.

## Workflow Model

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

Before the implementation PR merges, `pr link-issues` writes GitHub closing links into the implementation PR body so GitHub closes the PR-associated proposal, design, and implement issues at merge time. After merge, `archive durable-spec --create-pr --close-issues` opens a separate PR that writes the long-lived behavior contract into the repository and idempotently closes any still-open active issues.

Use `--capability` as a stable capability directory rather than the original change name. Before finalizing the archive PR, inspect existing related durable specs and treat the generated durable spec as a draft to merge, revise, or regroup by durable functional modules while preserving Source SPEC links for traceability.

## Agent Skills And Slash Commands

`issue-spec init` can generate OpenSpec-style agent workflow artifacts for a project:

```bash
issue-spec init --repo owner/repo --tools codex,claude --delivery both
```

- Codex skills are written to `.agents/skills/issue-spec-*`, the current Codex repo skill location.
- Claude skills are written to `.claude/skills/issue-spec-*`.
- Both skill sets also include a generated `.*/skills/issue-spec-github/SKILL.md` support skill for adjacent GitHub CLI operations that issue-spec does not wrap directly.
- Claude slash commands are written to `.claude/commands/issue-spec/*.md`, invoked like `/issue-spec:propose`.
- User-global Codex prompts are not modified by default. Use `--install-global-prompts` to explicitly install compatibility prompts under `${CODEX_HOME:-~/.codex}/prompts`; use `--global-prompts-dir <dir>` for an isolated destination and `--global-prompts-dry-run` to preview every absolute target path without writing. Codex custom prompts are deprecated by current Codex docs; prefer skills for shared workflows.
- `--delivery skills` writes only skills; `--delivery commands` writes only slash commands.

If `--tools` is omitted, init detects existing `.agents` or `.claude` directories and refreshes those workflows. Use `--tools none` to initialize only `.issue-spec/config.json` and optional labels.

## Configuration and reference

Keep the README focused on the product and first successful workflow. Detailed
authoring and command contracts live in the reference guide:

- **[Project workflow configuration](docs/reference.md#project-workflow-configuration)**
- **[Preferred natural language](docs/reference.md#preferred-natural-language)**
- **[Complete CLI reference](docs/reference.md#cli-reference)**
- **[Canonical typed comments and validation](docs/reference.md#canonical-typed-comments)**

## Development

```bash
go test ./...
go build ./cmd/issue-spec
```

### Running unit tests locally

Local unit tests require the Go toolchain version declared in [`go.mod`](go.mod)
(currently `go 1.25`), which is the source of truth for the required Go version.

From the repository root, run:

```bash
go test ./...
```

This is the same unit test command the CI check runs
(see [`.github/workflows/unit-tests.yml`](.github/workflows/unit-tests.yml)),
so a clean local run reproduces the checks that gate pull requests and pushes to
`main`.

## Acknowledgements

`issue-spec` is inspired by [OpenSpec](https://github.com/Fission-AI/OpenSpec) and is designed to preserve its spec-first, agent-friendly workflow habits while adapting active change state, human review, and multi-agent coordination to GitHub issues and pull requests.
