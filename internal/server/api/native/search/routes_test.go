package searchapi

import (
	"context"
	"errors"
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
	"github.com/higress-group/issue-spec/internal/server/models"
	searchservice "github.com/higress-group/issue-spec/internal/server/search"
)

func TestSearchRoutesAndScopes(t *testing.T) {
	if _, err := NewRouteSet(Dependencies{}); err == nil {
		t.Fatal("NewRouteSet accepted missing dependencies")
	}
	service := &fakeService{page: searchservice.Page{Items: []searchservice.Issue{{Number: 17, Title: "鉴权锁争用", Matches: []searchservice.Match{}}}, Page: 2, PerPage: 5}}
	mux := searchMux(t, service)
	orgID, repoID := uuid.New(), uuid.New()
	response := searchRequest(mux, "/api/v1/orgs/"+orgID.String()+"/repos/"+repoID.String()+"/search/issues?q=%E9%94%81&state=open&source=comments&stage=implement&page=2&per_page=5", true)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"title":"鉴权锁争用"`) ||
		service.repoScope != (models.RepoScope{OrgID: orgID, RepoID: repoID}) || service.options.Query != "锁" ||
		service.options.State != "open" || service.options.Source != searchservice.SourceComments || service.options.Stage != "implement" || service.options.Page != 2 {
		t.Fatalf("response=%d scope=%+v options=%+v body=%s", response.Code, service.repoScope, service.options, response.Body.String())
	}
	contextResponse := searchRequest(mux, "/api/v1/context/repos/acme/widgets/search/issues?q=alpha", false)
	if contextResponse.Code != http.StatusOK || service.owner != "acme" || service.repository != "widgets" {
		t.Fatalf("context response=%d owner=%s repo=%s", contextResponse.Code, service.owner, service.repository)
	}
	orgResponse := searchRequest(mux, "/api/v1/orgs/"+orgID.String()+"/search/issues?q=alpha", true)
	if orgResponse.Code != http.StatusOK || service.orgScope.OrgID != orgID {
		t.Fatalf("org response=%d scope=%+v", orgResponse.Code, service.orgScope)
	}
}

func TestSearchRouteAuthenticationValidationAndConcealment(t *testing.T) {
	service := &fakeService{page: searchservice.Page{Items: []searchservice.Issue{}, Page: 1, PerPage: 20}}
	mux := searchMux(t, service)
	orgID, repoID := uuid.NewString(), uuid.NewString()
	repoPath := "/api/v1/orgs/" + orgID + "/repos/" + repoID + "/search/issues?q=alpha"
	if response := searchRequest(mux, repoPath, false); response.Code != http.StatusOK {
		t.Fatalf("anonymous repository search=%d body=%s", response.Code, response.Body.String())
	}
	assertProblem(t, searchRequest(mux, "/api/v1/orgs/"+orgID+"/search/issues?q=alpha", false), 401, "authentication_required")
	for _, path := range []string{repoPath + "&unknown=x", strings.Replace(repoPath, "q=alpha", "q=", 1),
		strings.Replace(repoPath, "q=alpha", "q=alpha&page=no", 1), "/api/v1/orgs/bad/repos/" + repoID + "/search/issues?q=x"} {
		assertProblem(t, searchRequest(mux, path, true), 422, "invalid_request")
	}
	for _, item := range []struct {
		err    error
		status int
		code   string
	}{{adminservice.ErrNotFound, 404, "not_found"}, {adminservice.ErrForbidden, 403, "forbidden"},
		{searchservice.ErrInvalidOptions, 422, "invalid_request"}, {errors.New("database secret"), 500, "internal_error"}} {
		service.err = item.err
		response := searchRequest(mux, repoPath, true)
		assertProblem(t, response, item.status, item.code)
		if strings.Contains(response.Body.String(), "database secret") {
			t.Fatalf("internal error leaked: %s", response.Body.String())
		}
	}
}

type fakeService struct {
	page       searchservice.Page
	err        error
	repoScope  models.RepoScope
	orgScope   models.OrgScope
	options    searchservice.Options
	owner      string
	repository string
}

func (f *fakeService) Repository(_ context.Context, _ authz.Subject, scope models.RepoScope, options searchservice.Options) (searchservice.Page, error) {
	f.repoScope, f.options = scope, options
	return f.page, f.err
}

func (f *fakeService) Organization(_ context.Context, _ authz.Subject, scope models.OrgScope, options searchservice.Options) (searchservice.Page, error) {
	f.orgScope, f.options = scope, options
	return f.page, f.err
}

func (f *fakeService) ContextRepository(_ context.Context, _ authz.Subject, owner, repository string, options searchservice.Options) (searchservice.Page, error) {
	f.owner, f.repository, f.options = owner, repository, options
	return f.page, f.err
}

func searchMux(t *testing.T, service Service) http.Handler {
	t.Helper()
	set, err := NewRouteSet(Dependencies{Service: service, Authenticate: testAuthenticate, AuthenticateOptional: testAuthenticateOptional,
		WebOrigin: "https://issues.test"})
	if err != nil {
		t.Fatal(err)
	}
	mux, err := routeset.NewMux(routeset.Policy{}, set)
	if err != nil {
		t.Fatal(err)
	}
	return mux
}

func testAuthenticateOptional(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			next.ServeHTTP(w, r)
			return
		}
		testAuthenticate(next).ServeHTTP(w, r)
	})
}

func testAuthenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			adminapi.WriteProblem(w, http.StatusUnauthorized, "authentication_required", "Authentication required")
			return
		}
		principal := serverauth.Principal{User: serverauth.User{ID: uuid.MustParse("11111111-1111-4111-8111-111111111111")}, Kind: serverauth.CredentialSession}
		next.ServeHTTP(w, r.WithContext(serverauth.WithPrincipal(r.Context(), principal)))
	})
}

func searchRequest(handler http.Handler, path string, authenticated bool) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, path, nil)
	if authenticated {
		request.Header.Set("Authorization", "test")
	}
	request.Header.Set("X-Request-ID", "search-request")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func assertProblem(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if response.Code != status || !strings.Contains(response.Body.String(), `"code":"`+code+`"`) ||
		response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("X-Request-ID") == "" {
		t.Fatalf("problem=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
}
