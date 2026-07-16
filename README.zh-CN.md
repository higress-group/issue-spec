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

## 为团队自托管 issue-spec

在自己的基础设施中运行由团队掌控的 issue-spec 工作台。自托管 Server
将组织与仓库权限、Issue 和 Change 页面、Service Account、Provider-neutral
代码证据、Runner、通知 Webhook 与持久化 PostgreSQL 状态整合在一起。

[![自托管 issue-spec 工作台](docs/self-hosting/assets/self-hosted-dashboard.png)](docs/self-hosting/README.zh-CN.md)

它支持私网部署以及 GitHub OAuth 或 OIDC 登录，同时让源代码、PR/MR、
Review 和 CI 继续留在团队已有的代码托管平台中。

显式启用 PostgreSQL 检索后，Web 工作台和直接连接的 Agent CLI 可以在新改动前
找回相关 Issue 正文、评论与历史 Change 讨论。生成的 Codex/Claude 工作流会直接
使用这一能力；Runner Session 复用相同流程，不再拥有一条独立的检索路径。

**[查看自托管 Server、架构、权限模型、部署与运维详情 →](docs/self-hosting/README.zh-CN.md)**

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

`issue-spec runner` 监听经过授权的 issue 命令评论，并通过 acpx 在受管仓库工作区中
调度 Codex 或 Claude。

```bash
issue-spec runner preflight --repo owner/repo --runner "$(gh api user --jq .login)"
issue-spec runner poll --repo owner/repo --runner "$(gh api user --jq .login)" --agent codex
```

命令接入、权限校验、通知账号、沙箱、并发、工作区、恢复和全部运行参数见
**[Runner 运维指南](docs/runner.zh-CN.md)**。

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

### 安全工作流关卡与分级证据

使用 `status --gate proposal|design|implement|final|archive --json` 预判下一关；使用带已观察 version 或 digest 的 `comment transition` 安全修改单个产物；使用 `workflow reconcile --plan ... --checkpoint ... --json` 执行可恢复、按依赖排序的批处理。在分配 delegated workspace 或 worker 之前，先运行 `doctor agent --operation ... --json`。PROCESS 现在显式声明五种 execution class，让 change-bearing、review、verification、orchestration 与 external 工作分别使用真实的证据载体，而不是一律伪造行级 rationale。

命令、原子性边界、严格凭据策略、恢复行为与完整证据矩阵见 [Workflow safety, reconciliation, and PROCESS evidence](docs/workflow-safety.md)。

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

## 配置与参考

README 聚焦产品介绍和第一次跑通工作流；详细的编写规则与命令契约放在参考文档中：

- **[项目工作流配置](docs/reference.zh-CN.md#项目工作流配置)**
- **[首选自然语言](docs/reference.zh-CN.md#首选自然语言)**
- **[完整 CLI 参考](docs/reference.zh-CN.md#cli-参考)**
- **[Canonical 类型化评论与校验](docs/reference.zh-CN.md#canonical-类型化评论)**

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
