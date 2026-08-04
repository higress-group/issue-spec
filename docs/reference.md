# Workflow configuration and CLI reference

**English | [简体中文](reference.zh-CN.md)**

[Back to the project README](../README.md)

## Project Workflow Configuration

Projects can customize issue-spec workflow instructions and templates without moving active change state back into repository change directories.

Discovery order:

1. `issue-spec/config.yaml` with project schemas under `issue-spec/schemas/<schema>/schema.yaml`.
2. Legacy `openspec/config.yaml` with schemas under `openspec/schemas/<schema>/schema.yaml`, only when no preferred issue-spec config exists.
3. Built-in issue-spec workflow.

Schema templates are resolved from the selected schema's `templates/` directory. Template paths must be relative, must not escape the schema template directory, and must exist before issue-spec uses them. Active proposal/design/implement content and SPEC/TASK/PROCESS/QUESTION typed comments remain in GitHub issue-native storage. Historical REVIEW/VERIFY comments, rationale, and findings remain readable audit data only. Legacy OpenSpec outputs such as `proposal.md`, `specs/**/*.md`, `tasks.md`, `review.md`, and `verify.md` are treated as storage mapping hints, not active files to write.

Validate or inspect the selected workflow before writing artifacts:

```bash
issue-spec workflow validate --repo owner/repo --json
issue-spec workflow which --repo owner/repo --json
```

New durable specs default to `issue-spec/specs/<capability>/spec.md`. If `openspec/specs/<capability>/spec.md` already exists, `durable-spec` can update that legacy durable spec and reports the compatibility path selection.

### HTML review authoring

HTML review authoring is enabled by default. To keep Proposal, Design, and Implement authoring on the typed Markdown workflow without loading or generating human-review projection instructions, configure the required boolean explicitly:

```yaml
# issue-spec/config.yaml
html_review:
  enabled: false
```

When `html_review` is absent, `enabled` resolves to `true` for backward compatibility. If the mapping is present, `enabled` is required and must be a boolean; scalar values, missing `enabled`, non-boolean values, and unknown fields fail workflow validation before generation or issue creation mutates anything.

With `enabled: false`, `issue-spec init` omits the projection checkpoints from generated skills, slash commands, and prompts, does not emit the embedded `human-review-projections.md` resource, and removes only that exact managed stale resource from an earlier enabled generation. Built-in Proposal, Design, and Implement issue bodies omit their Human Review Projection sections. Custom workflow templates and explicit `--body-file` content remain authoritative.

This setting controls repository authoring guidance only. Typed planning artifacts, stored historical REVIEW/VERIFY audit data, projection comments, HTML preview parsing/storage, and Web preview execution are unchanged. Current provider-native review, checks, approval, and merge remain in the provider UI under human control.

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

Design and Implement creation validate the explicit predecessor phase and change lineage, but do not gate authoring on optional SPEC, QUESTION, TASK, PROCESS, or relationship state. Planning status validates selected artifacts that exist; it does not require omitted optional artifact types.

issue-spec comment create --repo owner/repo --issue 1 --body-file reply.md --json
issue-spec comment edit --repo owner/repo --comment-id 123 --body-file reply.md --json
issue-spec comment delete --repo owner/repo --comment-id 123 --json
issue-spec comment generate --type SPEC --id SPEC-1001 --status confirmed --scope "canonical SPEC generation" --input-file spec.json
issue-spec comment upsert --repo owner/repo --issue 1 --type SPEC --id SPEC-1001 --body-file spec.md
issue-spec comment upsert --repo owner/repo --issue 1 --type SPEC --id SPEC-1001 --body-file legacy.md --allow-noncanonical
issue-spec comment get --repo owner/repo --issue 1 --id SPEC-1001 --type SPEC --json
issue-spec comment get --repo owner/repo --issue 1 --id SPEC-1001 --comment-id 123 --include-body --json
issue-spec comment list --repo owner/repo --issue 1 --json
issue-spec comment list --repo owner/repo --issue 1 --type SPEC --json --include-body
issue-spec comment list --repo owner/repo --issue 1 --active-only --json
issue-spec comment list --repo owner/repo --issue 1 --status ready,in-progress,done --json
issue-spec comment list --repo owner/repo --issue 1 --history --include-body --json

issue-spec question create --repo owner/repo --issue 1 --id QUESTION-1001 --blocking --question "What must be decided?"
issue-spec question answer --repo owner/repo --issue 1 --id ANSWER-1001 --question-id QUESTION-1001 --select option-id --json
issue-spec question answer --repo owner/repo --issue 1 --id ANSWER-1002 --question-id QUESTION-1001 --custom "A different answer" --json
issue-spec question resolve --repo owner/repo --issue 1 --id QUESTION-1001 --resolution-file resolution.md

issue-spec link --repo owner/repo --from SPEC-1001 --from-issue 1 --to TASK-2001 --to-issue 2
issue-spec status --repo owner/repo --proposal 1 --design 2 --implement 3
issue-spec verify-links --repo owner/repo --proposal 1 --design 2 --implement 3

issue-spec workflow validate --repo owner/repo --json
issue-spec workflow which --repo owner/repo --schema custom-workflow --json

issue-spec search issues --repo owner/repo --query "error or symbol" --state all --source all --limit 10
```

New typed IDs are repository-unique and use `<TYPE>-<issue><three-digit sequence>`.
The final three digits are a sequence allocated within one Issue and type; all preceding
digits are the repository-unique Issue number. For example, the first QUESTION on Issue 44
is `QUESTION-44001`. The type prefix already separates artifact types, so no repository-wide
availability search or additional type digit is needed. Preserve legacy IDs because links,
ANSWER scope, and history may already reference them.

For a self-hosted profile, `question answer` confirms the current QUESTION through that
profile's validated native API and submits only its current digest plus the selected option
IDs or custom text. The server creates the canonical ANSWER; JSON output reports that
server-generated `id` and includes the caller's distinct `--id` as `requested_id`.
GitHub-backed profiles retain the existing append-only typed-comment behavior and use the
caller-provided `--id` as the ANSWER identity.

```bash

issue-spec --profile team code-change attach --repo acme/widgets --implement 3 --change-id 42 --revision abc123 [--refresh --expected-version 7] [--json]
issue-spec --profile team code-change link-process --repo acme/widgets --implement 3 --process PROCESS-3001 --expected-version 5 [--json]

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
  --process PROCESS-3001 \
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

Provider-aware delivery stops after the exact pushed head and PR/MR are ready
for human review. Report the head, change URL, tests, rationale, known risks,
and unsupported provider operations. The human reads current provider-native
CI, approvals, conversations, ownership, and branch policy and decides whether
to merge in the provider UI.

`comment create` writes an ordinary issue timeline comment through the selected
hosted GitHub or self-hosted REST issue backend. It accepts `--body-file -` for
stdin pipelines and, with `--json`, returns only bounded creation metadata such
as the comment ID and URL. It does not add a typed marker or validate the body
as a workflow artifact.

`comment edit` and `comment delete` locate exactly one comment by repository and
positive provider comment ID. Edit requires a non-empty `--body-file` and uses
the selected backend's existing PATCH operation. Delete requires the selected
backend to advertise comment deletion and sends a DELETE request; it is
irreversible. Both commands validate their inputs before backend selection and
return only bounded action, repository, and comment-ID metadata, never the
comment body or credential. A self-hosted profile uses only its configured REST
origin and never probes `gh`.

`comment list --json` keeps its existing parsed-artifact schema by default.
Adding `--include-body` gives each returned artifact a top-level `body` field
containing the exact original backend Markdown; the flag requires `--json`.
Type filtering and canonical diagnostics are unchanged in either mode, and no
matches are encoded as `[]` rather than `null`.

`comment get --issue N --id PROCESS-001 --json` returns one bounded typed
artifact rather than the issue timeline. An optional `--type` asserts its type.
When `--comment-id` supplies a prior provider locator and the backend supports
direct observation, the CLI reads that comment directly and verifies its issue,
marker, type, and stable ID. Otherwise it may scan internally, but unrelated
bodies are never returned. Duplicate stable IDs and locator mismatches fail
closed. `--include-body` includes only the target's exact Markdown.

Targeted and explicitly filtered list results include `representation_digest`,
the lowercase SHA-256 of the exact remote Markdown bytes; no newline or
whitespace normalization is performed. `representation_version` is included
when the backend exposes one. Links are grouped by header relation and normally
contain `count`, at most 10 sorted `{type,id,url}` items, and
`truncated_count`; unresolved external references still retain their URL.
`--include-all-links` returns every item and requires `--json`.

`comment list --active-only --json` selects every non-superseded canonical
artifact, including `done` and `confirmed`. `--status` accepts a comma-separated
set of exact statuses. `--history` selects superseded artifacts and can be
combined with `--include-body` for explicit audit detail. `--active-only` and
`--history` are mutually exclusive. With none of these new filters (and without
`--include-all-links`), the existing list JSON contract remains unchanged.

## Canonical Typed Comments

Typed comments carry requirements, tasks, and optional implementation ownership across coordinator handoffs. Instead of hand-writing raw Markdown, use `issue-spec comment generate` to render canonical bodies from structured JSON, then pipe the output straight into `comment upsert`:

```bash
issue-spec comment generate --type SPEC --id SPEC-1001 --status confirmed --scope "canonical SPEC generation" --input-file spec.json \
  | issue-spec comment upsert --repo owner/repo --issue 1 --type SPEC --id SPEC-1001 --body-file -
```

`comment generate` writes a complete typed-comment Markdown body (marker + visible header + canonical content) to stdout and never touches the network. The command family renders `SPEC`, `TASK`, and `PROCESS` bodies with type-specific JSON shapes; REVIEW and VERIFY generation has been removed.

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

The exact PROCESS id selected by the coordinator from the typed DAG is the workspace selector; prompt text and runner command grammar are never selectors. Use the same repository, issue, PROCESS, integration root, workspace root, and owner token across the six Coordinator-owned lifecycle commands:

```bash
issue-spec workflow workspace prepare   --repo owner/repo --issue 12 --process PROCESS-001 ...
issue-spec workflow workspace inspect   --repo owner/repo --issue 12 --process PROCESS-001 ...
issue-spec workflow workspace complete  --repo owner/repo --issue 12 --process PROCESS-001 --result-commit <sha> ...
issue-spec workflow workspace integrate --repo owner/repo --issue 12 --process PROCESS-001 --expected-head <sha> ...
issue-spec workflow workspace reconcile --repo owner/repo --issue 12 --process PROCESS-001 ...
issue-spec workflow workspace cleanup   --repo owner/repo --issue 12 --process PROCESS-001 ...
```

The assigned implementation worker does not invoke `complete` or `integrate`. It returns a bounded handoff containing the exact result commit, changed paths, focused-test results, decisions, risks, and useful line-rationale drafts. The Coordinator supplies its owner token and runs `workspace complete --result-commit`. That command validates the one-commit/DCO/ownership Git contract and changed paths before recording the result commit and advancing the workspace lifecycle; the Coordinator reruns configured integration checks before accepting the result.

For every implementation role assignment, `design_context` is required and covered by the portable assignment digest. It contains the canonical Design `source_url`, fixed `read_mode: complete-issue-body`, fixed `conflict_policy: design-authoritative-stop`, and the Coordinator-authored `invariant`, `applicable_decisions`, `implementation_direction`, `must_preserve`, `must_not`, and `minimum_verification` fields. The workspace compiler derives the Design URL from the current Implement issue's canonical predecessor chain and fails closed on a missing, ambiguous, or mismatched source. It emits the structured values without sorting, summarizing, or reinterpreting them.

Before changing or reviewing code, the assigned role reads the complete Design issue body with `issue-spec read issue --repo owner/repo --issue <design_context.source_url>` without comments, timeline, history, or gate expansion. If that body conflicts with the structured projection, the role stops and reports the conflict. Runtime-specific session IDs are not collected or passed as Design authority, audit metadata, or correctness inputs.

Generated guidance follows the same bounded read model. Coordinator guidance selects optional planning by risk, uses exact `comment get` and filtered lists, and selects execution mode before assigning writers. Once Design or TASK is selected or the user requests delegation, the Coordinator writes no code on delegated or managed paths. Without managed PROCESS, exactly one real non-Coordinator worker owns the bounded implementation. With managed PROCESS, every change-bearing work package/PROCESS has one real non-Coordinator owner and distinct packages may use concurrent writers. Direct Coordinator edits remain only for a narrow direct-PR fast path with no selected Design/TASK and no delegation request; file count is not a selector. The Coordinator dispatches and waits, inspects exact result commits, integrates, validates proportionately, checks rationale anchors, pushes one exact reviewable head, creates or selects its PR/MR, reports the human handoff, and stops before approval or merge. The implementation role section contains only its sealed assignment, authoritative Design read, owned invariant/work, focused checks, and bounded result responsibilities. Complete proposal/Design copies, full DAGs, link matrices, closure/archive policy, and provider-routing policy stay out of role sections. Deterministic regression budgets count UTF-8 bytes, headings/fields, and instruction-array items; they never use a model tokenizer or justify removing an uncovered safety rule.

Before that handoff, a real read-only reviewer independent of every code writer checks the exact base and current exact head without write access or provider credentials. P0/P1 findings return unchanged to the original writer that owns the affected code; after repair, focused tests, integration, and push, the same reviewer rechecks the new head. This repeats automatically until zero P0/P1 remain. Only still-applicable P2 findings from the final reviewed head are published unchanged, using a safe provider-native non-blocking line comment when supported or the provider-neutral ordinary `change.comment` operation with `path:symbol/line` otherwise. P2 never enters the repair loop or pauses completion; unavailable or failed publication is reported with the rendered body while the workflow continues. The loop creates no typed REVIEW/VERIFY, finding evidence, receipt, readiness gate, approval, or merge authority and does not require PROCESS without a separate managed-coordination need.

Compatibility is deliberately asymmetric. A pre-D14 version-1 assignment without `design_context` remains readable in an existing local registry or PROCESS workspace so inspection, cleanup, and recovery do not declare the registry corrupt. That historical object is read-only compatibility evidence: strict implementation issuance, redispatch digests, assignment-file parsing, and new implementation submissions still reject it. The CLI never synthesizes missing Design context.

An imported result file is not an identity or provenance trust root. Its writer, subject, logical agent name, credentials, and assurance labels are informational and cannot create accepted implementation receipt authority or satisfy a non-Coordinator/independence gate. Unverified imports and reserved assurance values may be structurally validated, but the resulting PROCESS workspace contains no accepted-implementation-receipt marker. Runtime-attested Coordinator import is explicitly deferred until a real runtime attestation trust root exists; there is no signer, secret, or caller-named attestor interface in this workflow.

Legacy rationale, review publication/synchronization, verification submission/finalization, and Archive commands return `deprecated_workflow` before mutation. Their historical artifacts remain explicit audit-read data and never become delivery acceptance.

The replacement for review explanation is not another issue-spec evidence command. The delegated worker, narrow Coordinator fast-path writer, or each managed PROCESS worker owns zero or more line-rationale drafts for non-obvious decisions, identified by repository-relative path, stable symbol plus changed-line anchor, and why/tradeoff/risk—not a provider diff position—and containing no secret, raw payload, or credential. After the exact head is integrated and pushed, the Coordinator validates and maps those anchors, continued applicability, and sensitive-data absence, then publishes unchanged worker-authored text as non-blocking inline provider discussions. Invalid, stale, or sensitive drafts return to the writer or are dropped with explanation, never Coordinator-rewritten under worker authorship. The ordinary top-level `### Implementation Rationale` discussion summarizes and indexes them. If safe inline discussion is unavailable or would create an unresolved merge blocker, it instead preserves `path:symbol/line` plus the worker text in that top-level discussion. Obvious code creates no draft or quota. These discussions have no marker, rationale ID, typed carrier, PROCESS/SPEC binding, evidence field, gate, or merge effect. A required provider write failure is reported with the rendered body for retry or manual publication.

`change-bearing` uses a writable owned branch. Historical `review` and `verification` execution classes remain parseable for audit but generation and workspace mutation reject them. `orchestration` records lifecycle bookkeeping without a checkout. `external` uses mode `none`. Their completion state is implementation bookkeeping and never a merge gate.

Runner commands never carry a PROCESS selector. The runner launches exactly one ACPX coordinator and keeps its cwd and primary sandbox workspace at the public session clone. The coordinator selects a ready PROCESS from the typed DAG and invokes the workspace CLI. Runner mode supplies trusted session-local defaults through `ISSUE_SPEC_PROCESS_INTEGRATION_ROOT` and `ISSUE_SPEC_PROCESS_WORKSPACE_ROOT`; a standalone coordinator passes explicit roots.

After `prepare`, the coordinator delegates through the current agent runtime's native child/subagent facility, passing the exact worktree path as cwd plus the branch, write ownership, PROCESS id, parent TASK, and predecessor handoff. The child is not another ACPX session. It shares the coordinator's outer runner sandbox, authors one result commit, runs focused tests, and returns that bounded handoff without creating a role receipt. The coordinator revalidates the exact Git result while running `complete --result-commit` and `integrate` from its unchanged session clone before synchronizing status and cleanup. These implementation controls never become delivery acceptance.

After runner resume or restart, the top-level runner recovers only the ACPX/session job. From the unchanged session clone, the coordinator owns the PROCESS lifecycle: it uses `inspect` or `reconcile` on the exact lease before `complete` and `integrate`, then invokes owner-token cleanup only after an explicit integration or retention decision. Top-level runner session-clone retention calls `git worktree list` and fails closed by retaining the clone when runner metadata is dirty or uncertain, a linked worktree exists, or git worktree inspection fails. It does not own, persist, or retry child PROCESS cleanup.

`workflow workspace cleanup` is always an explicit owner-token-authorized destructive operation. It can remove unintegrated change-bearing work and does not decide or enforce integration/retention eligibility for its caller, so invoke it only after making that decision.

## Historical receipt audit

Previously stored accepted-receipt markers and REVIEW/VERIFY carriers remain
available through explicit comment and receipt inspection. The removed
`workflow reconcile --projection` writer cannot project them into lifecycle or
relationship state, and no receipt can contribute to merge readiness.

### Canonical validation by default

`comment upsert` validates canonical discipline by default before creating or updating the remote comment:

- **SPEC** — rejects bodies missing a `## Requirement:` heading, normative MUST/SHALL language, or `### Scenario:` `**WHEN**`/`**THEN**` bullets.
- **TASK** — rejects bodies missing a `## Task:` heading or the `### Execution Planning` section.
- **PROCESS** — rejects bodies missing a `## Process:` heading or a `### Parent TASK` section.

Serial-chain `### Handoff` evidence is not required at write time (only chains need it, which is not knowable per-comment at upsert). Planning status evaluates it when the Implement gate is requested. One shared validator in `internal/model` is reused by `comment upsert`, `comment list`, and planning status, operating on the logical body after marker/header stripping so raw generated bodies and already-wrapped bodies behave identically.

### Migration escape hatch

`--allow-noncanonical` is a **write-time migration bypass only**. It lets you write a malformed SPEC body for staged migration, but it does **not** create durable approval:

- The write is marked `noncanonical` in command output.
- `comment list`, planning `status`, and `durable-spec check` recompute canonical validity from the remote body and keep reporting or blocking on the malformed active comment.
- `durable-spec check` fails while an active SPEC comment remains malformed.

The correct long-term fix is to regenerate the comment into canonical shape with `comment generate`, or supersede it if it is no longer active.
