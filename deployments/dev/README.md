# Local PostgreSQL fixture

Start PostgreSQL 17 from the repository root:

```sh
docker compose -f deployments/dev/compose.yaml up -d --wait
export TEST_DATABASE_URL='postgres://issue_spec:issue_spec@127.0.0.1:5432/issue_spec?sslmode=disable'
go test ./internal/server/store
```

Set `ISSUE_SPEC_POSTGRES_PORT` before `docker compose up` if port 5432 is
already in use. Tests create and remove an isolated PostgreSQL schema; they
skip cleanly when `TEST_DATABASE_URL` is unset.
