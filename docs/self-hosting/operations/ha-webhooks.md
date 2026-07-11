# HA webhook delivery operations

Run at least two identical replicas against the same PostgreSQL database.
Outbox expansion and delivery claims use row locking, `SKIP LOCKED`, repository
sequence ordering and fenced representation versions. A claim's
`next_attempt_at` is its lease expiry; if a worker dies, another replica can
claim it after expiry without changing the stable event or delivery ID.

SIGTERM sets readiness false and calls `StopClaims` before HTTP shutdown.
Workers check quiescence before every new expansion and claim. A worker already
inside `ProcessOne` uses the drain context and may finish; after
`GRACEFUL_SHUTDOWN_TIMEOUT`, cancellation releases database/network resources
and the lease is recovered by another replica.

Retryable 408, 429 and 5xx responses use the subscription's bounded backoff.
Terminal 4xx and exhausted attempts move to the dead-letter state. Authorized
operators inspect `/api/v1/orgs/{org}/repos/{repo}/deliveries/{delivery}` and
request redelivery with the corresponding `POST .../redeliver` endpoint. Never
delete delivery or attempt rows to force a retry.

Webhook secret rotation creates a new encrypted version while the previous
version is accepted only for the configured overlap. Rotate the wrapping key by
adding new base64 material under a new ID in `WEBHOOK_ENCRYPTION_KEYS_FILE`,
keeping all old IDs, and changing `current` to the new ID. Rotate every active
subscription secret so new ciphertext uses the current ID; verify no active or
overlap secret row refers to the old ID before removing it after the backup
retention window. Never reuse an identifier for different material.

`GET /livez` reports process life only. `GET /readyz` requires the server to be
accepting, the critical delivery worker to be running, PostgreSQL connectivity,
and migration version 8. `/metrics` exposes aggregate request/error counters
only; it contains no Authorization value, token, tenant URL, repository name or
webhook destination. Structured request logs contain request ID, method,
status, and duration, but not path/query/header/body. Delivery and credential
audit rows are the durable source for tenant-scoped investigation.
