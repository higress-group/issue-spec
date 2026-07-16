package search

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const advisoryLockKey = "issue-spec:postgres-search-indexes:v1"

var searchIndexes = []string{
	`CREATE INDEX CONCURRENTLY IF NOT EXISTS issue_spec_search_issues_bigm_v1
		ON issues USING gin (lower(title || E'\n' || body) public.gin_bigm_ops)`,
	`CREATE INDEX CONCURRENTLY IF NOT EXISTS issue_spec_search_comments_bigm_v1
		ON comments USING gin (lower(body) public.gin_bigm_ops)`,
	`CREATE INDEX CONCURRENTLY IF NOT EXISTS issue_spec_search_issues_jieba_v1
		ON issues USING gin (to_tsvector('public.jiebacfg'::regconfig, title || E'\n' || body))`,
	`CREATE INDEX CONCURRENTLY IF NOT EXISTS issue_spec_search_comments_jieba_v1
		ON comments USING gin (to_tsvector('public.jiebacfg'::regconfig, body))`,
	`CREATE INDEX CONCURRENTLY IF NOT EXISTS issue_spec_search_change_keys_v1
		ON issue_spec_artifacts (organization_id, repository_id, lower(change_key), issue_id)
		WHERE active AND issue_id IS NOT NULL`,
}

// Prepare validates the explicitly selected PostgreSQL search mode and
// reconciles application-owned indexes. It never installs extensions or
// changes database parameters.
func Prepare(ctx context.Context, pool *pgxpool.Pool) error {
	if pool == nil {
		return fmt.Errorf("search prepare: database is required")
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("search prepare: acquire connection: %w", err)
	}
	defer conn.Release()

	if err := validateCapabilities(ctx, conn); err != nil {
		return err
	}
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended($1, 0))`, advisoryLockKey); err != nil {
		return fmt.Errorf("search prepare: acquire index lock: %w", err)
	}
	defer func() {
		_, _ = conn.Exec(context.Background(), `SELECT pg_advisory_unlock(hashtextextended($1, 0))`, advisoryLockKey)
	}()
	for _, statement := range searchIndexes {
		if _, err := conn.Exec(ctx, statement); err != nil {
			return fmt.Errorf("search prepare: reconcile index: %w", err)
		}
	}
	return validateIndexes(ctx, conn)
}

type capabilityConn interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func validateCapabilities(ctx context.Context, conn capabilityConn) error {
	var bigm, jieba bool
	if err := conn.QueryRow(ctx, `SELECT
		EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'pg_bigm'),
		EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'pg_jieba')`).Scan(&bigm, &jieba); err != nil {
		return fmt.Errorf("search prepare: inspect extensions: %w", err)
	}
	if !bigm || !jieba {
		return fmt.Errorf("search prepare: SEARCH_MODE=postgres requires operator-installed pg_bigm and pg_jieba extensions")
	}
	var preload string
	if err := conn.QueryRow(ctx, `SHOW shared_preload_libraries`).Scan(&preload); err != nil {
		return fmt.Errorf("search prepare: inspect shared_preload_libraries: %w", err)
	}
	loaded := false
	for _, item := range strings.Split(preload, ",") {
		if strings.TrimSpace(item) == "pg_jieba" {
			loaded = true
			break
		}
	}
	if !loaded {
		return fmt.Errorf("search prepare: SEARCH_MODE=postgres requires pg_jieba in shared_preload_libraries")
	}
	var vector, pattern string
	if err := conn.QueryRow(ctx, `SELECT to_tsvector('public.jiebacfg'::regconfig, '消费者鉴权锁争用')::text, public.likequery('锁')`).Scan(&vector, &pattern); err != nil {
		return fmt.Errorf("search prepare: validate pg_jieba and pg_bigm behavior: %w", err)
	}
	if strings.TrimSpace(vector) == "" || strings.TrimSpace(pattern) == "" {
		return fmt.Errorf("search prepare: PostgreSQL search extensions returned unusable results")
	}
	return nil
}

func validateIndexes(ctx context.Context, conn capabilityConn) error {
	var count int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM pg_indexes WHERE schemaname = current_schema()
		AND indexname = ANY($1::text[])`, []string{
		"issue_spec_search_issues_bigm_v1", "issue_spec_search_comments_bigm_v1",
		"issue_spec_search_issues_jieba_v1", "issue_spec_search_comments_jieba_v1",
		"issue_spec_search_change_keys_v1",
	}).Scan(&count); err != nil {
		return fmt.Errorf("search prepare: validate indexes: %w", err)
	}
	if count != len(searchIndexes) {
		return fmt.Errorf("search prepare: expected %d application-owned indexes, found %d", len(searchIndexes), count)
	}
	return nil
}
