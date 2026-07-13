package state

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestV2StateMigratesToCurrentWithoutDroppingRunnerData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	legacy := `{
  "schema_version": 2,
  "repositories": {"github.com/o/r": {"host":"github.com","repo":"o/r","last_seen_comment_id":42}},
  "jobs": {"job-1": {"id":"job-1","repo":"o/r","status":"queued","command_idempotency_key":"cmd-1"}},
  "public_sessions": {"o/r#session-1": {"repo":"o/r","public_session_id":"session-1","acpx_record_id":"record-1","status":"running"}}
}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.SchemaVersion != SchemaVersion || loaded.Repositories["github.com/o/r"].LastSeenCommentID != 42 ||
		loaded.Jobs["job-1"].CommandIdempotencyKey != "cmd-1" || loaded.PublicSessions["o/r#session-1"].AcpxRecordID != "record-1" ||
		loaded.Deliveries == nil {
		t.Fatalf("v2 migration lost data: %+v", loaded)
	}
	if err := SaveFile(path, loaded); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	for _, required := range []string{fmt.Sprintf(`"schema_version": %d`, SchemaVersion), `"last_seen_comment_id": 42`, `"command_idempotency_key": "cmd-1"`, `"acpx_record_id": "record-1"`} {
		if !strings.Contains(string(data), required) {
			t.Fatalf("saved current state missing %s: %s", required, data)
		}
	}
}

func TestV5StateMigratesToCurrentWithoutDroppingDeliveryData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	legacy := `{
  "schema_version": 5,
  "webhook_deliveries": {
    "delivery-v5": {
      "delivery_id": "delivery-v5",
      "event_id": "event-v5",
      "subscription_id": "subscription-v5",
      "body_sha256": "body-v5",
      "raw_envelope": "eyJrIjoidiJ9",
      "schema_version": 1,
      "event_key": "issue_comment.created",
      "event_type": "issue_comment",
      "action": "created",
      "organization_id": "org-v5",
      "repository_id": "repo-v5",
      "issue_id": "issue-v5",
      "issue_number": 42,
      "comment_id": "comment-v5",
      "comment_revision": 3,
      "author_login": "octocat",
      "envelope_body_sha256": "envelope-v5",
      "received_at": "2026-07-01T01:02:03Z",
      "status": "processing",
      "attempt": 2,
      "lease_owner": "runner-v5",
      "lease_token": "lease-v5",
      "lease_until": "2026-07-01T01:07:03Z",
      "completed_at": "2026-07-01T01:08:03Z",
      "last_error": "delivery-v5-last-error",
      "outcome": "job",
      "job_id": "job-v5",
      "cancellation_id": "cancellation-v5",
      "status_writeback_key": "writeback-v5",
      "ack_pending": true,
      "ack_completed_at": "2026-07-01T01:09:03Z",
      "authoritative_revision": 3,
      "conflict_count": 1,
      "last_conflict_at": "2026-07-01T01:06:03Z"
    }
  }
}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	delivery := loaded.Deliveries["delivery-v5"]
	expected := WebhookDelivery{
		DeliveryID: "delivery-v5", EventID: "event-v5", SubscriptionID: "subscription-v5", BodySHA256: "body-v5",
		RawEnvelope: []byte(`{"k":"v"}`), SchemaVersion: 1, EventKey: "issue_comment.created", EventType: "issue_comment", Action: "created",
		OrganizationID: "org-v5", RepositoryID: "repo-v5", IssueID: "issue-v5", IssueNumber: 42, CommentID: "comment-v5",
		CommentRevision: 3, AuthorLogin: "octocat", EnvelopeBodySHA256: "envelope-v5", ReceivedAt: time.Date(2026, 7, 1, 1, 2, 3, 0, time.UTC),
		Status: DeliveryProcessing, Attempt: 2, LeaseOwner: "runner-v5", LeaseToken: "lease-v5", LeaseUntil: time.Date(2026, 7, 1, 1, 7, 3, 0, time.UTC),
		CompletedAt: time.Date(2026, 7, 1, 1, 8, 3, 0, time.UTC), LastError: "delivery-v5-last-error", Outcome: DeliveryOutcomeJob,
		JobID: "job-v5", CancellationID: "cancellation-v5", StatusWritebackKey: "writeback-v5", AckPending: true,
		AckCompletedAt: time.Date(2026, 7, 1, 1, 9, 3, 0, time.UTC), AuthoritativeRevision: 3, ConflictCount: 1,
		LastConflictAt: time.Date(2026, 7, 1, 1, 6, 3, 0, time.UTC),
	}
	if loaded.SchemaVersion != SchemaVersion || !reflect.DeepEqual(delivery, expected) {
		t.Fatalf("v5 migration lost delivery data: schema=%d delivery=%+v", loaded.SchemaVersion, delivery)
	}
	if err := SaveFile(path, loaded); err != nil {
		t.Fatal(err)
	}
	reloaded, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.Deliveries["delivery-v5"]; !reflect.DeepEqual(got, expected) {
		t.Fatalf("v5 delivery changed after current-schema resave: before=%+v after=%+v", delivery, got)
	}
}

func TestV3DeliveryMigratesToCurrentWithoutInventingDecision(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	legacy := `{"schema_version":3,"webhook_deliveries":{"delivery-1":{"delivery_id":"delivery-1","status":"processing","lease_owner":"old","lease_token":"token"}}}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	delivery := loaded.Deliveries["delivery-1"]
	if loaded.SchemaVersion != SchemaVersion || delivery.Outcome != "" || delivery.AckPending || delivery.JobID != "" {
		t.Fatalf("v3 delivery migration invented processing state: schema=%d delivery=%+v", loaded.SchemaVersion, delivery)
	}
}
