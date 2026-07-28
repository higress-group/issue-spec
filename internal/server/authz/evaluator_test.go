package authz

import (
	"testing"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/capability"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
	"github.com/higress-group/issue-spec/internal/server/models"
)

func TestRepositoryAuthorizationMatrix(t *testing.T) {
	orgID, repoID, otherRepoID := uuid.New(), uuid.New(), uuid.New()
	scope := models.RepoScope{OrgID: orgID, RepoID: repoID}
	active := func(kind serverauth.CredentialKind, scopes ...string) Subject {
		return Authenticated(serverauth.Principal{
			User: serverauth.User{ID: uuid.New(), Status: "active"}, Kind: kind, Scopes: scopes,
		})
	}
	patFor := func(scopes []string, allowed uuid.UUID) Subject {
		return Authenticated(serverauth.Principal{
			User: serverauth.User{ID: uuid.New(), Status: "active"}, Kind: serverauth.CredentialPAT,
			Scopes: scopes, RepoRestricted: true,
			RepositoryCaps: []serverauth.RepositoryCap{{OrgID: orgID, RepoID: allowed}},
		})
	}
	restricted := func(kind serverauth.CredentialKind, scopes []string, allowedOrg, allowedRepo uuid.UUID) Subject {
		return Authenticated(serverauth.Principal{
			User: serverauth.User{ID: uuid.New(), Status: "active"}, Kind: kind,
			Scopes: scopes, RepoRestricted: true, OrgID: allowedOrg, RepoID: allowedRepo,
			RepositoryCaps: []serverauth.RepositoryCap{{OrgID: allowedOrg, RepoID: allowedRepo}},
		})
	}
	restrictedOps := func(scopes, operations []string) Subject {
		return Authenticated(serverauth.Principal{User: serverauth.User{ID: uuid.New(), Status: "active"},
			Kind: serverauth.CredentialDelegated, Scopes: scopes, Operations: operations, RepoRestricted: true,
			OrgID: orgID, RepoID: repoID, RepositoryCaps: []serverauth.RepositoryCap{{OrgID: orgID, RepoID: repoID}}})
	}
	public := authorityFacts{Exists: true, Visibility: models.VisibilityPublic, ContributionPolicy: models.ContributionAuthenticated}
	internal := public
	internal.Visibility = models.VisibilityInternal
	private := public
	private.Visibility = models.VisibilityPrivate
	member := private
	member.IdentityActive = true
	member.OrganizationMember = true
	member.BasePermission = models.BasePermissionTriage
	memberRole := member
	memberRole.BasePermission = models.BasePermissionRead
	memberRole.OrganizationRole = "member"
	owner := member
	owner.OrganizationRole = "owner"

	tests := []struct {
		name         string
		subject      Subject
		request      RepositoryRequest
		facts        authorityFacts
		allowed      bool
		visible      bool
		reason       Reason
		permission   Permission
		runnerPolicy bool
	}{
		{"anonymous public read", Anonymous(), RepositoryRequest{Scope: scope, Operation: OperationRead}, public, true, true, ReasonAllowed, PermissionRead, false},
		{"anonymous internal concealed", Anonymous(), RepositoryRequest{Scope: scope, Operation: OperationRead}, internal, false, false, ReasonInvisible, PermissionNone, false},
		{"inactive credential does not degrade to anonymous", active(serverauth.CredentialSession), RepositoryRequest{Scope: scope, Operation: OperationRead}, public, false, false, ReasonInactiveIdentity, PermissionNone, false},
		{"active identity reads internal", active(serverauth.CredentialSession), RepositoryRequest{Scope: scope, Operation: OperationRead}, authorityFacts{Exists: true, IdentityActive: true, Visibility: models.VisibilityInternal}, true, true, ReasonAllowed, PermissionRead, false},
		{"private outsider concealed", active(serverauth.CredentialSession), RepositoryRequest{Scope: scope, Operation: OperationRead}, authorityFacts{Exists: true, IdentityActive: true, Visibility: models.VisibilityPrivate}, false, false, ReasonInvisible, PermissionNone, false},
		{"base permission grants triage", active(serverauth.CredentialSession), RepositoryRequest{Scope: scope, Operation: OperationTriage}, member, true, true, ReasonAllowed, PermissionTriage, false},
		{"base permission caps write", active(serverauth.CredentialSession), RepositoryRequest{Scope: scope, Operation: OperationWrite}, member, false, true, ReasonInsufficientPermission, PermissionTriage, false},
		{"member role grants write across organization repositories", active(serverauth.CredentialSession), RepositoryRequest{Scope: scope, Operation: OperationWrite}, memberRole, true, true, ReasonAllowed, PermissionWrite, false},
		{"owner grants admin", active(serverauth.CredentialSession), RepositoryRequest{Scope: scope, Operation: OperationAdminRepository}, owner, true, true, ReasonAllowed, PermissionAdmin, false},
		{"collaborator raises permission", active(serverauth.CredentialSession), RepositoryRequest{Scope: scope, Operation: OperationWrite}, authorityFacts{Exists: true, IdentityActive: true, Visibility: models.VisibilityPrivate, CollaboratorRole: "write"}, true, true, ReasonAllowed, PermissionWrite, false},
		{"pat repository cap fails closed", patFor([]string{"repo"}, otherRepoID), RepositoryRequest{Scope: scope, Operation: OperationRead}, owner, false, false, ReasonRepositoryCap, PermissionNone, false},
		{"pat read scope caps owner", patFor([]string{"issues:read"}, repoID), RepositoryRequest{Scope: scope, Operation: OperationWrite}, owner, false, true, ReasonCredentialScope, PermissionNone, false},
		{"delegated exact cap writes", restricted(serverauth.CredentialDelegated, []string{"issues:write"}, orgID, repoID), RepositoryRequest{Scope: scope, Operation: OperationWrite}, owner, true, true, ReasonAllowed, PermissionWrite, false},
		{"delegated operation claim allows write", restrictedOps([]string{"issues:write"}, []string{string(capability.OperationArtifactWrite)}), RepositoryRequest{Scope: scope, Operation: OperationWrite}, owner, true, true, ReasonAllowed, PermissionWrite, false},
		{"delegated comment claim cannot authorize native write", restrictedOps([]string{"issues:write"}, []string{string(capability.OperationIssueCommentWrite)}), RepositoryRequest{Scope: scope, Operation: OperationWrite}, owner, false, true, ReasonCredentialScope, PermissionNone, false},
		{"delegated comment claim cannot trigger runner", restrictedOps([]string{"issues:write"}, []string{string(capability.OperationIssueCommentWrite)}), RepositoryRequest{Scope: scope, Operation: OperationTriggerRunner}, owner, false, true, ReasonCredentialScope, PermissionNone, false},
		{"delegated comment claim retains comment triage", restrictedOps([]string{"issues:write"}, []string{string(capability.OperationIssueCommentWrite)}), RepositoryRequest{Scope: scope, Operation: OperationTriage}, owner, true, true, ReasonAllowed, PermissionWrite, false},
		{"delegated operation claim denies scope expansion", restrictedOps([]string{"issues:write"}, []string{string(capability.OperationIssueRead)}), RepositoryRequest{Scope: scope, Operation: OperationWrite}, owner, false, true, ReasonCredentialScope, PermissionNone, false},
		{"delegated unrestricted shape denied", active(serverauth.CredentialDelegated, "issues:write"), RepositoryRequest{Scope: scope, Operation: OperationWrite}, owner, false, false, ReasonRepositoryCap, PermissionNone, false},
		{"delegated wrong repo denied", restricted(serverauth.CredentialDelegated, []string{"issues:write"}, orgID, otherRepoID), RepositoryRequest{Scope: scope, Operation: OperationWrite}, owner, false, false, ReasonRepositoryCap, PermissionNone, false},
		{"delegated wrong org denied", restricted(serverauth.CredentialDelegated, []string{"issues:write"}, uuid.New(), repoID), RepositoryRequest{Scope: scope, Operation: OperationWrite}, owner, false, false, ReasonRepositoryCap, PermissionNone, false},
		{"recovery site admin", active(serverauth.CredentialRecovery, "site:admin"), RepositoryRequest{Scope: scope, Operation: OperationAdminRepository}, owner, true, true, ReasonAllowed, PermissionAdmin, false},
		{"site admin identity pat still token capped", active(serverauth.CredentialPAT, "issues:read"), RepositoryRequest{Scope: scope, Operation: OperationAdminRepository}, authorityFacts{Exists: true, IdentityActive: true, SiteAdmin: true, Visibility: models.VisibilityPrivate}, false, true, ReasonCredentialScope, PermissionNone, false},
		{"disabled contribution policy", active(serverauth.CredentialSession), RepositoryRequest{Scope: scope, Operation: OperationContribute}, authorityFacts{Exists: true, IdentityActive: true, Visibility: models.VisibilityPublic, ContributionPolicy: models.ContributionDisabled}, false, true, ReasonContributionPolicy, PermissionRead, false},
		{"members contribution rejects outsider", active(serverauth.CredentialSession), RepositoryRequest{Scope: scope, Operation: OperationContribute}, authorityFacts{Exists: true, IdentityActive: true, Visibility: models.VisibilityPublic, ContributionPolicy: models.ContributionMembers}, false, true, ReasonContributionPolicy, PermissionRead, false},
		{"members contribution accepts collaborator", active(serverauth.CredentialSession), RepositoryRequest{Scope: scope, Operation: OperationContribute}, authorityFacts{Exists: true, IdentityActive: true, Visibility: models.VisibilityPublic, ContributionPolicy: models.ContributionMembers, CollaboratorRole: "read"}, true, true, ReasonAllowed, PermissionRead, false},
		{"anonymous mutation is denied", Anonymous(), RepositoryRequest{Scope: scope, Operation: OperationContribute}, authorityFacts{Exists: true, Visibility: models.VisibilityPublic, ContributionPolicy: models.ContributionPublic}, false, true, ReasonContributionPolicy, PermissionRead, false},
		{"repo scope cannot replace evidence scope", active(serverauth.CredentialPAT, "repo"), RepositoryRequest{Scope: scope, Operation: OperationPublishEvidence}, owner, false, true, ReasonCredentialScope, PermissionNone, false},
		{"admin repo cannot replace evidence scope", active(serverauth.CredentialPAT, "admin:repo"), RepositoryRequest{Scope: scope, Operation: OperationPublishEvidence}, owner, false, true, ReasonCredentialScope, PermissionNone, false},
		{"issues write cannot replace evidence scope", active(serverauth.CredentialPAT, "issues:write"), RepositoryRequest{Scope: scope, Operation: OperationPublishEvidence}, owner, false, true, ReasonCredentialScope, PermissionNone, false},
		{"session admin cannot replace evidence scope", active(serverauth.CredentialSession), RepositoryRequest{Scope: scope, Operation: OperationPublishEvidence}, owner, false, true, ReasonCredentialScope, PermissionNone, false},
		{"recovery admin cannot replace evidence scope", active(serverauth.CredentialRecovery, "site:admin"), RepositoryRequest{Scope: scope, Operation: OperationPublishEvidence}, owner, false, true, ReasonCredentialScope, PermissionNone, false},
		{"evidence scope and exact repository cap allow without designation", patFor([]string{"evidence:write"}, repoID), RepositoryRequest{Scope: scope, Operation: OperationPublishEvidence}, owner, true, true, ReasonAllowed, PermissionWrite, false},
		{"evidence rejects inactive identity", patFor([]string{"evidence:write"}, repoID), RepositoryRequest{Scope: scope, Operation: OperationPublishEvidence}, authorityFacts{Exists: true, Visibility: models.VisibilityPublic}, false, false, ReasonInactiveIdentity, PermissionNone, false},
		{"evidence rejects invisible repository", patFor([]string{"evidence:write"}, repoID), RepositoryRequest{Scope: scope, Operation: OperationPublishEvidence}, authorityFacts{Exists: true, IdentityActive: true, Visibility: models.VisibilityPrivate}, false, false, ReasonInvisible, PermissionNone, false},
		{"evidence still needs live identity authority", patFor([]string{"evidence:write"}, repoID), RepositoryRequest{Scope: scope, Operation: OperationPublishEvidence}, authorityFacts{Exists: true, IdentityActive: true, Visibility: models.VisibilityPublic}, false, true, ReasonInsufficientPermission, PermissionRead, false},
		{"evidence still needs exact repository cap", patFor([]string{"evidence:write"}, otherRepoID), RepositoryRequest{Scope: scope, Operation: OperationPublishEvidence}, owner, false, false, ReasonRepositoryCap, PermissionNone, false},
		{"unsupported credential cannot publish evidence", active(serverauth.CredentialKind("future"), "evidence:write"), RepositoryRequest{Scope: scope, Operation: OperationPublishEvidence}, owner, false, true, ReasonCredentialScope, PermissionNone, false},
		{"runner returns successor gate", active(serverauth.CredentialPAT, "issues:write", "runner:delegate"), RepositoryRequest{Scope: scope, Operation: OperationTriggerRunner}, owner, true, true, ReasonAllowed, PermissionWrite, true},
		{"archived is absent", active(serverauth.CredentialSession), RepositoryRequest{Scope: scope, Operation: OperationRead}, authorityFacts{}, false, false, ReasonNotFound, PermissionNone, false},
		{"unknown operation fails closed", active(serverauth.CredentialSession), RepositoryRequest{Scope: scope, Operation: Operation("future")}, public, false, false, ReasonUnsupportedOperation, PermissionNone, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			decision := evaluateRepository(test.subject, test.request, test.facts)
			if decision.Allowed != test.allowed || decision.Visible != test.visible || decision.Reason != test.reason ||
				decision.EffectivePermission != test.permission || decision.RequiresRunnerPolicy != test.runnerPolicy {
				t.Fatalf("decision = %+v", decision)
			}
		})
	}
}

func TestOrganizationAuthorizationMatrix(t *testing.T) {
	principal := func(kind serverauth.CredentialKind, scopes ...string) Subject {
		return Authenticated(serverauth.Principal{User: serverauth.User{ID: uuid.New()}, Kind: kind, Scopes: scopes})
	}
	facts := authorityFacts{Exists: true, IdentityActive: true, OrganizationMember: true, BasePermission: models.BasePermissionRead}
	owner := facts
	owner.OrganizationRole = "owner"
	maintainer := facts
	maintainer.OrganizationRole = "maintainer"
	siteAdmin := facts
	siteAdmin.SiteAdmin = true
	tests := []struct {
		name      string
		subject   Subject
		operation Operation
		facts     authorityFacts
		allowed   bool
		reason    Reason
	}{
		{"anonymous denied", Anonymous(), OperationReadOrganization, facts, false, ReasonInactiveIdentity},
		{"member session reads", principal(serverauth.CredentialSession), OperationReadOrganization, facts, true, ReasonAllowed},
		{"member cannot admin", principal(serverauth.CredentialSession), OperationAdminOrganization, facts, false, ReasonInsufficientPermission},
		{"owner session admins", principal(serverauth.CredentialSession), OperationAdminOrganization, owner, true, ReasonAllowed},
		{"reader cannot manage integrations", principal(serverauth.CredentialSession), OperationManageIntegrations, facts, false, ReasonInsufficientPermission},
		{"maintainer manages integrations", principal(serverauth.CredentialSession), OperationManageIntegrations, maintainer, true, ReasonAllowed},
		{"owner manages integrations", principal(serverauth.CredentialSession), OperationManageIntegrations, owner, true, ReasonAllowed},
		{"org read pat cannot manage integrations", principal(serverauth.CredentialPAT, "read:org"), OperationManageIntegrations, owner, false, ReasonCredentialScope},
		{"org admin pat manages integrations", principal(serverauth.CredentialPAT, "admin:org"), OperationManageIntegrations, owner, true, ReasonAllowed},
		{"owner pat read scope is capped", principal(serverauth.CredentialPAT, "read:org"), OperationAdminOrganization, owner, false, ReasonCredentialScope},
		{"owner pat admin scope", principal(serverauth.CredentialPAT, "admin:org"), OperationAdminOrganization, owner, true, ReasonAllowed},
		{"site admin pat is still token capped", principal(serverauth.CredentialPAT, "issues:read"), OperationAdminOrganization, siteAdmin, false, ReasonCredentialScope},
		{"repo restricted pat cannot org admin", Subject{Principal: &serverauth.Principal{User: serverauth.User{ID: uuid.New()}, Kind: serverauth.CredentialPAT, Scopes: []string{"admin:org"}, RepoRestricted: true}}, OperationAdminOrganization, owner, false, ReasonRepositoryCap},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			decision := evaluateOrganization(test.subject, test.operation, test.facts)
			if decision.Allowed != test.allowed || decision.Reason != test.reason {
				t.Fatalf("decision = %+v", decision)
			}
		})
	}
}
