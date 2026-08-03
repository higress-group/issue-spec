# issue-spec

**English | [简体中文](README.zh-CN.md)**

`issue-spec` is an issue-native, self-hostable spec workflow for agentic
software development: strengthen human review of the proposal and the design
*before* code gets written, then orchestrate the implementation as small tasks
that each go to the right agent.

```text
-> review requirements and design first, code second
-> active changes in issues and typed comments, durable specs in the repo
-> human review rendered as rich HTML, human decisions as structured answers
-> implementation as an agent DAG, each PROCESS owned by the right agent
-> agents read the same state through a lean CLI, without the HTML weight
```

Every substantial change moves through three issues — **Proposal** (what and
why), **Design** (how and acceptance), **Implement** (execution DAG) — with
typed `SPEC`, `QUESTION`, `TASK`, `PROCESS`, `REVIEW`, and `VERIFY` comments
keeping traceability from requirement to merged line.

## A self-hosted workspace for your team

Run an operator-controlled issue-spec server on your own infrastructure:
browser workspace, GitHub-compatible issue API, organization and repository
authorization, service accounts, provider-neutral code evidence, runners,
webhooks, and durable PostgreSQL state — while source code, PRs/MRs, reviews,
and CI stay on the code host your team already uses.

### Review surfaces built for human decisions

Agents publish each phase as a sandboxed `html-preview` review projection: a
rendered brief that shows what changed, what is settled, and what still needs a
decision. Open decisions are typed `QUESTION` comments, answered right on the
issue page with native controls; every confirmed choice becomes an immutable
`ANSWER` record that later agents and workflow gates consume.

The **Proposal** choice brief separates settled boundaries from the decisions a
human still owns, with the native answer panel right below it:

[![Proposal choice brief with native question answering](docs/self-hosting/assets/self-hosted-review-proposal.png)](docs/self-hosting/README.md)

The **Design** explainer renders the data flow, invariants, rejected
alternatives, and acceptance checks:

[![Design explainer with data flow and invariants](docs/self-hosting/assets/self-hosted-review-design.png)](docs/self-hosting/README.md)

The **Implement** execution brief shows the PROCESS DAG: what runs in parallel,
what is blocked, and which agent owns each node:

[![Implement execution brief with the PROCESS DAG](docs/self-hosting/assets/self-hosted-review-implement.png)](docs/self-hosting/README.md)

The HTML stays out of agent context: agents read proposals, designs, questions,
and effective answers through the `issue-spec` CLI, which returns the compact
canonical artifacts instead of the rendered review surfaces — human-friendly
review without token-heavy agent reads.

### Issues, search, and change management

Proposal, design, and implement issues keep the current artifact in the body
and the full decision history in the timeline. Lists, filters, and labels keep
in-flight changes findable across repositories.

[![Issue workspace with search and filters](docs/self-hosting/assets/self-hosted-dashboard.png)](docs/self-hosting/README.md)

Full-text search groups matching issues and comments by their related change,
so one query surfaces the historical proposal, design, and implement trail a
new change should build on:

[![Search results grouped by related change](docs/self-hosting/assets/self-hosted-search.png)](docs/self-hosting/README.md)

The same capability is CLI-native — agents run
`issue-spec search issues --repo owner/repo --query "..." --source change --stage design`
to find prior related changes and their decisions before proposing a new one.

Change boards group the three phase issues into one change and expose
lifecycle, TASK/PROCESS progress, and linked code changes — a GitHub pull
request and an internal merge request can sit under the same change.

[![Change board](docs/self-hosting/assets/self-hosted-change-board.png)](docs/self-hosting/README.md)

### Issues and code live on separate authorities

Self-hosting separates issue state from code state on purpose: the server is
authoritative for issues and typed workflow artifacts, while the code host
your team already uses stays authoritative for source, branches, PRs/MRs,
reviews, CI, and merges. You connect the two with an operator-registered
**code-provider bridge** that reports normalized, revision-bound code evidence
(and performs only explicitly requested external mutations); the server still
evaluates every gate itself, and one change can link providers side by side —
a GitHub pull request and an internal merge request under the same change.

```yaml
# issue-spec/config.yaml — repository side selects the operator-registered bridge
external_code:
  provider_key: code.example
  evidence:
    required: [review, check, merge]
    freshness: { review: 24h, check: 1h }
```

- [Integrate company code and work platforms](docs/self-hosting/enterprise-provider-integration.md) — provider inventory, source bindings, work trackers
- [Code provider bridge protocol v1](docs/self-hosting/bridges/code-provider-v1.md) — trust boundaries, evidence ingestion
- [Runner Git credential command v1](docs/self-hosting/bridges/git-credential-v1.md) — clone/push credentials for runners

**[Self-hosted server: architecture, access model, deployment, operations →](docs/self-hosting/README.md)**

## One shared context for humans and agents

The `issue-spec` CLI is how agents participate in the same workflow humans
review in the browser:

- agents author and read optional typed planning artifacts (`SPEC`, `TASK`,
  `PROCESS`, ...) with validation and safe transitions
- a coordinator defaults bounded implementation to one writer, and uses managed
  PROCESS workspaces only when concurrency, isolation, ownership, recovery, or
  dependency-ordered integration requires them
- teammates and their agents pick up planning mid-flight because optional
  decisions and blockers live in Issues, while review findings, current checks,
  and merge authority remain owned by the code provider
- `issue-spec runner` executes authorized comment commands (`/new`, `/resume`)
  in managed workspaces, so work can be triggered straight from an issue

The actual code writer records line-local rationale only for non-obvious design
decisions, using stable code anchors rather than provider diff positions. After
the exact head is pushed, the coordinator validates those anchors and publishes
the worker-authored text as non-blocking inline comments. The ordinary
`### Implementation Rationale` discussion summarizes and indexes them, or
carries `path:symbol/line` fallbacks when safe inline discussion is unavailable.
No rationale quota or gate exists; provider review and current configured checks
remain the only merge authority.

## Works with GitHub too

The same workflow runs directly against github.com or GitHub Enterprise using
your `gh` login — issues, typed comments, PR review threads, and durable spec
archive PRs. The only self-host-specific conveniences are the rendered
`html-preview` review surfaces and the interactive QUESTION answer panel:
GitHub shows their source as plain readable Markdown instead, and answers are
recorded through the CLI.

## Quick start

Install the CLI:

```bash
go install github.com/higress-group/issue-spec/cmd/issue-spec@latest
```

Authenticate (GitHub mode reuses your `gh` session) and initialize a
repository:

```bash
gh auth login
issue-spec init --repo owner/repo --create-labels --tools codex,claude --delivery both
```

Then drive the workflow from your agent with the generated skills or slash
commands:

```text
/issue-spec:propose "your idea"
/issue-spec:apply
/issue-spec:review
/issue-spec:verify
/issue-spec:archive
```

To self-host the server for your team, start from the
**[self-hosting guide](docs/self-hosting/README.md)**.

## Learn more

- **[Workflow guide](docs/workflow.md)** — full walkthrough, workflow model, multi-agent coordination, review flow, GitHub-mode setup
- **[CLI reference](docs/reference.md)** — configuration, command contracts, canonical typed comments
- **[Runner operations](docs/runner.md)** — comment intake, authorization, sandboxing, concurrency, recovery
- **[Workflow safety](docs/workflow-safety.md)** — gates, reconciliation, PROCESS evidence
- **[Self-hosting](docs/self-hosting/README.md)** — architecture, authentication, bridges, operations

## Development

Local builds and tests use the Go toolchain declared in [`go.mod`](go.mod):

```bash
go build ./cmd/issue-spec
make build-server
go test ./...
```

`go test ./...` is the same unit test command CI runs
(see [`.github/workflows/unit-tests.yml`](.github/workflows/unit-tests.yml)).
To start the Server with PostgreSQL, follow the
[local Server development guide](docs/self-hosting/local-development.md).

## Acknowledgements

`issue-spec` is inspired by [OpenSpec](https://github.com/Fission-AI/OpenSpec)
and preserves its spec-first workflow habits while adapting active change
state, human review, and multi-agent coordination to issues and pull requests.
See [the workflow guide](docs/workflow.md#relationship-to-openspec) for how the
two relate.
