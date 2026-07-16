# acpx-comment-triggered-workflow

## Purpose

Document the long-lived behavior contract for running issue-spec workflows from GitHub issue comments through the issue-spec runner, acpx, and a sandboxed coordinator agent.

Proposal Issues:
- https://github.com/higress-group/issue-spec/issues/145
- https://github.com/higress-group/issue-spec/issues/24

## Requirements

### Requirement: comment runner intake

Issue-spec MUST provide a runner mode that discovers issue comment commands for configured repositories and turns accepted commands into durable runner jobs.

#### Scenario: notification-backed polling

- **WHEN** the runner starts polling configured repositories
- **THEN** it SHALL load durable state, run preflight checks, verify the authenticated runner identity and repository notification subscription, and poll GitHub notifications as the primary intake path.

#### Scenario: repository comment fallback

- **WHEN** notifications do not surface a relevant issue comment or the repository fallback cadence is due
- **THEN** the runner SHALL poll repository issue comments with stored cursors and conditional validators at a lower-frequency fallback cadence.

#### Scenario: conditional and rate-limit pacing

- **WHEN** GitHub returns conditional no-change responses, `X-Poll-Interval`, or rate-limit metadata
- **THEN** the selected backend SHALL treat no-change as normal, persist pacing metadata, and delay later polling instead of burning request budget.

#### Scenario: backend parity

- **WHEN** `ISSUE_SPEC_GITHUB_BACKEND=gh`, `rest`, or `auto` selects either backend
- **THEN** notification polling, issue comment fallback, permission checks, runner comments, reactions, and pacing metadata SHALL preserve equivalent runner semantics.

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/24#issuecomment-4865331597

### Requirement: command parsing and authorization

The runner MUST parse and authorize comment commands before acpx dispatch, and issue comment text MUST NOT control runner flags, shell commands, clone URLs, working directories, model settings, or permission modes. Issue comment text MAY select the coordinator agent for a `/new` command, but only from a fixed `codex`/`claude` allow-list; it MUST NOT introduce any agent, command, model, flag, or permission mode outside that allow-list.

#### Scenario: accepted command grammar

- **WHEN** a comment body begins with `/new <prompt>`, `/new <agent> <prompt>` where `<agent>` case-insensitively matches the fixed `codex`/`claude` allow-list, `/resume <public-session-id> <prompt>`, or `/cancel <public-session-id>`
- **THEN** the runner SHALL normalize it into exactly one command candidate with repository, issue, trigger comment id, commenter, command verb, prompt or target public session id, selected agent when present, body hash, GitHub comment updated timestamp, and idempotency key.

#### Scenario: optional agent selector on new

- **WHEN** a `/new` command's first token case-insensitively matches an allow-listed agent (`codex` or `claude`)
- **THEN** the runner SHALL bind that normalized-lower-case agent as the new session's coordinator kind and use the remainder as the prompt; when the first token is not an allow-listed agent (including a quoted prompt that begins with the literal word `codex` or `claude`), the runner SHALL treat the entire tail as the prompt and use the runner's configured default agent.

#### Scenario: selected agent bound to session

- **WHEN** a `/new <agent>` session is created and later addressed by `/resume`
- **THEN** the runner SHALL persist the selected coordinator agent with the public session and reuse it for every `/resume` turn without re-parsing an agent selector, and a selected non-default agent SHALL run with its own default model rather than the runner's configured `--model`.

#### Scenario: rejected command grammar

- **WHEN** a comment starts with an unsupported slash command, misses required prompt text, has a malformed public session id, or uses ambiguous cancellation syntax
- **THEN** the runner SHALL reject the command before acpx dispatch and write bounded diagnostics when public feedback is required.

#### Scenario: authorization policy

- **WHEN** a valid command is observed
- **THEN** the runner SHALL authorize the current commenter through the selected GitHub backend and allow only configured users with write-equivalent repository permission: `write`, `maintain`, or `admin`.

#### Scenario: command idempotency and remote ack

- **WHEN** an accepted command comment is delivered more than once with the same repository, issue, comment id, GitHub updated timestamp, commenter, command verb, public session id when present, and body hash
- **THEN** the runner SHALL derive the same command idempotency key and SHALL NOT enqueue duplicate job or cancellation work.

#### Scenario: edited comments without durable seen state

- **WHEN** an ignored comment or rejected command is later edited into a valid command
- **THEN** the runner MAY accept and enqueue the edited command using the edited comment's command idempotency key.

#### Scenario: runner eyes acknowledgement

- **WHEN** an accepted command comment already has an `eyes` reaction from the configured runner identity
- **THEN** the runner SHALL treat the command as remotely acknowledged, report a duplicate with reason `remote_runner_ack`, and SHALL NOT enqueue job or cancellation work.

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/24#issuecomment-4865331592

### Requirement: public sessions and managed workspaces

The runner MUST map public session ids to durable acpx session metadata and managed workspaces without exposing raw acpx, provider, or local filesystem identifiers as user-facing resume ids.

#### Scenario: new session workspace

- **WHEN** an authorized `/new <prompt>` command is accepted
- **THEN** the runner SHALL allocate an opaque public session id, clone the issue repository into a unique managed workspace under the configured workspace root, create a workspace branch from the trusted repository ref, and start a fresh acpx session from that workspace.

#### Scenario: persisted session binding

- **WHEN** a `/new` session is created
- **THEN** the runner SHALL persist the public session id, repository scope, stable acpx record id, refreshed acpx metadata, session creator, bound coordinator agent, workspace path, clone URL, branch/ref, checkout SHA, and lifecycle state for later resume.

#### Scenario: repository-scoped resume

- **WHEN** an authorized `/resume <public-session-id> <prompt>` command targets a known session in the same repository
- **THEN** the runner SHALL re-authorize the current commenter, resolve the stored workspace and stable acpx record id, reuse the same managed workspace, and dispatch the prompt to the existing logical acpx session.

#### Scenario: session serialization

- **WHEN** two queued jobs target the same public session
- **THEN** the runner SHALL hold an exclusive workspace/session lock and run at most one acpx turn for that public session at a time.

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/24#issuecomment-4865331608
- https://github.com/higress-group/issue-spec/issues/24#issuecomment-4865331602

### Requirement: sandboxed acpx coordinator execution

The runner MUST invoke acpx as an external coordinator backend using argv arrays and bounded prompt input, with the managed workspace as the coordinator working directory.

#### Scenario: bounded coordinator context

- **WHEN** a job dispatches to acpx
- **THEN** the runner SHALL pass a bounded context bundle containing exactly one authorized command candidate, runner metadata, constraints, and selected issue-spec artifacts instead of asking the coordinator to rediscover or choose a trigger command.

#### Scenario: default sandbox

- **WHEN** a runner job dispatches on Linux without `--unsafe-no-sandbox`
- **THEN** acpx SHALL run through bubblewrap with workspace filesystem isolation, temporary `HOME`, `GH_CONFIG_DIR`, `XDG_CONFIG_HOME`, and `CODEX_HOME` when needed, inherited proxy settings, broad token environment variables scrubbed, required system paths read-only, and the managed workspace mounted for writes.

#### Scenario: unsafe mode

- **WHEN** `--unsafe-no-sandbox` is explicitly supplied or default sandboxing is unsupported on the platform
- **THEN** the runner SHALL make filesystem-boundary loss visible through config, durable state, and status comments, and SHALL NOT claim workspace filesystem isolation.

#### Scenario: agent-specific preflight

- **WHEN** the coordinator agent is `codex` or `claude`
- **THEN** preflight SHALL distinguish the relevant host auth/config requirements, acpx availability, Codex agent-full-access needs, and Claude user-settings/auth/allowed-tool requirements before dispatching workflow work that depends on them.

#### Scenario: multi-agent non-blocking preflight

- **WHEN** preflight runs on a runner where both `codex` and `claude` are selectable per `/new`
- **THEN** preflight SHALL report readiness for each selectable agent, and an unready non-default agent SHALL NOT block runner startup even under strict agent capabilities; instead the specific `/new <that-agent>` job SHALL fail fast at dispatch while the configured default agent keeps serving.

#### Scenario: self-hosted evidence-writer preflight

- **WHEN** a self-hosted Runner preflights one or more configured repositories
- **THEN** it SHALL use the native read-only evidence authority to verify that the PAT authenticates as the configured Runner login and that this identity has an active Evidence Writer assignment for every exact repository before dispatch, without treating `evidence:write` scope or a capability result as the assignment

#### Scenario: native child workers

- **WHEN** the acpx-launched coordinator uses the selected code agent's native child worker mechanism for issue-spec DAG work
- **THEN** the runner SHALL treat acpx as the top-level headless coordinator transport and persist bounded child provenance reported by the coordinator without needing direct access to the native worker runtime.

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/24#issuecomment-4865331603

### Requirement: durable job state and recovery

The runner MUST persist enough durable state outside GitHub comments to avoid duplicate acpx execution, support resume, serialize sessions, recover after restart, and audit workflow provenance.

#### Scenario: state model

- **WHEN** jobs, public sessions, workspaces, locks, cancellations, status writebacks, backend cursors, sandbox metadata, acpx metadata, coordinator summaries, CLI-direct artifact writes, or idempotency indexes are created or changed
- **THEN** the runner SHALL persist their durable state with idempotency keys and non-sensitive provenance.

#### Scenario: no durable all-comments seen index

- **WHEN** comments are observed during notification or fallback intake
- **THEN** the runner SHALL NOT require a durable all-comments seen index; stable command idempotency and the runner identity's remote `eyes` acknowledgement are the intended duplicate controls.

#### Scenario: write-ahead dispatch

- **WHEN** the runner is about to invoke acpx
- **THEN** it SHALL persist dispatch intent including runner job id, command idempotency key, public session id, stable acpx record id when known, turn correlation data, context bundle provenance, status comment id when known, workspace metadata, and lock ownership.

#### Scenario: startup reconciliation

- **WHEN** the runner starts before polling new comments
- **THEN** it SHALL reconcile queued, dispatched, and running jobs from durable state so completed, failed, cancelled, still-running, and ambiguous turns are reflected without creating duplicate acpx turns.

#### Scenario: interrupted recovery

- **WHEN** restart reconciliation cannot prove whether a dispatched or running acpx turn completed
- **THEN** the runner SHALL mark the job `interrupted`, preserve ambiguity diagnostics, avoid automatic retry, and require a new explicit user command to continue.

#### Scenario: cancellation

- **WHEN** an authorized `/cancel <public-session-id>` command targets an in-flight turn
- **THEN** the runner SHALL use the configured acpx cancellation capability, record the canceling user and result, and mark the target job `cancelled` only after cancellation is confirmed.

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/24#issuecomment-4865331593

### Requirement: runner status writeback and workflow provenance

The runner MUST own job lifecycle status writeback while allowing the sandboxed coordinator to create or update issue-spec workflow artifacts through existing issue-spec CLI commands.

#### Scenario: status comment lifecycle

- **WHEN** a command job is accepted
- **THEN** the runner SHALL create a deterministic ordinary issue comment with a hidden marker and update that same comment to `running`, `completed`, `failed`, `cancelled`, or `interrupted` as the lifecycle changes.

#### Scenario: public status content

- **WHEN** the runner writes a status comment
- **THEN** it SHALL keep the visible issue comment concise by including lifecycle status, current phase when known, the public session id, terminal `/resume` guidance when available, and a short result summary with relevant workflow artifact references.

#### Scenario: durable provenance

- **WHEN** runner ids, trigger comment ids, user metadata, agent/model details, sandbox metadata, CLI command names, stdout/stderr summaries, child/process evidence, or detailed diagnostics are available
- **THEN** the runner SHALL keep those details in durable state instead of expanding them in the visible public issue comment unless a future policy explicitly makes them public.

#### Scenario: workflow artifact writes

- **WHEN** the coordinator needs to create or update proposal, design, typed-comment, link, review, verify, or archive artifacts
- **THEN** it SHALL invoke existing issue-spec CLI commands inside the sandbox using runner-provided GitHub authentication access, while the runner captures bounded command and artifact provenance in durable state rather than applying a custom workflow-action envelope.

Source SPEC comments:
- https://github.com/higress-group/issue-spec/issues/24#issuecomment-4865331607

### Requirement: Out-of-band cancellation of in-flight jobs

The runner MUST process a queued cancellation whose target job is dispatched or running without waiting for that job's in-flight dispatch call to return. Cancellation processing MUST run independently of the single async job-dispatch goroutine so that a cancellation cannot be serialized behind the very job it targets.

#### Scenario: Cancel while target dispatch is blocked

- **WHEN** a job is running and its async dispatch goroutine is blocked inside the acpx coordinator call, and a valid /cancel for that job's public session is queued
- **THEN** the runner MUST process the cancellation in the same poll cycle it is queued, without waiting for the blocked dispatch call to return

#### Scenario: No queued cancellation present

- **WHEN** a poll cycle runs and no cancellation is queued
- **THEN** the runner MUST perform no cancellation work and MUST leave job dispatch cadence unaffected

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/145#issuecomment-4904525769

### Requirement: Cancel in-flight jobs by session identity without a persisted record id

A queued cancellation targeting a dispatched or running job that has a public session id MUST be attempted via the ACP cancel path even when that job has not yet persisted a stable acpx record id. In-flight /new jobs acquire their stable record id only at completion, yet their acpx session already exists and is cancelable by session identity, so a missing record id MUST NOT by itself cause the runner to reject cancellation of an active job.

#### Scenario: Running /new job without a persisted record id

- **WHEN** a /new job is running with a public session id but no persisted stable acpx record id, and a valid /cancel for that session is queued
- **THEN** the runner MUST attempt ACP cancellation using the job's session identity rather than rejecting the cancellation for a missing record id

#### Scenario: Queued job not yet dispatched

- **WHEN** the target job is still queued and has not started acpx dispatch
- **THEN** the runner MUST cancel it before dispatch without requiring a stable acpx record id

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/145#issuecomment-4904526022

### Requirement: Cancelled state survives late dispatch completion

Once a job has been transitioned to cancelled, a subsequently returning blocked dispatch call for that job MUST NOT overwrite the cancelled terminal state and MUST NOT emit a conflicting terminal status writeback for that job.

#### Scenario: Blocked dispatch returns after out-of-band cancel

- **WHEN** a job has been cancelled out-of-band and its previously blocked dispatch call later returns with a result or error
- **THEN** the runner MUST NOT transition the job to completed or failed and MUST NOT post a conflicting terminal status update for that job

#### Scenario: Cancellation confirmed for active job

- **WHEN** an ACP cancellation for an active job is confirmed
- **THEN** the runner MUST record the job as cancelled and release its workspace lock so the session can be reused

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/145#issuecomment-4904527246

### Requirement: Visible acceptance feedback for cancellation commands

When the runner accepts and queues a valid /cancel command, it MUST provide prompt visible feedback on the trigger comment on acceptance, and it MUST surface the terminal cancellation outcome to the user rather than leaving an accepted cancellation silent.

#### Scenario: Accepted cancellation is acknowledged

- **WHEN** a valid /cancel command is accepted and queued and has not yet reached a terminal outcome
- **THEN** the runner MUST add a reaction to the trigger comment so the command is visibly acknowledged from GitHub

#### Scenario: Terminal cancellation outcome is reported

- **WHEN** a queued cancellation reaches a terminal outcome
- **THEN** the runner MUST post or update a status comment describing whether the cancellation was confirmed, failed, unsupported, or rejected

Source SPEC comment: https://github.com/higress-group/issue-spec/issues/145#issuecomment-4904527613
