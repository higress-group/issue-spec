package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestV2StateMigratesToV4WithoutDroppingRunnerData(t *testing.T) {
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
	if loaded.SchemaVersion != 4 || loaded.Repositories["github.com/o/r"].LastSeenCommentID != 42 ||
		loaded.Jobs["job-1"].CommandIdempotencyKey != "cmd-1" || loaded.PublicSessions["o/r#session-1"].AcpxRecordID != "record-1" ||
		loaded.Deliveries == nil {
		t.Fatalf("v2 migration lost data: %+v", loaded)
	}
	if err := SaveFile(path, loaded); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	for _, required := range []string{`"schema_version": 4`, `"last_seen_comment_id": 42`, `"command_idempotency_key": "cmd-1"`, `"acpx_record_id": "record-1"`} {
		if !strings.Contains(string(data), required) {
			t.Fatalf("saved v4 state missing %s: %s", required, data)
		}
	}
}

func TestV3DeliveryMigratesToV4WithoutInventingDecision(t *testing.T) {
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
	if loaded.SchemaVersion != 4 || delivery.Outcome != "" || delivery.AckPending || delivery.JobID != "" {
		t.Fatalf("v3 delivery migration invented processing state: schema=%d delivery=%+v", loaded.SchemaVersion, delivery)
	}
}
