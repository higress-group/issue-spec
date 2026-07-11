package outbox_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	issueapi "github.com/higress-group/issue-spec/internal/server/api/github/issues"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
	"github.com/higress-group/issue-spec/internal/server/authz"
	"github.com/higress-group/issue-spec/internal/server/events/outbox"
	"github.com/higress-group/issue-spec/internal/server/models"
	"github.com/higress-group/issue-spec/internal/server/projection/artifacts"
	"github.com/higress-group/issue-spec/internal/server/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestCommentMutationProjectionAndOutboxAreAtomic(t *testing.T) {
	pool, scope, owner := environment(t)
	authorizer, err := authz.New(pool)
	if err != nil {
		t.Fatal(err)
	}
	service, err := issueapi.NewService(store.New(pool), authorizer, artifacts.MarkerProjector{}, outbox.Hook{})
	if err != nil {
		t.Fatal(err)
	}
	subject := authz.Authenticated(owner)
	_, issue, err := service.CreateIssue(t.Context(), "acme", "widgets", subject,
		models.NewIssue{Title: "event issue", Body: "raw issue"})
	if err != nil {
		t.Fatal(err)
	}
	raw := "<!-- issue-spec:type=PROCESS id=PROCESS-007 version=1 -->\r\nAgent: Worker\nType: PROCESS\nID: PROCESS-007\nStatus: done\nScope: raw\nLinks:\n\n## Raw  \n"
	_, comment, err := service.CreateComment(t.Context(), "acme", "widgets", issue.Issue.Number, subject, raw)
	if err != nil {
		t.Fatal(err)
	}
	var event outbox.Envelope
	var eventID uuid.UUID
	var schemaVersion int
	var sequence int64
	if err := pool.QueryRow(t.Context(), `SELECT id, schema_version, repository_sequence, payload
		FROM event_outbox WHERE organization_id = $1 AND repository_id = $2
		AND event_type = 'issue_comment.created'`, scope.OrgID, scope.RepoID).
		Scan(&eventID, &schemaVersion, &sequence, &event); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte(raw))
	if schemaVersion != 1 || sequence <= 0 || event.EventID != eventID || event.RawBody != raw ||
		event.BodyHash != stringHex(hash[:]) || event.Comment == nil ||
		event.Comment.StableID != comment.Comment.ID || event.EventKey != "issue_comment.created:"+comment.Comment.ID.String()+":v1" {
		t.Fatalf("event = %+v schema=%d sequence=%d", event, schemaVersion, sequence)
	}
	encoded, _ := json.Marshal(event)
	var decoded map[string]any
	_ = json.Unmarshal(encoded, &decoded)
	issueEnvelope, _ := decoded["issue"].(map[string]any)
	if _, exists := issueEnvelope["representation_version"]; exists {
		t.Fatalf("comment envelope invented issue representation version: %s", encoded)
	}
	mutation := issueapi.MutationEvent{Type: "issue_comment.created", Scope: scope,
		Issue:   models.Issue{ID: comment.Comment.IssueID, Scope: scope, Number: issue.Issue.Number},
		Comment: &comment, RawBody: raw, BodyHash: hash, ActorUserID: owner.User.ID,
		RepresentationVersion: comment.Comment.RepresentationVersion}
	beforeRetry := rowCount(t, pool, "event_outbox")
	if err := store.New(pool).WithinTx(t.Context(), func(tx *store.Tx) error {
		repository := tx.ScopedRepo(scope)
		if err := (outbox.Hook{}).Emit(t.Context(), repository, mutation); err != nil {
			return err
		}
		return (outbox.Hook{}).Emit(t.Context(), repository, mutation)
	}); err != nil {
		t.Fatalf("idempotent hook retry: %v", err)
	}
	if afterRetry := rowCount(t, pool, "event_outbox"); afterRetry != beforeRetry {
		t.Fatalf("idempotent hook rows=%d->%d", beforeRetry, afterRetry)
	}
	var projections int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM issue_spec_typed_comments
		WHERE comment_id = $1`, comment.Comment.ID).Scan(&projections); err != nil || projections != 1 {
		t.Fatalf("projection count=%d err=%v", projections, err)
	}

	failing, err := issueapi.NewService(store.New(pool), authorizer, artifacts.MarkerProjector{}, failAfterInsert{delegate: outbox.Hook{}})
	if err != nil {
		t.Fatal(err)
	}
	beforeComments, beforeEvents, beforeProjections := rowCount(t, pool, "comments"), rowCount(t, pool, "event_outbox"), rowCount(t, pool, "issue_spec_typed_comments")
	if _, _, err := failing.CreateComment(t.Context(), "acme", "widgets", issue.Issue.Number, subject,
		strings.Replace(raw, "PROCESS-007", "PROCESS-FAIL", 2)); err == nil {
		t.Fatal("failing hook committed")
	}
	if rowCount(t, pool, "comments") != beforeComments || rowCount(t, pool, "event_outbox") != beforeEvents ||
		rowCount(t, pool, "issue_spec_typed_comments") != beforeProjections {
		t.Fatal("comment, projection, or outbox escaped hook rollback")
	}
}

type failAfterInsert struct{ delegate outbox.Hook }

func (h failAfterInsert) Emit(ctx context.Context, repository store.RepoStore, event issueapi.MutationEvent) error {
	if err := h.delegate.Emit(ctx, repository, event); err != nil {
		return err
	}
	return errors.New("injected hook failure")
}

func stringHex(value []byte) string {
	const alphabet = "0123456789abcdef"
	result := make([]byte, len(value)*2)
	for index, item := range value {
		result[index*2], result[index*2+1] = alphabet[item>>4], alphabet[item&15]
	}
	return string(result)
}

func rowCount(t *testing.T, pool *pgxpool.Pool, table string) int {
	var count int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM `+pgx.Identifier{table}.Sanitize()).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func environment(t *testing.T) (*pgxpool.Pool, models.RepoScope, serverauth.Principal) {
	pool := migratedPool(t)
	orgID, repoID, userID := uuid.New(), uuid.New(), uuid.New()
	if _, err := pool.Exec(t.Context(), `INSERT INTO users (id, login, display_name) VALUES ($1, 'owner', 'owner')`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO orgs (id, name, display_name, base_permission) VALUES ($1, 'acme', 'acme', 'read')`, orgID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO repos
		(id, organization_id, name, display_name, visibility, contribution_policy)
		VALUES ($1, $2, 'widgets', 'widgets', 'private', 'members')`, repoID, orgID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO org_memberships
		(organization_id, user_id, role, state, activated_at) VALUES ($1, $2, 'owner', 'active', clock_timestamp())`, orgID, userID); err != nil {
		t.Fatal(err)
	}
	sessionID := uuid.New()
	if _, err := pool.Exec(t.Context(), `INSERT INTO sessions
		(id, user_id, token_prefix, token_hash, csrf_hash, idle_expires_at, absolute_expires_at)
		VALUES ($1, $2, $3, $4, $5, clock_timestamp() + interval '1 hour', clock_timestamp() + interval '2 hours')`,
		sessionID, userID, "session-"+sessionID.String(), []byte(sessionID.String()), []byte("csrf")); err != nil {
		t.Fatal(err)
	}
	return pool, models.RepoScope{OrgID: orgID, RepoID: repoID}, serverauth.Principal{
		User: serverauth.User{ID: userID, Login: "owner", Status: "active"}, Kind: serverauth.CredentialSession, CredentialID: sessionID,
	}
}

func migratedPool(t *testing.T) *pgxpool.Pool {
	databaseURL := strings.TrimSpace(os.Getenv("TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL")
	}
	admin, err := pgxpool.New(t.Context(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := "outbox_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(t.Context(), "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+quoted+" CASCADE") })
	config, _ := pgxpool.ParseConfig(databaseURL)
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := store.RunMigrations(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	return pool
}
