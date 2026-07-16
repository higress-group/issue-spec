# Self-hosted server deployment

The supported production artifact is the single `issue-spec-server` binary or
the repository `runtime` container target. The binary contains the pinned Vite
build; production never reads a host static directory.

## Required configuration

Set `ENVIRONMENT=production`, `LISTEN_ADDR`, `DATABASE_URL`,
`API_PUBLIC_URL`, and `WEB_PUBLIC_URL`. The default `TRANSPORT_POSTURE=https`
requires HTTPS root origins. Controlled employee networks may explicitly use
`TRANSPORT_POSTURE=trusted-internal-http` with matching HTTP origins; follow
the [trusted internal HTTP checklist](../authentication/v1/trusted-internal-http.md).
Mount these regular, non-symlink files with mode `0600`:

- `BOOTSTRAP_SECRET_FILE`: at least 32 bytes; consumed by the first bootstrap
  claim.
- `TOKEN_PEPPER_FILE`: at least 32 bytes; loss invalidates token lookups.
- `ENCRYPTION_KEY_FILE`: at least 32 bytes; loss makes encrypted webhook
  secrets unrecoverable.
- optional `AUTH_PROVIDERS_FILE`: OIDC/GitHub OAuth runtime definitions,
  including client secrets. The file is bounded to 64 KiB and never rendered
  in config or logs.
- optional `WEBHOOK_ENCRYPTION_KEYS_FILE`: a JSON keyring with `current` and a
  `keys` object whose values are base64-encoded keys of at least 32 bytes. If
  omitted, `ENCRYPTION_KEY_FILE` is used under key ID `primary`.

External identity configuration, safe placeholder examples, GitHub/OIDC
quickstarts, admission, and rotation are maintained in the versioned
[self-hosted authentication guide](../authentication/README.md). Provider IDs
and names are unique, client secrets belong only in the protected file, and
callback URLs are always constructed from `API_PUBLIC_URL`, never request
headers.

Use `WEBHOOK_ALLOWED_PRIVATE_CIDRS` only for explicit operator-owned destination
ranges. The legacy variable name does not restrict entries to RFC 1918 space:
it may include non-private ranges that are internally routed and controlled by
the operator. When `TRANSPORT_POSTURE=trusted-internal-http`, HTTP webhook
receivers are permitted only when every resolved and connect-time address is
covered by an explicit allowed CIDR; an empty allowlist permits none. TLS remains
required for every other production posture and for destinations outside the
allowlist. Loopback, link-local, unspecified, multicast, and cloud metadata
addresses remain denied even when a configured CIDR contains them. The same DNS
resolver and policy are used for subscription preflight and the actual delivery
connection.

## Optional PostgreSQL issue search

Search is explicit and disabled by default. Leave `SEARCH_MODE=disabled` when
the PostgreSQL instance does not provide search extensions; the server starts
normally, advertises `features.search=false`, mounts no search routes, and does
not run a sequential-scan fallback.

To enable full-text search, the database operator must install `pg_bigm` and
`pg_jieba` in the issue-spec database and configure `pg_jieba` in
`shared_preload_libraries` according to the PostgreSQL provider's extension
procedure. For example, after the provider parameter change and database
restart:

```sql
CREATE EXTENSION pg_bigm;
CREATE EXTENSION pg_jieba;
```

Then start the server with `SEARCH_MODE=postgres`. The server never installs
extensions or changes PostgreSQL parameters. It validates extension presence,
preload state, tokenization/query behavior, and operator classes, then
reconciles five application-owned expression indexes under an advisory lock.
If any required capability is missing, explicit postgres mode fails startup
instead of silently degrading. Ordinary schema migrations remain independent
from this optional capability.

The container runs as uid 65532, needs only a writable `/tmp`, and supports a
read-only root filesystem. Drop all Linux capabilities and set
`no-new-privileges`. Terminate with SIGTERM. Readiness drops first, delivery
workers stop taking claims, in-flight HTTP and delivery work drains, and only
the configured graceful timeout cancels a delivery so another HA replica can
recover its expired lease.

## Build reproducibility

`make generate-web` builds the pinned npm lockfile, atomically synchronizes the
checked-in production `dist`, and generates only the asset metadata manifest.
`make verify-generated` fails on drift. A fresh `go test ./...` does not need
Node or an ignored `web/dist`. `make release-server` builds a trimpath,
CGO-free binary; `make docker-server` runs the equivalent multi-stage build.
