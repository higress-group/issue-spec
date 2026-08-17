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
	"github.com/higress-group/issue-spec/internal/model"
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

// EnsureBinding reuses an identical active coordinate, creates one only when
// absent, and refuses to replace incompatible authority implicitly.
func (s *Service) EnsureBinding(ctx context.Context, subject authz.Subject, actor adminservice.Actor, scope models.RepoScope, input CreateBindingVersionInput) (EnsureBindingResult, error) {
	input = normalizeBindingInput(input)
	if err := validateActor(subject, actor); err != nil {
		return EnsureBindingResult{}, err
	}
	if err := validateBindingInput(input); err != nil {
		return EnsureBindingResult{}, err
	}
	var result EnsureBindingResult
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		decision, err := s.authz.EvaluateRepositoryTx(ctx, tx, subject, authz.RepositoryRequest{Scope: scope, Operation: authz.OperationManageIntegrations})
		if err != nil {
			return err
		}
		if err := decision.AuthorizationError(); err != nil {
			return err
		}
		existing, scanErr := scanBinding(tx.QueryRow(ctx, `SELECT id, organization_id, repository_id, provider_key,
			external_repository_id, clone_url, web_url, default_branch, version, active, created_at, updated_at
			FROM source_bindings WHERE organization_id = $1 AND repository_id = $2 AND active FOR UPDATE`, scope.OrgID, scope.RepoID))
		if scanErr == nil {
			if !bindingCoordinatesEqual(existing, input) {
				return adminservice.ErrConflict
			}
			result = EnsureBindingResult{Binding: existing, Created: false}
			return nil
		}
		if !errors.Is(scanErr, adminservice.ErrNotFound) {
			return scanErr
		}
		var priorVersion int64
		if err := tx.QueryRow(ctx, `SELECT COALESCE(max(version), 0) FROM source_bindings
			WHERE organization_id = $1 AND repository_id = $2`, scope.OrgID, scope.RepoID).Scan(&priorVersion); err != nil {
			return err
		}
		created, err := scanBinding(tx.QueryRow(ctx, `INSERT INTO source_bindings
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
		result = EnsureBindingResult{Binding: created, Created: true}
		if err := bumpCollection(ctx, tx, scope, "bindings_collection_version"); err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, scope, created.ID, "source_binding.ensure.create", "source_binding", map[string]any{
			"provider_key": input.ProviderKey, "external_repository_id": input.ExternalRepositoryID, "version": created.Version,
		})
	})
	return result, mapError(err)
}

func bindingCoordinatesEqual(binding Binding, input CreateBindingVersionInput) bool {
	return binding.ProviderKey == input.ProviderKey && binding.ExternalRepositoryID == input.ExternalRepositoryID &&
		binding.CloneURL == input.CloneURL && binding.WebURL == input.WebURL && binding.DefaultBranch == input.DefaultBranch
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
// filtered before return; repository-visible records, including their metadata,
// are visible to every caller with repository read permission.
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
		items = append(items, item)
	}
	return items, rows.Err()
}

// UpsertReference uses the full provider/relation/external-repository/object
// identity, so identically numbered objects in different external repos never
// alias. A semantic no-op leaves versions and collection validators unchanged.
// Active code_change writes on valid Implement Issues additionally serialize on
// the issue row and enforce the issue's single-active relationship lifecycle.
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
		changed := false
		if activeCodeChangeInput(input) {
			implement, lockErr := lockIssueAndCheckImplement(ctx, tx, scope, input.IssueID)
			if lockErr != nil {
				return lockErr
			}
			if implement {
				result, changed, err = establishCodeChangeReference(ctx, tx, scope, input, decision.EffectivePermission)
			} else {
				result, changed, err = upsertGenericReference(ctx, tx, scope, input)
			}
		} else {
			if err := ensureIssue(ctx, tx, scope, input.IssueID); err != nil {
				return err
			}
			result, changed, err = upsertGenericReference(ctx, tx, scope, input)
		}
		if err != nil {
			return err
		}
		if !changed {
			return nil
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

func upsertGenericReference(ctx context.Context, tx pgx.Tx, scope models.RepoScope, input UpsertReferenceInput) (Reference, bool, error) {
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
			return existing, false, nil
		}
		result, err := scanReference(tx.QueryRow(ctx, `UPDATE external_references SET canonical_url = $8,
			title = $9, lifecycle_state = $10, visibility = $11, metadata = $12::jsonb,
			representation_version = representation_version + 1, updated_at = clock_timestamp()
			WHERE organization_id = $1 AND repository_id = $2 AND issue_id = $3 AND provider_key = $4
			AND relation_kind = $5 AND external_repository_id = $6 AND external_id = $7
			RETURNING id, organization_id, repository_id, issue_id, provider_key, relation_kind,
			external_repository_id, external_id, canonical_url, title, lifecycle_state, visibility,
			metadata, representation_version, created_at, updated_at`, scope.OrgID, scope.RepoID,
			input.IssueID, input.ProviderKey, input.RelationKind, input.ExternalRepositoryID, input.ExternalID,
			input.CanonicalURL, input.Title, input.LifecycleState, input.Visibility, string(input.Metadata)))
		return result, err == nil, err
	case errors.Is(scanErr, pgx.ErrNoRows):
		result, err := insertReference(ctx, tx, scope, input)
		return result, err == nil, err
	default:
		return Reference{}, false, scanErr
	}
}

func lockIssueAndCheckImplement(ctx context.Context, tx pgx.Tx, scope models.RepoScope, issueID uuid.UUID) (bool, error) {
	var found uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM issues WHERE organization_id = $1
		AND repository_id = $2 AND id = $3 FOR UPDATE`, scope.OrgID, scope.RepoID, issueID).Scan(&found); err != nil {
		return false, err
	}
	var implement bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM issue_spec_artifacts
		WHERE organization_id = $1 AND repository_id = $2 AND issue_id = $3
		AND artifact_type = 'implement' AND active AND metadata->>'marker_version' = '1')`,
		scope.OrgID, scope.RepoID, issueID).Scan(&implement)
	return implement, err
}

func establishCodeChangeReference(ctx context.Context, tx pgx.Tx, scope models.RepoScope, input UpsertReferenceInput, permission authz.Permission) (Reference, bool, error) {
	incomingRevision, ok := headRevision(input.Metadata)
	if !ok {
		return Reference{}, false, adminservice.ErrInvalidInput
	}
	rows, err := tx.Query(ctx, `SELECT id, organization_id, repository_id, issue_id, provider_key,
		relation_kind, external_repository_id, external_id, canonical_url, title, lifecycle_state,
		visibility, metadata, representation_version, created_at, updated_at FROM external_references
		WHERE organization_id = $1 AND repository_id = $2 AND issue_id = $3
		AND relation_kind = 'code_change' AND lifecycle_state = 'active'
		ORDER BY provider_key, external_repository_id, external_id, id FOR UPDATE`,
		scope.OrgID, scope.RepoID, input.IssueID)
	if err != nil {
		return Reference{}, false, err
	}
	defer rows.Close()
	active := make([]Reference, 0, 2)
	for rows.Next() {
		item, scanErr := scanReferenceFields(rows)
		if scanErr != nil {
			return Reference{}, false, scanErr
		}
		active = append(active, item)
	}
	if err := rows.Err(); err != nil {
		return Reference{}, false, err
	}
	if len(active) == 0 {
		result, insertErr := insertReference(ctx, tx, scope, input)
		return result, insertErr == nil, insertErr
	}
	if permission < authz.PermissionMaintain {
		for _, reference := range active {
			if reference.Visibility == VisibilityMaintainers {
				return Reference{}, false, codeChangeConflict(permission, CodeChangeConflictHiddenActiveReferences, active...)
			}
		}
	}
	if len(active) > 1 {
		return Reference{}, false, codeChangeConflict(permission, CodeChangeConflictAmbiguousActiveReferences, active...)
	}
	existing := active[0]
	if validateHTTPSURL(existing.CanonicalURL) != nil {
		return Reference{}, false, codeChangeConflict(permission, CodeChangeConflictInvalidActiveReference, existing)
	}
	existingRevision, ok := headRevision(existing.Metadata)
	if !ok {
		return Reference{}, false, codeChangeConflict(permission, CodeChangeConflictInvalidActiveReference, existing)
	}
	if existing.ProviderKey != input.ProviderKey || existing.ExternalRepositoryID != input.ExternalRepositoryID ||
		existing.ExternalID != input.ExternalID {
		return Reference{}, false, codeChangeConflict(permission, CodeChangeConflictDifferentActiveChange, existing)
	}
	if existingRevision == incomingRevision {
		if existing.CanonicalURL != input.CanonicalURL {
			return Reference{}, false, codeChangeConflict(permission, CodeChangeConflictCanonicalURLDrift, existing)
		}
		return existing, false, nil
	}
	if !input.Refresh {
		return Reference{}, false, codeChangeConflict(permission, CodeChangeConflictRefreshRequired, existing)
	}
	if input.ExpectedVersion == nil || *input.ExpectedVersion != existing.RepresentationVersion {
		return Reference{}, false, codeChangeConflict(permission, CodeChangeConflictStaleReferenceVersion, existing)
	}
	result, err := scanReference(tx.QueryRow(ctx, `UPDATE external_references SET canonical_url = $5,
		title = $6, lifecycle_state = $7, visibility = $8, metadata = $9::jsonb,
		representation_version = representation_version + 1, updated_at = clock_timestamp()
		WHERE organization_id = $1 AND repository_id = $2 AND issue_id = $3 AND id = $4
		AND representation_version = $10
		RETURNING id, organization_id, repository_id, issue_id, provider_key, relation_kind,
		external_repository_id, external_id, canonical_url, title, lifecycle_state, visibility,
		metadata, representation_version, created_at, updated_at`, scope.OrgID, scope.RepoID,
		input.IssueID, existing.ID, input.CanonicalURL, input.Title, input.LifecycleState,
		input.Visibility, string(input.Metadata), *input.ExpectedVersion))
	if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, adminservice.ErrNotFound) {
		return Reference{}, false, codeChangeConflict(permission, CodeChangeConflictStaleReferenceVersion, existing)
	}
	return result, err == nil, err
}

func insertReference(ctx context.Context, tx pgx.Tx, scope models.RepoScope, input UpsertReferenceInput) (Reference, error) {
	return scanReference(tx.QueryRow(ctx, `INSERT INTO external_references
		(id, organization_id, repository_id, issue_id, provider_key, relation_kind,
		 external_repository_id, external_id, canonical_url, title, lifecycle_state, visibility, metadata)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13::jsonb)
		RETURNING id, organization_id, repository_id, issue_id, provider_key, relation_kind,
		external_repository_id, external_id, canonical_url, title, lifecycle_state, visibility,
		metadata, representation_version, created_at, updated_at`, uuid.New(), scope.OrgID, scope.RepoID,
		input.IssueID, input.ProviderKey, input.RelationKind, input.ExternalRepositoryID, input.ExternalID,
		input.CanonicalURL, input.Title, input.LifecycleState, input.Visibility, string(input.Metadata)))
}

func activeCodeChangeInput(input UpsertReferenceInput) bool {
	return input.RelationKind == "code_change" && input.LifecycleState == "active"
}

func headRevision(metadata json.RawMessage) (string, bool) {
	var value struct {
		HeadRevision string `json:"head_revision"`
	}
	if json.Unmarshal(metadata, &value) != nil || value.HeadRevision == "" || value.HeadRevision != strings.TrimSpace(value.HeadRevision) {
		return "", false
	}
	return value.HeadRevision, true
}

func codeChangeConflict(permission authz.Permission, reason CodeChangeConflictReason, references ...Reference) error {
	if permission < authz.PermissionMaintain {
		for _, reference := range references {
			if reference.Visibility == VisibilityMaintainers {
				return &CodeChangeConflictError{Reason: CodeChangeConflictHiddenActiveReferences}
			}
		}
	}
	identities := make([]ReferenceIdentity, 0, len(references))
	for _, reference := range references {
		identities = append(identities, ReferenceIdentity{ID: reference.ID, ProviderKey: reference.ProviderKey,
			ExternalRepositoryID: reference.ExternalRepositoryID, ExternalID: reference.ExternalID,
			RepresentationVersion: reference.RepresentationVersion})
	}
	return &CodeChangeConflictError{Reason: reason, References: identities}
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
	if input.Refresh != (input.ExpectedVersion != nil) || (input.ExpectedVersion != nil && *input.ExpectedVersion < 1) {
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
	item, err := scanReferenceFields(row)
	if err != nil {
		return Reference{}, err
	}
	if validateHTTPSURL(item.CanonicalURL) != nil {
		return Reference{}, errors.New("bindings: stored external reference contains an unsafe canonical URL")
	}
	return item, nil
}

func scanReferenceFields(row rowScanner) (Reference, error) {
	var item Reference
	err := row.Scan(&item.ID, &item.Scope.OrgID, &item.Scope.RepoID, &item.IssueID,
		&item.ProviderKey, &item.RelationKind, &item.ExternalRepositoryID, &item.ExternalID,
		&item.CanonicalURL, &item.Title, &item.LifecycleState, &item.Visibility, &item.Metadata,
		&item.RepresentationVersion, &item.CreatedAt, &item.UpdatedAt)
	if err != nil {
		return Reference{}, err
	}
	return item, nil
}

func referenceEqual(existing Reference, input UpsertReferenceInput) bool {
	existingMetadata, err := canonicalObject(existing.Metadata)
	if err != nil {
		return false
	}
	return existing.CanonicalURL == input.CanonicalURL && equalOptionalTitle(existing.Title, input.Title) &&
		existing.LifecycleState == input.LifecycleState && existing.Visibility == input.Visibility &&
		bytes.Equal(existingMetadata, input.Metadata)
}

func equalOptional(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func equalOptionalTitle(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return model.CanonicalTitle(*left) == model.CanonicalTitle(*right)
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
