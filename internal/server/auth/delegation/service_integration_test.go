package delegation

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
	"github.com/higress-group/issue-spec/internal/server/auth/pat"
	"github.com/higress-group/issue-spec/internal/server/models"
	"github.com/higress-group/issue-spec/internal/server/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIssueAndRevokeJobLinearizeWithoutResurrectingLease(t *testing.T) {
	pool := delegationTestPool(t)
	secrets, err := serverauth.NewSecrets([]byte(strings.Repeat("p", 32)), []byte(strings.Repeat("e", 32)))
	if err != nil {
		t.Fatal(err)
	}
	userID, orgID, repoID := uuid.New(), uuid.New(), uuid.New()
	if _, err := pool.Exec(t.Context(), `INSERT INTO users (id, login, display_name) VALUES ($1, 'runner', 'Runner')`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO orgs (id, name, display_name) VALUES ($1, 'runner-org', 'Runner Org')`, orgID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO repos (id, organization_id, name, display_name) VALUES ($1, $2, 'repo', 'Repo')`, repoID, orgID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO repo_collaborators
		(id, organization_id, repository_id, user_id, role) VALUES ($1, $2, $3, $4, 'write')`,
		uuid.New(), orgID, repoID, userID); err != nil {
		t.Fatal(err)
	}
	patService := pat.New(pool, secrets)
	created, err := patService.Create(t.Context(), userID, pat.CreateInput{Name: "runner-parent",
		Scopes: []string{"runner:delegate", "issues:write"}, Repositories: []models.RepoScope{{OrgID: orgID, RepoID: repoID}}})
	if err != nil {
		t.Fatal(err)
	}
	principal, err := patService.AuthenticateBearer(t.Context(), created.Plaintext)
	if err != nil {
		t.Fatal(err)
	}
	repo := models.RepoScope{OrgID: orgID, RepoID: repoID}

	tests := []struct {
		name          string
		blockedOp     string
		firstIssue    bool
		wantIssueErr  error
		wantTombstone int
	}{
		{name: "issue commits before revoke", blockedOp: "issue", firstIssue: true, wantTombstone: 1},
		{name: "revoke commits before waiting issue", blockedOp: "revoke", firstIssue: false, wantIssueErr: serverauth.ErrRevokedCredential, wantTombstone: 1},
	}
	for index, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := New(pool, secrets)
			jobID := "job-linearize-" + string(rune('a'+index))
			locked := make(chan struct{})
			release := make(chan struct{})
			var once sync.Once
			service.afterJobLock = func(operation string) {
				if operation != tt.blockedOp {
					return
				}
				once.Do(func() { close(locked) })
				<-release
			}
			issue := func() error {
				_, err := service.Issue(context.Background(), IssueInput{Issuer: principal, Repo: repo, JobID: jobID,
					Purpose: "issue-api", Audience: "instance-a", Subject: "runner-child",
					Scopes: []string{"issues:write"}, TTL: time.Minute})
				return err
			}
			revoke := func() error { return service.RevokeJob(context.Background(), repo, jobID) }

			firstResult := make(chan error, 1)
			secondResult := make(chan error, 1)
			if tt.firstIssue {
				go func() { firstResult <- issue() }()
			} else {
				go func() { firstResult <- revoke() }()
			}
			select {
			case <-locked:
			case <-time.After(3 * time.Second):
				t.Fatal("first operation did not acquire the job lock")
			}
			if tt.firstIssue {
				go func() { secondResult <- revoke() }()
			} else {
				go func() { secondResult <- issue() }()
			}
			waitForAdvisoryWaiter(t, pool)
			close(release)
			firstErr, secondErr := <-firstResult, <-secondResult
			var issueErr, revokeErr error
			if tt.firstIssue {
				issueErr, revokeErr = firstErr, secondErr
			} else {
				revokeErr, issueErr = firstErr, secondErr
			}
			if !errors.Is(issueErr, tt.wantIssueErr) || revokeErr != nil {
				t.Fatalf("issue error=%v want=%v; revoke error=%v", issueErr, tt.wantIssueErr, revokeErr)
			}
			var active, tombstones int
			if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM delegated_tokens
				WHERE organization_id = $1 AND repository_id = $2 AND job_id = $3
				AND revoked_at IS NULL AND expires_at > clock_timestamp()`, orgID, repoID, jobID).Scan(&active); err != nil {
				t.Fatal(err)
			}
			if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM delegated_job_revocations
				WHERE organization_id = $1 AND repository_id = $2 AND job_id = $3`, orgID, repoID, jobID).Scan(&tombstones); err != nil {
				t.Fatal(err)
			}
			if active != 0 || tombstones != tt.wantTombstone {
				t.Fatalf("active tokens=%d tombstones=%d", active, tombstones)
			}
		})
	}
}

func TestRevokeJobCleanupRetryIsIdempotentAndDurable(t *testing.T) {
	pool := delegationTestPool(t)
	secrets, err := serverauth.NewSecrets([]byte(strings.Repeat("p", 32)), []byte(strings.Repeat("e", 32)))
	if err != nil {
		t.Fatal(err)
	}
	userID, orgID, repoID := uuid.New(), uuid.New(), uuid.New()
	if _, err := pool.Exec(t.Context(), `INSERT INTO users (id, login, display_name) VALUES ($1, 'cleanup-runner', 'Cleanup Runner')`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO orgs (id, name, display_name) VALUES ($1, 'cleanup-org', 'Cleanup Org')`, orgID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO repos (id, organization_id, name, display_name) VALUES ($1, $2, 'cleanup-repo', 'Cleanup Repo')`, repoID, orgID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO repo_collaborators
		(id, organization_id, repository_id, user_id, role) VALUES ($1, $2, $3, $4, 'write')`,
		uuid.New(), orgID, repoID, userID); err != nil {
		t.Fatal(err)
	}
	patService := pat.New(pool, secrets)
	parent, err := patService.Create(t.Context(), userID, pat.CreateInput{Name: "cleanup-parent",
		Scopes: []string{"runner:delegate", "issues:write"}, Repositories: []models.RepoScope{{OrgID: orgID, RepoID: repoID}}})
	if err != nil {
		t.Fatal(err)
	}
	principal, err := patService.AuthenticateBearer(t.Context(), parent.Plaintext)
	if err != nil {
		t.Fatal(err)
	}
	repo := models.RepoScope{OrgID: orgID, RepoID: repoID}
	service := New(pool, secrets)
	created, err := service.Issue(t.Context(), IssueInput{Issuer: principal, Repo: repo, JobID: "job-cleanup-retry",
		Purpose: "issue-api", Audience: "instance-a", Subject: "runner-child", Scopes: []string{"issues:write"}, TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.RevokeJob(t.Context(), repo, "job-cleanup-retry"); err != nil {
		t.Fatal(err)
	}
	if err := service.RevokeJob(t.Context(), repo, "job-cleanup-retry"); err != nil {
		t.Fatalf("idempotent cleanup retry: %v", err)
	}
	if _, err := service.Authenticate(t.Context(), created.Plaintext, Expected{Repo: repo, JobID: "job-cleanup-retry",
		Purpose: "issue-api", Audience: "instance-a"}); !errors.Is(err, serverauth.ErrRevokedCredential) {
		t.Fatalf("revoked delegated token authenticate error=%v", err)
	}
	var active, tombstones int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM delegated_tokens
		WHERE organization_id = $1 AND repository_id = $2 AND job_id = $3
		AND revoked_at IS NULL AND expires_at > clock_timestamp()`, orgID, repoID, "job-cleanup-retry").Scan(&active); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM delegated_job_revocations
		WHERE organization_id = $1 AND repository_id = $2 AND job_id = $3`, orgID, repoID, "job-cleanup-retry").Scan(&tombstones); err != nil {
		t.Fatal(err)
	}
	if active != 0 || tombstones != 1 {
		t.Fatalf("active delegated tokens=%d tombstones=%d", active, tombstones)
	}
}

func waitForAdvisoryWaiter(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var waiting int
		if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock' AND wait_event = 'advisory'
			AND query LIKE '%pg_advisory_xact_lock%'`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("second operation did not wait on the job advisory lock")
}

func delegationTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	databaseURL := strings.TrimSpace(os.Getenv("TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	adminConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	adminPool, err := pgxpool.NewWithConfig(t.Context(), adminConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(adminPool.Close)
	schema := "delegation_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := adminPool.Exec(t.Context(), "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = adminPool.Exec(ctx, "DROP SCHEMA IF EXISTS "+quoted+" CASCADE")
	})
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
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
