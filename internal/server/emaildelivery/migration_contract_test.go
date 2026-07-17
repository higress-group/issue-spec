package emaildelivery

import (
	"os"
	"strings"
	"testing"
)

func TestVerifiedEmailMigrationContract(t *testing.T) {
	contents, err := os.ReadFile("../store/migrations/0018_verified_email_mentions.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(contents)
	for _, required := range []string{
		"ADD COLUMN onboarding_completed_at timestamptz",
		"ADD COLUMN notification_email text",
		"notification_email_key text GENERATED ALWAYS AS (lower(notification_email)) STORED",
		"ADD COLUMN notification_email_verified_at timestamptz",
		"CREATE TABLE email_verification_requests",
		"CREATE TABLE comment_mentions",
		"CREATE TABLE change_notification_milestones",
		"CREATE TABLE email_deliveries",
		"kind IN ('verification', 'mention', 'repo_issue_created', 'change_milestone')",
		"state IN ('pending', 'delivering', 'succeeded', 'failed', 'suppressed')",
		"WHERE onboarding_completed_at IS NULL",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("migration is missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"SET notification_email = email",
		"ALTER TABLE repo_subscriptions",
		"CREATE TABLE notification_preferences",
		"CREATE TABLE webhook_",
	} {
		if strings.Contains(sql, forbidden) {
			t.Errorf("migration contains forbidden compatibility change %q", forbidden)
		}
	}
}
