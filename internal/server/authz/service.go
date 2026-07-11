package authz

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	adminservice "github.com/higress-group/issue-spec/internal/server/admin"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
	"github.com/higress-group/issue-spec/internal/server/models"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Service loads live authority facts and evaluates them. It deliberately does
// not cache membership, account status, archive state, or collaborators so a
// lifecycle change is reflected by the next request.
type Service struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) (*Service, error) {
	if pool == nil {
		return nil, errInvalidService
	}
	return &Service{pool: pool}, nil
}

func (s *Service) EvaluateRepository(ctx context.Context, subject Subject, request RepositoryRequest) (Decision, error) {
	if s == nil || s.pool == nil {
		return Decision{}, errInvalidService
	}
	if err := request.Scope.Validate(); err != nil {
		return Decision{}, err
	}
	if !knownOperation(request.Operation) {
		return evaluateRepository(subject, request, authorityFacts{Exists: true}), nil
	}
	facts, err := s.loadRepositoryFacts(ctx, subject, request.Scope)
	if err != nil {
		return Decision{}, err
	}
	return evaluateRepository(subject, request, facts), nil
}

func (s *Service) EvaluateOrganization(ctx context.Context, subject Subject, scope models.OrgScope, operation Operation) (Decision, error) {
	if s == nil || s.pool == nil {
		return Decision{}, errInvalidService
	}
	if err := scope.Validate(); err != nil {
		return Decision{}, err
	}
	facts, err := s.loadOrganizationFacts(ctx, subject, scope)
	if err != nil {
		return Decision{}, err
	}
	return evaluateOrganization(subject, operation, facts), nil
}

// RepositoryAccess is a typed, already-filtered row suitable for list and
// aggregation consumers. It intentionally carries no raw SQL predicate.
type RepositoryAccess struct {
	Scope               models.RepoScope
	EffectivePermission Permission
	Visibility          models.Visibility
	ContributionPolicy  models.ContributionPolicy
}

// FilterReadableRepositories applies authorization before a caller counts,
// groups, or paginates resources. The input remains tenant-composite throughout.
func (s *Service) FilterReadableRepositories(ctx context.Context, subject Subject, scopes []models.RepoScope) ([]RepositoryAccess, error) {
	result := make([]RepositoryAccess, 0, len(scopes))
	for _, scope := range scopes {
		decision, err := s.EvaluateRepository(ctx, subject, RepositoryRequest{Scope: scope, Operation: OperationRead})
		if err != nil {
			return nil, err
		}
		if !decision.Allowed {
			continue
		}
		var access RepositoryAccess
		access.Scope = scope
		access.EffectivePermission = decision.EffectivePermission
		if err := s.pool.QueryRow(ctx, `SELECT visibility, contribution_policy FROM repos
			WHERE organization_id = $1 AND id = $2 AND archived_at IS NULL`, scope.OrgID, scope.RepoID).
			Scan(&access.Visibility, &access.ContributionPolicy); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				continue
			}
			return nil, fmt.Errorf("authz: load filtered repository: %w", err)
		}
		result = append(result, access)
	}
	return result, nil
}

// ListReadableRepositories uses one fixed query to discover candidates, then
// runs the same evaluator used by item routes. It never exposes a composable SQL
// fragment to consumers.
func (s *Service) ListReadableRepositories(ctx context.Context, subject Subject, scope models.OrgScope) ([]RepositoryAccess, error) {
	if s == nil || s.pool == nil {
		return nil, errInvalidService
	}
	if err := scope.Validate(); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT r.id
		FROM repos r JOIN orgs o ON o.id = r.organization_id
		WHERE r.organization_id = $1 AND r.archived_at IS NULL AND o.archived_at IS NULL
		ORDER BY r.updated_at DESC, r.id`, scope.OrgID)
	if err != nil {
		return nil, fmt.Errorf("authz: list repository candidates: %w", err)
	}
	defer rows.Close()
	scopes := make([]models.RepoScope, 0)
	for rows.Next() {
		var repoID uuid.UUID
		if err := rows.Scan(&repoID); err != nil {
			return nil, fmt.Errorf("authz: scan repository candidate: %w", err)
		}
		scopes = append(scopes, models.RepoScope{OrgID: scope.OrgID, RepoID: repoID})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("authz: iterate repository candidates: %w", err)
	}
	rows.Close()
	return s.FilterReadableRepositories(ctx, subject, scopes)
}

// Authorize implements admin.Authorizer and keeps native route guards on the
// same tenant model as compatibility handlers.
func (s *Service) Authorize(ctx context.Context, principal serverauth.Principal, request adminservice.AuthorizationRequest) error {
	subject := Authenticated(principal)
	var (
		decision Decision
		err      error
	)
	switch request.Action {
	case adminservice.ActionSiteAdmin:
		decision, err = s.evaluateSiteAdmin(ctx, subject)
	case adminservice.ActionOrganizationRead:
		decision, err = s.EvaluateOrganization(ctx, subject, models.OrgScope{OrgID: request.OrganizationID}, OperationReadOrganization)
	case adminservice.ActionOrganizationAdmin, adminservice.ActionCredentialAdmin:
		decision, err = s.EvaluateOrganization(ctx, subject, models.OrgScope{OrgID: request.OrganizationID}, OperationAdminOrganization)
	case adminservice.ActionRepositoryRead:
		decision, err = s.EvaluateRepository(ctx, subject, RepositoryRequest{
			Scope: models.RepoScope{OrgID: request.OrganizationID, RepoID: request.RepositoryID}, Operation: OperationRead,
		})
	case adminservice.ActionRepositoryAdmin:
		decision, err = s.EvaluateRepository(ctx, subject, RepositoryRequest{
			Scope: models.RepoScope{OrgID: request.OrganizationID, RepoID: request.RepositoryID}, Operation: OperationAdminRepository,
		})
	default:
		return adminservice.ErrForbidden
	}
	if err != nil {
		return err
	}
	return decision.AuthorizationError()
}

func (s *Service) evaluateSiteAdmin(ctx context.Context, subject Subject) (Decision, error) {
	principal, ok := subjectPrincipal(subject)
	decision := Decision{Exists: true, RequiredPermission: PermissionAdmin, Reason: ReasonInactiveIdentity}
	if !ok {
		return decision, nil
	}
	var active, siteAdmin bool
	err := s.pool.QueryRow(ctx, `SELECT u.status = 'active', EXISTS (
		SELECT 1 FROM site_role_assignments sr WHERE sr.user_id = u.id AND sr.role = 'site_admin'
	) FROM users u WHERE u.id = $1`, principal.User.ID).Scan(&active, &siteAdmin)
	if errors.Is(err, pgx.ErrNoRows) {
		return decision, nil
	}
	if err != nil {
		return Decision{}, fmt.Errorf("authz: load site authority: %w", err)
	}
	decision.Visible = active
	if !active {
		return decision, nil
	}
	cap, reason, allowed := organizationCredentialCap(principal, OperationAdminOrganization)
	decision.EffectivePermission = minPermission(permissionForSiteAdmin(siteAdmin), cap)
	if !allowed {
		decision.Reason = reason
		return decision, nil
	}
	if !siteAdmin || decision.EffectivePermission < PermissionAdmin {
		decision.Reason = ReasonInsufficientPermission
		return decision, nil
	}
	decision.Allowed = true
	decision.Reason = ReasonAllowed
	return decision, nil
}

func permissionForSiteAdmin(siteAdmin bool) Permission {
	if siteAdmin {
		return PermissionAdmin
	}
	return PermissionNone
}

func (s *Service) loadRepositoryFacts(ctx context.Context, subject Subject, scope models.RepoScope) (authorityFacts, error) {
	principal, _ := subjectPrincipal(subject)
	facts := authorityFacts{}
	err := s.pool.QueryRow(ctx, `SELECT
		o.base_permission, r.visibility, r.contribution_policy,
		COALESCE((SELECT u.status = 'active' FROM users u WHERE u.id = $3), false),
		EXISTS (SELECT 1 FROM site_role_assignments sr WHERE sr.user_id = $3 AND sr.role = 'site_admin'),
		EXISTS (SELECT 1 FROM org_memberships om WHERE om.organization_id = o.id AND om.user_id = $3
			AND om.state = 'active' AND om.archived_at IS NULL),
		COALESCE((SELECT om.role FROM org_memberships om WHERE om.organization_id = o.id AND om.user_id = $3
			AND om.state = 'active' AND om.archived_at IS NULL), ''),
		EXISTS (SELECT 1 FROM service_accounts sa WHERE sa.organization_id = o.id AND sa.user_id = $3 AND sa.disabled_at IS NULL),
		COALESCE((SELECT rc.role FROM repo_collaborators rc WHERE rc.organization_id = o.id AND rc.repository_id = r.id
			AND rc.user_id = $3 AND rc.archived_at IS NULL), '')
	FROM repos r JOIN orgs o ON o.id = r.organization_id
	WHERE r.organization_id = $1 AND r.id = $2 AND r.archived_at IS NULL AND o.archived_at IS NULL`,
		scope.OrgID, scope.RepoID, principal.User.ID).Scan(
		&facts.BasePermission, &facts.Visibility, &facts.ContributionPolicy,
		&facts.IdentityActive, &facts.SiteAdmin, &facts.OrganizationMember,
		&facts.OrganizationRole, &facts.ServiceAccount, &facts.CollaboratorRole,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return authorityFacts{}, nil
	}
	if err != nil {
		return authorityFacts{}, fmt.Errorf("authz: load repository authority: %w", err)
	}
	facts.Exists = true
	return facts, nil
}

func (s *Service) loadOrganizationFacts(ctx context.Context, subject Subject, scope models.OrgScope) (authorityFacts, error) {
	principal, _ := subjectPrincipal(subject)
	facts := authorityFacts{}
	err := s.pool.QueryRow(ctx, `SELECT
		o.base_permission,
		COALESCE((SELECT u.status = 'active' FROM users u WHERE u.id = $2), false),
		EXISTS (SELECT 1 FROM site_role_assignments sr WHERE sr.user_id = $2 AND sr.role = 'site_admin'),
		EXISTS (SELECT 1 FROM org_memberships om WHERE om.organization_id = o.id AND om.user_id = $2
			AND om.state = 'active' AND om.archived_at IS NULL),
		COALESCE((SELECT om.role FROM org_memberships om WHERE om.organization_id = o.id AND om.user_id = $2
			AND om.state = 'active' AND om.archived_at IS NULL), ''),
		EXISTS (SELECT 1 FROM service_accounts sa WHERE sa.organization_id = o.id AND sa.user_id = $2 AND sa.disabled_at IS NULL)
	FROM orgs o WHERE o.id = $1 AND o.archived_at IS NULL`, scope.OrgID, principal.User.ID).Scan(
		&facts.BasePermission, &facts.IdentityActive, &facts.SiteAdmin,
		&facts.OrganizationMember, &facts.OrganizationRole, &facts.ServiceAccount,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return authorityFacts{}, nil
	}
	if err != nil {
		return authorityFacts{}, fmt.Errorf("authz: load organization authority: %w", err)
	}
	facts.Exists = true
	return facts, nil
}
