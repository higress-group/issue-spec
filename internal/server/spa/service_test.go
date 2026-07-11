package spa

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
	"github.com/higress-group/issue-spec/internal/server/authz"
	"github.com/higress-group/issue-spec/internal/server/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestCurrentContextAndTenantSafeCandidates(t *testing.T) {
	pool := spaPool(t)
	authorization, err := authz.New(pool)
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(pool, authorization)
	if err != nil {
		t.Fatal(err)
	}
	adminID := insertSPAUser(t, pool, "admin")
	memberID := insertSPAUser(t, pool, "member")
	collaboratorID := insertSPAUser(t, pool, "collaborator")
	outsiderID := insertSPAUser(t, pool, "outsider")
	otherTenantID := insertSPAUser(t, pool, "other-tenant")
	serviceUserID := insertSPAUser(t, pool, "svc-bot")
	orgID := insertSPAOrg(t, pool, "alpha")
	otherOrgID := insertSPAOrg(t, pool, "beta")
	repoID := insertSPARepo(t, pool, orgID, "private")
	insertSPAMembership(t, pool, orgID, adminID, "owner")
	memberMembershipID := insertSPAMembership(t, pool, orgID, memberID, "member")
	insertSPAMembership(t, pool, otherOrgID, otherTenantID, "owner")
	if _, err := pool.Exec(t.Context(), `INSERT INTO repo_collaborators
		(id, organization_id, repository_id, user_id, role) VALUES ($1, $2, $3, $4, 'read')`,
		uuid.New(), orgID, repoID, collaboratorID); err != nil {
		t.Fatal(err)
	}
	serviceAccountID := uuid.New()
	if _, err := pool.Exec(t.Context(), `INSERT INTO service_accounts
		(id, user_id, organization_id, name, created_by_user_id) VALUES ($1, $2, $3, 'bot', $4)`,
		serviceAccountID, serviceUserID, orgID, adminID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO site_role_assignments (id, user_id, role)
		VALUES ($1, $2, 'site_admin')`, uuid.New(), adminID); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	principal := serverauth.Principal{User: serverauth.User{ID: adminID, Login: "admin", DisplayName: "admin"},
		Kind: serverauth.CredentialSession, IdleExpiresAt: now.Add(time.Hour), ExpiresAt: now.Add(24 * time.Hour)}

	current, err := service.Current(t.Context(), principal, "issue_spec_csrf")
	if err != nil {
		t.Fatal(err)
	}
	if !current.User.SiteAdmin || current.Credential.ScopeMode != "identity" || current.Credential.Scopes != nil ||
		current.Credential.IdleExpiresAt == nil || current.Session == nil || len(current.AllowedActions) != 1 {
		t.Fatalf("current context = %+v", current)
	}

	associated, err := service.UserCandidates(t.Context(), principal, orgID, PurposeAdministration, MatchPrefix, "", 20)
	if err != nil {
		t.Fatal(err)
	}
	byLogin := make(map[string]UserCandidate, len(associated.Users))
	for _, user := range associated.Users {
		byLogin[user.Login] = user
	}
	for _, expected := range []string{"admin", "member", "collaborator", "svc-bot"} {
		if _, ok := byLogin[expected]; !ok {
			t.Fatalf("associated candidates missing %q: %+v", expected, associated.Users)
		}
	}
	if _, leaked := byLogin["outsider"]; leaked {
		t.Fatal("unassociated user leaked through prefix candidates")
	}
	if _, leaked := byLogin["other-tenant"]; leaked {
		t.Fatal("other tenant user leaked through prefix candidates")
	}
	if byLogin["member"].Membership == nil || byLogin["member"].Membership.ID != memberMembershipID {
		t.Fatalf("member relationship = %+v", byLogin["member"])
	}
	if byLogin["svc-bot"].Kind != "service_account" || byLogin["svc-bot"].ServiceAccountID == nil ||
		*byLogin["svc-bot"].ServiceAccountID != serviceAccountID {
		t.Fatalf("service account candidate = %+v", byLogin["svc-bot"])
	}

	exact, err := service.UserCandidates(t.Context(), principal, orgID, PurposeMembership, MatchExact, "OUTSIDER", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(exact.Users) != 1 || exact.Users[0].ID != outsiderID {
		t.Fatalf("exact candidate = %+v", exact.Users)
	}
	managed, err := service.UserCandidates(t.Context(), principal, orgID, PurposeManagedPAT, MatchExact, "outsider", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(managed.Users) != 0 {
		t.Fatalf("unassociated managed PAT candidates = %+v", managed.Users)
	}
}

func insertSPAUser(t *testing.T, pool *pgxpool.Pool, login string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := pool.Exec(t.Context(), `INSERT INTO users (id, login, display_name) VALUES ($1, $2, $2)`, id, login); err != nil {
		t.Fatal(err)
	}
	return id
}

func insertSPAOrg(t *testing.T, pool *pgxpool.Pool, name string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := pool.Exec(t.Context(), `INSERT INTO orgs (id, name, display_name, base_permission)
		VALUES ($1, $2, $2, 'none')`, id, name); err != nil {
		t.Fatal(err)
	}
	return id
}

func insertSPARepo(t *testing.T, pool *pgxpool.Pool, orgID uuid.UUID, name string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := pool.Exec(t.Context(), `INSERT INTO repos
		(id, organization_id, name, display_name, visibility) VALUES ($1, $2, $3, $3, 'private')`, id, orgID, name); err != nil {
		t.Fatal(err)
	}
	return id
}

func insertSPAMembership(t *testing.T, pool *pgxpool.Pool, orgID, userID uuid.UUID, role string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := pool.Exec(t.Context(), `INSERT INTO org_memberships
		(id, organization_id, user_id, role, state, activated_at) VALUES ($1, $2, $3, $4, 'active', clock_timestamp())`,
		id, orgID, userID, role); err != nil {
		t.Fatal(err)
	}
	return id
}

func spaPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	databaseURL := strings.TrimSpace(os.Getenv("TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	adminPool, err := pgxpool.New(t.Context(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(adminPool.Close)
	schema := "spa_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := adminPool.Exec(t.Context(), "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = adminPool.Exec(ctx, "DROP SCHEMA IF EXISTS "+quoted+" CASCADE")
	})
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := store.RunMigrations(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	return pool
}
