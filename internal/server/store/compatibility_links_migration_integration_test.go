package store

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestLegacyCommentLinkMigrationUpgradesV9MarkdownAndPreservesImmutableHistory(t *testing.T) {
	pool := newIntegrationPool(t)
	applyMigrationPrefix(t, pool, 9)

	const (
		legacyA int64 = 2702158502842464763
		safeA   int64 = 9005925674908155
		legacyB int64 = 8348327966965602268
		safeB   int64 = 7661457075443676
		safeOld int64 = 4398412918452088
	)
	commentA := uuid.MustParse("60ea2b7a-854d-417d-9d83-a0262f4a13bb")
	commentB := uuid.MustParse("acfd1a5c-2d37-49a8-bf86-4caba6c3a529")
	commentSafe := uuid.MustParse("00000000-0000-0000-0000-000000000114")
	userID, orgID, repoID, issueID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	artifactID, projectionID := uuid.New(), uuid.New()
	eventID, subscriptionID, secretID := uuid.New(), uuid.New(), uuid.New()
	deliveryID, attemptID, auditID := uuid.New(), uuid.New(), uuid.New()

	legacyFragmentA := fmt.Sprintf("#issuecomment-%d", legacyA)
	safeFragmentA := fmt.Sprintf("#issuecomment-%d", safeA)
	legacyFragmentB := fmt.Sprintf("#issuecomment-%d", legacyB)
	safeFragmentB := fmt.Sprintf("#issuecomment-%d", safeB)
	safeFragmentUnchanged := fmt.Sprintf("#issuecomment-%d", safeOld)
	unknownFragment := "#issuecomment-999999999999999999999999999999"
	rewrite := strings.NewReplacer(legacyFragmentA, safeFragmentA, legacyFragmentB, safeFragmentB)

	issueBody := "issue one https://issues.test/o/r/issues/1" + legacyFragmentA +
		" two " + legacyFragmentB + " repeat " + legacyFragmentA +
		" safe " + safeFragmentUnchanged + " unknown " + unknownFragment
	commentBody := "<!-- issue-spec:type=PROCESS id=PROCESS-900 version=1 -->\n" +
		"Links:\n- Related Comments: https://issues.test/o/r/issues/1" + legacyFragmentA +
		", https://issues.test/o/r/issues/1" + legacyFragmentB +
		"\nrepeat " + legacyFragmentA + " safe " + safeFragmentUnchanged +
		" unknown " + unknownFragment

	artifactMetadata, err := json.Marshal(map[string]any{
		"marker_version": 1,
		"source":         map[string]any{"kind": "issue", "stable_id": issueID.String()},
		"related":        []string{"https://issues.test/o/r/issues/1" + legacyFragmentA, "literal"},
	})
	if err != nil {
		t.Fatal(err)
	}
	typedMetadata, err := json.Marshal(map[string]any{
		"status": "confirmed",
		"source": map[string]any{"kind": "comment", "stable_id": commentA.String()},
		"links": map[string]any{"Related Comments": []string{
			"https://issues.test/o/r/issues/1" + legacyFragmentA,
			"https://issues.test/o/r/issues/1" + legacyFragmentB,
			"https://issues.test/o/r/issues/1" + safeFragmentUnchanged,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	eventPayload, err := json.Marshal(map[string]any{
		"schema_version": 1,
		"comment": map[string]any{
			"stable_id": commentA.String(), "numeric_id": legacyA,
		},
		"raw_body": commentBody,
	})
	if err != nil {
		t.Fatal(err)
	}
	eventHash := []byte("immutable-event-hash")
	attemptHeaders := `{"X-Legacy-Link":"https://issues.test/o/r/issues/1` + legacyFragmentA + `"}`
	auditMetadata := `{"stable_id":"` + commentA.String() + `","legacy_link":"` + legacyFragmentA + `"}`

	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO users (id, login, display_name) VALUES ($1, 'link-upgrade', 'link-upgrade')`, []any{userID}},
		{`INSERT INTO orgs (id, name, display_name) VALUES ($1, 'link-upgrade', 'link-upgrade')`, []any{orgID}},
		{`INSERT INTO repos (id, organization_id, name, display_name) VALUES ($1, $2, 'repo', 'repo')`, []any{repoID, orgID}},
		{`INSERT INTO issues (id, organization_id, repository_id, number, author_id, title, body)
			VALUES ($1, $2, $3, 1, $4, 'upgrade links', $5)`, []any{issueID, orgID, repoID, userID, issueBody}},
		{`INSERT INTO comments (id, organization_id, repository_id, issue_id, author_id, body)
			VALUES ($1, $2, $3, $4, $5, $6)`, []any{commentA, orgID, repoID, issueID, userID, commentBody}},
		{`INSERT INTO comments (id, organization_id, repository_id, issue_id, author_id, body)
			VALUES ($1, $2, $3, $4, $5, $6)`, []any{commentB, orgID, repoID, issueID, userID, "already safe " + safeFragmentUnchanged}},
		{`INSERT INTO comments (id, organization_id, repository_id, issue_id, author_id, body)
			VALUES ($1, $2, $3, $4, $5, 'safe source')`, []any{commentSafe, orgID, repoID, issueID, userID}},
		{`INSERT INTO issue_spec_artifacts
			(id, organization_id, repository_id, issue_id, change_key, artifact_type, content, metadata, created_by_user_id)
			VALUES ($1, $2, $3, $4, 'upgrade-links', 'proposal', $5, $6::jsonb, $7)`,
			[]any{artifactID, orgID, repoID, issueID, issueBody, string(artifactMetadata), userID}},
		{`INSERT INTO issue_spec_typed_comments
			(id, organization_id, repository_id, issue_id, comment_id, comment_type, comment_key, body, metadata, created_by_user_id)
			VALUES ($1, $2, $3, $4, $5, 'PROCESS', 'PROCESS-900', $6, $7::jsonb, $8)`,
			[]any{projectionID, orgID, repoID, issueID, commentA, commentBody, string(typedMetadata), userID}},
		{`INSERT INTO webhook_subscriptions
			(id, organization_id, repository_id, scope_type, url, event_types, created_by_user_id)
			VALUES ($1, $2, $3, 'repository', 'https://hooks.example.test/events', ARRAY['issue_comment.created'], $4)`,
			[]any{subscriptionID, orgID, repoID, userID}},
		{`INSERT INTO webhook_secret_versions
			(id, organization_id, repository_id, subscription_id, version, secret_ciphertext, created_by_user_id)
			VALUES ($1, $2, $3, $4, 1, $5, $6)`, []any{secretID, orgID, repoID, subscriptionID, []byte("ciphertext"), userID}},
		{`INSERT INTO event_outbox
			(id, organization_id, repository_id, schema_version, aggregate_type, aggregate_id,
			 event_type, event_key, payload_hash, payload)
			VALUES ($1, $2, $3, 1, 'comment', $4, 'issue_comment.created',
			 'immutable-link-event', $5, $6::jsonb)`, []any{eventID, orgID, repoID, commentA, eventHash, string(eventPayload)}},
		{`INSERT INTO webhook_deliveries
			(id, organization_id, repository_id, event_id, subscription_id, secret_version_id)
			VALUES ($1, $2, $3, $4, $5, $6)`, []any{deliveryID, orgID, repoID, eventID, subscriptionID, secretID}},
		{`INSERT INTO webhook_delivery_attempts
			(id, organization_id, repository_id, delivery_id, attempt_number, request_headers, response_status, response_body)
			VALUES ($1, $2, $3, $4, 1, $5::jsonb, 202, $6)`,
			[]any{attemptID, orgID, repoID, deliveryID, attemptHeaders, "accepted " + legacyFragmentA}},
		{`INSERT INTO audit_events
			(id, organization_id, repository_id, actor_user_id, actor_identity_key, action,
			 resource_type, resource_id, request_id, metadata)
			VALUES ($1, $2, $3, $4, 'user:link-upgrade', 'migration.fixture', 'comment', $5,
			 'request-link-upgrade', $6::jsonb)`, []any{auditID, orgID, repoID, userID, commentA, auditMetadata}},
	}
	for _, statement := range statements {
		if _, err := pool.Exec(t.Context(), statement.query, statement.args...); err != nil {
			t.Fatalf("seed v9 fixture: %v\n%s", err, statement.query)
		}
	}
	var storedA, storedB, storedSafe int64
	if err := pool.QueryRow(t.Context(), `SELECT
		(SELECT compatibility_id FROM comments WHERE id = $1),
		(SELECT compatibility_id FROM comments WHERE id = $2),
		(SELECT compatibility_id FROM comments WHERE id = $3)`, commentA, commentB, commentSafe).
		Scan(&storedA, &storedB, &storedSafe); err != nil {
		t.Fatal(err)
	}
	if storedA != safeA || storedB != safeB || storedSafe != safeOld {
		t.Fatalf("v9 compatibility fixtures = %d/%d/%d", storedA, storedB, storedSafe)
	}

	var eventBefore, eventHashBefore, attemptHeadersBefore, attemptBodyBefore, auditBefore string
	var eventSchemaBefore int
	var deliveryVersionBefore int64
	if err := pool.QueryRow(t.Context(), `SELECT payload::text, encode(payload_hash, 'hex'), schema_version
		FROM event_outbox WHERE id = $1`, eventID).Scan(&eventBefore, &eventHashBefore, &eventSchemaBefore); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), `SELECT representation_version FROM webhook_deliveries WHERE id = $1`, deliveryID).
		Scan(&deliveryVersionBefore); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), `SELECT request_headers::text, response_body
		FROM webhook_delivery_attempts WHERE id = $1`, attemptID).Scan(&attemptHeadersBefore, &attemptBodyBefore); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), `SELECT metadata::text FROM audit_events WHERE id = $1`, auditID).Scan(&auditBefore); err != nil {
		t.Fatal(err)
	}
	if eventHashBefore != hex.EncodeToString(eventHash) {
		t.Fatalf("seed event hash = %q", eventHashBefore)
	}

	if err := RunMigrations(t.Context(), pool); err != nil {
		t.Fatalf("upgrade v9 to v10: %v", err)
	}

	var gotIssueBody, gotCommentBody, gotSafeCommentBody string
	var issueVersion, issueCommentsVersion, commentVersion, safeCommentVersion int64
	if err := pool.QueryRow(t.Context(), `SELECT body, representation_version, comments_collection_version
		FROM issues WHERE id = $1`, issueID).Scan(&gotIssueBody, &issueVersion, &issueCommentsVersion); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), `SELECT body, representation_version FROM comments WHERE id = $1`, commentA).
		Scan(&gotCommentBody, &commentVersion); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), `SELECT body, representation_version FROM comments WHERE id = $1`, commentB).
		Scan(&gotSafeCommentBody, &safeCommentVersion); err != nil {
		t.Fatal(err)
	}
	if gotIssueBody != rewrite.Replace(issueBody) || gotCommentBody != rewrite.Replace(commentBody) {
		t.Fatalf("canonical Markdown was not rewritten:\nissue=%q\ncomment=%q", gotIssueBody, gotCommentBody)
	}
	if strings.Count(gotCommentBody, safeFragmentA) != 2 || !strings.Contains(gotCommentBody, safeFragmentB) ||
		!strings.Contains(gotCommentBody, safeFragmentUnchanged) || !strings.Contains(gotCommentBody, unknownFragment) {
		t.Fatalf("multi-link rewrite lost a safe/unknown/repeated fragment: %q", gotCommentBody)
	}
	if gotSafeCommentBody != "already safe "+safeFragmentUnchanged || issueVersion != 2 ||
		issueCommentsVersion != 2 || commentVersion != 2 || safeCommentVersion != 1 {
		t.Fatalf("canonical versions/body = issue %d/%d comment %d safe %d/%q",
			issueVersion, issueCommentsVersion, commentVersion, safeCommentVersion, gotSafeCommentBody)
	}

	var artifactContent, typedBody string
	var artifactMetadataAfter, typedMetadataAfter []byte
	var artifactVersion, typedVersion int64
	var artifactIssueID, typedCommentID, artifactCreator, typedCreator uuid.UUID
	if err := pool.QueryRow(t.Context(), `SELECT content, metadata, representation_version,
		issue_id, created_by_user_id FROM issue_spec_artifacts WHERE id = $1`, artifactID).
		Scan(&artifactContent, &artifactMetadataAfter, &artifactVersion, &artifactIssueID, &artifactCreator); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), `SELECT body, metadata, representation_version,
		comment_id, created_by_user_id FROM issue_spec_typed_comments WHERE id = $1`, projectionID).
		Scan(&typedBody, &typedMetadataAfter, &typedVersion, &typedCommentID, &typedCreator); err != nil {
		t.Fatal(err)
	}
	if artifactContent != rewrite.Replace(issueBody) || typedBody != rewrite.Replace(commentBody) ||
		artifactVersion != 2 || typedVersion != 2 || artifactIssueID != issueID ||
		typedCommentID != commentA || artifactCreator != userID || typedCreator != userID {
		t.Fatalf("projection source/content mismatch: artifact=%d/%s/%s typed=%d/%s/%s",
			artifactVersion, artifactIssueID, artifactCreator, typedVersion, typedCommentID, typedCreator)
	}
	assertProjectionMetadata(t, artifactMetadataAfter, "issue", issueID.String(), safeFragmentA)
	assertProjectionMetadata(t, typedMetadataAfter, "comment", commentA.String(), safeFragmentA, safeFragmentB, safeFragmentUnchanged)

	var repoIssuesVersion, repoCommentsVersion, repoArtifactsVersion int64
	if err := pool.QueryRow(t.Context(), `SELECT issues_collection_version,
		comments_collection_version, artifacts_collection_version FROM repos WHERE id = $1`, repoID).
		Scan(&repoIssuesVersion, &repoCommentsVersion, &repoArtifactsVersion); err != nil {
		t.Fatal(err)
	}
	if repoIssuesVersion != 2 || repoCommentsVersion != 2 || repoArtifactsVersion != 2 {
		t.Fatalf("repository collection versions = %d/%d/%d", repoIssuesVersion, repoCommentsVersion, repoArtifactsVersion)
	}

	var eventAfter, eventHashAfter, attemptHeadersAfter, attemptBodyAfter, auditAfter string
	var eventSchemaAfter int
	var deliveryVersionAfter int64
	if err := pool.QueryRow(t.Context(), `SELECT payload::text, encode(payload_hash, 'hex'), schema_version
		FROM event_outbox WHERE id = $1`, eventID).Scan(&eventAfter, &eventHashAfter, &eventSchemaAfter); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), `SELECT representation_version FROM webhook_deliveries WHERE id = $1`, deliveryID).
		Scan(&deliveryVersionAfter); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), `SELECT request_headers::text, response_body
		FROM webhook_delivery_attempts WHERE id = $1`, attemptID).Scan(&attemptHeadersAfter, &attemptBodyAfter); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), `SELECT metadata::text FROM audit_events WHERE id = $1`, auditID).Scan(&auditAfter); err != nil {
		t.Fatal(err)
	}
	if eventAfter != eventBefore || eventHashAfter != eventHashBefore || eventSchemaAfter != eventSchemaBefore ||
		deliveryVersionAfter != deliveryVersionBefore || attemptHeadersAfter != attemptHeadersBefore ||
		attemptBodyAfter != attemptBodyBefore || auditAfter != auditBefore {
		t.Fatalf("immutable history changed: event=%v hash=%v schema=%v delivery=%v headers=%v body=%v audit=%v",
			eventAfter == eventBefore, eventHashAfter == eventHashBefore, eventSchemaAfter == eventSchemaBefore,
			deliveryVersionAfter == deliveryVersionBefore, attemptHeadersAfter == attemptHeadersBefore,
			attemptBodyAfter == attemptBodyBefore, auditAfter == auditBefore)
	}
	if !strings.Contains(eventAfter, fmt.Sprintf(`"numeric_id": %d`, legacyA)) &&
		!strings.Contains(eventAfter, fmt.Sprintf(`"numeric_id":%d`, legacyA)) {
		t.Fatalf("schema-v1 event no longer contains emission-time numeric_id: %s", eventAfter)
	}

	if err := RunMigrations(t.Context(), pool); err != nil {
		t.Fatalf("repeat latest migration: %v", err)
	}
	var repeatIssueVersion, repeatCommentVersion, repeatArtifactVersion, repeatTypedVersion int64
	if err := pool.QueryRow(t.Context(), `SELECT
		(SELECT representation_version FROM issues WHERE id = $1),
		(SELECT representation_version FROM comments WHERE id = $2),
		(SELECT representation_version FROM issue_spec_artifacts WHERE id = $3),
		(SELECT representation_version FROM issue_spec_typed_comments WHERE id = $4)`,
		issueID, commentA, artifactID, projectionID).
		Scan(&repeatIssueVersion, &repeatCommentVersion, &repeatArtifactVersion, &repeatTypedVersion); err != nil {
		t.Fatal(err)
	}
	if repeatIssueVersion != 2 || repeatCommentVersion != 2 || repeatArtifactVersion != 2 || repeatTypedVersion != 2 {
		t.Fatalf("repeat migration advanced versions: %d/%d/%d/%d",
			repeatIssueVersion, repeatCommentVersion, repeatArtifactVersion, repeatTypedVersion)
	}
}

func applyMigrationPrefix(t *testing.T, pool *pgxpool.Pool, count int) {
	t.Helper()
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if count < 0 || count > len(migrations) {
		t.Fatalf("migration prefix %d outside 0..%d", count, len(migrations))
	}
	if _, err := pool.Exec(t.Context(), `CREATE TABLE schema_migrations (
		version bigint PRIMARY KEY, name text NOT NULL, checksum bytea NOT NULL,
		applied_at timestamptz NOT NULL DEFAULT clock_timestamp())`); err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations[:count] {
		if _, err := pool.Exec(t.Context(), migration.sql); err != nil {
			t.Fatalf("apply migration prefix %s: %v", migration.Name, err)
		}
		if _, err := pool.Exec(t.Context(), `INSERT INTO schema_migrations (version, name, checksum)
			VALUES ($1, $2, $3)`, migration.Version, migration.Name, migration.checksum); err != nil {
			t.Fatal(err)
		}
	}
}

func assertProjectionMetadata(t *testing.T, raw []byte, sourceKind, stableID string, fragments ...string) {
	t.Helper()
	var metadata map[string]any
	if err := json.Unmarshal(raw, &metadata); err != nil {
		t.Fatal(err)
	}
	source, ok := metadata["source"].(map[string]any)
	if !ok || source["kind"] != sourceKind || source["stable_id"] != stableID {
		t.Fatalf("projection source metadata changed: %+v", metadata["source"])
	}
	encoded := string(raw)
	for _, fragment := range fragments {
		if !strings.Contains(encoded, fragment) {
			t.Fatalf("projection metadata missing fragment %q: %s", fragment, raw)
		}
	}
	if strings.Contains(encoded, "2702158502842464763") || strings.Contains(encoded, "8348327966965602268") {
		t.Fatalf("projection metadata retained legacy fragment: %s", raw)
	}
}
