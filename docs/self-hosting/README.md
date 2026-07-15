# Self-hosted issue-spec server

**English | [简体中文](README.zh-CN.md)**

The self-hosted server brings the issue-native issue-spec workflow to an
operator-controlled environment. It combines a browser workspace, a
GitHub-compatible issue API, organization and repository authorization,
provider-neutral code evidence, webhooks, runners, and durable PostgreSQL
state in one deployable service.

Use self-hosting when a team needs private-network deployment, local identity
and authorization, internal code-host integration, or automation identities
that do not depend on a personal GitHub account.

![Self-hosted workspace overview](assets/self-hosted-dashboard.png)

## What the server owns

```text
Browser and issue-spec CLI
          |
          v
 issue-spec-server  <---->  GitHub OAuth or OIDC
    |       |
    |       +------------>  code-provider bridge / evidence writer
    |       +------------>  GitHub-compatible or runner webhooks
    v
 PostgreSQL
```

The server is authoritative for:

- organizations, repositories, memberships, collaborators, and visibility;
- issues, comments, labels, reactions, typed workflow projections, and change
  boards;
- personal and managed access tokens, service accounts, optional token delegation,
  source bindings, and evidence-writer designation;
- webhook subscriptions, filtering, encrypted credentials, delivery attempts,
  retries, replay, and audit records.

The external code host remains authoritative for source code, branches,
commits, pull requests or merge requests, reviews, and CI. A source binding and
code-provider adapter project that evidence into issue-spec without giving the
server ambient source credentials.

## Product tour

### Issues keep the full decision history

Proposal, design, and implementation issues carry the current artifact in the
issue body. SPEC, QUESTION, TASK, PROCESS, REVIEW, and VERIFY records are typed
comments in the same timeline. Ordinary human comments remain available for
discussion and notifications.

![Issue detail with typed workflow comments](assets/self-hosted-issue-detail.png)

### Change boards show workflow state, not just issue counts

The change board groups proposal, design, and implementation issues into one
change. It exposes lifecycle, TASK and PROCESS progress, diagnostics, and
linked code changes without making the external code provider the issue
backend.

![Change board](assets/self-hosted-change-board.png)

The detail view keeps traceability and provider-neutral code relationships in
one place. A GitHub pull request and an internal merge request can appear under
the same change while retaining their provider identity.

![Change detail and code evidence](assets/self-hosted-change-detail.png)

### Integrations are policy controlled and auditable

Repository administrators can configure runner intake or `github.v3`
notification webhooks. Notification policies can select issue actions, change
kinds, human or typed comments, and human or automation actors. Query
credentials are encrypted and never returned to the browser after creation.

![Webhook delivery control room](assets/self-hosted-webhook-integrations.png)

## Access model

Repository visibility controls reads. Contribution policy separately controls
who may create issues or comments.

| Visibility | Anonymous | Authenticated outsider | Member or collaborator |
| --- | --- | --- | --- |
| `public` | read | read | read |
| `internal` | concealed | read | read |
| `private` | concealed | concealed | read |

| Contribution policy | Who may contribute |
| --- | --- |
| `disabled` | nobody |
| `members` | organization members, service accounts, or explicit collaborators |
| `authenticated` | any active signed-in identity |
| `public` | any active signed-in identity when repository visibility is public |

Anonymous mutation is always denied. Signing in through GitHub OAuth or OIDC
creates an external identity; it does not implicitly grant an organization
role, repository permission, runner authority, or evidence-writer status.

## Deployment path

The production artifact is a single `issue-spec-server` binary or the
repository runtime container. The binary embeds the generated web application.
PostgreSQL and three operator-owned secret files are required.

1. Read the [deployment and hardening guide](operations/deployment.md).
2. Choose HTTPS or complete the
   [trusted internal HTTP checklist](authentication/v1/trusted-internal-http.md).
3. Configure [GitHub OAuth](authentication/v1/github-oauth.md) or
   [OIDC](authentication/v1/oidc.md).
4. Build a reproducible server artifact:

   ```bash
   make release-server
   # or
   make docker-server IMAGE=registry.example/issue-spec-server:VERSION
   ```

5. Start PostgreSQL and the server with the required environment and secret
   mounts. Wait for `/readyz` before routing employee traffic.
6. Claim bootstrap once, sign in with the configured provider, create the
   first organization, and assign local roles explicitly.
7. Back up PostgreSQL together with the token pepper and encryption keyring;
   see [backup, restore, upgrade, and recovery](operations/backup-restore.md).

The `deployments/dev` fixture is for local development. Do not copy its
credentials or development posture into production.

## Connect a local repository

Create a PAT from **Access tokens** in the web application, then configure an
origin-bound self-hosted profile. Read the canonical origins and instance ID
from `/api/v1/meta` instead of guessing them.

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

Initialize an existing server repository:

```bash
issue-spec --profile team init \
  --repo acme/workflow \
  --server-org acme \
  --server-repo workflow \
  --tools codex,claude \
  --delivery both
```

An authorized caller may add `--create-if-missing` to register the repository.
Use `--bind-source`, `--provider`, and the external repository coordinates when
the source lives on a separate code host. Source bindings contain canonical
repository identity and URLs, never a personal clone credential.

For a provider-neutral integration plan, operator registry example, bridge
scaffold, code-evidence mapping, and Jira-like work-item projection pattern,
read [Integrate company code and work platforms](enterprise-provider-integration.md).

## Automation identities

A service account is an organization-bound non-human identity for CI, runners,
evidence synchronization, or scheduled integrations. Creating one does not
create a token or grant repository authority.

The safe sequence is:

1. create the service account;
2. grant only the required repository collaborator role;
3. create a managed PAT with an explicit repository cap and minimum scopes;
4. store the one-time token in the automation secret store;
5. disable the service account to invalidate its credentials when retired.

Service-account and PAT activity is classified as automation, allowing webhook
policies and audit review to distinguish it from human browser activity.

## Webhooks and runners

Two delivery contracts share the same transactional outbox and delivery
ledger:

- `issue-spec.v1` uses bearer authentication for `issue-spec runner serve`;
- `github.v3` emits GitHub-compatible issue and issue-comment events for
  notification receivers such as a GitHub-compatible DingTalk robot.

Typed comments and automation actors can be excluded without parsing untrusted
comment text. Failed deliveries are retried, become dead letters after the
configured attempt limit, and can be replayed without changing event identity.
For multi-replica behavior and recovery, read
[HA webhook delivery operations](operations/ha-webhooks.md).
For the complete PAT, source binding, webhook, systemd, and comment-command
setup, see [Self-hosted runner: trigger agents from issue comments](runner.md).

## Operations index

- [Authentication guide](authentication/README.md)
- [Deployment and hardening](operations/deployment.md)
- [Backup, restore, upgrade, and recovery](operations/backup-restore.md)
- [HA webhook delivery](operations/ha-webhooks.md)
- [Comment-triggered agents with runner serve](runner.md)
- [Enterprise code and work-platform integration](enterprise-provider-integration.md)
- [Code-provider bridge contract](bridges/code-provider-v1.md)
- [Git credential bridge contract](bridges/git-credential-v1.md)

## Regenerate the documentation screenshots

The images in this guide are not captured from a real organization. They are
generated from deterministic, credential-free Playwright fixtures also used by
the visual regression suite.

```bash
make docs-self-hosted-screenshots
```

The command builds the web application, updates the English and Simplified
Chinese desktop golden snapshots, and copies both documentation variants into
`docs/self-hosting/assets`. Review visual
diffs before committing regenerated images. Never replace these fixtures with
screenshots from an internal deployment, real issue content, access tokens, or
employee identities.
