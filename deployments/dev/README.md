# Local PostgreSQL and Server fixture

For prerequisites, a complete Compose path, a host-built Server path, port
overrides, migrations, testing, and cleanup, see the
[local Server development guide](../../docs/self-hosting/local-development.md)
or its [Simplified Chinese version](../../docs/self-hosting/local-development.zh-CN.md).

Start PostgreSQL 17 from the repository root:

```sh
docker compose -f deployments/dev/compose.yaml up -d --wait
export TEST_DATABASE_URL='postgres://issue_spec:issue_spec@127.0.0.1:5432/issue_spec?sslmode=disable'
go test ./internal/server/store
```

Set `ISSUE_SPEC_POSTGRES_PORT` before `docker compose up` if port 5432 is
already in use, and use the same port in the host-side `TEST_DATABASE_URL`.
Tests create and remove an isolated PostgreSQL schema; they skip cleanly when
`TEST_DATABASE_URL` is unset.

To run the complete server image, create development-only secret files and
enable the optional `server` profile:

```sh
mkdir -p deployments/dev/secrets
umask 077
openssl rand -hex 32 > deployments/dev/secrets/bootstrap
openssl rand -hex 32 > deployments/dev/secrets/token-pepper
openssl rand -hex 32 > deployments/dev/secrets/encryption-key
docker compose -f deployments/dev/compose.yaml --profile server up -d --build --wait
curl -fsS http://127.0.0.1:8080/readyz
```

When changing `ISSUE_SPEC_SERVER_PORT`, also set `ISSUE_SPEC_PUBLIC_URL` to the
matching browser origin. The detailed guide includes a complete example.

These files are local fixtures, not production secret management. Remove the
directory when the fixture is no longer needed.

The fixture passes `ISSUE_SPEC_SEARCH_MODE` to the server as `SEARCH_MODE` and
defaults it to `disabled`. The stock `postgres:17-alpine` service does not
provide `pg_bigm` or `pg_jieba`; use an operator-managed PostgreSQL instance
with both extensions installed before selecting `ISSUE_SPEC_SEARCH_MODE=postgres`.

## Production-shaped authentication fixture

The authentication overlay runs the server with production parsing and an
explicit trusted-internal HTTP posture. Copy a canonical example to the
ignored secrets directory, then edit it without printing the client secret:

```sh
install -m 600 docs/self-hosting/authentication/v1/examples/github-unrestricted.json \
  deployments/dev/secrets/auth-providers.json
${EDITOR:?set EDITOR} deployments/dev/secrets/auth-providers.json
docker compose -f deployments/dev/compose.yaml \
  -f deployments/dev/compose.auth.yaml \
  --profile server up -d --build --wait
curl -fsS http://127.0.0.1:8080/api/v1/auth/providers
```

Replace every `__ISSUE_SPEC_...__` placeholder before starting. The overlay is
an executable local-link fixture, not a source of production credentials. Its
callback is `http://127.0.0.1:8080/api/v1/auth/github/callback`; configure that
exact value in a development-only OAuth App. See the
[versioned authentication guide](../../docs/self-hosting/authentication/README.md)
for production origins, organization admission, and rotation.
