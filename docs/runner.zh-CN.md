# 评论触发的 Runner

**[English](runner.md) | 简体中文**

[返回项目 README](../README.zh-CN.md)

`issue-spec runner` 可以监听仓库的 issue 评论，当一位被授权的维护者发出命令评论时，启动一个无头（headless）的 acpx 协调 agent。

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

Codex 支撑的 runner 分发使用 acpx 的 Codex provider，它会在启动 Codex 之前先拉起 `npx -y @agentclientprotocol/codex-acp@^0.0.44`。runner preflight 会检查 `acpx`、`npm` 和 `npx`；无法访问 npm registry 的主机应在启动 runner 前用 `npm cache add @agentclientprotocol/codex-acp@^0.0.44` 预先缓存该包。

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

若要把一次 resume 精确绑定到一个 PROCESS workspace，必须把 `--process` 放在 public session id 后面：

```text
/resume <public-session-id> --process PROCESS-008 <prompt>
```

runner 不会从 prompt 文本推断 PROCESS，`/new` 会拒绝 `--process`，且 job 的持久化精确 assignment 不可变。对应的 standalone 生命周期命令为 `issue-spec workflow workspace prepare`、`inspect`、`complete`、`integrate`、`reconcile` 与 `cleanup`；这些命令必须复用精确的仓库、issue、PROCESS、roots 与 owner token。

acpx 启动前，change-bearing 使用其可写的 assigned worktree；review 与 verification 使用 detached snapshot，dirty 时 fail closed；orchestration 不创建 checkout；external 执行必须通过已配置 provider adapter 的 readiness gate。Linux bubblewrap 会把 review/verification snapshot 以只读方式绑定，但 unsafe 模式不提供 OS 级不可变性。重启会先 reconcile 同一 assignment。runner 的终态、取消与 reconcile cleanup 会持久化 intent、校验 integration/retention eligibility，并重试 pending cleanup。这个保护不覆盖手动调用的 standalone `workflow workspace cleanup`：该 owner-token 授权的破坏性命令可能删除未集成的 change-bearing 工作，只能在完成预期的集成或保留决策后使用。

`/new` 会创建一个全新的公共 runner 会话，把目标仓库克隆进一个受管理的 workspace，从该 workspace 启动 acpx，并写一条包含公共会话 id 的简洁状态评论。`/resume` 复用该公共会话与 workspace。公共会话是「仓库范围」的，由被授权的仓库维护者共享；它们不是私有的用户会话。

协调器与人的讨论是显式的。被沙箱隔离的协调器可以使用镜像进来的 GitHub 认证来提出澄清问题。阻塞性的工作流决策应记录为 `QUESTION` 类型化评论；轻量的澄清可以使用普通的 issue 时间线评论，例如 `gh issue comment <issue> --repo owner/repo --body-file <file>`。GitHub issue 评论是扁平的时间线评论，而非嵌套在某条 issue 评论下的回复；协调器应链接触发评论或状态评论，并带上公共会话 id。要继续同一个 acpx 会话，被授权的维护者必须新建一条命令评论：

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
- `--workspace-retention <duration>` 控制真实轮询周期何时移除过期的、非活跃的受管理 workspace。默认 7 天。处于 queued、dispatched、running、locked 与 interrupted 状态的 workspace 会被保护。
- `--poll-interval` 与 `--fallback-interval` 分别控制通知轮询与较低频率的仓库评论回退。
- `--fallback-initial-lookback <duration>` 在尚未存储游标时限制首次仓库评论回退的范围。默认 `720h`（30 天）；设为 `0` 可扫描所有历史评论。
- `--max-concurrency <n>` 可以并行运行相互独立的会话。默认 3；当 runner 主机具备足够的 CPU、内存与 agent 配额时，可调高以提升吞吐。同一公共会话的命令会被 workspace/session 锁串行化。
- 持续运行的 `runner poll` 默认在后台 goroutine 中分发就绪任务，从而在 acpx 任务运行时保持通知/回退轮询的响应性。当分发空闲时它仍会对在途工作进行 reconcile，并在分发繁忙时保持过期 workspace 的清理运行。`--once` 保持同步以便诊断；当需要检查直接分发错误时，`--sync-dispatch` 会强制持续轮询回到前台分发。`--async-dispatch` 作为显式默认值被接受，且不能与 `--once` 或 `--sync-dispatch` 组合。
- `--allowed-user <login>` 允许某位人类维护者触发 `/new`、`/resume` 与 `/cancel`；可重复该参数或用逗号分隔多个 login。若省略，则只接受已认证的 runner 身份。被允许的用户仍必须具备等同于 write 的仓库权限。
- `--notification-runner <login>` 启用一个「仅通知」的轮询身份。当设置了它但未设置 `--notification-token-env` 时，runner 从 `ISSUE_SPEC_NOTIFICATION_TOKEN` 读取 token。
- `--notification-token-env <name>` 选择包含「仅通知」token 的环境变量。它可以与 `--notification-runner` 一起使用，也可以单独使用；当提供了 runner login 时，preflight 会校验该 token 认证为该 login。
- `--agent codex|claude` 通过 acpx 选择协调 agent。`--model <name>` 把所配置的 model/profile 传给 acpx。
- `--gh-config-dir <path>` 选择要镜像进沙箱的宿主 GitHub CLI 配置目录。默认情况下 runner 会从宿主 GitHub CLI 环境推导。
- `--allow-cancel=false` 关闭 `/cancel` intake。

在 Linux 上，runner 分发默认使用 bubblewrap，把协调器的文件系统写入限制在受管理的 workspace 内，同时仍允许 GitHub、model 与包操作的网络访问。当 bubblewrap 不在 `PATH` 上时，请安装它或设置 `ISSUE_SPEC_BWRAP_PATH` / `--bwrap-path`。若 bubblewrap 不可用或不受支持，runner 会让 preflight 失败，而不是在没有隔离的情况下静默运行。

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

由 acpx 拉起的协调器通过在沙箱内运行现有的 issue-spec CLI 命令，来创建或更新 proposal、design、类型化评论、review、verify 与 archive 产物。外层 runner 拥有授权、简洁的任务生命周期状态评论、workspace 隔离、重启 reconcile、取消状态，以及存储在持久化 runner 状态中的有界溯源信息。
