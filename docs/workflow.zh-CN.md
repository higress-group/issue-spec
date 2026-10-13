# 规划、实施与人工交接

当前 agent 可以在同一会话完成授权的规划、实施和自测。Design、TASK、PROCESS
不要求另一个代码作者。项目可以通过自己的补充规则定义专职 coordinator 和阶段
评审；这不是 issue-spec 的默认角色。分派、发布、推送、批准和合并仍取决于实际
用户授权与宿主能力。

## 只选择有用的规划

有界变更可使用普通 Issue。产品决策、长期契约或明确执行风险需要时，再选择
Proposal、Design、Implement、SPEC、TASK 或 PROCESS。文件数和 agent 数不是语义
拆分边界。受管 PROCESS 用于并发写入、保护已有工作的隔离、路径所有权、可恢复
交接或按依赖集成。

选中阶段依次：保存正文，发现真正阻塞的 QUESTION，启用时生成评审 projection，
再编写选定的类型化子产物。普通 QUESTION 使用 resolution，带 Choice Model 的
问题使用 ANSWER。不要为已经确定的私有实现细节制造问题或选项。

## 读取、实施与验证

先读完整当前 Design 和适用的有效决策，再从紧凑索引展开选中的 TASK、必需的
SPEC 和依赖。摘要不能替代规范正文。查找相关普通讨论可用
`search issues --scope discussion`；默认 Proposal 搜索继续可用。

owner 在分配的 checkout 实施、自测、维护长期规格并报告实际候选。受管执行仍
保留精确 base、所有权、worktree 隔离、DCO、生成器、测试、依赖顺序和有界交接。
这些执行控制不构成交付验收。

按变更行为和明确项目关卡选择检查；只有新改动、失败或具体未解风险才扩大。
证据绑定实际验证版本，修复后重跑受影响检查，自测不代表独立评审。

## 项目补充与评审

通用正文生成在 `.agents/skills`，Claude 逐技能链接到同一目录。读取
`issue-spec/config.yaml` 的 context/rules，并按需加载 `guidance.shared` 与当前
`propose`、`apply` 或 `review` 补充。项目维护这些文件和专用技能，详见
[配置与安全升级](reference.zh-CN.md)。

按用户请求的里程碑评审。项目可以由 coordinator 集中评审多个变更，也可以明确
委派评审；issue-spec 不自动另起 reviewer 或要求 PR/MR。检查实际候选与已有
证据，指出具体失败路径，将修复交还对应 owner，重查受影响行为。

## 授权发布与交接

报告 base/candidate、结果、检查、缺口、风险和待评审事项。仅在获得授权且平台
支持时创建代码变更或发布评论。按需发布的实现说明可使用普通
`### Implementation Rationale` 评论和稳定的 `path:symbol/line` 锚点，解释非显然
决策、保留实际作者身份，并针对已发布版本验证锚点。没有理由配额或强制发布
仪式；写入失败时保留正文并报告。

批准和合并遵循实际授权及当前平台策略，不创建 readiness receipt 或 agent 自行
签发的合并权限。

## Provider 操作与历史数据

`issue-spec.code-provider/v1` 提供操作级 `change.create`、`change.comment`、
`evidence.snapshot`。任意已实现子集都有效，能力缺失只限制对应操作。snapshot
是审计或导航上下文，不复刻平台合并策略。

历史 REVIEW、VERIFY、rationale evidence、finalization、receipt 和 Archive 仅供
显式审计读取。退役写入口在变更前返回 `deprecated_workflow`，不参与当前交付。
