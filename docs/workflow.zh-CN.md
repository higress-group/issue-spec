# 最小化、绑定代码提供方的工作流

Issue-spec 只有一种交付模型：先选择一个有界的 Issue 契约，在同一个精确代码版本上满足当前提供方检查与提供方评审，以零写入方式查看就绪性，再使用提供方签发的完整权威令牌执行条件合并，最后在提供方确认已合并后协调关闭 Issue。

规划制品是可选的。它们帮助团队决策、拆分和隔离实现，但其状态、链接、回执、理由与历史都不会改变合并就绪性。

## 按工程风险选择契约

- 对无需 Proposal 承载产品与设计风险的有界变更使用普通 Issue；文件数量不是选择标准。
- 需要明确确认需求时使用 Proposal；只有存在设计风险时才增加 Design，只有存在协调、委派或工作区隔离风险时才增加 Implement/TASK/PROCESS。
- 根契约必须且只能是普通 Issue 或 Proposal；Design 与 Implement 只能附着在 Proposal 路径上。

SPEC、QUESTION、TASK 与 PROCESS 评论仍是规范的议题原生规划制品。仓库持久化投影在实现分支的 `issue-spec/specs/**` 中落地已确认行为。durable-spec、DCO、CLA、安全与业务策略都是普通的已配置检查。

## 配置稳定的合并输入

仓库配置选择运维方注册的提供方，并声明稳定的提供方原生检查身份：

```yaml
external_code:
  provider_key: code.example
  merge:
    required_checks:
      - source: provider
        provider: code.example
        key: app:42/context:unit
        owner: app:42
        display_name: unit
```

仅有展示名称不能充当检查身份。旧的 `external_code.evidence` 配置会被拒绝，不会自动映射到新模型。

## 预检一个不可变发布集合

在启用 Runner 分派或合并前，把它作为运维方控制的部署切换步骤：先停止相关写入，并验证 CLI、Server、Runner、生成制品与提供方桥接器属于同一个固定发布集合：

```sh
issue-spec workflow preflight \
  --repo owner/repo \
  --release-set 2.0.0 \
  --server-release 2.0.0 \
  --runner-release 2.0.0 \
  --generated-manifest .agents/skills/issue-spec-workflow/release.json \
  --generated-digest sha256:... \
  --provider-generation minimal-merge-authority/v1 \
  --provider-build code-example@sha256:... \
  --json
```

预检只读并报告 `purpose=operator-controlled-deployment-readiness`。运维方传入刚观测到的 Server/Runner 身份；命令把它们与本地 CLI、生成 manifest、provider generation/build/capabilities、检查 key/owner、规范主体映射来源、固定的 `post-merge-idempotent` 协调模式和 `provider-authority-token` 执行方式比较。它是受信部署切换检查，不是持久化 receipt 或合并权威，合并命令也不消费其输出。真正承载权威的命令会在读取或写入前独立重验 provider generation、build、capabilities、主体映射、精确 subject 和 fresh authority token。

缺少 merge-authority provider 不阻塞 `init`、Proposal、Design 或直接手工开发。Init 会报告 `workflow_readiness.mode=planning-only` 与 `merge_capable=false`，且不生成 provider-authority Skill。运维方必须让 Runner dispatch 保持 quiesced；`merge-check` 和条件合并会独立 fail closed。即使 provider capability 完整，init 时也只报告 `operator-preflight-required`、`provider_authority_capable=true` 和 `merge_capable=false`，因为 init 不会建立主体映射、配置 check identity 或部署就绪性。

## 评审与检查归提供方所有

实际代码写作者负责为自己所改代码中的非显然设计决策产出零条或多条行级 rationale
草稿。direct 路径由实际单写者负责（无论是 coordinator 还是受委派 child）；managed
PROCESS 路径由各 worker 对自己的代码负责。每条有价值的草稿应包含仓库相对路径、
稳定 symbol 加 changed-line anchor，以及简明的 why/tradeoff/risk，且不得包含
secret、raw payload 或 credential。写作者不需要 provider 权限，也不猜测最终 diff
position。显然的代码不产出草稿；没有配额、覆盖率目标或占位评论。

精确 head 集成并推送后，coordinator 校验 worker 提供的 anchor，确认内容仍适用且
无敏感信息，再映射到 changed line，把未经改写的 worker rationale 发布成 provider
原生、非阻塞的行级讨论。无效、过时或敏感的草稿应退回 writer 修正，或在说明原因后
丢弃；禁止 coordinator 自行改写后冒充 worker 作者。
在请求人类评审前，发布或刷新标题为 `### Implementation Rationale` 的普通顶层讨论，
作为变更摘要和行级 rationale 索引；它还说明意图、边界、风险、验证与当前结果、精确
subject/head 及所选 Issue/Proposal/Design 链接。不要求 Implement、TASK、PROCESS
或 SPEC。

如果 provider 不支持非阻塞行级讨论，或者行级讨论会自身成为 unresolved merge
blocker，则不创建阻塞讨论，而在顶层评论中保留 `path:symbol/line` 和 worker 原文。
Coordinator 负责发布，但不能替 worker 重写 rationale。

使用提供方原生讨论界面，不要调用已弃用的 `code-change rationale` 证据命令，
也不要添加机器 marker、rationale ID、类型化 carrier、PROCESS/SPEC 绑定、
证据字段或门禁。如果必需的顶层或行级写入失败且无法使用安全降级，必须报告提供方
错误，保留已渲染正文以供重试或手工发布，并且不能宣告人类评审交接完成。这些评论
及其发布状态只是可变的评审体验；`merge-check` 与条件合并永远不会消费它们。

提供方返回一个策略完整、绑定精确版本的评审快照。独立性通过受信规范主体，与完整的发起人、作者、共同作者和提交者集合比较。当前 changes-requested、未解决的必需会话以及开放的 P0/P1 发现都会阻塞；至少一个有效批准者必须独立。

对于每个已配置检查，提供方只选择一个绑定不透明 key、owner、精确版本及提供方配置代际的当前结论。历史尝试和来自其他 owner 的同名检查仅供审计。只有 `success` 通过。

不存在议题原生评审回退或外部权威代际。无法返回完整评审/检查快照并原子验证其权威令牌的提供方不具备合并能力，必须失败关闭。

## 零写入就绪检查

普通 Issue 路径：

```sh
issue-spec merge-check --repo owner/repo --issue 41 --pr 87 --json
```

可选阶段规划路径：

```sh
issue-spec merge-check --repo owner/repo \
  --proposal 41 --design 42 --implement 43 \
  --change-id change-87 --head <exact-head> --json
```

`merge-check` 执行零写入。它不会运行检查，也不会读取 TASK/PROCESS 生命周期、REVIEW/VERIFY 评论、角色回执、理由、关系覆盖、finalization、Archive 状态或合并前关闭链接。决策与快照摘要只是诊断输出，不能复用为证明。

候选 CLI 的 dogfood 必须保持只读，直到完整精确版本检查集合与提供方原生评审权威都得到证明。

## 条件合并与事后协调

```sh
issue-spec code-change merge --repo owner/repo \
  --proposal 41 --design 42 --implement 43 \
  --change-id change-87 --expected-head <exact-head> --json
```

合并命令会重新采集最新快照、再次执行同一个纯判定，并要求提供方原子验证不透明权威令牌与预期 head。仅有 head CAS 不够，因为评审、策略、发现、会话与检查可以在相同 head 上漂移。普通 GitHub REST 的“先读后写”并不原子；除非运维方桥接器证明完整的提供方原生保护，否则必须保持失败关闭。

新鲜观察到已合并状态后，issue-spec 会幂等协调精确选择的 Issue 集合。协调失败不能撤销代码合并，也不能让合并状态变得含糊；应独立重试这项记账工作。

## 弃用边界

旧的评审同步/完成、验证提交/最终验证、作为证据的理由、仅证据 PROCESS 完成、finalization、关闭验证与 Archive 门禁都会在本地、Issue、关系、证据或提供方发生任何写入前返回 `deprecated_workflow`。历史制品只能通过显式审计读取。上述面向人类的普通提供方讨论不属于退役范围。

升级和回滚都是完整集合切换：停止分派与合并，安装固定的二进制、桥接器、生成制品与配置，通过预检后再恢复。不要运行混合生成制品，也不要把新事实翻译成旧 REVIEW/VERIFY 权威。
