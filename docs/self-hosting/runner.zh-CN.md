# 自托管 Runner：通过 Issue 评论触发 Agent

**[English](runner.md) | 简体中文**

[返回自托管 Server 文档](README.zh-CN.md)

本文说明如何让自托管 issue-spec Server 通过 Issue 评论触发 Codex 或 Claude
Agent。GitHub 后端使用 `issue-spec runner poll`；自托管 Server 必须使用
`issue-spec runner serve` 接收 Server 主动投递的 Webhook。

```text
维护者评论 /new
        |
        v
issue-spec Server 事务 Outbox
        |
        | issue-spec.v1 + Bearer secret
        v
runner serve /api/v1/runner/webhooks
        |
        +--> 校验评论作者与仓库权限
        +--> 获取 Source Binding 和任务级 Git 凭据
        +--> 委托短期 Issue Token
        v
      acpx --> Codex 或 Claude
        |
        +--> 分支、Commit、PR/MR、测试
        +--> Issue 类型化评论和 Runner 状态评论
```

## 1. 准备 Runner 主机

推荐在 Linux 主机上以专用系统用户运行 Runner。主机需要：

- 与 Server 兼容版本的 `issue-spec`；
- `acpx`，以及所选 Agent 的运行环境；Codex Provider 还需要 `npm` 和 `npx`；
- `bubblewrap`，用于默认的文件系统沙箱；
- 到 issue-spec Server、代码平台、模型服务和所需软件源的网络；
- 一个实现 [`issue-spec-git-credential-v1`](bridges/git-credential-v1.md) 的运维侧
  Git Credential Command。

先验证本机依赖：

```bash
issue-spec --version
acpx --version
bwrap --version
```

不要把 `--unsafe-no-sandbox` 作为常规配置。只有明确接受 Agent 能访问 Runner
宿主文件系统的风险时，才使用该选项。

## 2. 为仓库配置 Source Binding

Runner 从 Source Binding 获取代码平台、外部仓库身份、HTTPS Clone URL 和默认分支。
在仓库的 **Source connection / 源码连接** 页面创建并启用绑定。绑定只保存仓库身份
和 URL，不保存 Clone Credential。

Git Credential Command 必须根据绑定签发只对该仓库有效的短期凭据。它不是
issue-spec PAT，也不应返回代码平台的长期个人 Token。

## 3. 创建独立 Service Account 和 Runner PAT

生产 Runner 不应使用管理员或维护者的个人身份。为每个独立的 Runner 安全边界创建
一个 issue-spec Service Account：

1. 在 **管理后台 > 服务账号** 创建账号，例如 `Runner Bot`，保存自动生成的准确
   Login，例如 `svc-runner-bot-a1b2c3d4`；
2. 在目标仓库的 **协作者** 页面解析该 Login，并授予最低的 `write` 角色；
3. 在 **管理后台 > 托管访问令牌** 中解析该 Service Account；
4. 选择 **运行器预设**，并且只选择 Runner 要服务的那个仓库；
5. 确认 Scope 包含 `read:user`、`issues:read`、`issues:write`、
   `runner:delegate` 和 `evidence:write`；
6. 创建并保存只显示一次的 Managed PAT。

![为独立服务账号签发 Runner Managed PAT](assets/self-hosted-runner-service-account.zh-CN.png)

`--runner` 必须填写 Service Account 的准确 Login。其他真人维护者若要触发任务，使用
可重复的 `--allowed-user` 加入允许列表；Server 还会独立校验评论作者具有仓库 Write
等效权限。这样 Runner 自动化身份、评论发起人和 Linux 进程用户互不混用。

### 简化方式：使用自己的账号

本地试用、个人环境或短期联调也可以直接使用自己的 issue-spec 账号：

1. 确认自己的账号对目标仓库具有 `write` 或更高权限；
2. 在 **访问令牌** 页面选择 **运行器预设**，并且只选择目标仓库；
3. 保存只显示一次的个人 PAT；
4. 启动时把自己的准确 Login 传给 `--runner`。

个人 PAT 使用相同的五个 Scope，并且同样必须精确绑定一个仓库。默认只有
`--runner` 对应的自己可以发出命令；需要允许其他维护者时再增加 `--allowed-user`。
这种方式配置更少，但 Runner 写入、凭据轮换和账号停用都与个人身份绑定，不适合作为
团队长期运行或多人共用的生产自动化。不要用浏览器 Session Cookie 或登录会话替代 PAT。

从 Server 的 `/api/v1/meta` 读取公开地址和 Instance ID，然后在 Runner 系统用户下
创建与 Origin 绑定的 Profile：

```bash
curl -fsS https://issues.example.com/api/v1/meta

printf '%s\n' "$ISSUE_SPEC_TOKEN" | issue-spec auth login \
  --profile team \
  --kind self-hosted \
  --api-url https://issues.example.com \
  --native-api-url https://issues.example.com/api/v1 \
  --web-url https://issues.example.com \
  --instance-id issue-spec:00000000-0000-4000-8000-000000000000 \
  --with-token

unset ISSUE_SPEC_TOKEN
issue-spec --profile team auth status --json
```

上述 Managed PAT 或个人 PAT 是 Runner 的父凭据。每个任务实际使用的是 Server
委托的短期、仓库范围 Issue Token。父凭据必须精确绑定一个仓库；需要服务多个仓库时，
应为每个仓库创建独立的 PAT、Profile 和 `runner serve` 进程。

## 4. 创建 Runner Intake Webhook

进入目标仓库的 **Webhooks** 页面并选择 **New webhook / 新建 Webhook**：

1. 投递协议保持 **Runner intake (`issue-spec.v1`)**；
2. Receiver URL 填写 Runner 对 Server 可达的完整地址，例如
   `https://runner.example.com/api/v1/runner/webhooks`；
3. 选择 `issue_comment.created` 和 `issue_comment.edited`；
4. 创建路由。

![Runner Intake Webhook 配置](assets/self-hosted-runner-intake.zh-CN.png)

创建成功后同时保存 **订阅 ID** 和只显示一次的 **Webhook 密钥**。两者分别传给
`--subscription-id` 和 `--secret-file`，不能互换。

![保存订阅 ID 和 Webhook 密钥](assets/self-hosted-runner-credentials.zh-CN.png)

把密钥写入仅 Runner 用户可读的文件，不要出现在命令行参数、仓库或 systemd Unit：

```bash
sudo install -d -o issue-spec-runner -g issue-spec-runner -m 0700 \
  /etc/issue-spec-runner
printf '%s\n' "$RUNNER_WEBHOOK_SECRET" | sudo install \
  -o issue-spec-runner -g issue-spec-runner -m 0600 /dev/stdin \
  /etc/issue-spec-runner/webhook-secret
unset RUNNER_WEBHOOK_SECRET
```

生产模式只接受权限为 `0600` 的 Secret File，不接受环境变量中的 Webhook 密钥。

## 5. 接入代码平台凭据

`--git-credential-command` 指向一个绝对路径的运维侧可执行文件。Runner 不通过
Shell 启动它，也不会把 Profile PAT、Webhook Secret 或宿主环境传进去。Command
根据标准输入中的固定 Source Binding 返回任务级用户名、密码、过期时间和 Lease ID，
并支持幂等的 `revoke_lease` 与 `revoke_job`。

完整 JSON 协议、校验规则和撤销语义见
[`Runner Git credential command v1`](bridges/git-credential-v1.md)。例如：

```text
/usr/local/libexec/issue-spec-git-credential
```

该程序应向代码平台的机器人凭据服务或 Token Broker 请求短期凭据；issue-spec
本身不内置任何代码平台长期凭据。

## 6. Preflight 与前台启动

先用 self-hosted Profile 检查仓库权限、Agent、acpx 和沙箱：

```bash
issue-spec --profile team runner preflight \
  --repo acme/workflow \
  --runner svc-runner-bot-a1b2c3d4 \
  --agent codex \
  --json
```

再前台启动一次，确认日志中显示预期的 Profile、Subscription、监听地址和 Endpoint：

```bash
issue-spec --profile team runner serve \
  --repo acme/workflow \
  --runner svc-runner-bot-a1b2c3d4 \
  --allowed-user maintainer \
  --listen 127.0.0.1:9876 \
  --subscription-id 11111111-2222-4333-8444-555555555555 \
  --secret-file /etc/issue-spec-runner/webhook-secret \
  --git-credential-command /usr/local/libexec/issue-spec-git-credential \
  --state /var/lib/issue-spec-runner/state.json \
  --workspace-root /var/lib/issue-spec-runner/workspaces \
  --agent codex
```

`--allowed-user` 可以重复。默认最多并行运行 3 个任务，可用
`--max-concurrent-jobs` 调整。同一个父凭据不能跨仓库委托任务。

### 网络和 TLS

- Runner 前面已有 HTTPS 反向代理时，让 `runner serve` 监听 Loopback，代理只转发
  `/api/v1/runner/webhooks`。
- Runner 直接提供 HTTPS 时，使用精确的非 Loopback IP，而不是通配地址，并添加
  `--production --tls-cert FILE --tls-key FILE`。
- Server 保存的 Receiver URL 必须能从 Server 投递 Worker 访问；不要填写仅浏览器
  或 Runner 自己可达的地址。

## 7. 使用 systemd 持续运行

先确保 `issue-spec-runner` 用户已经完成 Profile 登录，并拥有状态和 Workspace 目录：

```bash
sudo install -d -o issue-spec-runner -g issue-spec-runner -m 0700 \
  /var/lib/issue-spec-runner
```

创建 `/etc/systemd/system/issue-spec-runner.service`：

```ini
[Unit]
Description=issue-spec comment-triggered runner
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=issue-spec-runner
Group=issue-spec-runner
Environment=HOME=/var/lib/issue-spec-runner
ExecStart=/usr/local/bin/issue-spec --profile team runner serve \
  --repo acme/workflow \
  --runner svc-runner-bot-a1b2c3d4 \
  --allowed-user maintainer \
  --listen 127.0.0.1:9876 \
  --subscription-id 11111111-2222-4333-8444-555555555555 \
  --secret-file /etc/issue-spec-runner/webhook-secret \
  --git-credential-command /usr/local/libexec/issue-spec-git-credential \
  --state /var/lib/issue-spec-runner/state.json \
  --workspace-root /var/lib/issue-spec-runner/workspaces \
  --agent codex
Restart=on-failure
RestartSec=5s
NoNewPrivileges=true
PrivateTmp=true
UMask=0077

[Install]
WantedBy=multi-user.target
```

启动并查看日志：

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now issue-spec-runner
sudo systemctl status issue-spec-runner
sudo journalctl -u issue-spec-runner -f
```

## 8. 通过评论触发 Agent

命令必须从评论第一行开头开始：

```text
/new 修复登录失败问题，补充测试并创建 PR
/resume s_demo_42 按评审建议调整错误处理
/cancel s_demo_42
```

- `/new <prompt>` 创建新的公共 Session 和 Workspace；
- `/resume <public-session-id> <prompt>` 继续原 Session；
- `/cancel <public-session-id>` 取消允许取消的在途任务；
- 普通评论不会触发 Agent。

Runner 会在 Issue 时间线写入状态、阶段、Public Session ID、结果和可复制的
`/resume` 模板。公共 Session 属于仓库中被授权的维护者，不是某个人的私有会话。

![评论触发 Agent 与 Runner 状态](assets/self-hosted-runner-command.zh-CN.png)

## 9. 验证与排障

首次联调建议执行一个只读任务，并依次确认：

1. Webhook Delivery 从 Pending 变为 Succeeded；
2. Runner 日志显示事件已入队并通过作者授权；
3. 任务 Workspace 已创建，Git 凭据 Lease 能获取并撤销；
4. Issue 出现 Runner 状态评论；
5. Agent 能写类型化评论，并按任务要求提交代码或创建 PR/MR。

| 现象 | 优先检查 |
| --- | --- |
| Webhook 为 `401` | Subscription ID、当前 Secret、Runner 与 Server 时钟 |
| Webhook 无法连接 | Receiver URL、DNS、防火墙、反向代理和 TLS |
| 评论被忽略 | 命令是否位于开头、作者是否在允许列表、是否具有 Write 权限 |
| `runner:delegate` 失败 | PAT 是否只限制到该仓库、Scope 是否完整、`--runner` 是否为 PAT 所属账号的 Login |
| 找不到源码或 Clone 失败 | Source Binding 是否 Active，Clone URL 是否为 HTTPS，Git Credential Command 返回的 Binding 是否完全匹配 |
| Preflight 报沙箱失败 | Linux 上安装 `bubblewrap` 或显式配置 `--bwrap` |
| Codex 无法启动 | `acpx`、`npm`、`npx` 和模型凭据是否对 systemd 用户可用 |

轮换 Webhook Secret 时，先在 Web UI 轮换，再把旧 Secret 作为
`--previous-secret-file` 提供，并用 `--previous-secrets-valid-until` 设置最长 24 小时的
重叠窗口。确认新 Secret 投递成功后移除旧 Secret。

## 安全边界

- Webhook Secret 只验证 Server 到 Runner 的投递，不授予 Issue 或代码权限；
- Profile PAT 只用于向 Server 委托短期、仓库范围的任务 Token；
- Source Binding 不含凭据；Git 凭据必须按 Job 和 Binding 短期签发；
- Agent 只能处理明确配置的 `--repo`，评论作者还必须同时通过允许列表和仓库权限校验；
- Runner 状态、Workspace 和 Credential Lease 都应进入集中日志与审计。
