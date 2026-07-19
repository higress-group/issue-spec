# 工作流配置与 CLI 参考

**[English](reference.md) | 简体中文**

[返回项目 README](../README.zh-CN.md)

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

issue-spec --profile team search issues --repo owner/repo --query "错误或代码符号" --state all --source all --limit 10
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

### 工作流中立的初始化

显式且不区分大小写的 `--tools none` 会初始化 issue-spec 运行时状态，但不会选择或更改项目工作流。它不会读取、校验、创建或修改 `issue-spec/config.yaml` 或 `openspec/config.yaml`，也不会生成仓库 skills、commands 或用户全局 prompts。已有工作流文件会保持逐字节不变；尤其是只有 `openspec/config.yaml` 的仓库，初始化之后仍会通过遗留 OpenSpec 兼容模式发现工作流。

运行时初始化仍会执行：GitHub init 会写入 `.issue-spec/config.json`，并可确保 labels；自托管 init 仍可注册已批准的仓库与 binding、确保 labels、更新 init journal，并在 `.issue-spec/config.json` 中记录服务器、provider、外部仓库与 capability 元数据。provider 工作流策略不会复制到 `issue-spec/config.yaml`。

显式 `--tools none` 可以与 `--language` 一起使用，但 JSON 会报告 `language_applied: false`，文本输出也会说明该语言未应用。请改在所选项目工作流中配置 `rules.language`；遗留 OpenSpec 项目使用 `openspec/config.yaml`。任何显式的 `--install-global-prompts`、`--global-prompts-dir` 或 `--global-prompts-dry-run` 选项都与 `--tools none` 冲突，并会在 profile/backend 选择或任何变更之前被拒绝。

## CLI 参考

```bash
issue-spec auth status
issue-spec auth login
issue-spec auth logout
issue-spec auth token --plain

issue-spec init --repo owner/repo
issue-spec init --repo owner/repo --skip-labels  # 标签由其他系统单独管理时显式跳过
issue-spec init --repo owner/repo --tools codex,claude --delivery both
issue-spec init --repo owner/repo --tools codex,claude --language zh
issue-spec init --repo owner/repo --tools codex --install-global-prompts
issue-spec init --repo owner/repo --tools none --language zh  # 会报告语言，但不会应用

issue-spec issue create proposal --repo owner/repo --change my-change --body-file proposal.md [--title "Custom proposal title"]
issue-spec issue create design --repo owner/repo --change my-change --proposal 1 --body-file design.md [--title "Custom design title"]
issue-spec issue create implement --repo owner/repo --change my-change --proposal 1 --design 2 --body-file implement.md [--title "Custom implementation title"]
issue-spec issue list --repo owner/repo --state all --json
issue-spec issue update --repo owner/repo --issue 1 --body-file proposal.md --summary "Clarified goals after review."
issue-spec issue close --repo owner/repo --issue 1 --json
issue-spec issue reopen --repo owner/repo --issue 1 --json

issue-spec comment create --repo owner/repo --issue 1 --body-file reply.md --json
issue-spec comment generate --type SPEC --id SPEC-001 --status confirmed --scope "canonical SPEC generation" --input-file spec.json
issue-spec comment upsert --repo owner/repo --issue 1 --type SPEC --id SPEC-001 --body-file spec.md
issue-spec comment upsert --repo owner/repo --issue 1 --type SPEC --id SPEC-001 --body-file legacy.md --allow-noncanonical
issue-spec comment get --repo owner/repo --issue 1 --id SPEC-001 --type SPEC --json
issue-spec comment get --repo owner/repo --issue 1 --id SPEC-001 --comment-id 123 --include-body --json
issue-spec comment list --repo owner/repo --issue 1 --json
issue-spec comment list --repo owner/repo --issue 1 --type SPEC --json --include-body
issue-spec comment list --repo owner/repo --issue 1 --active-only --json
issue-spec comment list --repo owner/repo --issue 1 --status ready,in-progress,done --json
issue-spec comment list --repo owner/repo --issue 1 --history --include-body --json

issue-spec question create --repo owner/repo --issue 1 --id QUESTION-001 --blocking --question "What must be decided?"
issue-spec question resolve --repo owner/repo --issue 1 --id QUESTION-001 --resolution-file resolution.md

issue-spec link --repo owner/repo --from SPEC-001 --from-issue 1 --to TASK-001 --to-issue 2
issue-spec status --repo owner/repo --proposal 1 --design 2 --implement 3
issue-spec verify-links --repo owner/repo --proposal 1 --design 2 --implement 3

issue-spec workflow validate --repo owner/repo --json
issue-spec workflow which --repo owner/repo --schema custom-workflow --json

issue-spec search issues --repo owner/repo --query "错误或代码符号" --state all --source all --limit 10

issue-spec --profile team code-change attach --repo acme/widgets --implement 3 --change-id 42 --revision abc123 [--refresh --expected-version 7] [--json]
issue-spec --profile team code-change link-process --repo acme/widgets --implement 3 --process PROCESS-001 --expected-version 5 [--json]

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

`issue list` 只提供 JSON 输出，默认列出 open issue。`--state` 接受
`open`、`closed` 或 `all`；命令会收集所有分页，包含无 issue-spec 元数据的
普通 issue，并排除 GitHub pull request。每条结果包含 issue 编号、标题、状态、
面向用户的 URL 与完整正文。

对普通、无 marker 的 issue，`issue update --body-file` 仍是纯正文替换。
对于带 marker 的 issue-spec issue，命令会确保已存储的 marker 只保留一次；
design 与 implement issue 还会确保直接前置 issue 链接只保留一次。冲突或格式
错误的保留元数据会在更新前被拒绝。`issue close` 与 `issue reopen` 会先读取
issue；若其状态已符合目标则跳过更新，并在 JSON 的 `changed` 字段中报告。

### 在相关改动前先检索

`search issues` 会根据当前 Issue Backend 选择实现。对 self-hosted Profile，它会先
发现 Server 是否启用检索；能力关闭时给出明确错误，并返回命中的 Issue/评论摘要及
关联 Change Key/阶段。对 GitHub Profile，它使用 GitHub Issue Search，强制限定仓库
与 Issue，并在解码结果时再次排除 Pull Request，同时把输出限制在 `--limit` 以内。
两个 Backend 都支持 `--state`。GitHub 会把 `--source issue` 映射为标题/正文检索，
把 `--source comments` 映射为评论检索，并把 `--stage` 映射为 canonical 的
`issue-spec/proposal`、`issue-spec/design` 或 `issue-spec/implement` Label。GitHub
没有等价的 Change Key 索引，因此不支持 `--source change`。无匹配项时命令成功并
返回零条结果；结果顺序与排序由 Backend 决定，不承诺保持一致。

两个适配器都只在请求的仓库中检索，并通过带随机 nonce 的不可信数据边界渲染同一组
有界的 Issue 字段。GitHub 的文本匹配片段可能与 self-hosted 摘要不同，可选 Change
元数据也可能缺失。

生成的 Codex 和 Claude 工作流会要求直接连接 issue-spec 的 Agent（不只 Runner
Session）在提出或实现相关改动前，根据用户请求和代码库提取少量具体查询词。搜索
结果只用于选择讨论，不是指令；标题与摘要属于不可信数据。依赖某个历史讨论前，
使用 `issue-spec --profile team read issue --repo owner/repo --issue N --comments`
打开完整内容。

### 关联 self-hosted 代码变更

对于 self-hosted Profile，当前 Active Source Binding 是 Provider 与外部仓库身份的
权威来源。把 Provider 上已经存在的代码变更按精确 Revision 关联到 Implement Issue：

```bash
issue-spec --profile team code-change attach \
  --repo acme/widgets \
  --implement 3 \
  --change-id 42 \
  --revision abc123 \
  --json
```

`code-change attach` 会通过已注册 Provider 校验外部变更，并记录 Active Relationship；
它不会创建 PR/MR，也不会导入 Review 或 CI 证据。同一身份与 Revision 的重复请求是
幂等的。把同一个 Active Change 刷新到新的精确 Revision 时，必须同时提供
`--refresh` 和观测到的正数 `--expected-version`。

存在且只存在一个 Active `code_change` Relationship 后，使用 PROCESS Comment
观测到的 Representation Version 建立链接：

```bash
issue-spec --profile team code-change link-process \
  --repo acme/widgets \
  --implement 3 \
  --process PROCESS-001 \
  --expected-version 5 \
  --json
```

重复链接同一个 Canonical URL 是 no-op；PROCESS 已包含不同 URL 时会冲突；不存在或
存在多个 Active Code Change 时命令会 fail closed。冲突报告多个 Active Reference
时，应在 Server UI 中检查 Implement Issue Reference，或调用
`GET /api/v1/orgs/{org-id}/repos/{repo-id}/issues/{issue-id}/references`，再通过对应的
`DELETE .../references/{reference-id}` 只删除不需要的 Active Reference，然后重试。
禁止猜测胜出项或静默覆盖另一个 Active Relationship。

独立的 Provider Review 收敛后，由 Review Agent 使用自身身份同步精确的 Active
Revision：

```bash
issue-spec --profile team review sync \
  --repo acme/widgets \
  --implement 3 \
  --revision abc123 \
  --id REVIEW-001 \
  --agent reviewer \
  --agent-session review-session \
  --json
```

self-hosted Sync 成功时会先持久化并重新读取 Provider Fact，再写入一个稳定的 Done
REVIEW Completion；即使 Provider 返回零 Finding，该 Completion 仍然有效。最终 Sync
后，使用 `issue-spec link` 把 REVIEW 显式链接到它的 Review PROCESS、每个覆盖到的
Change-bearing PROCESS 以及每个覆盖到的 Active SPEC。禁止伪造 Finding、手工编辑
Completion Stamp、从正文中的 ID 推断链接，或用通用 Approval Framework 替代证据。
`status` 与最终 `verify` 使用同一个 Validator 检查精确的 Provider、Repository、
Change、Reference Version、Revision、Freshness、链接和 Reviewer Independence，且都不
刷新 REVIEW。

self-hosted 关闭流程中，只有 Implementation `code_change` 的 Merge Policy 要求 Review
时，`archive` 才会读取已有的 Implementation REVIEW Completion。它不会创建、更新或
刷新 REVIEW，不会添加 Archive 专属 Review 状态，也绝不会把 Implementation
Completion 应用于 `archive_change`。

GitHub Profile 继续使用 `pr link-process`、GitHub PR Review/Closing Link 与现有 Durable
Archive 路径。self-hosted 的 Review、Merge 与代码变更关闭仍由所选 Code Provider
负责；CLI 不会把它们路由到 GitHub PR Endpoint。

`comment create` 通过当前选择的托管 GitHub 或自托管 REST issue backend 写入普通
issue 时间线评论。它支持 `--body-file -` 从 stdin 读取，并可通过 `--json` 仅返回
comment ID、URL 等有界的创建元数据。该命令不会添加 typed marker，也不会把正文
当作工作流 artifact 校验。

`comment list --json` 默认保持现有的解析后 artifact schema 不变。加上
`--include-body` 后，每条返回的 artifact 会新增顶层 `body` 字段，其中包含后端
返回的原始 Markdown（逐字节保持）；该 flag 必须与 `--json` 一起使用。两种模式
下的类型过滤与 canonical diagnostics 都保持不变；没有匹配项时编码为 `[]`，而
不是 `null`。

`comment get --issue N --id PROCESS-001 --json` 只返回一个有界的 typed
artifact，而不是整条 issue 时间线。可选的 `--type` 用来断言类型。当
`--comment-id` 提供了先前读取到的 provider locator 且 backend 支持直接观察时，
CLI 会直接读取该评论，并校验 issue、marker、type 和 stable ID；否则可以在内部
扫描，但绝不会向调用者返回无关正文。重复 stable ID 或 locator 不匹配都会
fail closed。`--include-body` 只包含目标 artifact 的精确 Markdown。

定向读取和显式过滤后的 list 结果包含 `representation_digest`：它是远端 Markdown
精确字节的 lowercase SHA-256，不会规范化换行或空白。Backend 提供时还会返回
`representation_version`。链接按 header relation 分组，默认包含 `count`、最多 10
个排序后的 `{type,id,url}` item 和 `truncated_count`；无法在当前 issue 内解析的
外部引用仍会保留 URL。`--include-all-links` 返回全部 item，且必须与 `--json`
一起使用。

`comment list --active-only --json` 选择所有未 superseded 的 canonical artifact，
其中包括 `done` 和 `confirmed`。`--status` 接受逗号分隔的精确状态集合。
`--history` 选择 superseded artifact，可配合 `--include-body` 显式读取审计详情。
`--active-only` 与 `--history` 互斥。不使用这些新 filter（且不使用
`--include-all-links`）时，原有 list JSON contract 保持不变。

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
    "owned_areas": ["internal/templates/**", "docs/reference.md"],
    "shared_touchpoints": ["internal/model"],
    "dependencies": ["SPEC generator schema"],
    "coupling": "low",
    "execution_mode": "coordinator-owned",
    "complexity": "small"
  }
}
```

Ownership 声明按字面解释。`docs/reference.md` 这样的仓库相对 bare path 只拥有该 exact file；目录 subtree 必须使用显式的尾部 `/**` 声明，例如 `internal/templates/**`。不得用 bare `internal/templates` 暗示拥有其后代路径。包含 bare path 的历史 PROCESS 评论仍可读取，也不会自动迁移，但当该路径解析为 tracked directory 时，`workspace prepare` 可能拒绝它。分配 workspace 前，必须显式修正 PROCESS artifact，或传入修正后的 `--write-ownership internal/templates/**`。

PROCESS 正文记录其父 TASK，并且对于串行链，还记录传给下一节点的交接（handoff）证据：

```json
{
  "title": "extend generators",
  "owner": "Worker Agent A",
  "parent_task": "TASK-001",
  "dependencies": ["N/A"],
  "write_ownership": ["internal/templates/**", "docs/reference.md"],
  "covers": ["TASK-001"],
  "handoff": "state.json contract fixed; successor may parse it"
}
```

被省略的规划字段会渲染为 canonical 默认值（`TBD` / `N/A`），从而让平凡改动保持低摩擦，同时这些小节仍然存在以供协调器阅读。

## PROCESS workspace

coordinator 从 typed DAG 选择的精确 PROCESS id 是唯一 workspace selector；prompt 文本和 runner 命令语法都不是 selector。六个由 Coordinator 拥有的生命周期命令必须复用同一组仓库、issue、PROCESS、integration root、workspace root 与 owner token：

```bash
issue-spec workflow workspace prepare   --repo owner/repo --issue 12 --process PROCESS-001 ...
issue-spec workflow workspace inspect   --repo owner/repo --issue 12 --process PROCESS-001 ...
issue-spec workflow workspace complete  --repo owner/repo --issue 12 --process PROCESS-001 --result-commit <sha> ...
issue-spec workflow workspace integrate --repo owner/repo --issue 12 --process PROCESS-001 --expected-head <sha> ...
issue-spec workflow workspace reconcile --repo owner/repo --issue 12 --process PROCESS-001 ...
issue-spec workflow workspace cleanup   --repo owner/repo --issue 12 --process PROCESS-001 ...
```

被分配的 implementation worker 不执行 `complete` 或 `integrate`。它返回一个有界 handoff，其中包含精确 result commit、focused-test 结果，以及在使用时生成的 sealed result receipt。Coordinator 提供 owner token 并执行 `workspace complete --result-file`。该命令会校验精确的 assignment id、digest、generation、schema、base/result revision，以及单 commit、DCO、ownership、changed paths 和 required tests 的 Git 合约，然后记录 result commit 并推进 workspace lifecycle。

每个 implementation 或 review 角色 assignment 都必须携带 `design_context`，并将其纳入 portable assignment digest。该对象包含 canonical Design `source_url`、固定的 `read_mode: complete-issue-body`、固定的 `conflict_policy: design-authoritative-stop`，以及由 Coordinator 编写的 `invariant`、`applicable_decisions`、`implementation_direction`、`must_preserve`、`must_not` 与 `minimum_verification` 字段。workspace compiler 从当前 Implement issue 的 canonical predecessor chain 推导 Design URL；source 缺失、歧义或不匹配时 fail closed。结构化值在输出时不排序、不摘要，也不重新解释。

修改或评审代码前，被分配的角色必须执行 `issue-spec read issue --repo owner/repo --issue <design_context.source_url>`，只读取完整 Design issue body，不扩展 comments、timeline、history 或 gate。若 Design 正文与结构化 projection 冲突，角色必须停止并报告冲突。`CODEX_THREAD_ID`、`--agent-session` 等 runtime session 值只是可选 audit metadata，不是 Design authority 或 correctness input。

导入的 result file 不是身份或 provenance 信任根。其 writer、subject、逻辑 agent 名称、`CODEX_THREAD_ID`、`--agent-session`、credential 与 assurance label 都只是信息，不能创建 accepted implementation receipt authority，也不能满足非 Coordinator/独立性 gate。unverified import 与保留的 assurance 值可以通过结构校验，但得到的 PROCESS workspace 不包含 accepted-implementation-receipt marker。runtime-attested Coordinator import 明确推迟到存在真实 runtime attestation 信任根之后；本流程不引入 signer、secret 或由调用方命名的 attestor interface。

现有的窄范围直接 role-owned publication 命令继续为 rationale、review 与 verification 提供兼容路径。例如 verification 角色针对精确 assignment 与 snapshot 调用 `verify submit --agent Verifier`。agent/session 字段始终是 `self-reported` metadata，不会因为来自 `CODEX_THREAD_ID` 或显式参数而变成更强的 evidence。`verify submit` 不包含 Coordinator 侧的 owner-token import。

`change-bearing` 使用可写的独占分支；`review` 与 `verification` 使用 detached immutable workflow snapshot，dirty 状态会 fail closed，但 CLI 不会为每个 child 创建 OS 强制的独立 sandbox；`orchestration` 只记录生命周期账本，不创建 checkout。`external` 使用 mode `none`；完成该 PROCESS 并通过 final gate 需要已消费的 provider-neutral exact-revision evidence。

runner 命令不携带 PROCESS selector。runner 只启动一个 ACPX coordinator，并让它的 cwd 与主 sandbox workspace 始终保持在 public session clone。coordinator 从 typed DAG 选择 ready PROCESS，再调用 workspace CLI。runner 模式通过 `ISSUE_SPEC_PROCESS_INTEGRATION_ROOT` 和 `ISSUE_SPEC_PROCESS_WORKSPACE_ROOT` 提供可信的 session-local 默认值；standalone coordinator 则显式传入 roots。

`prepare` 完成后，coordinator 使用当前 agent runtime 的原生 child/subagent 机制分发工作，并传入精确 worktree 路径作为 cwd，同时传入 branch、write ownership、PROCESS id、parent TASK 与前序 handoff。该 child 不是另一个 ACPX session；它共享 coordinator 的 runner 外层 sandbox，自行生成 result commit、执行 focused tests、seal 可选 receipt，并返回这个有界 handoff。coordinator 从未改变的 session clone 执行 `complete` 与 `integrate` 时重新校验精确 Git 结果，再同步状态并 cleanup；导入 receipt 并不能证明是谁生成了它。verification 保留直接 role-owned `verify submit` 兼容路径，在发布 accepted VERIFY 评论前校验精确 snapshot 与 required evidence，不再使用 Coordinator-owned sidecar import。

runner resume 或 restart 后，top-level runner 只恢复 ACPX/session job。PROCESS 生命周期由 coordinator 所有：它从未改变的 session clone 对精确 lease 执行 `inspect` 或 `reconcile`，再执行 `complete` 与 `integrate`，并且只在显式完成 integration 或 retention 决策后调用 owner-token cleanup。top-level runner 的 session-clone retention 会调用 `git worktree list`；当 runner metadata 为 dirty 或 uncertain、存在 linked worktree，或 git worktree inspection 失败时都会 fail closed 并保留 clone。它不拥有、持久化或重试 child PROCESS cleanup。

`workflow workspace cleanup` 始终是显式的 owner-token 授权破坏性操作。它可能删除尚未集成的 change-bearing 工作，也不会替调用者判断或强制执行 integration/retention eligibility，因此只能在调用者完成该决策后使用。

## 已接受 receipt 的投影

`workflow reconcile --projection` 会把已经由角色命令接受的 receipt 引用编译成现有的确定性 transition/link plan，并复用原有 checkpoint engine：

```bash
issue-spec workflow reconcile \
  --projection receipt-projection.json \
  --allow-nonatomic \
  --checkpoint .issue-spec/reconcile/receipt-projection.json \
  --json
```

版本 1 的 projection 只承载身份，不接受 receipt 内容、subject revision、provenance、assurance、任意正文或 provider mutation：

```json
{
  "version": 1,
  "repo": "owner/repo",
  "hostname": "github.com",
  "proposal": 101,
  "issue": 103,
  "accepted_receipts": [
    {
      "role": "review",
      "carrier": {"type": "REVIEW", "id": "REVIEW-001"},
      "receipt_id": "receipt-review-1",
      "receipt_digest": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
      "generation": 1,
      "lifecycle": [
        {"target": {"type": "REVIEW", "id": "REVIEW-001"}, "status": "done"}
      ],
      "coverage_targets": [
        {"type": "PROCESS", "id": "PROCESS-002"},
        {"type": "SPEC", "id": "SPEC-001"}
      ],
      "current_targets": [
        {"type": "PROCESS", "id": "PROCESS-002"}
      ]
    }
  ]
}
```

任何写入发生前，reconcile 都会观察 carrier 上紧凑且不可变的 accepted-receipt marker，并要求 role、receipt id、digest、assignment generation 以及 carrier 现有的 `done` 状态完全匹配。因此唯一的 lifecycle 条目是断言，而不是转换 carrier 的授权：projection 不能完成、supersede、降级或重新打开 carrier，也不能这样修改相关 `PROCESS`/`TASK`。review carrier 必须是 `REVIEW`，verification carrier 必须是 `VERIFY`，implementation carrier 必须是 `PROCESS`。在 PROCESS 尚未包含 issue-native accepted-implementation-receipt marker 时，implementation projection 会 fail closed；workspace assignment/result 状态不能替代该 marker。

关系类型是固定的：implementation 的 coverage target 为 `TASK`/`SPEC`，current target 为 `TASK`；review 与 verification 的 coverage target 为 `PROCESS`/`SPEC`，current target 为 `PROCESS`。版本 1 不接受调用方提供的 provider evidence URL；rationale、finding 与 check 只能留在角色自有 carrier 或 provider 权威观察中。生产 reconcile 会把声明的 proposal 绑定到规范的 proposal/design/implementation issue，从 accepted carrier 的不可变身份、SpecRef 与受管 assignment coverage 推导获得授权的精确 target ID，并在对应的绑定 issue 上为每个 ID 解析唯一的 provider comment URL。carrier 缺失或重复、change/issue 错误、同类型但未获授权的 ID、URL 不匹配都会在任何写入前 fail closed。解析后的 plan 会记录精确的 authority 表示摘要、assignment 身份和 provider URL，因此 authority 过期时重试也会 fail closed。reconcile 只在可变 target 上补齐缺失的反向链接；它绝不改写 accepted carrier 字节，carrier 也不需要预先写入 target URL。

非原子 provider fallback 默认关闭。GitHub projection 写入必须通过 `--allow-nonatomic`（或 projection 顶层的 `"allow_nonatomic": true`）显式选择。编译后的 plan 会把该策略纳入 digest。每次 fallback mutation 都会重新进行一次精确观察，将当前 representation digest 与计算 mutation 所依据的正文比较，并且只有在写后精确观察完全匹配时才写入 checkpoint。因此复用相同的已编译 plan 与 checkpoint 仍沿用原有的 digest 绑定重试路径；由于不支持 conditional mutation 的 backend 无法消除最后的写入竞态，结果仍会标记为 non-atomic。

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
