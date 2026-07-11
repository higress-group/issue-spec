package admin_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	adminservice "github.com/higress-group/issue-spec/internal/server/admin"
	apierrors "github.com/higress-group/issue-spec/internal/server/api/github/errors"
	adminapi "github.com/higress-group/issue-spec/internal/server/api/native/admin"
	orgapi "github.com/higress-group/issue-spec/internal/server/api/native/orgs"
	repoapi "github.com/higress-group/issue-spec/internal/server/api/native/repos"
	"github.com/higress-group/issue-spec/internal/server/api/routeset"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
)

func TestNativeAdministrationRouteSetsFailClosedWithoutAuthorizer(t *testing.T) {
	service := &adminservice.Service{}
	authenticate := withPrincipal(serverauth.Principal{User: serverauth.User{ID: uuid.New(), Status: "active"}})
	tests := []struct {
		name string
		new  func(adminservice.Authorizer) error
	}{
		{name: "organizations", new: func(authorizer adminservice.Authorizer) error {
			_, err := orgapi.NewRouteSet(orgapi.Dependencies{Service: service, Authorizer: authorizer, Authenticate: authenticate})
			return err
		}},
		{name: "repositories", new: func(authorizer adminservice.Authorizer) error {
			_, err := repoapi.NewRouteSet(repoapi.Dependencies{Service: service, Authorizer: authorizer, Authenticate: authenticate})
			return err
		}},
		{name: "credentials", new: func(authorizer adminservice.Authorizer) error {
			_, err := adminapi.NewRouteSet(adminapi.Dependencies{Service: service, Authorizer: authorizer, Authenticate: authenticate})
			return err
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.new(nil); err == nil {
				t.Fatal("route set accepted a nil authorizer")
			}
		})
	}
}

func TestNativeAdministrationRouteSetsDenyBeforeTenantHandlers(t *testing.T) {
	service := &adminservice.Service{}
	principal := serverauth.Principal{User: serverauth.User{ID: uuid.New(), Login: "operator", Status: "active"}}
	orgID := uuid.New()
	tests := []struct {
		name   string
		path   string
		action adminservice.Action
		new    func(adminservice.Authorizer) (routeset.RouteSet, error)
	}{
		{name: "organizations", path: "/api/v1/orgs/" + orgID.String(), action: adminservice.ActionOrganizationRead,
			new: func(authorizer adminservice.Authorizer) (routeset.RouteSet, error) {
				return orgapi.NewRouteSet(orgapi.Dependencies{Service: service, Authorizer: authorizer, Authenticate: withPrincipal(principal)})
			}},
		{name: "repositories", path: "/api/v1/orgs/" + orgID.String() + "/repos", action: adminservice.ActionOrganizationRead,
			new: func(authorizer adminservice.Authorizer) (routeset.RouteSet, error) {
				return repoapi.NewRouteSet(repoapi.Dependencies{Service: service, Authorizer: authorizer, Authenticate: withPrincipal(principal)})
			}},
		{name: "credentials", path: "/api/v1/orgs/" + orgID.String() + "/service-accounts", action: adminservice.ActionOrganizationAdmin,
			new: func(authorizer adminservice.Authorizer) (routeset.RouteSet, error) {
				return adminapi.NewRouteSet(adminapi.Dependencies{Service: service, Authorizer: authorizer, Authenticate: withPrincipal(principal)})
			}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var received adminservice.AuthorizationRequest
			authorizer := adminservice.AuthorizerFunc(func(_ context.Context, _ serverauth.Principal, request adminservice.AuthorizationRequest) error {
				received = request
				return adminservice.ErrForbidden
			})
			set, err := test.new(authorizer)
			if err != nil {
				t.Fatal(err)
			}
			mux, err := routeset.NewMux(routeset.Policy{}, set)
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			request.Header.Set("X-Request-ID", "tenant-denied-"+test.name)
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, request)
			if response.Code != http.StatusForbidden {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
			if received.Action != test.action || received.OrganizationID != orgID {
				t.Fatalf("authorization request = %+v", received)
			}
			assertProblem(t, response, "forbidden", "tenant-denied-"+test.name)
		})
	}
}

func TestNativeAdministrationRouteSetsConcealInvisibleResources(t *testing.T) {
	service := &adminservice.Service{}
	principal := serverauth.Principal{User: serverauth.User{ID: uuid.New(), Login: "operator", Status: "active"}}
	orgID := uuid.New()
	authorizer := adminservice.AuthorizerFunc(func(context.Context, serverauth.Principal, adminservice.AuthorizationRequest) error {
		return adminservice.ErrNotFound
	})
	set, err := orgapi.NewRouteSet(orgapi.Dependencies{
		Service: service, Authorizer: authorizer, Authenticate: withPrincipal(principal),
	})
	if err != nil {
		t.Fatal(err)
	}
	mux, err := routeset.NewMux(routeset.Policy{}, set)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/orgs/"+orgID.String(), nil)
	request.Header.Set("X-Request-ID", "tenant-invisible")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	assertProblem(t, response, "not_found", "tenant-invisible")
}

func TestNativeAdministrationRouteSetsRejectMissingPrincipal(t *testing.T) {
	service := &adminservice.Service{}
	passThrough := func(next http.Handler) http.Handler { return next }
	orgID := uuid.New().String()
	tests := []struct {
		name string
		path string
		set  routeset.RouteSet
	}{
		{name: "organizations", path: "/api/v1/orgs/" + orgID},
		{name: "repositories", path: "/api/v1/orgs/" + orgID + "/repos"},
		{name: "credentials", path: "/api/v1/orgs/" + orgID + "/service-accounts"},
	}
	var err error
	tests[0].set, err = orgapi.NewRouteSet(orgapi.Dependencies{Service: service, Authorizer: adminservice.DenyAllAuthorizer{}, Authenticate: passThrough})
	if err != nil {
		t.Fatal(err)
	}
	tests[1].set, err = repoapi.NewRouteSet(repoapi.Dependencies{Service: service, Authorizer: adminservice.DenyAllAuthorizer{}, Authenticate: passThrough})
	if err != nil {
		t.Fatal(err)
	}
	tests[2].set, err = adminapi.NewRouteSet(adminapi.Dependencies{Service: service, Authorizer: adminservice.DenyAllAuthorizer{}, Authenticate: passThrough})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mux, err := routeset.NewMux(routeset.Policy{}, test.set)
			if err != nil {
				t.Fatal(err)
			}
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.path, nil))
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
			assertProblem(t, response, "authentication_required", response.Header().Get("X-Request-ID"))
		})
	}
}

func TestWithRequestIDGeneratesOnceAndReusesProblemIdentity(t *testing.T) {
	var first, second string
	handler := adminapi.WithRequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		first = adminapi.RequestID(r)
		second = adminapi.RequestID(r)
		adminapi.WriteProblem(w, http.StatusConflict, "conflict", "Resource conflict")
	}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if first == "" || first != second {
		t.Fatalf("request IDs first=%q second=%q", first, second)
	}
	assertProblem(t, response, "conflict", first)
}

func TestWithRequestIDReplacesUnsafeClientValue(t *testing.T) {
	handler := adminapi.WithRequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		adminapi.WriteJSON(w, http.StatusOK, map[string]string{"request_id": adminapi.RequestID(r)})
	}))
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("X-Request-ID", "unsafe request id with spaces")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if got := response.Header().Get("X-Request-ID"); got == "" || got == "unsafe request id with spaces" {
		t.Fatalf("normalized X-Request-ID = %q", got)
	}
	var body map[string]string
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["request_id"] != response.Header().Get("X-Request-ID") {
		t.Fatalf("body request_id=%q header=%q", body["request_id"], response.Header().Get("X-Request-ID"))
	}
}

func withPrincipal(principal serverauth.Principal) adminapi.Authenticate {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(serverauth.WithPrincipal(r.Context(), principal)))
		})
	}
}

func assertProblem(t *testing.T, response *httptest.ResponseRecorder, code, requestID string) {
	t.Helper()
	if got := response.Header().Get("Content-Type"); got != "application/problem+json" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := response.Header().Get("X-Request-ID"); got == "" || got != requestID {
		t.Fatalf("X-Request-ID = %q, want %q", got, requestID)
	}
	var problem apierrors.Problem
	if err := json.NewDecoder(response.Body).Decode(&problem); err != nil {
		t.Fatal(err)
	}
	if problem.Code != code || problem.RequestID != requestID || problem.Status != response.Code {
		t.Fatalf("problem = %+v", problem)
	}
}
