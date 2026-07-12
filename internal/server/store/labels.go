package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/server/models"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var ErrLabelAlreadyExists = errors.New("store: label already exists")

const labelColumns = `id, organization_id, repository_id, name, color, description,
	representation_version, created_at, updated_at`

func (s RepoStore) ListLabels(ctx context.Context, page, perPage int) (models.LabelPage, error) {
	if err := s.validate(); err != nil || page < 1 || perPage < 1 {
		return models.LabelPage{}, ErrInvalidInput
	}
	var result models.LabelPage
	if err := s.db.QueryRow(ctx, `SELECT count(*), COALESCE(max(updated_at), to_timestamp(0))
		FROM labels WHERE organization_id = $1 AND repository_id = $2`, s.scope.OrgID, s.scope.RepoID).
		Scan(&result.Total, &result.LastModified); err != nil {
		return models.LabelPage{}, fmt.Errorf("count labels: %w", err)
	}
	rows, err := s.db.Query(ctx, `SELECT `+labelColumns+` FROM labels
		WHERE organization_id = $1 AND repository_id = $2
		ORDER BY name_key, id LIMIT $3 OFFSET $4`, s.scope.OrgID, s.scope.RepoID, perPage, (page-1)*perPage)
	if err != nil {
		return models.LabelPage{}, fmt.Errorf("list labels: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		label, err := scanLabel(rows)
		if err != nil {
			return models.LabelPage{}, err
		}
		result.Items = append(result.Items, label)
	}
	if err := rows.Err(); err != nil {
		return models.LabelPage{}, err
	}
	if err := s.db.QueryRow(ctx, `SELECT labels_collection_version FROM repos
		WHERE organization_id = $1 AND id = $2`, s.scope.OrgID, s.scope.RepoID).
		Scan(&result.CollectionVersion); err != nil {
		return models.LabelPage{}, fmt.Errorf("load labels collection version: %w", mapError(err))
	}
	return result, nil
}

func (s RepoStore) CreateLabel(ctx context.Context, input models.NewLabel) (models.Label, error) {
	if err := s.requireMutationTx(); err != nil {
		return models.Label{}, err
	}
	if input.ID == uuid.Nil {
		input.ID = uuid.New()
	}
	color := strings.TrimPrefix(input.Color, "#")
	if color == "" {
		color = "ededed"
	}
	row := s.db.QueryRow(ctx, `INSERT INTO labels
		(id, organization_id, repository_id, name, color, description)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING `+labelColumns,
		input.ID, s.scope.OrgID, s.scope.RepoID, strings.TrimSpace(input.Name), color, input.Description)
	label, err := scanLabel(row)
	if err != nil {
		return models.Label{}, fmt.Errorf("create label: %w", mapLabelError(err))
	}
	if _, err := s.IncrementCollectionVersions(ctx, RepoCollectionLabels); err != nil {
		return models.Label{}, err
	}
	return label, nil
}

func (s RepoStore) LabelByName(ctx context.Context, name string) (models.Label, error) {
	if err := s.validate(); err != nil {
		return models.Label{}, err
	}
	label, err := scanLabel(s.db.QueryRow(ctx, `SELECT `+labelColumns+` FROM labels
		WHERE organization_id = $1 AND repository_id = $2 AND name_key = lower($3)`,
		s.scope.OrgID, s.scope.RepoID, strings.TrimSpace(name)))
	if err != nil {
		return models.Label{}, fmt.Errorf("get label: %w", mapLabelError(err))
	}
	return label, nil
}

func (s RepoStore) UpdateLabel(ctx context.Context, currentName string, input models.LabelUpdate) (models.Label, error) {
	if err := s.requireMutationTx(); err != nil {
		return models.Label{}, err
	}
	row := s.db.QueryRow(ctx, `UPDATE labels SET name = $4, color = $5, description = $6,
		representation_version = representation_version + 1, updated_at = clock_timestamp()
		WHERE organization_id = $1 AND repository_id = $2 AND name_key = lower($3)
		RETURNING `+labelColumns, s.scope.OrgID, s.scope.RepoID, strings.TrimSpace(currentName),
		strings.TrimSpace(input.Name), strings.TrimPrefix(input.Color, "#"), input.Description)
	label, err := scanLabel(row)
	if err != nil {
		return models.Label{}, fmt.Errorf("update label: %w", mapLabelError(err))
	}
	tag, err := s.db.Exec(ctx, `UPDATE issues SET
		representation_version = representation_version + 1,
		labels_collection_version = labels_collection_version + 1,
		updated_at = clock_timestamp() WHERE organization_id = $1 AND repository_id = $2
		AND id IN (SELECT issue_id FROM issue_labels WHERE organization_id = $1
		AND repository_id = $2 AND label_id = $3)`, s.scope.OrgID, s.scope.RepoID, label.ID)
	if err != nil {
		return models.Label{}, fmt.Errorf("invalidate issues for label update: %w", err)
	}
	collections := []RepoCollection{RepoCollectionLabels}
	if tag.RowsAffected() > 0 {
		collections = append(collections, RepoCollectionIssues)
	}
	if _, err := s.IncrementCollectionVersions(ctx, collections...); err != nil {
		return models.Label{}, err
	}
	return label, nil
}

func (s RepoStore) IssueLabels(ctx context.Context, number int64) (models.Issue, []models.Label, error) {
	issue, err := s.IssueByNumber(ctx, number)
	if err != nil {
		return models.Issue{}, nil, err
	}
	labels, err := s.labelsForIssue(ctx, issue.ID)
	return issue, labels, err
}

func (s RepoStore) AddIssueLabels(ctx context.Context, number int64, names []string, actor uuid.UUID) (models.Issue, []models.Label, error) {
	return s.changeIssueLabels(ctx, number, names, actor, false)
}

func (s RepoStore) ReplaceIssueLabels(ctx context.Context, number int64, names []string, actor uuid.UUID) (models.Issue, []models.Label, error) {
	return s.changeIssueLabels(ctx, number, names, actor, true)
}

func (s RepoStore) changeIssueLabels(ctx context.Context, number int64, names []string, actor uuid.UUID, replace bool) (models.Issue, []models.Label, error) {
	if err := s.requireMutationTx(); err != nil || number <= 0 || actor == uuid.Nil {
		return models.Issue{}, nil, ErrInvalidInput
	}
	issue, err := s.issueForLabelMutation(ctx, number)
	if err != nil {
		return models.Issue{}, nil, err
	}
	wanted, err := s.labelsByNames(ctx, names)
	if err != nil {
		return models.Issue{}, nil, err
	}
	changed := false
	if replace {
		ids := make([]uuid.UUID, len(wanted))
		for index := range wanted {
			ids[index] = wanted[index].ID
		}
		var tag pgconn.CommandTag
		if len(ids) == 0 {
			tag, err = s.db.Exec(ctx, `DELETE FROM issue_labels WHERE organization_id = $1
				AND repository_id = $2 AND issue_id = $3`, s.scope.OrgID, s.scope.RepoID, issue.ID)
		} else {
			tag, err = s.db.Exec(ctx, `DELETE FROM issue_labels WHERE organization_id = $1
				AND repository_id = $2 AND issue_id = $3 AND NOT (label_id = ANY($4::uuid[]))`,
				s.scope.OrgID, s.scope.RepoID, issue.ID, ids)
		}
		if err != nil {
			return models.Issue{}, nil, fmt.Errorf("replace issue labels: %w", err)
		}
		changed = tag.RowsAffected() > 0
	}
	for _, label := range wanted {
		tag, err := s.db.Exec(ctx, `INSERT INTO issue_labels
			(organization_id, repository_id, issue_id, label_id, assigned_by_user_id)
			VALUES ($1, $2, $3, $4, $5) ON CONFLICT (issue_id, label_id) DO NOTHING`,
			s.scope.OrgID, s.scope.RepoID, issue.ID, label.ID, actor)
		if err != nil {
			return models.Issue{}, nil, fmt.Errorf("add issue label: %w", mapError(err))
		}
		changed = changed || tag.RowsAffected() > 0
	}
	if changed {
		issue, err = s.bumpIssueLabels(ctx, issue.ID)
		if err != nil {
			return models.Issue{}, nil, err
		}
	}
	labels, err := s.labelsForIssue(ctx, issue.ID)
	return issue, labels, err
}

func (s RepoStore) RemoveIssueLabel(ctx context.Context, number int64, name string) (models.Issue, []models.Label, error) {
	if err := s.requireMutationTx(); err != nil || number <= 0 {
		return models.Issue{}, nil, ErrInvalidInput
	}
	issue, err := s.issueForLabelMutation(ctx, number)
	if err != nil {
		return models.Issue{}, nil, err
	}
	tag, err := s.db.Exec(ctx, `DELETE FROM issue_labels il USING labels l
		WHERE il.organization_id = $1 AND il.repository_id = $2 AND il.issue_id = $3
		AND l.organization_id = il.organization_id AND l.repository_id = il.repository_id
		AND l.id = il.label_id AND l.name_key = lower($4)`, s.scope.OrgID, s.scope.RepoID,
		issue.ID, strings.TrimSpace(name))
	if err != nil {
		return models.Issue{}, nil, fmt.Errorf("remove issue label: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return models.Issue{}, nil, ErrNotFound
	}
	issue, err = s.bumpIssueLabels(ctx, issue.ID)
	if err != nil {
		return models.Issue{}, nil, err
	}
	labels, err := s.labelsForIssue(ctx, issue.ID)
	return issue, labels, err
}

func (s RepoStore) issueForLabelMutation(ctx context.Context, number int64) (models.Issue, error) {
	row := s.db.QueryRow(ctx, `SELECT `+issueColumns+` FROM issues WHERE organization_id = $1
		AND repository_id = $2 AND number = $3 FOR UPDATE`, s.scope.OrgID, s.scope.RepoID, number)
	issue, err := scanIssue(row)
	if err != nil {
		return models.Issue{}, fmt.Errorf("load issue for labels: %w", mapError(err))
	}
	return issue, nil
}

func (s RepoStore) labelsByNames(ctx context.Context, names []string) ([]models.Label, error) {
	normalized := normalizedLabels(names)
	if len(normalized) == 0 {
		return []models.Label{}, nil
	}
	rows, err := s.db.Query(ctx, `SELECT `+labelColumns+` FROM labels WHERE organization_id = $1
		AND repository_id = $2 AND name_key = ANY($3::text[]) ORDER BY name_key, id`,
		s.scope.OrgID, s.scope.RepoID, normalized)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var labels []models.Label
	for rows.Next() {
		label, err := scanLabel(rows)
		if err != nil {
			return nil, err
		}
		labels = append(labels, label)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(labels) != len(normalized) {
		return nil, ErrLabelNotFound
	}
	return labels, nil
}

func (s RepoStore) bumpIssueLabels(ctx context.Context, issueID uuid.UUID) (models.Issue, error) {
	row := s.db.QueryRow(ctx, `UPDATE issues SET
		representation_version = representation_version + 1,
		labels_collection_version = labels_collection_version + 1,
		updated_at = clock_timestamp() WHERE organization_id = $1 AND repository_id = $2 AND id = $3
		RETURNING `+issueColumns, s.scope.OrgID, s.scope.RepoID, issueID)
	issue, err := scanIssue(row)
	if err != nil {
		return models.Issue{}, fmt.Errorf("bump issue labels version: %w", mapError(err))
	}
	if _, err := s.IncrementCollectionVersions(ctx, RepoCollectionIssues); err != nil {
		return models.Issue{}, err
	}
	return issue, nil
}

func (s RepoStore) requireMutationTx() error {
	if err := s.validate(); err != nil {
		return err
	}
	if !s.inTx {
		return errors.New("store: protocol mutation requires a transaction-backed repository scope")
	}
	return nil
}

func scanLabel(row rowScanner) (models.Label, error) {
	var label models.Label
	err := row.Scan(&label.ID, &label.Scope.OrgID, &label.Scope.RepoID, &label.Name,
		&label.Color, &label.Description, &label.RepresentationVersion, &label.CreatedAt, &label.UpdatedAt)
	return label, err
}

func mapLabelError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.ConstraintName == "labels_repo_name_key_unique" {
		return fmt.Errorf("%w: %w", ErrLabelAlreadyExists, err)
	}
	return mapError(err)
}
