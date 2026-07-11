package changes

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func loadRepositories(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, repoIDs []uuid.UUID) (map[uuid.UUID]repositorySnapshot, error) {
	rows, err := tx.Query(ctx, `SELECT id, name, display_name, issues_collection_version,
		comments_collection_version, labels_collection_version, artifacts_collection_version, updated_at
		FROM repos WHERE organization_id = $1 AND id = ANY($2::uuid[]) AND archived_at IS NULL
		ORDER BY id`, orgID, repoIDs)
	if err != nil {
		return nil, fmt.Errorf("changes: load repositories: %w", err)
	}
	defer rows.Close()
	result := make(map[uuid.UUID]repositorySnapshot, len(repoIDs))
	for rows.Next() {
		var item repositorySnapshot
		if err := rows.Scan(&item.repository.ID, &item.repository.Name, &item.repository.DisplayName,
			&item.issues, &item.comments, &item.labels, &item.artifacts, &item.updatedAt); err != nil {
			return nil, fmt.Errorf("changes: scan repository: %w", err)
		}
		result[item.repository.ID] = item
	}
	return result, rows.Err()
}

func loadArtifacts(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, repoIDs []uuid.UUID) ([]rawArtifact, map[uuid.UUID][]string, error) {
	rows, err := tx.Query(ctx, `WITH candidate_issues AS (
		SELECT organization_id, repository_id, issue_id
		FROM issue_spec_artifacts
		WHERE organization_id = $1 AND repository_id = ANY($2::uuid[]) AND active AND issue_id IS NOT NULL
		UNION
		SELECT organization_id, repository_id, source_id
		FROM projection_anomalies
		WHERE organization_id = $1 AND repository_id = ANY($2::uuid[])
			AND projection_name = 'issue-spec-marker' AND source_type = 'issue' AND resolved_at IS NULL
	)
	SELECT i.repository_id, i.id, i.number, i.title, i.body, i.state, i.updated_at,
		COALESCE(array_agg(DISTINCT l.name ORDER BY l.name) FILTER (WHERE l.id IS NOT NULL), '{}') AS labels,
		EXISTS (SELECT 1 FROM issue_spec_artifacts a WHERE a.organization_id = i.organization_id
			AND a.repository_id = i.repository_id AND a.issue_id = i.id AND a.active) AS projected
	FROM candidate_issues c
	JOIN issues i ON i.organization_id = c.organization_id AND i.repository_id = c.repository_id AND i.id = c.issue_id
	LEFT JOIN issue_labels il ON il.organization_id = i.organization_id AND il.repository_id = i.repository_id AND il.issue_id = i.id
	LEFT JOIN labels l ON l.organization_id = il.organization_id AND l.repository_id = il.repository_id AND l.id = il.label_id
	GROUP BY i.repository_id, i.id
	ORDER BY i.repository_id, i.number`, orgID, repoIDs)
	if err != nil {
		return nil, nil, fmt.Errorf("changes: load artifact snapshot: %w", err)
	}
	defer rows.Close()
	result := make([]rawArtifact, 0)
	diagnostics := make(map[uuid.UUID][]string)
	for rows.Next() {
		var item rawArtifact
		if err := rows.Scan(&item.repositoryID, &item.issueID, &item.number, &item.title, &item.body,
			&item.state, &item.updatedAt, &item.labels, &item.projected); err != nil {
			return nil, nil, fmt.Errorf("changes: scan artifact: %w", err)
		}
		parsed, ok := parseRawArtifact(item)
		if !ok {
			diagnostics[item.repositoryID] = append(diagnostics[item.repositoryID], AnomalyMalformedIssueMarker)
			continue
		}
		result = append(result, parsed)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("changes: iterate artifacts: %w", err)
	}
	return result, diagnostics, nil
}

func loadTypedArtifacts(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, repoIDs []uuid.UUID) ([]typedArtifact, error) {
	rows, err := tx.Query(ctx, `SELECT repository_id, issue_id, comment_type, comment_key,
		COALESCE(metadata->>'status', ''),
		COALESCE(metadata->'links'->'PR', '[]'::jsonb),
		updated_at
		FROM issue_spec_typed_comments
		WHERE organization_id = $1 AND repository_id = ANY($2::uuid[])
		ORDER BY repository_id, comment_key`, orgID, repoIDs)
	if err != nil {
		return nil, fmt.Errorf("changes: load typed artifact snapshot: %w", err)
	}
	defer rows.Close()
	result := make([]typedArtifact, 0)
	for rows.Next() {
		var item typedArtifact
		var rawLinks json.RawMessage
		if err := rows.Scan(&item.repositoryID, &item.issueID, &item.typ, &item.key, &item.status, &rawLinks, &item.updatedAt); err != nil {
			return nil, fmt.Errorf("changes: scan typed artifact: %w", err)
		}
		item.typ = strings.ToUpper(strings.TrimSpace(item.typ))
		item.status = strings.ToLower(strings.TrimSpace(item.status))
		item.closureLink = hasAcceptedClosureLink(rawLinks)
		result = append(result, item)
	}
	return result, rows.Err()
}

func hasAcceptedClosureLink(raw json.RawMessage) bool {
	var values []string
	if json.Unmarshal(raw, &values) != nil {
		return false
	}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || strings.EqualFold(value, "N/A") || strings.EqualFold(value, "TBD") {
			continue
		}
		// The value already came from the typed comment's semantic `PR` field.
		// Keep the core provider-neutral: GitHub uses /pull/, while GitLab,
		// Aone, Gerrit, and other adapters use different change URL shapes.
		parsed, err := url.Parse(value)
		if err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil &&
			parsed.RawQuery == "" && parsed.Fragment == "" && parsed.Path != "" {
			return true
		}
	}
	return false
}
