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

func TestPostgresSearchProposalTitleBodyAuthorizationAndIndexes(t *testing.T) {
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
	if err := Prepare(t.Context(), pool); err != nil {
		t.Fatalf("recover same-name wrong search index: %v", err)
	}

	orgID, publicRepoID, privateRepoID := uuid.New(), uuid.New(), uuid.New()
	primaryID, privateID := uuid.New(), uuid.New()
	titleID, bodyID, commentOnlyID, ordinaryID, designID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO orgs (id, name, display_name) VALUES ($1, 'search-acme', 'Search Acme')`, []any{orgID}},
		{`INSERT INTO repos (id, organization_id, name, display_name, visibility) VALUES
			($1, $3, 'public-repo', 'Public Repo', 'public'), ($2, $3, 'private-repo', 'Private Repo', 'private')`, []any{publicRepoID, privateRepoID, orgID}},
		{`INSERT INTO issues (id, organization_id, repository_id, number, title, body) VALUES
			($1, $3, $4, 1, 'ListConfigsBySource 刷新慢', '调查消费者鉴权锁路径'),
			($2, $3, $5, 1, 'private-only-secret', 'must never leak')`, []any{primaryID, privateID, orgID, publicRepoID, privateRepoID}},
		{`INSERT INTO issues (id, organization_id, repository_id, number, title, body) VALUES
			($1, $6, $7, 17, '鉴权', 'title ranking fixture'),
			($2, $6, $7, 18, 'Proposal body fixture', '鉴权 body ranking fixture'),
			($3, $6, $7, 19, 'Comment-only Proposal', 'no title or body match'),
			($4, $6, $7, 20, 'Ordinary discussion', 'ordinary-only-token 鉴权'),
			($5, $6, $7, 21, 'Design discussion', 'design-only-token 鉴权')`, []any{titleID, bodyID, commentOnlyID, ordinaryID, designID, orgID, publicRepoID}},
		{`INSERT INTO comments (organization_id, repository_id, issue_id, body) VALUES
			($1, $2, $3, 'comment-only-token auth-lock 更新后仍不应被检索')`, []any{orgID, publicRepoID, commentOnlyID}},
		{`INSERT INTO issue_spec_artifacts (organization_id, repository_id, issue_id, change_key, artifact_type, content) VALUES
			($1, $2, $3, 'auth-lock', 'proposal', 'proposal'),
			($1, $2, $4, 'auth-title', 'proposal', 'proposal'),
			($1, $2, $5, 'auth-body', 'proposal', 'proposal'),
			($1, $2, $6, 'comment-only', 'proposal', 'proposal'),
			($1, $2, $7, 'design-only', 'design', 'design')`, []any{orgID, publicRepoID, primaryID, titleID, bodyID, commentOnlyID, designID}},
		{`INSERT INTO issue_spec_artifacts (organization_id, repository_id, issue_id, change_key, artifact_type, content)
			VALUES ($1, $2, $3, 'private-proposal', 'proposal', 'proposal')`, []any{orgID, privateRepoID, privateID}},
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
	for _, query := range []string{"ListConfigsBySource", "锁"} {
		page, err := service.Repository(t.Context(), authz.Anonymous(), scope, Options{Query: query})
		if err != nil || len(page.Items) != 1 || page.Items[0].Number != 1 || len(page.Items[0].Matches) != 1 ||
			page.Items[0].Matches[0].Source != SourceIssue || len(page.Items[0].Changes) != 1 || page.Items[0].Changes[0].Stage != "proposal" {
			t.Fatalf("Proposal title/body search %q = %+v, %v", query, page, err)
		}
	}
	for _, query := range []string{"comment-only-token", "auth-lock", "ordinary-only-token", "design-only-token", "#17"} {
		page, err := service.Repository(t.Context(), authz.Anonymous(), scope, Options{Query: query})
		if err != nil || len(page.Items) != 0 {
			t.Fatalf("excluded source search %q = %+v, %v", query, page, err)
		}
	}
	ranked, err := service.Repository(t.Context(), authz.Anonymous(), scope, Options{Query: "鉴权"})
	if err != nil || len(ranked.Items) != 3 || ranked.Total != 3 || ranked.Items[0].Number != 17 {
		t.Fatalf("Proposal title/body ranking = %+v, %v", ranked, err)
	}
	emptyPage, err := service.Repository(t.Context(), authz.Anonymous(), scope, Options{Query: "鉴权", Page: 99, PerPage: 1})
	if err != nil || len(emptyPage.Items) != 0 || emptyPage.Total != ranked.Total || emptyPage.HasNext {
		t.Fatalf("out-of-range page = %+v, %v", emptyPage, err)
	}
	const updatedComment = "updated-comment-token 更新后仍不应被检索"
	if _, err := pool.Exec(t.Context(), `UPDATE comments SET body = $1 WHERE issue_id = $2`, updatedComment, commentOnlyID); err != nil {
		t.Fatal(err)
	}
	commentPage, err := service.Repository(t.Context(), authz.Anonymous(), scope, Options{Query: "updated-comment-token"})
	if err != nil || len(commentPage.Items) != 0 {
		t.Fatalf("updated comment search = %+v, %v", commentPage, err)
	}
	const updatedBody = "updated-proposal-token 更新后立即可检索"
	if _, err := pool.Exec(t.Context(), `UPDATE issues SET body = $1, state = 'closed' WHERE id = $2`, updatedBody, bodyID); err != nil {
		t.Fatal(err)
	}
	updated, err := service.Repository(t.Context(), authz.Anonymous(), scope, Options{Query: "updated-proposal-token", State: "closed"})
	if err != nil || len(updated.Items) != 1 || updated.Items[0].Number != 18 {
		t.Fatalf("updated Proposal body search = %+v, %v", updated, err)
	}
	openOnly, err := service.Repository(t.Context(), authz.Anonymous(), scope, Options{Query: "updated-proposal-token", State: "open"})
	if err != nil || len(openOnly.Items) != 0 {
		t.Fatalf("Proposal state filtering = %+v, %v", openOnly, err)
	}
	page, err := service.Organization(t.Context(), authz.Anonymous(), models.OrgScope{OrgID: orgID}, Options{Query: "private-only-secret"})
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("private result leaked: %+v, %v", page, err)
	}

	var indexCount int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM pg_indexes WHERE schemaname = current_schema()
		AND indexname LIKE 'issue_spec_search_%'`).Scan(&indexCount); err != nil || indexCount != len(searchIndexes) {
		t.Fatalf("search indexes = %d, want=%d, err=%v", indexCount, len(searchIndexes), err)
	}
	var proposalIndex bool
	if err := pool.QueryRow(t.Context(), `SELECT EXISTS (SELECT 1 FROM pg_indexes WHERE schemaname = current_schema()
		AND indexname = 'issue_spec_search_proposals_v1')`).Scan(&proposalIndex); err != nil || !proposalIndex {
		t.Fatalf("Proposal search index exists=%t err=%v", proposalIndex, err)
	}
}
