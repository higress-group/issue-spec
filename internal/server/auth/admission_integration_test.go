package auth_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	nativeauth "github.com/higress-group/issue-spec/internal/server/api/native/auth"
	"github.com/higress-group/issue-spec/internal/server/api/routeset"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
	"github.com/higress-group/issue-spec/internal/server/auth/githuboauth"
	"github.com/higress-group/issue-spec/internal/server/auth/pat"
	"github.com/higress-group/issue-spec/internal/server/auth/session"
	"github.com/higress-group/issue-spec/internal/server/authz"
	"golang.org/x/oauth2"
)

func TestGitHubOrganizationAdmissionPersistsStableBindingAndRedactedAudit(t *testing.T) {
	pool := migratedPool(t)
	secrets := testSecrets(t)
	provider := insertProvider(t, pool, "github-admission", "github-oauth", "https://github.com")
	const tokenSentinel = "github-token-that-must-never-be-persisted"
	responseID := int64(42)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+tokenSentinel {
			t.Errorf("membership authorization = %q", r.Header.Get("Authorization"))
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		login := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"state": "active",
			"organization": map[string]any{"id": responseID, "login": login}, "user": map[string]any{"id": 99}})
	}))
	defer server.Close()

	newGate := func(t *testing.T, organization githuboauth.ApprovedOrganization) githuboauth.AdmissionGate {
		t.Helper()
		policy, err := githuboauth.NormalizeAdmission(&githuboauth.AdmissionConfig{
			Mode: githuboauth.AdmissionOrganizationRestricted, Organizations: []githuboauth.ApprovedOrganization{organization},
			MembershipURL: server.URL + "/memberships",
		}, false, server.URL, server.URL+"/user")
		if err != nil {
			t.Fatal(err)
		}
		gate, err := githuboauth.NewOrganizationAdmissionGate(githuboauth.OrganizationAdmissionGateConfig{
			ProviderID: provider.ID, Policy: policy, UserURL: server.URL + "/user", Pool: pool, Secrets: secrets,
		})
		if err != nil {
			t.Fatal(err)
		}
		return gate
	}
	client := oauth2.NewClient(t.Context(), oauth2.StaticTokenSource(&oauth2.Token{AccessToken: tokenSentinel, TokenType: "Bearer"}))
	identity := serverauth.ExternalIdentity{Issuer: provider.Issuer, Subject: "99", Login: "octocat"}
	gate := newGate(t, githuboauth.ApprovedOrganization{Login: "acme"})
	evidence, err := gate.Evaluate(t.Context(), githuboauth.AdmissionRequest{Client: client, Identity: identity, RequestID: "admission-1"})
	if err != nil {
		t.Fatal(err)
	}
	if !evidence.Audited || evidence.Decision != "allowed" || evidence.Subject == identity.Subject || evidence.RequestID != "admission-1" {
		t.Fatalf("admission evidence = %+v", evidence)
	}
	var bindingID uuid.UUID
	var externalID int64
	var configuredLogin string
	if err := pool.QueryRow(t.Context(), `SELECT id, external_org_id, configured_login
		FROM github_admission_organizations WHERE provider_id=$1`, provider.ID).
		Scan(&bindingID, &externalID, &configuredLogin); err != nil || externalID != 42 || configuredLogin != "acme" {
		t.Fatalf("organization binding id=%s external=%d login=%q err=%v", bindingID, externalID, configuredLogin, err)
	}

	// An explicit stable ID lets the operator rename the configured login while
	// preserving the organization realm.
	renameGate := newGate(t, githuboauth.ApprovedOrganization{Login: "renamed-acme", ID: "42"})
	if _, err := renameGate.Evaluate(t.Context(), githuboauth.AdmissionRequest{Client: client, Identity: identity, RequestID: "admission-rename"}); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), `SELECT configured_login FROM github_admission_organizations WHERE id=$1`, bindingID).Scan(&configuredLogin); err != nil || configuredLogin != "renamed-acme" {
		t.Fatalf("renamed binding login=%q err=%v", configuredLogin, err)
	}

	responseID = 43
	_, err = renameGate.Evaluate(t.Context(), githuboauth.AdmissionRequest{Client: client, Identity: identity, RequestID: "admission-mismatch"})
	if class, ok := githuboauth.AdmissionFailure(err); !ok || class != githuboauth.AdmissionOrganizationIdentityMismatch {
		t.Fatalf("identity mismatch error = %v class=%q", err, class)
	}
	var auditText string
	if err := pool.QueryRow(t.Context(), `SELECT string_agg(actor_identity_key || metadata::text, ' ' ORDER BY created_at)
		FROM audit_events WHERE resource_type='auth_provider' AND resource_id=$1`, provider.ID).Scan(&auditText); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(auditText, tokenSentinel) || strings.Contains(auditText, "octocat") || strings.Contains(auditText, "renamed-acme") ||
		!strings.Contains(auditText, "github_admission_organization_identity_mismatch") {
		t.Fatalf("unsafe or incomplete admission audit = %q", auditText)
	}
	var persistedToken bool
	if err := pool.QueryRow(t.Context(), `SELECT EXISTS (
		SELECT 1 FROM auth_providers WHERE config::text LIKE $1
		UNION ALL SELECT 1 FROM identities WHERE claims::text LIKE $1
		UNION ALL SELECT 1 FROM audit_events WHERE metadata::text LIKE $1 OR actor_identity_key LIKE $1
	)`, "%"+tokenSentinel+"%").Scan(&persistedToken); err != nil || persistedToken {
		t.Fatalf("transient OAuth token persisted=%v err=%v", persistedToken, err)
	}
}

func TestDeniedAdmissionStopsBeforeIdentityProvisionAndSessionReplacement(t *testing.T) {
	pool := migratedPool(t)
	secrets := testSecrets(t)
	existingUserID := insertUser(t, pool, "existing-admission-user")
	sessions, err := session.New(pool, secrets, session.Config{Secure: true, IdleTTL: time.Hour, AbsoluteTTL: 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	existing, err := sessions.Create(t.Context(), existingUserID, "admission-test", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	pats := pat.New(pool, secrets)
	authority, err := authz.New(pool)
	if err != nil {
		t.Fatal(err)
	}
	providerID := uuid.New()
	adapter := &deniedAdmissionAdapter{providerID: providerID}
	middleware := serverauth.Middleware{SessionCookieName: sessions.CookieName(), Sessions: sessions,
		Bearer: serverauth.BearerChain{pats}, AllowedOrigins: map[string]struct{}{"https://issues.example.test": {}}}
	set, err := nativeauth.NewRouteSet(nativeauth.Dependencies{Identity: serverauth.NewIdentityService(pool), Sessions: sessions,
		PATs: pats, Authority: authority, Middleware: middleware, Adapters: map[string]nativeauth.LoginAdapter{"github": adapter},
		WebOrigin: "https://issues.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	mux, err := routeset.NewMux(routeset.Policy{}, set)
	if err != nil {
		t.Fatal(err)
	}
	var usersBefore, identitiesBefore, sessionsBefore int
	if err := pool.QueryRow(t.Context(), `SELECT
		(SELECT count(*) FROM users), (SELECT count(*) FROM identities), (SELECT count(*) FROM sessions)`).
		Scan(&usersBefore, &identitiesBefore, &sessionsBefore); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/auth/github/callback?state=state&code=code", nil)
	request.Header.Set("X-Request-ID", "admission-denied-request")
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || strings.Contains(response.Body.String(), string(githuboauth.AdmissionPending)) ||
		!strings.Contains(response.Body.String(), "Authentication failed") {
		t.Fatalf("denied callback = %d %s", response.Code, response.Body.String())
	}
	if adapter.requestID != "admission-denied-request" {
		t.Fatalf("admission request id = %q", adapter.requestID)
	}
	var usersAfter, identitiesAfter, sessionsAfter int
	if err := pool.QueryRow(t.Context(), `SELECT
		(SELECT count(*) FROM users), (SELECT count(*) FROM identities), (SELECT count(*) FROM sessions)`).
		Scan(&usersAfter, &identitiesAfter, &sessionsAfter); err != nil {
		t.Fatal(err)
	}
	if usersAfter != usersBefore || identitiesAfter != identitiesBefore || sessionsAfter != sessionsBefore {
		t.Fatalf("denied callback mutated state users=%d/%d identities=%d/%d sessions=%d/%d",
			usersBefore, usersAfter, identitiesBefore, identitiesAfter, sessionsBefore, sessionsAfter)
	}
	if _, err := sessions.Authenticate(t.Context(), existing.Token); err != nil {
		t.Fatalf("denied callback invalidated existing session: %v", err)
	}
}

type deniedAdmissionAdapter struct {
	providerID uuid.UUID
	requestID  string
}

func (a *deniedAdmissionAdapter) ProviderID() uuid.UUID { return a.providerID }
func (a *deniedAdmissionAdapter) Kind() string          { return "github-oauth" }
func (a *deniedAdmissionAdapter) Begin(context.Context, string) (string, error) {
	return "", errors.New("not used")
}
func (a *deniedAdmissionAdapter) Complete(context.Context, string, string) (serverauth.ExternalIdentity, string, error) {
	return serverauth.ExternalIdentity{}, "", errors.New("legacy completion must not be used")
}
func (a *deniedAdmissionAdapter) CompleteLogin(ctx context.Context, _, _ string) (serverauth.LoginCompletion, error) {
	a.requestID = serverauth.AdmissionRequestID(ctx)
	return serverauth.LoginCompletion{}, &githuboauth.AdmissionError{Class: githuboauth.AdmissionPending}
}
