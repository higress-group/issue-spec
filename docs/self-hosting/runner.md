# Self-hosted runner: trigger agents from issue comments

**English | [简体中文](runner.zh-CN.md)**

[Back to the self-hosted server guide](README.md)

This guide connects a self-hosted issue-spec server to Codex or Claude through
issue comments. GitHub-backed repositories use `issue-spec runner poll`; a
self-hosted server must use `issue-spec runner serve` to receive server-pushed
webhooks.

```text
Maintainer comments /new
        |
        v
issue-spec server transactional outbox
        |
        | issue-spec.v1 + bearer secret
        v
runner serve /api/v1/runner/webhooks
        |
        +--> authorize comment author and repository
        +--> resolve source binding and job-scoped Git credential
        +--> delegate a short-lived issue token
        v
      acpx --> Codex or Claude
        |
        +--> branch, commit, PR/MR, tests, and issue writeback
```

## 1. Prepare the runner host

Use a dedicated system account on Linux. Install a compatible `issue-spec`
binary, `acpx`, the selected agent runtime, `bubblewrap`, and—when using the
Codex provider—`npm` and `npx`. The host needs network access to the issue-spec
server, code host, model service, and required package registries.

The operator must also provide an executable implementing
[`issue-spec-git-credential-v1`](bridges/git-credential-v1.md). Do not use
`--unsafe-no-sandbox` unless the operator explicitly accepts that the agent can
access the runner host filesystem.

## 2. Create the source binding

Create and activate a binding on the repository's **Source connection** page.
It supplies the provider, external repository identity, HTTPS clone URL, and
default branch without storing a clone credential. The Git credential command
must mint a short-lived credential for this exact binding, separate from the
issue-spec PAT.

## 3. Create an independent service account and runner PAT

A production runner should not use an administrator or maintainer's personal
identity. Create one issue-spec service account for each independent runner
security boundary:

1. create an account such as `Runner Bot` under **Administration > Service accounts**
   and save its generated exact login, such as `svc-runner-bot-a1b2c3d4`;
2. resolve that login under the repository's **Collaborators** page and grant
   the minimum `write` role;
3. resolve the service account under **Administration > Managed access tokens**;
4. select **Runner preset** and exactly one repository;
5. confirm these scopes and create the Managed PAT:

```text
read:user, issues:read, issues:write, runner:delegate, evidence:write
```

![Issue a Runner Managed PAT to an independent service account](assets/self-hosted-runner-service-account.png)

Save the one-time token. `--runner` must be the exact service-account login.
Add human maintainers with repeatable `--allowed-user` flags; each author must
also have write-equivalent repository permission. This keeps the runner
automation identity, command author, and Linux process user separate.

Read the public origins and instance ID from `/api/v1/meta`, then create the
origin-bound profile as the runner system user:

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

The Managed PAT is the parent credential. Each job receives a short-lived,
repository-scoped issue token delegated by the server. A parent credential is
restricted to exactly one repository; use a separate Managed PAT, profile, and
`runner serve` process for each additional repository.

## 4. Create the Runner intake webhook

On the repository's **Webhooks** page, select **New webhook**:

1. keep **Runner intake (`issue-spec.v1`)** as the delivery contract;
2. enter a server-reachable URL such as
   `https://runner.example.com/api/v1/runner/webhooks`;
3. select `issue_comment.created` and `issue_comment.edited`;
4. create the route.

![Runner intake webhook configuration](assets/self-hosted-runner-intake.png)

Save both the **Subscription ID** and one-time **Webhook secret**. They map to
`--subscription-id` and `--secret-file` respectively.

![Save the subscription ID and webhook secret](assets/self-hosted-runner-credentials.png)

Store the secret in a file readable only by the runner user. Never put it in a
command-line argument, repository, or systemd unit. Production mode requires a
`0600` secret file and rejects environment-based webhook secrets.

## 5. Connect code-host credentials

`--git-credential-command` names an absolute operator-owned executable. The
runner invokes it without a shell and does not expose the profile PAT, webhook
secret, or host environment. The command receives a pinned source binding and
returns a job-scoped username, password, expiry, and lease ID. It must support
idempotent `revoke_lease` and `revoke_job` actions.

See [`Runner Git credential command v1`](bridges/git-credential-v1.md) for the
complete contract. A typical path is
`/usr/local/libexec/issue-spec-git-credential`.

## 6. Run preflight and start in the foreground

```bash
issue-spec --profile team runner preflight \
  --repo acme/workflow \
  --runner svc-runner-bot-a1b2c3d4 \
  --agent codex \
  --json

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

Repeat `--allowed-user` as needed. One parent credential cannot delegate across
repositories. The default maximum is three concurrent jobs.

If TLS terminates at a reverse proxy, listen on loopback and expose only
`/api/v1/runner/webhooks`. For direct TLS, bind an exact non-loopback IP—not a
wildcard—and add `--production --tls-cert FILE --tls-key FILE`. The receiver URL
must be reachable from the server's delivery worker.

## 7. Run continuously with systemd

Complete profile login as the `issue-spec-runner` user and create private state
and workspace directories. Then install a unit based on this example:

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

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now issue-spec-runner
sudo systemctl status issue-spec-runner
sudo journalctl -u issue-spec-runner -f
```

## 8. Trigger the agent from a comment

The command must start at the beginning of the comment:

```text
/new Fix the login failure, add tests, and open a PR
/resume s_demo_42 Apply the review feedback to error handling
/cancel s_demo_42
```

`/new` creates a public session and workspace, `/resume` continues one, and
`/cancel` cancels an eligible in-flight job. Ordinary comments do not trigger
the agent. Runner status comments contain the phase, public session ID, result,
and a copyable `/resume` template.

![Comment-triggered agent and runner status](assets/self-hosted-runner-command.png)

## 9. Verify and troubleshoot

For the first test, use a read-only task and verify the webhook delivery,
runner authorization log, workspace creation, Git credential acquisition and
revocation, runner status comment, and typed workflow writeback in that order.

| Symptom | Check first |
| --- | --- |
| Webhook returns `401` | Subscription ID, current secret, and server/runner clocks |
| Webhook cannot connect | Receiver URL, DNS, firewall, reverse proxy, and TLS |
| Comment is ignored | Command position, allowlist, and write-equivalent permission |
| `runner:delegate` fails | Exact repository restriction, scopes, and service-account login matching `--runner` |
| Clone fails | Active source binding, HTTPS URL, and exact binding echoed by the credential command |
| Sandbox preflight fails | Install `bubblewrap` or configure `--bwrap` on Linux |
| Codex does not start | Ensure `acpx`, `npm`, `npx`, and model credentials are available to the systemd user |

When rotating a webhook secret, provide the old value with
`--previous-secret-file` and set a `--previous-secrets-valid-until` overlap no
longer than 24 hours. Remove the old secret after confirming delivery with the
new value.

## Security boundaries

- the webhook secret authenticates delivery only; it grants no issue or source authority;
- the profile PAT delegates short-lived, repository-scoped job tokens;
- source bindings contain no credentials; Git credentials are short-lived and binding-specific;
- the runner handles only explicit `--repo` values, and authors must pass both allowlist and repository authorization;
- runner state, workspaces, and credential leases should feed centralized logs and audit.
