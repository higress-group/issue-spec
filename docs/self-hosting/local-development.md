# Local server development

**English | [Simplified Chinese](local-development.zh-CN.md)**

Use this guide to run `issue-spec-server` from the current checkout with a
local PostgreSQL database. The fixture is for development and evaluation only;
use the [deployment and hardening guide](operations/deployment.md) for a
production installation.

## Prerequisites

- the Go toolchain declared by [`go.mod`](../../go.mod);
- Docker with the Compose plugin;
- `make`, `openssl`, and `curl`.

Node.js is not required for a normal Go build because the checked-in web
application is embedded in the Server binary. Install the package manager
declared by [`web/package.json`](../../web/package.json) only when changing the
web application and regenerating its assets.

Create development-only secret files from the repository root before using
either path below:

```bash
mkdir -p deployments/dev/secrets
umask 077
openssl rand -hex 32 > deployments/dev/secrets/bootstrap
openssl rand -hex 32 > deployments/dev/secrets/token-pepper
openssl rand -hex 32 > deployments/dev/secrets/encryption-key
```

These files are ignored by Git. Do not reuse them in production or print their
contents in logs.

## Run the complete stack with Compose

This is the shortest path for evaluating the browser workspace:

```bash
docker compose -f deployments/dev/compose.yaml --profile server up -d --build --wait
curl -fsS http://127.0.0.1:8080/readyz
```

Open <http://127.0.0.1:8080/bootstrap> and enter the bootstrap secret from
`deployments/dev/secrets/bootstrap` to create the first local administrator.
An external identity provider is not required for this development bootstrap
path.

If the default ports are already in use, keep the published Server URL and port
in sync:

```bash
ISSUE_SPEC_POSTGRES_PORT=55432 \
ISSUE_SPEC_SERVER_PORT=18080 \
ISSUE_SPEC_PUBLIC_URL=http://127.0.0.1:18080 \
docker compose -f deployments/dev/compose.yaml --profile server up -d --build --wait
curl -fsS http://127.0.0.1:18080/readyz
```

`ISSUE_SPEC_POSTGRES_PORT` changes only the host-side PostgreSQL port. The
containerized Server still connects to PostgreSQL on the Compose network at
port 5432.

## Build the Server on the host

Start only PostgreSQL:

```bash
docker compose -f deployments/dev/compose.yaml up -d --wait postgres
```

Build the Server binary from the checked-in web assets:

```bash
make build-server
```

Configure the host process and start it:

```bash
export ENVIRONMENT=development
export LISTEN_ADDR=127.0.0.1:8080
export DATABASE_URL='postgres://issue_spec:issue_spec@127.0.0.1:5432/issue_spec?sslmode=disable'
export API_PUBLIC_URL=http://127.0.0.1:8080
export WEB_PUBLIC_URL=http://127.0.0.1:8080
export BOOTSTRAP_SECRET_FILE="$PWD/deployments/dev/secrets/bootstrap"
export TOKEN_PEPPER_FILE="$PWD/deployments/dev/secrets/token-pepper"
export ENCRYPTION_KEY_FILE="$PWD/deployments/dev/secrets/encryption-key"
export MIGRATIONS_MODE=auto
export SEARCH_MODE=disabled
./dist/issue-spec-server
```

In another terminal, require readiness before opening the browser:

```bash
curl -fsS http://127.0.0.1:8080/readyz
```

When publishing PostgreSQL on another host port, set the port before starting
the fixture and use the same port in `DATABASE_URL`:

```bash
ISSUE_SPEC_POSTGRES_PORT=55432 \
docker compose -f deployments/dev/compose.yaml up -d --wait postgres
export DATABASE_URL='postgres://issue_spec:issue_spec@127.0.0.1:55432/issue_spec?sslmode=disable'
```

`MIGRATIONS_MODE=auto` is the default and applies the embedded schema under a
PostgreSQL advisory lock. Repeated starts are safe. `SEARCH_MODE=disabled` is
also the default because the stock `postgres:17-alpine` fixture does not include
`pg_bigm` or `pg_jieba`; see
[optional PostgreSQL issue search](operations/deployment.md#optional-postgresql-issue-search)
before enabling it.

## Run Server tests against PostgreSQL

The test suite creates and removes isolated schemas when `TEST_DATABASE_URL` is
set:

```bash
export TEST_DATABASE_URL='postgres://issue_spec:issue_spec@127.0.0.1:5432/issue_spec?sslmode=disable'
make test-server
```

Keep `TEST_DATABASE_URL` synchronized with `ISSUE_SPEC_POSTGRES_PORT` when using
a non-default host port. Tests that need PostgreSQL skip when the variable is
unset.

## Choose a build artifact

- Use `make build-server` while developing from a checkout.
- `go install github.com/higress-group/issue-spec/cmd/issue-spec-server@latest`
  is convenient for Go-native evaluation. Pin an immutable commit or semantic
  version when the installed binary must be repeatable.
- Use `make docker-server IMAGE=registry.example/issue-spec-server:VERSION` to
  build the hardened runtime image for an operator-owned registry. Prefer an
  immutable image digest for production deployment and rollback.

PostgreSQL remains a separate operator-owned service in every case; do not
package it into the Server image.

## Stop or reset the fixture

Stop the containers while preserving the PostgreSQL volume:

```bash
docker compose -f deployments/dev/compose.yaml --profile server down
```

To intentionally delete all local fixture data, add `--volumes`. This cannot be
undone.
