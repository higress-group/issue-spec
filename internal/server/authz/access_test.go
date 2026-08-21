package authz

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
	"github.com/higress-group/issue-spec/internal/server/models"
)

func TestSiteAccessSeparatesIdentityRoleFromCredentialScope(t *testing.T) {
	pool := migratedPool(t)
	service, err := New(pool)
	if err != nil {
		t.Fatal(err)
	}
	userID := insertUser(t, pool, "site-owner")
	if _, err := pool.Exec(t.Context(), `INSERT INTO site_role_assignments (id, user_id, role)
		VALUES ($1, $2, 'site_admin')`, uuid.New(), userID); err != nil {
		t.Fatal(err)
	}

	session := serverauth.Principal{User: serverauth.User{ID: userID}, Kind: serverauth.CredentialSession}
	access, err := service.ResolveSiteAccess(t.Context(), Authenticated(session))
	if err != nil {
		t.Fatal(err)
	}
	if !access.IdentitySiteAdmin || len(access.AllowedActions) != 1 || access.AllowedActions[0] != AccessSiteAdmin {
		t.Fatalf("session site access = %+v", access)
	}

	pat := serverauth.Principal{User: serverauth.User{ID: userID}, Kind: serverauth.CredentialPAT,
		Scopes: []string{"issues:read"}}
	access, err = service.ResolveSiteAccess(t.Context(), Authenticated(pat))
	if err != nil {
		t.Fatal(err)
	}
	if !access.IdentitySiteAdmin || len(access.AllowedActions) != 0 {
		t.Fatalf("scope-capped PAT site access = %+v", access)
	}
}

func TestAccessibleOrganizationAndRepositoryProjection(t *testing.T) {
	pool := migratedPool(t)
	service, err := New(pool)
	if err != nil {
		t.Fatal(err)
	}
	userID := insertUser(t, pool, "operator")
	ownedOrg := insertOrganization(t, pool, "owned", models.BasePermissionNone)
	ownedRepo := insertRepository(t, pool, ownedOrg, "private", models.VisibilityPrivate)
	insertMembership(t, pool, ownedOrg, userID, "owner")
	containerOrg := insertOrganization(t, pool, "container", models.BasePermissionNone)
	containerRepo := insertRepository(t, pool, containerOrg, "internal", models.VisibilityInternal)
	privateOrg := insertOrganization(t, pool, "hidden", models.BasePermissionNone)
	_ = insertRepository(t, pool, privateOrg, "hidden", models.VisibilityPrivate)
	principal := serverauth.Principal{User: serverauth.User{ID: userID}, Kind: serverauth.CredentialSession}

	organizations, err := service.ListAccessibleOrganizations(t.Context(), Authenticated(principal))
	if err != nil {
		t.Fatal(err)
	}
	if len(organizations) != 2 {
		t.Fatalf("organizations = %+v", organizations)
	}
	byID := make(map[uuid.UUID]OrganizationAccess, len(organizations))
	for _, organization := range organizations {
		byID[organization.Organization.ID] = organization
	}
	if owned := byID[ownedOrg]; owned.ContainerOnly || !containsAccess(owned.AllowedActions, AccessOrganizationAdmin) ||
		!containsAccess(owned.AllowedActions, AccessCredentialAdmin) || !containsAccess(owned.AllowedActions, AccessManageIntegrations) {
		t.Fatalf("owned organization = %+v", owned)
	}
	if container := byID[containerOrg]; !container.ContainerOnly || len(container.AllowedActions) != 0 {
		t.Fatalf("container organization = %+v", container)
	} else if encoded, err := json.Marshal(container); err != nil {
		t.Fatal(err)
	} else {
		value := string(encoded)
		for _, forbidden := range []string{"description", "base_permission", "representation_version", "members_collection_version"} {
			if strings.Contains(value, forbidden) {
				t.Fatalf("container JSON leaked %q: %s", forbidden, value)
			}
		}
		if !strings.Contains(value, `"effective_permission":"none"`) {
			t.Fatalf("container JSON lacks stable effective permission: %s", value)
		}
	}
	if _, exposed := byID[privateOrg]; exposed {
		t.Fatal("private organization without readable repository was exposed")
	}

	repositories, err := service.ListRepositoryAccess(t.Context(), Authenticated(principal), models.OrgScope{OrgID: ownedOrg})
	if err != nil {
		t.Fatal(err)
	}
	if len(repositories) != 1 || repositories[0].Repository.ID != ownedRepo ||
		!containsAccess(repositories[0].AllowedActions, AccessRepositoryAdmin) ||
		!containsAccess(repositories[0].AllowedActions, AccessManageIntegrations) ||
		!containsAccess(repositories[0].AllowedActions, AccessTriggerRunner) {
		t.Fatalf("owned repositories = %+v", repositories)
	}
	if encoded, err := json.Marshal(repositories[0]); err != nil {
		t.Fatal(err)
	} else if value := string(encoded); !strings.Contains(value, `"effective_permission":"admin"`) ||
		strings.Contains(value, `"effective_permission":5`) {
		t.Fatalf("repository JSON lacks stable string permission: %s", value)
	}
	repositories, err = service.ListRepositoryAccess(t.Context(), Authenticated(principal), models.OrgScope{OrgID: containerOrg})
	if err != nil {
		t.Fatal(err)
	}
	if len(repositories) != 1 || repositories[0].Repository.ID != containerRepo ||
		len(repositories[0].AllowedActions) != 1 || repositories[0].AllowedActions[0] != AccessRead {
		t.Fatalf("container repositories = %+v", repositories)
	}
}

func containsAccess(values []AccessAction, expected AccessAction) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
