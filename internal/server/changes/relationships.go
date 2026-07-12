package changes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	pathpkg "path"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	adminservice "github.com/higress-group/issue-spec/internal/server/admin"
	"github.com/higress-group/issue-spec/internal/server/authz"
	"github.com/higress-group/issue-spec/internal/server/models"
	"github.com/jackc/pgx/v5"
)

type sourceBindingIdentity struct {
	providerKey          string
	externalRepositoryID string
	updatedAt            time.Time
}

type relationshipSnapshot struct {
	scope             models.RepoScope
	bindingVersion    int64
	referencesVersion int64
	updatedAt         time.Time
}

// IssueRelationships resolves a canonical repository/issue route and projects
// only active code_change references visible to the caller. It deliberately
// does not expose the internal issue UUID to browser clients.
func (s *Service) IssueRelationships(ctx context.Context, subject authz.Subject, owner, repository string, number int64) (IssueRelationships, error) {
	owner, repository = strings.TrimSpace(owner), strings.TrimSpace(repository)
	if owner == "" || repository == "" || number <= 0 {
		return IssueRelationships{}, adminservice.ErrInvalidInput
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return IssueRelationships{}, fmt.Errorf("changes: begin relationship snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var repositoryState relationshipSnapshot
	err = tx.QueryRow(ctx, `SELECT o.id, r.id, r.bindings_collection_version,
		r.references_collection_version, r.updated_at
		FROM orgs o JOIN repos r ON r.organization_id = o.id
		WHERE o.name_key = lower($1) AND r.name_key = lower($2)
		AND o.archived_at IS NULL AND r.archived_at IS NULL`, owner, repository).
		Scan(&repositoryState.scope.OrgID, &repositoryState.scope.RepoID, &repositoryState.bindingVersion,
			&repositoryState.referencesVersion, &repositoryState.updatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return IssueRelationships{}, adminservice.ErrNotFound
	}
	if err != nil {
		return IssueRelationships{}, fmt.Errorf("changes: resolve relationship repository: %w", err)
	}
	decision, err := s.authz.EvaluateRepositoryTx(ctx, tx, subject, authz.RepositoryRequest{
		Scope: repositoryState.scope, Operation: authz.OperationRead,
	})
	if err != nil {
		return IssueRelationships{}, err
	}
	if err := decision.AuthorizationError(); err != nil {
		return IssueRelationships{}, err
	}
	var issueID uuid.UUID
	var issueReferencesVersion int64
	var issueUpdatedAt time.Time
	err = tx.QueryRow(ctx, `SELECT id, references_collection_version, updated_at FROM issues
		WHERE organization_id = $1 AND repository_id = $2 AND number = $3`, repositoryState.scope.OrgID,
		repositoryState.scope.RepoID, number).Scan(&issueID, &issueReferencesVersion, &issueUpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return IssueRelationships{}, adminservice.ErrNotFound
	}
	if err != nil {
		return IssueRelationships{}, fmt.Errorf("changes: resolve relationship issue: %w", err)
	}
	permissions := map[uuid.UUID]authz.Permission{repositoryState.scope.RepoID: decision.EffectivePermission}
	projected, relationshipModified, err := loadCodeChangeRelationships(ctx, tx, repositoryState.scope.OrgID,
		[]uuid.UUID{repositoryState.scope.RepoID}, []uuid.UUID{issueID}, permissions)
	if err != nil {
		return IssueRelationships{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return IssueRelationships{}, fmt.Errorf("changes: commit relationship snapshot: %w", err)
	}
	lastModified := repositoryState.updatedAt
	for _, candidate := range []time.Time{issueUpdatedAt, relationshipModified} {
		if candidate.After(lastModified) {
			lastModified = candidate
		}
	}
	return IssueRelationships{Relationships: append(make([]models.CodeChangeRelationship, 0, len(projected[issueID])), projected[issueID]...),
		Validator: relationshipValidator(repositoryState, issueID, issueReferencesVersion), LastModified: lastModified}, nil
}

func relationshipValidator(repository relationshipSnapshot, issueID uuid.UUID, issueReferencesVersion int64) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%d|%d|%d", repository.scope.RepoID, issueID,
		repository.bindingVersion, repository.referencesVersion, issueReferencesVersion)))
	return `"` + hex.EncodeToString(digest[:]) + `"`
}

func loadCodeChangeRelationships(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, repoIDs, issueIDs []uuid.UUID,
	permissions map[uuid.UUID]authz.Permission) (map[uuid.UUID][]models.CodeChangeRelationship, time.Time, error) {
	result := make(map[uuid.UUID][]models.CodeChangeRelationship, len(issueIDs))
	for _, issueID := range issueIDs {
		result[issueID] = []models.CodeChangeRelationship{}
	}
	if len(repoIDs) == 0 || len(issueIDs) == 0 {
		return result, time.Time{}, nil
	}
	bindings, lastModified, err := loadActiveSourceBindings(ctx, tx, orgID, repoIDs)
	if err != nil {
		return nil, time.Time{}, err
	}
	rows, err := tx.Query(ctx, `SELECT repository_id, issue_id, provider_key, relation_kind,
		external_repository_id, external_id, canonical_url, title, lifecycle_state, visibility,
		metadata, updated_at
		FROM external_references
		WHERE organization_id = $1 AND repository_id = ANY($2::uuid[])
		AND issue_id = ANY($3::uuid[]) AND relation_kind = 'code_change' AND lifecycle_state = 'active'
		ORDER BY updated_at DESC, id`, orgID, repoIDs, issueIDs)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("changes: load code-change relationships: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var repositoryID, issueID uuid.UUID
		var relationship models.CodeChangeRelationship
		var visibility string
		var metadata json.RawMessage
		var updatedAt time.Time
		if err := rows.Scan(&repositoryID, &issueID, &relationship.ProviderKey, &relationship.RelationKind,
			&relationship.ExternalRepositoryID, &relationship.ExternalID, &relationship.CanonicalURL,
			&relationship.Title, &relationship.LifecycleState, &visibility, &metadata, &updatedAt); err != nil {
			return nil, time.Time{}, fmt.Errorf("changes: scan code-change relationship: %w", err)
		}
		permission := permissions[repositoryID]
		if visibility == "maintainers" && permission < authz.PermissionMaintain {
			continue
		}
		if visibility != "repository" && visibility != "maintainers" {
			return nil, time.Time{}, errors.New("changes: stored code-change relationship has invalid visibility")
		}
		if !safeCanonicalHTTPSURL(relationship.CanonicalURL) {
			return nil, time.Time{}, errors.New("changes: stored code-change relationship has unsafe canonical URL")
		}
		if permission >= authz.PermissionMaintain {
			relationship.Metadata = append(json.RawMessage(nil), metadata...)
		}
		relationship.SourceBindingMatch = models.SourceBindingUnbound
		if binding, ok := bindings[repositoryID]; ok {
			if binding.providerKey == relationship.ProviderKey && binding.externalRepositoryID == relationship.ExternalRepositoryID {
				relationship.SourceBindingMatch = models.SourceBindingMatched
			} else {
				relationship.SourceBindingMatch = models.SourceBindingMismatched
			}
		}
		result[issueID] = append(result[issueID], relationship)
		if updatedAt.After(lastModified) {
			lastModified = updatedAt
		}
	}
	if err := rows.Err(); err != nil {
		return nil, time.Time{}, fmt.Errorf("changes: iterate code-change relationships: %w", err)
	}
	return result, lastModified, nil
}

func loadActiveSourceBindings(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, repoIDs []uuid.UUID) (map[uuid.UUID]sourceBindingIdentity, time.Time, error) {
	rows, err := tx.Query(ctx, `SELECT repository_id, provider_key, external_repository_id, updated_at
		FROM source_bindings WHERE organization_id = $1 AND repository_id = ANY($2::uuid[]) AND active`, orgID, repoIDs)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("changes: load active source bindings: %w", err)
	}
	defer rows.Close()
	result := make(map[uuid.UUID]sourceBindingIdentity, len(repoIDs))
	lastModified := time.Time{}
	for rows.Next() {
		var repositoryID uuid.UUID
		var binding sourceBindingIdentity
		if err := rows.Scan(&repositoryID, &binding.providerKey, &binding.externalRepositoryID, &binding.updatedAt); err != nil {
			return nil, time.Time{}, fmt.Errorf("changes: scan active source binding: %w", err)
		}
		result[repositoryID] = binding
		if binding.updatedAt.After(lastModified) {
			lastModified = binding.updatedAt
		}
	}
	return result, lastModified, rows.Err()
}

func safeCanonicalHTTPSURL(raw string) bool {
	if raw == "" || raw != strings.TrimSpace(raw) || strings.Contains(raw, "\\") ||
		strings.IndexFunc(raw, unicode.IsControl) >= 0 || strings.ContainsAny(raw, "?#") {
		return false
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || !parsed.IsAbs() || parsed.Host == "" || parsed.Hostname() == "" ||
		parsed.User != nil || parsed.Opaque != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" ||
		parsed.RawFragment != "" || (parsed.Path != "" && !strings.HasPrefix(parsed.Path, "/")) ||
		parsed.Host != strings.ToLower(parsed.Host) || strings.HasSuffix(parsed.Hostname(), ".") || parsed.Port() == "443" ||
		parsed.String() != raw || strings.IndexFunc(parsed.Path, unicode.IsControl) >= 0 {
		return false
	}
	if parsed.Path == "" {
		return true
	}
	cleanPath := pathpkg.Clean(parsed.Path)
	if strings.HasSuffix(parsed.Path, "/") && parsed.Path != "/" {
		cleanPath += "/"
	}
	return cleanPath == parsed.Path
}
