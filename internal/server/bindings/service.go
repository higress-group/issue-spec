package bindings

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	pathpkg "path"
	"strings"
	"unicode"

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
		return nil, errors.New("bindings: database and authorization are required")
	}
	return &Service{pool: pool, authz: authorization}, nil
}

// ActiveBinding returns the active, credential-free source binding visible to
// the caller. Missing and invisible repositories are deliberately identical.
func (s *Service) ActiveBinding(ctx context.Context, subject authz.Subject, scope models.RepoScope) (Binding, error) {
	decision, err := s.authz.EvaluateRepository(ctx, subject, authz.RepositoryRequest{Scope: scope, Operation: authz.OperationRead})
	if err != nil {
		return Binding{}, err
	}
	if err := decision.AuthorizationError(); err != nil {
		return Binding{}, err
	}
	return scanBinding(s.pool.QueryRow(ctx, `SELECT id, organization_id, repository_id, provider_key,
		external_repository_id, clone_url, web_url, default_branch, version, active, created_at, updated_at
		FROM source_bindings WHERE organization_id = $1 AND repository_id = $2 AND active`, scope.OrgID, scope.RepoID))
}

// CreateBindingVersion serializes on the repository row, deactivates only the
// prior active marker, and appends version N+1 with audit and collection bump
// in the same transaction. Historical source coordinates never change.
func (s *Service) CreateBindingVersion(ctx context.Context, subject authz.Subject, actor adminservice.Actor, scope models.RepoScope, input CreateBindingVersionInput) (Binding, error) {
	input = normalizeBindingInput(input)
	if err := validateActor(subject, actor); err != nil {
		return Binding{}, err
	}
	if err := validateBindingInput(input); err != nil {
		return Binding{}, err
	}
	var created Binding
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		decision, err := s.authz.EvaluateRepositoryTx(ctx, tx, subject, authz.RepositoryRequest{Scope: scope, Operation: authz.OperationManageIntegrations})
		if err != nil {
			return err
		}
		if err := decision.AuthorizationError(); err != nil {
			return err
		}
		var priorVersion int64
		err = tx.QueryRow(ctx, `SELECT COALESCE(max(version), 0) FROM source_bindings
			WHERE organization_id = $1 AND repository_id = $2`, scope.OrgID, scope.RepoID).Scan(&priorVersion)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE source_bindings SET active = false, updated_at = clock_timestamp()
			WHERE organization_id = $1 AND repository_id = $2 AND active`, scope.OrgID, scope.RepoID); err != nil {
			return err
		}
		created, err = scanBinding(tx.QueryRow(ctx, `INSERT INTO source_bindings
			(id, organization_id, repository_id, provider_key, external_repository_id,
			 clone_url, web_url, default_branch, version, active, created_by_user_id, updated_by_user_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, true, $10, $10)
			RETURNING id, organization_id, repository_id, provider_key, external_repository_id,
			 clone_url, web_url, default_branch, version, active, created_at, updated_at`, uuid.New(), scope.OrgID,
			scope.RepoID, input.ProviderKey, input.ExternalRepositoryID, input.CloneURL, input.WebURL,
			input.DefaultBranch, priorVersion+1, actor.UserID))
		if err != nil {
			return err
		}
		if err := bumpCollection(ctx, tx, scope, "bindings_collection_version"); err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, scope, created.ID, "source_binding.version.create", "source_binding", map[string]any{
			"provider_key": input.ProviderKey, "external_repository_id": input.ExternalRepositoryID, "version": created.Version,
		})
	})
	return created, mapError(err)
}

// DeactivateBinding disables the current active version without rewriting its
// source coordinates. Repeating the operation after deactivation is a 404 and
// does not advance the collection validator.
func (s *Service) DeactivateBinding(ctx context.Context, subject authz.Subject, actor adminservice.Actor, scope models.RepoScope) error {
	if err := validateActor(subject, actor); err != nil {
		return err
	}
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		decision, err := s.authz.EvaluateRepositoryTx(ctx, tx, subject, authz.RepositoryRequest{Scope: scope, Operation: authz.OperationManageIntegrations})
		if err != nil {
			return err
		}
		if err := decision.AuthorizationError(); err != nil {
			return err
		}
		var bindingID uuid.UUID
		if err := tx.QueryRow(ctx, `UPDATE source_bindings SET active = false, updated_at = clock_timestamp()
			WHERE organization_id = $1 AND repository_id = $2 AND active RETURNING id`, scope.OrgID, scope.RepoID).
			Scan(&bindingID); err != nil {
			return err
		}
		if err := bumpCollection(ctx, tx, scope, "bindings_collection_version"); err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, scope, bindingID, "source_binding.deactivate", "source_binding", nil)
	})
	return mapError(err)
}

// ListReferences returns tenant-scoped references. Maintainer-only records are
// filtered before return and metadata is redacted below maintain permission.
func (s *Service) ListReferences(ctx context.Context, subject authz.Subject, scope models.RepoScope, issueID uuid.UUID) ([]Reference, error) {
	if issueID == uuid.Nil {
		return nil, adminservice.ErrInvalidInput
	}
	decision, err := s.authz.EvaluateRepository(ctx, subject, authz.RepositoryRequest{Scope: scope, Operation: authz.OperationRead})
	if err != nil {
		return nil, err
	}
	if err := decision.AuthorizationError(); err != nil {
		return nil, err
	}
	if err := ensureIssue(ctx, s.pool, scope, issueID); err != nil {
		return nil, mapError(err)
	}
	query := `SELECT id, organization_id, repository_id, issue_id, provider_key, relation_kind,
		external_repository_id, external_id, canonical_url, title, lifecycle_state, visibility,
		metadata, representation_version, created_at, updated_at FROM external_references
		WHERE organization_id = $1 AND repository_id = $2 AND issue_id = $3`
	if decision.EffectivePermission < authz.PermissionMaintain {
		query += ` AND visibility = 'repository'`
	}
	query += ` ORDER BY updated_at DESC, id`
	rows, err := s.pool.Query(ctx, query, scope.OrgID, scope.RepoID, issueID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]Reference, 0)
	for rows.Next() {
		item, scanErr := scanReference(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		if decision.EffectivePermission < authz.PermissionMaintain {
			item.Metadata = nil
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// UpsertReference uses the full provider/relation/external-repository/object
// identity, so identically numbered objects in different external repos never
// alias. A semantic no-op leaves versions and collection validators unchanged.
func (s *Service) UpsertReference(ctx context.Context, subject authz.Subject, actor adminservice.Actor, scope models.RepoScope, input UpsertReferenceInput) (Reference, error) {
	input = normalizeReferenceInput(input)
	canonicalMetadata, err := canonicalObject(input.Metadata)
	if err != nil || validateActor(subject, actor) != nil || validateReferenceInput(input) != nil {
		return Reference{}, adminservice.ErrInvalidInput
	}
	input.Metadata = canonicalMetadata
	var result Reference
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		decision, err := s.authz.EvaluateRepositoryTx(ctx, tx, subject, authz.RepositoryRequest{Scope: scope, Operation: authz.OperationWrite})
		if err != nil {
			return err
		}
		if err := decision.AuthorizationError(); err != nil {
			return err
		}
		if err := ensureIssue(ctx, tx, scope, input.IssueID); err != nil {
			return err
		}
		row := tx.QueryRow(ctx, `SELECT id, organization_id, repository_id, issue_id, provider_key,
			relation_kind, external_repository_id, external_id, canonical_url, title, lifecycle_state,
			visibility, metadata, representation_version, created_at, updated_at FROM external_references
			WHERE organization_id = $1 AND repository_id = $2 AND issue_id = $3 AND provider_key = $4
			AND relation_kind = $5 AND external_repository_id = $6 AND external_id = $7 FOR UPDATE`,
			scope.OrgID, scope.RepoID, input.IssueID, input.ProviderKey, input.RelationKind,
			input.ExternalRepositoryID, input.ExternalID)
		existing, scanErr := scanReference(row)
		switch {
		case scanErr == nil:
			if referenceEqual(existing, input) {
				result = existing
				return nil
			}
			result, err = scanReference(tx.QueryRow(ctx, `UPDATE external_references SET canonical_url = $8,
				title = $9, lifecycle_state = $10, visibility = $11, metadata = $12::jsonb,
				representation_version = representation_version + 1, updated_at = clock_timestamp()
				WHERE organization_id = $1 AND repository_id = $2 AND issue_id = $3 AND provider_key = $4
				AND relation_kind = $5 AND external_repository_id = $6 AND external_id = $7
				RETURNING id, organization_id, repository_id, issue_id, provider_key, relation_kind,
				external_repository_id, external_id, canonical_url, title, lifecycle_state, visibility,
				metadata, representation_version, created_at, updated_at`, scope.OrgID, scope.RepoID,
				input.IssueID, input.ProviderKey, input.RelationKind, input.ExternalRepositoryID, input.ExternalID,
				input.CanonicalURL, input.Title, input.LifecycleState, input.Visibility, string(input.Metadata)))
		case errors.Is(scanErr, pgx.ErrNoRows):
			result, err = scanReference(tx.QueryRow(ctx, `INSERT INTO external_references
				(id, organization_id, repository_id, issue_id, provider_key, relation_kind,
				 external_repository_id, external_id, canonical_url, title, lifecycle_state, visibility, metadata)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13::jsonb)
				RETURNING id, organization_id, repository_id, issue_id, provider_key, relation_kind,
				external_repository_id, external_id, canonical_url, title, lifecycle_state, visibility,
				metadata, representation_version, created_at, updated_at`, uuid.New(), scope.OrgID, scope.RepoID,
				input.IssueID, input.ProviderKey, input.RelationKind, input.ExternalRepositoryID, input.ExternalID,
				input.CanonicalURL, input.Title, input.LifecycleState, input.Visibility, string(input.Metadata)))
		default:
			return scanErr
		}
		if err != nil {
			return err
		}
		if err := bumpReferenceCollections(ctx, tx, scope, input.IssueID); err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, scope, result.ID, "external_reference.upsert", "external_reference", map[string]any{
			"provider_key": input.ProviderKey, "relation_kind": input.RelationKind,
			"external_repository_id": input.ExternalRepositoryID, "external_id": input.ExternalID,
			"visibility": input.Visibility,
		})
	})
	return result, mapError(err)
}

// DeleteReference removes a mutable link while keeping its safe audit record.
func (s *Service) DeleteReference(ctx context.Context, subject authz.Subject, actor adminservice.Actor, scope models.RepoScope, issueID, referenceID uuid.UUID) error {
	if issueID == uuid.Nil || referenceID == uuid.Nil || validateActor(subject, actor) != nil {
		return adminservice.ErrInvalidInput
	}
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		decision, err := s.authz.EvaluateRepositoryTx(ctx, tx, subject, authz.RepositoryRequest{Scope: scope, Operation: authz.OperationWrite})
		if err != nil {
			return err
		}
		if err := decision.AuthorizationError(); err != nil {
			return err
		}
		var provider, externalRepositoryID, externalID string
		err = tx.QueryRow(ctx, `DELETE FROM external_references WHERE organization_id = $1
			AND repository_id = $2 AND issue_id = $3 AND id = $4
			RETURNING provider_key, external_repository_id, external_id`, scope.OrgID, scope.RepoID,
			issueID, referenceID).Scan(&provider, &externalRepositoryID, &externalID)
		if err != nil {
			return err
		}
		if err := bumpReferenceCollections(ctx, tx, scope, issueID); err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, scope, referenceID, "external_reference.delete", "external_reference", map[string]any{
			"provider_key": provider, "external_repository_id": externalRepositoryID, "external_id": externalID,
		})
	})
	return mapError(err)
}

func normalizeBindingInput(input CreateBindingVersionInput) CreateBindingVersionInput {
	input.ProviderKey = strings.TrimSpace(input.ProviderKey)
	input.ExternalRepositoryID = strings.TrimSpace(input.ExternalRepositoryID)
	// Persisted URLs are validated byte-for-byte. Do not trim them into a
	// different accepted value: surrounding whitespace/control data is an
	// invalid credential-bearing coordinate, not harmless presentation input.
	input.DefaultBranch = strings.TrimSpace(input.DefaultBranch)
	return input
}

func validateBindingInput(input CreateBindingVersionInput) error {
	if input.ProviderKey == "" || input.ExternalRepositoryID == "" || input.DefaultBranch == "" {
		return adminservice.ErrInvalidInput
	}
	if err := validateCloneURL(input.CloneURL); err != nil {
		return err
	}
	return validateHTTPSURL(input.WebURL)
}

func validateCloneURL(raw string) error {
	return validatePersistedURL(raw, true)
}

func validateHTTPSURL(raw string) error {
	return validatePersistedURL(raw, false)
}

func validatePersistedURL(raw string, allowSSH bool) error {
	if raw == "" || raw != strings.TrimSpace(raw) || strings.Contains(raw, "\\") ||
		strings.IndexFunc(raw, unicode.IsControl) >= 0 || strings.ContainsAny(raw, "?#") {
		return adminservice.ErrInvalidInput
	}
	parsed, err := url.Parse(raw)
	validScheme := parsed != nil && parsed.Scheme == "https"
	if allowSSH && parsed != nil && parsed.Scheme == "ssh" {
		validScheme = true
	}
	if err != nil || !validScheme || !parsed.IsAbs() || parsed.Host == "" || parsed.Hostname() == "" ||
		parsed.User != nil || parsed.Opaque != "" || parsed.RawQuery != "" || parsed.ForceQuery ||
		parsed.Fragment != "" || parsed.RawFragment != "" || (parsed.Path != "" && !strings.HasPrefix(parsed.Path, "/")) ||
		parsed.Host != strings.ToLower(parsed.Host) || strings.HasSuffix(parsed.Hostname(), ".") ||
		(parsed.Scheme == "https" && parsed.Port() == "443") || parsed.String() != raw ||
		strings.IndexFunc(parsed.Path, unicode.IsControl) >= 0 {
		return adminservice.ErrInvalidInput
	}
	if parsed.Path != "" {
		cleanPath := pathpkg.Clean(parsed.Path)
		if strings.HasSuffix(parsed.Path, "/") && parsed.Path != "/" {
			cleanPath += "/"
		}
		if cleanPath != parsed.Path {
			return adminservice.ErrInvalidInput
		}
	}
	return nil
}

func normalizeReferenceInput(input UpsertReferenceInput) UpsertReferenceInput {
	input.ProviderKey = strings.TrimSpace(input.ProviderKey)
	input.RelationKind = strings.TrimSpace(input.RelationKind)
	input.ExternalRepositoryID = strings.TrimSpace(input.ExternalRepositoryID)
	input.ExternalID = strings.TrimSpace(input.ExternalID)
	input.LifecycleState = strings.TrimSpace(input.LifecycleState)
	if input.LifecycleState == "" {
		input.LifecycleState = "active"
	}
	if input.Visibility == "" {
		input.Visibility = VisibilityRepository
	}
	if input.Title != nil {
		value := strings.TrimSpace(*input.Title)
		input.Title = &value
	}
	return input
}

func validateReferenceInput(input UpsertReferenceInput) error {
	if input.IssueID == uuid.Nil || input.ProviderKey == "" || input.RelationKind == "" ||
		input.ExternalRepositoryID == "" || input.ExternalID == "" || input.LifecycleState == "" ||
		(input.Visibility != VisibilityRepository && input.Visibility != VisibilityMaintainers) {
		return adminservice.ErrInvalidInput
	}
	return validateHTTPSURL(input.CanonicalURL)
}

func validateActor(subject authz.Subject, actor adminservice.Actor) error {
	if subject.Principal == nil || subject.Principal.User.ID == uuid.Nil || actor.UserID != subject.Principal.User.ID ||
		strings.TrimSpace(actor.IdentityKey) == "" || strings.TrimSpace(actor.RequestID) == "" {
		return adminservice.ErrInvalidInput
	}
	return nil
}

type rowScanner interface{ Scan(...any) error }

func scanBinding(row rowScanner) (Binding, error) {
	var item Binding
	err := row.Scan(&item.ID, &item.Scope.OrgID, &item.Scope.RepoID, &item.ProviderKey,
		&item.ExternalRepositoryID, &item.CloneURL, &item.WebURL, &item.DefaultBranch,
		&item.Version, &item.Active, &item.CreatedAt, &item.UpdatedAt)
	if err != nil {
		return Binding{}, mapError(err)
	}
	if validateCloneURL(item.CloneURL) != nil || validateHTTPSURL(item.WebURL) != nil {
		return Binding{}, errors.New("bindings: stored source binding contains an unsafe external URL")
	}
	return item, nil
}

func scanReference(row rowScanner) (Reference, error) {
	var item Reference
	err := row.Scan(&item.ID, &item.Scope.OrgID, &item.Scope.RepoID, &item.IssueID,
		&item.ProviderKey, &item.RelationKind, &item.ExternalRepositoryID, &item.ExternalID,
		&item.CanonicalURL, &item.Title, &item.LifecycleState, &item.Visibility, &item.Metadata,
		&item.RepresentationVersion, &item.CreatedAt, &item.UpdatedAt)
	if err != nil {
		return Reference{}, err
	}
	if validateHTTPSURL(item.CanonicalURL) != nil {
		return Reference{}, errors.New("bindings: stored external reference contains an unsafe canonical URL")
	}
	return item, nil
}

func referenceEqual(existing Reference, input UpsertReferenceInput) bool {
	existingMetadata, err := canonicalObject(existing.Metadata)
	if err != nil {
		return false
	}
	return existing.CanonicalURL == input.CanonicalURL && equalOptional(existing.Title, input.Title) &&
		existing.LifecycleState == input.LifecycleState && existing.Visibility == input.Visibility &&
		bytes.Equal(existingMetadata, input.Metadata)
}

func equalOptional(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func canonicalObject(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return json.RawMessage(`{}`), nil
	}
	var object map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&object); err != nil || object == nil {
		return nil, adminservice.ErrInvalidInput
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, adminservice.ErrInvalidInput
	}
	return json.Marshal(object)
}

type queryRower interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func ensureIssue(ctx context.Context, db queryRower, scope models.RepoScope, issueID uuid.UUID) error {
	var found uuid.UUID
	return db.QueryRow(ctx, `SELECT id FROM issues WHERE organization_id = $1 AND repository_id = $2 AND id = $3`,
		scope.OrgID, scope.RepoID, issueID).Scan(&found)
}

func bumpCollection(ctx context.Context, tx pgx.Tx, scope models.RepoScope, column string) error {
	switch column {
	case "bindings_collection_version", "references_collection_version":
	default:
		return errors.New("bindings: unsupported collection")
	}
	tag, err := tx.Exec(ctx, `UPDATE repos SET `+column+` = `+column+` + 1, updated_at = clock_timestamp()
		WHERE organization_id = $1 AND id = $2`, scope.OrgID, scope.RepoID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return pgx.ErrNoRows
	}
	return nil
}

func bumpReferenceCollections(ctx context.Context, tx pgx.Tx, scope models.RepoScope, issueID uuid.UUID) error {
	tag, err := tx.Exec(ctx, `UPDATE issues SET references_collection_version = references_collection_version + 1,
		updated_at = clock_timestamp() WHERE organization_id = $1 AND repository_id = $2 AND id = $3`,
		scope.OrgID, scope.RepoID, issueID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return pgx.ErrNoRows
	}
	return bumpCollection(ctx, tx, scope, "references_collection_version")
}

func insertAudit(ctx context.Context, tx pgx.Tx, actor adminservice.Actor, scope models.RepoScope, resourceID uuid.UUID, action, resourceType string, metadata any) error {
	if metadata == nil {
		metadata = map[string]any{}
	}
	payload, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO audit_events
		(id, organization_id, repository_id, actor_user_id, actor_identity_key, action,
		 resource_type, resource_id, request_id, metadata)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10::jsonb)`, uuid.New(), scope.OrgID,
		scope.RepoID, actor.UserID, actor.IdentityKey, action, resourceType, resourceID,
		actor.RequestID, string(payload))
	return err
}

func mapError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return adminservice.ErrNotFound
	}
	return fmt.Errorf("bindings: %w", err)
}
