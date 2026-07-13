# Runner Git credential command v1

`issue-spec runner serve` requires an operator-owned executable through
`--git-credential-command`. The runner invokes the executable directly, never
through a shell, and writes one JSON object to standard input. The process gets
only `PATH=/usr/bin:/bin` plus a fixed C locale; profile PATs, webhook secrets
and the host environment are not inherited.

Every request contains:

```json
{
  "protocol": "issue-spec-git-credential-v1",
  "request_id": "4b73757b-cbec-45a9-a364-f118b57142dd",
  "action": "acquire",
  "identity": {
    "job_id": "job-123",
    "purpose": "git",
    "binding": {
      "binding_id": "0ae20e4a-31dd-4d3c-965c-9868ae5dfebc",
      "version": 7,
      "provider_key": "gitlab",
      "external_repository_id": "platform/gateway",
      "clone_url": "https://git.example.test/platform/gateway.git"
    }
  }
}
```

The command must return exactly one JSON object. Unknown or duplicate keys,
trailing values, protocol/request/action mismatches and identity mismatches are
rejected. An `acquire` response echoes the full identity and adds the returned
lease id to both `identity.lease_id` and `lease.lease_id`:

```json
{
  "protocol": "issue-spec-git-credential-v1",
  "request_id": "4b73757b-cbec-45a9-a364-f118b57142dd",
  "action": "acquire",
  "identity": {
    "job_id": "job-123",
    "purpose": "git",
    "binding": {
      "binding_id": "0ae20e4a-31dd-4d3c-965c-9868ae5dfebc",
      "version": 7,
      "provider_key": "gitlab",
      "external_repository_id": "platform/gateway",
      "clone_url": "https://git.example.test/platform/gateway.git"
    },
    "lease_id": "lease-456"
  },
  "lease": {
    "lease_id": "lease-456",
    "username": "runner-job-123",
    "password": "short-lived-secret",
    "expires_at": "2026-07-11T12:05:00Z"
  }
}
```

The runner calls `revoke_lease` with `job_id` and `lease_id` whenever a clone
or child lease ends, and calls `revoke_job` with `job_id` on terminal,
cancellation, restart reconciliation and uncertain acquisition. Successful
revoke responses echo the same identity and return `"revoked": true`. Revoke
operations must therefore be idempotent. An adapter error may instead return
an `error` object containing only a stable lowercase `code`; stderr and response
bodies are never surfaced to the job.

The adapter should mint one credential for the exact HTTPS clone URL in the
pinned binding and should not return a broad host credential. Runner-side
timeouts, output limits and concurrency bounds are configurable, but cannot be
disabled. Credentials are materialized only into per-job private files and are
not accepted from issue content, webhook bodies or ambient host credentials.

For choosing between a credential command, host SSH, and a code-provider
bridge, see
[Integrate company code and work platforms](../enterprise-provider-integration.md).
