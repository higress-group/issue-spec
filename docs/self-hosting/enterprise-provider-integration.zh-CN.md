# 对接企业代码平台与工作项平台

自托管 issue-spec 可以把 Change Spec 的流程状态保存在 issue-spec Server，源码、
合并请求、Review 与 CI 则继续留在企业已有的代码平台。工作项平台可以关联或投影同一
个变更，但不应在无意间变成第二套事实源。

本文介绍 Provider 选型、运维配置、代码平台 Wrapper 实现、Source Binding、Runner
Git 凭据、OIDC，以及 Jira 类工作项同步。所有示例均使用虚构地址。

## 1. 先拆分集成边界

不要实现一个同时掌管所有权限的“企业 Provider”。

| 能力面 | 推荐事实源 | 对接方式 |
|---|---|---|
| Issue、类型化评论、Change 视图 | issue-spec Server | Native API 与 self-hosted CLI Profile |
| 源码、PR/MR、Review、CI、Merge | 企业代码平台 | `issue-spec.code-provider/v1` Bridge |
| Clone 与 Push 凭据 | 企业 Git 服务 | `issue-spec-git-credential-v1` 或可信宿主 SSH |
| 员工登录 | 企业身份平台 | OIDC |
| 需求/工作项可见性 | Jira 类平台 | API/Webhook 投影 Sidecar |

```text
浏览器 ------ OIDC ------> issue-spec Server <------ Runner Webhook
                               ^                         |
                               | Native API              | 固定 Source Binding
工作项投影 Sidecar ------------+                         v
                                                 Git Credential Bridge
issue-spec Server / CLI -- Code Provider Bridge --> 企业代码平台
```

当前稳定的运维侧 Command 协议覆盖的是**代码平台 Provider**，还没有一个可通过
命令插件替换 IssueBackend 的通用 Jira Provider ABI。设计工作项集成前请先阅读
[对接 Jira 类工作项平台](#6-对接-jira-类工作项平台)。

## 2. 盘点企业平台能力

实现 Wrapper 前先明确：

- 稳定的仓库标识，以及不含凭据的规范 Clone URL 与 Web URL；
- PR/MR 标识是全局 ID，还是仓库内 IID；
- 不可变 Head Revision 与 Merge Revision 字段；
- Review、Discussion、Pipeline、Job 与 Merge API；
- Webhook 签名、Delivery ID、重试与事件顺序语义；
- Service Account 权限，以及能否签发短期 Git 凭据；
- API 限流、分页、最终一致性与幂等能力；
- 哪个系统分别负责 Issue 文本、类型化产物、代码证据与工作项状态。

首期只选择最小能力。先接入只读的 Check 与 Merge 证据，通常比一开始就开放 MR
创建和评论更安全。

## 3. 实现 Code Provider Bridge

Bridge 是运维方持有的可执行程序。issue-spec 直接启动它，在标准输入写入一个严格
JSON 请求，并只接受标准输出中的一个严格 JSON 响应。Bridge 只能获得显式配置的
参数和环境变量。

完整协议见 [`issue-spec.code-provider/v1`](bridges/code-provider-v1.md)。

### 生成 Wrapper 脚手架

仓库提供了通用技能和 Python 脚手架：

```bash
python3 .agents/skills/configure-enterprise-provider/scripts/scaffold_provider.py \
  --provider-key code.example \
  --display-name "Example Code" \
  --remote-authority git.example.test \
  --capability evidence.snapshot \
  --recommended-evidence change \
  --recommended-evidence check \
  --output "$HOME/.config/issue-spec/providers/code.example"
```

命令会生成：

- `provider_bridge.py`：严格协议 Envelope 与安全错误处理；
- `providers.json`：私有运维注册信息和可公开的 Provider 描述；
- `implementation-plan.json`：目标 Capability 和启用检查清单。

脚手架会故意对 Snapshot 和 Mutation 返回 `not_implemented`，因此运行时和
`providers.json` 默认都不启用任何 Capability。`--capability` 和
`--recommended-evidence` 只会把实施目标写入 `implementation-plan.json`，不会提前
宣称能力。部署前必须替换对应分支、补齐协议测试，再把已经完成的值同时写入
`provider_bridge.py` 与 `providers.json`。

### 把平台对象映射为中性证据

| 中性坐标 | 常见平台字段 |
|---|---|
| `provider_key` | 运维注册 Key |
| `external_repository` | 稳定 Project ID 或规范 Namespace |
| `change_id` | 有明确仓库作用域的 PR/MR ID |
| `subject_revision` | 精确 Head Commit SHA |
| `canonical_url` | 不含凭据的规范 HTTPS 页面地址 |

处理 `snapshot` 时必须查询请求指定的 `subject_revision`，不能静默替换成最新 Head。
平台对象需要规范化为 `change`、`review`、`check`、`merge` 和 `archive` Fact，并使用
稳定 ID 与规范 Payload Digest。是否通过流程 Gate 由 issue-spec 判断，Wrapper 不得
合成一个 `approved` 结论。

Review Fact 必须带真实的 `FINDING-*`、`PROCESS-*` 与 `SPEC-*` 关联。如果平台无法
保留这些元数据，就不要把普通 Review 文本声明为流程证据。

`create_change` 只有响应可以引入新的 `change_id`；`comment` 必须回显完全相同的请求
Reference。两类 Mutation 都应使用平台 Idempotency Key 或本地 Ledger 保证幂等。

## 4. 注册与配置 Provider

Registry 必须位于代码仓库外，由服务运维方持有，权限为 `0600` 或更严格：

```json
{
  "version": 1,
  "providers": {
    "code.example": {
      "path": "/opt/issue-spec/providers/code-example/provider-bridge",
      "args": ["serve-stdio"],
      "environment": [
        "CODE_EXAMPLE_API_URL=https://git.example.test/api",
        "CODE_EXAMPLE_TOKEN_FILE=/run/secrets/code-example-token"
      ],
      "timeout": "30s",
      "max_output_bytes": 1048576,
      "description": {
        "display_name": "Example Code",
        "remote_authorities": ["git.example.test"],
        "code_change_label": "Merge request",
        "capabilities": ["evidence.snapshot"],
        "recommended_evidence": ["change", "check"]
      }
    }
  }
}
```

Server 和所有会执行 Provider 操作的 CLI 进程都应指向同一个可信 Registry：

```bash
export ISSUE_SPEC_CODE_PROVIDERS_FILE=/etc/issue-spec/code-providers.json
```

self-hosted Profile 也可以保存绝对路径 `operator_registry_file`；环境变量优先。不要在
仓库的 `issue-spec/config.yaml` 中保存可执行路径、环境变量或凭据来源。

重启 Server 后，确认 `/api/v1/meta` 只公开安全的 Provider 描述，不得出现 Bridge
路径、环境变量、Token File 或凭据。

### 验证 Capability 握手

```bash
python3 .agents/skills/configure-enterprise-provider/scripts/validate_provider.py \
  --registry /etc/issue-spec/code-providers.json \
  --provider-key code.example
```

校验器会检查私有文件权限、严格 Registry 结构、可执行文件位置、响应大小、协议身份，
以及运行时 Capability 是否与公开描述一致。

## 5. 为 issue-spec 仓库绑定源码

Source Binding 只保存坐标，不保存凭据。先用 Plan 模式检查，不做远端或本地写入：

```bash
issue-spec --profile team init \
  --repo acme/payments-spec \
  --server-org acme \
  --server-repo payments-spec \
  --bind-source \
  --provider code.example \
  --external-repo platform/payments \
  --source-clone-url https://git.example.test/platform/payments.git \
  --source-web-url https://git.example.test/platform/payments \
  --default-branch main \
  --tools codex,claude \
  --delivery skills \
  --plan
```

确认解析后的 Provider、Remote Authority、Server Repo、External Repo、Clone URL、
Web URL 与默认分支。确认无误后去掉 `--plan`；只有经过批准的非交互变更才增加
`--yes`。

仓库内生成的工作流配置可以选择 `code.example` 及其证据策略，但不能替换运维侧
Provider 注册。

## 6. 对接 Jira 类工作项平台

当前版本不会从运维 Registry 加载任意 IssueBackend 可执行程序。不要虚构
`ISSUE_SPEC_ISSUE_PROVIDERS_FILE`，也不要宣称只靠配置即可启用 Jira Wrapper。

推荐模式：

1. issue-spec Server 负责 Issue Body、类型化评论、权限、Change 状态和 Runner 命令。
2. 使用独立 Service Account 运行工作项投影 Sidecar。
3. 创建或关联一个外部工作项，并保存唯一映射：
   `server_instance_id + repository_id + issue_id -> tracker_project + item_id`。
4. 消费签名后的 issue-spec Webhook，或带 Checkpoint 轮询 Native API。
5. 只投影稳定状态摘要与双向 HTTPS 链接，不把类型化评论复制为自由文本评论。
6. 使用 Delivery ID、Inbox/Outbox Ledger、条件更新和 Origin Marker 防止重复与回环。
7. 定期 Reconcile，因为任一平台都可能丢失 Webhook。

如果外部工作项平台必须作为 Issue 事实源，需要把 Go `IssueBackend` Adapter 作为核心
产品代码实现和维护。这个接口目前不是稳定的进程外插件协议；在提供可移植的 Issue
Provider Wrapper 前，需要先完成上游协议设计。

## 7. 配置身份与 Runner Git 权限

员工浏览器登录优先使用 [OIDC](authentication/v1/oidc.md)。OIDC 只建立身份，不会
自动授予组织或仓库角色；issue-spec 权限需要独立配置。

Runner Clone/Push 优先使用实现
[`issue-spec-git-credential-v1`](bridges/git-credential-v1.md) 的短期凭据 Command，
按固定 Source Binding 签发 Lease，并在 Job 完成时撤销。

可信部署也可以选择专用 Runner OS 账号的宿主 SSH，但必须开启 Sandbox，并以只读方式
挂载 `.ssh`。宿主 SSH 的权限范围大于 Job Scoped Credential，不应作为默认方案。

## 8. 验收与运维

先在非生产仓库验证：

- Server 与 CLI 加载相同的 Provider 描述；
- Source Binding Authority 与规范 URL 可以通过校验；
- 所有声明的 Action 成功，所有未声明的 Action Fail Closed；
- Snapshot 会拒绝错误的 Provider、Repo、Change 或 Revision；
- Stale、Duplicate、Pending、Failed、Merged 与 Superseded Fact 行为正确；
- 重试不会创建重复 Change 或 Comment；
- 401、403、404、429、Timeout、Cancellation 与 5xx 被映射为安全稳定的错误；
- 响应大小限制、Secret Redaction、凭据轮转和回滚已经演练；
- 工作项投影只有一个事实源，不会产生同步回环。

企业具体配置和详细证据只保存在获批的内部系统。公开文档、Issue 与 PR 只使用通用
示例和脱敏后的通过/失败摘要。

## 使用 Agent 辅助配置

适配新平台时可直接调用仓库技能：

```text
Use $configure-enterprise-provider to assess our code and work-item APIs,
scaffold the bridge, create a private provider registry, and produce a
non-production validation plan.
```

技能入口位于 `.agents/skills/configure-enterprise-provider/SKILL.md`。
