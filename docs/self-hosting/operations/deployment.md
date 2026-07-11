# Self-hosted server deployment

The supported production artifact is the single `issue-spec-server` binary or
the repository `runtime` container target. The binary contains the pinned Vite
build; production never reads a host static directory.

## Required configuration

Set `ENVIRONMENT=production`, `LISTEN_ADDR`, `DATABASE_URL`,
`API_PUBLIC_URL`, and `WEB_PUBLIC_URL`. Both public origins must be HTTPS root
origins. Mount these regular, non-symlink files with mode `0600`:

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

Example provider file:

```json
{
  "providers": [
    {
      "id": "cbfb6bf8-e6e9-4d08-ad63-0b7477fc2484",
      "name": "company",
      "kind": "oidc",
      "issuer": "https://identity.example.com",
      "client_id": "issue-spec",
      "client_secret": "replace-through-secret-manager",
      "scopes": ["groups"]
    }
  ]
}
```

`github-oauth` accepts the same identity fields. `auth_url`, `token_url`, and
`user_url` are optional and intended for an operator-controlled GitHub
Enterprise endpoint or conformance fixture; the auth and token URLs must be
set together. Provider IDs and names are unique, and callback URLs are always
constructed from `API_PUBLIC_URL`, never request headers.

Use `WEBHOOK_ALLOWED_PRIVATE_CIDRS` only for explicit operator-owned internal
destinations. Loopback, link-local and cloud metadata addresses remain denied.
The same DNS resolver and policy are used for subscription preflight and the
actual delivery connection.

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
