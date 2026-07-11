package authz

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

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
	facts, err := s.loadRepositoryFacts(ctx, s.pool, subject, request.Scope)
	if err != nil {
		return Decision{}, err
	}
	return evaluateRepository(subject, request, facts), nil
}

// EvaluateRepositoryTx evaluates and locks the authority rows in the caller's
// mutation transaction. A concurrent revocation therefore linearizes either
// before the decision (and denies) or after the protected mutation commits.
func (s *Service) EvaluateRepositoryTx(ctx context.Context, tx pgx.Tx, subject Subject, request RepositoryRequest) (Decision, error) {
	if s == nil || s.pool == nil || tx == nil {
		return Decision{}, errInvalidService
	}
	if err := request.Scope.Validate(); err != nil {
		return Decision{}, err
	}
	if !knownOperation(request.Operation) {
		return evaluateRepository(subject, request, authorityFacts{Exists: true}), nil
	}
	if err := lockRepositoryAuthority(ctx, tx, subject, request.Scope); err != nil {
		return Decision{}, err
	}
	facts, err := s.loadRepositoryFacts(ctx, tx, subject, request.Scope)
	if err != nil {
		return Decision{}, err
	}
	valid, reason, err := lockAndValidateCredential(ctx, tx, subject, request.Scope)
	if err != nil {
		return Decision{}, err
	}
	if !valid {
		visible := facts.Exists && reason != ReasonRepositoryCap
		return Decision{Exists: facts.Exists, Visible: visible, Reason: reason}, nil
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
	facts, err := s.loadOrganizationFacts(ctx, s.pool, subject, scope)
	if err != nil {
		return Decision{}, err
	}
	return evaluateOrganization(subject, operation, facts), nil
}

func (s *Service) EvaluateOrganizationTx(ctx context.Context, tx pgx.Tx, subject Subject, scope models.OrgScope, operation Operation) (Decision, error) {
	if s == nil || s.pool == nil || tx == nil {
		return Decision{}, errInvalidService
	}
	if err := scope.Validate(); err != nil {
		return Decision{}, err
	}
	if err := lockOrganizationAuthority(ctx, tx, subject, scope); err != nil {
		return Decision{}, err
	}
	facts, err := s.loadOrganizationFacts(ctx, tx, subject, scope)
	if err != nil {
		return Decision{}, err
	}
	valid, reason, err := lockAndValidateOrganizationCredential(ctx, tx, subject)
	if err != nil {
		return Decision{}, err
	}
	if !valid {
		visible := facts.Exists && reason != ReasonRepositoryCap
		return Decision{Exists: facts.Exists, Visible: visible, Reason: reason}, nil
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

type queryRower interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func (s *Service) loadRepositoryFacts(ctx context.Context, db queryRower, subject Subject, scope models.RepoScope) (authorityFacts, error) {
	principal, _ := subjectPrincipal(subject)
	facts := authorityFacts{}
	err := db.QueryRow(ctx, `SELECT
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

func (s *Service) loadOrganizationFacts(ctx context.Context, db queryRower, subject Subject, scope models.OrgScope) (authorityFacts, error) {
	principal, _ := subjectPrincipal(subject)
	facts := authorityFacts{}
	err := db.QueryRow(ctx, `SELECT
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

func lockRepositoryAuthority(ctx context.Context, tx pgx.Tx, subject Subject, scope models.RepoScope) error {
	if _, err := tx.Exec(ctx, `SELECT o.id FROM orgs o JOIN repos r ON r.organization_id = o.id
		WHERE o.id = $1 AND r.id = $2 FOR UPDATE OF r FOR SHARE OF o`, scope.OrgID, scope.RepoID); err != nil {
		return fmt.Errorf("authz: lock repository authority: %w", err)
	}
	principal, ok := subjectPrincipal(subject)
	if !ok {
		return nil
	}
	queries := []struct {
		query string
		args  []any
	}{
		{`SELECT id FROM users WHERE id = $1 FOR SHARE`, []any{principal.User.ID}},
		{`SELECT id FROM org_memberships WHERE organization_id = $1 AND user_id = $2 FOR SHARE`, []any{scope.OrgID, principal.User.ID}},
		{`SELECT id FROM repo_collaborators WHERE organization_id = $1 AND repository_id = $2 AND user_id = $3 FOR SHARE`, []any{scope.OrgID, scope.RepoID, principal.User.ID}},
		{`SELECT id FROM site_role_assignments WHERE user_id = $1 FOR SHARE`, []any{principal.User.ID}},
		{`SELECT id FROM service_accounts WHERE organization_id = $1 AND user_id = $2 FOR SHARE`, []any{scope.OrgID, principal.User.ID}},
	}
	for _, item := range queries {
		rows, err := tx.Query(ctx, item.query, item.args...)
		if err != nil {
			return fmt.Errorf("authz: lock identity authority: %w", err)
		}
		rows.Close()
	}
	return nil
}

func lockOrganizationAuthority(ctx context.Context, tx pgx.Tx, subject Subject, scope models.OrgScope) error {
	if _, err := tx.Exec(ctx, `SELECT id FROM orgs WHERE id = $1 FOR SHARE`, scope.OrgID); err != nil {
		return fmt.Errorf("authz: lock organization authority: %w", err)
	}
	principal, ok := subjectPrincipal(subject)
	if !ok {
		return nil
	}
	queries := []struct {
		query string
		args  []any
	}{
		{`SELECT id FROM users WHERE id = $1 FOR SHARE`, []any{principal.User.ID}},
		{`SELECT id FROM org_memberships WHERE organization_id = $1 AND user_id = $2 FOR SHARE`, []any{scope.OrgID, principal.User.ID}},
		{`SELECT id FROM site_role_assignments WHERE user_id = $1 FOR SHARE`, []any{principal.User.ID}},
		{`SELECT id FROM service_accounts WHERE organization_id = $1 AND user_id = $2 FOR SHARE`, []any{scope.OrgID, principal.User.ID}},
	}
	for _, item := range queries {
		rows, err := tx.Query(ctx, item.query, item.args...)
		if err != nil {
			return fmt.Errorf("authz: lock organization identity authority: %w", err)
		}
		rows.Close()
	}
	return nil
}

func lockAndValidateOrganizationCredential(ctx context.Context, tx pgx.Tx, subject Subject) (bool, Reason, error) {
	principal, ok := subjectPrincipal(subject)
	if !ok {
		return false, ReasonCredentialScope, nil
	}
	if principal.Kind == serverauth.CredentialDelegated || principal.RepoRestricted {
		return false, ReasonRepositoryCap, nil
	}
	if principal.Kind == serverauth.CredentialRecovery {
		return principal.HasScope("site:admin"), ReasonCredentialScope, nil
	}
	if principal.Kind == serverauth.CredentialSession {
		return lockAndValidateSession(ctx, tx, principal)
	}
	if principal.Kind != serverauth.CredentialPAT || principal.CredentialID == uuid.Nil {
		return false, ReasonCredentialScope, nil
	}
	now := time.Now().UTC()
	var active bool
	err := tx.QueryRow(ctx, `SELECT user_id = $2 AND revoked_at IS NULL
		AND (expires_at IS NULL OR expires_at > $3) FROM personal_access_tokens
		WHERE id = $1 FOR SHARE`, principal.CredentialID, principal.User.ID, now).Scan(&active)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ReasonCredentialScope, nil
	}
	if err != nil || !active {
		return false, ReasonCredentialScope, err
	}
	scopes, repositories, err := lockPATCaps(ctx, tx, principal.CredentialID)
	if err != nil {
		return false, ReasonCredentialScope, err
	}
	if len(repositories) != 0 || !equalStringSet(scopes, principal.Scopes) {
		return false, ReasonRepositoryCap, nil
	}
	return true, ReasonAllowed, nil
}

func lockAndValidateSession(ctx context.Context, tx pgx.Tx, principal serverauth.Principal) (bool, Reason, error) {
	if principal.CredentialID == uuid.Nil {
		return false, ReasonCredentialScope, nil
	}
	var active bool
	now := time.Now().UTC()
	err := tx.QueryRow(ctx, `SELECT user_id = $2 AND revoked_at IS NULL
		AND idle_expires_at > $3 AND absolute_expires_at > $3
		FROM sessions WHERE id = $1 FOR SHARE`, principal.CredentialID, principal.User.ID, now).Scan(&active)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ReasonCredentialScope, nil
	}
	return active, ReasonCredentialScope, err
}

func lockAndValidateCredential(ctx context.Context, tx pgx.Tx, subject Subject, scope models.RepoScope) (bool, Reason, error) {
	principal, ok := subjectPrincipal(subject)
	if !ok {
		return true, ReasonAllowed, nil
	}
	now := time.Now().UTC()
	switch principal.Kind {
	case serverauth.CredentialSession:
		if principal.CredentialID == uuid.Nil {
			return false, ReasonCredentialScope, nil
		}
		var active bool
		err := tx.QueryRow(ctx, `SELECT user_id = $2 AND revoked_at IS NULL
			AND idle_expires_at > $3 AND absolute_expires_at > $3
			FROM sessions WHERE id = $1 FOR SHARE`, principal.CredentialID, principal.User.ID, now).Scan(&active)
		if errors.Is(err, pgx.ErrNoRows) {
			return false, ReasonCredentialScope, nil
		}
		return active, ReasonCredentialScope, err
	case serverauth.CredentialPAT:
		if principal.CredentialID == uuid.Nil {
			return false, ReasonCredentialScope, nil
		}
		var active bool
		err := tx.QueryRow(ctx, `SELECT user_id = $2 AND revoked_at IS NULL
			AND (expires_at IS NULL OR expires_at > $3)
			FROM personal_access_tokens WHERE id = $1 FOR SHARE`, principal.CredentialID, principal.User.ID, now).Scan(&active)
		if errors.Is(err, pgx.ErrNoRows) {
			return false, ReasonCredentialScope, nil
		}
		if err != nil {
			return false, ReasonCredentialScope, err
		}
		if !active {
			return false, ReasonCredentialScope, nil
		}
		scopes, repositories, err := lockPATCaps(ctx, tx, principal.CredentialID)
		if err != nil {
			return false, ReasonCredentialScope, err
		}
		if !equalStringSet(scopes, principal.Scopes) {
			return false, ReasonCredentialScope, nil
		}
		restricted := len(repositories) > 0
		if restricted != principal.RepoRestricted {
			return false, ReasonRepositoryCap, nil
		}
		if restricted {
			allowed := false
			for _, cap := range repositories {
				if cap.OrgID == scope.OrgID && cap.RepoID == scope.RepoID {
					allowed = true
				}
			}
			if !allowed {
				return false, ReasonRepositoryCap, nil
			}
		}
		return true, ReasonAllowed, nil
	case serverauth.CredentialDelegated:
		if principal.CredentialID == uuid.Nil {
			return false, ReasonCredentialScope, nil
		}
		var userID, orgID, repoID uuid.UUID
		var parentID *uuid.UUID
		var expiresAt time.Time
		var revokedAt *time.Time
		var claims []byte
		err := tx.QueryRow(ctx, `SELECT user_id, personal_access_token_id, organization_id,
			repository_id, expires_at, revoked_at, claims FROM delegated_tokens
			WHERE id = $1 FOR SHARE`, principal.CredentialID).
			Scan(&userID, &parentID, &orgID, &repoID, &expiresAt, &revokedAt, &claims)
		if errors.Is(err, pgx.ErrNoRows) {
			return false, ReasonCredentialScope, nil
		}
		if err != nil {
			return false, ReasonCredentialScope, err
		}
		if userID != principal.User.ID || revokedAt != nil || !now.Before(expiresAt) {
			return false, ReasonCredentialScope, nil
		}
		if orgID != scope.OrgID || repoID != scope.RepoID {
			return false, ReasonRepositoryCap, nil
		}
		var payload struct {
			Scopes []string `json:"scopes"`
		}
		if json.Unmarshal(claims, &payload) != nil || !equalStringSet(payload.Scopes, principal.Scopes) {
			return false, ReasonCredentialScope, nil
		}
		if parentID != nil {
			var parentActive bool
			err = tx.QueryRow(ctx, `SELECT user_id = $2 AND revoked_at IS NULL
				AND (expires_at IS NULL OR expires_at > $3) FROM personal_access_tokens
				WHERE id = $1 FOR SHARE`, *parentID, principal.User.ID, now).Scan(&parentActive)
			if errors.Is(err, pgx.ErrNoRows) {
				return false, ReasonCredentialScope, nil
			}
			if err != nil {
				return false, ReasonCredentialScope, err
			}
			if !parentActive {
				return false, ReasonCredentialScope, nil
			}
		}
		return true, ReasonAllowed, nil
	case serverauth.CredentialRecovery:
		return principal.HasScope("site:admin"), ReasonCredentialScope, nil
	default:
		return false, ReasonCredentialScope, nil
	}
}

func lockPATCaps(ctx context.Context, tx pgx.Tx, credentialID uuid.UUID) ([]string, []serverauth.RepositoryCap, error) {
	scopeRows, err := tx.Query(ctx, `SELECT scope FROM pat_scopes WHERE personal_access_token_id = $1 FOR SHARE`, credentialID)
	if err != nil {
		return nil, nil, err
	}
	var scopes []string
	for scopeRows.Next() {
		var scope string
		if err := scopeRows.Scan(&scope); err != nil {
			scopeRows.Close()
			return nil, nil, err
		}
		scopes = append(scopes, scope)
	}
	if err := scopeRows.Err(); err != nil {
		scopeRows.Close()
		return nil, nil, err
	}
	scopeRows.Close()
	repoRows, err := tx.Query(ctx, `SELECT organization_id, repository_id FROM pat_repositories
		WHERE personal_access_token_id = $1 FOR SHARE`, credentialID)
	if err != nil {
		return nil, nil, err
	}
	var repositories []serverauth.RepositoryCap
	for repoRows.Next() {
		var cap serverauth.RepositoryCap
		if err := repoRows.Scan(&cap.OrgID, &cap.RepoID); err != nil {
			repoRows.Close()
			return nil, nil, err
		}
		repositories = append(repositories, cap)
	}
	if err := repoRows.Err(); err != nil {
		repoRows.Close()
		return nil, nil, err
	}
	repoRows.Close()
	return scopes, repositories, nil
}

func equalStringSet(left, right []string) bool {
	left, right = append([]string(nil), left...), append([]string(nil), right...)
	sort.Strings(left)
	sort.Strings(right)
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
