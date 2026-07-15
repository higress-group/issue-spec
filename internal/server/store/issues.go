package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/server/models"
	"github.com/jackc/pgx/v5"
)

var ErrLabelNotFound = errors.New("store: issue label not found")

const issueColumns = `
	id, organization_id, repository_id, number, author_id, title, body, state,
	representation_version, comments_collection_version, labels_collection_version,
	bindings_collection_version, references_collection_version, evidence_collection_version,
	created_at, updated_at, closed_at`

const qualifiedIssueColumns = `
	i.id, i.organization_id, i.repository_id, i.number, i.author_id, i.title, i.body, i.state,
	i.representation_version, i.comments_collection_version, i.labels_collection_version,
	i.bindings_collection_version, i.references_collection_version, i.evidence_collection_version,
	i.created_at, i.updated_at, i.closed_at`

// PGX exposes the caller-owned transaction only for security-sensitive
// adapters that must evaluate authorization in the same snapshot as a write.
func (t *Tx) PGX() pgx.Tx { return t.tx }

func (s *Store) ResolveRepository(ctx context.Context, owner, name string) (models.RepositoryResource, error) {
	return resolveRepository(ctx, s.pool, owner, name)
}

func (t *Tx) ResolveRepository(ctx context.Context, owner, name string) (models.RepositoryResource, error) {
	return resolveRepository(ctx, t.tx, owner, name)
}

func resolveRepository(ctx context.Context, db DBTX, owner, name string) (models.RepositoryResource, error) {
	owner, name = strings.TrimSpace(owner), strings.TrimSpace(name)
	if owner == "" || name == "" {
		return models.RepositoryResource{}, ErrInvalidInput
	}
	var resource models.RepositoryResource
	err := db.QueryRow(ctx, `SELECT o.id, r.id, o.name, r.name,
		r.issues_collection_version, r.comments_collection_version, r.updated_at
		FROM orgs o JOIN repos r ON r.organization_id = o.id
		WHERE o.name_key = lower($1) AND r.name_key = lower($2)
		AND o.archived_at IS NULL AND r.archived_at IS NULL`, owner, name).
		Scan(&resource.Scope.OrgID, &resource.Scope.RepoID, &resource.Owner, &resource.Name,
			&resource.IssuesCollectionVersion, &resource.CommentsCollectionVersion, &resource.UpdatedAt)
	if err != nil {
		return models.RepositoryResource{}, fmt.Errorf("resolve repository: %w", mapError(err))
	}
	return resource, nil
}

// AllocateIssueNumber atomically advances the repository-local sequence. When
// called through Tx.Repo it participates in the caller's transaction.
func (s RepoStore) AllocateIssueNumber(ctx context.Context) (int64, error) {
	if err := s.validate(); err != nil {
		return 0, err
	}
	var number int64
	err := s.db.QueryRow(ctx, `
		UPDATE repos
		SET next_issue_number = next_issue_number + 1
		WHERE organization_id = $1 AND id = $2
		RETURNING next_issue_number - 1`, s.scope.OrgID, s.scope.RepoID).Scan(&number)
	if err != nil {
		return 0, fmt.Errorf("allocate issue number: %w", mapError(err))
	}
	return number, nil
}

// CreateIssue allocates the per-repository number and inserts the issue in one
// transaction. A caller already in a transaction keeps that same transaction.
func (s RepoStore) CreateIssue(ctx context.Context, input models.NewIssue) (models.Issue, error) {
	if err := s.validate(); err != nil {
		return models.Issue{}, err
	}
	if input.ID == uuid.Nil {
		input.ID = uuid.New()
	}
	if s.inTx {
		return s.createIssue(ctx, input)
	}
	if s.root == nil {
		return models.Issue{}, errors.New("store: CreateIssue requires a store- or transaction-backed repository scope")
	}
	var issue models.Issue
	err := s.root.WithinTx(ctx, func(tx *Tx) error {
		var err error
		issue, err = tx.ScopedRepo(s.scope).createIssue(ctx, input)
		return err
	})
	return issue, err
}

func (s RepoStore) createIssue(ctx context.Context, input models.NewIssue) (models.Issue, error) {
	number, err := s.AllocateIssueNumber(ctx)
	if err != nil {
		return models.Issue{}, err
	}
	row := s.db.QueryRow(ctx, `
		INSERT INTO issues (
			id, organization_id, repository_id, number, author_id, title, body
		) VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING `+issueColumns,
		input.ID, s.scope.OrgID, s.scope.RepoID, number, input.AuthorID, input.Title, input.Body)
	issue, err := scanIssue(row)
	if err != nil {
		return models.Issue{}, fmt.Errorf("insert issue: %w", mapError(err))
	}
	for _, label := range input.Labels {
		tag, err := s.db.Exec(ctx, `INSERT INTO issue_labels
			(organization_id, repository_id, issue_id, label_id, assigned_by_user_id)
			SELECT $1, $2, $3, l.id, $4 FROM labels l
			WHERE l.organization_id = $1 AND l.repository_id = $2 AND l.name_key = lower($5)
			ON CONFLICT (issue_id, label_id) DO NOTHING`, s.scope.OrgID, s.scope.RepoID,
			issue.ID, input.AuthorID, label)
		if err != nil {
			return models.Issue{}, fmt.Errorf("assign issue label: %w", mapError(err))
		}
		if tag.RowsAffected() == 0 {
			return models.Issue{}, fmt.Errorf("%w: %q", ErrLabelNotFound, label)
		}
	}
	return issue, nil
}

func (s RepoStore) IssueByNumber(ctx context.Context, number int64) (models.Issue, error) {
	if err := s.validate(); err != nil {
		return models.Issue{}, err
	}
	row := s.db.QueryRow(ctx, `SELECT `+issueColumns+`
		FROM issues
		WHERE organization_id = $1 AND repository_id = $2 AND number = $3`,
		s.scope.OrgID, s.scope.RepoID, number)
	issue, err := scanIssue(row)
	if err != nil {
		return models.Issue{}, fmt.Errorf("get issue %d: %w", number, mapError(err))
	}
	return issue, nil
}

func (s RepoStore) IssueSnapshotByNumber(ctx context.Context, number int64) (models.IssueSnapshot, error) {
	if err := s.validate(); err != nil || number <= 0 {
		return models.IssueSnapshot{}, ErrInvalidInput
	}
	row := s.db.QueryRow(ctx, `SELECT `+qualifiedIssueColumns+`,
		COALESCE(u.login, 'ghost'), COALESCE(u.nickname, u.display_name, u.login, 'ghost'),
		(SELECT count(*) FROM comments c
		WHERE c.organization_id = i.organization_id AND c.repository_id = i.repository_id AND c.issue_id = i.id)
		FROM issues i LEFT JOIN users u ON u.id = i.author_id
		WHERE i.organization_id = $1 AND i.repository_id = $2 AND i.number = $3`,
		s.scope.OrgID, s.scope.RepoID, number)
	snapshot, err := scanIssueSnapshot(row)
	if err != nil {
		return models.IssueSnapshot{}, fmt.Errorf("get issue snapshot %d: %w", number, mapError(err))
	}
	snapshot.Labels, err = s.labelsForIssue(ctx, snapshot.Issue.ID)
	return snapshot, err
}

func (s RepoStore) ListIssues(ctx context.Context, options models.IssueListOptions) (models.IssuePage, error) {
	if err := s.validate(); err != nil || options.Page < 1 || options.PerPage < 1 {
		return models.IssuePage{}, ErrInvalidInput
	}
	clauses := []string{"i.organization_id = $1", "i.repository_id = $2"}
	args := []any{s.scope.OrgID, s.scope.RepoID}
	if options.State != nil {
		if *options.State != models.IssueStateOpen && *options.State != models.IssueStateClosed {
			return models.IssuePage{}, ErrInvalidInput
		}
		args = append(args, *options.State)
		clauses = append(clauses, fmt.Sprintf("i.state = $%d", len(args)))
	}
	if options.Since != nil {
		args = append(args, options.Since.UTC())
		clauses = append(clauses, fmt.Sprintf("i.updated_at >= $%d", len(args)))
	}
	labels := normalizedLabels(options.Labels)
	if len(labels) > 0 {
		args = append(args, labels, len(labels))
		clauses = append(clauses, fmt.Sprintf(`(SELECT count(DISTINCT l.name_key) FROM issue_labels il
			JOIN labels l ON l.organization_id = il.organization_id AND l.repository_id = il.repository_id AND l.id = il.label_id
			WHERE il.organization_id = i.organization_id AND il.repository_id = i.repository_id
			AND il.issue_id = i.id AND l.name_key = ANY($%d::text[])) = $%d`, len(args)-1, len(args)))
	}
	where := strings.Join(clauses, " AND ")
	var page models.IssuePage
	if err := s.db.QueryRow(ctx, `SELECT count(*), COALESCE(max(i.updated_at), to_timestamp(0))
		FROM issues i WHERE `+where, args...).Scan(&page.Total, &page.LastModified); err != nil {
		return models.IssuePage{}, fmt.Errorf("count issues: %w", err)
	}
	args = append(args, options.PerPage, (options.Page-1)*options.PerPage)
	rows, err := s.db.Query(ctx, `SELECT `+qualifiedIssueColumns+`, COALESCE(u.login, 'ghost'),
		COALESCE(u.nickname, u.display_name, u.login, 'ghost'),
		(SELECT count(*) FROM comments c WHERE c.organization_id = i.organization_id
		AND c.repository_id = i.repository_id AND c.issue_id = i.id)
		FROM issues i LEFT JOIN users u ON u.id = i.author_id WHERE `+where+
		` ORDER BY i.updated_at DESC, i.number DESC, i.id DESC LIMIT $`+fmt.Sprint(len(args)-1)+` OFFSET $`+fmt.Sprint(len(args)), args...)
	if err != nil {
		return models.IssuePage{}, fmt.Errorf("list issues: %w", err)
	}
	for rows.Next() {
		item, err := scanIssueSnapshot(rows)
		if err != nil {
			rows.Close()
			return models.IssuePage{}, err
		}
		page.Items = append(page.Items, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return models.IssuePage{}, err
	}
	rows.Close()
	for index := range page.Items {
		page.Items[index].Labels, err = s.labelsForIssue(ctx, page.Items[index].Issue.ID)
		if err != nil {
			return models.IssuePage{}, err
		}
	}
	if err := s.db.QueryRow(ctx, `SELECT issues_collection_version FROM repos
		WHERE organization_id = $1 AND id = $2`, s.scope.OrgID, s.scope.RepoID).Scan(&page.CollectionVersion); err != nil {
		return models.IssuePage{}, fmt.Errorf("load issues collection version: %w", mapError(err))
	}
	return page, nil
}

func normalizedLabels(values []string) []string {
	seen := map[string]struct{}{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func (s RepoStore) labelsForIssue(ctx context.Context, issueID uuid.UUID) ([]models.Label, error) {
	rows, err := s.db.Query(ctx, `SELECT l.id, l.organization_id, l.repository_id,
		l.name, l.color, l.description, l.representation_version, l.created_at, l.updated_at
		FROM issue_labels il JOIN labels l ON l.organization_id = il.organization_id
		AND l.repository_id = il.repository_id AND l.id = il.label_id
		WHERE il.organization_id = $1 AND il.repository_id = $2 AND il.issue_id = $3
		ORDER BY l.name_key, l.id`, s.scope.OrgID, s.scope.RepoID, issueID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	labels := make([]models.Label, 0)
	for rows.Next() {
		var label models.Label
		if err := rows.Scan(&label.ID, &label.Scope.OrgID, &label.Scope.RepoID, &label.Name,
			&label.Color, &label.Description, &label.RepresentationVersion,
			&label.CreatedAt, &label.UpdatedAt); err != nil {
			return nil, err
		}
		labels = append(labels, label)
	}
	return labels, rows.Err()
}

type rowScanner interface {
	Scan(...any) error
}

func scanIssue(row rowScanner) (models.Issue, error) {
	var issue models.Issue
	err := row.Scan(
		&issue.ID,
		&issue.Scope.OrgID,
		&issue.Scope.RepoID,
		&issue.Number,
		&issue.AuthorID,
		&issue.Title,
		&issue.Body,
		&issue.State,
		&issue.RepresentationVersion,
		&issue.CommentsCollectionVersion,
		&issue.LabelsCollectionVersion,
		&issue.BindingsCollectionVersion,
		&issue.ReferencesCollectionVersion,
		&issue.EvidenceCollectionVersion,
		&issue.CreatedAt,
		&issue.UpdatedAt,
		&issue.ClosedAt,
	)
	return issue, err
}

func scanIssueSnapshot(row rowScanner) (models.IssueSnapshot, error) {
	var snapshot models.IssueSnapshot
	err := row.Scan(
		&snapshot.Issue.ID, &snapshot.Issue.Scope.OrgID, &snapshot.Issue.Scope.RepoID,
		&snapshot.Issue.Number, &snapshot.Issue.AuthorID, &snapshot.Issue.Title,
		&snapshot.Issue.Body, &snapshot.Issue.State, &snapshot.Issue.RepresentationVersion,
		&snapshot.Issue.CommentsCollectionVersion, &snapshot.Issue.LabelsCollectionVersion,
		&snapshot.Issue.BindingsCollectionVersion, &snapshot.Issue.ReferencesCollectionVersion,
		&snapshot.Issue.EvidenceCollectionVersion, &snapshot.Issue.CreatedAt,
		&snapshot.Issue.UpdatedAt, &snapshot.Issue.ClosedAt, &snapshot.AuthorLogin,
		&snapshot.AuthorDisplayName,
		&snapshot.CommentCount,
	)
	return snapshot, err
}

var _ = time.Time{}
