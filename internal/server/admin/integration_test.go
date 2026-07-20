package admin_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	adminservice "github.com/higress-group/issue-spec/internal/server/admin"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
	"github.com/higress-group/issue-spec/internal/server/auth/pat"
	"github.com/higress-group/issue-spec/internal/server/auth/recovery"
	"github.com/higress-group/issue-spec/internal/server/authz"
	"github.com/higress-group/issue-spec/internal/server/models"
	"github.com/higress-group/issue-spec/internal/server/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const testDatabaseEnv = "TEST_DATABASE_URL"

func TestEnsureRepositoryConcurrentReuseDefaultsAndAudit(t *testing.T) {
	pool := migratedPool(t)
	service, adminUser := newService(t, pool)
	actor := actor(adminUser.ID, "ensure-org")
	org := createOrganization(t, service, actor, "ensure-org")
	const attempts = 12
	results := make(chan adminservice.EnsureRepositoryResult, attempts)
	errs := make(chan error, attempts)
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			requestActor := actor
			requestActor.RequestID = fmt.Sprintf("ensure-repo-%d", i)
			result, err := service.EnsureRepository(context.Background(), requestActor, org.ID,
				adminservice.EnsureRepositoryInput{Name: "widgets", DisplayName: "Widgets"})
			if err != nil {
				errs <- err
				return
			}
			results <- result
		}(i)
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	var created int
	var id uuid.UUID
	for result := range results {
		if result.Created {
			created++
		}
		if id == uuid.Nil {
			id = result.Repository.ID
		}
		if result.Repository.ID != id || result.Repository.Visibility != models.VisibilityPrivate ||
			result.Repository.ContributionPolicy != models.ContributionMembers || result.Repository.DefaultBranch != "main" {
			t.Fatalf("ensure result = %+v", result)
		}
	}
	var rows, audits int
	if err := pool.QueryRow(t.Context(), `SELECT
		(SELECT count(*) FROM repos WHERE organization_id=$1 AND name_key='widgets'),
		(SELECT count(*) FROM audit_events WHERE organization_id=$1 AND action='repository.ensure.create')`, org.ID).
		Scan(&rows, &audits); err != nil {
		t.Fatal(err)
	}
	if created != 1 || rows != 1 || audits != 1 {
		t.Fatalf("created=%d rows=%d audits=%d", created, rows, audits)
	}
}

func TestBootstrapConcurrentClaimCreatesExactlyOneAdministrator(t *testing.T) {
	pool := migratedPool(t)
	secrets := testSecrets(t)
	service, err := adminservice.New(pool, []byte(bootstrapSecret()), secrets)
	if err != nil {
		t.Fatal(err)
	}
	status, err := service.BootstrapStatus(t.Context())
	if err != nil || !status.Available || status.Completed {
		t.Fatalf("initial BootstrapStatus = %+v, %v", status, err)
	}
	if _, err := service.ClaimBootstrap(t.Context(), adminservice.BootstrapClaimInput{
		Secret: "wrong-secret", Login: "admin", DisplayName: "Administrator", RequestID: "wrong",
	}); !errors.Is(err, adminservice.ErrInvalidBootstrapSecret) {
		t.Fatalf("wrong bootstrap secret error = %v", err)
	}

	const claims = 32
	type result struct {
		claim     adminservice.BootstrapClaimResult
		requestID string
		err       error
	}
	results := make(chan result, claims)
	var wait sync.WaitGroup
	for i := range claims {
		wait.Add(1)
		go func(i int) {
			defer wait.Done()
			requestID := fmt.Sprintf("bootstrap-%d", i)
			claim, err := service.ClaimBootstrap(t.Context(), adminservice.BootstrapClaimInput{
				Secret: bootstrapSecret(), Login: "first-admin", DisplayName: "First Administrator",
				RequestID: requestID,
			})
			results <- result{claim: claim, requestID: requestID, err: err}
		}(i)
	}
	wait.Wait()
	close(results)
	var success int
	var adminID uuid.UUID
	var bootstrapRecovery recovery.Created
	var winningRequestID string
	claimStarted := time.Now().UTC()
	for result := range results {
		if result.err == nil {
			success++
			adminID = result.claim.User.ID
			bootstrapRecovery = result.claim.Recovery
			winningRequestID = result.requestID
			continue
		}
		if !errors.Is(result.err, adminservice.ErrBootstrapCompleted) && !errors.Is(result.err, adminservice.ErrConflict) {
			t.Fatalf("concurrent bootstrap error = %v", result.err)
		}
	}
	if success != 1 {
		t.Fatalf("successful bootstrap claims = %d, want 1", success)
	}
	if bootstrapRecovery.ID == uuid.Nil || strings.TrimSpace(bootstrapRecovery.Plaintext) == "" {
		t.Fatalf("bootstrap recovery credential = %+v", bootstrapRecovery)
	}
	if ttl := bootstrapRecovery.ExpiresAt.Sub(claimStarted); ttl < 14*time.Minute || ttl > 16*time.Minute {
		t.Fatalf("bootstrap recovery TTL = %v, want approximately 15m", ttl)
	}
	var users, admins, audits, recoveryAudits int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM users`).Scan(&users); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM site_role_assignments WHERE role = 'site_admin'`).Scan(&admins); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_events WHERE action = 'bootstrap.claim'`).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_events WHERE action = 'recovery.mint'`).Scan(&recoveryAudits); err != nil {
		t.Fatal(err)
	}
	if users != 1 || admins != 1 || audits != 1 || recoveryAudits != 1 {
		t.Fatalf("bootstrap rows users=%d admins=%d audits=%d recovery_audits=%d", users, admins, audits, recoveryAudits)
	}
	var sharedRequestIDs int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_events
		WHERE action IN ('bootstrap.claim', 'recovery.mint') AND request_id = $1`, winningRequestID).Scan(&sharedRequestIDs); err != nil {
		t.Fatal(err)
	}
	if sharedRequestIDs != 2 {
		t.Fatalf("bootstrap request ID audit rows = %d, want 2 for %q", sharedRequestIDs, winningRequestID)
	}
	bootstrapPrincipal, err := recovery.New(pool, secrets).Consume(t.Context(), bootstrapRecovery.Plaintext, "bootstrap-recovery-consume")
	if err != nil || bootstrapPrincipal.User.ID != adminID || !bootstrapPrincipal.HasScope("site:admin") {
		t.Fatalf("bootstrap recovery consume = %+v, %v", bootstrapPrincipal, err)
	}
	if _, err := recovery.New(pool, secrets).Consume(t.Context(), bootstrapRecovery.Plaintext, "bootstrap-recovery-reuse"); !errors.Is(err, serverauth.ErrInvalidCredential) {
		t.Fatalf("bootstrap recovery reuse error = %v", err)
	}
	status, err = service.BootstrapStatus(t.Context())
	if err != nil || !status.Completed || status.Available || status.CompletedByID == nil || *status.CompletedByID != adminID {
		t.Fatalf("completed BootstrapStatus = %+v, %v", status, err)
	}
}

func TestBootstrapRecoveryFailureRollsBackEntireClaim(t *testing.T) {
	pool := migratedPool(t)
	service, err := adminservice.New(pool, []byte(bootstrapSecret()), testSecrets(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `CREATE FUNCTION reject_bootstrap_recovery_audit() RETURNS trigger LANGUAGE plpgsql AS $body$
		BEGIN
			IF NEW.action = 'recovery.mint' THEN
				RAISE EXCEPTION 'injected recovery audit failure';
			END IF;
			RETURN NEW;
		END;
		$body$`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `CREATE TRIGGER reject_bootstrap_recovery_audit BEFORE INSERT ON audit_events
		FOR EACH ROW EXECUTE FUNCTION reject_bootstrap_recovery_audit()`); err != nil {
		t.Fatal(err)
	}
	input := adminservice.BootstrapClaimInput{Secret: bootstrapSecret(), Login: "rollback-admin",
		DisplayName: "Rollback Administrator", RequestID: "bootstrap-recovery-rollback"}
	if _, err := service.ClaimBootstrap(t.Context(), input); err == nil {
		t.Fatal("bootstrap unexpectedly succeeded through recovery audit failure")
	}
	var users, admins, recoveryCredentials, completed int
	if err := pool.QueryRow(t.Context(), `SELECT
		(SELECT count(*) FROM users),
		(SELECT count(*) FROM site_role_assignments),
		(SELECT count(*) FROM recovery_credentials),
		(SELECT count(*) FROM bootstrap_state WHERE completed)`).
		Scan(&users, &admins, &recoveryCredentials, &completed); err != nil {
		t.Fatal(err)
	}
	if users != 0 || admins != 0 || recoveryCredentials != 0 || completed != 0 {
		t.Fatalf("partial bootstrap rows users=%d admins=%d recovery=%d completed=%d",
			users, admins, recoveryCredentials, completed)
	}
	if _, err := pool.Exec(t.Context(), `DROP TRIGGER reject_bootstrap_recovery_audit ON audit_events`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `DROP FUNCTION reject_bootstrap_recovery_audit()`); err != nil {
		t.Fatal(err)
	}
	claim, err := service.ClaimBootstrap(t.Context(), input)
	if err != nil || claim.User.Login != "rollback-admin" || claim.Recovery.Plaintext == "" {
		t.Fatalf("bootstrap after rollback = %+v, %v", claim, err)
	}
}

func TestTenantLifecycleSoftArchiveCASAndCrossOrgIsolation(t *testing.T) {
	pool := migratedPool(t)
	service, adminUser := newService(t, pool)
	actor := actor(adminUser.ID, "tenant-lifecycle")
	orgA := createOrganization(t, service, actor, "org-a")
	actor.RequestID = "create-org-b"
	orgB := createOrganization(t, service, actor, "org-b")
	actor.RequestID = "create-repo-a"
	repoA := createRepository(t, service, actor, orgA.ID, "repo-a")
	actor.RequestID = "create-repo-b"
	repoB := createRepository(t, service, actor, orgB.ID, "repo-b")

	var siteAudits, orgAudits, repoAudits int
	if err := pool.QueryRow(t.Context(), `SELECT
		count(*) FILTER (WHERE action = 'bootstrap.claim' AND organization_id IS NULL AND repository_id IS NULL),
		count(*) FILTER (WHERE action = 'organization.create' AND organization_id IS NOT NULL AND repository_id IS NULL),
		count(*) FILTER (WHERE action = 'repository.create' AND organization_id IS NOT NULL AND repository_id IS NOT NULL)
		FROM audit_events`).Scan(&siteAudits, &orgAudits, &repoAudits); err != nil {
		t.Fatal(err)
	}
	if siteAudits != 1 || orgAudits != 2 || repoAudits != 2 {
		t.Fatalf("audit scopes site=%d org=%d repo=%d", siteAudits, orgAudits, repoAudits)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO audit_events
		(id, organization_id, repository_id, actor_identity_key, action, resource_type, request_id)
		VALUES ($1, $2, $3, 'test:cross-tenant', 'test.cross_tenant', 'repository', 'cross-tenant-audit')`,
		uuid.New(), orgA.ID, repoB.ID); err == nil {
		t.Fatal("cross-tenant repository audit unexpectedly succeeded")
	}

	base := models.BasePermissionWrite
	display := "Organization A"
	actor.RequestID = "update-org-a"
	updatedOrg, err := service.UpdateOrganization(t.Context(), actor, orgA.ID, adminservice.UpdateOrganizationInput{
		DisplayName: &display, BasePermission: &base, ExpectedVersion: orgA.RepresentationVersion,
	})
	if err != nil || updatedOrg.BasePermission != models.BasePermissionWrite {
		t.Fatalf("UpdateOrganization = %+v, %v", updatedOrg, err)
	}
	if _, err := service.UpdateOrganization(t.Context(), actor, orgA.ID, adminservice.UpdateOrganizationInput{
		DisplayName: &display, ExpectedVersion: orgA.RepresentationVersion,
	}); !errors.Is(err, adminservice.ErrVersionConflict) {
		t.Fatalf("stale organization update error = %v", err)
	}

	visibility := models.VisibilityPublic
	policy := models.ContributionAuthenticated
	actor.RequestID = "update-repo-a"
	updatedRepo, err := service.UpdateRepository(t.Context(), actor, repoA.Scope, adminservice.UpdateRepositoryInput{
		Visibility: &visibility, ContributionPolicy: &policy, ExpectedVersion: repoA.RepresentationVersion,
	})
	if err != nil || updatedRepo.Visibility != visibility || updatedRepo.ContributionPolicy != policy {
		t.Fatalf("UpdateRepository = %+v, %v", updatedRepo, err)
	}
	if _, err := service.GetRepository(t.Context(), models.RepoScope{OrgID: orgB.ID, RepoID: repoA.ID}, false); !errors.Is(err, adminservice.ErrNotFound) {
		t.Fatalf("cross-org repository read error = %v", err)
	}

	memberID := insertUser(t, pool, "member")
	actor.RequestID = "invite-member"
	membership, err := service.InviteMembership(t.Context(), actor, orgA.ID,
		adminservice.InviteMembershipInput{UserID: memberID, Role: "member"})
	if err != nil || membership.State != models.MembershipActive || membership.ActivatedAt == nil {
		t.Fatalf("InviteMembership = %+v, %v", membership, err)
	}
	actor.RequestID = "change-member-role"
	membership, err = service.UpdateMembership(t.Context(), actor, orgA.ID, membership.ID,
		adminservice.UpdateMembershipInput{Role: "maintainer", State: models.MembershipActive,
			ExpectedVersion: membership.RepresentationVersion})
	if err != nil || membership.Role != "maintainer" || membership.State != models.MembershipActive || membership.ActivatedAt == nil {
		t.Fatalf("UpdateMembership = %+v, %v", membership, err)
	}
	actor.RequestID = "archive-member"
	if err := service.ArchiveMembership(t.Context(), actor, orgA.ID, membership.ID, membership.RepresentationVersion); err != nil {
		t.Fatalf("ArchiveMembership = %v", err)
	}
	actor.RequestID = "readd-member"
	membership, err = service.InviteMembership(t.Context(), actor, orgA.ID,
		adminservice.InviteMembershipInput{UserID: memberID, Role: "member"})
	if err != nil || membership.State != models.MembershipActive || membership.ActivatedAt == nil {
		t.Fatalf("ReaddMembership = %+v, %v", membership, err)
	}
	ownerMembership := findMembership(t, service, orgA.ID, adminUser.ID)
	actor.RequestID = "reinvite-owner-as-reader"
	if _, err := service.InviteMembership(t.Context(), actor, orgA.ID,
		adminservice.InviteMembershipInput{UserID: adminUser.ID, Role: "reader"}); !errors.Is(err, adminservice.ErrConflict) {
		t.Fatalf("re-invite live owner error = %v", err)
	}
	ownerAfterInvite := findMembership(t, service, orgA.ID, adminUser.ID)
	if ownerAfterInvite.ID != ownerMembership.ID || ownerAfterInvite.Role != "owner" || ownerAfterInvite.State != models.MembershipActive {
		t.Fatalf("owner changed after re-invite: before=%+v after=%+v", ownerMembership, ownerAfterInvite)
	}
	var ownerRows int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM org_memberships
		WHERE organization_id = $1 AND user_id = $2 AND role = 'owner' AND state = 'active' AND archived_at IS NULL`,
		orgA.ID, adminUser.ID).Scan(&ownerRows); err != nil || ownerRows != 1 {
		t.Fatalf("live owner rows = %d, %v", ownerRows, err)
	}
	actor.RequestID = "archive-last-owner"
	if err := service.ArchiveMembership(t.Context(), actor, orgA.ID, ownerMembership.ID,
		ownerMembership.RepresentationVersion); !errors.Is(err, adminservice.ErrLastOrganizationOwner) {
		t.Fatalf("last owner archive error = %v", err)
	}
	emptyCollaborators, err := service.ListCollaborators(t.Context(), repoA.Scope, false)
	if err != nil || emptyCollaborators == nil || len(emptyCollaborators) != 0 {
		t.Fatalf("empty collaborators = %#v, %v; want non-nil empty slice", emptyCollaborators, err)
	}

	actor.RequestID = "collaborator-upsert"
	collaborator, err := service.UpsertCollaborator(t.Context(), actor, repoA.Scope,
		adminservice.UpsertCollaboratorInput{UserID: memberID, Role: "write"})
	if err != nil {
		t.Fatal(err)
	}
	actor.RequestID = "cross-org-collaborator-archive"
	if err := service.ArchiveCollaborator(t.Context(), actor,
		models.RepoScope{OrgID: orgB.ID, RepoID: repoB.ID}, collaborator.ID,
		collaborator.RepresentationVersion); !errors.Is(err, adminservice.ErrNotFound) {
		t.Fatalf("cross-org collaborator archive error = %v", err)
	}
	currentCollaborators, err := service.ListCollaborators(t.Context(), repoA.Scope, false)
	if err != nil || len(currentCollaborators) != 1 {
		t.Fatalf("collaborator changed after cross-org guess: %+v, %v", currentCollaborators, err)
	}

	actor.RequestID = "archive-repo-a"
	if err := service.ArchiveRepository(t.Context(), actor, repoA.Scope, updatedRepo.RepresentationVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := service.GetRepository(t.Context(), repoA.Scope, false); !errors.Is(err, adminservice.ErrNotFound) {
		t.Fatalf("archived repository visible: %v", err)
	}
	actor.RequestID = "archive-org-b"
	if err := service.ArchiveOrganization(t.Context(), actor, orgB.ID, orgB.RepresentationVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := service.GetOrganization(t.Context(), orgB.ID, false); !errors.Is(err, adminservice.ErrNotFound) {
		t.Fatalf("archived organization visible: %v", err)
	}

	var mutationAudits int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_events WHERE action IN
		('organization.create','organization.update','organization.archive','repository.create',
		'repository.update','repository.archive','membership.invite','membership.update','collaborator.upsert')`).Scan(&mutationAudits); err != nil {
		t.Fatal(err)
	}
	if mutationAudits < 10 {
		t.Fatalf("lifecycle audit count = %d, want at least 10", mutationAudits)
	}
}

func TestServiceAccountManagedPATRecoveryAndRollback(t *testing.T) {
	pool := migratedPool(t)
	service, adminUser := newService(t, pool)
	secrets := testSecrets(t)
	actor := actor(adminUser.ID, "credential-org")
	org := createOrganization(t, service, actor, "credential-org")
	actor.RequestID = "other-org"
	otherOrg := createOrganization(t, service, actor, "other-org")
	actor.RequestID = "credential-repo"
	repo := createRepository(t, service, actor, org.ID, "evidence-repo")
	actor.RequestID = "other-credential-repo"
	otherRepo := createRepository(t, service, actor, otherOrg.ID, "other-evidence-repo")
	emptyAccounts, err := service.ListServiceAccounts(t.Context(), org.ID, false)
	if err != nil || emptyAccounts == nil || len(emptyAccounts) != 0 {
		t.Fatalf("empty ListServiceAccounts = %+v, %v", emptyAccounts, err)
	}
	actor.RequestID = "service-account-create"
	account, err := service.CreateServiceAccount(t.Context(), actor, org.ID,
		adminservice.CreateServiceAccountInput{Name: "evidence-writer"})
	if err != nil {
		t.Fatal(err)
	}

	actor.RequestID = "managed-pat-create"
	created, err := service.CreateManagedPAT(t.Context(), actor, org.ID, adminservice.CreateManagedPATInput{
		TargetUserID: account.UserID, Name: "writer", Scopes: []string{"evidence:write", "issues:read"},
		RepositoryIDs: []uuid.UUID{repo.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	principal, err := pat.New(pool, secrets).AuthenticateBearer(t.Context(), created.Plaintext)
	if err != nil || !principal.HasScope("evidence:write") || !principal.AllowsRepository(org.ID, repo.ID) {
		t.Fatalf("managed PAT auth = %+v, %v", principal, err)
	}
	listed, err := service.ListManagedPATs(t.Context(), org.ID, account.UserID)
	if err != nil || len(listed) != 1 || listed[0].Scopes[0] != "evidence:write" || len(listed[0].RepositoryIDs) != 1 {
		t.Fatalf("ListManagedPATs = %+v, %v", listed, err)
	}
	actor.RequestID = "site-wide-managed-pat-create"
	siteWide, err := service.CreateManagedPAT(t.Context(), actor, org.ID, adminservice.CreateManagedPATInput{
		TargetUserID: account.UserID, Name: "site-wide", Scopes: []string{"issues:read"}, RepositoryIDs: []uuid.UUID{},
	})
	if err != nil || len(siteWide.RepositoryIDs) != 0 {
		t.Fatalf("site-wide managed PAT = %+v, %v", siteWide, err)
	}
	siteWidePrincipal, err := pat.New(pool, secrets).AuthenticateBearer(t.Context(), siteWide.Plaintext)
	if err != nil || siteWidePrincipal.RepoRestricted || !siteWidePrincipal.AllowsRepository(org.ID, repo.ID) ||
		!siteWidePrincipal.AllowsRepository(otherOrg.ID, otherRepo.ID) {
		t.Fatalf("site-wide managed PAT auth = %+v, %v", siteWidePrincipal, err)
	}
	authorizer, err := authz.New(pool)
	if err != nil {
		t.Fatal(err)
	}
	decision, err := authorizer.EvaluateRepository(t.Context(), authz.Authenticated(siteWidePrincipal), authz.RepositoryRequest{
		Scope: models.RepoScope{OrgID: otherOrg.ID, RepoID: otherRepo.ID}, Operation: authz.OperationRead,
	})
	if err != nil || decision.Allowed {
		t.Fatalf("site-wide managed PAT cross-org authorization = %+v, %v", decision, err)
	}
	actor.RequestID = "managed-pat-rotate"
	rotated, err := service.RotateManagedPAT(t.Context(), actor, org.ID, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pat.New(pool, secrets).AuthenticateBearer(t.Context(), created.Plaintext); !errors.Is(err, serverauth.ErrRevokedCredential) {
		t.Fatalf("old managed PAT error = %v", err)
	}
	if _, err := pat.New(pool, secrets).AuthenticateBearer(t.Context(), rotated.Plaintext); err != nil {
		t.Fatalf("rotated managed PAT invalid: %v", err)
	}
	actor.RequestID = "cross-org-pat-revoke"
	if err := service.RevokeManagedPAT(t.Context(), actor, otherOrg.ID, rotated.ID); !errors.Is(err, adminservice.ErrNotFound) {
		t.Fatalf("cross-org PAT revoke error = %v", err)
	}
	if _, err := pat.New(pool, secrets).AuthenticateBearer(t.Context(), rotated.Plaintext); err != nil {
		t.Fatalf("cross-org PAT revoke changed token: %v", err)
	}

	if _, err := pool.Exec(t.Context(), `CREATE FUNCTION reject_service_disable_audit() RETURNS trigger LANGUAGE plpgsql AS $body$
		BEGIN
			IF NEW.action = 'service_account.disable' THEN
				RAISE EXCEPTION 'injected audit failure';
			END IF;
			RETURN NEW;
		END;
		$body$`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `CREATE TRIGGER reject_service_disable_audit BEFORE INSERT ON audit_events
		FOR EACH ROW EXECUTE FUNCTION reject_service_disable_audit()`); err != nil {
		t.Fatal(err)
	}
	actor.RequestID = "service-disable-rollback"
	if err := service.DisableServiceAccount(t.Context(), actor, org.ID, account.ID); err == nil {
		t.Fatal("DisableServiceAccount unexpectedly succeeded through audit failure")
	}
	var userStatus string
	var disabledAt *time.Time
	if err := pool.QueryRow(t.Context(), `SELECT u.status, sa.disabled_at FROM service_accounts sa
		JOIN users u ON u.id = sa.user_id WHERE sa.organization_id = $1 AND sa.id = $2`, org.ID, account.ID).
		Scan(&userStatus, &disabledAt); err != nil || userStatus != "active" || disabledAt != nil {
		t.Fatalf("failed disable partially committed: status=%q disabled=%v err=%v", userStatus, disabledAt, err)
	}
	if _, err := pool.Exec(t.Context(), `DROP TRIGGER reject_service_disable_audit ON audit_events`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `DROP FUNCTION reject_service_disable_audit()`); err != nil {
		t.Fatal(err)
	}
	actor.RequestID = "cross-org-service-disable"
	if err := service.DisableServiceAccount(t.Context(), actor, otherOrg.ID, account.ID); !errors.Is(err, adminservice.ErrNotFound) {
		t.Fatalf("cross-org service account disable error = %v", err)
	}
	actor.RequestID = "service-disable"
	if err := service.DisableServiceAccount(t.Context(), actor, org.ID, account.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pat.New(pool, secrets).AuthenticateBearer(t.Context(), rotated.Plaintext); !errors.Is(err, serverauth.ErrDisabledAccount) && !errors.Is(err, serverauth.ErrRevokedCredential) {
		t.Fatalf("service account disable PAT error = %v", err)
	}

	recovered, err := service.RecoverAdmin(t.Context(), adminUser.ID, "root@console", "identity provider outage",
		"offline-recovery", 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	recoveryPrincipal, err := recovery.New(pool, secrets).Consume(t.Context(), recovered.Plaintext, "consume-recovery")
	if err != nil || !recoveryPrincipal.HasScope("site:admin") {
		t.Fatalf("offline recovery = %+v, %v", recoveryPrincipal, err)
	}
}

func TestSiteWideManagedPATIssuancePreservesIssuerTenantBoundary(t *testing.T) {
	pool := migratedPool(t)
	service, siteAdmin := newService(t, pool)
	siteActor := actor(siteAdmin.ID, "site-wide-boundary-org-a")
	orgA := createOrganization(t, service, siteActor, "site-wide-boundary-a")
	siteActor.RequestID = "site-wide-boundary-org-b"
	orgB := createOrganization(t, service, siteActor, "site-wide-boundary-b")
	siteActor.RequestID = "site-wide-boundary-repo-a"
	repoA := createRepository(t, service, siteActor, orgA.ID, "repo-a")
	siteActor.RequestID = "site-wide-boundary-repo-b"
	repoB := createRepository(t, service, siteActor, orgB.ID, "repo-b")

	targetUserID := insertUser(t, pool, "site-wide-target")
	orgAdminID := insertUser(t, pool, "site-wide-org-admin")
	addActiveMembership(t, pool, orgA.ID, targetUserID, "member")
	addActiveMembership(t, pool, orgB.ID, targetUserID, "member")
	addActiveMembership(t, pool, orgA.ID, orgAdminID, "owner")
	orgActor := actor(orgAdminID, "restricted-human-managed-pat")

	if _, err := service.CreateManagedPAT(t.Context(), orgActor, orgA.ID, adminservice.CreateManagedPATInput{
		TargetUserID: targetUserID, Name: "restricted-human", Scopes: []string{"issues:read"},
		RepositoryIDs: []uuid.UUID{repoA.ID},
	}); err != nil {
		t.Fatalf("organization administrator restricted human PAT error = %v", err)
	}
	orgActor.RequestID = "site-wide-human-managed-pat-denied"
	if _, err := service.CreateManagedPAT(t.Context(), orgActor, orgA.ID, adminservice.CreateManagedPATInput{
		TargetUserID: targetUserID, Name: "site-wide-human-denied", Scopes: []string{"issues:read"},
	}); !errors.Is(err, adminservice.ErrForbidden) {
		t.Fatalf("organization administrator site-wide human PAT error = %v", err)
	}

	siteActor.RequestID = "site-wide-service-account-create"
	account, err := service.CreateServiceAccount(t.Context(), siteActor, orgA.ID,
		adminservice.CreateServiceAccountInput{Name: "site-wide-runner"})
	if err != nil {
		t.Fatal(err)
	}
	orgActor.RequestID = "site-wide-service-account-managed-pat"
	if _, err := service.CreateManagedPAT(t.Context(), orgActor, orgA.ID, adminservice.CreateManagedPATInput{
		TargetUserID: account.UserID, Name: "site-wide-service-account", Scopes: []string{"issues:read"},
	}); err != nil {
		t.Fatalf("organization administrator site-wide service-account PAT error = %v", err)
	}

	siteActor.RequestID = "site-wide-human-managed-pat"
	siteWide, err := service.CreateManagedPAT(t.Context(), siteActor, orgA.ID, adminservice.CreateManagedPATInput{
		TargetUserID: targetUserID, Name: "site-wide-human", Scopes: []string{"issues:read"},
	})
	if err != nil {
		t.Fatal(err)
	}
	principal, err := pat.New(pool, testSecrets(t)).AuthenticateBearer(t.Context(), siteWide.Plaintext)
	if err != nil || principal.RepoRestricted {
		t.Fatalf("site-wide human PAT principal = %+v, %v", principal, err)
	}
	authorizer, err := authz.New(pool)
	if err != nil {
		t.Fatal(err)
	}
	decision, err := authorizer.EvaluateRepository(t.Context(), authz.Authenticated(principal), authz.RepositoryRequest{
		Scope: models.RepoScope{OrgID: orgB.ID, RepoID: repoB.ID}, Operation: authz.OperationRead,
	})
	if err != nil || !decision.Allowed {
		t.Fatalf("site-wide human PAT live cross-org permission = %+v, %v", decision, err)
	}

	orgActor.RequestID = "site-wide-human-rotate-denied"
	if _, err := service.RotateManagedPAT(t.Context(), orgActor, orgA.ID, siteWide.ID); !errors.Is(err, adminservice.ErrForbidden) {
		t.Fatalf("organization administrator site-wide human PAT rotation error = %v", err)
	}
	if _, err := pat.New(pool, testSecrets(t)).AuthenticateBearer(t.Context(), siteWide.Plaintext); err != nil {
		t.Fatalf("denied rotation changed original PAT: %v", err)
	}
	siteActor.RequestID = "site-wide-human-rotate"
	if _, err := service.RotateManagedPAT(t.Context(), siteActor, orgA.ID, siteWide.ID); err != nil {
		t.Fatalf("site administrator site-wide human PAT rotation error = %v", err)
	}
}

func newService(t *testing.T, pool *pgxpool.Pool) (*adminservice.Service, serverauth.User) {
	t.Helper()
	service, err := adminservice.New(pool, []byte(bootstrapSecret()), testSecrets(t))
	if err != nil {
		t.Fatal(err)
	}
	claim, err := service.ClaimBootstrap(t.Context(), adminservice.BootstrapClaimInput{
		Secret: bootstrapSecret(), Login: "admin-" + uuid.NewString()[:8], DisplayName: "Administrator",
		RequestID: "bootstrap-setup",
	})
	if err != nil {
		t.Fatal(err)
	}
	return service, claim.User
}

func bootstrapSecret() string { return strings.Repeat("bootstrap-secret-", 3) }

func testSecrets(t *testing.T) *serverauth.Secrets {
	t.Helper()
	secrets, err := serverauth.NewSecrets([]byte(strings.Repeat("p", 32)), []byte(strings.Repeat("e", 32)))
	if err != nil {
		t.Fatal(err)
	}
	return secrets
}

func actor(userID uuid.UUID, requestID string) adminservice.Actor {
	return adminservice.Actor{UserID: userID, IdentityKey: "user:" + userID.String(), RequestID: requestID}
}

func createOrganization(t *testing.T, service *adminservice.Service, actor adminservice.Actor, name string) models.AdminOrganization {
	t.Helper()
	organization, err := service.CreateOrganization(t.Context(), actor, adminservice.CreateOrganizationInput{
		Name: name, DisplayName: name, BasePermission: models.BasePermissionRead,
	})
	if err != nil {
		t.Fatal(err)
	}
	return organization
}

func createRepository(t *testing.T, service *adminservice.Service, actor adminservice.Actor, orgID uuid.UUID, name string) models.AdminRepository {
	t.Helper()
	repository, err := service.CreateRepository(t.Context(), actor, orgID, adminservice.CreateRepositoryInput{
		Name: name, DisplayName: name, Visibility: models.VisibilityPrivate,
		DefaultBranch: "main", ContributionPolicy: models.ContributionMembers,
	})
	if err != nil {
		t.Fatal(err)
	}
	return repository
}

func findMembership(t *testing.T, service *adminservice.Service, orgID, userID uuid.UUID) models.AdminMembership {
	t.Helper()
	items, err := service.ListMemberships(t.Context(), orgID, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.UserID == userID {
			return item
		}
	}
	t.Fatalf("membership for %s not found", userID)
	return models.AdminMembership{}
}

func insertUser(t *testing.T, pool *pgxpool.Pool, login string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := pool.Exec(t.Context(), `INSERT INTO users (id, login, display_name) VALUES ($1, $2, $2)`, id, login); err != nil {
		t.Fatal(err)
	}
	return id
}

func addActiveMembership(t *testing.T, pool *pgxpool.Pool, orgID, userID uuid.UUID, role string) {
	t.Helper()
	if _, err := pool.Exec(t.Context(), `INSERT INTO org_memberships
		(id, organization_id, user_id, role, state, activated_at)
		VALUES ($1, $2, $3, $4, 'active', clock_timestamp())`, uuid.New(), orgID, userID, role); err != nil {
		t.Fatal(err)
	}
}

func migratedPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	databaseURL := strings.TrimSpace(os.Getenv(testDatabaseEnv))
	if databaseURL == "" {
		t.Skipf("set %s to run PostgreSQL integration tests", testDatabaseEnv)
	}
	adminConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	adminPool, err := pgxpool.NewWithConfig(t.Context(), adminConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(adminPool.Close)
	schema := "admin_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
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
