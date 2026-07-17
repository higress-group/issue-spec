package emaildelivery

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	serverstore "github.com/higress-group/issue-spec/internal/server/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMigrationAndTransactionBoundFourKindEnqueue(t *testing.T) {
	pool := migratedEmailPool(t)
	fixture := insertEmailFixture(t, pool)

	var onboarding *time.Time
	var providerEmail string
	var notificationEmail *string
	if err := pool.QueryRow(t.Context(), `SELECT onboarding_completed_at, email, notification_email
		FROM users WHERE id = $1`, fixture.userID).Scan(&onboarding, &providerEmail, &notificationEmail); err != nil {
		t.Fatal(err)
	}
	if onboarding != nil || providerEmail != "identity@example.test" || notificationEmail != nil {
		t.Fatalf("post-migration user = onboarding:%v provider:%q notification:%v", onboarding, providerEmail, notificationEmail)
	}

	transaction, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	transactionStore, err := NewStore(transaction)
	if err != nil {
		t.Fatal(err)
	}
	rollbackInput := fixture.repoInput([]byte(`{"issue":"rollback"}`))
	rolledBack, inserted, err := transactionStore.Enqueue(t.Context(), rollbackInput)
	if err != nil || !inserted {
		t.Fatalf("transactional enqueue = %v/%v", inserted, err)
	}
	if err := transaction.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	queue, _ := NewStore(pool)
	if _, err := queue.Get(t.Context(), rolledBack.ID); !errors.Is(err, ErrNoWork) {
		t.Fatalf("rolled-back delivery remains: %v", err)
	}

	inputs := []EnqueueInput{
		fixture.verificationInput([]byte(`{"verification":"one"}`)),
		fixture.mentionInput([]byte(`{"mention":"one"}`)),
		fixture.repoInput([]byte(`{"issue":"one"}`)),
		fixture.milestoneInput([]byte(`{"milestone":"one"}`)),
	}
	for _, input := range inputs {
		delivery, inserted, err := queue.Enqueue(t.Context(), input)
		if err != nil || !inserted || delivery.Kind != input.Kind || delivery.ID == uuid.Nil || delivery.State != StatePending {
			t.Fatalf("enqueue %s = %+v/%v/%v", input.Kind, delivery, inserted, err)
		}
		again, inserted, err := queue.Enqueue(t.Context(), input)
		if err != nil || inserted || again.ID != delivery.ID {
			t.Fatalf("idempotent enqueue %s = %s/%v/%v", input.Kind, again.ID, inserted, err)
		}
		conflict := input
		conflict.Snapshot = []byte(`{"different":"snapshot"}`)
		if _, _, err := queue.Enqueue(t.Context(), conflict); !errors.Is(err, ErrConflict) {
			t.Fatalf("semantic conflict %s = %v", input.Kind, err)
		}
	}
	var kinds, states []string
	if err := pool.QueryRow(t.Context(), `SELECT array_agg(DISTINCT kind ORDER BY kind),
		array_agg(DISTINCT state ORDER BY state) FROM email_deliveries`).Scan(&kinds, &states); err != nil {
		t.Fatal(err)
	}
	if strings.Join(kinds, ",") != "change_milestone,mention,repo_issue_created,verification" || strings.Join(states, ",") != "pending" {
		t.Fatalf("stored kinds/states = %v/%v", kinds, states)
	}
}

func TestMigrationBackfillsOnlyOnboardingAndPreservesSubscriptions(t *testing.T) {
	pool := migratedEmailPool(t)
	userID, orgID, repoID := uuid.New(), uuid.New(), uuid.New()
	if _, err := pool.Exec(t.Context(), `INSERT INTO users (id, login, display_name, email)
		VALUES ($1,'legacy-person','Legacy Person','identity@example.test')`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO orgs (id, name, display_name)
		VALUES ($1,'legacy-org','Legacy Org')`, orgID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO repos (id, organization_id, name, display_name)
		VALUES ($1,$2,'legacy-repo','Legacy Repo')`, repoID, orgID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO repo_subscriptions
		(organization_id, repository_id, user_id, reason) VALUES ($1,$2,$3,'ignored')`, orgID, repoID, userID); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		"DROP TABLE email_deliveries",
		"DROP TABLE change_notification_milestones",
		"DROP TABLE comment_mentions",
		"DROP TABLE email_verification_requests",
		`ALTER TABLE users
			DROP COLUMN notification_email_verified_at,
			DROP COLUMN notification_email_key,
			DROP COLUMN notification_email,
			DROP COLUMN onboarding_completed_at`,
		"DELETE FROM schema_migrations WHERE version = 18",
	} {
		if _, err := pool.Exec(t.Context(), statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := serverstore.RunMigrations(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	var onboarding *time.Time
	var providerEmail string
	var notificationEmail *string
	var reason string
	if err := pool.QueryRow(t.Context(), `SELECT u.onboarding_completed_at, u.email, u.notification_email, s.reason
		FROM users u JOIN repo_subscriptions s ON s.user_id = u.id
		WHERE u.id = $1 AND s.organization_id = $2 AND s.repository_id = $3`, userID, orgID, repoID).
		Scan(&onboarding, &providerEmail, &notificationEmail, &reason); err != nil {
		t.Fatal(err)
	}
	if onboarding == nil || providerEmail != "identity@example.test" || notificationEmail != nil || reason != "ignored" {
		t.Fatalf("upgrade = onboarding:%v provider:%q notification:%v subscription:%q",
			onboarding, providerEmail, notificationEmail, reason)
	}
}

func TestVerificationSuccessClearsCiphertextAndSupersessionFencesClaim(t *testing.T) {
	pool := migratedEmailPool(t)
	fixture := insertEmailFixture(t, pool)
	queue, _ := NewStore(pool)
	delivery, _, err := queue.Enqueue(t.Context(), fixture.verificationInput([]byte(`{"verification":"one"}`)))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	claim, err := queue.ClaimOne(t.Context(), now, time.Minute)
	if err != nil || claim.ID != delivery.ID {
		t.Fatalf("claim = %+v/%v", claim, err)
	}
	stale := *claim
	stale.LeaseVersion--
	if err := queue.Succeed(t.Context(), &stale, now.Add(time.Second)); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale success = %v", err)
	}
	if err := queue.Succeed(t.Context(), claim, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	var ciphertext []byte
	var sentAt *time.Time
	if err := pool.QueryRow(t.Context(), `SELECT token_ciphertext, sent_at FROM email_verification_requests
		WHERE id = $1`, fixture.requestID).Scan(&ciphertext, &sentAt); err != nil {
		t.Fatal(err)
	}
	if ciphertext != nil || sentAt == nil {
		t.Fatalf("accepted verification retained ciphertext: %x/%v", ciphertext, sentAt)
	}

	if _, err := pool.Exec(t.Context(), `UPDATE email_verification_requests
		SET superseded_at = $2, updated_at = $2 WHERE id = $1`, fixture.requestID, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	nextRequest := uuid.New()
	if _, err := pool.Exec(t.Context(), `INSERT INTO email_verification_requests
		(id, user_id, pending_email, token_digest, token_ciphertext, expires_at)
		VALUES ($1,$2,'replacement@example.test',$3,$4,clock_timestamp() + interval '1 day')`,
		nextRequest, fixture.userID, bytes.Repeat([]byte{2}, 32), []byte("encrypted-replacement")); err != nil {
		t.Fatal(err)
	}
	fixture.requestID = nextRequest
	nextDelivery, _, err := queue.Enqueue(t.Context(), fixture.verificationInput([]byte(`{"verification":"two"}`)))
	if err != nil {
		t.Fatal(err)
	}
	nextClaim, err := queue.ClaimOne(t.Context(), now.Add(4*time.Second), time.Minute)
	if err != nil || nextClaim.ID != nextDelivery.ID {
		t.Fatalf("replacement claim = %+v/%v", nextClaim, err)
	}
	if err := queue.SuppressVerification(t.Context(), nextRequest, now.Add(5*time.Second), ReasonRecipientUnavailable); err != nil {
		t.Fatal(err)
	}
	if err := queue.Succeed(t.Context(), nextClaim, now.Add(6*time.Second)); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("superseded claim was not fenced: %v", err)
	}
	stored, err := queue.Get(t.Context(), nextDelivery.ID)
	if err != nil || stored.State != StateSuppressed || stored.LastReason == nil || *stored.LastReason != ReasonRecipientUnavailable {
		t.Fatalf("suppressed delivery = %+v/%v", stored, err)
	}
}

func TestLeaseRecoveryAndExpiredFifthAttemptConvergesToFailed(t *testing.T) {
	pool := migratedEmailPool(t)
	fixture := insertEmailFixture(t, pool)
	queue, _ := NewStore(pool)
	delivery, _, err := queue.Enqueue(t.Context(), fixture.mentionInput([]byte(`{"mention":"one"}`)))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	var prior *Claim
	for attempt := 1; attempt <= MaxAttempts; attempt++ {
		claim, err := queue.ClaimOne(t.Context(), now, time.Second)
		if err != nil || claim.ID != delivery.ID || claim.Attempts != attempt {
			t.Fatalf("attempt %d claim = %+v/%v", attempt, claim, err)
		}
		if prior != nil {
			if err := queue.Fail(t.Context(), prior, now, ReasonSMTPUnavailable); !errors.Is(err, ErrLeaseLost) {
				t.Fatalf("attempt %d stale lease = %v", attempt, err)
			}
		}
		prior = claim
		now = now.Add(2 * time.Second)
	}
	if _, err := queue.ClaimOne(t.Context(), now, time.Second); !errors.Is(err, ErrNoWork) {
		t.Fatalf("claim after fifth expired lease = %v", err)
	}
	stored, err := queue.Get(t.Context(), delivery.ID)
	if err != nil || stored.State != StateFailed || stored.Attempts != MaxAttempts ||
		stored.LastReason == nil || *stored.LastReason != ReasonSMTPAmbiguous {
		t.Fatalf("expired fifth attempt = %+v/%v", stored, err)
	}
}

type emailFixture struct {
	userID, orgID, repoID, issueID, commentID, requestID, milestoneID uuid.UUID
}

func insertEmailFixture(t *testing.T, pool *pgxpool.Pool) emailFixture {
	t.Helper()
	fixture := emailFixture{userID: uuid.New(), orgID: uuid.New(), repoID: uuid.New(), issueID: uuid.New(),
		commentID: uuid.New(), requestID: uuid.New(), milestoneID: uuid.New()}
	if _, err := pool.Exec(t.Context(), `INSERT INTO users (id, login, display_name, email)
		VALUES ($1,'person','Person','identity@example.test')`, fixture.userID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO orgs (id, name, display_name)
		VALUES ($1,'example','Example')`, fixture.orgID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO repos (id, organization_id, name, display_name)
		VALUES ($1,$2,'widgets','Widgets')`, fixture.repoID, fixture.orgID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO issues
		(id, organization_id, repository_id, number, author_id, title, body)
		VALUES ($1,$2,$3,1,$4,'Example issue','body')`, fixture.issueID, fixture.orgID, fixture.repoID, fixture.userID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO comments
		(id, organization_id, repository_id, issue_id, author_id, body)
		VALUES ($1,$2,$3,$4,$5,'mention body')`, fixture.commentID, fixture.orgID, fixture.repoID, fixture.issueID, fixture.userID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO email_verification_requests
		(id, user_id, pending_email, token_digest, token_ciphertext, expires_at)
		VALUES ($1,$2,'pending@example.test',$3,$4,clock_timestamp() + interval '1 day')`,
		fixture.requestID, fixture.userID, bytes.Repeat([]byte{1}, 32), []byte("encrypted-token")); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO change_notification_milestones
		(id, organization_id, repository_id, change_key, milestone, triggering_issue_id,
		 actor_user_id, occurred_at) VALUES ($1,$2,$3,'example-change','proposal',$4,$5,clock_timestamp() - interval '1 second')`,
		fixture.milestoneID, fixture.orgID, fixture.repoID, fixture.issueID, fixture.userID); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func (f emailFixture) verificationInput(snapshot []byte) EnqueueInput {
	return EnqueueInput{Kind: KindVerification, IdempotencyKey: f.requestID.String(), RecipientUserID: f.userID,
		VerificationRequestID: &f.requestID, Snapshot: snapshot}
}

func (f emailFixture) mentionInput(snapshot []byte) EnqueueInput {
	return EnqueueInput{Kind: KindMention, IdempotencyKey: f.commentID.String() + ":" + f.userID.String(),
		RecipientUserID: f.userID, OrganizationID: &f.orgID, RepositoryID: &f.repoID,
		CommentID: &f.commentID, Snapshot: snapshot}
}

func (f emailFixture) repoInput(snapshot []byte) EnqueueInput {
	return EnqueueInput{Kind: KindRepoIssueCreated, IdempotencyKey: f.issueID.String() + ":" + f.userID.String(),
		RecipientUserID: f.userID, OrganizationID: &f.orgID, RepositoryID: &f.repoID,
		IssueID: &f.issueID, Snapshot: snapshot}
}

func (f emailFixture) milestoneInput(snapshot []byte) EnqueueInput {
	return EnqueueInput{Kind: KindChangeMilestone, IdempotencyKey: f.milestoneID.String() + ":" + f.userID.String(),
		RecipientUserID: f.userID, OrganizationID: &f.orgID, RepositoryID: &f.repoID,
		MilestoneID: &f.milestoneID, Snapshot: snapshot}
}

func migratedEmailPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	databaseURL := strings.TrimSpace(os.Getenv("TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL")
	}
	admin, err := pgxpool.New(t.Context(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := "email_delivery_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(t.Context(), "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+quoted+" CASCADE") })
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	config.MaxConns = 16
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := serverstore.RunMigrations(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	return pool
}
