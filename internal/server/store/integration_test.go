package store

import (
	"context"
	"errors"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/server/models"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const testDatabaseEnv = "TEST_DATABASE_URL"

func TestRunMigrationsConcurrentAndIdempotent(t *testing.T) {
	pool := newIntegrationPool(t)

	// Hold the same session-level lock on a dedicated connection. Both
	// migration calls must wait, demonstrating that the lock is acquired and
	// retained on one connection for the whole run.
	lockConn, err := pool.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lockConn.Exec(t.Context(), `SELECT pg_advisory_lock($1)`, migrationAdvisoryLock); err != nil {
		lockConn.Release()
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	results := make(chan error, 2)
	for range 2 {
		go func() { results <- RunMigrations(ctx, pool) }()
	}
	select {
	case err := <-results:
		t.Fatalf("RunMigrations returned before advisory lock release: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	var unlocked bool
	if err := lockConn.QueryRow(t.Context(), `SELECT pg_advisory_unlock($1)`, migrationAdvisoryLock).Scan(&unlocked); err != nil {
		lockConn.Release()
		t.Fatal(err)
	}
	lockConn.Release()
	if !unlocked {
		t.Fatal("test advisory lock was not held")
	}
	for range 2 {
		if err := <-results; err != nil {
			t.Fatalf("concurrent RunMigrations() error = %v", err)
		}
	}
	if err := RunMigrations(t.Context(), pool); err != nil {
		t.Fatalf("repeated RunMigrations() error = %v", err)
	}

	var count int
	var version int64
	var name string
	if err := pool.QueryRow(t.Context(), `SELECT count(*), max(version), max(name) FROM schema_migrations`).Scan(&count, &version, &name); err != nil {
		t.Fatal(err)
	}
	// Keep the forward-only migration-set assertion synchronized with the
	// latest embedded file; PROCESS-005 adds reaction compatibility data.
	if count != int(LatestSchemaVersion) || version != LatestSchemaVersion || name != "0006_webhook_outbox.sql" {
		t.Fatalf("migration metadata = count %d, version %d, name %q", count, version, name)
	}
}

func TestSchemaScopedMigrationsKeepPgcryptoDurableAcrossSchemaDrop(t *testing.T) {
	first := newIntegrationPool(t)
	second := newIntegrationPool(t)
	results := make(chan error, 2)
	go func() { results <- RunMigrations(t.Context(), first) }()
	go func() { results <- RunMigrations(t.Context(), second) }()
	for range 2 {
		if err := <-results; err != nil {
			t.Fatalf("parallel schema migration: %v", err)
		}
	}

	var firstSchema string
	if err := first.QueryRow(t.Context(), `SELECT current_schema()`).Scan(&firstSchema); err != nil {
		t.Fatal(err)
	}
	first.Close()
	admin, err := pgxpool.New(t.Context(), strings.TrimSpace(os.Getenv(testDatabaseEnv)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	if _, err := admin.Exec(t.Context(), "DROP SCHEMA "+pgx.Identifier{firstSchema}.Sanitize()+" CASCADE"); err != nil {
		t.Fatal(err)
	}

	orgID := insertOrg(t, second, "durable-extension-org")
	repoID := insertRepo(t, second, orgID, "durable-extension-repo")
	userID := uuid.New()
	if _, err := second.Exec(t.Context(), `INSERT INTO users (id, login, display_name)
		VALUES ($1, 'durable-extension-user', 'durable-extension-user')`, userID); err != nil {
		t.Fatal(err)
	}
	issue, err := New(second).Repo(orgID, repoID).CreateIssue(t.Context(), models.NewIssue{
		ID: uuid.New(), AuthorID: &userID, Title: "durable extension", Body: "raw",
	})
	if err != nil {
		t.Fatal(err)
	}
	commentID := uuid.New()
	var compatibilityID int64
	if err := second.QueryRow(t.Context(), `INSERT INTO comments
		(id, organization_id, repository_id, issue_id, author_id, body)
		VALUES ($1, $2, $3, $4, $5, '') RETURNING compatibility_id`,
		commentID, orgID, repoID, issue.ID, userID).Scan(&compatibilityID); err != nil {
		t.Fatalf("stable id after another schema drop: %v", err)
	}
	if compatibilityID <= 0 {
		t.Fatalf("compatibility id = %d", compatibilityID)
	}
	var extensionSchema string
	if err := second.QueryRow(t.Context(), `SELECT n.nspname FROM pg_extension e
		JOIN pg_namespace n ON n.oid = e.extnamespace WHERE e.extname = 'pgcrypto'`).Scan(&extensionSchema); err != nil {
		t.Fatal(err)
	}
	if extensionSchema != "issue_spec_extensions" {
		t.Fatalf("pgcrypto schema = %q", extensionSchema)
	}
	if err := RunMigrations(t.Context(), second); err != nil {
		t.Fatalf("repeat migration with durable extension: %v", err)
	}
}

func TestIncrementCollectionVersionsMapsEveryRepositoryCollection(t *testing.T) {
	pool := migratedIntegrationPool(t)
	orgID := insertOrg(t, pool, "collection-version-org")
	repoID := insertRepo(t, pool, orgID, "repo")
	collections := []RepoCollection{
		RepoCollectionIssues, RepoCollectionComments, RepoCollectionLabels,
		RepoCollectionArtifacts, RepoCollectionWebhooks, RepoCollectionReactions,
		RepoCollectionBindings, RepoCollectionReferences, RepoCollectionEvidence,
		RepoCollectionCollaborators, RepoCollectionSubscriptions,
	}
	versions, err := New(pool).Repo(orgID, repoID).IncrementCollectionVersions(t.Context(), collections...)
	if err != nil {
		t.Fatal(err)
	}
	for index, version := range versions {
		if version != 2 {
			t.Fatalf("collection %s version = %d", collections[index], version)
		}
	}
	columns := []string{
		"issues_collection_version", "comments_collection_version", "labels_collection_version",
		"artifacts_collection_version", "webhooks_collection_version", "reactions_collection_version",
		"bindings_collection_version", "references_collection_version", "evidence_collection_version",
		"collaborators_collection_version", "subscriptions_collection_version",
	}
	destinations := make([]any, len(columns))
	stored := make([]int64, len(columns))
	for index := range stored {
		destinations[index] = &stored[index]
	}
	if err := pool.QueryRow(t.Context(), `SELECT `+strings.Join(columns, ", ")+` FROM repos WHERE id = $1`, repoID).
		Scan(destinations...); err != nil {
		t.Fatal(err)
	}
	for index, version := range stored {
		if version != 2 {
			t.Fatalf("column %s version = %d", columns[index], version)
		}
	}
	if _, err := New(pool).Repo(orgID, repoID).IncrementCollectionVersions(t.Context(),
		RepoCollectionIssues, RepoCollectionIssues); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("duplicate collection error = %v", err)
	}
}

func TestConcurrentIssueNumberAllocationIsPerRepository(t *testing.T) {
	pool := migratedIntegrationPool(t)
	orgID := insertOrg(t, pool, "allocation-org")
	repoA := insertRepo(t, pool, orgID, "repo-a")
	repoB := insertRepo(t, pool, orgID, "repo-b")
	store := New(pool)

	type result struct {
		repo   uuid.UUID
		number int64
		err    error
	}
	const allocationsPerRepo = 24
	results := make(chan result, allocationsPerRepo*2)
	for _, repoID := range []uuid.UUID{repoA, repoB} {
		for range allocationsPerRepo {
			go func(repoID uuid.UUID) {
				number, err := store.Repo(orgID, repoID).AllocateIssueNumber(t.Context())
				results <- result{repo: repoID, number: number, err: err}
			}(repoID)
		}
	}

	allocated := map[uuid.UUID][]int64{repoA: {}, repoB: {}}
	for range allocationsPerRepo * 2 {
		result := <-results
		if result.err != nil {
			t.Fatalf("AllocateIssueNumber() error = %v", result.err)
		}
		allocated[result.repo] = append(allocated[result.repo], result.number)
	}
	for _, repoID := range []uuid.UUID{repoA, repoB} {
		numbers := allocated[repoID]
		sort.Slice(numbers, func(i, j int) bool { return numbers[i] < numbers[j] })
		if len(numbers) != allocationsPerRepo {
			t.Fatalf("repo %s received %d allocations", repoID, len(numbers))
		}
		for i, number := range numbers {
			if want := int64(i + 1); number != want {
				t.Fatalf("repo %s allocation[%d] = %d, want %d", repoID, i, number, want)
			}
		}
		var next int64
		if err := pool.QueryRow(t.Context(), `SELECT next_issue_number FROM repos WHERE organization_id = $1 AND id = $2`, orgID, repoID).Scan(&next); err != nil {
			t.Fatal(err)
		}
		if next != allocationsPerRepo+1 {
			t.Fatalf("repo %s next_issue_number = %d", repoID, next)
		}
	}
}

func TestCaseInsensitiveNamesAreUniqueInTheirScope(t *testing.T) {
	pool := migratedIntegrationPool(t)
	orgA := insertOrg(t, pool, "Acme")
	repoA := insertRepo(t, pool, orgA, "Main")

	_, err := pool.Exec(t.Context(), `INSERT INTO orgs (id, name, display_name) VALUES ($1, $2, $3)`, uuid.New(), "aCME", "duplicate")
	requirePGCode(t, err, "23505")
	_, err = pool.Exec(t.Context(), `INSERT INTO repos (id, organization_id, name, display_name) VALUES ($1, $2, $3, $4)`, uuid.New(), orgA, "mAIN", "duplicate")
	requirePGCode(t, err, "23505")

	labelA := uuid.New()
	if _, err := pool.Exec(t.Context(), `INSERT INTO labels (id, organization_id, repository_id, name, color) VALUES ($1, $2, $3, $4, $5)`, labelA, orgA, repoA, "Bug", "d73a4a"); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(t.Context(), `INSERT INTO labels (id, organization_id, repository_id, name, color) VALUES ($1, $2, $3, $4, $5)`, uuid.New(), orgA, repoA, "bUG", "ffffff")
	requirePGCode(t, err, "23505")

	orgB := insertOrg(t, pool, "Other")
	repoB := insertRepo(t, pool, orgB, "main")
	if _, err := pool.Exec(t.Context(), `INSERT INTO labels (id, organization_id, repository_id, name, color) VALUES ($1, $2, $3, $4, $5)`, uuid.New(), orgB, repoB, "bug", "ffffff"); err != nil {
		t.Fatalf("same repo/label names in another org should be allowed: %v", err)
	}
}

func TestIdentitySessionBindingAndWebhookFoundationContracts(t *testing.T) {
	pool := migratedIntegrationPool(t)
	orgID := insertOrg(t, pool, "contract-org")
	repoID := insertRepo(t, pool, orgID, "repo")

	userA := uuid.New()
	userB := uuid.New()
	for _, user := range []struct {
		id    uuid.UUID
		login string
	}{
		{userA, "first-user"},
		{userB, "second-user"},
	} {
		if _, err := pool.Exec(t.Context(), `INSERT INTO users (id, login, display_name, email)
			VALUES ($1, $2, $2, 'shared@example.test')`, user.id, user.login); err != nil {
			t.Fatalf("same email must not auto-merge identities: %v", err)
		}
	}

	now := time.Now().UTC()
	if _, err := pool.Exec(t.Context(), `INSERT INTO sessions (
		id, user_id, token_prefix, token_hash, csrf_hash, idle_expires_at, absolute_expires_at
	) VALUES ($1, $2, $3, $4, $5, $6, $7)`, uuid.New(), userA, "sess_1234",
		[]byte("token-hash"), []byte("csrf-hash"), now.Add(time.Hour), now.Add(24*time.Hour)); err != nil {
		t.Fatalf("insert session contract: %v", err)
	}
	_, err := pool.Exec(t.Context(), `INSERT INTO sessions (
		id, user_id, token_prefix, token_hash, csrf_hash, idle_expires_at, absolute_expires_at
	) VALUES ($1, $2, $3, $4, $5, $6, $7)`, uuid.New(), userA, "sess_bad",
		[]byte("bad-token-hash"), []byte("bad-csrf-hash"), now.Add(24*time.Hour), now.Add(time.Hour))
	requirePGCode(t, err, "23514")

	if _, err := pool.Exec(t.Context(), `INSERT INTO delegated_tokens (
		id, user_id, organization_id, repository_id, job_id, purpose, token_hash,
		audience, subject, expires_at
	) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`, uuid.New(), userA,
		orgID, repoID, "job-123", "comment-writeback", []byte("delegated-hash"),
		"issue-spec-server", "runner-child", now.Add(time.Hour)); err != nil {
		t.Fatalf("insert delegated token contract: %v", err)
	}

	if _, err := pool.Exec(t.Context(), `INSERT INTO source_bindings (
		id, organization_id, repository_id, provider_key, external_repository_id,
		clone_url, web_url, default_branch, version, created_by_user_id, updated_by_user_id
	) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 1, $9, $9)`, uuid.New(), orgID,
		repoID, "git", "external-42", "https://code.example.test/acme/repo.git",
		"https://code.example.test/acme/repo", "main", userA); err != nil {
		t.Fatalf("insert source binding contract: %v", err)
	}
	_, err = pool.Exec(t.Context(), `INSERT INTO source_bindings (
		id, organization_id, repository_id, provider_key, external_repository_id,
		clone_url, web_url, default_branch, version
	) VALUES ($1, $2, $3, 'git', 'external-43', 'https://code.example.test/other.git',
		'https://code.example.test/other', 'main', 2)`, uuid.New(), orgID, repoID)
	requirePGCode(t, err, "23505")
	if _, err := pool.Exec(t.Context(), `INSERT INTO source_bindings (
		id, organization_id, repository_id, provider_key, external_repository_id,
		clone_url, web_url, default_branch, version, active
	) VALUES ($1, $2, $3, 'git', 'external-43', 'https://code.example.test/other.git',
		'https://code.example.test/other', 'main', 2, false)`, uuid.New(), orgID, repoID); err != nil {
		t.Fatalf("insert inactive historical binding: %v", err)
	}

	if _, err := pool.Exec(t.Context(), `INSERT INTO webhook_subscriptions (
		id, organization_id, scope_type, url, event_types
	) VALUES ($1, $2, 'organization', 'https://runner.example.test/org', ARRAY['comment.created'])`,
		uuid.New(), orgID); err != nil {
		t.Fatalf("insert organization-scoped webhook: %v", err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO webhook_subscriptions (
		id, organization_id, repository_id, scope_type, url, event_types
	) VALUES ($1, $2, $3, 'repository', 'https://runner.example.test/repo', ARRAY['comment.created'])`,
		uuid.New(), orgID, repoID); err != nil {
		t.Fatalf("insert repository-scoped webhook: %v", err)
	}
	_, err = pool.Exec(t.Context(), `INSERT INTO webhook_subscriptions (
		id, organization_id, repository_id, scope_type, url, event_types
	) VALUES ($1, $2, $3, 'organization', 'https://runner.example.test/bad', ARRAY['comment.created'])`,
		uuid.New(), orgID, repoID)
	requirePGCode(t, err, "23514")

	var primaryKey string
	if err := pool.QueryRow(t.Context(), `SELECT pg_get_constraintdef(oid)
		FROM pg_constraint WHERE conrelid = 'issue_labels'::regclass AND contype = 'p'`).Scan(&primaryKey); err != nil {
		t.Fatal(err)
	}
	if primaryKey != "PRIMARY KEY (issue_id, label_id)" {
		t.Fatalf("issue_labels primary key = %q", primaryKey)
	}
}

func TestCompositeForeignKeysRejectCrossRepositoryTargets(t *testing.T) {
	pool := migratedIntegrationPool(t)
	orgID := insertOrg(t, pool, "fk-org")
	repoA := insertRepo(t, pool, orgID, "repo-a")
	repoB := insertRepo(t, pool, orgID, "repo-b")
	store := New(pool)
	issueA := createIssue(t, store.Repo(orgID, repoA), "issue a")
	issueB := createIssue(t, store.Repo(orgID, repoB), "issue b")

	commentA := uuid.New()
	if _, err := pool.Exec(t.Context(), `INSERT INTO comments (id, organization_id, repository_id, issue_id, body) VALUES ($1, $2, $3, $4, $5)`, commentA, orgID, repoA, issueA.ID, "comment"); err != nil {
		t.Fatal(err)
	}
	_, err := pool.Exec(t.Context(), `INSERT INTO comments (id, organization_id, repository_id, issue_id, body) VALUES ($1, $2, $3, $4, $5)`, uuid.New(), orgID, repoB, issueA.ID, "cross repo")
	requirePGCode(t, err, "23503")

	labelB := uuid.New()
	if _, err := pool.Exec(t.Context(), `INSERT INTO labels (id, organization_id, repository_id, name, color) VALUES ($1, $2, $3, $4, $5)`, labelB, orgID, repoB, "repo-b-label", "123abc"); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(t.Context(), `INSERT INTO issue_labels (organization_id, repository_id, issue_id, label_id) VALUES ($1, $2, $3, $4)`, orgID, repoA, issueA.ID, labelB)
	requirePGCode(t, err, "23503")

	_, err = pool.Exec(t.Context(), `INSERT INTO comment_reactions (
		organization_id, repository_id, issue_id, comment_id, identity_key, reaction_key
	) VALUES ($1, $2, $3, $4, $5, $6)`, orgID, repoB, issueB.ID, commentA, "user:one", "+1")
	requirePGCode(t, err, "23503")
}

func TestExternalEvidenceIsAppendOnlyAndIdempotent(t *testing.T) {
	pool := migratedIntegrationPool(t)
	orgID := insertOrg(t, pool, "evidence-org")
	repoID := insertRepo(t, pool, orgID, "repo")
	repo := New(pool).Repo(orgID, repoID)
	issue := createIssue(t, repo, "evidence issue")

	input := models.NewExternalEvidence{
		IssueID:           issue.ID,
		ProviderKey:       "github",
		EvidenceType:      "check-run",
		ExternalID:        "1234",
		IngestKey:         "github:check-run:1234:v1",
		NormalizedState:   "succeeded",
		SubjectRevision:   "abc",
		ObservedAt:        time.Now().UTC(),
		Payload:           []byte(`{"sha":"abc","conclusion":"success"}`),
		Provenance:        []byte(`{"adapter":"test"}`),
		WriterIdentityKey: "service:test-adapter",
	}
	first, err := repo.AppendExternalEvidence(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	input.Payload = []byte(`{"conclusion":"success","sha":"abc"}`)
	second, err := repo.AppendExternalEvidence(t.Context(), input)
	if err != nil {
		t.Fatalf("semantic retry error = %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("idempotent retry created %s, want existing %s", second.ID, first.ID)
	}
	input.Payload = []byte(`{"sha":"def","conclusion":"success"}`)
	if _, err := repo.AppendExternalEvidence(t.Context(), input); !errors.Is(err, ErrIdempotencyMismatch) {
		t.Fatalf("mismatched retry error = %v, want ErrIdempotencyMismatch", err)
	}

	_, err = pool.Exec(t.Context(), `UPDATE external_evidence SET payload = '{}'::jsonb WHERE id = $1`, first.ID)
	requirePGCode(t, err, "55000")
	_, err = pool.Exec(t.Context(), `DELETE FROM issues WHERE organization_id = $1 AND repository_id = $2 AND id = $3`, orgID, repoID, issue.ID)
	requirePGCode(t, err, "23503")
}

func TestOutboxSemanticIdempotencyAndActiveArtifactUniqueness(t *testing.T) {
	pool := migratedIntegrationPool(t)
	orgID := insertOrg(t, pool, "outbox-org")
	repoID := insertRepo(t, pool, orgID, "repo")
	repo := New(pool).Repo(orgID, repoID)
	aggregateID := uuid.New()
	input := models.NewOutboxEvent{
		AggregateType: "issue",
		AggregateID:   aggregateID,
		EventType:     "issue.updated",
		EventKey:      "issue:" + aggregateID.String() + ":representation:2",
		Payload:       []byte(`{"number":1,"version":2}`),
	}
	first, err := repo.EnqueueEvent(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	input.Payload = []byte(`{"version":2,"number":1}`)
	second, err := repo.EnqueueEvent(t.Context(), input)
	if err != nil {
		t.Fatalf("semantic retry error = %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("idempotent retry created %s, want existing %s", second.ID, first.ID)
	}
	input.EventType = "issue.closed"
	if _, err := repo.EnqueueEvent(t.Context(), input); !errors.Is(err, ErrIdempotencyMismatch) {
		t.Fatalf("mismatched retry error = %v, want ErrIdempotencyMismatch", err)
	}

	artifactID := uuid.New()
	if _, err := pool.Exec(t.Context(), `INSERT INTO issue_spec_artifacts (
		id, organization_id, repository_id, change_key, artifact_type, content
	) VALUES ($1, $2, $3, $4, $5, $6)`, artifactID, orgID, repoID, "add-runner", "proposal", "one"); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(t.Context(), `INSERT INTO issue_spec_artifacts (
		id, organization_id, repository_id, change_key, artifact_type, content
	) VALUES ($1, $2, $3, $4, $5, $6)`, uuid.New(), orgID, repoID, "add-runner", "proposal", "two")
	requirePGCode(t, err, "23505")
	if _, err := pool.Exec(t.Context(), `INSERT INTO issue_spec_artifacts (
		id, organization_id, repository_id, change_key, artifact_type, content, active
	) VALUES ($1, $2, $3, $4, $5, $6, false)`, uuid.New(), orgID, repoID, "add-runner", "proposal", "history"); err != nil {
		t.Fatalf("inactive artifact should not conflict: %v", err)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE issue_spec_artifacts SET active = false WHERE id = $1`, artifactID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO issue_spec_artifacts (
		id, organization_id, repository_id, change_key, artifact_type, content
	) VALUES ($1, $2, $3, $4, $5, $6)`, uuid.New(), orgID, repoID, "add-runner", "proposal", "replacement"); err != nil {
		t.Fatalf("replacement active artifact should be allowed: %v", err)
	}
}

func TestVersionCompareAndSwapPrimitives(t *testing.T) {
	pool := migratedIntegrationPool(t)
	orgID := insertOrg(t, pool, "cas-org")
	repoID := insertRepo(t, pool, orgID, "repo")
	repo := New(pool).Repo(orgID, repoID)
	issue := createIssue(t, repo, "before")

	updated, err := repo.UpdateIssueCAS(t.Context(), issue.Number, 1, models.IssueUpdate{
		Title: "after",
		Body:  "closed by test",
		State: models.IssueStateClosed,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.RepresentationVersion != 2 || updated.ClosedAt == nil {
		t.Fatalf("updated issue version/closed_at = %d/%v", updated.RepresentationVersion, updated.ClosedAt)
	}
	if _, err := repo.UpdateIssueCAS(t.Context(), issue.Number, 1, models.IssueUpdate{Title: "stale", State: models.IssueStateOpen}); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale issue CAS error = %v", err)
	}

	version, err := repo.BumpCollectionVersion(t.Context(), RepoCollectionIssues, 1)
	if err != nil {
		t.Fatal(err)
	}
	if version != 2 {
		t.Fatalf("issues collection version = %d, want 2", version)
	}
	if _, err := repo.BumpCollectionVersion(t.Context(), RepoCollectionIssues, 1); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale collection CAS error = %v", err)
	}
}

func migratedIntegrationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := newIntegrationPool(t)
	if err := RunMigrations(t.Context(), pool); err != nil {
		t.Fatalf("RunMigrations() error = %v", err)
	}
	return pool
}

func newIntegrationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	databaseURL := strings.TrimSpace(os.Getenv(testDatabaseEnv))
	if databaseURL == "" {
		t.Skipf("set %s to run PostgreSQL integration tests", testDatabaseEnv)
	}

	adminConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatalf("parse %s: %v", testDatabaseEnv, err)
	}
	adminPool, err := pgxpool.NewWithConfig(t.Context(), adminConfig)
	if err != nil {
		t.Fatalf("open integration database: %v", err)
	}
	t.Cleanup(adminPool.Close)
	if err := adminPool.Ping(t.Context()); err != nil {
		t.Fatalf("ping integration database: %v", err)
	}

	schema := "issue_spec_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := adminPool.Exec(t.Context(), "CREATE SCHEMA "+quotedSchema); err != nil {
		t.Fatalf("create test schema: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := adminPool.Exec(ctx, "DROP SCHEMA IF EXISTS "+quotedSchema+" CASCADE"); err != nil {
			t.Errorf("drop test schema: %v", err)
		}
	})

	testConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	testConfig.ConnConfig.RuntimeParams["search_path"] = schema
	testConfig.MaxConns = 32
	pool, err := pgxpool.NewWithConfig(t.Context(), testConfig)
	if err != nil {
		t.Fatalf("open schema-scoped pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func insertOrg(t *testing.T, pool *pgxpool.Pool, name string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := pool.Exec(t.Context(), `INSERT INTO orgs (id, name, display_name) VALUES ($1, $2, $3)`, id, name, name); err != nil {
		t.Fatalf("insert org %q: %v", name, err)
	}
	return id
}

func insertRepo(t *testing.T, pool *pgxpool.Pool, orgID uuid.UUID, name string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := pool.Exec(t.Context(), `INSERT INTO repos (id, organization_id, name, display_name) VALUES ($1, $2, $3, $4)`, id, orgID, name, name); err != nil {
		t.Fatalf("insert repo %q: %v", name, err)
	}
	return id
}

func createIssue(t *testing.T, repo RepoStore, title string) models.Issue {
	t.Helper()
	issue, err := repo.CreateIssue(t.Context(), models.NewIssue{Title: title})
	if err != nil {
		t.Fatalf("CreateIssue(%q): %v", title, err)
	}
	return issue
}

func requirePGCode(t *testing.T, err error, code string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected PostgreSQL error %s", code)
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("error = %T %v, want PostgreSQL error %s", err, err, code)
	}
	if pgErr.Code != code {
		t.Fatalf("PostgreSQL code = %s (%s), want %s", pgErr.Code, pgErr.Message, code)
	}
}
