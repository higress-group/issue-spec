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

### Optional notification email

Set `SMTP_CONFIG_FILE` only when notification email is required. The target
must use the same secret-file contract as the other protected inputs: a
regular, non-symlink file readable by the service identity, mode `0600`, with
a 64 KiB maximum. Its complete contents are one JSON object with exactly this
provider-neutral schema:

| Field | JSON type | Constraint |
| --- | --- | --- |
| `host` | string | relay DNS name or IPv4 address, without a scheme or path |
| `port` | integer | `1` through `65535`; the implicit-TLS listener |
| `username` | string | non-empty authentication identity |
| `password` | string | non-empty authentication secret |
| `from_address` | string | one syntactically valid bare sender mailbox |
| `allowed_email_domain_suffixes` | array of strings | optional notification-recipient domain suffixes |

For example, the following redacted configuration restricts notification
addresses to `example.test` and its subdomains:

```json
{
  "host": "mail.example.test",
  "port": 2465,
  "username": "mailer@example.test",
  "password": "<smtp-password>",
  "from_address": "notifications@example.test",
  "allowed_email_domain_suffixes": ["example.test"]
}
```

Suffix matching is case-insensitive and follows DNS label boundaries. A suffix
of `example.test` permits both `person@example.test` and
`person@team.example.test`, but not `person@evilexample.test` or
`person@example.test.evil.test`. Omitting the field or setting it to an empty
array preserves the existing behavior of accepting any syntactically valid
notification-email domain.

Unknown fields, malformed JSON, group/other-readable permissions, and partial
configuration fail startup with redacted errors. The server supports
authenticated SMTP over an already established TLS connection only; this file
does not configure IMAP, plaintext SMTP, or a STARTTLS downgrade.

When `SMTP_CONFIG_FILE` is absent, the server advertises mail-dependent
capabilities as disabled, hides their browser controls, does not mount mention
or repository-subscription mutation routes, and does not start the email or
verification-expiry workers. Existing issues, comments, webhook delivery, and
stored subscription rows remain usable and unchanged.

Rotate the relay credential by atomically replacing the protected file and
restarting replicas one at a time. The process reads and validates the file at
startup; it does not watch for in-place changes. Keep the prior secret version
available until every restarted replica is ready, then revoke it. Never put
the file contents, relay response text, mailbox addresses, or credentials in
command lines, logs, screenshots, or reusable deployment manifests.

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
reconciles five required application-owned indexes under an advisory lock:
repository-scoped pg_bigm and pg_jieba GIN indexes for Issue title/body text,
the same two repository-scoped GIN indexes for comment bodies, and the
active-Proposal association index. Proposal
discovery uses only Proposal Issue titles and bodies, while each repository's
Issues page may search its complete Issue and comment discussion. As a
non-normative upgrade note, an installation coming from an earlier search
version may retain obsolete unscoped pg_bigm/jieba v1 indexes or a change-key
text index; the server no longer reads or validates those indexes, and
operators may drop them later through their normal database change process
instead of an automatic startup deletion. The repository-scoped comment
pg_bigm and pg_jieba v2 indexes remain required and MUST NOT be dropped while
PostgreSQL search is enabled.
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
