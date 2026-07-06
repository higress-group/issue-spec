# issue-spec

**[English](README.md) | 简体中文**

`issue-spec` 是一个以 GitHub issue 为存储载体、采用 OpenSpec 风格工作流的命令行工具，面向 agent 驱动的软件开发。

它保留了 OpenSpec 的习惯：proposal -> specs -> design -> tasks -> review -> verify -> archive，但把「进行中的变更状态」从代码仓库里搬了出来，转而存放在 GitHub issue、类型化评论（typed comments）以及 PR review 线程中。

我们的理念：

```text
-> 沿用 OpenSpec 习惯，状态原生落在 GitHub
-> 进行中的变更放在 issue 里，长期留存的 spec 放在仓库里
-> 人的决策发生在评论线程中，而非隐藏的本地文件里
-> 使用小而聚焦的 agent DAG，而非巨大的一次性实现 prompt
-> 行级 review 发现（finding）会回链到对应的 spec
```

## 实际效果一览

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

## 快速开始

安装 CLI：

```bash
go install github.com/higress-group/issue-spec/cmd/issue-spec@latest
```

在当前机器上通过 GitHub CLI 完成认证。`issue-spec` 会复用该 `gh` 会话来执行 GitHub 操作：

```bash
gh auth login
gh auth status
issue-spec auth status --json
```

初始化一个仓库：

```bash
issue-spec init --repo owner/repo --create-labels --tools codex,claude --delivery both
```

然后就可以在你的 agent 里使用生成的 skills 或 slash 命令风格的工作流：

```text
/issue-spec:propose "your idea"
/issue-spec:apply
/issue-spec:review
/issue-spec:verify
/issue-spec:archive
```

## GitHub 认证

`issue-spec` 要求本机已安装并认证 GitHub CLI。它使用 `gh auth status` 所报告的同一账号与 host：

```bash
gh auth status
issue-spec auth status --json
```

对于 GitHub Enterprise，先用 GitHub CLI 登录，然后把相同的 host 传给 issue-spec 命令：

```bash
gh auth login --hostname ghe.example.com
issue-spec auth status --hostname ghe.example.com --json
```

`issue-spec auth status`、`init` 以及常规工作流命令都不会打印 token 值。只有在显式请求时，`issue-spec auth token --plain` 才会打印当前的 `gh` token。

`archive durable-spec --create-pr` 仍然使用本地 `git` 来进行 fetch、worktree、commit 与 push。GitHub API 的读取和 PR 创建则使用同一个已认证的 `gh` 账号。

## Runner：评论触发的工作流

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

只有作为显式的运维选择时才使用 `--unsafe-no-sandbox`：

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

## 为什么选择 issue-spec

### 进行中的 spec 不留在代码仓库里

OpenSpec 的进行中变更通常是位于 `openspec/changes/<change>/...` 下的仓库文件。这对本地的 spec 驱动开发很好用，但也意味着草稿、被取代或被放弃的变更 spec 会被 `grep`、`rg`、代码搜索，或之后读取仓库的 agent 找到。

`issue-spec` 转而把进行中的变更产物存放在 GitHub issue 里：

- proposal issue：proposal 正文，加上 `SPEC` 与 `QUESTION` 评论
- design issue：design 正文，加上 `TASK` 与 `QUESTION` 评论
- implement issue：实现 DAG，加上 `PROCESS`、`REVIEW` 与 `VERIFY` 评论

issue 正文是当前可编辑的 proposal/design/implementation 产物，而非占位空壳。创建时使用 `--body-file`，当讨论改变了正文时使用 `issue-spec issue update --body-file --summary`，这样人们就能在同一个 GitHub issue 里审阅最新内容与审计轨迹。

生成的 issue 标题使用人类可读的 `Proposal: <subject>`、`Design: <subject>` 与 `Implement: <subject>` 家族。使用 `--body-file` 时，subject 会尽可能从第一个 Markdown H1 派生，同时变更名仍保留在 issue marker 与元数据中。仅在用户明确要求自定义标题时才使用 `issue create --title`。旧的、标题为 `issue-spec proposal: <change>`、`issue-spec design: <change>` 或 `issue-spec implement: <change>` 的 issue 仍是有效的工作流产物，无需重命名。

这让仓库聚焦于当前代码与长期留存的 spec。草稿变更历史仍可在 GitHub 中审阅，包含评论线程、编辑、链接与人工审批点。

人在环（human-in-the-loop）决策是一等公民：

- 阻塞性问题是 `QUESTION` 评论
- 被接受的假设记录在 issue 历史中
- review 发现是带 owner 与关联 spec 的 PR 行评论
- 验证证据存储在 `VERIFY` 评论中

### 原生的多 agent DAG 协调

`issue-spec` 把实现与 review 当作原生的多 agent 工作流来处理。工作被拆分为小的 `TASK` 与 `PROCESS` 单元，并回链到相关的 `SPEC` 评论、PR 工作与 review 证据。

目标是让每次 model 调用都保持在其有效的推理区间内：范围窄、上下文清晰、所有权明确、测试聚焦、review 面小。

implement issue 记录该 DAG：

- worker owner 与 review agent owner
- branch/worktree 或 PR 节点
- 依赖
- 拥有的文件与范围
- 关联的 TASK/SPEC 评论
- 状态、阻塞项与验证证据

对于非平凡的变更，DAG 应包含专门的 review PROCESS 节点，而不仅仅是实现 PROCESS 节点。当各 review 范围相互独立时（例如 CLI/API 行为、工作流文档、测试、兼容性或安全敏感面），协调器可以并行运行多个 review agent。小改动可以由协调器直接实现并 review，但 implement 或 verify 记录应说明该任务是有意保持串行的。

协调器执行遵循一个「就绪节点」循环：

- 选择那些依赖已完成、且写/审范围互不重叠的 PROCESS 节点
- 当能在不制造集成风险的前提下减小上下文时，并行分发相互独立的 worker 或 review agent
- 按依赖顺序集成已完成的 worker 输出，并为改动的行添加 PR rationale
- 在最终验证之前，把 P0/P1 review 发现路由回其 owner PROCESS
- 仅在 review 证据已记录且阻塞性发现已解决后，才把 review PROCESS 节点标记为 done

CLI 不充当自动拉起 agent 的调度器。它提供共享状态、链接与关卡（gate），让协调器能够安全地把工作拆分到多个 agent 之间，而不丢失可追溯性。

### PR 原生的 review 流程

OpenSpec 本就把 review 与 verify 作为工作流阶段来鼓励。`issue-spec` 把这一纪律直接连接到 GitHub PR review 评论：

- `pr rationale` 记录 worker 为何改动某条具体 PR diff 行，并把它链接到某个 `SPEC` 与 `PROCESS`
- `review finding` 创建可操作的 PR 行发现，带严重级别、owner process 与关联的 spec 上下文
- `review reply` 让 worker 在修复后关闭原始 review 线程
- `review sync` 把 rationale 评论、发现、已解决发现、PR 检查与 review 状态汇总回 `REVIEW` 评论

这给了人更好的 review 体验：发现被附在确切的代码行上，而 issue 评论则保留了分配、工作流状态与 spec 上下文。

最终验证会在 archive 之前检查：未解决的阻塞性问题、可追溯性、P0/P1 发现、PR rationale 覆盖、PR 检查以及长期 spec 覆盖。

## 工作流模型

每个实质性的变更使用三种 issue 类别。

| Issue | 用途 | 类型化评论 |
| --- | --- | --- |
| Proposal | 做什么与为什么 | `SPEC`、`QUESTION` |
| Design | 怎么做与验收策略 | `TASK`、`QUESTION` |
| Implement | 多 agent 执行、review、verify | `PROCESS`、`QUESTION`、`REVIEW`、`VERIFY` |

可追溯性是双向的：

```text
SPEC <-> TASK <-> PROCESS <-> PR rationale
                   |
                   +-> REVIEW findings and replies
                   +-> VERIFY evidence
```

在实现 PR 合并之前，`pr link-issues` 会把 GitHub 关闭链接写入实现 PR 正文，这样 GitHub 会在合并时关闭与该 PR 关联的 proposal、design 与 implement issue。合并之后，`archive durable-spec --create-pr --close-issues` 会开一个单独的 PR，把长期行为契约写入仓库，并幂等地关闭任何仍处于打开状态的活跃 issue。

使用 `--capability` 作为稳定的能力（capability）目录，而不是原始变更名。在最终确定 archive PR 之前，检查现有的相关长期 spec，并把生成的长期 spec 当作草稿来合并、修订，或按长期功能模块重新分组，同时保留 Source SPEC 链接以维持可追溯性。

## Agent Skills 与 Slash 命令

`issue-spec init` 可以为一个项目生成 OpenSpec 风格的 agent 工作流产物：

```bash
issue-spec init --repo owner/repo --tools codex,claude --delivery both
```

- Codex skills 写入 `.agents/skills/issue-spec-*`，即当前的 Codex 仓库 skill 位置。
- Claude skills 写入 `.claude/skills/issue-spec-*`。
- 两套 skill 还都包含一个生成的 `.*/skills/issue-spec-github/SKILL.md` 支持 skill，用于处理 issue-spec 未直接封装的相邻 GitHub CLI 操作。
- Claude slash 命令写入 `.claude/commands/issue-spec/*.md`，以 `/issue-spec:propose` 的方式调用。
- Codex slash prompts 写入 `${CODEX_HOME:-~/.codex}/prompts/issue-spec-*.md`，以兼容 Codex 自定义 prompt。当前 Codex 文档已弃用 Codex 自定义 prompt；对于共享工作流，优先使用 skills。
- `--delivery skills` 只写 skills；`--delivery commands` 只写 slash 命令。

若省略 `--tools`，init 会检测已存在的 `.agents` 或 `.claude` 目录并刷新这些工作流。使用 `--tools none` 只初始化 `.issue-spec/config.json` 与可选的标签（labels）。

## 项目工作流配置

项目可以自定义 issue-spec 的工作流指令与模板，而无需把进行中的变更状态搬回仓库的变更目录。

发现顺序：

1. `issue-spec/config.yaml`，项目 schema 位于 `issue-spec/schemas/<schema>/schema.yaml`。
2. 遗留的 `openspec/config.yaml`，schema 位于 `openspec/schemas/<schema>/schema.yaml`，仅当不存在更优先的 issue-spec config 时。
3. 内置的 issue-spec 工作流。

Schema 模板从所选 schema 的 `templates/` 目录解析。模板路径必须是相对路径，不得逃逸出 schema 模板目录，并且在 issue-spec 使用之前必须存在。进行中的 proposal/design/implement 内容、SPEC/TASK/PROCESS/QUESTION/REVIEW/VERIFY 类型化评论、PR rationale 与 review 发现都保留在 GitHub issue 原生存储中。遗留的 OpenSpec 输出（如 `proposal.md`、`specs/**/*.md`、`tasks.md`、`review.md` 与 `verify.md`）被视为存储映射提示，而不是要写入的活跃文件。

在写入产物之前，验证或检查所选工作流：

```bash
issue-spec workflow validate --repo owner/repo --json
issue-spec workflow which --repo owner/repo --json
```

新的长期 spec 默认写入 `issue-spec/specs/<capability>/spec.md`。若 `openspec/specs/<capability>/spec.md` 已存在，archive 可以更新那个遗留的长期 spec，并报告所选的兼容路径。

### 偏好的自然语言

默认情况下，agent 使用英文来撰写生成的产物。要让 issue 正文、类型化评论、design 说明与 rationale 以另一种语言输出，请在 `issue-spec/config.yaml` 中添加一个 `rules.language` 条目。该值会被嵌入到每一个生成的 skill、slash 命令与 prompt 中，作为一条工作流规则，从而让协调器遵循它。

最快的方式是 init 上的 `--language` 标志，它会为你脚手架或合并该条目：

```bash
issue-spec init --repo owner/repo --tools codex,claude --language zh
```

常见的代码（`zh`、`zh-tw`、`en`、`ja`、`ko`）会被展开为一个描述性标签；其他任何值则原样存储。生成的规则会指示 agent 用所选语言撰写自然语言内容，同时把 canonical 结构标记保留为英文（`## Requirement:`、`### Scenario:`、`**WHEN**`/`**THEN**`、MUST/SHALL 以及类型化评论头），这样 canonical 校验仍然能通过。

你也可以手写。请连同 `--language` 会替你写入的 `language_instructions` 护栏一起写——否则 agent 可能把 canonical 结构标记也翻译掉，导致校验失败：

```yaml
# issue-spec/config.yaml
rules:
  language: "Simplified Chinese (简体中文)"
  language_instructions: "Write all natural-language content in Simplified Chinese (简体中文). Keep canonical structural tokens in English so validation passes: the `## Requirement:` and `### Scenario:` headings, the `**WHEN**`/`**THEN**` scenario bullets, the MUST/SHALL normative keywords, and typed comment headers."
```

编辑 config 后重新运行 `issue-spec init`，让生成的 skills 与命令拾取该规则。注意：当 `--language` 合并一个已存在的 `issue-spec/config.yaml` 时，会通过 YAML 往返重写该文件，因此手写的注释会被丢弃、key 会被重新排序。

## CLI 参考

```bash
issue-spec auth status
issue-spec auth login
issue-spec auth logout
issue-spec auth token --plain

issue-spec init --repo owner/repo --create-labels
issue-spec init --repo owner/repo --tools codex,claude --delivery both
issue-spec init --repo owner/repo --tools codex,claude --language zh

issue-spec issue create proposal --repo owner/repo --change my-change --body-file proposal.md [--title "Custom proposal title"]
issue-spec issue create design --repo owner/repo --change my-change --proposal 1 --body-file design.md [--title "Custom design title"]
issue-spec issue create implement --repo owner/repo --change my-change --proposal 1 --design 2 --body-file implement.md [--title "Custom implementation title"]
issue-spec issue update --repo owner/repo --issue 1 --body-file proposal.md --summary "Clarified goals after review."

issue-spec comment generate --type SPEC --id SPEC-001 --status confirmed --scope "canonical SPEC generation" --input-file spec.json
issue-spec comment upsert --repo owner/repo --issue 1 --type SPEC --id SPEC-001 --body-file spec.md
issue-spec comment upsert --repo owner/repo --issue 1 --type SPEC --id SPEC-001 --body-file legacy.md --allow-noncanonical
issue-spec comment list --repo owner/repo --issue 1 --json

issue-spec question create --repo owner/repo --issue 1 --id QUESTION-001 --blocking --question "What must be decided?"
issue-spec question resolve --repo owner/repo --issue 1 --id QUESTION-001 --resolution-file resolution.md

issue-spec link --repo owner/repo --from SPEC-001 --from-issue 1 --to TASK-001 --to-issue 2
issue-spec status --repo owner/repo --proposal 1 --design 2 --implement 3
issue-spec verify-links --repo owner/repo --proposal 1 --design 2 --implement 3

issue-spec workflow validate --repo owner/repo --json
issue-spec workflow which --repo owner/repo --schema custom-workflow --json

issue-spec pr rationale --repo owner/repo --pr 4 --path internal/foo.go --line 42 --process PROCESS-001 --spec SPEC-001 --spec-url https://github.com/owner/repo/issues/1#issuecomment-1 --body "Why this line changes."
issue-spec pr link-process --repo owner/repo --issue 3 --process PROCESS-001 --pr 4
issue-spec pr link-issues --repo owner/repo --pr 4 --proposal 1 --design 2 --implement 3

issue-spec review sync --repo owner/repo --pr 4 --implement 3 --id REVIEW-001
issue-spec review finding --repo owner/repo --pr 4 --path internal/foo.go --line 42 --id FINDING-001 --severity P1 --process PROCESS-001 --spec SPEC-001 --spec-url https://github.com/owner/repo/issues/1#issuecomment-1 --body "What must be fixed."
issue-spec review reply --repo owner/repo --pr 4 --comment-id 123456 --finding FINDING-001 --process PROCESS-001 --status resolved --body "Fixed in the latest patch."

issue-spec verify --repo owner/repo --proposal 1 --design 2 --implement 3 --pr 4 --durable-spec issue-spec/specs/issue-spec-cli/spec.md

issue-spec archive durable-spec --repo owner/repo --proposal 1 --capability issue-spec-cli
issue-spec archive durable-spec --repo owner/repo --proposal 1 --design 2 --implement 3 --pr 4 --capability issue-spec-cli --create-pr --branch issue-spec/durable-spec-issue-spec-cli --close-issues

issue-spec runner preflight --repo owner/repo --runner login
issue-spec runner poll --repo owner/repo --runner login --once --dry-run
issue-spec runner poll --repo owner/repo --runner login --agent codex
```

## Canonical 类型化评论

类型化评论在协调器交接之间承载需求、任务、process 所有权、review 与验证证据。与其手写原始 Markdown，不如使用 `issue-spec comment generate` 从结构化 JSON 渲染 canonical 正文，然后把输出直接管道给 `comment upsert`：

```bash
issue-spec comment generate --type SPEC --id SPEC-001 --status confirmed --scope "canonical SPEC generation" --input-file spec.json \
  | issue-spec comment upsert --repo owner/repo --issue 1 --type SPEC --id SPEC-001 --body-file -
```

`comment generate` 会把一个完整的类型化评论 Markdown 正文（marker + 可见头 + canonical 内容）写到 stdout，且从不触网。同一命令家族用类型专属的 JSON 形态渲染 `TASK`、`PROCESS`、`REVIEW` 与 `VERIFY` 正文。

### SPEC 生成器输入 JSON

```json
{
  "requirement": {
    "title": "canonical SPEC comments",
    "text": "The CLI MUST render canonical SPEC Markdown from structured fields."
  },
  "scenarios": [
    {
      "title": "structured fields render a canonical SPEC body",
      "when": "a caller provides requirement and scenario fields",
      "then": "the CLI renders a body accepted by comment upsert"
    }
  ]
}
```

渲染出的正文包含一个 `## Requirement:` 标题、规范性的 MUST/SHALL 措辞，以及一个或多个带 `**WHEN**`/`**THEN**` 项的 `### Scenario:` 小节。未知的 JSON 字段会被拒绝，因此 schema 漂移会快速失败。

### TASK 与 PROCESS 生成器输入 JSON

TASK 正文承载协调器分解工作所需的 PROCESS 规划元数据。`execution_planning` 对象渲染必需的 `### Execution Planning` 小节：

```json
{
  "title": "canonical typed-comment authoring",
  "summary": "Extend the generators so TASK/PROCESS bodies carry planning metadata.",
  "checklist": ["Add execution_planning fields", "Enforce canonical validation"],
  "covers": ["SPEC-001", "SPEC-006"],
  "execution_planning": {
    "owned_areas": ["internal/templates"],
    "shared_touchpoints": ["internal/model"],
    "dependencies": ["SPEC generator schema"],
    "coupling": "low",
    "execution_mode": "coordinator-owned",
    "complexity": "small"
  }
}
```

PROCESS 正文记录其父 TASK，并且对于串行链，还记录传给下一节点的交接（handoff）证据：

```json
{
  "title": "extend generators",
  "owner": "Worker Agent A",
  "parent_task": "TASK-001",
  "dependencies": ["N/A"],
  "write_ownership": ["internal/templates"],
  "covers": ["TASK-001"],
  "handoff": "state.json contract fixed; successor may parse it"
}
```

被省略的规划字段会渲染为 canonical 默认值（`TBD` / `N/A`），从而让平凡改动保持低摩擦，同时这些小节仍然存在以供协调器阅读。

### 默认的 canonical 校验

`comment upsert` 在创建或更新远端评论之前，默认校验 canonical 纪律：

- **SPEC** —— 拒绝缺少 `## Requirement:` 标题、规范性 MUST/SHALL 措辞，或 `### Scenario:` 的 `**WHEN**`/`**THEN**` 项的正文。
- **TASK** —— 拒绝缺少 `## Task:` 标题或 `### Execution Planning` 小节的正文。
- **PROCESS** —— 拒绝缺少 `## Process:` 标题或 `### Parent TASK` 小节的正文。

串行链的 `### Handoff` 证据在写入时并非必需（只有链才需要它，而这在 upsert 时无法逐评论得知）—— 它改为在 `verify` 时强制。`internal/model` 中的一个共享校验器被 `comment upsert`、`comment list`、`status`、`verify` 与 `archive` 复用，它在剥离 marker/header 后的逻辑正文上运行，因此原始生成正文与已包装正文的行为完全一致。

### 迁移逃生舱

`--allow-noncanonical` 是一个**仅限写入时的迁移旁路**。它让你为分阶段迁移写入一个格式不合规的 SPEC 正文，但它**不会**创建持久的批准：

- 该写入在命令输出中被标记为 `noncanonical`。
- `comment list`、`status`、`verify` 与 archive 就绪性会从远端正文重新计算 canonical 有效性，并持续对格式不合规的活跃评论进行报告或阻塞。
- 只要仍存在格式不合规的活跃 SPEC 评论，`verify` 与 durable-spec archive 就会在 archive 创建之前失败。

正确的长期修复是用 `comment generate` 把评论重新生成为 canonical 形态，或在它不再活跃时将其取代（supersede）。

## 开发

```bash
go test ./...
go build ./cmd/issue-spec
```

### 在本地运行单元测试

本地单元测试要求 [`go.mod`](go.mod) 中声明的 Go 工具链版本（当前为 `go 1.25`），它是所需 Go 版本的唯一真实来源。

在仓库根目录运行：

```bash
go test ./...
```

这与 CI 检查所运行的单元测试命令相同（参见 [`.github/workflows/unit-tests.yml`](.github/workflows/unit-tests.yml)），因此一次干净的本地运行能复现那些为 pull request 及推送到 `main` 把关的检查。

## 致谢

`issue-spec` 受 [OpenSpec](https://github.com/Fission-AI/OpenSpec) 启发，旨在保留其 spec 优先、对 agent 友好的工作流习惯，同时把进行中的变更状态、人工 review 与多 agent 协调适配到 GitHub issue 与 pull request 上。
