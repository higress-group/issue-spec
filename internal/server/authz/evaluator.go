package authz

import (
	"github.com/google/uuid"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
	"github.com/higress-group/issue-spec/internal/server/models"
)

type authorityFacts struct {
	Exists             bool
	BasePermission     models.BasePermission
	Visibility         models.Visibility
	ContributionPolicy models.ContributionPolicy
	IdentityActive     bool
	SiteAdmin          bool
	OrganizationMember bool
	OrganizationRole   string
	ServiceAccount     bool
	CollaboratorRole   string
}

func evaluateRepository(subject Subject, request RepositoryRequest, facts authorityFacts) Decision {
	required, supported := requiredPermission(request.Operation)
	decision := Decision{
		Exists:             facts.Exists,
		RequiredPermission: required,
		Reason:             ReasonNotFound,
	}
	if !facts.Exists {
		return decision
	}
	if !supported {
		decision.Reason = ReasonUnsupportedOperation
		return decision
	}

	principal, authenticated := subjectPrincipal(subject)
	if authenticated && !facts.IdentityActive {
		decision.Reason = ReasonInactiveIdentity
		return decision
	}

	identityPermission := identityPermission(facts)
	decision.Visible = repositoryVisible(facts.Visibility, authenticated, identityPermission)
	if !decision.Visible {
		decision.Reason = ReasonInvisible
		return decision
	}

	// Visibility itself grants read access. Higher permissions still come only
	// from site, organization, or collaborator authority.
	effective := maxPermission(PermissionRead, identityPermission)
	if authenticated {
		cap, reason, ok := repositoryCredentialCap(principal, request)
		if !ok {
			decision.EffectivePermission = minPermission(effective, cap)
			decision.Reason = reason
			if reason == ReasonRepositoryCap {
				decision.Visible = false
			}
			return decision
		}
		effective = minPermission(effective, cap)
	}
	decision.EffectivePermission = effective
	if effective < required {
		decision.Reason = ReasonInsufficientPermission
		return decision
	}

	if request.Operation == OperationContribute && !contributionAllowed(facts, authenticated) {
		decision.Reason = ReasonContributionPolicy
		return decision
	}
	if request.Operation == OperationPublishEvidence && !request.DesignatedEvidenceWriter {
		decision.Reason = ReasonEvidenceWriterRequired
		return decision
	}

	decision.Allowed = true
	decision.Reason = ReasonAllowed
	decision.RequiresRunnerPolicy = request.Operation == OperationTriggerRunner
	return decision
}

func evaluateOrganization(subject Subject, operation Operation, facts authorityFacts) Decision {
	required, supported := requiredPermission(operation)
	decision := Decision{Exists: facts.Exists, RequiredPermission: required, Reason: ReasonNotFound}
	if !facts.Exists {
		return decision
	}
	if !supported || (operation != OperationReadOrganization && operation != OperationManageIntegrations && operation != OperationAdminOrganization) {
		decision.Reason = ReasonUnsupportedOperation
		return decision
	}
	principal, authenticated := subjectPrincipal(subject)
	if !authenticated || !facts.IdentityActive {
		decision.Reason = ReasonInactiveIdentity
		return decision
	}
	permission := identityPermission(facts)
	decision.Visible = permission >= PermissionRead
	if !decision.Visible {
		decision.Reason = ReasonInvisible
		return decision
	}
	cap, reason, ok := organizationCredentialCap(principal, operation)
	if !ok {
		decision.EffectivePermission = minPermission(permission, cap)
		decision.Reason = reason
		if reason == ReasonRepositoryCap {
			decision.Visible = false
		}
		return decision
	}
	decision.EffectivePermission = minPermission(permission, cap)
	if decision.EffectivePermission < required {
		decision.Reason = ReasonInsufficientPermission
		return decision
	}
	decision.Allowed = true
	decision.Reason = ReasonAllowed
	return decision
}

func subjectPrincipal(subject Subject) (serverauth.Principal, bool) {
	if subject.Principal == nil || subject.Principal.User.ID == uuid.Nil {
		return serverauth.Principal{}, false
	}
	return *subject.Principal, true
}

func identityPermission(facts authorityFacts) Permission {
	if !facts.IdentityActive {
		return PermissionNone
	}
	if facts.SiteAdmin {
		return PermissionAdmin
	}
	organizationPermission := PermissionNone
	if facts.OrganizationMember || facts.ServiceAccount {
		organizationPermission = permissionFromBase(facts.BasePermission)
		organizationPermission = maxPermission(organizationPermission, permissionFromRole(facts.OrganizationRole))
	}
	return maxPermission(organizationPermission, permissionFromRole(facts.CollaboratorRole))
}

func repositoryVisible(visibility models.Visibility, authenticated bool, permission Permission) bool {
	switch visibility {
	case models.VisibilityPublic:
		return true
	case models.VisibilityInternal:
		return authenticated
	case models.VisibilityPrivate:
		return permission >= PermissionRead
	default:
		return false
	}
}

func requiredPermission(operation Operation) (Permission, bool) {
	switch operation {
	case OperationRead, OperationContribute, OperationReadOrganization:
		return PermissionRead, true
	case OperationTriage:
		return PermissionTriage, true
	case OperationWrite, OperationTriggerRunner, OperationPublishEvidence:
		return PermissionWrite, true
	case OperationManageIntegrations:
		return PermissionMaintain, true
	case OperationAdminRepository, OperationAdminOrganization:
		return PermissionAdmin, true
	default:
		return PermissionNone, false
	}
}

func contributionAllowed(facts authorityFacts, authenticated bool) bool {
	if !authenticated {
		return false
	}
	switch facts.ContributionPolicy {
	case models.ContributionDisabled:
		return false
	case models.ContributionMembers:
		return facts.SiteAdmin || facts.OrganizationMember || facts.ServiceAccount || facts.CollaboratorRole != ""
	case models.ContributionAuthenticated:
		return true
	case models.ContributionPublic:
		// Anonymous authentication is intentionally limited to public reads.
		return facts.Visibility == models.VisibilityPublic
	default:
		return false
	}
}

func repositoryCredentialCap(principal serverauth.Principal, request RepositoryRequest) (Permission, Reason, bool) {
	if principal.Kind == serverauth.CredentialDelegated &&
		(!principal.RepoRestricted || principal.OrgID != request.Scope.OrgID || principal.RepoID != request.Scope.RepoID ||
			len(principal.RepositoryCaps) != 1 || principal.RepositoryCaps[0].OrgID != request.Scope.OrgID ||
			principal.RepositoryCaps[0].RepoID != request.Scope.RepoID) {
		return PermissionNone, ReasonRepositoryCap, false
	}
	if !principal.AllowsRepository(request.Scope.OrgID, request.Scope.RepoID) {
		return PermissionNone, ReasonRepositoryCap, false
	}
	switch principal.Kind {
	case serverauth.CredentialSession:
		if request.Operation == OperationPublishEvidence && !principal.HasScope("evidence:write") {
			return PermissionNone, ReasonCredentialScope, false
		}
		return PermissionAdmin, ReasonAllowed, true
	case serverauth.CredentialRecovery:
		if request.Operation == OperationPublishEvidence && !principal.HasScope("evidence:write") {
			return PermissionNone, ReasonCredentialScope, false
		}
		if principal.HasScope("site:admin") {
			return PermissionAdmin, ReasonAllowed, true
		}
		return PermissionNone, ReasonCredentialScope, false
	case serverauth.CredentialPAT, serverauth.CredentialDelegated:
		if request.Operation == OperationPublishEvidence && !principal.HasScope("evidence:write") {
			return PermissionNone, ReasonCredentialScope, false
		}
		cap := tokenPermissionCap(principal, request.Operation)
		if cap == PermissionNone {
			return cap, ReasonCredentialScope, false
		}
		return cap, ReasonAllowed, true
	default:
		return PermissionNone, ReasonCredentialScope, false
	}
}

func tokenPermissionCap(principal serverauth.Principal, operation Operation) Permission {
	if principal.HasScope("repo") {
		return PermissionAdmin
	}
	switch operation {
	case OperationRead:
		if principal.HasScope("admin:repo") {
			return PermissionAdmin
		}
		if principal.HasScope("issues:write") {
			return PermissionWrite
		}
		if principal.HasScope("issues:read") {
			return PermissionRead
		}
	case OperationContribute, OperationTriage, OperationWrite, OperationTriggerRunner:
		if principal.HasScope("admin:repo") {
			return PermissionAdmin
		}
		if principal.HasScope("issues:write") {
			return PermissionWrite
		}
	case OperationPublishEvidence:
		if principal.HasScope("evidence:write") {
			return PermissionWrite
		}
	case OperationManageIntegrations, OperationAdminRepository:
		if principal.HasScope("admin:repo") {
			return PermissionAdmin
		}
	}
	return PermissionNone
}

func organizationCredentialCap(principal serverauth.Principal, operation Operation) (Permission, Reason, bool) {
	if principal.Kind == serverauth.CredentialDelegated || principal.RepoRestricted {
		return PermissionNone, ReasonRepositoryCap, false
	}
	switch principal.Kind {
	case serverauth.CredentialSession:
		return PermissionAdmin, ReasonAllowed, true
	case serverauth.CredentialRecovery:
		if principal.HasScope("site:admin") {
			return PermissionAdmin, ReasonAllowed, true
		}
	case serverauth.CredentialPAT, serverauth.CredentialDelegated:
		if principal.HasScope("admin:org") {
			return PermissionAdmin, ReasonAllowed, true
		}
		if operation == OperationReadOrganization && principal.HasScope("read:org") {
			return PermissionRead, ReasonAllowed, true
		}
	}
	return PermissionNone, ReasonCredentialScope, false
}
