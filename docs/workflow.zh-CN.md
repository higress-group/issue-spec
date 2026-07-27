# issue-spec 工作流指南

**[English](workflow.md) | 简体中文**

本指南收录了 [README](../README.zh-CN.md) 有意保持精炼而省略的完整工作流演示、协调模型与 GitHub 模式配置细节。

## 实际效果一览

```text
You: /issue-spec:propose add-dark-mode
AI:  Created proposal issue #101
     Added SPEC comments for theme behavior and persistence
     Added QUESTION comments for unresolved UX decisions

Human: 在 Issue #101 页面上直接回答 QUESTION-101001（或以评论回复）。
AI:    记录 ANSWER 并更新相关 SPEC 评论。

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

在实现 PR 合并之前，`pr link-issues` 会把关闭链接写入实现 PR 正文，这样代码托管平台会在合并时关闭与该 PR 关联的 proposal、design 与 implement issue。合并之后，`archive durable-spec --create-pr --close-issues` 会开一个单独的 PR，把长期行为契约写入仓库，并幂等地关闭任何仍处于打开状态的活跃 issue。

使用 `--capability` 作为稳定的能力（capability）目录，而不是原始变更名。在最终确定 archive PR 之前，检查现有的相关长期 spec，并把生成的长期 spec 当作草稿来合并、修订，或按长期功能模块重新分组，同时保留 Source SPEC 链接以维持可追溯性。

## 进行中的变更状态存放在 issue 里

活跃变更产物存放在 issue 中，而不是仓库文件里：

- proposal issue：proposal 正文，加上 `SPEC` 与 `QUESTION` 评论
- design issue：design 正文，加上 `TASK` 与 `QUESTION` 评论
- implement issue：实现 DAG，加上 `PROCESS`、`REVIEW` 与 `VERIFY` 评论

issue 正文是当前可编辑的 proposal/design/implementation 产物，而非占位空壳。创建时使用 `--body-file`，当讨论改变了正文时使用 `issue-spec issue update --body-file --summary`，这样人们就能在同一个 issue 里审阅最新内容与审计轨迹。

生成的 issue 标题使用人类可读的 `Proposal: <subject>`、`Design: <subject>` 与 `Implement: <subject>` 家族。使用 `--body-file` 时，subject 会尽可能从第一个 Markdown H1 派生，同时变更名仍保留在 issue marker 与元数据中。仅在用户明确要求自定义标题时才使用 `issue create --title`。

这让仓库聚焦于当前代码与长期留存的 spec：草稿、被取代或被放弃的变更 spec 不会出现在 `grep`、代码搜索或 agent 之后的仓库读取中。草稿变更历史仍可在 issue 跟踪器中审阅，包含评论线程、编辑、链接与人工审批点。

人在环（human-in-the-loop）决策是一等公民：

- 阻塞性问题是带结构化选项模型（choice model）的 `QUESTION` 评论
- 每次确认的选择都是一条不可变、只追加的 `ANSWER` 评论
- 被接受的假设记录在 issue 历史中
- review 发现是带 owner 与关联 spec 的 PR 行评论
- 验证证据存储在 `VERIFY` 评论中

## 原生的多 agent DAG 协调

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

## PR 原生的 review 流程

review 与 verify 直接连接到 PR review 评论：

- `pr rationale` 记录 worker 为何改动某条具体 PR diff 行，并把它链接到某个 `SPEC` 与 `PROCESS`
- `review finding` 创建可操作的 PR 行发现，带严重级别、owner process 与关联的 spec 上下文
- `review reply` 让 worker 在修复后关闭原始 review 线程
- `review sync` 把 rationale 评论、发现、已解决发现、PR 检查与 review 状态汇总回 `REVIEW` 评论

这给了人更好的 review 体验：发现被附在确切的代码行上，而 issue 评论则保留了分配、工作流状态与 spec 上下文。

最终验证会在 archive 之前检查：未解决的阻塞性问题、可追溯性、P0/P1 发现、PR rationale 覆盖、PR 检查以及长期 spec 覆盖。

## 安全工作流关卡与分级证据

使用 `status --gate proposal|design|implement|final|archive --json` 预判下一关；使用带已观察 version 或 digest 的 `comment transition` 安全修改单个产物；使用 `workflow reconcile --plan ... --checkpoint ... --json` 执行可恢复、按依赖排序的批处理。在分配 delegated workspace 或 worker 之前，先运行 `doctor agent --operation ... --json`。PROCESS 显式声明五种 execution class，让 change-bearing、review、verification、orchestration 与 external 工作分别使用真实的证据载体，而不是一律伪造行级 rationale。

命令、原子性边界、严格凭据策略、恢复行为与完整证据矩阵见 [Workflow safety, reconciliation, and PROCESS evidence](workflow-safety.md)。

## 查找关联变更

在提出新变更之前，先检索 issue 后端里它应该参考的历史轨迹：

```bash
issue-spec search issues --repo owner/repo --query "schema allowlist" \
  --source change --stage design --state all --limit 10
```

`--source change` 会把命中结果按关联变更分组（自托管后端），`--stage proposal|design|implement` 可把结果收窄到某一阶段。在自托管 Web UI 上，同样的检索会把 issue 与评论命中归组到各自的关联变更下。

## Agent Skills 与 Slash 命令

`issue-spec init` 可以为一个项目生成 agent 工作流产物：

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

## GitHub 模式配置

`issue-spec` 可以直接对着 github.com 或 GitHub Enterprise 运行同一套工作流。它要求本机已安装并认证 GitHub CLI，并使用 `gh auth status` 所报告的同一账号与 host：

```bash
gh auth login
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

在 GitHub 上，所有类型化产物都保持为可读的 Markdown。GitHub 不会执行的只有两个自托管评审界面：沙箱化的 `html-preview` 评审投影，以及交互式 QUESTION 答题面板；在 GitHub 上它们的源码仍可作为普通 Markdown 审阅，答案则改由 CLI 记录。

## 与 OpenSpec 的关系

`issue-spec` 受 [OpenSpec](https://github.com/Fission-AI/OpenSpec) 启发，保留了其 spec 优先的编写习惯：proposal -> specs -> design -> tasks -> review -> verify -> archive，长期 spec 归档在仓库中。主要的适配在于活跃状态的存放位置（issue 与类型化评论，而非工作文件），以及人类的审阅方式（渲染出的评审投影、PR 行级发现、结构化的 QUESTION/ANSWER 决策）。
