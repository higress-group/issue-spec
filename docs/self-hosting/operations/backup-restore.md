# Backup, restore, upgrade and recovery

Treat PostgreSQL and the mounted key material as one recovery unit. A database
dump without the encryption key cannot recover webhook credentials; a key
snapshot without the matching database state cannot recover identity or audit
history.

## Backup and restore smoke

1. Quiesce writes with the load balancer or scale application replicas to zero.
2. Create a PostgreSQL custom-format dump.
3. Snapshot the token pepper, encryption-key set, bootstrap/provider secret
   references and their key identifiers into encrypted operator storage.
4. Record the server image digest and embedded schema version with the backup.
5. Restore into an isolated PostgreSQL instance, mount a copied key set, start
   the exact saved image with `MIGRATIONS_MODE=validate`, and require `/readyz`
   plus an authorized encrypted-webhook read before declaring the backup good.

`make backup-smoke BACKUP_DIR=/secure/snapshot KEY_DIR=/run/issue-spec-secrets
DATABASE_URL=...` creates the two local artifacts and verifies neither is
empty. It deliberately does not upload, encrypt or retain them; those are
operator policy decisions.

Restore with `pg_restore --clean --if-exists --no-owner --dbname "$DATABASE_URL"
issue-spec.pgdump`, restore the key files at their configured absolute paths,
then start one validating replica. Never run a restored database against a new
key generated under the old key identifier.

## Upgrades and rollback

The current embedded schema is version 8. `MIGRATIONS_MODE=auto` takes the
database migration advisory lock and applies forward migrations before the
listener starts. Production rollouts should first run one instance in auto
mode, then run all application replicas with `MIGRATIONS_MODE=validate`.
Readiness checks both connectivity and exact embedded migration state.

Keep the prior image and database/key backup for one release. Application
rollback is supported only while the new migration remains readable by the
prior release. If a release marks a migration destructive, restore the paired
database/key snapshot instead of starting old code on the new schema. Do not
manually edit the migration ledger.

Break-glass access is an offline database-operator action: use the server
administration recovery flow to mint a short-lived, one-time credential for an
existing site administrator. Every mint and consume operation writes audit
evidence; there is no network mint endpoint.
