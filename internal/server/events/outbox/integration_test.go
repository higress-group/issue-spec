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
	_, proposal, err := service.CreateIssue(t.Context(), "acme", "widgets", subject,
		models.NewIssue{Title: "proposal", Body: "<!-- issue-spec:issue=proposal change=notify version=1 -->\nbody"})
	if err != nil {
		t.Fatal(err)
	}
	var proposalEvent outbox.Envelope
	if err := pool.QueryRow(t.Context(), `SELECT payload FROM event_outbox WHERE aggregate_id = $1
		AND event_type = 'issue.created'`, proposal.Issue.ID).Scan(&proposalEvent); err != nil {
		t.Fatal(err)
	}
	if proposalEvent.Notification == nil || proposalEvent.Notification.IssueKind != "proposal" {
		t.Fatalf("proposal classification = %+v", proposalEvent.Notification)
	}
	raw := "<!-- issue-spec:type=PROCESS id=PROCESS-007 version=1 -->\r\nAgent: Worker\nType: PROCESS\nID: PROCESS-007\nStatus: done\nScope: raw\nLinks:\n\n## Raw  \n"
	_, comment, err := service.CreateComment(t.Context(), "acme", "widgets", issue.Issue.Number, subject, raw)
	if err != nil {
		t.Fatal(err)
	}
	_, issueAfterCreate, err := service.GetIssue(t.Context(), "acme", "widgets", issue.Issue.Number, subject)
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
	if event.Notification == nil || event.Notification.CommentClass != "typed" ||
		event.Notification.IssueKind != "ordinary" || event.Notification.ActorClass != "human" ||
		event.Notification.Sender.Login != "owner" || event.Notification.Comment == nil ||
		event.Notification.Comment.Body != raw {
		t.Fatalf("notification classification = %+v", event.Notification)
	}
	assertCommentIssueSnapshot(t, event, issueAfterCreate.Issue)
	mutation := issueapi.MutationEvent{Type: "issue_comment.created", Scope: scope,
		Issue: models.Issue{ID: comment.Comment.IssueID, Scope: scope, Number: issue.Issue.Number,
			CreatedAt: issueAfterCreate.Issue.CreatedAt, UpdatedAt: issueAfterCreate.Issue.UpdatedAt},
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
	var compatibilityID int64
	if err := pool.QueryRow(t.Context(), `SELECT compatibility_id FROM comments
		WHERE organization_id = $1 AND repository_id = $2 AND id = $3`, scope.OrgID, scope.RepoID, comment.Comment.ID).
		Scan(&compatibilityID); err != nil {
		t.Fatal(err)
	}
	editedRaw := strings.Replace(raw, "## Raw  ", "## Edited  ", 1)
	_, editedComment, err := service.UpdateComment(t.Context(), "acme", "widgets", compatibilityID, subject, editedRaw)
	if err != nil {
		t.Fatal(err)
	}
	_, issueAfterEdit, err := service.GetIssue(t.Context(), "acme", "widgets", issue.Issue.Number, subject)
	if err != nil {
		t.Fatal(err)
	}
	var editedEvent outbox.Envelope
	if err := pool.QueryRow(t.Context(), `SELECT payload FROM event_outbox
		WHERE organization_id = $1 AND repository_id = $2
		AND event_type = 'issue_comment.edited'`, scope.OrgID, scope.RepoID).Scan(&editedEvent); err != nil {
		t.Fatal(err)
	}
	editedHash := sha256.Sum256([]byte(editedRaw))
	if editedEvent.RawBody != editedRaw || editedEvent.BodyHash != stringHex(editedHash[:]) ||
		editedEvent.Comment == nil || editedEvent.Comment.StableID != editedComment.Comment.ID ||
		editedEvent.Comment.RepresentationVersion != editedComment.Comment.RepresentationVersion ||
		editedEvent.EventKey != "issue_comment.edited:"+editedComment.Comment.ID.String()+":v2" {
		t.Fatalf("edited event = %+v", editedEvent)
	}
	if editedEvent.Notification == nil || editedEvent.Notification.CommentClass != "typed" ||
		editedEvent.Notification.Comment == nil || editedEvent.Notification.Comment.Body != editedRaw {
		t.Fatalf("edited notification classification = %+v", editedEvent.Notification)
	}
	assertCommentIssueSnapshot(t, editedEvent, issueAfterEdit.Issue)
	if issueAfterEdit.Issue.UpdatedAt.Before(issueAfterCreate.Issue.UpdatedAt) {
		t.Fatalf("edited issue timestamp regressed: created-event=%s edited-event=%s",
			issueAfterCreate.Issue.UpdatedAt, issueAfterEdit.Issue.UpdatedAt)
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
	beforeComments, beforeEvents, beforeProjections = rowCount(t, pool, "comments"), rowCount(t, pool, "event_outbox"), rowCount(t, pool, "issue_spec_typed_comments")
	if _, _, err := failing.UpdateComment(t.Context(), "acme", "widgets", compatibilityID, subject,
		strings.Replace(editedRaw, "PROCESS-007", "PROCESS-ROLLBACK", 2)); err == nil {
		t.Fatal("failing update hook committed")
	}
	_, afterFailedUpdate, err := service.GetComment(t.Context(), "acme", "widgets", compatibilityID, subject)
	if err != nil {
		t.Fatal(err)
	}
	if afterFailedUpdate.Comment.Body != editedComment.Comment.Body ||
		afterFailedUpdate.Comment.RepresentationVersion != editedComment.Comment.RepresentationVersion ||
		rowCount(t, pool, "comments") != beforeComments || rowCount(t, pool, "event_outbox") != beforeEvents ||
		rowCount(t, pool, "issue_spec_typed_comments") != beforeProjections {
		t.Fatal("edited comment, projection, or outbox escaped hook rollback")
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

func assertCommentIssueSnapshot(t *testing.T, event outbox.Envelope, issue models.Issue) {
	t.Helper()
	if event.Issue.StableID != issue.ID || event.Issue.Number != issue.Number ||
		event.Issue.CreatedAt.IsZero() || event.Issue.UpdatedAt.IsZero() ||
		!event.Issue.CreatedAt.Equal(issue.CreatedAt) || !event.Issue.UpdatedAt.Equal(issue.UpdatedAt) {
		t.Fatalf("event issue = %+v authoritative issue = %+v", event.Issue, issue)
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	issueEnvelope, _ := decoded["issue"].(map[string]any)
	if _, exists := issueEnvelope["representation_version"]; exists {
		t.Fatalf("comment envelope invented issue representation version: %s", encoded)
	}
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
