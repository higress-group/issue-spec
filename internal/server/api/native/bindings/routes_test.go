package bindingsapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	adminservice "github.com/higress-group/issue-spec/internal/server/admin"
	adminapi "github.com/higress-group/issue-spec/internal/server/api/native/admin"
	"github.com/higress-group/issue-spec/internal/server/api/routeset"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
	"github.com/higress-group/issue-spec/internal/server/authz"
	"github.com/higress-group/issue-spec/internal/server/bindings"
	"github.com/higress-group/issue-spec/internal/server/models"
)

func TestRouteSetValidationAndBindingHandlers(t *testing.T) {
	if _, err := NewRouteSet(Dependencies{}); err == nil {
		t.Fatal("NewRouteSet() accepted missing dependencies")
	}
	service := &fakeBindingsService{}
	set, err := NewRouteSet(Dependencies{Service: service, Authenticate: testAuthenticate})
	if err != nil || len(set.Routes) != 3 {
		t.Fatalf("NewRouteSet() = %+v, %v", set, err)
	}
	mux, err := routeset.NewMux(routeset.Policy{}, set)
	if err != nil {
		t.Fatal(err)
	}
	orgID, repoID := uuid.New(), uuid.New()
	path := "/api/v1/orgs/" + orgID.String() + "/repos/" + repoID.String() + "/bindings"
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{
		"provider_key":"github","external_repository_id":"acme/widgets",
		"clone_url":"https://code.example/acme/widgets.git","web_url":"https://code.example/acme/widgets",
		"default_branch":"main"}`))
	req.Header.Set("Authorization", "test")
	req.Header.Set("X-Request-ID", "binding-request")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, req)
	if response.Code != http.StatusCreated || response.Header().Get("Cache-Control") != "no-store" ||
		service.scope != (models.RepoScope{OrgID: orgID, RepoID: repoID}) || service.actor.RequestID != "binding-request" {
		t.Fatalf("create response=%d headers=%v scope=%+v actor=%+v body=%s", response.Code, response.Header(), service.scope, service.actor, response.Body.String())
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, path+"/active", nil)
	deleteReq.Header.Set("Authorization", "test")
	deleteResponse := httptest.NewRecorder()
	mux.ServeHTTP(deleteResponse, deleteReq)
	if deleteResponse.Code != http.StatusNoContent || !service.deactivated {
		t.Fatalf("delete response=%d deactivated=%t", deleteResponse.Code, service.deactivated)
	}
}

func TestBindingHandlersProblemsAndStrictJSON(t *testing.T) {
	service := &fakeBindingsService{}
	set, _ := NewRouteSet(Dependencies{Service: service, Authenticate: testAuthenticate})
	mux, _ := routeset.NewMux(routeset.Policy{}, set)
	validPath := "/api/v1/orgs/" + uuid.NewString() + "/repos/" + uuid.NewString() + "/bindings"

	unauthorized := httptest.NewRecorder()
	mux.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodPost, validPath, strings.NewReader(`{}`)))
	assertBindingProblem(t, unauthorized, http.StatusUnauthorized, "authentication_required", true)

	invalidUUID := httptest.NewRequest(http.MethodPost, "/api/v1/orgs/not-a-uuid/repos/"+uuid.NewString()+"/bindings", strings.NewReader(`{}`))
	invalidUUID.Header.Set("Authorization", "test")
	invalidResponse := httptest.NewRecorder()
	mux.ServeHTTP(invalidResponse, invalidUUID)
	assertBindingProblem(t, invalidResponse, http.StatusUnprocessableEntity, "invalid_request", true)

	unknown := httptest.NewRequest(http.MethodPost, validPath, strings.NewReader(`{"unknown":true}`))
	unknown.Header.Set("Authorization", "test")
	unknownResponse := httptest.NewRecorder()
	mux.ServeHTTP(unknownResponse, unknown)
	assertBindingProblem(t, unknownResponse, http.StatusBadRequest, "invalid_json", true)

	tooLarge := httptest.NewRequest(http.MethodPost, validPath, strings.NewReader(`{"provider_key":"`+strings.Repeat("x", 1<<20)+`"}`))
	tooLarge.Header.Set("Authorization", "test")
	largeResponse := httptest.NewRecorder()
	mux.ServeHTTP(largeResponse, tooLarge)
	assertBindingProblem(t, largeResponse, http.StatusBadRequest, "invalid_json", true)

	for _, test := range []struct {
		err  error
		want int
		code string
	}{{adminservice.ErrNotFound, http.StatusNotFound, "not_found"}, {adminservice.ErrForbidden, http.StatusForbidden, "forbidden"}} {
		service.err = test.err
		req := httptest.NewRequest(http.MethodGet, validPath+"/active", nil)
		req.Header.Set("Authorization", "test")
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, req)
		assertBindingProblem(t, response, test.want, test.code, true)
	}

	service.err = adminservice.ErrInvalidInput
	const secret = "BINDING_QUERY_TOKEN"
	queryToken := httptest.NewRequest(http.MethodPost, validPath, strings.NewReader(`{
		"provider_key":"code.example","external_repository_id":"acme/widgets",
		"clone_url":"https://code.example/acme/widgets.git",
		"web_url":"https://code.example/acme/widgets?access_token=`+secret+`","default_branch":"main"}`))
	queryToken.Header.Set("Authorization", "test")
	queryTokenResponse := httptest.NewRecorder()
	mux.ServeHTTP(queryTokenResponse, queryToken)
	assertBindingProblem(t, queryTokenResponse, http.StatusUnprocessableEntity, "invalid_request", true)
	if strings.Contains(queryTokenResponse.Body.String(), secret) {
		t.Fatalf("binding API reflected rejected credential: %s", queryTokenResponse.Body.String())
	}
}

type fakeBindingsService struct {
	scope       models.RepoScope
	actor       adminservice.Actor
	err         error
	deactivated bool
}

func (f *fakeBindingsService) ActiveBinding(context.Context, authz.Subject, models.RepoScope) (bindings.Binding, error) {
	return bindings.Binding{}, f.err
}

func (f *fakeBindingsService) CreateBindingVersion(_ context.Context, _ authz.Subject, actor adminservice.Actor, scope models.RepoScope, input bindings.CreateBindingVersionInput) (bindings.Binding, error) {
	f.scope, f.actor = scope, actor
	return bindings.Binding{ID: uuid.New(), Scope: scope, ProviderKey: input.ProviderKey, Version: 1, Active: true}, f.err
}

func (f *fakeBindingsService) DeactivateBinding(context.Context, authz.Subject, adminservice.Actor, models.RepoScope) error {
	f.deactivated = true
	return f.err
}

func testAuthenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			adminapi.WriteProblem(w, http.StatusUnauthorized, "authentication_required", "Authentication required")
			return
		}
		principal := serverauth.Principal{User: serverauth.User{ID: uuid.MustParse("11111111-1111-1111-1111-111111111111")}, Kind: serverauth.CredentialSession}
		next.ServeHTTP(w, r.WithContext(serverauth.WithPrincipal(r.Context(), principal)))
	})
}

func assertBindingProblem(t *testing.T, response *httptest.ResponseRecorder, status int, code string, requestID bool) {
	t.Helper()
	body := response.Body.String()
	if response.Code != status || !strings.Contains(body, `"code":"`+code+`"`) ||
		(requestID && (!strings.Contains(body, `"request_id"`) || response.Header().Get("X-Request-ID") == "")) {
		t.Fatalf("problem response=%d headers=%v body=%s", response.Code, response.Header(), body)
	}
}
