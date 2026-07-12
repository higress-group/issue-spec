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

## CLI 参考

```bash
issue-spec auth status
issue-spec auth login
issue-spec auth logout
issue-spec auth token --plain

issue-spec init --repo owner/repo --create-labels
issue-spec init --repo owner/repo --tools codex,claude --delivery both
issue-spec init --repo owner/repo --tools codex,claude --language zh

issue-spec issue create proposal --repo owner/repo --change my-change --body-file proposal.md [--title "Custom proposal title"]
issue-spec issue create design --repo owner/repo --change my-change --proposal 1 --body-file design.md [--title "Custom design title"]
issue-spec issue create implement --repo owner/repo --change my-change --proposal 1 --design 2 --body-file implement.md [--title "Custom implementation title"]
issue-spec issue update --repo owner/repo --issue 1 --body-file proposal.md --summary "Clarified goals after review."

issue-spec comment generate --type SPEC --id SPEC-001 --status confirmed --scope "canonical SPEC generation" --input-file spec.json
issue-spec comment upsert --repo owner/repo --issue 1 --type SPEC --id SPEC-001 --body-file spec.md
issue-spec comment upsert --repo owner/repo --issue 1 --type SPEC --id SPEC-001 --body-file legacy.md --allow-noncanonical
issue-spec comment list --repo owner/repo --issue 1 --json

issue-spec question create --repo owner/repo --issue 1 --id QUESTION-001 --blocking --question "What must be decided?"
issue-spec question resolve --repo owner/repo --issue 1 --id QUESTION-001 --resolution-file resolution.md

issue-spec link --repo owner/repo --from SPEC-001 --from-issue 1 --to TASK-001 --to-issue 2
issue-spec status --repo owner/repo --proposal 1 --design 2 --implement 3
issue-spec verify-links --repo owner/repo --proposal 1 --design 2 --implement 3

issue-spec workflow validate --repo owner/repo --json
issue-spec workflow which --repo owner/repo --schema custom-workflow --json

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
    "owned_areas": ["internal/templates"],
    "shared_touchpoints": ["internal/model"],
    "dependencies": ["SPEC generator schema"],
    "coupling": "low",
    "execution_mode": "coordinator-owned",
    "complexity": "small"
  }
}
```

PROCESS 正文记录其父 TASK，并且对于串行链，还记录传给下一节点的交接（handoff）证据：

```json
{
  "title": "extend generators",
  "owner": "Worker Agent A",
  "parent_task": "TASK-001",
  "dependencies": ["N/A"],
  "write_ownership": ["internal/templates"],
  "covers": ["TASK-001"],
  "handoff": "state.json contract fixed; successor may parse it"
}
```

被省略的规划字段会渲染为 canonical 默认值（`TBD` / `N/A`），从而让平凡改动保持低摩擦，同时这些小节仍然存在以供协调器阅读。

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
