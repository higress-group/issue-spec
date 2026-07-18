package changes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/server/models"
	"github.com/jackc/pgx/v5"
)

// LifecycleSnapshot is the smallest transaction-bound view needed by the
// notification integration. It deliberately reuses buildCard, so completed
// keeps the same verified/closure/artifact rules as the authoritative board.
type LifecycleSnapshot struct {
	ChangeKey string
	Lifecycle Lifecycle
}

// LifecycleForIssueTx resolves the change currently projected for issueID and
// evaluates that one change using the caller's open mutation transaction.
// Ordinary issues and projection anomalies return an empty snapshot.
func LifecycleForIssueTx(ctx context.Context, tx pgx.Tx, scope models.RepoScope, issueID uuid.UUID) (LifecycleSnapshot, error) {
	if tx == nil || scope.Validate() != nil || issueID == uuid.Nil {
		return LifecycleSnapshot{}, errors.New("changes: invalid lifecycle transaction input")
	}
	var changeKey string
	err := tx.QueryRow(ctx, `SELECT change_key FROM issue_spec_artifacts
		WHERE organization_id = $1 AND repository_id = $2 AND issue_id = $3 AND active
		ORDER BY artifact_type LIMIT 1`, scope.OrgID, scope.RepoID, issueID).Scan(&changeKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return LifecycleSnapshot{}, nil
	}
	if err != nil {
		return LifecycleSnapshot{}, fmt.Errorf("changes: resolve lifecycle change: %w", err)
	}
	changeKey = NormalizeChangeKey(changeKey)
	artifacts, err := loadLifecycleArtifacts(ctx, tx, scope, changeKey)
	if err != nil {
		return LifecycleSnapshot{}, err
	}
	issueIDs := make([]uuid.UUID, 0, len(artifacts))
	for _, artifact := range artifacts {
		issueIDs = append(issueIDs, artifact.issueID)
	}
	if len(artifacts) == 0 {
		return LifecycleSnapshot{}, nil
	}
	typed, err := loadLifecycleTypedArtifacts(ctx, tx, scope, issueIDs)
	if err != nil {
		return LifecycleSnapshot{}, err
	}
	card := buildCard(scope.OrgID, Repository{ID: scope.RepoID}, changeKey, artifacts, typed,
		map[uuid.UUID][]models.CodeChangeRelationship{})
	return LifecycleSnapshot{ChangeKey: card.ChangeKey, Lifecycle: card.Lifecycle}, nil
}

const lifecycleArtifactsQuery = `WITH candidate_issues AS (
	SELECT organization_id, repository_id, issue_id
	FROM issue_spec_artifacts
	WHERE organization_id = $1 AND repository_id = $2 AND active AND issue_id IS NOT NULL
		AND lower(btrim(change_key)) = $3
	UNION
	SELECT anomaly.organization_id, anomaly.repository_id, anomaly.source_id
	FROM projection_anomalies anomaly
	JOIN issues candidate ON candidate.organization_id = anomaly.organization_id
		AND candidate.repository_id = anomaly.repository_id AND candidate.id = anomaly.source_id
	WHERE anomaly.organization_id = $1 AND anomaly.repository_id = $2
		AND anomaly.projection_name = 'issue-spec-marker' AND anomaly.source_type = 'issue'
		AND anomaly.resolved_at IS NULL
		AND lower(btrim(substring(candidate.body FROM
			'<!--[[:space:]]*issue-spec:issue=[^[:space:]>]+[[:space:]]+change=([^[:space:]>]+)[[:space:]]+version=[^[:space:]>]+[[:space:]]*-->'))) = $3
)
SELECT issue.repository_id, issue.id, issue.number, issue.title, issue.body, issue.state, issue.updated_at,
	COALESCE(array_agg(DISTINCT label.name ORDER BY label.name) FILTER (WHERE label.id IS NOT NULL), '{}') AS labels,
	EXISTS (SELECT 1 FROM issue_spec_artifacts artifact
		WHERE artifact.organization_id = issue.organization_id AND artifact.repository_id = issue.repository_id
		AND artifact.issue_id = issue.id AND artifact.active) AS projected
FROM candidate_issues candidate
JOIN issues issue ON issue.organization_id = candidate.organization_id
	AND issue.repository_id = candidate.repository_id AND issue.id = candidate.issue_id
LEFT JOIN issue_labels assignment ON assignment.organization_id = issue.organization_id
	AND assignment.repository_id = issue.repository_id AND assignment.issue_id = issue.id
LEFT JOIN labels label ON label.organization_id = assignment.organization_id
	AND label.repository_id = assignment.repository_id AND label.id = assignment.label_id
GROUP BY issue.repository_id, issue.id
ORDER BY issue.number`

func loadLifecycleArtifacts(ctx context.Context, tx pgx.Tx, scope models.RepoScope,
	changeKey string) ([]rawArtifact, error) {
	rows, err := tx.Query(ctx, lifecycleArtifactsQuery, scope.OrgID, scope.RepoID, changeKey)
	if err != nil {
		return nil, fmt.Errorf("changes: load lifecycle artifacts: %w", err)
	}
	defer rows.Close()
	result := make([]rawArtifact, 0, 3)
	for rows.Next() {
		var item rawArtifact
		if err := rows.Scan(&item.repositoryID, &item.issueID, &item.number, &item.title, &item.body,
			&item.state, &item.updatedAt, &item.labels, &item.projected); err != nil {
			return nil, fmt.Errorf("changes: scan lifecycle artifact: %w", err)
		}
		parsed, ok := parseRawArtifact(item)
		if ok && parsed.repositoryID == scope.RepoID && parsed.changeKey == changeKey {
			result = append(result, parsed)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("changes: iterate lifecycle artifacts: %w", err)
	}
	return result, nil
}

const lifecycleTypedArtifactsQuery = `SELECT repository_id, issue_id, comment_type, comment_key,
	COALESCE(metadata->>'status', ''),
	COALESCE(metadata->'links'->'PR', '[]'::jsonb),
	updated_at
	FROM issue_spec_typed_comments
	WHERE organization_id = $1 AND repository_id = $2 AND issue_id = ANY($3::uuid[])
	ORDER BY comment_key`

func loadLifecycleTypedArtifacts(ctx context.Context, tx pgx.Tx, scope models.RepoScope,
	issueIDs []uuid.UUID) ([]typedArtifact, error) {
	if len(issueIDs) == 0 {
		return []typedArtifact{}, nil
	}
	rows, err := tx.Query(ctx, lifecycleTypedArtifactsQuery, scope.OrgID, scope.RepoID, issueIDs)
	if err != nil {
		return nil, fmt.Errorf("changes: load lifecycle typed artifacts: %w", err)
	}
	defer rows.Close()
	result := make([]typedArtifact, 0)
	for rows.Next() {
		var item typedArtifact
		var rawLinks json.RawMessage
		if err := rows.Scan(&item.repositoryID, &item.issueID, &item.typ, &item.key, &item.status,
			&rawLinks, &item.updatedAt); err != nil {
			return nil, fmt.Errorf("changes: scan lifecycle typed artifact: %w", err)
		}
		item.typ = strings.ToUpper(strings.TrimSpace(item.typ))
		item.status = strings.ToLower(strings.TrimSpace(item.status))
		item.closureLink = hasAcceptedClosureLink(rawLinks)
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("changes: iterate lifecycle typed artifacts: %w", err)
	}
	return result, nil
}
