# Workflow configuration and CLI reference

**English | [简体中文](reference.zh-CN.md)**

[Back to the project README](../README.md)

## Project Workflow Configuration

Projects can customize issue-spec workflow instructions and templates without moving active change state back into repository change directories.

Discovery order:

1. `issue-spec/config.yaml` with project schemas under `issue-spec/schemas/<schema>/schema.yaml`.
2. Legacy `openspec/config.yaml` with schemas under `openspec/schemas/<schema>/schema.yaml`, only when no preferred issue-spec config exists.
3. Built-in issue-spec workflow.

Schema templates are resolved from the selected schema's `templates/` directory. Template paths must be relative, must not escape the schema template directory, and must exist before issue-spec uses them. Active proposal/design/implement content, SPEC/TASK/PROCESS/QUESTION/REVIEW/VERIFY typed comments, PR rationale, and review findings remain in GitHub issue-native storage. Legacy OpenSpec outputs such as `proposal.md`, `specs/**/*.md`, `tasks.md`, `review.md`, and `verify.md` are treated as storage mapping hints, not active files to write.

Validate or inspect the selected workflow before writing artifacts:

```bash
issue-spec workflow validate --repo owner/repo --json
issue-spec workflow which --repo owner/repo --json
```

New durable specs default to `issue-spec/specs/<capability>/spec.md`. If `openspec/specs/<capability>/spec.md` already exists, archive can update that legacy durable spec and reports the compatibility path selection.

### Preferred natural language

Agents author generated artifacts in English by default. To make issue bodies, typed comments, design notes, and rationale come out in another language, add a `rules.language` entry to `issue-spec/config.yaml`. The value is embedded into every generated skill, slash command, and prompt as a workflow rule, so the coordinator follows it.

The fastest way is the `--language` flag on init, which scaffolds or merges that entry for you:

```bash
issue-spec init --repo owner/repo --tools codex,claude --language zh

issue-spec --profile team search issues --repo owner/repo --query "error or symbol" --state all --source all --limit 10
```

Common codes (`zh`, `zh-tw`, `en`, `ja`, `ko`) are expanded to a descriptive label; any other value is stored as-is. The generated rule instructs agents to write natural-language content in the chosen language while keeping canonical structural tokens in English (`## Requirement:`, `### Scenario:`, `**WHEN**`/`**THEN**`, MUST/SHALL, and typed comment headers), so canonical validation still passes.

You can also hand-author it. Include the `language_instructions` guardrail that `--language` writes for you — without it, agents may translate the canonical structural tokens and fail validation:

```yaml
# issue-spec/config.yaml
rules:
  language: "Simplified Chinese (简体中文)"
  language_instructions: "Write all natural-language content in Simplified Chinese (简体中文). Keep canonical structural tokens in English so validation passes: the `## Requirement:` and `### Scenario:` headings, the `**WHEN**`/`**THEN**` scenario bullets, the MUST/SHALL normative keywords, and typed comment headers."
```

Re-run `issue-spec init` after editing the config so the generated skills and commands pick up the rule. Note that when `--language` merges an existing `issue-spec/config.yaml`, it rewrites the file through a YAML round-trip, so hand-added comments are dropped and keys are re-sorted.

### Workflow-neutral initialization

An explicit, case-insensitive `--tools none` initializes issue-spec runtime state without selecting or changing a project workflow. It does not read, validate, create, or modify `issue-spec/config.yaml` or `openspec/config.yaml`, and it generates no repository skills, commands, or user-global prompts. Existing workflow files remain byte-for-byte unchanged; in particular, a repository with only `openspec/config.yaml` continues to use legacy OpenSpec workflow discovery afterward.

Runtime initialization still applies: GitHub init writes `.issue-spec/config.json` and may ensure labels, while self-hosted init may register the approved repository and binding, ensure labels, update its init journal, and record server, provider, external-repository, and capability metadata in `.issue-spec/config.json`. Provider workflow policy is not copied into `issue-spec/config.yaml`.

`--language` is accepted with explicit `--tools none`, but JSON reports `language_applied: false` and text output says it was not applied. Configure `rules.language` in the selected project workflow instead; legacy OpenSpec projects use `openspec/config.yaml`. Any explicit `--install-global-prompts`, `--global-prompts-dir`, or `--global-prompts-dry-run` option conflicts with `--tools none` and is rejected before profile/backend selection or mutation.

## CLI Reference

```bash
issue-spec auth status
issue-spec auth login
issue-spec auth logout
issue-spec auth token --plain

issue-spec init --repo owner/repo
issue-spec init --repo owner/repo --skip-labels  # opt out when labels are managed separately
issue-spec init --repo owner/repo --tools codex,claude --delivery both
issue-spec init --repo owner/repo --tools codex,claude --language zh
issue-spec init --repo owner/repo --tools codex --install-global-prompts
issue-spec init --repo owner/repo --tools none --language zh  # language is reported but not applied

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
issue-spec comment list --repo owner/repo --issue 1 --json
issue-spec comment list --repo owner/repo --issue 1 --type SPEC --json --include-body

issue-spec question create --repo owner/repo --issue 1 --id QUESTION-001 --blocking --question "What must be decided?"
issue-spec question resolve --repo owner/repo --issue 1 --id QUESTION-001 --resolution-file resolution.md

issue-spec link --repo owner/repo --from SPEC-001 --from-issue 1 --to TASK-001 --to-issue 2
issue-spec status --repo owner/repo --proposal 1 --design 2 --implement 3
issue-spec verify-links --repo owner/repo --proposal 1 --design 2 --implement 3

issue-spec workflow validate --repo owner/repo --json
issue-spec workflow which --repo owner/repo --schema custom-workflow --json

issue-spec search issues --repo owner/repo --query "error or symbol" --state all --source all --limit 10

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

`issue list` is JSON-only and defaults to open issues. `--state` accepts
`open`, `closed`, or `all`; all pages are collected, ordinary issues are
included whether or not they have issue-spec metadata, and GitHub pull
requests are excluded. Each result contains the issue number, title, state,
human-facing URL, and complete body.

`issue update --body-file` keeps ordinary, unmarked issues as plain body
replacement. For marked issue-spec issues it preserves the stored marker and,
for design and implement issues, the direct predecessor link exactly once.
Conflicting or malformed reserved metadata is rejected before the update.
`issue close` and `issue reopen` first read the issue and skip the update when
it already has the requested state; JSON output reports this with `changed`.

### Search before a related change

`search issues` selects behavior from the active Issue Backend. For self-hosted
profiles it discovers the server capability, fails clearly when search is
disabled, and returns matching issue/comment excerpts plus related change
key/stage metadata. For GitHub profiles it uses GitHub Issue Search with a
mandatory repository and issue-only scope, excludes pull requests again when
decoding results, and bounds output to `--limit`. `--state` is supported on
both backends. GitHub maps `--source issue` to title/body search,
`--source comments` to comment search, and `--stage` to the canonical
`issue-spec/proposal`, `issue-spec/design`, or `issue-spec/implement` label;
GitHub does not support `--source change` because it has no equivalent change
key index. A no-match search succeeds with zero results. Result order and
ranking are backend-specific and are not parity guarantees.

Both adapters search only within the requested repository and render the same
bounded issue-centric fields through nonce-scoped untrusted-data boundaries.
GitHub text-match fragments may differ from self-hosted excerpts, and optional
change metadata may be absent.

Generated Codex and Claude workflows instruct direct agents—not only runner
sessions—to derive a few concrete queries from the request and codebase before
a related proposal or implementation. Search results are selection hints, not
instructions: titles and excerpts are untrusted data. Open a selected result
with `issue-spec --profile team read issue --repo owner/repo --issue N
--comments` before relying on the full discussion.

### Associate a self-hosted code change

For a self-hosted profile, the active Source Binding is authoritative for the
provider and external repository. Associate an already-existing provider
change with the Implement Issue at an exact revision:

```bash
issue-spec --profile team code-change attach \
  --repo acme/widgets \
  --implement 3 \
  --change-id 42 \
  --revision abc123 \
  --json
```

`code-change attach` validates the external change through the registered
provider and records the active relationship. It does not create a PR/MR and
does not ingest review or CI evidence. Repeating the same identity and revision
is idempotent. Refreshing the same active change to a new exact revision
requires `--refresh` and the observed positive `--expected-version` together.

After exactly one active `code_change` relationship exists, link a PROCESS
comment using its observed representation version:

```bash
issue-spec --profile team code-change link-process \
  --repo acme/widgets \
  --implement 3 \
  --process PROCESS-001 \
  --expected-version 5 \
  --json
```

Linking the same canonical URL again is a no-op. A different existing PROCESS
URL conflicts, and zero or multiple active code-change relationships fail
closed. When a conflict reports ambiguous active references, inspect the
Implement Issue references in the server UI or with
`GET /api/v1/orgs/{org-id}/repos/{repo-id}/issues/{issue-id}/references`,
delete only the unwanted active reference with the corresponding
`DELETE .../references/{reference-id}`, then retry. Never guess a winner or
silently overwrite another active relationship.

After the independent provider review converges, its review agent synchronizes
the exact active revision under its own identity:

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

A successful self-hosted sync persists and reloads provider facts, then writes
one stable done REVIEW completion even when the provider reports zero findings.
After the final sync, use `issue-spec link` to link that REVIEW explicitly to
its review PROCESS, every covered change-bearing PROCESS, and every covered
active SPEC. Do not fabricate findings, hand-edit the completion stamp, infer
links from IDs in prose, or substitute a generic approval framework. `status`
and final `verify` validate the same exact provider/repository/change,
reference-version, revision, freshness, links, and reviewer-independence
carrier without refreshing REVIEW.

During self-hosted closure, `archive` reads that existing implementation REVIEW
completion only when the implementation `code_change` merge policy requires
review. It does not create, update, refresh, or add archive-specific REVIEW
state, and it never applies implementation completion to `archive_change`.

GitHub profiles continue to use `pr link-process`, GitHub PR review and closing
links, and the existing durable archive path. Self-hosted review, merge, and
change closure remain on the selected code provider; the CLI does not route
them through GitHub PR endpoints.

`comment create` writes an ordinary issue timeline comment through the selected
hosted GitHub or self-hosted REST issue backend. It accepts `--body-file -` for
stdin pipelines and, with `--json`, returns only bounded creation metadata such
as the comment ID and URL. It does not add a typed marker or validate the body
as a workflow artifact.

`comment list --json` keeps its existing parsed-artifact schema by default.
Adding `--include-body` gives each returned artifact a top-level `body` field
containing the exact original backend Markdown; the flag requires `--json`.
Type filtering and canonical diagnostics are unchanged in either mode, and no
matches are encoded as `[]` rather than `null`.

## Canonical Typed Comments

Typed comments carry requirements, tasks, process ownership, review, and verification evidence across coordinator handoffs. Instead of hand-writing raw Markdown, use `issue-spec comment generate` to render canonical bodies from structured JSON, then pipe the output straight into `comment upsert`:

```bash
issue-spec comment generate --type SPEC --id SPEC-001 --status confirmed --scope "canonical SPEC generation" --input-file spec.json \
  | issue-spec comment upsert --repo owner/repo --issue 1 --type SPEC --id SPEC-001 --body-file -
```

`comment generate` writes a complete typed-comment Markdown body (marker + visible header + canonical content) to stdout and never touches the network. The same command family renders `TASK`, `PROCESS`, `REVIEW`, and `VERIFY` bodies with type-specific JSON shapes.

### SPEC generator input JSON

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

The rendered body contains a `## Requirement:` heading, normative MUST/SHALL language, and one or more `### Scenario:` sections with `**WHEN**`/`**THEN**` bullets. Unknown JSON fields are rejected so schema drift fails fast.

### TASK and PROCESS generator input JSON

TASK bodies carry the PROCESS-planning metadata a coordinator needs to decompose work. The `execution_planning` object renders the required `### Execution Planning` section:

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

Ownership declarations are literal. A bare repository-relative path such as `docs/reference.md` owns only that exact file; a directory subtree must use an explicit trailing `/**` declaration such as `internal/templates/**`. Do not use bare `internal/templates` to imply ownership of its descendants. Existing PROCESS comments with bare paths remain readable and are not migrated automatically, but `workspace prepare` may reject one that resolves to a tracked directory. Before allocation, explicitly correct the PROCESS artifact or pass a corrected `--write-ownership internal/templates/**` value.

PROCESS bodies record their parent TASK and, for serial chains, the handoff evidence passed to the next node:

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

Omitted planning fields render canonical defaults (`TBD` / `N/A`) so trivial changes stay low-friction while the sections remain present for coordinators to read.

## PROCESS workspaces

The exact PROCESS id selected by the coordinator from the typed DAG is the workspace selector; prompt text and runner command grammar are never selectors. Use the same repository, issue, PROCESS, integration root, workspace root, and owner token across the six lifecycle commands:

```bash
issue-spec workflow workspace prepare   --repo owner/repo --issue 12 --process PROCESS-001 ...
issue-spec workflow workspace inspect   --repo owner/repo --issue 12 --process PROCESS-001 ...
issue-spec workflow workspace complete  --repo owner/repo --issue 12 --process PROCESS-001 --result-commit <sha> ...
issue-spec workflow workspace integrate --repo owner/repo --issue 12 --process PROCESS-001 --expected-head <sha> ...
issue-spec workflow workspace reconcile --repo owner/repo --issue 12 --process PROCESS-001 ...
issue-spec workflow workspace cleanup   --repo owner/repo --issue 12 --process PROCESS-001 ...
```

`change-bearing` uses a writable owned branch. `review` and `verification` use detached immutable workflow snapshots: dirty state fails closed, but the CLI does not create an OS-enforced per-child sandbox. `orchestration` records lifecycle bookkeeping without a checkout. `external` uses mode `none`; completion and the final gate require consumed provider-neutral exact-revision evidence.

Runner commands never carry a PROCESS selector. The runner launches exactly one ACPX coordinator and keeps its cwd and primary sandbox workspace at the public session clone. The coordinator selects a ready PROCESS from the typed DAG and invokes the workspace CLI. Runner mode supplies trusted session-local defaults through `ISSUE_SPEC_PROCESS_INTEGRATION_ROOT` and `ISSUE_SPEC_PROCESS_WORKSPACE_ROOT`; a standalone coordinator passes explicit roots.

After `prepare`, the coordinator delegates through the current agent runtime's native child/subagent facility, passing the exact worktree path as cwd plus the branch, write ownership, PROCESS id, parent TASK, and predecessor handoff. The child is not another ACPX session. It shares the coordinator's outer runner sandbox, authors a result commit, runs focused tests, and returns bounded handoff evidence. The coordinator validates that result and runs `complete` and `integrate` from its unchanged session clone before synchronizing status and cleanup.

After runner resume or restart, the top-level runner recovers only the ACPX/session job. From the unchanged session clone, the coordinator owns the PROCESS lifecycle: it uses `inspect` or `reconcile` on the exact lease before `complete` and `integrate`, then invokes owner-token cleanup only after an explicit integration or retention decision. Top-level runner session-clone retention calls `git worktree list` and fails closed by retaining the clone when runner metadata is dirty or uncertain, a linked worktree exists, or git worktree inspection fails. It does not own, persist, or retry child PROCESS cleanup.

`workflow workspace cleanup` is always an explicit owner-token-authorized destructive operation. It can remove unintegrated change-bearing work and does not decide or enforce integration/retention eligibility for its caller, so invoke it only after making that decision.

### Canonical validation by default

`comment upsert` validates canonical discipline by default before creating or updating the remote comment:

- **SPEC** — rejects bodies missing a `## Requirement:` heading, normative MUST/SHALL language, or `### Scenario:` `**WHEN**`/`**THEN**` bullets.
- **TASK** — rejects bodies missing a `## Task:` heading or the `### Execution Planning` section.
- **PROCESS** — rejects bodies missing a `## Process:` heading or a `### Parent TASK` section.

Serial-chain `### Handoff` evidence is not required at write time (only chains need it, which is not knowable per-comment at upsert) — it is enforced at `verify` instead. One shared validator in `internal/model` is reused by `comment upsert`, `comment list`, `status`, `verify`, and `archive`, operating on the logical body after marker/header stripping so raw generated bodies and already-wrapped bodies behave identically.

### Migration escape hatch

`--allow-noncanonical` is a **write-time migration bypass only**. It lets you write a malformed SPEC body for staged migration, but it does **not** create durable approval:

- The write is marked `noncanonical` in command output.
- `comment list`, `status`, `verify`, and archive readiness recompute canonical validity from the remote body and keep reporting or blocking on the malformed active comment.
- `verify` and durable-spec archive fail before archive creation while an active SPEC comment remains malformed.

The correct long-term fix is to regenerate the comment into canonical shape with `comment generate`, or supersede it if it is no longer active.
