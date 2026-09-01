# 人工交接工作流

issue-spec 只有一个交付边界：实现并验证一个精确代码 head，创建或选择对应的
provider 原生 PR/MR，为人工评审解释改动，报告交接信息，然后停止。当前 CI、
评审、批准和合并归人和代码平台所有。

规划工件都是可选的。只在它们能改进真实决策或受管执行时使用；工件状态不是
交付验收。

## 选择最小规划路径

有界的单写作者变更默认只用普通 Issue。只有产品决策、durable contract 变化或
明确协作风险值得时，才增加 Proposal、Design、Implement、SPEC、TASK 或 PROCESS。

子 agent 是执行选择，不是 PROCESS 触发条件。先选择 execution mode，再分配 writer。一旦
选择了 Design 或 TASK，或者用户明确要求独立 worker，Coordinator 在 delegated 和 managed
路径都不修改代码。未选择 managed PROCESS 时，由恰好一个真实的非 Coordinator worker
负责有界实现；选择 managed PROCESS 后，每个会产生代码变更的 work package 都有一个真实的
非 Coordinator owner，不同 package 可以并发。只有以下需要才选择 managed PROCESS：

- 多个代码写作者并发；
- 隔离以保护已有工作；
- 声明式写所有权（跨 PROCESS 重叠仅作 advisory 报告）；
- 可跨会话恢复的交接；
- 按依赖顺序集成。

只读调查和评审子 agent 永远不需要 PROCESS。

Coordinator 直接修改代码只保留给没有选定 Design/TASK、用户也未要求 delegation 的窄
direct-PR fast path。文件数量永远不能选择这个例外。

## 规划、实现与验证

选择规划时依次：保存阶段 Issue 正文；将真正未决事项记录为 QUESTION；按需更新
无状态的人类评审 projection；只创建实际需要的 SPEC/TASK/PROCESS；在实现分支
物化已确认的 durable spec。

managed PROCESS 保留精确 base、所有权、worktree 隔离、DCO、生成器、测试、依赖
顺序和有界 handoff。这些只是执行安全，不是评审或合并门禁。

每个 implementation worker 负责一个 package 的代码、focused tests、精确 result commit、
decisions、risks 和非显然的行级 rationale 草稿。Coordinator 负责 dispatch/wait、检查精确
commit、集成、按风险执行最终验证、校验 anchor 和 provider 发布；worker 不接收 provider 凭据。

运行本次实现所需的测试和项目检查，推送一个精确可评审 head，再通过 provider
的 `change.create`、GitHub 工具或明确的人工降级路径创建/选择 PR/MR。

可选 `evidence.snapshot` 只提供审计和导航上下文。issue-spec 不归一化 provider
当前策略，也不复刻其合并决策。

## 解释非显然代码

每个实际代码写作者只为非显然决策提供零条或多条行级 rationale 草稿，包括仓库
相对路径、稳定 symbol 加 changed-line anchor，以及简洁的 why/tradeoff/risk。
不得包含 secret、raw payload、credential、猜测的 diff position、填充内容或配额。

精确 head 集成并推送后，Coordinator 验证锚点、内容仍适用且无敏感信息，再把
作者原文作为非阻塞 provider 行级讨论发布。无效、过时或敏感的草稿退回作者，
或说明原因后丢弃；不能改写后冒充作者。

顶层普通 `### Implementation Rationale` 讨论包含意图、决策与权衡、边界与风险、
测试结果、精确 head、规划链接和行级 rationale 索引。如果平台不能安全发布行级
评论，就在顶层保留 `path:symbol/line` 和作者原文。发布失败保持可见和可重试。
rationale 是给人看的上下文，不是类型化证据或验收状态。

## 交接并停止

最终报告：

- 精确推送 head；
- PR/MR 链接；
- 测试及结果；
- rationale 发布状态；
- 已知风险、边界和不支持的 provider 操作；
- 本次实际使用的规划工件。

随后在批准和合并之前停止。不要创建 readiness receipt、provider 策略模型、合并
命令或自动的合并后协调。人直接在 provider 界面检查当前 CI、批准、讨论、所有权
和分支策略，并决定是否合并。

## Provider 能力

`issue-spec.code-provider/v1` 只保留三个操作级能力：`change.create`、
`change.comment`、`evidence.snapshot`。任意已实现子集都有效。缺失能力只限制对应
动作，不会把仓库切换为全局 planning-only。

历史 REVIEW、VERIFY、rationale evidence、finalization、receipt、Archive 和 provider
evidence 在支持时仍可审计读取；已退役写命令在 mutation 前返回
`deprecated_workflow`，不参与当前交付。
