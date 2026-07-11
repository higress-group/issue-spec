package referencesapi

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

func TestReferenceRouteSetSuccessRedactionAndDelete(t *testing.T) {
	if _, err := NewRouteSet(Dependencies{}); err == nil {
		t.Fatal("NewRouteSet() accepted missing dependencies")
	}
	service := &fakeReferenceService{items: []bindings.Reference{{ID: uuid.New(), Visibility: bindings.VisibilityRepository, Metadata: nil}}}
	set, err := NewRouteSet(Dependencies{Service: service, Authenticate: referenceAuthenticate})
	if err != nil || len(set.Routes) != 3 {
		t.Fatalf("NewRouteSet() = %+v, %v", set, err)
	}
	mux, _ := routeset.NewMux(routeset.Policy{}, set)
	orgID, repoID, issueID := uuid.New(), uuid.New(), uuid.New()
	path := "/api/v1/orgs/" + orgID.String() + "/repos/" + repoID.String() + "/issues/" + issueID.String() + "/references"

	list := httptest.NewRequest(http.MethodGet, path, nil)
	list.Header.Set("Authorization", "test")
	listResponse := httptest.NewRecorder()
	mux.ServeHTTP(listResponse, list)
	if listResponse.Code != http.StatusOK || listResponse.Header().Get("Cache-Control") != "no-store" ||
		strings.Contains(listResponse.Body.String(), "metadata") {
		t.Fatalf("list response=%d headers=%v body=%s", listResponse.Code, listResponse.Header(), listResponse.Body.String())
	}

	upsert := httptest.NewRequest(http.MethodPut, path, strings.NewReader(`{
		"provider_key":"github","relation_kind":"code-change","external_repository_id":"acme/widgets",
		"external_id":"42","canonical_url":"https://code.example/acme/widgets/pull/42",
		"lifecycle_state":"open","visibility":"repository","metadata":{"safe":true}}`))
	upsert.Header.Set("Authorization", "test")
	upsert.Header.Set("X-Request-ID", "reference-request")
	upsertResponse := httptest.NewRecorder()
	mux.ServeHTTP(upsertResponse, upsert)
	if upsertResponse.Code != http.StatusOK || service.input.IssueID != issueID || service.actor.RequestID != "reference-request" {
		t.Fatalf("upsert response=%d input=%+v actor=%+v body=%s", upsertResponse.Code, service.input, service.actor, upsertResponse.Body.String())
	}

	referenceID := uuid.New()
	deleteRequest := httptest.NewRequest(http.MethodDelete, path+"/"+referenceID.String(), nil)
	deleteRequest.Header.Set("Authorization", "test")
	deleteResponse := httptest.NewRecorder()
	mux.ServeHTTP(deleteResponse, deleteRequest)
	if deleteResponse.Code != http.StatusNoContent || service.deleted != referenceID {
		t.Fatalf("delete response=%d deleted=%s", deleteResponse.Code, service.deleted)
	}
}

func TestReferenceProblemsStrictJSONUUIDVisibilityAndConcealment(t *testing.T) {
	service := &fakeReferenceService{}
	set, _ := NewRouteSet(Dependencies{Service: service, Authenticate: referenceAuthenticate})
	mux, _ := routeset.NewMux(routeset.Policy{}, set)
	issueID := uuid.New()
	path := "/api/v1/orgs/" + uuid.NewString() + "/repos/" + uuid.NewString() + "/issues/" + issueID.String() + "/references"

	unauthorized := httptest.NewRecorder()
	mux.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, path, nil))
	assertReferenceProblem(t, unauthorized, http.StatusUnauthorized, "authentication_required")

	invalid := httptest.NewRequest(http.MethodGet, "/api/v1/orgs/nope/repos/"+uuid.NewString()+"/issues/"+issueID.String()+"/references", nil)
	invalid.Header.Set("Authorization", "test")
	invalidResponse := httptest.NewRecorder()
	mux.ServeHTTP(invalidResponse, invalid)
	assertReferenceProblem(t, invalidResponse, http.StatusUnprocessableEntity, "invalid_request")

	for _, body := range []string{`{"unknown":true}`, `{"provider_key":"` + strings.Repeat("x", 1<<20) + `"}`} {
		req := httptest.NewRequest(http.MethodPut, path, strings.NewReader(body))
		req.Header.Set("Authorization", "test")
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, req)
		assertReferenceProblem(t, response, http.StatusBadRequest, "invalid_json")
	}

	service.validateVisibility = true
	badVisibility := httptest.NewRequest(http.MethodPut, path, strings.NewReader(`{
		"provider_key":"github","relation_kind":"code","external_repository_id":"a/b","external_id":"1",
		"canonical_url":"https://example.test/1","visibility":"private"}`))
	badVisibility.Header.Set("Authorization", "test")
	badVisibilityResponse := httptest.NewRecorder()
	mux.ServeHTTP(badVisibilityResponse, badVisibility)
	assertReferenceProblem(t, badVisibilityResponse, http.StatusUnprocessableEntity, "invalid_request")
	service.validateVisibility = false

	for _, test := range []struct {
		err  error
		want int
		code string
	}{{adminservice.ErrNotFound, http.StatusNotFound, "not_found"}, {adminservice.ErrForbidden, http.StatusForbidden, "forbidden"}} {
		service.err = test.err
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "test")
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, req)
		assertReferenceProblem(t, response, test.want, test.code)
	}
}

type fakeReferenceService struct {
	items              []bindings.Reference
	input              bindings.UpsertReferenceInput
	actor              adminservice.Actor
	deleted            uuid.UUID
	err                error
	validateVisibility bool
}

func (f *fakeReferenceService) ListReferences(context.Context, authz.Subject, models.RepoScope, uuid.UUID) ([]bindings.Reference, error) {
	return f.items, f.err
}

func (f *fakeReferenceService) UpsertReference(_ context.Context, _ authz.Subject, actor adminservice.Actor, _ models.RepoScope, input bindings.UpsertReferenceInput) (bindings.Reference, error) {
	f.input, f.actor = input, actor
	if f.validateVisibility && input.Visibility != bindings.VisibilityRepository && input.Visibility != bindings.VisibilityMaintainers {
		return bindings.Reference{}, adminservice.ErrInvalidInput
	}
	return bindings.Reference{ID: uuid.New(), IssueID: input.IssueID, Visibility: input.Visibility}, f.err
}

func (f *fakeReferenceService) DeleteReference(_ context.Context, _ authz.Subject, _ adminservice.Actor, _ models.RepoScope, _ uuid.UUID, referenceID uuid.UUID) error {
	f.deleted = referenceID
	return f.err
}

func referenceAuthenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			adminapi.WriteProblem(w, http.StatusUnauthorized, "authentication_required", "Authentication required")
			return
		}
		principal := serverauth.Principal{User: serverauth.User{ID: uuid.MustParse("22222222-2222-2222-2222-222222222222")}, Kind: serverauth.CredentialSession}
		next.ServeHTTP(w, r.WithContext(serverauth.WithPrincipal(r.Context(), principal)))
	})
}

func assertReferenceProblem(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	body := response.Body.String()
	if response.Code != status || !strings.Contains(body, `"code":"`+code+`"`) || !strings.Contains(body, `"request_id"`) || response.Header().Get("X-Request-ID") == "" {
		t.Fatalf("problem response=%d headers=%v body=%s", response.Code, response.Header(), body)
	}
}
