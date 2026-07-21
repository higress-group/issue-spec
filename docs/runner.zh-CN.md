# 评论触发的 Runner

**[English](runner.md) | 简体中文**

[返回项目 README](../README.zh-CN.md)

`issue-spec runner` 可以监听仓库的 issue 评论，当一位被授权的维护者发出命令评论时，启动一个无头（headless）的 acpx 协调 agent。

本页介绍 GitHub 后端的 `runner poll`。如果 Issue 后端是自托管 Server，请使用
`runner serve`，并参见[自托管 Runner 接入指南](self-hosting/runner.zh-CN.md)。

最小启动方式：

```bash
gh auth login
issue-spec runner poll \
  --repo owner/repo \
  --runner "$(gh api user --jq .login)" \
  --agent codex
```

默认情况下，runner 只接受来自「与 `gh` 登录账号相同」的命令评论。这让默认行为保持 fail-closed（默认拒绝）：除非显式配置额外用户，否则只有主 runner 账号能触发 `/new`、`/resume` 或 `/cancel`。主 runner 账号同时拥有状态评论、reaction、issue-spec 工作流写入，以及协调器执行的任何 PR/issue 操作。

请确保该 GitHub 账号已 watch 该仓库，并开启了 issue 和 PR 通知。可以用 preflight 检查来验证本地 `gh` 认证、仓库访问权限、watch 状态、sandbox 前置条件、acpx 以及所选 agent：

```bash
issue-spec runner preflight --repo owner/repo --runner "$(gh api user --jq .login)"
```

Codex 支撑的 runner 分发通过 acpx 的 Codex provider 先启动 ACP adapter，再启动
Codex。实际决定 Codex runtime 及其所广告模型 ID 的是 adapter，而不是 `PATH` 中的
`codex` 二进制。内置的 `@agentclientprotocol/codex-acp@^0.0.44` 应视为兼容性
fallback，而不是运维侧的版本策略：即使主机上的 `codex` CLI 已更新，npm cache 仍可能
保留旧 adapter 及其携带的 Codex。

应为 Runner 的服务用户通过 ACPX agent override 固定已验证的 adapter。例如
[openclaw/acpx#434](https://github.com/openclaw/acpx/issues/434#issuecomment-4946457075)
已验证的较新 adapter 版本是 `1.1.2`：

```json
{
  "agents": {
    "codex": {
      "command": "npx",
      "args": ["-y", "@agentclientprotocol/codex-acp@1.1.2"]
    }
  }
}
```

将该内容保存为 Runner 服务用户的 `~/.acpx/config.json`，并将版本纳入运维配置管理。
runner 只会把选中的 Codex override 复制到每个任务隔离的 ACPX home，因此该固定版本会在
bubblewrap 内生效，而不会携带其他无关的 ACPX 配置。无法访问 npm registry 的主机应预先
缓存精确的固定版本，例如 `npm cache add @agentclientprotocol/codex-acp@1.1.2`。

Runner 会拒绝仓库自带的 `.acpxrc.json`，因为 ACPX 的项目配置优先级高于运行方维护的全局
override。Adapter 命令和固定版本应只配置在 Runner 服务账号下。

在启动 Runner 前，以该服务用户验证 adapter：

```bash
acpx config show
acpx --verbose --timeout 60 --deny-all --format json \
  codex exec 'Reply with exactly OK and do not use tools.'
```

`runner preflight --verify-agent-runtime` 会创建临时空工作区，并通过 Runner 实际配置的沙箱、
隔离 HOME、CODEX_HOME、ACPX override 和 Proxy 环境执行同一个「禁止工具」的 ACP session。
它适合验证部署候选版本；默认 preflight 不会连接模型 runtime。自托管 Runner 若使用宿主 SSH，
preflight 也必须传入 `--allow-host-ssh`，这样 macOS unsafe 模式会复用与 `runner serve`
相同的宿主 HOME，Linux 模式也会校验同一 SSH 目录与 Agent Socket。

当 Runner 任务需要创建提交时，必须成对配置 `--git-author-name` 和
`--git-author-email`。Runner 只在每个受管 clone 内写入 repo-local `user.name` 与
`user.email`，并在保留工作区恢复时同步当前配置。Runner 会在 repo-local config 中记录
自己的托管状态：移除参数后恢复原有的 repo-local 值；若配置后已被其他参与者改写，则保留
被改写的字段，并只恢复仍与 Runner 托管身份一致的字段。宿主的全局或系统 Git 配置仍不会
暴露给 Agent。

若为 runner 配置了 `--model`，该值会传给 ACPX，并优先于复制进来的 Codex 配置中的模型。
必须使用 adapter 实际广告的精确模型 ID（包括可能的 reasoning-effort 后缀），并用同一个
`--model` 值执行上述 smoke test。不能只根据 `codex --version` 推断模型是否可用。

为了更快地检测由主 runner 账号所写的评论，建议使用一个专用的「仅通知」GitHub 账号。GitHub 通知是按用户区分的，对于由「同一个正在轮询通知的账号」所写的评论，可能不会产生新的通知。若没有专用通知账号，自己所写的命令评论仍会被较低频率的仓库评论回退机制发现；这种保守的默认策略避免了激进的全量评论轮询，也降低了触达 GitHub API 限制的概率。

创建一个 bot 或服务账号，watch 该仓库并开启 issue 与 PR 通知，然后导出一个能读取仓库通知的 token：

```bash
export ISSUE_SPEC_NOTIFICATION_TOKEN=...
issue-spec runner poll \
  --repo owner/repo \
  --runner "$(gh api user --jq .login)" \
  --notification-runner issue-spec-notify-bot \
  --agent codex
```

通知 token 仅用于 `notifications` 轮询和通知类 preflight 检查。命令授权与 GitHub 写入仍由主 runner 账号执行。当 token 存放在不同的环境变量中时，使用 `--notification-token-env <name>`。

支持的命令评论：

```text
/new <prompt>
/resume <public-session-id> <prompt>
/cancel <public-session-id>
```

runner 命令语法有意不提供 PROCESS selector。`/new` 与 `/resume` 只定位 public coordinator session。runner 为该 session 只启动一个 ACPX coordinator，并在 new、resume、cancel 与 restart reconcile 的整个生命周期中，让它的 cwd 与主 sandbox workspace 始终保持在受管 session clone。runner 不会启动嵌套 ACPX worker，也不会把 coordinator 重绑定到 PROCESS worktree。

coordinator 从 typed DAG 选择精确的 ready PROCESS，并以稳定的仓库、issue、PROCESS、roots 与 owner token，通过 `issue-spec workflow workspace prepare`、`inspect`、`complete`、`integrate`、`reconcile` 与 `cleanup` 拥有其完整生命周期。runner 模式通过 `ISSUE_SPEC_PROCESS_INTEGRATION_ROOT` 和 `ISSUE_SPEC_PROCESS_WORKSPACE_ROOT` 提供可信的 session-local 默认值；standalone coordinator 显式传入 `--integration-root` 与 `--workspace-root`。`change-bearing` 使用可写的独占分支；`review` 与 `verification` 使用 detached immutable workflow snapshot，dirty 时 fail closed；`orchestration` 不创建 checkout；`external` 使用 mode `none`，完成该 PROCESS 并通过 final gate 需要已消费的 provider-neutral exact-revision evidence。

`prepare` 完成后，coordinator 使用当前 agent runtime 的原生 child/subagent 机制，把精确 worktree 路径作为 cwd，并一并传入 branch、write ownership、PROCESS id、parent TASK 与前序 handoff。child 不是另一个 ACPX session。它共享 coordinator 的 runner 外层 sandbox，因此 issue-spec 不承诺独立的 per-child OS sandbox 或 read-only bind；unsafe 模式不提供文件系统隔离。child 自行生成 result commit、执行 focused tests，并返回有界 handoff evidence。coordinator 校验这些结果后，从未改变的 session clone 执行 `complete` 与 `integrate`，再同步 PROCESS 状态、链接与 handoff。只有在显式完成 integration 或 retention 决策后，它才调用 owner-token cleanup。

resume 或 restart 后，top-level runner 只恢复 ACPX/session job。coordinator 从未改变的 session clone 对精确 PROCESS lease 执行 inspect 或 reconcile，再执行 `complete` 与 `integrate`；缺失、不匹配、dirty 或 needs-reconcile 状态都会阻塞。runner 不拥有、持久化或重试 child PROCESS cleanup。`workflow workspace cleanup` 始终是 owner-token 授权的破坏性命令：它可能删除未集成的 change-bearing 工作，也不会替调用者判断或强制执行 integration/retention eligibility。

`/new` 会创建一个全新的公共 runner 会话，把目标仓库克隆进一个受管理的 workspace，从该 workspace 启动 acpx，并写一条包含公共会话 id 的简洁状态评论。`/resume` 复用该公共会话与 workspace。公共会话是「仓库范围」的，由被授权的仓库维护者共享；它们不是私有的用户会话。

协调器与人的讨论是显式的。常规命令完成结果应放在 coordinator summary 中，并由 Runner 写入状态评论；coordinator 不应额外创建一条讨论评论来重复结果。阻塞性的工作流决策应记录为 `QUESTION` 类型化评论。当轻量澄清、建议或交接确实需要一条独立、面向人的时间线记录时，被沙箱隔离的 coordinator 使用 `issue-spec comment create --repo owner/repo --issue <issue> --body-file <file> --json`。该命令通过当前选择的 issue backend 写入，也支持自托管 REST profile，不依赖 `gh`。类型化工作流证据继续使用 `comment upsert` 与 `comment transition`；普通正文不会被转换成 typed artifact。issue 评论是扁平的时间线评论，而非嵌套在某条评论下的回复；coordinator 应链接触发评论或状态评论，并带上公共会话 id。要继续同一个 acpx 会话，被授权的维护者必须新建一条命令评论：

```text
/resume <public-session-id> <answer or next instruction>
```

普通的后续评论在 GitHub 上依然可见，但会被 runner 的 intake 忽略。终态 runner 状态评论会包含一个带公共会话 id 的 `/resume` 模板。

使用 dry run 来检查配置与 intake，而不会创建 GitHub 评论、改变 runner 状态、创建 workspace 或分发 acpx。dry-run 仍会读取 GitHub 通知与评论，因此在繁忙仓库上的第一次运行可能明显慢于之后会持久化游标（cursor）的真实轮询周期。默认情况下，初始仓库评论回退被限制在最近 30 天内：

```bash
issue-spec runner poll \
  --repo owner/repo \
  --runner "$(gh api user --jq .login)" \
  --once \
  --dry-run \
  --json
```

常用的 runner 选项：

- `--state <path>` 存储持久化的 runner 状态。默认情况下，单仓库 runner 使用 `~/.issue-spec/runners/<host>/<owner>/<repo>/<runner>/state.json`；多仓库 runner 使用一个稳定的共享作用域 `~/.issue-spec/runners/<host>/multi/.../<runner>/state.json`。重复的命令投递由稳定的命令幂等性与 runner 的 `eyes` reaction 确认来控制。
- `--workspace-root <path>` 存储受管理的仓库克隆。默认使用与 `state.json` 相邻的 `workspaces` 目录，位于同一 runner 作用域下。显式路径按给定值使用。
- `--log-dir <path>` 存储私有的持久诊断日志。默认使用 `state.json` 同级的 `logs` 目录，启动时会打印实际路径。排障时先运行 `rg -n '"level":"error"' <日志目录>/errors.ndjson`，再通过 `index.ndjson` 按 delivery、job 或 public session ID 缩小范围。
- `--workspace-retention <duration>` 控制真实轮询周期何时移除过期、非活跃的受管 session clone。默认 7 天。处于 queued、dispatched、running、locked 与 interrupted 状态的 session job 会被保护。删除 clone 前 retention 会调用 `git worktree list`；当 runner metadata 为 dirty 或 uncertain、存在 linked worktree，或 git worktree inspection 失败时都会 fail closed 并保留 clone。它不会清理 child PROCESS workspace。
- `--poll-interval` 与 `--fallback-interval` 分别控制通知轮询与较低频率的仓库评论回退。
- `--fallback-initial-lookback <duration>` 在尚未存储游标时限制首次仓库评论回退的范围。默认 `720h`（30 天）；设为 `0` 可扫描所有历史评论。
- `--max-concurrency <n>` 可以并行运行相互独立的会话。默认 3；当 runner 主机具备足够的 CPU、内存与 agent 配额时，可调高以提升吞吐。同一公共会话的命令会被 workspace/session 锁串行化。
- 持续运行的 `runner poll` 默认在后台 goroutine 中分发就绪任务，从而在 acpx 任务运行时保持通知/回退轮询的响应性。当分发空闲时它仍会对在途工作进行 reconcile，并在分发繁忙时保持过期 workspace 的清理运行。`--once` 保持同步以便诊断；当需要检查直接分发错误时，`--sync-dispatch` 会强制持续轮询回到前台分发。`--async-dispatch` 作为显式默认值被接受，且不能与 `--once` 或 `--sync-dispatch` 组合。
- `--allowed-user <login>` 允许某位人类维护者触发 `/new`、`/resume` 与 `/cancel`；可重复该参数或用逗号分隔多个 login。若省略，则只接受已认证的 runner 身份。被允许的用户仍必须具备等同于 write 的仓库权限。
- `--notification-runner <login>` 启用一个「仅通知」的轮询身份。当设置了它但未设置 `--notification-token-env` 时，runner 从 `ISSUE_SPEC_NOTIFICATION_TOKEN` 读取 token。
- `--notification-token-env <name>` 选择包含「仅通知」token 的环境变量。它可以与 `--notification-runner` 一起使用，也可以单独使用；当提供了 runner login 时，preflight 会校验该 token 认证为该 login。
- `--agent codex|claude|qoder` 通过 acpx 选择协调 agent。`--model <name>` 把所配置的 model/profile 传给 acpx。
- `--gh-config-dir <path>` 选择要镜像进沙箱的宿主 GitHub CLI 配置目录。默认情况下 runner 会从宿主 GitHub CLI 环境推导。
- `--allow-cancel=false` 关闭 `/cancel` intake。

在 Linux 上，runner 分发默认使用 bubblewrap，把 coordinator 的文件系统写入限制在受管 session clone 与该 session 的 PROCESS workspace pool 内，同时仍允许 GitHub、model 与包操作的网络访问。原生 child 共享这一外层边界；bubblewrap 不会为每个 child 创建独立 sandbox。当 bubblewrap 不在 `PATH` 上时，请安装它或设置 `ISSUE_SPEC_BWRAP_PATH` / `--bwrap-path`。若 bubblewrap 不可用或不受支持，runner 会让 preflight 失败，而不是在没有隔离的情况下静默运行。

只有作为显式的运维选择时才使用 `--unsafe-no-sandbox`；macOS 上没有 bubblewrap 时也必须显式指定，不存在从 sandbox 模式自动降级：

```bash
issue-spec runner poll --repo owner/repo --runner maintainer --unsafe-no-sandbox
```

unsafe 模式会关闭文件系统边界，并在持久化状态中记录 `sandbox_provider=none` 与 `fs_boundary=disabled`。常规的 issue-spec CLI 命令仍是跨平台的；默认的沙箱化 runner 分发路径需要 Linux，除非显式选择 unsafe 模式。

对于 Codex 支撑的运行，runner 默认要求 agent 具有 full access，以便协调器能在受管理的 workspace 内运行 issue-spec CLI 命令、shell 命令、测试以及原生子 agent：

```bash
issue-spec runner poll --repo owner/repo --runner maintainer --agent codex --model gpt-5.5[xhigh]
```

对于 Claude Code 支撑的运行，包含 issue-spec 工作流所需的工具：

```bash
issue-spec runner poll \
  --repo owner/repo \
  --runner maintainer \
  --agent claude \
  --claude-allowed-tools Task,Bash
```

runner 只拉起一个 ACPX coordinator；它通过在 session sandbox 内运行现有 issue-spec CLI 命令，创建或更新 proposal、design、类型化评论、review、verify 与 archive 产物。PROCESS 实现由 coordinator 准备 workspace，再交给 runtime-native child，而不是启动嵌套 ACPX session。外层 runner 拥有授权、简洁的 session-job 生命周期状态评论、session 级 workspace 隔离、ACPX/session restart recovery、取消状态，以及存储在持久化 runner 状态中的有界溯源信息；它不拥有 child PROCESS 生命周期或 cleanup。
