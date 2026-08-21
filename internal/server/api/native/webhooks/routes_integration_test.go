package webhooks_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	service, err := subscriptions.New(store.New(pool), authorizer, keys, subscriptions.Config{
		Production: true, DestinationPreflight: rejectingDestinationPreflight{},
	})
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

	createValidationCases := []struct {
		name   string
		body   map[string]any
		reason string
		field  string
	}{
		{"destination url", map[string]any{"repository_id": repoID,
			"url":         "https://runner:credential-must-not-reflect@public.example.test/hook",
			"event_types": []string{"issue_comment.created"}}, "invalid_destination_url", "url"},
		{"destination denied", map[string]any{"repository_id": repoID,
			"url": "https://blocked.example.test/hook", "event_types": []string{"issue_comment.created"}},
			"destination_denied", "url"},
		{"event type", map[string]any{"repository_id": repoID, "url": "https://public.example.test/hook",
			"event_types": []string{"private-event-must-not-reflect"}}, "invalid_event_type", "event_types"},
		{"delivery policy", map[string]any{"repository_id": repoID, "url": "https://public.example.test/hook",
			"event_types": []string{}, "delivery_format": "github.v3", "signing_mode": "bearer"},
			"invalid_delivery_policy", "signing_mode"},
		{"retry policy", map[string]any{"repository_id": repoID, "url": "https://public.example.test/hook",
			"event_types": []string{"issue_comment.created"},
			"retry":       map[string]any{"max_attempts": 3, "initial_backoff": "duration-must-not-reflect", "max_backoff": "1m"}},
			"invalid_retry_policy", "retry.initial_backoff"},
		{"retry attempts", map[string]any{"repository_id": repoID, "url": "https://public.example.test/hook",
			"event_types": []string{"issue_comment.created"},
			"retry":       map[string]any{"max_attempts": 0, "initial_backoff": "1s", "max_backoff": "1m"}},
			"invalid_retry_policy", "retry.max_attempts"},
		{"destination query", map[string]any{"repository_id": repoID,
			"url":         "https://public.example.test/hook?access_token=query-must-not-reflect",
			"event_types": []string{"issue_comment.created"}}, "invalid_destination_query", "url"},
	}
	for _, test := range createValidationCases {
		t.Run("create "+test.name, func(t *testing.T) {
			response := serveWebhook(t, mux, http.MethodPost, collection, test.body)
			assertWebhookValidationProblem(t, response, test.reason, test.field)
		})
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
	organizationCreated := serveWebhook(t, mux, http.MethodPost, collection, map[string]any{
		"url":         "https://organization-runner.example.test/hook",
		"event_types": []string{"issue_comment.created", "issue_comment.edited"},
	})
	var organizationBody struct {
		ID     uuid.UUID `json:"id"`
		Secret string    `json:"secret"`
	}
	if organizationCreated.Code != http.StatusCreated ||
		json.Unmarshal(organizationCreated.Body.Bytes(), &organizationBody) != nil ||
		organizationBody.ID == uuid.Nil || organizationBody.Secret == "" {
		t.Fatalf("organization create status=%d body=%s", organizationCreated.Code, organizationCreated.Body.String())
	}
	verificationPath := collection + "/" + organizationBody.ID.String() + "/runner-verification"
	verified := serveWebhookWithAuthorization(t, mux, http.MethodPost, verificationPath,
		"Bearer "+organizationBody.Secret)
	if verified.Code != http.StatusOK || verified.Header().Get("Cache-Control") != "no-store" ||
		!strings.Contains(verified.Body.String(), `"scope_type":"organization"`) ||
		strings.Contains(verified.Body.String(), organizationBody.Secret) ||
		strings.Contains(verified.Body.String(), "organization-runner.example.test") ||
		strings.Contains(verified.Body.String(), `"url"`) {
		t.Fatalf("runner verification status=%d body=%s", verified.Code, verified.Body.String())
	}
	for _, test := range []struct {
		name          string
		path          string
		authorization string
	}{
		{name: "wrong secret", path: verificationPath, authorization: "Bearer profile-pat-must-not-authorize"},
		{name: "wrong organization", path: "/api/v1/orgs/" + uuid.NewString() + "/webhooks/" +
			organizationBody.ID.String() + "/runner-verification", authorization: "Bearer " + organizationBody.Secret},
		{name: "missing bearer", path: verificationPath},
	} {
		t.Run("runner verification "+test.name, func(t *testing.T) {
			response := serveWebhookWithAuthorization(t, mux, http.MethodPost, test.path, test.authorization)
			if response.Code != http.StatusNotFound || strings.Contains(response.Body.String(), organizationBody.Secret) {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
	itemPath := collection + "/" + createdBody.ID.String()
	updateBase := map[string]any{"expected_version": createdBody.RepresentationVersion,
		"url": "https://public.example.test/hook", "active": true,
		"event_types": []string{"issue_comment.created"}, "delivery_format": "issue-spec.v1",
		"signing_mode": "bearer", "retry": map[string]any{"max_attempts": 3,
			"initial_backoff": "1s", "max_backoff": "1m"}}
	updateValidationCases := []struct {
		name   string
		mutate func(map[string]any)
		reason string
		field  string
	}{
		{"destination url", func(body map[string]any) {
			body["url"] = "https://runner:credential-must-not-reflect@public.example.test/hook"
		}, "invalid_destination_url", "url"},
		{"destination denied", func(body map[string]any) {
			body["url"] = "https://blocked.example.test/hook"
		}, "destination_denied", "url"},
		{"event type", func(body map[string]any) {
			body["event_types"] = []string{"private-event-must-not-reflect"}
		}, "invalid_event_type", "event_types"},
		{"delivery policy", func(body map[string]any) {
			body["signing_mode"] = "none"
		}, "invalid_delivery_policy", "signing_mode"},
		{"retry policy", func(body map[string]any) {
			body["retry"] = map[string]any{"max_attempts": 3,
				"initial_backoff": "duration-must-not-reflect", "max_backoff": "1m"}
		}, "invalid_retry_policy", "retry.initial_backoff"},
		{"retry attempts", func(body map[string]any) {
			body["retry"] = map[string]any{"max_attempts": 0,
				"initial_backoff": "1s", "max_backoff": "1m"}
		}, "invalid_retry_policy", "retry.max_attempts"},
		{"destination query", func(body map[string]any) {
			body["url"] = "https://public.example.test/hook?access_token=query-must-not-reflect"
		}, "invalid_destination_query", "url"},
	}
	for _, test := range updateValidationCases {
		t.Run("update "+test.name, func(t *testing.T) {
			body := make(map[string]any, len(updateBase))
			for key, value := range updateBase {
				body[key] = value
			}
			test.mutate(body)
			response := serveWebhook(t, mux, http.MethodPatch, itemPath, body)
			assertWebhookValidationProblem(t, response, test.reason, test.field)
		})
	}
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

type rejectingDestinationPreflight struct{}

func (rejectingDestinationPreflight) Validate(_ context.Context, destination string) error {
	if strings.Contains(destination, "blocked.example.test") {
		return errors.New("resolved blocked.example.test to 10.0.0.7 via internal-dns")
	}
	return nil
}

func assertWebhookValidationProblem(t *testing.T, response *httptest.ResponseRecorder, reason, field string) {
	t.Helper()
	var problem struct {
		Code      string         `json:"code"`
		RequestID string         `json:"request_id"`
		Meta      map[string]any `json:"meta"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil {
		t.Fatalf("decode status=%d body=%s: %v", response.Code, response.Body.String(), err)
	}
	if response.Code != http.StatusUnprocessableEntity || problem.Code != reason ||
		problem.Meta["field"] != field || problem.RequestID == "" ||
		response.Header().Get("X-Request-ID") != problem.RequestID {
		t.Fatalf("status=%d headers=%v problem=%+v", response.Code, response.Header(), problem)
	}
	for _, forbidden := range []string{
		"credential-must-not-reflect", "duration-must-not-reflect", "private-event-must-not-reflect",
		"query-must-not-reflect", "access_token", "10.0.0.7", "internal-dns", "blocked.example.test",
	} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("validation response leaked %q: %s", forbidden, response.Body.String())
		}
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

func serveWebhookWithAuthorization(t *testing.T, handler http.Handler, method, path, authorization string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, nil)
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
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
