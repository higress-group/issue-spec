package search

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const advisoryLockKey = "issue-spec:postgres-search-indexes:v1"

type searchIndex struct {
	name      string
	statement string
	signature string
}

var searchIndexes = []searchIndex{
	{"issue_spec_search_issues_bigm_v1",
		`CREATE INDEX CONCURRENTLY IF NOT EXISTS issue_spec_search_issues_bigm_v1
			ON issues USING gin (lower(title || E'\n' || body) public.gin_bigm_ops)`,
		`issues USING gin (lower(((title || '\n'::text) || body)) gin_bigm_ops)`},
	{"issue_spec_search_issues_jieba_v1",
		`CREATE INDEX CONCURRENTLY IF NOT EXISTS issue_spec_search_issues_jieba_v1
			ON issues USING gin (to_tsvector('public.jiebacfg'::regconfig, title || E'\n' || body))`,
		`issues USING gin (to_tsvector('jiebacfg'::regconfig, ((title || '\n'::text) || body)))`},
	{"issue_spec_search_proposals_v1",
		`CREATE INDEX CONCURRENTLY IF NOT EXISTS issue_spec_search_proposals_v1
			ON issue_spec_artifacts (organization_id, repository_id, issue_id, change_key)
			WHERE active AND artifact_type = 'proposal' AND issue_id IS NOT NULL`,
		`issue_spec_artifacts USING btree (organization_id, repository_id, issue_id, change_key) WHERE (active AND (artifact_type = 'proposal'::text) AND (issue_id IS NOT NULL))`},
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
	for _, index := range searchIndexes {
		if err := reconcileIndex(ctx, conn, index); err != nil {
			return err
		}
	}
	return validateIndexes(ctx, conn)
}

type capabilityConn interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

type indexConn interface {
	capabilityConn
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

func reconcileIndex(ctx context.Context, conn indexConn, expected searchIndex) error {
	exists, healthy, _, err := inspectIndex(ctx, conn, expected)
	if err != nil {
		return fmt.Errorf("search prepare: inspect index %s: %w", expected.name, err)
	}
	if exists && !healthy {
		if _, err := conn.Exec(ctx, "DROP INDEX CONCURRENTLY "+pgx.Identifier{expected.name}.Sanitize()); err != nil {
			return fmt.Errorf("search prepare: drop stale index %s: %w", expected.name, err)
		}
	}
	if !exists || !healthy {
		if _, err := conn.Exec(ctx, expected.statement); err != nil {
			return fmt.Errorf("search prepare: create index %s: %w", expected.name, err)
		}
	}
	return nil
}

func inspectIndex(ctx context.Context, conn capabilityConn, expected searchIndex) (bool, bool, string, error) {
	var definition string
	var valid, ready bool
	err := conn.QueryRow(ctx, `SELECT pg_get_indexdef(c.oid), i.indisvalid, i.indisready
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		JOIN pg_index i ON i.indexrelid = c.oid
		WHERE n.nspname = current_schema() AND c.relname = $1`, expected.name).Scan(&definition, &valid, &ready)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, false, "", nil
	}
	if err != nil {
		return false, false, "", err
	}
	return true, valid && ready && indexDefinitionMatches(definition, expected.signature), definition, nil
}

func indexDefinitionMatches(definition, signature string) bool {
	normalized := strings.ReplaceAll(definition, "public.", "")
	normalized = strings.ReplaceAll(normalized, "\n", `\n`)
	return strings.Contains(normalized, signature)
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
	var similarity float32
	if err := conn.QueryRow(ctx, `SELECT to_tsvector('public.jiebacfg'::regconfig, '消费者鉴权锁争用')::text,
		public.likequery('锁'), public.bigm_similarity('消费者鉴权锁争用', '鉴权')`).Scan(&vector, &pattern, &similarity); err != nil {
		return fmt.Errorf("search prepare: validate pg_jieba and pg_bigm behavior: %w", err)
	}
	if strings.TrimSpace(vector) == "" || strings.TrimSpace(pattern) == "" || similarity <= 0 {
		return fmt.Errorf("search prepare: PostgreSQL search extensions returned unusable results")
	}
	return nil
}

func validateIndexes(ctx context.Context, conn capabilityConn) error {
	for _, expected := range searchIndexes {
		exists, healthy, definition, err := inspectIndex(ctx, conn, expected)
		if err != nil {
			return fmt.Errorf("search prepare: validate index %s: %w", expected.name, err)
		}
		if !exists || !healthy {
			return fmt.Errorf("search prepare: index %s is invalid, not ready, or has an unexpected definition: %s", expected.name, definition)
		}
	}
	return nil
}
