package changes

import (
	"context"
	"errors"
	"fmt"

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
	artifacts, _, err := loadArtifacts(ctx, tx, scope.OrgID, []uuid.UUID{scope.RepoID})
	if err != nil {
		return LifecycleSnapshot{}, err
	}
	selected := make([]rawArtifact, 0, 3)
	issueIDs := make(map[uuid.UUID]struct{}, 3)
	for _, artifact := range artifacts {
		if artifact.repositoryID == scope.RepoID && artifact.changeKey == changeKey {
			selected = append(selected, artifact)
			issueIDs[artifact.issueID] = struct{}{}
		}
	}
	if len(selected) == 0 {
		return LifecycleSnapshot{}, nil
	}
	typed, err := loadTypedArtifacts(ctx, tx, scope.OrgID, []uuid.UUID{scope.RepoID})
	if err != nil {
		return LifecycleSnapshot{}, err
	}
	selectedTyped := make([]typedArtifact, 0, len(typed))
	for _, item := range typed {
		if _, ok := issueIDs[item.issueID]; ok {
			selectedTyped = append(selectedTyped, item)
		}
	}
	card := buildCard(scope.OrgID, Repository{ID: scope.RepoID}, changeKey, selected, selectedTyped,
		map[uuid.UUID][]models.CodeChangeRelationship{})
	return LifecycleSnapshot{ChangeKey: card.ChangeKey, Lifecycle: card.Lifecycle}, nil
}
