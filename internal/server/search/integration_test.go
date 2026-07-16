package search

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/server/authz"
	"github.com/higress-group/issue-spec/internal/server/models"
	"github.com/higress-group/issue-spec/internal/server/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresSearchRecallAuthorizationAndIndexes(t *testing.T) {
	databaseURL := strings.TrimSpace(os.Getenv("TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL for PostgreSQL search integration test")
	}
	adminPool, err := pgxpool.New(t.Context(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := "search_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := adminPool.Exec(t.Context(), "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = adminPool.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+quoted+" CASCADE")
		adminPool.Close()
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
	defer pool.Close()
	if err := store.RunMigrations(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `CREATE INDEX issue_spec_search_issues_bigm_v1 ON issues (id)`); err != nil {
		t.Fatal(err)
	}
	if err := Prepare(t.Context(), pool); err == nil || !strings.Contains(err.Error(), "unexpected definition") {
		t.Fatalf("same-name wrong search index error = %v", err)
	}
	if _, err := pool.Exec(t.Context(), `DROP INDEX issue_spec_search_issues_bigm_v1`); err != nil {
		t.Fatal(err)
	}
	if err := Prepare(t.Context(), pool); err != nil {
		t.Fatal(err)
	}

	orgID, publicRepoID, privateRepoID := uuid.New(), uuid.New(), uuid.New()
	publicIssueID, privateIssueID := uuid.New(), uuid.New()
	titleIssueID, bodyIssueID, commentIssueID := uuid.New(), uuid.New(), uuid.New()
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO orgs (id, name, display_name) VALUES ($1, 'search-acme', 'Search Acme')`, []any{orgID}},
		{`INSERT INTO repos (id, organization_id, name, display_name, visibility) VALUES
			($1, $3, 'public-repo', 'Public Repo', 'public'), ($2, $3, 'private-repo', 'Private Repo', 'private')`, []any{publicRepoID, privateRepoID, orgID}},
		{`INSERT INTO issues (id, organization_id, repository_id, number, title, body) VALUES
			($1, $3, $4, 1, 'ListConfigsBySource 刷新慢', '调查消费者鉴权路径'),
			($2, $3, $5, 1, 'private-only-secret', 'must never leak')`, []any{publicIssueID, privateIssueID, orgID, publicRepoID, privateRepoID}},
		{`INSERT INTO issues (id, organization_id, repository_id, number, title, body) VALUES
			($1, $4, $5, 17, '鉴权', 'title ranking fixture'),
			($2, $4, $5, 18, 'mentions 17', '鉴权 body ranking fixture'),
			($3, $4, $5, 19, 'comment ranking fixture', 'no issue match')`, []any{titleIssueID, bodyIssueID, commentIssueID, orgID, publicRepoID}},
		{`INSERT INTO comments (organization_id, repository_id, issue_id, body) VALUES ($1, $2, $3, '消费者鉴权锁争用与刷新慢路径')`, []any{orgID, publicRepoID, publicIssueID}},
		{`INSERT INTO comments (organization_id, repository_id, issue_id, body) VALUES ($1, $2, $3, '鉴权 auth-lock comment ranking fixture')`, []any{orgID, publicRepoID, commentIssueID}},
		{`INSERT INTO issue_spec_artifacts (organization_id, repository_id, issue_id, change_key, artifact_type, content)
			VALUES ($1, $2, $3, 'auth-lock', 'proposal', 'proposal')`, []any{orgID, publicRepoID, publicIssueID}},
	}
	for _, statement := range statements {
		if _, err := pool.Exec(t.Context(), statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	authorization, err := authz.New(pool)
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(pool, authorization)
	if err != nil {
		t.Fatal(err)
	}
	scope := models.RepoScope{OrgID: orgID, RepoID: publicRepoID}
	for _, test := range []struct {
		options Options
		source  Source
	}{
		{Options{Query: "锁"}, SourceComment},
		{Options{Query: "ListConfigsBySource", Source: SourceIssue}, SourceIssue},
		{Options{Query: "auth-lock", Source: SourceChange}, SourceChange},
	} {
		page, err := service.Repository(t.Context(), authz.Anonymous(), scope, test.options)
		if err != nil || len(page.Items) != 1 || page.Items[0].Number != 1 || len(page.Items[0].Matches) == 0 || page.Items[0].Matches[0].Source != test.source {
			t.Fatalf("search %+v = %+v, %v", test.options, page, err)
		}
	}
	stagePage, err := service.Repository(t.Context(), authz.Anonymous(), scope,
		Options{Query: "auth-lock", Source: SourceChange, Stage: "proposal"})
	if err != nil || len(stagePage.Items) != 1 {
		t.Fatalf("proposal stage search = %+v, %v", stagePage, err)
	}
	stagePage, err = service.Repository(t.Context(), authz.Anonymous(), scope,
		Options{Query: "auth-lock", Source: SourceChange, Stage: "implement"})
	if err != nil || len(stagePage.Items) != 0 {
		t.Fatalf("implement stage search = %+v, %v", stagePage, err)
	}
	ranked, err := service.Repository(t.Context(), authz.Anonymous(), scope, Options{Query: "鉴权"})
	if err != nil || len(ranked.Items) != 4 || ranked.Total != 4 || ranked.Items[0].Number != 17 || ranked.Items[len(ranked.Items)-1].Number != 19 {
		t.Fatalf("title/body/comment ranking = %+v, %v", ranked, err)
	}
	exact, err := service.Repository(t.Context(), authz.Anonymous(), scope, Options{Query: "17"})
	if err != nil || len(exact.Items) < 2 || exact.Items[0].Number != 17 {
		t.Fatalf("exact issue ranking = %+v, %v", exact, err)
	}
	exact, err = service.Repository(t.Context(), authz.Anonymous(), scope, Options{Query: "auth-lock"})
	if err != nil || len(exact.Items) < 2 || exact.Items[0].Number != 1 {
		t.Fatalf("exact change ranking = %+v, %v", exact, err)
	}
	const updatedComment = "updated-body-token 更新后立即可检索"
	if _, err := pool.Exec(t.Context(), `UPDATE comments SET body = $1 WHERE issue_id = $2`, updatedComment, commentIssueID); err != nil {
		t.Fatal(err)
	}
	updated, err := service.Repository(t.Context(), authz.Anonymous(), scope, Options{Query: "updated-body-token"})
	if err != nil || len(updated.Items) != 1 || updated.Items[0].Number != 19 {
		t.Fatalf("updated comment search = %+v, %v", updated, err)
	}
	var storedComment string
	if err := pool.QueryRow(t.Context(), `SELECT body FROM comments WHERE issue_id = $1`, commentIssueID).Scan(&storedComment); err != nil || storedComment != updatedComment {
		t.Fatalf("stored updated comment = %q, %v", storedComment, err)
	}
	page, err := service.Organization(t.Context(), authz.Anonymous(), models.OrgScope{OrgID: orgID}, Options{Query: "private-only-secret"})
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("private result leaked: %+v, %v", page, err)
	}

	var plan string
	if err := pool.QueryRow(t.Context(), `EXPLAIN (COSTS OFF) SELECT id FROM issues
		WHERE lower(title || E'\n' || body) LIKE public.likequery('ListConfigsBySource')`).Scan(&plan); err != nil {
		t.Fatal(err)
	}
	// EXPLAIN returns multiple rows; checking index existence is deterministic
	// while the planner may prefer a sequential scan for this tiny fixture.
	var indexCount int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM pg_indexes WHERE schemaname = current_schema()
		AND indexname LIKE 'issue_spec_search_%'`).Scan(&indexCount); err != nil || indexCount != len(searchIndexes) {
		t.Fatalf("search indexes = %d, plan=%q, err=%v", indexCount, plan, err)
	}
}
