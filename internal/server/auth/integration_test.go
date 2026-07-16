package auth_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/capability"
	"github.com/higress-group/issue-spec/internal/server/api/github/codec"
	nativeauth "github.com/higress-group/issue-spec/internal/server/api/native/auth"
	"github.com/higress-group/issue-spec/internal/server/api/routeset"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
	"github.com/higress-group/issue-spec/internal/server/auth/delegation"
	"github.com/higress-group/issue-spec/internal/server/auth/pat"
	"github.com/higress-group/issue-spec/internal/server/auth/recovery"
	"github.com/higress-group/issue-spec/internal/server/auth/serviceaccount"
	"github.com/higress-group/issue-spec/internal/server/auth/session"
	"github.com/higress-group/issue-spec/internal/server/authz"
	"github.com/higress-group/issue-spec/internal/server/models"
	"github.com/higress-group/issue-spec/internal/server/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const testDatabaseEnv = "TEST_DATABASE_URL"

func TestIdentitySessionPATDelegationAndDisableLifecycle(t *testing.T) {
	pool := migratedPool(t)
	secrets := testSecrets(t)
	identities := serverauth.NewIdentityService(pool)
	sessions, err := session.New(pool, secrets, session.Config{Secure: true, IdleTTL: time.Hour, AbsoluteTTL: 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	pats := pat.New(pool, secrets)
	delegated := delegation.New(pool, secrets)

	providerA := insertProvider(t, pool, "oidc-a", "oidc", "https://idp-a.example")
	providerB := insertProvider(t, pool, "oidc-b", "oidc", "https://idp-b.example")
	sharedEmail := "shared@example.test"
	userA, err := identities.ResolveOrProvision(t.Context(), providerA, serverauth.ExternalIdentity{
		Issuer: providerA.Issuer, Subject: "subject-a", Login: "Alice", DisplayName: "Alice A",
		Email: &sharedEmail, AvatarURL: "https://avatars.example/alice-v1.png", Claims: json.RawMessage(`{"sub":"subject-a"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	changedEmail := "changed@example.test"
	again, err := identities.ResolveOrProvision(t.Context(), providerA, serverauth.ExternalIdentity{
		Issuer: providerA.Issuer, Subject: "subject-a", Login: "renamed", DisplayName: "Renamed",
		Email: &changedEmail, AvatarURL: "https://avatars.example/alice-v2.png", Claims: json.RawMessage(`{"sub":"subject-a","name":"Renamed"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != userA.ID || again.Login != userA.Login || again.DisplayName != "Renamed" {
		t.Fatalf("provider display change moved local identity: first=%+v again=%+v", userA, again)
	}
	profile, err := identities.Profile(t.Context(), userA.ID)
	if err != nil {
		t.Fatal(err)
	}
	profile, err = identities.UpdateNickname(t.Context(), userA.ID, "澄潭", profile.RepresentationVersion, "req-nickname")
	if err != nil {
		t.Fatal(err)
	}
	if profile.DisplayName != "澄潭" || profile.IdentityDisplayName != "Renamed" || profile.Nickname == nil || *profile.Nickname != "澄潭" {
		t.Fatalf("updated profile = %+v", profile)
	}
	if _, err := identities.UpdateNickname(t.Context(), userA.ID, "stale", profile.RepresentationVersion-1, "req-nickname-stale"); !errors.Is(err, serverauth.ErrConflict) {
		t.Fatalf("stale nickname update error = %v, want conflict", err)
	}
	again, err = identities.ResolveOrProvision(t.Context(), providerA, serverauth.ExternalIdentity{
		Issuer: providerA.Issuer, Subject: "subject-a", Login: "renamed", DisplayName: "Company Name",
		Email: &changedEmail, AvatarURL: "https://avatars.example/alice-v3.png", Claims: json.RawMessage(`{"sub":"subject-a","name":"Company Name"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if again.DisplayName != "澄潭" {
		t.Fatalf("provider refresh overwrote nickname: %+v", again)
	}
	profile, err = identities.PublicProfile(t.Context(), userA.Login)
	if err != nil || profile.DisplayName != "澄潭" || profile.IdentityDisplayName != "Company Name" {
		t.Fatalf("public profile = %+v err=%v", profile, err)
	}
	userB, err := identities.ResolveOrProvision(t.Context(), providerB, serverauth.ExternalIdentity{
		Issuer: providerB.Issuer, Subject: "subject-b", Login: "Alice", DisplayName: "Other Alice", Email: &sharedEmail,
	})
	if err != nil {
		t.Fatal(err)
	}
	if userA.ID == userB.ID {
		t.Fatal("same provider email auto-merged two users")
	}
	if err := identities.LinkIdentity(t.Context(), userA.ID, userA.ID, providerB,
		serverauth.ExternalIdentity{Issuer: providerB.Issuer, Subject: "linked-subject", AvatarURL: "https://other.example/linked.png"}, "req-link"); err != nil {
		t.Fatal(err)
	}
	var sourceProvider uuid.UUID
	var sourceAvatar string
	if err := pool.QueryRow(t.Context(), `SELECT i.provider_id,i.avatar_url FROM users u JOIN identities i ON i.id=u.profile_identity_id WHERE u.id=$1`, userA.ID).Scan(&sourceProvider, &sourceAvatar); err != nil {
		t.Fatal(err)
	}
	if sourceProvider != providerA.ID || sourceAvatar != "https://avatars.example/alice-v2.png" {
		t.Fatalf("profile source provider=%s avatar=%q", sourceProvider, sourceAvatar)
	}
	if err := identities.LinkIdentity(t.Context(), userB.ID, userB.ID, providerB,
		serverauth.ExternalIdentity{Issuer: providerB.Issuer, Subject: "linked-subject"}, "req-link-2"); !errors.Is(err, serverauth.ErrConflict) {
		t.Fatalf("relink error = %v, want conflict", err)
	}

	createdSession, err := sessions.Create(t.Context(), userA.ID, "integration-agent", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	var sessionHash []byte
	if err := pool.QueryRow(t.Context(), `SELECT token_hash FROM sessions WHERE id = $1`, createdSession.Principal.CredentialID).Scan(&sessionHash); err != nil {
		t.Fatal(err)
	}
	if string(sessionHash) == createdSession.Token || strings.Contains(string(sessionHash), createdSession.Token) {
		t.Fatal("session plaintext was persisted")
	}
	principal, err := sessions.Authenticate(t.Context(), createdSession.Token)
	if err != nil || principal.User.ID != userA.ID {
		t.Fatalf("Authenticate(session) = %+v, %v", principal, err)
	}
	if principal.User.DisplayName != "澄潭" {
		t.Fatalf("session display name = %q, want nickname", principal.User.DisplayName)
	}
	if err := sessions.ValidateCSRF(principal, createdSession.CSRFToken); err != nil {
		t.Fatal(err)
	}
	if err := sessions.ValidateCSRF(principal, createdSession.CSRFToken+"x"); !errors.Is(err, serverauth.ErrInvalidCSRF) {
		t.Fatalf("wrong csrf error = %v", err)
	}
	rotatedSession, err := sessions.Rotate(t.Context(), createdSession.Token, "integration-agent", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if !rotatedSession.Principal.ExpiresAt.Equal(createdSession.Principal.ExpiresAt) {
		t.Fatalf("session rotation extended absolute lifetime: old=%s new=%s",
			createdSession.Principal.ExpiresAt, rotatedSession.Principal.ExpiresAt)
	}
	if _, err := sessions.Authenticate(t.Context(), createdSession.Token); !errors.Is(err, serverauth.ErrInvalidCredential) && !errors.Is(err, serverauth.ErrRevokedCredential) {
		t.Fatalf("old session remained valid after rotation: %v", err)
	}
	if _, err := sessions.Authenticate(t.Context(), rotatedSession.Token); err != nil {
		t.Fatalf("rotated session invalid: %v", err)
	}
	expiringSession, err := sessions.Create(t.Context(), userA.ID, "expiry", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE sessions SET
		created_at = clock_timestamp() - interval '3 hours',
		last_seen_at = clock_timestamp() - interval '2 hours',
		idle_expires_at = clock_timestamp() - interval '1 hour',
		absolute_expires_at = clock_timestamp() - interval '30 minutes'
		WHERE id = $1`, expiringSession.Principal.CredentialID); err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.Authenticate(t.Context(), expiringSession.Token); !errors.Is(err, serverauth.ErrExpiredCredential) {
		t.Fatalf("expired session error = %v", err)
	}
	cookie := sessions.Cookie(rotatedSession.Token)
	if !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("session cookie policy = %+v", cookie)
	}

	orgID, repoID := insertOrgRepo(t, pool, "auth-lifecycle")
	if _, err := pool.Exec(t.Context(), `INSERT INTO repo_collaborators
		(id, organization_id, repository_id, user_id, role) VALUES ($1, $2, $3, $4, 'write')`,
		uuid.New(), orgID, repoID, userA.ID); err != nil {
		t.Fatal(err)
	}
	patCreated, err := pats.Create(t.Context(), userA.ID, pat.CreateInput{
		Name: "runner", Scopes: []string{"runner:delegate", "issues:write", "read:user"},
		Repositories: []models.RepoScope{{OrgID: orgID, RepoID: repoID}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var patHash []byte
	if err := pool.QueryRow(t.Context(), `SELECT token_hash FROM personal_access_tokens WHERE id = $1`, patCreated.ID).Scan(&patHash); err != nil {
		t.Fatal(err)
	}
	if string(patHash) == patCreated.Plaintext || strings.Contains(string(patHash), patCreated.Plaintext) {
		t.Fatal("PAT plaintext was persisted")
	}
	patPrincipal, err := pats.AuthenticateBearer(t.Context(), patCreated.Plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if !patPrincipal.HasScope("issues:write") || !patPrincipal.AllowsRepository(orgID, repoID) ||
		patPrincipal.AllowsRepository(orgID, uuid.New()) {
		t.Fatalf("PAT caps are incorrect: %+v", patPrincipal)
	}
	rotatedPAT, err := pats.Rotate(t.Context(), userA.ID, patCreated.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pats.AuthenticateBearer(t.Context(), patCreated.Plaintext); !errors.Is(err, serverauth.ErrRevokedCredential) {
		t.Fatalf("old PAT error = %v, want revoked", err)
	}
	patPrincipal, err = pats.AuthenticateBearer(t.Context(), rotatedPAT.Plaintext)
	if err != nil {
		t.Fatal(err)
	}
	expiresAt := time.Now().Add(time.Hour)
	expiringPAT, err := pats.Create(t.Context(), userA.ID, pat.CreateInput{Name: "expiring", Scopes: []string{"read:user"}, ExpiresAt: &expiresAt})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE personal_access_tokens SET
		created_at = clock_timestamp() - interval '2 hours',
		updated_at = clock_timestamp() - interval '2 hours',
		expires_at = clock_timestamp() - interval '1 hour' WHERE id = $1`, expiringPAT.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pats.AuthenticateBearer(t.Context(), expiringPAT.Plaintext); !errors.Is(err, serverauth.ErrExpiredCredential) {
		t.Fatalf("expired PAT error = %v", err)
	}

	delegatedCreated, err := delegated.Issue(t.Context(), delegation.IssueInput{
		Issuer: patPrincipal, Repo: models.RepoScope{OrgID: orgID, RepoID: repoID}, JobID: "job-1",
		Purpose: "comment-writeback", Audience: "issue-spec-server", Subject: "runner-child",
		Scopes: []string{"issues:write"}, Operations: []capability.Operation{capability.OperationIssueRead,
			capability.OperationArtifactWrite}, TTL: delegation.DefaultTTL,
	})
	if err != nil {
		t.Fatal(err)
	}
	if remaining := time.Until(delegatedCreated.ExpiresAt); remaining < delegation.DefaultTTL-10*time.Second || remaining > delegation.DefaultTTL {
		t.Fatalf("delegated default lifetime = %s, want approximately %s", remaining, delegation.DefaultTTL)
	}
	delegatedPrincipal, err := delegated.Authenticate(t.Context(), delegatedCreated.Plaintext, delegation.Expected{
		Repo: models.RepoScope{OrgID: orgID, RepoID: repoID}, JobID: "job-1",
		Purpose: "comment-writeback", Audience: "issue-spec-server",
	})
	if err != nil || !delegatedPrincipal.HasScope("issues:write") ||
		!delegatedPrincipal.HasOperation(string(capability.OperationArtifactWrite)) ||
		delegatedPrincipal.HasOperation(string(capability.OperationIssueCommentWrite)) {
		t.Fatalf("delegated auth = %+v, %v", delegatedPrincipal, err)
	}
	if _, err := delegated.Authenticate(t.Context(), delegatedCreated.Plaintext,
		delegation.Expected{Repo: models.RepoScope{OrgID: orgID, RepoID: repoID}, JobID: "other"}); !errors.Is(err, serverauth.ErrInsufficientScope) {
		t.Fatalf("delegated job mix-up error = %v", err)
	}
	revokedDelegated, err := delegated.Issue(t.Context(), delegation.IssueInput{
		Issuer: patPrincipal, Repo: models.RepoScope{OrgID: orgID, RepoID: repoID}, JobID: "job-revoke",
		Purpose: "comment-writeback", Audience: "issue-spec-server", Subject: "runner-child",
		Scopes: []string{"issues:write"}, TTL: 5 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := delegated.Revoke(t.Context(), revokedDelegated.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := delegated.AuthenticateBearer(t.Context(), revokedDelegated.Plaintext); !errors.Is(err, serverauth.ErrRevokedCredential) {
		t.Fatalf("revoked delegated token error = %v", err)
	}
	jobDelegated, err := delegated.Issue(t.Context(), delegation.IssueInput{
		Issuer: patPrincipal, Repo: models.RepoScope{OrgID: orgID, RepoID: repoID}, JobID: "job-revoke-all",
		Purpose: "comment-writeback", Audience: "issue-spec-server", Subject: "runner-child",
		Scopes: []string{"issues:write"}, TTL: 5 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := delegated.RevokeJob(t.Context(), models.RepoScope{OrgID: orgID, RepoID: repoID}, "job-revoke-all"); err != nil {
		t.Fatal(err)
	}
	if _, err := delegated.AuthenticateBearer(t.Context(), jobDelegated.Plaintext); !errors.Is(err, serverauth.ErrRevokedCredential) {
		t.Fatalf("job-revoked delegated token error = %v", err)
	}
	mismatchOnly, err := delegated.Issue(t.Context(), delegation.IssueInput{Issuer: patPrincipal,
		Repo: models.RepoScope{OrgID: orgID, RepoID: repoID}, JobID: "job-mismatch-only", Purpose: "issue-api",
		Audience: "issue-spec-server", Subject: "runner-child", Scopes: []string{"issues:write"}, TTL: 5 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := delegated.Authenticate(t.Context(), mismatchOnly.Plaintext, delegation.Expected{JobID: "wrong-job"}); !errors.Is(err, serverauth.ErrInsufficientScope) {
		t.Fatalf("mismatch-only auth error = %v", err)
	}
	var mismatchUsedAt *time.Time
	if err := pool.QueryRow(t.Context(), `SELECT used_at FROM delegated_tokens WHERE id = $1`, mismatchOnly.ID).Scan(&mismatchUsedAt); err != nil || mismatchUsedAt != nil {
		t.Fatalf("wrong expected binding marked token used: used_at=%v err=%v", mismatchUsedAt, err)
	}
	uniqueInput := delegation.IssueInput{Issuer: patPrincipal, Repo: models.RepoScope{OrgID: orgID, RepoID: repoID},
		JobID: "job-unique", Purpose: "issue-api", Audience: "issue-spec-server", Subject: "runner-child",
		Scopes: []string{"issues:write"}, TTL: 5 * time.Minute}
	uniqueFirst, err := delegated.Issue(t.Context(), uniqueInput)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := delegated.Issue(t.Context(), uniqueInput); !errors.Is(err, serverauth.ErrConflict) {
		t.Fatalf("duplicate active lease error = %v", err)
	}
	uniqueInput.Replace = true
	if _, err := delegated.Issue(t.Context(), uniqueInput); err != nil {
		t.Fatal(err)
	}
	if _, err := delegated.AuthenticateBearer(t.Context(), uniqueFirst.Plaintext); !errors.Is(err, serverauth.ErrRevokedCredential) {
		t.Fatalf("replaced delegated token error = %v", err)
	}
	concurrentInput := uniqueInput
	concurrentInput.JobID = "job-concurrent"
	concurrentInput.Replace = false
	type issueResult struct{ err error }
	results := make(chan issueResult, 8)
	for range 8 {
		go func() {
			_, issueErr := delegated.Issue(context.Background(), concurrentInput)
			results <- issueResult{err: issueErr}
		}()
	}
	var concurrentSuccess, concurrentConflict int
	for range 8 {
		result := <-results
		switch {
		case result.err == nil:
			concurrentSuccess++
		case errors.Is(result.err, serverauth.ErrConflict):
			concurrentConflict++
		default:
			t.Fatalf("concurrent issue error = %v", result.err)
		}
	}
	if concurrentSuccess != 1 || concurrentConflict != 7 {
		t.Fatalf("concurrent issue success=%d conflict=%d", concurrentSuccess, concurrentConflict)
	}
	var delegatedAuditCount int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_events WHERE action LIKE 'delegated_token.%'`).Scan(&delegatedAuditCount); err != nil || delegatedAuditCount != 9 {
		t.Fatalf("delegated audit count = %d, %v", delegatedAuditCount, err)
	}
	var leakedAuditCount int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_events WHERE metadata::text LIKE $1`, "%"+delegatedCreated.Plaintext+"%").Scan(&leakedAuditCount); err != nil || leakedAuditCount != 0 {
		t.Fatalf("delegated secret audit leak count = %d, %v", leakedAuditCount, err)
	}

	assertDelegatedLiveParent := func(name string, mutate func()) {
		t.Helper()
		created, issueErr := delegated.Issue(t.Context(), delegation.IssueInput{
			Issuer: patPrincipal, Repo: models.RepoScope{OrgID: orgID, RepoID: repoID}, JobID: "job-live-" + name,
			Purpose: "comment-writeback", Audience: "issue-spec-server", Subject: "runner-child",
			Scopes: []string{"issues:write"}, TTL: 5 * time.Minute,
		})
		if issueErr != nil {
			t.Fatal(issueErr)
		}
		mutate()
		if _, authErr := delegated.AuthenticateBearer(t.Context(), created.Plaintext); authErr == nil {
			t.Fatalf("delegated token remained valid after live parent %s change", name)
		}
	}
	assertDelegatedLiveParent("scope", func() {
		if _, err := pool.Exec(t.Context(), `DELETE FROM pat_scopes WHERE personal_access_token_id = $1 AND scope = 'issues:write'`, patPrincipal.CredentialID); err != nil {
			t.Fatal(err)
		}
	})
	if _, err := pool.Exec(t.Context(), `INSERT INTO pat_scopes (id, personal_access_token_id, scope) VALUES ($1, $2, 'issues:write')`, uuid.New(), patPrincipal.CredentialID); err != nil {
		t.Fatal(err)
	}
	otherRepoID := uuid.New()
	if _, err := pool.Exec(t.Context(), `INSERT INTO repos (id, organization_id, name, display_name) VALUES ($1, $2, 'other-repo', 'other-repo')`, otherRepoID, orgID); err != nil {
		t.Fatal(err)
	}
	assertDelegatedLiveParent("cap", func() {
		if _, err := pool.Exec(t.Context(), `UPDATE pat_repositories SET repository_id = $1 WHERE personal_access_token_id = $2`, otherRepoID, patPrincipal.CredentialID); err != nil {
			t.Fatal(err)
		}
	})
	if _, err := pool.Exec(t.Context(), `UPDATE pat_repositories SET repository_id = $1 WHERE personal_access_token_id = $2`, repoID, patPrincipal.CredentialID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO pat_repositories (personal_access_token_id, organization_id, repository_id) VALUES ($1, $2, $3)`, patPrincipal.CredentialID, orgID, otherRepoID); err != nil {
		t.Fatal(err)
	}
	if _, err := delegated.Issue(t.Context(), delegation.IssueInput{Issuer: patPrincipal,
		Repo: models.RepoScope{OrgID: orgID, RepoID: repoID}, JobID: "job-live-multiple-cap", Purpose: "issue-api",
		Audience: "issue-spec-server", Subject: "runner-child", Scopes: []string{"issues:write"}, TTL: 5 * time.Minute}); err != nil {
		t.Fatalf("multiple live repository caps issue error = %v", err)
	}
	if _, err := pool.Exec(t.Context(), `DELETE FROM pat_repositories WHERE personal_access_token_id = $1`, patPrincipal.CredentialID); err != nil {
		t.Fatal(err)
	}
	if _, err := delegated.Issue(t.Context(), delegation.IssueInput{Issuer: patPrincipal,
		Repo: models.RepoScope{OrgID: orgID, RepoID: repoID}, JobID: "job-live-unrestricted-cap", Purpose: "issue-api",
		Audience: "issue-spec-server", Subject: "runner-child", Scopes: []string{"issues:write"}, TTL: 5 * time.Minute}); err != nil {
		t.Fatalf("unrestricted live repository caps issue error = %v", err)
	}
	assertDelegatedLiveParent("permission", func() {
		if _, err := pool.Exec(t.Context(), `DELETE FROM repo_collaborators WHERE organization_id = $1 AND repository_id = $2 AND user_id = $3`, orgID, repoID, userA.ID); err != nil {
			t.Fatal(err)
		}
	})
	if _, err := pool.Exec(t.Context(), `INSERT INTO repo_collaborators
		(id, organization_id, repository_id, user_id, role) VALUES ($1, $2, $3, $4, 'write')`,
		uuid.New(), orgID, repoID, userA.ID); err != nil {
		t.Fatal(err)
	}
	revocablePAT, err := pats.Create(t.Context(), userA.ID, pat.CreateInput{Name: "revocable-parent",
		Scopes: []string{"runner:delegate", "issues:write"}, Repositories: []models.RepoScope{{OrgID: orgID, RepoID: repoID}}})
	if err != nil {
		t.Fatal(err)
	}
	revocablePrincipal, err := pats.AuthenticateBearer(t.Context(), revocablePAT.Plaintext)
	if err != nil {
		t.Fatal(err)
	}
	revocableChild, err := delegated.Issue(t.Context(), delegation.IssueInput{Issuer: revocablePrincipal,
		Repo: models.RepoScope{OrgID: orgID, RepoID: repoID}, JobID: "job-live-revoke", Purpose: "comment-writeback",
		Audience: "issue-spec-server", Subject: "runner-child", Scopes: []string{"issues:write"}, TTL: 5 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if err := pats.Revoke(t.Context(), userA.ID, revocablePAT.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := delegated.AuthenticateBearer(t.Context(), revocableChild.Plaintext); !errors.Is(err, serverauth.ErrRevokedCredential) {
		t.Fatalf("delegated token after parent revoke error = %v", err)
	}

	if err := identities.DisableUser(t.Context(), userB.ID, userA.ID, "req-disable"); err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.Authenticate(t.Context(), rotatedSession.Token); !errors.Is(err, serverauth.ErrInvalidCredential) && !errors.Is(err, serverauth.ErrDisabledAccount) {
		t.Fatalf("disabled user session error = %v", err)
	}
	if _, err := pats.AuthenticateBearer(t.Context(), rotatedPAT.Plaintext); !errors.Is(err, serverauth.ErrDisabledAccount) && !errors.Is(err, serverauth.ErrRevokedCredential) {
		t.Fatalf("disabled user PAT error = %v", err)
	}
	if _, err := delegated.AuthenticateBearer(t.Context(), delegatedCreated.Plaintext); !errors.Is(err, serverauth.ErrDisabledAccount) && !errors.Is(err, serverauth.ErrRevokedCredential) {
		t.Fatalf("disabled user delegated token error = %v", err)
	}
}

func TestPATRotateRollsBackNewTokenWhenOldRevocationFails(t *testing.T) {
	pool := migratedPool(t)
	userID := insertUser(t, pool, "rotation-rollback")
	service := pat.New(pool, testSecrets(t))
	original, err := service.Create(t.Context(), userID, pat.CreateInput{Name: "original", Scopes: []string{"read:user"}})
	if err != nil {
		t.Fatal(err)
	}
	// Function bodies cannot bind values, so safely interpolate the UUID's
	// canonical representation generated by google/uuid.
	functionSQL := strings.ReplaceAll(`CREATE FUNCTION reject_selected_pat_revoke() RETURNS trigger LANGUAGE plpgsql AS $body$
		BEGIN
			IF OLD.id = 'TOKEN_ID'::uuid AND NEW.revoked_at IS NOT NULL THEN
				RAISE EXCEPTION 'injected revoke failure';
			END IF;
			RETURN NEW;
		END;
		$body$`, "TOKEN_ID", original.ID.String())
	if _, err := pool.Exec(t.Context(), functionSQL); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `CREATE TRIGGER reject_selected_pat_revoke
		BEFORE UPDATE ON personal_access_tokens FOR EACH ROW EXECUTE FUNCTION reject_selected_pat_revoke()`); err != nil {
		t.Fatal(err)
	}
	var before int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM personal_access_tokens WHERE user_id = $1`, userID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Rotate(t.Context(), userID, original.ID); err == nil {
		t.Fatal("Rotate unexpectedly succeeded through injected old-token revoke failure")
	}
	var after int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM personal_access_tokens WHERE user_id = $1`, userID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("failed rotation left a new token: before=%d after=%d", before, after)
	}
	if _, err := service.AuthenticateBearer(t.Context(), original.Plaintext); err != nil {
		t.Fatalf("failed rotation revoked original token: %v", err)
	}
}

func TestRecoveryIsHashedOneTimeAndWrongSecretDoesNotConsume(t *testing.T) {
	pool := migratedPool(t)
	secrets := testSecrets(t)
	userID := insertUser(t, pool, "recovery-admin")
	if _, err := pool.Exec(t.Context(), `INSERT INTO site_role_assignments (id, user_id, role)
		VALUES ($1, $2, 'site_admin')`, uuid.New(), userID); err != nil {
		t.Fatal(err)
	}
	service := recovery.New(pool, secrets)
	nonAdminID := insertUser(t, pool, "recovery-non-admin")
	if _, err := service.Mint(t.Context(), nonAdminID, "root@console", "must fail", "req-non-admin", 5*time.Minute); !errors.Is(err, serverauth.ErrInvalidCredential) {
		t.Fatalf("non-admin recovery mint error = %v", err)
	}
	created, err := service.Mint(t.Context(), userID, "root@console", "identity provider outage", "req-recovery", 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	var storedHash []byte
	if err := pool.QueryRow(t.Context(), `SELECT token_hash FROM recovery_credentials WHERE id = $1`, created.ID).Scan(&storedHash); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(storedHash), created.Plaintext) {
		t.Fatal("recovery plaintext was persisted")
	}
	replacement := "x"
	if strings.HasSuffix(created.Plaintext, replacement) {
		replacement = "y"
	}
	wrong := created.Plaintext[:len(created.Plaintext)-1] + replacement
	if _, err := service.Consume(t.Context(), wrong, "req-wrong"); !errors.Is(err, serverauth.ErrInvalidCredential) {
		t.Fatalf("wrong secret error = %v", err)
	}
	principal, err := service.Consume(t.Context(), created.Plaintext, "req-consume")
	if err != nil || !principal.HasScope("site:admin") {
		t.Fatalf("Consume(recovery) = %+v, %v", principal, err)
	}
	if _, err := service.Consume(t.Context(), created.Plaintext, "req-replay"); !errors.Is(err, serverauth.ErrInvalidCredential) {
		t.Fatalf("recovery replay error = %v", err)
	}
	revoked, err := service.Mint(t.Context(), userID, "root@console", "rotate recovery", "req-recovery-2", 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Revoke(t.Context(), revoked.ID, "root@console", "req-revoke"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Consume(t.Context(), revoked.Plaintext, "req-revoked-consume"); !errors.Is(err, serverauth.ErrInvalidCredential) {
		t.Fatalf("revoked recovery error = %v", err)
	}
	expired, err := service.Mint(t.Context(), userID, "root@console", "expiry test", "req-recovery-3", 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE recovery_credentials SET
		created_at = clock_timestamp() - interval '2 hours', expires_at = clock_timestamp() - interval '1 hour'
		WHERE id = $1`, expired.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Consume(t.Context(), expired.Plaintext, "req-expired-consume"); !errors.Is(err, serverauth.ErrInvalidCredential) {
		t.Fatalf("expired recovery error = %v", err)
	}
	var auditCount int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_events
		WHERE action IN ('recovery.mint', 'recovery.consume', 'recovery.revoke')`).Scan(&auditCount); err != nil || auditCount != 5 {
		t.Fatalf("recovery audit count = %d, %v", auditCount, err)
	}
}

func TestServiceAccountAndAuthMigrationTenantConstraints(t *testing.T) {
	pool := migratedPool(t)
	actorID := insertUser(t, pool, "operator")
	orgA, repoA := insertOrgRepo(t, pool, "service-a")
	orgB, repoB := insertOrgRepo(t, pool, "service-b")
	service := serviceaccount.New(pool)
	account, err := service.Create(t.Context(), actorID, orgA, "automation", "req-service-create")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(account.Login, "svc-") {
		t.Fatalf("service account login = %q", account.Login)
	}
	patID := uuid.New()
	if _, err := pool.Exec(t.Context(), `INSERT INTO personal_access_tokens
		(id, user_id, name, token_prefix, token_hash) VALUES ($1, $2, 'cap', $3, $4)`,
		patID, actorID, "test-prefix-"+uuid.NewString(), []byte("hash-"+uuid.NewString())); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO pat_repositories
		(personal_access_token_id, organization_id, repository_id) VALUES ($1, $2, $3)`, patID, orgB, repoA); err == nil {
		t.Fatal("cross-tenant PAT repository cap was accepted")
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO pat_repositories
		(personal_access_token_id, organization_id, repository_id) VALUES ($1, $2, $3)`, patID, orgB, repoB); err != nil {
		t.Fatalf("valid PAT repository cap rejected: %v", err)
	}
	if err := service.Disable(t.Context(), actorID, account.ID, "req-service-disable"); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := pool.QueryRow(t.Context(), `SELECT status FROM users WHERE id = $1`, account.UserID).Scan(&status); err != nil || status != "disabled" {
		t.Fatalf("service account user status = %q, %v", status, err)
	}
}

func TestNativeUserAndPATRoutesEnforceCookieCSRFAndExposeScopes(t *testing.T) {
	pool := migratedPool(t)
	secrets := testSecrets(t)
	userID := insertUser(t, pool, "route-user")
	sessions, _ := session.New(pool, secrets, session.Config{Secure: true, IdleTTL: time.Hour, AbsoluteTTL: 24 * time.Hour})
	pats := pat.New(pool, secrets)
	createdSession, err := sessions.Create(t.Context(), userID, "route-test", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.Authenticate(t.Context(), createdSession.Token); err != nil {
		t.Fatalf("route session preflight: %v", err)
	}
	bearer, err := pats.Create(t.Context(), userID, pat.CreateInput{Name: "route", Scopes: []string{"read:user"}})
	if err != nil {
		t.Fatal(err)
	}
	middleware := serverauth.Middleware{SessionCookieName: sessions.CookieName(), Sessions: sessions,
		Bearer: serverauth.BearerChain{pats}, AllowedOrigins: map[string]struct{}{"https://issues.example.test": {}}}
	authority, err := authz.New(pool)
	if err != nil {
		t.Fatal(err)
	}
	routes, err := nativeauth.NewRouteSet(nativeauth.Dependencies{Identity: serverauth.NewIdentityService(pool), Sessions: sessions,
		PATs: pats, Authority: authority, Middleware: middleware, WebOrigin: "https://issues.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	mux, err := routeset.NewMux(routeset.Policy{}, routes)
	if err != nil {
		t.Fatal(err)
	}
	publicProfileRequest := httptest.NewRequest(http.MethodGet, "/api/v1/users/route-user", nil)
	publicProfileResponse := httptest.NewRecorder()
	mux.ServeHTTP(publicProfileResponse, publicProfileRequest)
	if publicProfileResponse.Code != http.StatusOK || !strings.Contains(publicProfileResponse.Body.String(), `"html_url":"https://issues.example.test/users/route-user"`) ||
		strings.Contains(publicProfileResponse.Body.String(), `"email"`) || strings.Contains(publicProfileResponse.Body.String(), `"identity_display_name"`) {
		t.Fatalf("public profile = %d body=%s", publicProfileResponse.Code, publicProfileResponse.Body.String())
	}
	profileRequest := httptest.NewRequest(http.MethodGet, "/api/v1/profile", nil)
	profileRequest.AddCookie(sessions.Cookie(createdSession.Token))
	profileResponse := httptest.NewRecorder()
	mux.ServeHTTP(profileResponse, profileRequest)
	var editableProfile struct {
		RepresentationVersion int64 `json:"representation_version"`
	}
	if profileResponse.Code != http.StatusOK || json.Unmarshal(profileResponse.Body.Bytes(), &editableProfile) != nil || editableProfile.RepresentationVersion < 1 {
		t.Fatalf("private profile = %d body=%s", profileResponse.Code, profileResponse.Body.String())
	}
	updateProfileRequest := httptest.NewRequest(http.MethodPatch, "/api/v1/profile", strings.NewReader(fmt.Sprintf(
		`{"nickname":"澄潭","expected_version":%d}`, editableProfile.RepresentationVersion)))
	updateProfileRequest.AddCookie(sessions.Cookie(createdSession.Token))
	updateProfileRequest.Header.Set("Origin", "https://issues.example.test")
	updateProfileRequest.Header.Set("X-CSRF-Token", createdSession.CSRFToken)
	updateProfileResponse := httptest.NewRecorder()
	mux.ServeHTTP(updateProfileResponse, updateProfileRequest)
	if updateProfileResponse.Code != http.StatusOK || !strings.Contains(updateProfileResponse.Body.String(), `"display_name":"澄潭"`) {
		t.Fatalf("PATCH profile = %d body=%s", updateProfileResponse.Code, updateProfileResponse.Body.String())
	}
	unauthenticated := httptest.NewRequest(http.MethodGet, "/api/v1/pats", nil)
	unauthenticatedResponse := httptest.NewRecorder()
	mux.ServeHTTP(unauthenticatedResponse, unauthenticated)
	if unauthenticatedResponse.Code != http.StatusUnauthorized ||
		!strings.HasPrefix(unauthenticatedResponse.Header().Get("Content-Type"), "application/problem+json") ||
		!strings.Contains(unauthenticatedResponse.Body.String(), `"code":"authentication_required"`) ||
		unauthenticatedResponse.Header().Get("X-Request-ID") == "" {
		t.Fatalf("native unauthenticated response = %d headers=%v body=%s", unauthenticatedResponse.Code,
			unauthenticatedResponse.Header(), unauthenticatedResponse.Body.String())
	}

	userRequest := httptest.NewRequest(http.MethodGet, "/user", nil)
	userRequest.Header.Set("Authorization", "Bearer "+bearer.Plaintext)
	userResponse := httptest.NewRecorder()
	mux.ServeHTTP(userResponse, userRequest)
	if userResponse.Code != http.StatusOK || userResponse.Header().Get("X-OAuth-Scopes") != "read:user" ||
		!strings.Contains(userResponse.Body.String(), `"login":"route-user"`) {
		t.Fatalf("GET /user = %d headers=%v body=%s", userResponse.Code, userResponse.Header(), userResponse.Body.String())
	}
	var compatibleUser struct {
		ID     int64  `json:"id"`
		NodeID string `json:"node_id"`
	}
	if err := json.Unmarshal(userResponse.Body.Bytes(), &compatibleUser); err != nil ||
		compatibleUser.ID != codec.StableNumericID(userID.String()) ||
		compatibleUser.NodeID != codec.NodeID("User", userID.String()) {
		t.Fatalf("GET /user compatibility identity = %+v, err=%v", compatibleUser, err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO site_role_assignments (id, user_id, role)
		VALUES ($1, $2, 'site_admin')`, uuid.New(), userID); err != nil {
		t.Fatal(err)
	}
	sessionUserRequest := httptest.NewRequest(http.MethodGet, "/user", nil)
	sessionUserRequest.AddCookie(sessions.Cookie(createdSession.Token))
	sessionUserResponse := httptest.NewRecorder()
	mux.ServeHTTP(sessionUserResponse, sessionUserRequest)
	if sessionUserResponse.Code != http.StatusOK || !strings.Contains(sessionUserResponse.Body.String(), `"site_admin":true`) {
		t.Fatalf("session GET /user did not use identity authority: %d %s", sessionUserResponse.Code, sessionUserResponse.Body.String())
	}

	body := `{"name":"created-from-route","scopes":["read:user"]}`
	withoutOrigin := httptest.NewRequest(http.MethodPost, "/api/v1/pats", strings.NewReader(body))
	withoutOrigin.AddCookie(sessions.Cookie(createdSession.Token))
	withoutOrigin.Header.Set("X-CSRF-Token", createdSession.CSRFToken)
	withoutOriginResponse := httptest.NewRecorder()
	mux.ServeHTTP(withoutOriginResponse, withoutOrigin)
	if withoutOriginResponse.Code != http.StatusForbidden {
		t.Fatalf("cookie mutation without Origin = %d", withoutOriginResponse.Code)
	}
	bearerCreate := httptest.NewRequest(http.MethodPost, "/api/v1/pats", strings.NewReader(body))
	bearerCreate.Header.Set("Authorization", "Bearer "+bearer.Plaintext)
	bearerCreateResponse := httptest.NewRecorder()
	mux.ServeHTTP(bearerCreateResponse, bearerCreate)
	if bearerCreateResponse.Code != http.StatusForbidden {
		t.Fatalf("bearer was allowed to mint a PAT: %d", bearerCreateResponse.Code)
	}

	createRequest := httptest.NewRequest(http.MethodPost, "/api/v1/pats", strings.NewReader(body))
	createRequest.AddCookie(sessions.Cookie(createdSession.Token))
	createRequest.Header.Set("Origin", "https://issues.example.test")
	createRequest.Header.Set("X-CSRF-Token", createdSession.CSRFToken)
	createResponse := httptest.NewRecorder()
	mux.ServeHTTP(createResponse, createRequest)
	if createResponse.Code != http.StatusCreated || !strings.Contains(createResponse.Body.String(), `"token":"iss_pat_`) {
		t.Fatalf("POST /api/v1/pats = %d body=%s", createResponse.Code, createResponse.Body.String())
	}

	listRequest := httptest.NewRequest(http.MethodGet, "/api/v1/pats", nil)
	listRequest.AddCookie(sessions.Cookie(createdSession.Token))
	listResponse := httptest.NewRecorder()
	mux.ServeHTTP(listResponse, listRequest)
	if listResponse.Code != http.StatusOK || strings.Contains(listResponse.Body.String(), `"token":"iss_pat_`) {
		t.Fatalf("GET /api/v1/pats leaked plaintext: %d body=%s", listResponse.Code, listResponse.Body.String())
	}
}

func TestSessionReplaceRollsBackPriorRevocationWhenCreationFails(t *testing.T) {
	pool := migratedPool(t)
	secrets := testSecrets(t)
	sessions, err := session.New(pool, secrets, session.Config{Secure: true, IdleTTL: time.Hour, AbsoluteTTL: 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	priorUserID := insertUser(t, pool, "prior-session")
	targetUserID := insertUser(t, pool, "replacement-session")
	prior, err := sessions.Create(t.Context(), priorUserID, "replace-test", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE users SET status = 'disabled' WHERE id = $1`, targetUserID); err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.Replace(t.Context(), prior.Token, targetUserID, "replace-test", "127.0.0.1"); !errors.Is(err, serverauth.ErrDisabledAccount) {
		t.Fatalf("replace failure = %v", err)
	}
	if principal, err := sessions.Authenticate(t.Context(), prior.Token); err != nil || principal.User.ID != priorUserID {
		t.Fatalf("prior session was revoked after replacement rollback: %+v, %v", principal, err)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE users SET status = 'active' WHERE id = $1`, targetUserID); err != nil {
		t.Fatal(err)
	}
	replacement, err := sessions.Replace(t.Context(), prior.Token, targetUserID, "replace-test", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.Authenticate(t.Context(), prior.Token); !errors.Is(err, serverauth.ErrInvalidCredential) && !errors.Is(err, serverauth.ErrRevokedCredential) {
		t.Fatalf("prior session remained valid after replacement: %v", err)
	}
	if principal, err := sessions.Authenticate(t.Context(), replacement.Token); err != nil || principal.User.ID != targetUserID {
		t.Fatalf("replacement session = %+v, %v", principal, err)
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
	schema := "auth_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
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

func testSecrets(t *testing.T) *serverauth.Secrets {
	t.Helper()
	secrets, err := serverauth.NewSecrets([]byte(strings.Repeat("p", 32)), []byte(strings.Repeat("e", 32)))
	if err != nil {
		t.Fatal(err)
	}
	return secrets
}

func insertProvider(t *testing.T, pool *pgxpool.Pool, name, kind, issuer string) serverauth.Provider {
	t.Helper()
	provider := serverauth.Provider{ID: uuid.New(), Name: name, Kind: kind, Issuer: issuer, Enabled: true, Config: json.RawMessage(`{}`)}
	if _, err := pool.Exec(t.Context(), `INSERT INTO auth_providers (id, name, kind, issuer) VALUES ($1, $2, $3, $4)`,
		provider.ID, provider.Name, provider.Kind, provider.Issuer); err != nil {
		t.Fatal(err)
	}
	return provider
}

func insertUser(t *testing.T, pool *pgxpool.Pool, login string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := pool.Exec(t.Context(), `INSERT INTO users (id, login, display_name) VALUES ($1, $2, $2)`, id, login); err != nil {
		t.Fatal(err)
	}
	return id
}

func insertOrgRepo(t *testing.T, pool *pgxpool.Pool, name string) (uuid.UUID, uuid.UUID) {
	t.Helper()
	orgID, repoID := uuid.New(), uuid.New()
	if _, err := pool.Exec(t.Context(), `INSERT INTO orgs (id, name, display_name) VALUES ($1, $2, $2)`, orgID, name); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO repos (id, organization_id, name, display_name) VALUES ($1, $2, 'repo', 'repo')`, repoID, orgID); err != nil {
		t.Fatal(err)
	}
	return orgID, repoID
}
