# Comment-triggered runner

**English | [简体中文](runner.zh-CN.md)**

[Back to the project README](../README.md)

`issue-spec runner` can watch repository issue comments and launch a headless acpx coordinator agent when an authorized maintainer comments a command.

Minimal start:

```bash
gh auth login
issue-spec runner poll \
  --repo owner/repo \
  --runner "$(gh api user --jq .login)" \
  --agent codex
```

By default, the runner only accepts command comments from the same GitHub account that `gh` is logged in as. That keeps the default fail-closed: the main runner account is the only account that can trigger `/new`, `/resume`, or `/cancel` unless additional users are explicitly configured. The main runner account also owns status comments, reactions, issue-spec workflow writes, and any PR/issue operations performed by the coordinator.

Make sure that GitHub account watches the repository with issue and PR notifications enabled. A preflight check can verify the local `gh` authentication, repository access, watch state, sandbox prerequisites, acpx, and selected agent:

```bash
issue-spec runner preflight --repo owner/repo --runner "$(gh api user --jq .login)"
```

Codex-backed runner dispatch uses acpx's Codex provider, which spawns `npx -y @agentclientprotocol/codex-acp@^0.0.44` before starting Codex. The runner preflight checks `acpx`, `npm`, and `npx`; hosts without npm registry access should pre-cache the package with `npm cache add @agentclientprotocol/codex-acp@^0.0.44` before starting the runner.

For faster detection of comments written by the main runner account, use a dedicated notification-only GitHub account. GitHub notifications are user-specific and may not produce a new notification for comments authored by the same account that polls notifications. Without a notification-only account, self-authored command comments are still discovered by the lower-frequency repository comments fallback; this conservative default avoids aggressive all-comment polling and reduces the chance of hitting GitHub API limits.

Create a bot or service account, watch the repository with issue and PR notifications enabled, and export a token that can read repository notifications:

```bash
export ISSUE_SPEC_NOTIFICATION_TOKEN=...
issue-spec runner poll \
  --repo owner/repo \
  --runner "$(gh api user --jq .login)" \
  --notification-runner issue-spec-notify-bot \
  --agent codex
```

The notification token is used only for `notifications` polling and notification preflight checks. The main runner account still authorizes commands and performs GitHub writes. Use `--notification-token-env <name>` when the token is stored in a different environment variable.

Supported command comments:

```text
/new <prompt>
/resume <public-session-id> <prompt>
/cancel <public-session-id>
```

`/new` creates a fresh public runner session, clones the target repository into a managed workspace, starts acpx from that workspace, and writes a concise status comment containing the public session id. `/resume` reuses that public session and workspace. Public sessions are repository-scoped and shared by authorized repository maintainers; they are not private user sessions.

Coordinator-human discussion is explicit. The sandboxed coordinator can use the mirrored GitHub auth to ask clarification questions. Blocking workflow decisions should be recorded as `QUESTION` typed comments; lightweight clarification can use ordinary issue timeline comments, for example with `gh issue comment <issue> --repo owner/repo --body-file <file>`. GitHub issue comments are flat timeline comments, not nested replies under a specific issue comment; the coordinator should link the trigger comment or status comment and include the public session id. To continue the same acpx session, an authorized maintainer must create a new command comment:

```text
/resume <public-session-id> <answer or next instruction>
```

Ordinary follow-up comments remain visible on GitHub but are ignored by runner intake. Terminal runner status comments include a `/resume` template with the public session id.

Use a dry run to check configuration and intake without creating GitHub comments, changing runner state, creating workspaces, or dispatching acpx. Dry-run still reads GitHub notifications and comments, so the first run on a busy repository can take noticeably longer than later real poll cycles that persist cursors. Initial repository comment fallback is limited to the last 30 days by default:

```bash
issue-spec runner poll \
  --repo owner/repo \
  --runner "$(gh api user --jq .login)" \
  --once \
  --dry-run \
  --json
```

Useful runner options:

- `--state <path>` stores durable runner state. By default, single-repository runners use `~/.issue-spec/runners/<host>/<owner>/<repo>/<runner>/state.json`; multi-repository runners use a stable shared scope under `~/.issue-spec/runners/<host>/multi/.../<runner>/state.json`. Duplicate command deliveries are controlled by stable command idempotency and the runner's `eyes` reaction ack.
- `--workspace-root <path>` stores managed repository clones. By default, it uses the same runner scope with a `workspaces` directory beside `state.json`. Explicit paths are used as provided.
- `--workspace-retention <duration>` controls when real poll cycles remove expired, non-active managed workspaces. The default is 7 days. Queued, dispatched, running, locked, and interrupted workspaces remain protected.
- `--poll-interval` and `--fallback-interval` control notification polling and lower-frequency repository comment fallback.
- `--fallback-initial-lookback <duration>` limits the first repository comments fallback when no cursor has been stored yet. The default is `720h` (30 days); set it to `0` to scan all historical comments.
- `--max-concurrency <n>` can run independent sessions in parallel. The default is 3; increase it for higher throughput when the runner host has enough CPU, memory, and agent quota. Commands for the same public session are serialized by a workspace/session lock.
- Continuous `runner poll` dispatches ready jobs in a background goroutine by default, so notification/fallback polling stays responsive while acpx jobs run. It still reconciles in-flight work when dispatch is idle, and keeps expired workspace cleanup running while dispatch is busy. `--once` stays synchronous for diagnostics, and `--sync-dispatch` forces continuous polling back to foreground dispatch when direct dispatch errors need to be inspected. `--async-dispatch` is accepted as an explicit default and cannot be combined with `--once` or `--sync-dispatch`.
- `--allowed-user <login>` allows a human maintainer to trigger `/new`, `/resume`, and `/cancel`; repeat it or comma-separate logins. If omitted, only the authenticated runner identity is accepted. Allowed users must still have write-equivalent repository permission.
- `--notification-runner <login>` enables a notification-only polling identity. When set without `--notification-token-env`, the runner reads the token from `ISSUE_SPEC_NOTIFICATION_TOKEN`.
- `--notification-token-env <name>` selects the environment variable that contains the notification-only token. It can be used with or without `--notification-runner`; when the runner login is provided, preflight verifies the token authenticates as that login.
- `--agent codex|claude` selects the coordinator agent through acpx. `--model <name>` passes the configured model/profile to acpx.
- `--gh-config-dir <path>` selects the host GitHub CLI config directory mirrored into the sandbox. By default the runner derives it from the host GitHub CLI environment.
- `--allow-cancel=false` disables `/cancel` intake.

On Linux, runner dispatch uses bubblewrap by default to keep coordinator filesystem writes inside the managed workspace while still allowing network access for GitHub, model, and package operations. Install bubblewrap or set `ISSUE_SPEC_BWRAP_PATH` / `--bwrap-path` when it is not on `PATH`. If bubblewrap is unavailable or unsupported, the runner fails preflight instead of silently running without isolation.

Use `--unsafe-no-sandbox` only as an explicit operator choice:

```bash
issue-spec runner poll --repo owner/repo --runner maintainer --unsafe-no-sandbox
```

Unsafe mode disables the filesystem boundary and records `sandbox_provider=none` and `fs_boundary=disabled` in durable state. Regular issue-spec CLI commands remain cross-platform; the default sandboxed runner dispatch path requires Linux unless unsafe mode is explicitly selected.

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

The acpx-launched coordinator creates or updates proposal, design, typed-comment, review, verify, and archive artifacts by running existing issue-spec CLI commands inside the sandbox. The outer runner owns authorization, concise job lifecycle status comments, workspace isolation, restart reconciliation, cancellation state, and bounded provenance stored in durable runner state.
