# issue-spec 自托管 Server

**[English](README.md) | 简体中文**

自托管 Server 把 issue-native 的 issue-spec 工作流部署到团队自己控制的环境中。
它在一个服务里提供 Web 工作台、GitHub 兼容 Issue API、组织与仓库权限、
provider-neutral 代码证据、Webhook、Runner，以及由 PostgreSQL 持久化的状态。

当团队需要私网部署、本地身份与权限、内部代码平台接入，或不依赖个人 GitHub
账号的自动化身份时，可以使用 self-hosted 模式。

![带仓库订阅入口的议题列表](assets/self-hosted-dashboard.zh-CN.png)

## Server 负责什么

```text
浏览器与 issue-spec CLI
          |
          v
 issue-spec-server  <---->  GitHub OAuth 或 OIDC
    |       |
    |       +------------>  code-provider bridge / evidence ledger
    |       +------------>  GitHub 兼容或 Runner Webhook
    v
 PostgreSQL
```

Server 是以下数据的权威来源：

- 组织、仓库、成员关系、Collaborator 与可见性；
- Issue、评论、Label、Reaction、类型化工作流投影和 Change Board；
- 个人与托管 PAT、Service Account、Runner 委托、Source Binding、Evidence Policy
  和不可变证据；
- Webhook 订阅、过滤策略、加密凭据、投递尝试、重试、重放与审计记录。

外部代码平台仍然负责源码、分支、Commit、PR/MR、Review 和 CI。Source Binding
和 Code Provider Adapter 只把证据投影到 issue-spec，不会让 Server 获得环境中的
源码凭据。

## 产品导览

### Issue 保留完整决策历史

Proposal、Design 和 Implement Issue 的正文保存当前产物；SPEC、QUESTION、TASK 与
PROCESS 是同一时间线中的活跃类型化评论，历史 REVIEW 与 VERIFY 只保留审计展示。
普通人的评论仍可用于讨论与通知。

![包含类型化工作流评论的 Issue 详情](assets/self-hosted-issue-detail.zh-CN.png)

### 评审投影为人渲染，问题原生作答

Agent 会为每个阶段发布沙箱化的 `html-preview` 评审投影——提案决策简报、
设计评审简报与执行简报；每条类型化 QUESTION 评论都自带原生答题面板。
确认的选择会成为不可变的类型化 ANSWER 评论，供后续 Agent 与工作流关卡消费。

![设计评审简报投影](assets/self-hosted-review-design.zh-CN.png)

### 检索按关联变更分组

全文检索覆盖 issue 正文与评论，并按关联变更与阶段分组展示命中结果，
评审者和 Agent 都能顺着关键词找到背后的历史 proposal、design、implement
轨迹。同样的查询也可在 CLI 中执行：`issue-spec search issues --source change
--stage <stage>`。

![按关联变更分组的检索结果](assets/self-hosted-search.zh-CN.png)

### Change Board 展示工作流状态，而不只是 Issue 数量

Change Board 把 Proposal、Design 和 Implement Issue 聚合成一个 Change，并展示
生命周期、TASK/PROCESS 进度、诊断信息和关联代码变更。

![Change Board](assets/self-hosted-change-board.zh-CN.png)

详情页把追溯关系和 provider-neutral 代码关联放在一起。GitHub PR 与内部 MR 可以
属于同一个 Change，同时保留各自的 Provider 身份。

![Change 详情与代码证据](assets/self-hosted-change-detail.zh-CN.png)

### 在下一次改动前找回历史决策

运维方启用 PostgreSQL 检索后，Web 工作台可以搜索当前身份可见的 Issue 正文与评论，
包括已经关闭的讨论；结果按 Issue 聚合，并展示关联 Change Key 和阶段。权限过滤发生
在匹配、排序、总数、摘要与分页之前。搜索请求最多接受 256 字节的查询和每页 50
条结果；服务端对数据库查询应用 5 秒截止时间，同时保留调用方设置的更早截止时间。

直接使用 Codex、Claude 或其他客户端对接 issue-spec 时，也使用同一个能力：

```bash
issue-spec --profile team search issues \
  --repo acme/workflow \
  --query ListConfigsBySource \
  --state all \
  --limit 10
```

搜索输出会把用户撰写的标题和摘要放在不可信数据边界内，并引导 Agent 用
`read issue --comments` 打开选中的完整讨论。这是通用 issue-spec 工作流，不是
Runner 专属流程；Runner 调度的 Agent 只是复用同一个 CLI 契约。显式扩展和启动
约定见[部署指南](operations/deployment.md#optional-postgresql-issue-search)。

### 集成具有明确策略和完整审计

仓库管理员可以配置 Runner Intake 或 `github.v3` 通知 Webhook。通知策略可以选择
Issue 动作、Change 类型、普通/类型化评论，以及真人/自动化 Actor。URL 查询参数中
的凭据会被加密，创建后不会再次返回浏览器。

![Webhook 投递控制台](assets/self-hosted-webhook-integrations.zh-CN.png)

## 权限模型

仓库可见性控制读取范围，Contribution Policy 单独控制谁可以创建 Issue 或评论。

| 可见性 | 匿名用户 | 已登录的外部用户 | 成员或 Collaborator |
| --- | --- | --- | --- |
| `public` | 可读 | 可读 | 可读 |
| `internal` | 隐藏 | 可读 | 可读 |
| `private` | 隐藏 | 隐藏 | 可读 |

| Contribution Policy | 可以贡献的身份 |
| --- | --- |
| `disabled` | 无人 |
| `members` | 组织成员、Service Account 或显式 Collaborator |
| `authenticated` | 任意有效的已登录身份 |
| `public` | 仓库为 Public 时，任意有效的已登录身份 |

匿名写入始终被拒绝。通过 GitHub OAuth 或 OIDC 登录只会建立外部身份，不会自动
获得组织角色、仓库权限、Runner 权限或证据发布权限。发布证据还必须同时满足显式凭据
Scope 与实时仓库权限。

## 部署路径

生产制品是单个 `issue-spec-server` 二进制或仓库中的 Runtime 容器。二进制已内嵌
生成后的 Web 应用。部署需要 PostgreSQL 和三个由运维方管理的密钥文件。第四个可选的
SMTP 密钥文件用于启用已验证通知邮箱、Mention 和显式仓库邮件订阅；不提供该文件时，
这些能力保持关闭，不影响 Issue 或 Webhook 的正常运行。

可在 SMTP JSON 中增加可选的通知邮箱域名限制（以下仅为脱敏示例）：

```json
{
  "host": "mail.example.test",
  "port": 2465,
  "username": "mailer@example.test",
  "password": "<smtp-password>",
  "from_address": "notifications@example.test",
  "allowed_email_domain_suffixes": ["example.test"]
}
```

后缀匹配不区分大小写，并严格遵循点分隔的域名边界。配置 `example.test` 时，
`person@example.test` 和 `person@team.example.test` 均允许，
`person@evilexample.test` 与 `person@example.test.evil.test` 则会被拒绝。
未配置该字段或配置空数组时保持原有兼容行为，允许任意语法有效的通知邮箱域名。

1. 阅读[部署与加固指南](operations/deployment.md)。
2. 选择 HTTPS；若必须使用内网 HTTP，完成
   [Trusted Internal HTTP 检查表](authentication/v1/trusted-internal-http.md)。
3. 配置 [GitHub OAuth](authentication/v1/github-oauth.md) 或
   [OIDC](authentication/v1/oidc.md)。
4. 构建可复现的 Server 制品：

   ```bash
   make release-server
   # 或
   make docker-server IMAGE=registry.example/issue-spec-server:VERSION
   ```

5. 启动 PostgreSQL 和 Server，并挂载必需的环境变量与密钥；`/readyz` 就绪后再
   接入团队流量。
6. 仅执行一次 Bootstrap Claim，使用配置的身份 Provider 登录，创建首个组织，
   并显式分配本地角色。
7. PostgreSQL 必须与 Token Pepper、Encryption Keyring 一起备份；参见
   [备份、恢复、升级与应急处理](operations/backup-restore.md)。

`deployments/dev` 仅用于本地开发。按照[本地 Server 开发指南](local-development.zh-CN.md)
可以运行完整 Compose 环境，或让宿主机二进制连接其中的 PostgreSQL。不要把 fixture 中的
凭据或开发模式复制到生产环境。

## 接入本地仓库

从已校验 CLI 安装和只填名称的 PAT，到创建简单 Issue 或标准 Proposal/SPEC 的完整
外部用户路径，见[启动需求工作流](requirements-onboarding.zh-CN.md)。

先在 Web 应用的 **Access tokens** 页面创建 PAT，然后配置与 Server Origin 绑定的
self-hosted Profile。应从 `/api/v1/meta` 读取 Origin 和 Instance ID，不要自行猜测。

```bash
printf '%s\n' "$ISSUE_SPEC_TOKEN" | issue-spec auth login \
  --profile team \
  --kind self-hosted \
  --api-url https://issues.example.com \
  --native-api-url https://issues.example.com/api/v1 \
  --web-url https://issues.example.com \
  --instance-id issue-spec:00000000-0000-4000-8000-000000000000 \
  --with-token

issue-spec --profile team auth status --json
```

初始化 Server 中已经存在的仓库：

```bash
issue-spec --profile team init \
  --repo acme/workflow \
  --server-org acme \
  --server-repo workflow \
  --tools codex,claude \
  --delivery both
```

有仓库创建权限时，可以增加 `--create-if-missing` 自动注册仓库。源码位于独立代码
平台时，使用 `--bind-source`、`--provider` 和外部仓库坐标。Source Binding 只包含
规范化仓库身份和 URL，不保存个人 Clone Credential。

Provider 所属的 PR/MR 已经存在后，先按精确 Head Revision 校验并关联到 Implement
Issue，再让各 PROCESS 复用这个 Active Relationship：

```bash
issue-spec --profile team code-change attach \
  --repo acme/workflow --implement 3 --change-id 42 --revision abc123 --json

issue-spec --profile team code-change link-process \
  --repo acme/workflow --implement 3 --process PROCESS-001 \
  --expected-version 5 --json
```

Provider 与外部仓库身份来自 Source Binding。`code-change attach` 不会创建外部变更，
也不会导入证据；Refresh 必须同时提供 `--refresh` 与 `--expected-version`。链接时必须
恰好存在一个 Active `code_change`。如果 Reference 存在歧义，应先检查并只删除不需要
的 Active Reference，再重试；禁止猜测或静默覆盖。GitHub 继续使用原有 PR 流程；
self-hosted 的 Review、Merge 与关闭仍由所选 Code Provider 负责。

Provider 原生评审与已配置检查在精确 head 上收敛后，只运行只读 `merge-check`；不要
把权威同步到 REVIEW/VERIFY、理由或 PROCESS 状态。只能使用 `code-change merge
--expected-head` 合并；该命令重新采集新鲜权威，并把完整提供方令牌交给条件合并。
普通 GitHub REST 的先读后写保持失败关闭。只有新鲜观察到提供方已合并后，才幂等
协调精确选择的 Issue 集合。

完整的 Provider-neutral 集成方案、运维 Registry 示例、Bridge 脚手架、代码证据
映射和 Jira 类工作项投影模式见
[对接企业代码平台与工作项平台](enterprise-provider-integration.zh-CN.md)。

## 自动化身份

Service Account 是组织范围内的非人类身份，适用于 CI、Runner、Evidence 同步和
定时集成。创建 Service Account 不会自动创建 Token，也不会自动授予仓库权限。

安全配置顺序是：

1. 创建 Service Account；
2. 只授予必需的仓库 Collaborator Role；
3. 创建包含必需 Scope 的 Managed PAT，并按需选择全站访问或明确的仓库范围；
4. 把一次性显示的 Token 放入自动化系统的 Secret Store；
5. 下线时禁用 Service Account，使其凭据失效。

组织管理员可以为本组织拥有且已启用的 Service Account 签发全站 Managed PAT。普通成员
通常应自行创建全站个人 PAT；只有站点管理员可以代其签发或轮换全站 Managed PAT。
“全站”表示令牌始终跟随主体的实时权限，本身不会授予任何仓库权限。

Service Account 与 PAT 的操作会被标记为 Automation，Webhook 策略和审计可以把
它们与浏览器中的真人操作区分开。

## Webhook 与 Runner

两种投递协议共享同一个事务 Outbox 和 Delivery Ledger：

- `issue-spec.v1` 使用 Bearer 认证，对接 `issue-spec runner serve`；
- `github.v3` 发送 GitHub 兼容的 Issue/Comment 事件，可直接对接兼容 GitHub
  Webhook 的钉钉机器人等通知接收端。

无需解析不可信评论文本，就可以排除类型化评论或 Automation Actor。失败投递会
自动重试，超过次数后进入 Dead Letter，并能在保持事件身份不变的情况下重放。
多副本投递与恢复语义见 [HA Webhook 运维](operations/ha-webhooks.md)。
从 PAT、Source Binding、Webhook 到 systemd 启动和评论命令的完整步骤见
[自托管 Runner：通过 Issue 评论触发 Agent](runner.zh-CN.md)。

## 运维文档索引

- [本地 Server 开发](local-development.zh-CN.md)
- [需求接入指南](requirements-onboarding.zh-CN.md)
- [认证指南](authentication/README.md)
- [部署与加固](operations/deployment.md)
- [备份、恢复、升级与应急处理](operations/backup-restore.md)
- [HA Webhook 投递](operations/ha-webhooks.md)
- [通过 Issue 评论触发 Agent：Runner 接入指南](runner.zh-CN.md)
- [企业代码平台与工作项平台集成](enterprise-provider-integration.zh-CN.md)
- [Code Provider Bridge 协议](bridges/code-provider-v1.md)
- [Git Credential Bridge 协议](bridges/git-credential-v1.md)

## 重新生成文档截图

本文图片不来自真实组织，而是由视觉回归测试使用的固定、无凭据 Playwright
Fixture 生成。

```bash
make docs-self-hosted-screenshots
```

命令会构建 Web 应用、更新中英文 Desktop Golden Snapshot，并把两种语言的文档图片
同步到 `docs/self-hosting/assets`。提交更新图片前必须检查视觉 Diff。不要用包含内部 Issue、
真实账号、Access Token 或内网环境信息的截图替换这些 Fixture。
