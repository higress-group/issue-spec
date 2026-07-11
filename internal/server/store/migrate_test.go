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
	if len(migrations) != 1 {
		t.Fatalf("len(EmbeddedMigrations()) = %d, want 1", len(migrations))
	}
	migration := migrations[0]
	if migration.Version != 1 {
		t.Errorf("Version = %d, want 1", migration.Version)
	}
	if migration.Version != LatestSchemaVersion {
		t.Errorf("embedded version = %d, LatestSchemaVersion = %d", migration.Version, LatestSchemaVersion)
	}
	if migration.Name != "0001_initial.sql" {
		t.Errorf("Name = %q, want 0001_initial.sql", migration.Name)
	}
	digest, err := hex.DecodeString(migration.Checksum)
	if err != nil {
		t.Fatalf("Checksum is not hex: %v", err)
	}
	if len(digest) != 32 {
		t.Errorf("checksum length = %d bytes, want 32", len(digest))
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
	if len(migrations) != 1 {
		t.Fatalf("len(loadMigrations()) = %d, want 1", len(migrations))
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
}

func containsSQL(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
