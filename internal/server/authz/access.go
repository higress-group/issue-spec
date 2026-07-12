package authz

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
	"github.com/higress-group/issue-spec/internal/server/models"
	"github.com/jackc/pgx/v5"
)

// AccessAction is the stable, frontend-safe spelling of an authorization
// decision. Conditional operations such as runner trigger and evidence publish
// are deliberately omitted because they require an additional policy fact.
type AccessAction string

const (
	AccessSiteAdmin          AccessAction = "site.admin"
	AccessOrganizationRead   AccessAction = "organization.read"
	AccessOrganizationAdmin  AccessAction = "organization.admin"
	AccessCredentialAdmin    AccessAction = "credential.admin"
	AccessRead               AccessAction = "read"
	AccessContribute         AccessAction = "contribute"
	AccessTriage             AccessAction = "triage"
	AccessWrite              AccessAction = "write"
	AccessManageIntegrations AccessAction = "integrations.manage"
	AccessRepositoryAdmin    AccessAction = "repository.admin"
)

type SiteAccess struct {
	IdentitySiteAdmin bool           `json:"identity_site_admin"`
	AllowedActions    []AccessAction `json:"allowed_actions"`
}

type OrganizationSummary struct {
	ID          uuid.UUID `json:"id"`
	Name        string    `json:"name"`
	DisplayName string    `json:"display_name"`
}

type OrganizationAccess struct {
	Organization        OrganizationSummary `json:"organization"`
	EffectivePermission string              `json:"effective_permission"`
	AllowedActions      []AccessAction      `json:"allowed_actions"`
	ContainerOnly       bool                `json:"container_only"`
}

type RepositorySummary struct {
	ID                 uuid.UUID                 `json:"id"`
	OrganizationID     uuid.UUID                 `json:"organization_id"`
	Name               string                    `json:"name"`
	DisplayName        string                    `json:"display_name"`
	Visibility         models.Visibility         `json:"visibility"`
	ContributionPolicy models.ContributionPolicy `json:"contribution_policy"`
}

type RepositoryContextAccess struct {
	Repository          RepositorySummary `json:"repository"`
	EffectivePermission string            `json:"effective_permission"`
	AllowedActions      []AccessAction    `json:"allowed_actions"`
}

// IdentitySiteAdmin reports the live identity assignment independently of the
// presented credential's scopes. /user uses this property, while navigation
// must use ResolveSiteAccess so token caps still apply.
func (s *Service) IdentitySiteAdmin(ctx context.Context, principal serverauth.Principal) (bool, error) {
	if s == nil || s.pool == nil {
		return false, errInvalidService
	}
	if principal.User.ID == uuid.Nil {
		return false, nil
	}
	var active, siteAdmin bool
	err := s.pool.QueryRow(ctx, `SELECT u.status = 'active', EXISTS (
		SELECT 1 FROM site_role_assignments sr WHERE sr.user_id = u.id AND sr.role = 'site_admin'
	) FROM users u WHERE u.id = $1`, principal.User.ID).Scan(&active, &siteAdmin)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("authz: load site identity: %w", err)
	}
	return active && siteAdmin, nil
}

func (s *Service) ResolveSiteAccess(ctx context.Context, subject Subject) (SiteAccess, error) {
	principal, ok := subjectPrincipal(subject)
	if !ok {
		return SiteAccess{}, nil
	}
	identitySiteAdmin, err := s.IdentitySiteAdmin(ctx, principal)
	if err != nil {
		return SiteAccess{}, err
	}
	result := SiteAccess{IdentitySiteAdmin: identitySiteAdmin, AllowedActions: []AccessAction{}}
	decision, err := s.evaluateSiteAdmin(ctx, subject)
	if err != nil {
		return SiteAccess{}, err
	}
	if decision.Allowed {
		result.AllowedActions = append(result.AllowedActions, AccessSiteAdmin)
	}
	return result, nil
}

// ListAccessibleOrganizations returns only active organization containers that
// the subject may read directly or that own at least one readable repository.
// The latter are marked container-only so callers expose only navigation data.
func (s *Service) ListAccessibleOrganizations(ctx context.Context, subject Subject) ([]OrganizationAccess, error) {
	if s == nil || s.pool == nil {
		return nil, errInvalidService
	}
	rows, err := s.pool.Query(ctx, `SELECT id, name, display_name, description, base_permission,
		representation_version, repositories_collection_version, members_collection_version,
		archived_at, created_at, updated_at
		FROM orgs WHERE archived_at IS NULL ORDER BY lower(name), id`)
	if err != nil {
		return nil, fmt.Errorf("authz: list organization candidates: %w", err)
	}
	defer rows.Close()
	organizations := make([]models.AdminOrganization, 0)
	for rows.Next() {
		var organization models.AdminOrganization
		if err := rows.Scan(&organization.ID, &organization.Name, &organization.DisplayName,
			&organization.Description, &organization.BasePermission, &organization.RepresentationVersion,
			&organization.RepositoriesCollectionVersion, &organization.MembersCollectionVersion,
			&organization.ArchivedAt, &organization.CreatedAt, &organization.UpdatedAt); err != nil {
			return nil, fmt.Errorf("authz: scan organization candidate: %w", err)
		}
		organizations = append(organizations, organization)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("authz: iterate organization candidates: %w", err)
	}

	result := make([]OrganizationAccess, 0, len(organizations))
	for _, organization := range organizations {
		scope := models.OrgScope{OrgID: organization.ID}
		readDecision, err := s.EvaluateOrganization(ctx, subject, scope, OperationReadOrganization)
		if err != nil {
			return nil, err
		}
		access := OrganizationAccess{Organization: OrganizationSummary{ID: organization.ID, Name: organization.Name,
			DisplayName: organization.DisplayName}, EffectivePermission: readDecision.EffectivePermission.String(),
			AllowedActions: []AccessAction{}}
		if readDecision.Allowed {
			access.AllowedActions = append(access.AllowedActions, AccessOrganizationRead)
		} else {
			repositories, err := s.ListReadableRepositories(ctx, subject, scope)
			if err != nil {
				return nil, err
			}
			if len(repositories) == 0 {
				continue
			}
			access.ContainerOnly = true
		}
		adminDecision, err := s.EvaluateOrganization(ctx, subject, scope, OperationAdminOrganization)
		if err != nil {
			return nil, err
		}
		if adminDecision.Allowed {
			access.EffectivePermission = adminDecision.EffectivePermission.String()
			access.AllowedActions = append(access.AllowedActions, AccessOrganizationAdmin, AccessCredentialAdmin)
		}
		result = append(result, access)
	}
	return result, nil
}

func (s *Service) ListRepositoryAccess(ctx context.Context, subject Subject, scope models.OrgScope) ([]RepositoryContextAccess, error) {
	readable, err := s.ListReadableRepositories(ctx, subject, scope)
	if err != nil {
		return nil, err
	}
	result := make([]RepositoryContextAccess, 0, len(readable))
	for _, item := range readable {
		var repository models.AdminRepository
		err := s.pool.QueryRow(ctx, `SELECT id, organization_id, name, display_name, description, visibility,
			default_branch, contribution_policy, representation_version, collaborators_collection_version,
			archived_at, created_at, updated_at FROM repos
			WHERE organization_id = $1 AND id = $2 AND archived_at IS NULL`, item.Scope.OrgID, item.Scope.RepoID).
			Scan(&repository.ID, &repository.OrganizationID, &repository.Name, &repository.DisplayName,
				&repository.Description, &repository.Visibility, &repository.DefaultBranch,
				&repository.ContributionPolicy, &repository.RepresentationVersion,
				&repository.CollaboratorsCollectionVersion, &repository.ArchivedAt,
				&repository.CreatedAt, &repository.UpdatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("authz: load repository access: %w", err)
		}
		repository.Scope = item.Scope
		access := RepositoryContextAccess{Repository: RepositorySummary{ID: repository.ID,
			OrganizationID: repository.OrganizationID, Name: repository.Name, DisplayName: repository.DisplayName,
			Visibility: repository.Visibility, ContributionPolicy: repository.ContributionPolicy},
			EffectivePermission: item.EffectivePermission.String(),
			AllowedActions:      []AccessAction{AccessRead}}
		for _, operation := range []struct {
			operation Operation
			action    AccessAction
		}{
			{OperationContribute, AccessContribute},
			{OperationTriage, AccessTriage},
			{OperationWrite, AccessWrite},
			{OperationManageIntegrations, AccessManageIntegrations},
			{OperationAdminRepository, AccessRepositoryAdmin},
		} {
			decision, err := s.EvaluateRepository(ctx, subject, RepositoryRequest{Scope: item.Scope, Operation: operation.operation})
			if err != nil {
				return nil, err
			}
			if decision.Allowed {
				access.AllowedActions = append(access.AllowedActions, operation.action)
				if decision.EffectivePermission > item.EffectivePermission {
					access.EffectivePermission = decision.EffectivePermission.String()
				}
			}
		}
		result = append(result, access)
	}
	return result, nil
}
