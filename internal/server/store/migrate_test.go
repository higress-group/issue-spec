package store

import (
	"encoding/hex"
	"testing"
)

func TestEmbeddedMigrationsMetadata(t *testing.T) {
	migrations, err := EmbeddedMigrations()
	if err != nil {
		t.Fatalf("EmbeddedMigrations() error = %v", err)
	}
	if len(migrations) != int(LatestSchemaVersion) {
		t.Fatalf("len(EmbeddedMigrations()) = %d, want %d", len(migrations), LatestSchemaVersion)
	}
	for i, migration := range migrations {
		wantVersion := int64(i + 1)
		if migration.Version != wantVersion {
			t.Errorf("migration[%d].Version = %d, want %d", i, migration.Version, wantVersion)
		}
		digest, err := hex.DecodeString(migration.Checksum)
		if err != nil {
			t.Fatalf("migration[%d] checksum is not hex: %v", i, err)
		}
		if len(digest) != 32 {
			t.Errorf("migration[%d] checksum length = %d bytes, want 32", i, len(digest))
		}
	}
	if migrations[len(migrations)-1].Version != LatestSchemaVersion {
		t.Errorf("latest embedded version = %d, LatestSchemaVersion = %d", migrations[len(migrations)-1].Version, LatestSchemaVersion)
	}

	// The exported slice is a metadata copy, so callers cannot mutate the
	// embedded migration registry used by RunMigrations.
	migrations[0].Name = "changed.sql"
	again, err := EmbeddedMigrations()
	if err != nil {
		t.Fatalf("second EmbeddedMigrations() error = %v", err)
	}
	if again[0].Name != "0001_initial.sql" {
		t.Fatalf("embedded migration metadata was mutable: %q", again[0].Name)
	}
}

func TestLoadMigrationsIncludesCompleteInitialSchema(t *testing.T) {
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations() error = %v", err)
	}
	if len(migrations) < 2 {
		t.Fatalf("loadMigrations() returned %d migrations, want initial and auth migrations", len(migrations))
	}
	for _, table := range []string{
		"users", "identities", "auth_providers", "oauth_login_transactions",
		"sessions", "personal_access_tokens", "pat_scopes", "delegated_tokens",
		"orgs", "org_memberships", "repos", "repo_collaborators",
		"repo_subscriptions", "site_role_assignments", "bootstrap_state",
		"audit_events", "issues", "comments", "labels", "issue_labels",
		"comment_reactions", "source_bindings", "external_references",
		"external_evidence", "webhook_subscriptions", "webhook_secret_versions",
		"event_outbox", "webhook_deliveries", "webhook_delivery_attempts",
		"issue_spec_artifacts", "issue_spec_typed_comments", "projection_anomalies",
	} {
		needle := "CREATE TABLE " + table + " ("
		if !containsSQL(migrations[0].sql, needle) {
			t.Errorf("initial migration is missing %q", needle)
		}
	}
	for _, table := range []string{"pat_repositories", "service_accounts", "recovery_credentials"} {
		needle := "CREATE TABLE " + table + " ("
		if !containsSQL(migrations[1].sql, needle) {
			t.Errorf("auth migration is missing %q", needle)
		}
	}
}

func TestProtocolFeatureMigrationMetadata(t *testing.T) {
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations() error = %v", err)
	}
	protocol := migrations[4]
	if protocol.Version != 5 || protocol.Name != "0005_protocol_features.sql" {
		t.Fatalf("protocol migration = %+v", protocol.MigrationInfo)
	}
	for _, contract := range []string{
		"ADD COLUMN compatibility_id bigint GENERATED ALWAYS AS",
		"comment_reactions_compatibility_id_unique UNIQUE (compatibility_id)",
		"comment_reactions_key_valid CHECK",
		"CREATE INDEX comment_reactions_repo_comment_list_idx\n    ON comment_reactions (\n        organization_id,\n        repository_id,\n        comment_id,\n        created_at,\n        id\n    )",
		"CREATE INDEX issue_labels_repo_issue_created_idx",
	} {
		if !containsSQL(protocol.sql, contract) {
			t.Errorf("protocol feature migration is missing %q", contract)
		}
	}
}

func TestWebhookOutboxMigrationMetadata(t *testing.T) {
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations() error = %v", err)
	}
	webhooks := migrations[5]
	if webhooks.Version != 6 || webhooks.Name != "0006_webhook_outbox.sql" {
		t.Fatalf("webhook migration = %+v, want webhook outbox migration", webhooks.MigrationInfo)
	}
	for _, contract := range []string{
		"ADD COLUMN next_event_sequence bigint NOT NULL DEFAULT 1",
		"ADD COLUMN schema_version integer NOT NULL DEFAULT 1",
		"ADD COLUMN repository_sequence bigint",
		"event_outbox_repository_sequence_unique UNIQUE",
		"CREATE FUNCTION allocate_event_repository_sequence()",
		"CREATE TRIGGER event_outbox_allocate_repository_sequence",
		"ADD COLUMN retry_max_attempts integer NOT NULL DEFAULT 8",
		"ADD COLUMN retry_initial_backoff interval NOT NULL",
		"ADD COLUMN retry_max_backoff interval NOT NULL",
		"ADD COLUMN encryption_key_id text NOT NULL DEFAULT 'legacy'",
		"ADD COLUMN accept_until timestamptz",
		"ADD COLUMN revoked_at timestamptz",
	} {
		if !containsSQL(webhooks.sql, contract) {
			t.Errorf("webhook outbox migration is missing %q", contract)
		}
	}
}

func TestBindingsEvidenceMigrationMetadata(t *testing.T) {
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations() error = %v", err)
	}
	bindingsEvidence := migrations[6]
	if bindingsEvidence.Version != 7 || bindingsEvidence.Name != "0007_bindings_evidence.sql" {
		t.Fatalf("bindings/evidence migration = %+v", bindingsEvidence.MigrationInfo)
	}
	for _, contract := range []string{
		"ADD COLUMN visibility text NOT NULL DEFAULT 'repository'",
		"external_references_external_unique UNIQUE (",
		"external_repository_id,\n        external_id",
		"CREATE TABLE repository_evidence_policies (",
		"CREATE TABLE repository_evidence_requirements (",
		"CREATE TABLE repository_evidence_writers (",
		"CREATE INDEX source_bindings_repo_active_version_idx",
		"CREATE INDEX external_evidence_repo_revision_idx",
		"CREATE INDEX repository_evidence_policy_version_idx",
		"CREATE INDEX repository_evidence_writer_active_idx",
	} {
		if !containsSQL(bindingsEvidence.sql, contract) {
			t.Errorf("bindings/evidence migration is missing %q", contract)
		}
	}
}

func containsSQL(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
