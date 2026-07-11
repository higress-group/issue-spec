package authz

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	adminservice "github.com/higress-group/issue-spec/internal/server/admin"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
	"github.com/higress-group/issue-spec/internal/server/models"
	"github.com/higress-group/issue-spec/internal/server/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestServiceTenantIsolationFilteringAndLifecycle(t *testing.T) {
	pool := migratedPool(t)
	service, err := New(pool)
	if err != nil {
		t.Fatal(err)
	}
	userID := insertUser(t, pool, "reader")
	otherUserID := insertUser(t, pool, "outsider")
	orgID := insertOrganization(t, pool, "alpha", models.BasePermissionRead)
	otherOrgID := insertOrganization(t, pool, "beta", models.BasePermissionRead)
	publicID := insertRepository(t, pool, orgID, "public", models.VisibilityPublic)
	internalID := insertRepository(t, pool, orgID, "internal", models.VisibilityInternal)
	privateID := insertRepository(t, pool, orgID, "private", models.VisibilityPrivate)
	otherRepoID := insertRepository(t, pool, otherOrgID, "other", models.VisibilityPrivate)
	insertMembership(t, pool, orgID, userID, "reader")

	principal := serverauth.Principal{User: serverauth.User{ID: userID}, Kind: serverauth.CredentialSession}
	outsider := serverauth.Principal{User: serverauth.User{ID: otherUserID}, Kind: serverauth.CredentialSession}
	read := func(subject Subject, orgID, repoID uuid.UUID) Decision {
		t.Helper()
		decision, err := service.EvaluateRepository(t.Context(), subject, RepositoryRequest{
			Scope: models.RepoScope{OrgID: orgID, RepoID: repoID}, Operation: OperationRead,
		})
		if err != nil {
			t.Fatal(err)
		}
		return decision
	}
	if decision := read(Anonymous(), orgID, publicID); !decision.Allowed {
		t.Fatalf("anonymous public read = %+v", decision)
	}
	if decision := read(Anonymous(), orgID, internalID); decision.Visible {
		t.Fatalf("anonymous internal read = %+v", decision)
	}
	if decision := read(Authenticated(outsider), orgID, internalID); !decision.Allowed {
		t.Fatalf("authenticated internal read = %+v", decision)
	}
	if decision := read(Authenticated(principal), orgID, privateID); !decision.Allowed {
		t.Fatalf("member private read = %+v", decision)
	}
	if decision := read(Authenticated(principal), orgID, otherRepoID); decision.Exists {
		t.Fatalf("cross-org composite scope = %+v", decision)
	}

	visible, err := service.ListReadableRepositories(t.Context(), Anonymous(), models.OrgScope{OrgID: orgID})
	if err != nil || len(visible) != 1 || visible[0].Scope.RepoID != publicID {
		t.Fatalf("anonymous list = %+v, %v", visible, err)
	}
	visible, err = service.ListReadableRepositories(t.Context(), Authenticated(principal), models.OrgScope{OrgID: orgID})
	if err != nil || len(visible) != 3 {
		t.Fatalf("member list = %+v, %v", visible, err)
	}

	adminRequest := adminservice.AuthorizationRequest{Action: adminservice.ActionRepositoryAdmin, OrganizationID: orgID, RepositoryID: privateID}
	if err := service.Authorize(t.Context(), principal, adminRequest); !errors.Is(err, adminservice.ErrForbidden) {
		t.Fatalf("reader admin error = %v", err)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE org_memberships SET role = 'owner', updated_at = clock_timestamp() WHERE organization_id = $1 AND user_id = $2`, orgID, userID); err != nil {
		t.Fatal(err)
	}
	if err := service.Authorize(t.Context(), principal, adminRequest); err != nil {
		t.Fatalf("updated owner admin error = %v", err)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE users SET status = 'disabled', updated_at = clock_timestamp() WHERE id = $1`, userID); err != nil {
		t.Fatal(err)
	}
	if decision := read(Authenticated(principal), orgID, publicID); decision.Allowed || decision.Reason != ReasonInactiveIdentity {
		t.Fatalf("disabled identity = %+v", decision)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE users SET status = 'active' WHERE id = $1`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE repos SET archived_at = clock_timestamp() WHERE id = $1`, privateID); err != nil {
		t.Fatal(err)
	}
	if decision := read(Authenticated(principal), orgID, privateID); decision.Exists {
		t.Fatalf("archived repository = %+v", decision)
	}
	if err := service.Authorize(t.Context(), principal, adminRequest); !errors.Is(err, adminservice.ErrNotFound) {
		t.Fatalf("archived repository admin error = %v", err)
	}
}

func TestConcurrentAuthorityChangesRemainFailClosed(t *testing.T) {
	pool := migratedPool(t)
	service, err := New(pool)
	if err != nil {
		t.Fatal(err)
	}
	userID := insertUser(t, pool, "racer")
	orgID := insertOrganization(t, pool, "race-org", models.BasePermissionRead)
	repoID := insertRepository(t, pool, orgID, "private", models.VisibilityPrivate)
	insertMembership(t, pool, orgID, userID, "reader")
	principal := serverauth.Principal{User: serverauth.User{ID: userID}, Kind: serverauth.CredentialSession}
	request := RepositoryRequest{Scope: models.RepoScope{OrgID: orgID, RepoID: repoID}, Operation: OperationAdminRepository}

	var wait sync.WaitGroup
	errCh := make(chan error, 64)
	for i := 0; i < 8; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for j := 0; j < 40; j++ {
				decision, err := service.EvaluateRepository(t.Context(), Authenticated(principal), request)
				if err != nil {
					errCh <- err
					return
				}
				if decision.Allowed && decision.EffectivePermission != PermissionAdmin {
					errCh <- errors.New("authorization allowed without admin permission")
					return
				}
			}
		}()
	}
	for i := 0; i < 40; i++ {
		role := "reader"
		if i%2 == 0 {
			role = "owner"
		}
		if _, err := pool.Exec(t.Context(), `UPDATE org_memberships SET role = $3, updated_at = clock_timestamp()
			WHERE organization_id = $1 AND user_id = $2`, orgID, userID, role); err != nil {
			t.Fatal(err)
		}
	}
	wait.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
}

func insertUser(t *testing.T, pool *pgxpool.Pool, login string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := pool.Exec(t.Context(), `INSERT INTO users (id, login, display_name) VALUES ($1, $2, $2)`, id, login); err != nil {
		t.Fatal(err)
	}
	return id
}

func insertOrganization(t *testing.T, pool *pgxpool.Pool, name string, base models.BasePermission) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := pool.Exec(t.Context(), `INSERT INTO orgs (id, name, display_name, base_permission) VALUES ($1, $2, $2, $3)`, id, name, base); err != nil {
		t.Fatal(err)
	}
	return id
}

func insertRepository(t *testing.T, pool *pgxpool.Pool, orgID uuid.UUID, name string, visibility models.Visibility) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := pool.Exec(t.Context(), `INSERT INTO repos (id, organization_id, name, display_name, visibility)
		VALUES ($1, $2, $3, $3, $4)`, id, orgID, name, visibility); err != nil {
		t.Fatal(err)
	}
	return id
}

func insertMembership(t *testing.T, pool *pgxpool.Pool, orgID, userID uuid.UUID, role string) {
	t.Helper()
	if _, err := pool.Exec(t.Context(), `INSERT INTO org_memberships
		(organization_id, user_id, role, state, activated_at) VALUES ($1, $2, $3, 'active', clock_timestamp())`, orgID, userID, role); err != nil {
		t.Fatal(err)
	}
}

func migratedPool(t *testing.T) *pgxpool.Pool {
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
	schema := "authz_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
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
	config.MaxConns = 32
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
