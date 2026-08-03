# 企业代码平台接入

适用于这样的部署：issue-spec Server 管理 Issue 原生的规划工件，企业代码平台
继续管理源码、PR/MR、CI、评审、批准和合并。

目标是形成清晰的人机交接，而不是再实现一套代码平台策略引擎：issue-spec
准备一个精确、可评审的 head 和对应 PR/MR；是否合并由人和代码平台原生界面
决定。

## 权责划分

| 表面 | 权威方 | 接入方式 |
|---|---|---|
| Issue、类型化规划、Change 状态、Runner 命令 | issue-spec Server | 原生 API/OIDC |
| 仓库坐标 | 运维方 | Source Binding |
| clone/push | Git 传输 | 受信 SSH 或 `git-credential-v1` |
| 创建 PR/MR、普通讨论 | 企业代码平台 | 可选 `issue-spec.code-provider/v1` 操作 |
| CI、评审、批准、合并 | 企业代码平台和人 | 原生策略/界面 |
| 工作项投影 | 企业项目平台 | 独立、幂等的适配器 |

各表面只保留一个权威方。尤其不要把代码平台的评审和检查状态复制成
issue-spec 的 readiness gate。

## 最小 provider 能力

代码桥接只支持三个相互独立的操作能力：

- `change.create`：为精确推送的 head 创建 PR/MR；
- `change.comment`：发布普通、非阻塞的评审讨论；
- `evidence.snapshot`：可选的精确 head 审计/导航快照。

可以声明任意已实现子集。缺少 `change.create` 时由人或外部工具创建 PR/MR，
不会禁用规划和实现；缺少 `change.comment` 时返回可手工发布的 rationale
正文；缺少 `evidence.snapshot` 只是不采集快照。

协议不再包含合并权限、provider 策略归一化、主体映射、ready 状态或合并动作。

## 生成桥接脚手架

```bash
python3 .agents/skills/configure-enterprise-provider/scripts/scaffold_provider.py \
  --provider-key code.example \
  --display-name "Example Code" \
  --remote-authority git.example.test \
  --capability change.create \
  --capability change.comment \
  --output "$HOME/.config/issue-spec/providers/code.example"
```

输出包括：

- `provider_bridge.py`：默认不声明能力的严格命令桥；
- `providers.json`：私有运维 registry；
- `implementation-plan.json`：所选操作的实现和激活步骤。

先实现并测试 handler，再把已实现能力同时写入运行时 `CAPABILITIES` 和 registry
的 `description.capabilities`。不要添加评审结论、批准或合并能力。

验证私有配置与握手：

```bash
python3 .agents/skills/configure-enterprise-provider/scripts/validate_provider.py \
  --registry "$HOME/.config/issue-spec/providers/code.example/providers.json" \
  --provider-key code.example
```

validator 不执行任何 provider 写操作。每个声明能力都要在非生产仓库单独完成
正常与异常路径契约测试。

## 私有注册与仓库配置

`providers.json`、可执行路径、token 文件和 API 环境必须位于仓库外。Server 与
需要 provider 操作的 CLI 进程通过 `ISSUE_SPEC_CODE_PROVIDERS_FILE` 或受信的
`operator_registry_file` 使用同一描述。

仓库内只写 provider key：

```yaml
workflow:
  external_code:
    provider_key: code.example
```

Source Binding 只保存无凭据坐标；clone/web URL 的 authority 必须匹配运维方
声明。Runner Git 凭据与 provider API 凭据相互隔离。

先运行 `issue-spec init --plan`。初始化根据实际操作能力生成 provider-aware
Skills，不再把 provider 分类为 planning-only 或 merge-capable。

## 人工评审交接

实际代码作者只为非显然实现返回有价值的行级 rationale 草稿。精确 head 集成
并推送后，Coordinator 验证锚点和敏感信息，再将原文发布为非阻塞行级讨论，
并维护顶层 `### Implementation Rationale` 摘要/索引。

如果平台不安全支持行级评论，就在顶层讨论保留 `path:symbol/line` 和作者原文。
发布失败要显式返回、可重试。rationale 是给人看的上下文，不是证据或批准。

最终交付报告精确 head、PR/MR 链接、测试、rationale、风险和限制，然后停止。
当前 CI、批准、策略和合并都留在代码平台原生界面，由人决定。

## 验收清单

- registry 是严格 JSON、绝对路径、私有且不含 secret 值；
- 运行时能力与运维描述完全一致；
- 每个声明操作都有非生产正常/异常路径测试；
- 创建 PR/MR 绑定预期仓库和精确 head，并具备幂等策略；
- 评论保持相同 change 身份且不制造阻塞评审；
- 可选快照拒绝错误 head 且仅用于审计；
- Source Binding 无凭据，Git 与 API 凭据隔离；
- 流程在人工批准和合并之前停止。

回滚某个操作时，同时从运行时与 registry 删除该 capability。彻底停用时再从
仓库 workflow 删除 provider key，并分别撤销 provider API 与 Runner Git 凭据。
