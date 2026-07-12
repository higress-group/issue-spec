package store

import (
	"bytes"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestWebhookRevocationMigrationPreservesLegacyUnsafeRowsAndImmutableHistory(t *testing.T) {
	pool := newIntegrationPool(t)
	applyMigrationPrefix(t, pool, 10)
	userID, orgID, repoID := uuid.New(), uuid.New(), uuid.New()
	subscriptionID, secretID, eventID, deliveryID, auditID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	const legacyURL = "https://runner.example.test/hook?access_token=legacy-secret"
	ciphertext := []byte("legacy-encrypted-secret")
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO users (id, login, display_name) VALUES ($1, 'legacy-webhook', 'Legacy Webhook')`, []any{userID}},
		{`INSERT INTO orgs (id, name, display_name) VALUES ($1, 'legacy-webhook', 'Legacy Webhook')`, []any{orgID}},
		{`INSERT INTO repos (id, organization_id, name, display_name) VALUES ($1, $2, 'repo', 'Repo')`, []any{repoID, orgID}},
		{`INSERT INTO webhook_subscriptions
			(id, organization_id, repository_id, scope_type, url, event_types, created_by_user_id)
			VALUES ($1, $2, $3, 'repository', $4, ARRAY['issue_comment.created'], $5)`,
			[]any{subscriptionID, orgID, repoID, legacyURL, userID}},
		{`INSERT INTO webhook_secret_versions
			(id, organization_id, repository_id, subscription_id, version, secret_ciphertext,
			 encryption_key_id, created_by_user_id)
			VALUES ($1, $2, $3, $4, 1, $5, 'legacy', $6)`,
			[]any{secretID, orgID, repoID, subscriptionID, ciphertext, userID}},
		{`INSERT INTO event_outbox
			(id, organization_id, repository_id, aggregate_type, aggregate_id, event_type,
			 event_key, payload_hash, payload)
			VALUES ($1, $2, $3, 'comment', $4, 'issue_comment.created', 'legacy-webhook-event', $5, '{}'::jsonb)`,
			[]any{eventID, orgID, repoID, uuid.New(), []byte("legacy-event-hash")}},
		{`INSERT INTO webhook_deliveries
			(id, organization_id, repository_id, event_id, subscription_id, secret_version_id)
			VALUES ($1, $2, $3, $4, $5, $6)`, []any{deliveryID, orgID, repoID, eventID, subscriptionID, secretID}},
		{`INSERT INTO audit_events
			(id, organization_id, repository_id, actor_user_id, actor_identity_key, action,
			 resource_type, resource_id, request_id, metadata)
			VALUES ($1, $2, $3, $4, 'user:legacy-webhook', 'legacy.fixture',
			 'webhook_subscription', $5, 'legacy-request', '{"preserve":true}'::jsonb)`,
			[]any{auditID, orgID, repoID, userID, subscriptionID}},
	}
	for _, statement := range statements {
		if _, err := pool.Exec(t.Context(), statement.query, statement.args...); err != nil {
			t.Fatalf("seed v10 webhook fixture: %v\n%s", err, statement.query)
		}
	}
	var deliveryStateBefore, auditBefore string
	var deliveryVersionBefore int64
	if err := pool.QueryRow(t.Context(), `SELECT state, representation_version FROM webhook_deliveries WHERE id = $1`,
		deliveryID).Scan(&deliveryStateBefore, &deliveryVersionBefore); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), `SELECT metadata::text FROM audit_events WHERE id = $1`, auditID).Scan(&auditBefore); err != nil {
		t.Fatal(err)
	}

	if err := RunMigrations(t.Context(), pool); err != nil {
		t.Fatalf("upgrade v10 to v11: %v", err)
	}
	var storedURL string
	var revokedAt *time.Time
	if err := pool.QueryRow(t.Context(), `SELECT url, revoked_at FROM webhook_subscriptions WHERE id = $1`,
		subscriptionID).Scan(&storedURL, &revokedAt); err != nil {
		t.Fatal(err)
	}
	var storedCiphertext []byte
	if err := pool.QueryRow(t.Context(), `SELECT secret_ciphertext FROM webhook_secret_versions WHERE id = $1`,
		secretID).Scan(&storedCiphertext); err != nil {
		t.Fatal(err)
	}
	var deliveryStateAfter, auditAfter string
	var deliveryVersionAfter int64
	if err := pool.QueryRow(t.Context(), `SELECT state, representation_version FROM webhook_deliveries WHERE id = $1`,
		deliveryID).Scan(&deliveryStateAfter, &deliveryVersionAfter); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), `SELECT metadata::text FROM audit_events WHERE id = $1`, auditID).Scan(&auditAfter); err != nil {
		t.Fatal(err)
	}
	if storedURL != legacyURL || revokedAt != nil || !bytes.Equal(storedCiphertext, ciphertext) ||
		deliveryStateAfter != deliveryStateBefore || deliveryVersionAfter != deliveryVersionBefore || auditAfter != auditBefore {
		t.Fatalf("migration mutated history url=%q revoked=%v secret=%q delivery=%s/%d audit=%s",
			storedURL, revokedAt, storedCiphertext, deliveryStateAfter, deliveryVersionAfter, auditAfter)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE webhook_subscriptions SET active = false,
		revoked_at = clock_timestamp(), updated_at = clock_timestamp() WHERE id = $1`, subscriptionID); err != nil {
		t.Fatalf("legacy unsafe row could not be terminally revoked: %v", err)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE webhook_subscriptions SET active = true WHERE id = $1`, subscriptionID); err == nil {
		t.Fatal("migration trigger allowed terminal legacy row to resume")
	}
}
