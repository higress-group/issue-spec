// Package spa provides the small server-side projection required by the
// embedded application shell. It consumes authz decisions rather than
// duplicating the permission matrix in HTTP handlers or TypeScript.
package spa

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	adminservice "github.com/higress-group/issue-spec/internal/server/admin"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
	"github.com/higress-group/issue-spec/internal/server/authz"
	"github.com/higress-group/issue-spec/internal/server/models"
	"github.com/higress-group/issue-spec/internal/server/publicurl"
	"github.com/higress-group/issue-spec/internal/server/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrInvalidInput = errors.New("spa: invalid input")

type Service struct {
	pool     *pgxpool.Pool
	database *store.Store
	authz    *authz.Service
	origins  *publicurl.Origins
}

func New(database *store.Store, authorization *authz.Service, configuredOrigins ...publicurl.Origins) (*Service, error) {
	if database == nil || database.Pool() == nil || authorization == nil {
		return nil, errors.New("spa: database and authorization service are required")
	}
	service := &Service{pool: database.Pool(), database: database, authz: authorization}
	if len(configuredOrigins) > 0 {
		origins := configuredOrigins[0]
		service.origins = &origins
	}
	return service, nil
}

type UserContext struct {
	ID          uuid.UUID `json:"id"`
	Login       string    `json:"login"`
	DisplayName string    `json:"display_name"`
	Email       *string   `json:"email,omitempty"`
	AvatarURL   string    `json:"avatar_url,omitempty"`
	SiteAdmin   bool      `json:"site_admin"`
}

type CredentialContext struct {
	Kind                 serverauth.CredentialKind `json:"kind"`
	ScopeMode            string                    `json:"scope_mode"`
	Scopes               []string                  `json:"scopes,omitempty"`
	RepositoryRestricted bool                      `json:"repository_restricted"`
	RepositoryCount      int                       `json:"repository_count"`
	AbsoluteExpiresAt    *time.Time                `json:"absolute_expires_at,omitempty"`
	IdleExpiresAt        *time.Time                `json:"idle_expires_at,omitempty"`
}

type SessionContext struct {
	CSRFCookieName string `json:"csrf_cookie_name"`
	CSRFHeaderName string `json:"csrf_header_name"`
}

type OrganizationContext struct {
	ID                  uuid.UUID            `json:"id"`
	Name                string               `json:"name"`
	DisplayName         string               `json:"display_name"`
	EffectivePermission string               `json:"effective_permission"`
	ContainerOnly       bool                 `json:"container_only"`
	AllowedActions      []authz.AccessAction `json:"allowed_actions"`
}

type CurrentContext struct {
	User           UserContext           `json:"user"`
	Credential     CredentialContext     `json:"credential"`
	Session        *SessionContext       `json:"session,omitempty"`
	AllowedActions []authz.AccessAction  `json:"allowed_actions"`
	Organizations  []OrganizationContext `json:"organizations"`
}

func (s *Service) Current(ctx context.Context, principal serverauth.Principal, csrfCookieName string) (CurrentContext, error) {
	subject := authz.Authenticated(principal)
	site, err := s.authz.ResolveSiteAccess(ctx, subject)
	if err != nil {
		return CurrentContext{}, err
	}
	organizations, err := s.authz.ListAccessibleOrganizations(ctx, subject)
	if err != nil {
		return CurrentContext{}, err
	}
	result := CurrentContext{
		User: UserContext{ID: principal.User.ID, Login: principal.User.Login, DisplayName: principal.User.DisplayName,
			Email: principal.User.Email, AvatarURL: s.avatarURL(principal.User.Login), SiteAdmin: site.IdentitySiteAdmin},
		Credential: CredentialContext{Kind: principal.Kind, ScopeMode: "token", Scopes: append([]string(nil), principal.Scopes...),
			RepositoryRestricted: principal.RepoRestricted, RepositoryCount: len(principal.RepositoryCaps)},
		AllowedActions: append([]authz.AccessAction{}, site.AllowedActions...),
		Organizations:  make([]OrganizationContext, 0, len(organizations)),
	}
	if !principal.ExpiresAt.IsZero() {
		expiresAt := principal.ExpiresAt
		result.Credential.AbsoluteExpiresAt = &expiresAt
	}
	if principal.Kind == serverauth.CredentialSession {
		result.Credential.ScopeMode = "identity"
		result.Credential.Scopes = nil
		if !principal.IdleExpiresAt.IsZero() {
			idleExpiresAt := principal.IdleExpiresAt
			result.Credential.IdleExpiresAt = &idleExpiresAt
		}
		result.Session = &SessionContext{CSRFCookieName: csrfCookieName, CSRFHeaderName: "X-CSRF-Token"}
	}
	for _, organization := range organizations {
		result.Organizations = append(result.Organizations, OrganizationContext{
			ID: organization.Organization.ID, Name: organization.Organization.Name,
			DisplayName:         organization.Organization.DisplayName,
			EffectivePermission: organization.EffectivePermission, ContainerOnly: organization.ContainerOnly,
			AllowedActions: append([]authz.AccessAction{}, organization.AllowedActions...),
		})
	}
	return result, nil
}

type RepositoriesContext struct {
	Repositories []authz.RepositoryContextAccess `json:"repositories"`
}

// RepositoryContext is the stable owner/name projection consumed by canonical
// repository web routes. Anonymous callers receive only read authority for a
// public repository; authenticated callers receive the same allowed-actions
// projection used by the signed-in repository chooser.
type RepositoryContext struct {
	Organization  OrganizationContext           `json:"organization"`
	Repository    authz.RepositoryContextAccess `json:"repository"`
	Authenticated bool                          `json:"authenticated"`
}

func (s *Service) Repository(ctx context.Context, subject authz.Subject, owner, name string) (RepositoryContext, error) {
	resource, err := s.database.ResolveRepository(ctx, owner, name)
	if errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrInvalidInput) {
		return RepositoryContext{}, adminservice.ErrNotFound
	}
	if err != nil {
		return RepositoryContext{}, err
	}
	decision, err := s.authz.EvaluateRepository(ctx, subject, authz.RepositoryRequest{
		Scope: resource.Scope, Operation: authz.OperationRead,
	})
	if err != nil {
		return RepositoryContext{}, err
	}
	if !decision.Allowed {
		return RepositoryContext{}, adminservice.ErrNotFound
	}
	organizations, err := s.authz.ListAccessibleOrganizations(ctx, subject)
	if err != nil {
		return RepositoryContext{}, err
	}
	repositories, err := s.authz.ListRepositoryAccess(ctx, subject, models.OrgScope{OrgID: resource.Scope.OrgID})
	if err != nil {
		return RepositoryContext{}, err
	}
	var result RepositoryContext
	for _, organization := range organizations {
		if organization.Organization.ID == resource.Scope.OrgID {
			result.Organization = OrganizationContext{ID: organization.Organization.ID,
				Name: organization.Organization.Name, DisplayName: organization.Organization.DisplayName,
				EffectivePermission: organization.EffectivePermission, ContainerOnly: organization.ContainerOnly,
				AllowedActions: append([]authz.AccessAction{}, organization.AllowedActions...)}
			break
		}
	}
	for _, repository := range repositories {
		if repository.Repository.ID == resource.Scope.RepoID {
			result.Repository = repository
			break
		}
	}
	if result.Organization.ID == uuid.Nil || result.Repository.Repository.ID == uuid.Nil {
		return RepositoryContext{}, adminservice.ErrNotFound
	}
	result.Authenticated = subject.Principal != nil
	return result, nil
}

func (s *Service) Repositories(ctx context.Context, principal serverauth.Principal, orgID uuid.UUID) (RepositoriesContext, error) {
	if orgID == uuid.Nil {
		return RepositoriesContext{}, ErrInvalidInput
	}
	organizations, err := s.authz.ListAccessibleOrganizations(ctx, authz.Authenticated(principal))
	if err != nil {
		return RepositoriesContext{}, err
	}
	visible := false
	for _, organization := range organizations {
		if organization.Organization.ID == orgID {
			visible = true
			break
		}
	}
	if !visible {
		return RepositoriesContext{}, adminservice.ErrNotFound
	}
	repositories, err := s.authz.ListRepositoryAccess(ctx, authz.Authenticated(principal), models.OrgScope{OrgID: orgID})
	if err != nil {
		return RepositoriesContext{}, err
	}
	return RepositoriesContext{Repositories: repositories}, nil
}

type CandidatePurpose string

const (
	PurposeAdministration CandidatePurpose = "administration"
	PurposeMembership     CandidatePurpose = "membership"
	PurposeCollaborator   CandidatePurpose = "collaborator"
	PurposeManagedPAT     CandidatePurpose = "managed_pat"
)

type CandidateMatch string

const (
	MatchPrefix CandidateMatch = "prefix"
	MatchExact  CandidateMatch = "exact"
)

type MembershipSummary struct {
	ID    uuid.UUID              `json:"id"`
	Role  string                 `json:"role"`
	State models.MembershipState `json:"state"`
}

type UserCandidate struct {
	ID               uuid.UUID          `json:"id"`
	Login            string             `json:"login"`
	DisplayName      string             `json:"display_name"`
	AvatarURL        string             `json:"avatar_url,omitempty"`
	Kind             string             `json:"kind"`
	Status           models.UserStatus  `json:"status"`
	Membership       *MembershipSummary `json:"membership,omitempty"`
	ServiceAccountID *uuid.UUID         `json:"service_account_id,omitempty"`
}

type UserCandidates struct {
	Users []UserCandidate `json:"users"`
}

func (s *Service) UserCandidates(ctx context.Context, principal serverauth.Principal, orgID uuid.UUID,
	purpose CandidatePurpose, match CandidateMatch, query string, limit int) (UserCandidates, error) {
	if orgID == uuid.Nil || !purpose.Valid() || !match.Valid() {
		return UserCandidates{}, ErrInvalidInput
	}
	query = strings.TrimSpace(query)
	if (match == MatchExact || purpose == PurposeMembership) && query == "" {
		return UserCandidates{}, ErrInvalidInput
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 50 {
		limit = 50
	}
	action := adminservice.ActionOrganizationAdmin
	if purpose == PurposeManagedPAT {
		action = adminservice.ActionCredentialAdmin
	}
	if err := s.authz.Authorize(ctx, principal, adminservice.AuthorizationRequest{Action: action, OrganizationID: orgID}); err != nil {
		return UserCandidates{}, err
	}

	associated := `(m.id IS NOT NULL OR sa.id IS NOT NULL OR EXISTS (
		SELECT 1 FROM repo_collaborators rc JOIN repos r
			ON r.organization_id = rc.organization_id AND r.id = rc.repository_id
		WHERE rc.organization_id = $1 AND rc.user_id = u.id
			AND rc.archived_at IS NULL AND r.archived_at IS NULL))`
	visibility := associated
	args := []any{orgID}
	externalMatch := ""
	if purpose == PurposeMembership || (purpose == PurposeCollaborator && match == MatchExact) {
		queryArgument := strings.ToLower(query)
		if match == MatchPrefix {
			queryArgument = likePrefix(queryArgument)
		}
		args = append(args, queryArgument)
		if purpose == PurposeMembership && match == MatchPrefix {
			externalMatch = fmt.Sprintf(`(u.login_key LIKE $%[1]d ESCAPE '\' OR lower(COALESCE(u.nickname, u.display_name)) LIKE $%[1]d ESCAPE '\')`, len(args))
		} else if match == MatchPrefix {
			externalMatch = fmt.Sprintf(`u.login_key LIKE $%d ESCAPE '\'`, len(args))
		} else {
			externalMatch = fmt.Sprintf("u.login_key = $%d", len(args))
		}
		visibility = `(` + associated + ` OR (` + externalMatch + ` AND u.status = 'active' AND NOT EXISTS (
			SELECT 1 FROM service_accounts any_sa WHERE any_sa.user_id = u.id)))`
	}
	filters := []string{visibility}
	if externalMatch != "" {
		filters = append(filters, externalMatch)
	} else if match == MatchPrefix && query != "" {
		args = append(args, likePrefix(query))
		filters = append(filters, fmt.Sprintf(`u.login_key LIKE $%d ESCAPE '\'`, len(args)))
	} else if match == MatchExact {
		if len(args) == 1 {
			args = append(args, strings.ToLower(query))
		}
		filters = append(filters, fmt.Sprintf("u.login_key = $%d", len(args)))
	}
	if purpose == PurposeMembership {
		filters = append(filters, "u.status = 'active'", "NOT EXISTS (SELECT 1 FROM service_accounts any_sa WHERE any_sa.user_id = u.id)")
	}
	if purpose == PurposeManagedPAT {
		filters = append(filters, "u.status = 'active'", "((m.id IS NOT NULL AND m.state = 'active') OR (sa.id IS NOT NULL AND sa.disabled_at IS NULL))")
	}
	args = append(args, limit)
	statement := `SELECT u.id, u.login, COALESCE(u.nickname, u.display_name), u.status,
		m.id, m.role, m.state, sa.id
		FROM users u
		LEFT JOIN LATERAL (
			SELECT om.id, om.role, om.state FROM org_memberships om
			WHERE om.organization_id = $1 AND om.user_id = u.id AND om.archived_at IS NULL
			ORDER BY om.created_at DESC, om.id LIMIT 1
		) m ON true
		LEFT JOIN service_accounts sa ON sa.organization_id = $1 AND sa.user_id = u.id
		WHERE ` + strings.Join(filters, " AND ") + fmt.Sprintf(" ORDER BY u.login_key, u.id LIMIT $%d", len(args))
	rows, err := s.pool.Query(ctx, statement, args...)
	if err != nil {
		return UserCandidates{}, fmt.Errorf("spa: list user candidates: %w", err)
	}
	defer rows.Close()
	result := UserCandidates{Users: []UserCandidate{}}
	for rows.Next() {
		var candidate UserCandidate
		var membershipID *uuid.UUID
		var membershipRole *string
		var membershipState *models.MembershipState
		if err := rows.Scan(&candidate.ID, &candidate.Login, &candidate.DisplayName, &candidate.Status,
			&membershipID, &membershipRole, &membershipState, &candidate.ServiceAccountID); err != nil {
			return UserCandidates{}, fmt.Errorf("spa: scan user candidate: %w", err)
		}
		candidate.Kind = "human"
		candidate.AvatarURL = s.avatarURL(candidate.Login)
		if candidate.ServiceAccountID != nil {
			candidate.Kind = "service_account"
		}
		if membershipID != nil && membershipRole != nil && membershipState != nil {
			candidate.Membership = &MembershipSummary{ID: *membershipID, Role: *membershipRole, State: *membershipState}
		}
		result.Users = append(result.Users, candidate)
	}
	if err := rows.Err(); err != nil {
		return UserCandidates{}, fmt.Errorf("spa: iterate user candidates: %w", err)
	}
	return result, nil
}

func likePrefix(query string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(strings.ToLower(query)) + "%"
}

func (s *Service) avatarURL(login string) string {
	if s == nil || s.origins == nil || strings.TrimSpace(login) == "" {
		return ""
	}
	return s.origins.Web.MustURL("/api/v1/avatars/" + url.PathEscape(login))
}

func (p CandidatePurpose) Valid() bool {
	return p == PurposeAdministration || p == PurposeMembership || p == PurposeCollaborator || p == PurposeManagedPAT
}

func (m CandidateMatch) Valid() bool { return m == MatchPrefix || m == MatchExact }
