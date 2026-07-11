package boardsapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	adminservice "github.com/higress-group/issue-spec/internal/server/admin"
	adminapi "github.com/higress-group/issue-spec/internal/server/api/native/admin"
	"github.com/higress-group/issue-spec/internal/server/api/routeset"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
	"github.com/higress-group/issue-spec/internal/server/authz"
	"github.com/higress-group/issue-spec/internal/server/changes"
	"github.com/higress-group/issue-spec/internal/server/models"
)

func TestBoardRoutesSuccessConditionalAndScopes(t *testing.T) {
	if _, err := NewRouteSet(Dependencies{}); err == nil {
		t.Fatal("NewRouteSet accepted missing dependencies")
	}
	service := &fakeBoardService{page: boardFixture()}
	mux := boardMux(t, service)
	orgID, repoID := uuid.New(), uuid.New()
	path := "/api/v1/orgs/" + orgID.String() + "/repos/" + repoID.String() + "/changes?stage=implement&page=2&per_page=5"
	response := boardRequest(mux, http.MethodGet, path, "", true, nil)
	if response.Code != http.StatusOK || response.Header().Get("ETag") != `"board-v1"` ||
		response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("X-Request-ID") == "" ||
		!strings.Contains(response.Body.String(), `"change_key":"alpha"`) {
		t.Fatalf("list response=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	if service.repoScope != (models.RepoScope{OrgID: orgID, RepoID: repoID}) || service.options.Stage != changes.StageImplement || service.options.Page != 2 || service.options.PerPage != 5 {
		t.Fatalf("scope=%+v options=%+v", service.repoScope, service.options)
	}
	conditional := boardRequest(mux, http.MethodGet, path, "", true, map[string]string{"If-None-Match": `"board-v1"`})
	if conditional.Code != http.StatusNotModified || conditional.Body.Len() != 0 || conditional.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("conditional=%d headers=%v body=%s", conditional.Code, conditional.Header(), conditional.Body.String())
	}
	weakListConditional := boardRequest(mux, http.MethodGet, path, "", true,
		map[string]string{"If-None-Match": `"other", W/"board-v1"`})
	if weakListConditional.Code != http.StatusNotModified || weakListConditional.Body.Len() != 0 {
		t.Fatalf("weak list conditional=%d headers=%v body=%s", weakListConditional.Code,
			weakListConditional.Header(), weakListConditional.Body.String())
	}
	modifiedSince := boardRequest(mux, http.MethodGet, path, "", true,
		map[string]string{"If-Modified-Since": service.page.LastModified.Add(time.Second).Format(http.TimeFormat)})
	if modifiedSince.Code != http.StatusNotModified || modifiedSince.Body.Len() != 0 {
		t.Fatalf("modified-since=%d headers=%v body=%s", modifiedSince.Code,
			modifiedSince.Header(), modifiedSince.Body.String())
	}
	detail := boardRequest(mux, http.MethodGet, "/api/v1/orgs/"+orgID.String()+"/repos/"+repoID.String()+"/changes/Alpha", "", true, nil)
	if detail.Code != http.StatusOK || service.changeKey != "Alpha" || !strings.Contains(detail.Body.String(), `"current_stage":"implement"`) {
		t.Fatalf("detail=%d key=%q body=%s", detail.Code, service.changeKey, detail.Body.String())
	}
	organization := boardRequest(mux, http.MethodGet, "/api/v1/orgs/"+orgID.String()+"/changes?lifecycle=active", "", true, nil)
	if organization.Code != http.StatusOK || service.orgScope.OrgID != orgID || service.options.Lifecycle != changes.LifecycleActive {
		t.Fatalf("organization=%d scope=%+v options=%+v body=%s", organization.Code, service.orgScope, service.options, organization.Body.String())
	}
}

func TestBoardRoutesStrictProblemsJSONAndQueryValidation(t *testing.T) {
	service := &fakeBoardService{page: boardFixture()}
	mux := boardMux(t, service)
	orgID, repoID := uuid.NewString(), uuid.NewString()
	base := "/api/v1/orgs/" + orgID + "/repos/" + repoID + "/changes"

	anonymous := boardRequest(mux, http.MethodGet, base, "", false, nil)
	if anonymous.Code != http.StatusOK {
		t.Fatalf("anonymous repository read=%d body=%s", anonymous.Code, anonymous.Body.String())
	}
	assertBoardProblem(t, boardRequest(mux, http.MethodGet, base, "", true, map[string]string{"Authorization": "invalid"}), http.StatusUnauthorized, "authentication_required")
	assertBoardProblem(t, boardRequest(mux, http.MethodPost, base+"/query", `{}`, false, nil), http.StatusUnauthorized, "authentication_required")
	assertBoardProblem(t, boardRequest(mux, http.MethodGet, "/api/v1/orgs/"+orgID+"/changes", "", false, nil), http.StatusUnauthorized, "authentication_required")
	assertBoardProblem(t, boardRequest(mux, http.MethodGet, "/api/v1/orgs/bad/repos/"+repoID+"/changes", "", true, nil), http.StatusUnprocessableEntity, "invalid_request")
	assertBoardProblem(t, boardRequest(mux, http.MethodGet, base+"?unknown=x", "", true, nil), http.StatusUnprocessableEntity, "invalid_request")
	assertBoardProblem(t, boardRequest(mux, http.MethodGet, base+"?page=zero", "", true, nil), http.StatusUnprocessableEntity, "invalid_request")
	assertBoardProblem(t, boardRequest(mux, http.MethodGet, base+"/alpha?stage=design", "", true, nil), http.StatusUnprocessableEntity, "invalid_request")
	assertBoardProblem(t, boardRequest(mux, http.MethodPost, base+"/query", `{"unknown":true}`, true, nil), http.StatusBadRequest, "invalid_json")
	assertBoardProblem(t, boardRequest(mux, http.MethodPost, base+"/query", `{`, true, nil), http.StatusBadRequest, "invalid_json")

	for _, test := range []struct {
		err    error
		status int
		code   string
	}{{adminservice.ErrNotFound, 404, "not_found"}, {adminservice.ErrForbidden, 403, "forbidden"}, {adminservice.ErrInvalidInput, 422, "invalid_request"}, {errors.New("database secret should not escape"), 500, "internal_error"}} {
		service.err = test.err
		response := boardRequest(mux, http.MethodGet, base, "", true, nil)
		assertBoardProblem(t, response, test.status, test.code)
		if strings.Contains(response.Body.String(), "database secret") {
			t.Fatalf("internal error leaked: %s", response.Body.String())
		}
	}
}

func TestBoardJSONQueryUsesStrictTypedOptions(t *testing.T) {
	service := &fakeBoardService{page: boardFixture()}
	mux := boardMux(t, service)
	orgID, repoID := uuid.NewString(), uuid.NewString()
	response := boardRequest(mux, http.MethodPost, "/api/v1/orgs/"+orgID+"/repos/"+repoID+"/changes/query",
		`{"stage":"design","lifecycle":"blocked","anomaly":"missing_required_links","page":3,"per_page":11}`, true, nil)
	if response.Code != http.StatusOK || service.options.Stage != changes.StageDesign || service.options.Lifecycle != changes.LifecycleBlocked ||
		service.options.Anomaly != changes.AnomalyMissingRequiredLinks || service.options.Page != 3 || service.options.PerPage != 11 {
		t.Fatalf("response=%d options=%+v body=%s", response.Code, service.options, response.Body.String())
	}
}

type fakeBoardService struct {
	page      changes.BoardPage
	err       error
	repoScope models.RepoScope
	orgScope  models.OrgScope
	options   changes.ListOptions
	changeKey string
}

func (f *fakeBoardService) RepositoryBoard(_ context.Context, _ authz.Subject, scope models.RepoScope, options changes.ListOptions) (changes.BoardPage, error) {
	f.repoScope, f.options = scope, options
	return f.page, f.err
}

func (f *fakeBoardService) OrganizationBoard(_ context.Context, _ authz.Subject, scope models.OrgScope, options changes.ListOptions) (changes.BoardPage, error) {
	f.orgScope, f.options = scope, options
	return f.page, f.err
}

func (f *fakeBoardService) Change(_ context.Context, _ authz.Subject, _ models.RepoScope, key string) (changes.ChangeCard, string, time.Time, error) {
	f.changeKey = key
	if f.err != nil {
		return changes.ChangeCard{}, "", time.Time{}, f.err
	}
	return f.page.Cards[0], f.page.Validator, f.page.LastModified, nil
}

func boardFixture() changes.BoardPage {
	card := changes.ChangeCard{Repository: changes.Repository{ID: uuid.New(), Name: "widgets", DisplayName: "Widgets"},
		ChangeKey: "alpha", Title: "Alpha", CurrentStage: changes.StageImplement, Lifecycle: changes.LifecycleActive,
		Anomalies: []string{}, UpdatedAt: time.Unix(1_700_000_000, 0).UTC()}
	return changes.BoardPage{Cards: []changes.ChangeCard{card}, Page: 1, PerPage: 20, Total: 1,
		Counts: changes.BoardCounts{Total: 1, Active: 1, Implement: 1}, Diagnostics: []changes.DiagnosticCount{},
		Validator: `"board-v1"`, LastModified: time.Unix(1_700_000_000, 0).UTC()}
}

func boardMux(t *testing.T, service Service) *http.ServeMux {
	t.Helper()
	set, err := NewRouteSet(Dependencies{Service: service, Authenticate: boardAuthenticate, AuthenticateOptional: boardAuthenticateOptional})
	if err != nil {
		t.Fatal(err)
	}
	mux, err := routeset.NewMux(routeset.Policy{}, set)
	if err != nil {
		t.Fatal(err)
	}
	return mux
}

func boardAuthenticateOptional(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			next.ServeHTTP(w, r)
			return
		}
		boardAuthenticate(next).ServeHTTP(w, r)
	})
}

func boardAuthenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			adminapi.WriteProblem(w, http.StatusUnauthorized, "authentication_required", "Authentication required")
			return
		}
		if r.Header.Get("Authorization") == "invalid" {
			adminapi.WriteProblem(w, http.StatusUnauthorized, "authentication_required", "Authentication required")
			return
		}
		principal := serverauth.Principal{User: serverauth.User{ID: uuid.MustParse("11111111-1111-4111-8111-111111111111")}, Kind: serverauth.CredentialSession}
		next.ServeHTTP(w, r.WithContext(serverauth.WithPrincipal(r.Context(), principal)))
	})
}

func boardRequest(mux http.Handler, method, path, body string, authenticated bool, headers map[string]string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if authenticated {
		request.Header.Set("Authorization", "test")
	}
	request.Header.Set("X-Request-ID", "board-request")
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	return response
}

func assertBoardProblem(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if response.Code != status || response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("X-Request-ID") == "" ||
		!strings.HasPrefix(response.Header().Get("Content-Type"), "application/problem+json") || !strings.Contains(response.Body.String(), `"code":"`+code+`"`) ||
		!strings.Contains(response.Body.String(), `"request_id"`) {
		t.Fatalf("problem=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
}
