package search

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	adminservice "github.com/higress-group/issue-spec/internal/server/admin"
	"github.com/higress-group/issue-spec/internal/server/authz"
	"github.com/higress-group/issue-spec/internal/server/models"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Service struct {
	pool  *pgxpool.Pool
	authz *authz.Service
}

func New(pool *pgxpool.Pool, authorization *authz.Service) (*Service, error) {
	if pool == nil || authorization == nil {
		return nil, errors.New("search: database and authorization are required")
	}
	return &Service{pool: pool, authz: authorization}, nil
}

func (s *Service) Repository(ctx context.Context, subject authz.Subject, scope models.RepoScope, options Options) (Page, error) {
	options, err := options.normalize()
	if err != nil {
		return Page{}, err
	}
	decision, err := s.authz.EvaluateRepository(ctx, subject, authz.RepositoryRequest{Scope: scope, Operation: authz.OperationRead})
	if err != nil {
		return Page{}, err
	}
	if err := decision.AuthorizationError(); err != nil {
		return Page{}, err
	}
	return s.query(ctx, scope.OrgID, []uuid.UUID{scope.RepoID}, options)
}

func (s *Service) Organization(ctx context.Context, subject authz.Subject, scope models.OrgScope, options Options) (Page, error) {
	options, err := options.normalize()
	if err != nil {
		return Page{}, err
	}
	readable, err := s.authz.ListReadableRepositories(ctx, subject, scope)
	if err != nil {
		return Page{}, err
	}
	repositories := make([]uuid.UUID, 0, len(readable))
	for _, item := range readable {
		repositories = append(repositories, item.Scope.RepoID)
	}
	if len(repositories) == 0 {
		return Page{Items: []Issue{}, Page: options.Page, PerPage: options.PerPage}, nil
	}
	return s.query(ctx, scope.OrgID, repositories, options)
}

func (s *Service) ContextRepository(ctx context.Context, subject authz.Subject, owner, repository string, options Options) (Page, error) {
	scope, err := s.resolveContextRepository(ctx, owner, repository)
	if err != nil {
		return Page{}, err
	}
	return s.Repository(ctx, subject, scope, options)
}

func (s *Service) resolveContextRepository(ctx context.Context, owner, repository string) (models.RepoScope, error) {
	var scope models.RepoScope
	err := s.pool.QueryRow(ctx, `SELECT o.id, r.id FROM orgs o JOIN repos r ON r.organization_id = o.id
		WHERE o.name_key = lower($1) AND r.name_key = lower($2) AND o.archived_at IS NULL AND r.archived_at IS NULL`,
		strings.TrimSpace(owner), strings.TrimSpace(repository)).Scan(&scope.OrgID, &scope.RepoID)
	if errors.Is(err, pgx.ErrNoRows) {
		return models.RepoScope{}, adminservice.ErrNotFound
	}
	if err != nil {
		return models.RepoScope{}, fmt.Errorf("search: resolve repository: %w", err)
	}
	return scope, nil
}

// FullRepository searches complete Issue discussions for the repository
// Issues page. Its longer deadline is isolated from Proposal discovery.
func (s *Service) FullRepository(ctx context.Context, subject authz.Subject, scope models.RepoScope, options FullOptions) (Page, error) {
	options, err := options.normalize()
	if err != nil {
		return Page{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, FullQueryTimeout)
	defer cancel()
	decision, err := s.authz.EvaluateRepository(ctx, subject, authz.RepositoryRequest{Scope: scope, Operation: authz.OperationRead})
	if err != nil {
		return Page{}, err
	}
	if err := decision.AuthorizationError(); err != nil {
		return Page{}, err
	}
	return s.queryFullRepository(ctx, scope, options)
}

func (s *Service) ContextRepositoryFull(ctx context.Context, subject authz.Subject,
	owner, repository string, options FullOptions) (Page, error) {
	scope, err := s.resolveContextRepository(ctx, owner, repository)
	if err != nil {
		return Page{}, err
	}
	return s.FullRepository(ctx, subject, scope, options)
}

type rawCommentMatch struct {
	ID   uuid.UUID `json:"id"`
	Body string    `json:"body"`
}

func (s *Service) query(ctx context.Context, orgID uuid.UUID, repositories []uuid.UUID, options Options) (Page, error) {
	ctx, cancel := context.WithTimeout(ctx, QueryTimeout)
	defer cancel()
	rows, err := s.pool.Query(ctx, searchQuery, orgID, repositories, strings.ToLower(options.Query),
		options.State, options.PerPage+1, (options.Page-1)*options.PerPage)
	if err != nil {
		return Page{}, fmt.Errorf("search: query issues: %w", err)
	}
	defer rows.Close()
	items := make([]Issue, 0, options.PerPage+1)
	var total int64
	for rows.Next() {
		var item Issue
		var issueBody string
		var changesJSON []byte
		var itemTotal int64
		if err := rows.Scan(&item.OrganizationID, &item.Organization, &item.RepositoryID, &item.Repository, &item.ID, &item.Number,
			&item.Title, &issueBody, &item.State, &item.UpdatedAt, &changesJSON, &item.Score,
			&itemTotal); err != nil {
			return Page{}, fmt.Errorf("search: scan result: %w", err)
		}
		total = itemTotal
		if err := json.Unmarshal(changesJSON, &item.Changes); err != nil {
			return Page{}, fmt.Errorf("search: decode change matches: %w", err)
		}
		item.Matches = []Match{{Source: SourceIssue, Excerpt: excerpt(item.Title+"\n"+issueBody, options.Query)}}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return Page{}, fmt.Errorf("search: iterate results: %w", err)
	}
	hasNext := len(items) > options.PerPage
	if hasNext {
		items = items[:options.PerPage]
	}
	if len(items) == 0 && options.Page > 1 {
		firstPageOptions := options
		firstPageOptions.Page = 1
		firstPageOptions.PerPage = 1
		firstPage, err := s.query(ctx, orgID, repositories, firstPageOptions)
		if err != nil {
			return Page{}, fmt.Errorf("search: recover total for empty page: %w", err)
		}
		total = firstPage.Total
	}
	return Page{Items: items, Page: options.Page, PerPage: options.PerPage, Total: total, HasNext: hasNext}, nil
}

func (s *Service) queryFullRepository(ctx context.Context, scope models.RepoScope, options FullOptions) (Page, error) {
	number := int64(0)
	trimmedNumber := strings.TrimPrefix(options.Query, "#")
	if parsed, err := strconv.ParseInt(trimmedNumber, 10, 64); err == nil && parsed > 0 {
		number = parsed
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return Page{}, fmt.Errorf("search: begin full repository query: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	// The search capability validates these four application-owned GIN indexes
	// at startup. PostgreSQL otherwise underprices per-row pg_bigm/pg_jieba
	// evaluation on modest repositories and may choose a linear scan. Keep this
	// planner setting transaction-local so only this index-backed operation is
	// affected.
	if _, err := tx.Exec(ctx, `SET LOCAL enable_seqscan = off`); err != nil {
		return Page{}, fmt.Errorf("search: require full-text index plan: %w", err)
	}
	rows, err := tx.Query(ctx, fullRepositorySearchQuery, scope.OrgID, scope.RepoID,
		strings.ToLower(options.Query), number, options.State, options.Labels, len(options.Labels),
		options.PerPage+1, (options.Page-1)*options.PerPage)
	if err != nil {
		return Page{}, fmt.Errorf("search: query full repository issues: %w", err)
	}
	defer rows.Close()
	items := make([]Issue, 0, options.PerPage+1)
	var total int64
	for rows.Next() {
		var item Issue
		var issueBody string
		var issueMatched bool
		var changesJSON, commentsJSON []byte
		var itemTotal int64
		if err := rows.Scan(&item.OrganizationID, &item.Organization, &item.RepositoryID, &item.Repository,
			&item.ID, &item.Number, &item.Title, &issueBody, &item.State, &item.UpdatedAt,
			&changesJSON, &item.Score, &issueMatched, &commentsJSON, &itemTotal); err != nil {
			return Page{}, fmt.Errorf("search: scan full repository result: %w", err)
		}
		total = itemTotal
		if err := json.Unmarshal(changesJSON, &item.Changes); err != nil {
			return Page{}, fmt.Errorf("search: decode full repository changes: %w", err)
		}
		item.Matches = make([]Match, 0, 4)
		if issueMatched {
			item.Matches = append(item.Matches, Match{Source: SourceIssue,
				Excerpt: excerpt(item.Title+"\n"+issueBody, options.Query)})
		}
		var comments []rawCommentMatch
		if err := json.Unmarshal(commentsJSON, &comments); err != nil {
			return Page{}, fmt.Errorf("search: decode full repository comment matches: %w", err)
		}
		for _, comment := range comments {
			id := comment.ID
			item.Matches = append(item.Matches, Match{Source: SourceComment, CommentID: &id,
				Excerpt: excerpt(comment.Body, options.Query)})
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return Page{}, fmt.Errorf("search: iterate full repository results: %w", err)
	}
	rows.Close()
	if err := tx.Commit(ctx); err != nil {
		return Page{}, fmt.Errorf("search: commit full repository query: %w", err)
	}
	hasNext := len(items) > options.PerPage
	if hasNext {
		items = items[:options.PerPage]
	}
	if len(items) == 0 && options.Page > 1 {
		firstPageOptions := options
		firstPageOptions.Page = 1
		firstPageOptions.PerPage = 1
		firstPage, err := s.queryFullRepository(ctx, scope, firstPageOptions)
		if err != nil {
			return Page{}, fmt.Errorf("search: recover full repository total for empty page: %w", err)
		}
		total = firstPage.Total
	}
	return Page{Items: items, Page: options.Page, PerPage: options.PerPage, Total: total, HasNext: hasNext}, nil
}

func excerpt(value, query string) string {
	const radius = 80
	value = strings.Join(strings.Fields(value), " ")
	if utf8.RuneCountInString(value) <= radius*2 {
		return value
	}
	runes := []rune(value)
	position := strings.Index(strings.ToLower(value), strings.ToLower(query))
	start := 0
	if position >= 0 {
		start = utf8.RuneCountInString(value[:position]) - radius
		if start < 0 {
			start = 0
		}
	}
	end := start + radius*2
	if end > len(runes) {
		end = len(runes)
	}
	prefix, suffix := "", ""
	if start > 0 {
		prefix = "…"
	}
	if end < len(runes) {
		suffix = "…"
	}
	return prefix + string(runes[start:end]) + suffix
}
