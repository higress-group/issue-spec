---
name: runner-diagnostics
description: Diagnose issue-spec comment Runner failures using preflight output, persistent NDJSON logs, ACPX/Codex adapter output, bubblewrap metadata, proxy inheritance, host SSH, webhook intake, and Provider bridge evidence. Use when a /new, /resume, or /cancel command is ignored, a Runner job fails, Codex does not start, a model is rejected, clone/push fails, a webhook is rejected, or a PR/MR was not created.
---

# Diagnose a Runner failure

Trace the failure from a public status comment to bounded local diagnostics.
Keep tokens, internal hostnames, absolute paths, raw adapter output, and private
deployment details out of issue comments and public pull requests.

## Start from the failing identifier

1. Record the public session ID, job ID, trigger-comment URL, status-comment
   URL, approximate time, and command (`/new`, `/resume`, or `/cancel`).
2. Locate the scoped Runner directory containing `state.json` and `logs/`.
   Use the runner's configured `--state` path rather than assuming a repository
   name or service-account layout.
3. Resolve the job or session through `logs/index.ndjson`, then read only the
   matching job and session records.

```text
state.json
logs/
  runner.ndjson
  errors.ndjson
  index.ndjson
  jobs/<job-id>.ndjson
  jobs/<job-id>-acpx-stdout.log
  jobs/<job-id>-acpx-stderr.log
  sessions/<public-session-id>/<turn-correlation-id>.ndjson
```

Use `jq` and narrow `rg` queries. Do not dump all logs.

```bash
rg -n '"id":"<job-id>"|"id":"<public-session-id>"' logs/index.ndjson
rg -n '"level":"error"|"component":"(intake|dispatch|acpx)"' logs/jobs/<job-id>.ndjson
tail -100 logs/jobs/<job-id>-acpx-stderr.log
```

## Reproduce in the correct order

1. Run `issue-spec runner preflight` as the same OS service user.
2. For Codex, add `--verify-agent-runtime` to create one tools-denied ACP
   session. This validates the effective ACPX adapter and any explicit
   `--model`; it is not a substitute for the bubblewrap check.
3. Check the durable job's `sandbox.agent_runtime`, `sandbox.bwrap`,
   `sandbox.env_decisions`, and bounded diagnostics before changing any config.
4. Re-run only the smallest failed layer: signed webhook delivery, command
   authorization, clone, read-only Git command, documentation-only commit and
   push, then Provider `change.create` when that capability is configured.

## Diagnose the common boundaries

### Command intake

- Confirm the command starts at the beginning of the comment.
- Confirm the author is allowed and has repository write-equivalent permission.
- For self-hosted intake, confirm the subscription ID, secret rotation window,
  receiver reachability, and server/runner clocks.
- Correlate the delivery ID, `cycle_id`, `trigger_comment_id`, and job ID before
  deciding that a command was ignored.

### Codex or ACPX

- Treat `acpx` and its configured `agents.codex` override as the effective
  runtime. A host `codex --version` does not prove that the adapter supports a
  model.
- Inspect `codex-acp` preflight and `sandbox.agent_runtime`. The latter stores
  only `builtin` or a safe adapter description, never the host config path.
- When ACPX rejects a model, use the exact adapter-advertised ID, including a
  reasoning-effort suffix. Remove an unnecessary explicit `--model` only after
  confirming the service-user default works.
- Check npm/npx access or the pre-cached pinned adapter package. Do not paste
  raw ACPX output into an issue because it can contain environment diagnostics.

### Proxy and network

- Inspect the systemd environment file and `proxy_inherited:*` decisions in
  sandbox metadata. Standard `HTTP_PROXY`, `HTTPS_PROXY`, and `NO_PROXY` names
  are inherited by the sandbox.
- Put directly reachable receiver, issue-server, and code-host addresses in
  `NO_PROXY`. Keep proxy credentials out of the unit, logs, and issue comments.
- Restart the service after editing the environment file, then re-run the live
  runtime probe as the service user.

### Sandbox, Git, and SSH

- Check `sandbox.bwrap` and `sandbox.preflight_result` before considering
  `--unsafe-no-sandbox`; do not use unsafe mode as a routine workaround.
- For brokered credentials, compare the active Source Binding identity and
  clone URL, then verify lease acquisition and revocation.
- For `--allow-host-ssh`, verify the dedicated Runner account's non-interactive
  SSH access and `known_hosts`. Treat all authority reachable by that identity
  as available to every job in that Runner boundary.
- Do not solve a missing code-host CLI by mounting arbitrary host binaries into
  bubblewrap.

### PR/MR creation

- Distinguish commit/push success from change creation. A job can safely push a
  branch and still lack authority to create a PR/MR.
- If a configured `issue-spec.code-provider/v1` bridge advertises
  `change.create`, verify its request and resulting change URL. Keep the
  company-specific wrapper and registry operator-owned.
- If the bridge does not advertise `change.create`, report the branch/revision
  evidence and create the change outside the sandbox. Do not broaden sandbox
  mounts to bypass this boundary.

## Report safely

Report the public session/job identifiers, failing boundary, timestamp,
sanitized error category, relevant preflight check, and the smallest safe next
action. Link to the status comment or change URL when available. Exclude raw
logs, credentials, internal addresses, full filesystem paths, and secret-like
environment values.
