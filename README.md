# issue-spec

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
issue-spec init --repo owner/repo --create-labels --tools codex,claude --delivery both
```

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

`issue-spec runner` can watch repository issue comments and launch a headless acpx coordinator agent when an authorized maintainer comments a command.

Supported command comments:

```text
/new <prompt>
/resume <public-session-id> <prompt>
/cancel <public-session-id>
```

`/new` creates a fresh public runner session, clones the target repository into a managed workspace, starts acpx from that workspace, and writes a status comment containing the public session id. `/resume` reuses that public session and workspace. Public sessions are repository-scoped and shared by authorized repository maintainers; they are not private user sessions.

Before running the poller, authenticate GitHub and make sure the runner identity watches the repository with issue and PR notifications enabled:

```bash
gh auth login
issue-spec auth status --json
issue-spec runner preflight --repo owner/repo --runner "$(gh api user --jq .login)"
```

Use a dry run to check configuration and intake without creating GitHub comments, changing runner state, creating workspaces, or dispatching acpx:

```bash
issue-spec runner poll \
  --repo owner/repo \
  --runner "$(gh api user --jq .login)" \
  --once \
  --dry-run \
  --json
```

Start a real poller:

```bash
issue-spec runner poll \
  --repo owner/repo \
  --runner "$(gh api user --jq .login)" \
  --agent codex
```

Useful runner options:

- `--state <path>` stores durable runner state. By default, single-repository runners use `~/.issue-spec/runners/<host>/<owner>/<repo>/<runner>/state.json`; multi-repository runners use a stable shared scope under `~/.issue-spec/runners/<host>/multi/.../<runner>/state.json`.
- `--workspace-root <path>` stores managed repository clones. By default, it uses the same runner scope with a `workspaces` directory beside `state.json`. Explicit paths are used as provided.
- `--poll-interval` and `--fallback-interval` control notification polling and lower-frequency repository comment fallback.
- `--max-concurrency <n>` can run independent sessions in parallel. Commands for the same public session are serialized by a workspace/session lock.
- `--agent codex|claude` selects the coordinator agent through acpx. `--model <name>` passes the configured model/profile to acpx.
- `--gh-config-dir <path>` selects the host GitHub CLI config directory mirrored into the sandbox. By default the runner derives it from the host GitHub CLI environment.
- `--allow-cancel=false` disables `/cancel` intake.

On Linux, runner dispatch uses bubblewrap by default to keep coordinator filesystem writes inside the managed workspace while still allowing network access for GitHub, model, and package operations. Install bubblewrap or set `ISSUE_SPEC_BWRAP_PATH` / `--bwrap-path` when it is not on `PATH`. If bubblewrap is unavailable or unsupported, the runner fails preflight instead of silently running without isolation.

Use `--unsafe-no-sandbox` only as an explicit operator choice:

```bash
issue-spec runner poll --repo owner/repo --runner maintainer --unsafe-no-sandbox
```

Unsafe mode disables the filesystem boundary and marks status comments and durable state with `sandbox_provider=none` and `fs_boundary=disabled`. Regular issue-spec CLI commands remain cross-platform; the default sandboxed runner dispatch path requires Linux unless unsafe mode is explicitly selected.

For Codex-backed runs, the runner defaults to requiring agent full access so the coordinator can run issue-spec CLI commands, shell commands, tests, and native subagents inside the managed workspace:

```bash
issue-spec runner poll --repo owner/repo --runner maintainer --agent codex --model gpt-5.5[xhigh]
```

For Claude Code-backed runs, include the tools needed by the issue-spec workflow:

```bash
issue-spec runner poll \
  --repo owner/repo \
  --runner maintainer \
  --agent claude \
  --claude-allowed-tools Task,Bash
```

The acpx-launched coordinator creates or updates proposal, design, typed-comment, review, verify, and archive artifacts by running existing issue-spec CLI commands inside the sandbox. The outer runner owns authorization, job lifecycle status comments, workspace isolation, restart reconciliation, cancellation state, and bounded provenance writeback.

## Why issue-spec

### Active specs stay out of the code repository

OpenSpec active changes are usually repository files under `openspec/changes/<change>/...`. That works well for local spec-driven development, but it also means draft, superseded, or abandoned change specs can be found by `grep`, `rg`, code search, or an agent reading the repository later.

`issue-spec` keeps active change artifacts in GitHub issues instead:

- proposal issue: proposal body plus `SPEC` and `QUESTION` comments
- design issue: design body plus `TASK` and `QUESTION` comments
- implement issue: implementation DAG plus `PROCESS`, `REVIEW`, and `VERIFY` comments

Issue bodies are the current editable proposal/design/implementation artifacts, not placeholder shells. Use `--body-file` when creating them and `issue-spec issue update --body-file --summary` when discussion changes the body, so humans can review the latest content and the audit trail in the same GitHub issue.

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

For non-trivial changes, the DAG should include dedicated review PROCESS nodes, not only implementation PROCESS nodes. A coordinator may run multiple review agents in parallel when their review scopes are independent, such as CLI/API behavior, workflow documentation, tests, compatibility, or security-sensitive surfaces. Small changes may be implemented and reviewed by the coordinator directly, but the implement or verify record should state that the task was intentionally kept serial.

Coordinator execution follows a ready-node loop:

- select PROCESS nodes whose dependencies are done and whose write/review scopes do not overlap
- dispatch independent worker or review agents in parallel when that reduces context size without creating integration risk
- integrate completed worker outputs by dependency order and add PR rationale for the changed lines
- route P0/P1 review findings back to the owner PROCESS before final verification
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

After the implementation PR merges, `archive durable-spec --create-pr` opens a separate PR that writes the long-lived behavior contract into the repository.

## Agent Skills And Slash Commands

`issue-spec init` can generate OpenSpec-style agent workflow artifacts for a project:

```bash
issue-spec init --repo owner/repo --tools codex,claude --delivery both
```

- Codex skills are written to `.agents/skills/issue-spec-*`, the current Codex repo skill location.
- Claude skills are written to `.claude/skills/issue-spec-*`.
- Both skill sets also include a generated `.*/skills/issue-spec-github/SKILL.md` support skill for adjacent GitHub CLI operations that issue-spec does not wrap directly.
- Claude slash commands are written to `.claude/commands/issue-spec/*.md`, invoked like `/issue-spec:propose`.
- Codex slash prompts are written to `${CODEX_HOME:-~/.codex}/prompts/issue-spec-*.md` for compatibility with Codex custom prompts. Codex custom prompts are deprecated by current Codex docs; prefer skills for shared workflows.
- `--delivery skills` writes only skills; `--delivery commands` writes only slash commands.

If `--tools` is omitted, init detects existing `.agents` or `.claude` directories and refreshes those workflows. Use `--tools none` to initialize only `.issue-spec/config.json` and optional labels.

## CLI Reference

```bash
issue-spec auth status
issue-spec auth login
issue-spec auth logout
issue-spec auth token --plain

issue-spec init --repo owner/repo --create-labels
issue-spec init --repo owner/repo --tools codex,claude --delivery both

issue-spec issue create proposal --repo owner/repo --change my-change --body-file proposal.md
issue-spec issue create design --repo owner/repo --change my-change --proposal 1 --body-file design.md
issue-spec issue create implement --repo owner/repo --change my-change --proposal 1 --design 2 --body-file implement.md
issue-spec issue update --repo owner/repo --issue 1 --body-file proposal.md --summary "Clarified goals after review."

issue-spec comment upsert --repo owner/repo --issue 1 --type SPEC --id SPEC-001 --body-file spec.md
issue-spec comment list --repo owner/repo --issue 1 --json

issue-spec question create --repo owner/repo --issue 1 --id QUESTION-001 --blocking --question "What must be decided?"
issue-spec question resolve --repo owner/repo --issue 1 --id QUESTION-001 --resolution-file resolution.md

issue-spec link --repo owner/repo --from SPEC-001 --from-issue 1 --to TASK-001 --to-issue 2
issue-spec status --repo owner/repo --proposal 1 --design 2 --implement 3
issue-spec verify-links --repo owner/repo --proposal 1 --design 2 --implement 3

issue-spec pr rationale --repo owner/repo --pr 4 --path internal/foo.go --line 42 --process PROCESS-001 --spec SPEC-001 --spec-url https://github.com/owner/repo/issues/1#issuecomment-1 --body "Why this line changes."
issue-spec pr link-process --repo owner/repo --issue 3 --process PROCESS-001 --pr 4

issue-spec review sync --repo owner/repo --pr 4 --implement 3 --id REVIEW-001
issue-spec review finding --repo owner/repo --pr 4 --path internal/foo.go --line 42 --id FINDING-001 --severity P1 --process PROCESS-001 --spec SPEC-001 --spec-url https://github.com/owner/repo/issues/1#issuecomment-1 --body "What must be fixed."
issue-spec review reply --repo owner/repo --pr 4 --comment-id 123456 --finding FINDING-001 --process PROCESS-001 --status resolved --body "Fixed in the latest patch."

issue-spec verify --repo owner/repo --proposal 1 --design 2 --implement 3 --pr 4 --durable-spec issue-spec/specs/issue-spec-cli/spec.md

issue-spec archive durable-spec --repo owner/repo --proposal 1 --capability issue-spec-cli
issue-spec archive durable-spec --repo owner/repo --proposal 1 --capability issue-spec-cli --create-pr --branch issue-spec/durable-spec-issue-spec-cli

issue-spec runner preflight --repo owner/repo --runner login
issue-spec runner poll --repo owner/repo --runner login --once --dry-run
issue-spec runner poll --repo owner/repo --runner login --agent codex
```

## Development

```bash
go test ./...
go build ./cmd/issue-spec
```

## Acknowledgements

`issue-spec` is inspired by [OpenSpec](https://github.com/Fission-AI/OpenSpec) and is designed to preserve its spec-first, agent-friendly workflow habits while adapting active change state, human review, and multi-agent coordination to GitHub issues and pull requests.
