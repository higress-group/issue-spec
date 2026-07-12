package webhooks_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/server/api/native/webhooks"
	"github.com/higress-group/issue-spec/internal/server/api/routeset"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
	"github.com/higress-group/issue-spec/internal/server/authz"
	"github.com/higress-group/issue-spec/internal/server/events/subscriptions"
	"github.com/higress-group/issue-spec/internal/server/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestNativeWebhookLifecycleRejectsURLSecretsAndExposesTerminalRevocation(t *testing.T) {
	pool := apiMigratedPool(t)
	orgID, repoID, userID, sessionID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO users (id, login, display_name) VALUES ($1, 'api-owner', 'API Owner')`, []any{userID}},
		{`INSERT INTO orgs (id, name, display_name, base_permission) VALUES ($1, 'api-org', 'API Org', 'read')`, []any{orgID}},
		{`INSERT INTO repos (id, organization_id, name, display_name, visibility, contribution_policy)
			VALUES ($1, $2, 'api-repo', 'API Repo', 'private', 'members')`, []any{repoID, orgID}},
		{`INSERT INTO org_memberships (organization_id, user_id, role, state, activated_at)
			VALUES ($1, $2, 'owner', 'active', clock_timestamp())`, []any{orgID, userID}},
		{`INSERT INTO sessions (id, user_id, token_prefix, token_hash, csrf_hash, idle_expires_at, absolute_expires_at)
			VALUES ($1, $2, $3, $4, $5, clock_timestamp() + interval '1 hour', clock_timestamp() + interval '2 hours')`,
			[]any{sessionID, userID, "session-" + sessionID.String(), []byte(sessionID.String()), []byte("csrf")}},
	}
	for _, statement := range statements {
		if _, err := pool.Exec(t.Context(), statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	principal := serverauth.Principal{User: serverauth.User{ID: userID, Login: "api-owner", Status: "active"},
		Kind: serverauth.CredentialSession, CredentialID: sessionID}
	authorizer, err := authz.New(pool)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := subscriptions.NewKeyring("primary", map[string][]byte{"primary": []byte(strings.Repeat("k", 32))})
	if err != nil {
		t.Fatal(err)
	}
	service, err := subscriptions.New(store.New(pool), authorizer, keys, subscriptions.Config{Production: true})
	if err != nil {
		t.Fatal(err)
	}
	authenticate := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(serverauth.WithPrincipal(r.Context(), principal)))
		})
	}
	set, err := webhooks.NewRouteSet(webhooks.Dependencies{Service: service, Authenticate: authenticate})
	if err != nil {
		t.Fatal(err)
	}
	mux, err := routeset.NewMux(routeset.Policy{}, set)
	if err != nil {
		t.Fatal(err)
	}
	collection := "/api/v1/orgs/" + orgID.String() + "/webhooks"

	unsafe := serveWebhook(t, mux, http.MethodPost, collection, map[string]any{
		"repository_id": repoID, "url": "https://runner.example.test/hook?access_token=must-not-reflect",
		"event_types": []string{"issue_comment.created"},
	})
	if unsafe.Code != http.StatusUnprocessableEntity || strings.Contains(unsafe.Body.String(), "must-not-reflect") {
		t.Fatalf("unsafe create status=%d body=%s", unsafe.Code, unsafe.Body.String())
	}
	githubCreated := serveWebhook(t, mux, http.MethodPost, collection, map[string]any{
		"repository_id": repoID, "url": "https://robot.example.test/hook?access_token=must-not-reflect",
		"delivery_format": "github.v3", "signing_mode": "hmac-sha256",
		"content_policy": map[string]any{"issue_actions": []string{"opened"},
			"comment_actions": []string{"created"}, "issue_kinds": []string{"proposal"},
			"comment_classes": []string{"human-untyped"}, "actor_classes": []string{"human"}},
	})
	if githubCreated.Code != http.StatusCreated || strings.Contains(githubCreated.Body.String(), "must-not-reflect") ||
		strings.Contains(githubCreated.Body.String(), "access_token") ||
		!strings.Contains(githubCreated.Body.String(), `"url":"https://robot.example.test/hook"`) ||
		!strings.Contains(githubCreated.Body.String(), `"has_destination_query":true`) {
		t.Fatalf("github create status=%d body=%s", githubCreated.Code, githubCreated.Body.String())
	}

	created := serveWebhook(t, mux, http.MethodPost, collection, map[string]any{
		"repository_id": repoID, "url": "https://runner.example.test/hook",
		"event_types": []string{"issue_comment.created"},
	})
	if created.Code != http.StatusCreated || created.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("create status=%d cache=%q body=%s", created.Code, created.Header().Get("Cache-Control"), created.Body.String())
	}
	var createdBody struct {
		ID                    uuid.UUID `json:"id"`
		Secret                string    `json:"secret"`
		RepresentationVersion int64     `json:"representation_version"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &createdBody); err != nil || createdBody.ID == uuid.Nil || createdBody.Secret == "" {
		t.Fatalf("created body=%s error=%v", created.Body.String(), err)
	}
	itemPath := collection + "/" + createdBody.ID.String()
	for index := range 2 {
		response := serveWebhook(t, mux, http.MethodDelete, itemPath, nil)
		if response.Code != http.StatusNoContent {
			t.Fatalf("revoke %d status=%d body=%s", index+1, response.Code, response.Body.String())
		}
	}
	read := serveWebhook(t, mux, http.MethodGet, itemPath, nil)
	if read.Code != http.StatusOK || !strings.Contains(read.Body.String(), `"revoked_at":"`) ||
		strings.Contains(read.Body.String(), createdBody.Secret) {
		t.Fatalf("revoked read status=%d body=%s", read.Code, read.Body.String())
	}
	resume := serveWebhook(t, mux, http.MethodPatch, itemPath, map[string]any{
		"expected_version": createdBody.RepresentationVersion + 1, "url": "https://runner.example.test/hook",
		"active": true, "event_types": []string{"issue_comment.created"},
	})
	if resume.Code != http.StatusConflict || !strings.Contains(resume.Body.String(), `"code":"webhook_revoked"`) {
		t.Fatalf("resume status=%d body=%s", resume.Code, resume.Body.String())
	}
	rotate := serveWebhook(t, mux, http.MethodPost, itemPath+"/rotate-secret", nil)
	if rotate.Code != http.StatusConflict || !strings.Contains(rotate.Body.String(), `"code":"webhook_revoked"`) {
		t.Fatalf("rotate status=%d body=%s", rotate.Code, rotate.Body.String())
	}

	legacy, err := service.Create(t.Context(), subscriptions.ActorFromPrincipal(principal, "legacy-api"),
		authz.Authenticated(principal), subscriptions.CreateInput{OrganizationID: orgID, RepositoryID: &repoID,
			URL: "https://runner.example.test/legacy", EventTypes: []string{"issue_comment.created"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE webhook_subscriptions SET url = $2 WHERE id = $1`,
		legacy.Subscription.ID, "https://runner.example.test/legacy?access_token=legacy-must-not-reflect"); err != nil {
		t.Fatal(err)
	}
	legacyRead := serveWebhook(t, mux, http.MethodGet, collection+"/"+legacy.Subscription.ID.String(), nil)
	if legacyRead.Code != http.StatusInternalServerError || strings.Contains(legacyRead.Body.String(), "legacy-must-not-reflect") ||
		strings.Contains(legacyRead.Body.String(), "access_token") {
		t.Fatalf("legacy read status=%d body=%s", legacyRead.Code, legacyRead.Body.String())
	}
}

func serveWebhook(t *testing.T, handler http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var encoded []byte
	if body != nil {
		var err error
		encoded, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	request := httptest.NewRequest(method, path, bytes.NewReader(encoded))
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func apiMigratedPool(t *testing.T) *pgxpool.Pool {
	databaseURL := strings.TrimSpace(os.Getenv("TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL")
	}
	admin, err := pgxpool.New(t.Context(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := "webhook_api_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(t.Context(), "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+quoted+" CASCADE") })
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
