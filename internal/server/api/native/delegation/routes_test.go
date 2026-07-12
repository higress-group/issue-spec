package delegationapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/capability"
	"github.com/higress-group/issue-spec/internal/server/api/routeset"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
	"github.com/higress-group/issue-spec/internal/server/auth/delegation"
	"github.com/higress-group/issue-spec/internal/server/models"
)

func TestExchangeReturnsPlaintextOnceWithNoStore(t *testing.T) {
	orgID, repoID, tokenID := uuid.New(), uuid.New(), uuid.New()
	service := &fakeService{created: delegation.Created{ID: tokenID, Plaintext: "dgt_secret", ExpiresAt: time.Now().Add(time.Minute)}}
	mux := testMux(t, service)
	body := `{"job_id":"job-1","purpose":"issue-api","audience":"runner.test","subject":"runner-child","scopes":["issues:write"],"operations":["artifact.write"],"ttl_seconds":120}`
	request := httptest.NewRequest(http.MethodPost, "/api/v1/orgs/"+orgID.String()+"/repos/"+repoID.String()+"/delegated-tokens/exchange", strings.NewReader(body))
	request.Header.Set("X-Request-ID", "request-1")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusCreated || response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("X-Request-ID") != "request-1" {
		t.Fatalf("response = %d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	var got delegation.Created
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil || got.Plaintext != "dgt_secret" || got.ID != tokenID {
		t.Fatalf("created = %+v, %v", got, err)
	}
	if service.issue.JobID != "job-1" || service.issue.Purpose != PurposeIssueAPI || service.issue.Audience != "runner.test" || service.issue.RequestID != "request-1" ||
		service.issue.Repo.OrgID != orgID || service.issue.Repo.RepoID != repoID || len(service.issue.Operations) != 1 ||
		service.issue.Operations[0] != capability.OperationArtifactWrite {
		t.Fatalf("issue input = %+v", service.issue)
	}
}

func TestExchangeRejectsUnknownFieldsAndWrongRealm(t *testing.T) {
	orgID, repoID := uuid.New(), uuid.New()
	service := &fakeService{}
	mux := testMux(t, service)
	base := "/api/v1/orgs/" + orgID.String() + "/repos/" + repoID.String() + "/delegated-tokens/exchange"
	for name, tc := range map[string]struct {
		body   string
		status int
	}{
		"unknown":    {`{"job_id":"j","purpose":"issue-api","audience":"runner.test","subject":"runner-child","scopes":["issues:write"],"ttl_seconds":60,"token":"no"}`, http.StatusBadRequest},
		"purpose":    {`{"job_id":"j","purpose":"git","audience":"runner.test","subject":"runner-child","scopes":["issues:write"],"ttl_seconds":60}`, http.StatusUnprocessableEntity},
		"audience":   {`{"job_id":"j","purpose":"issue-api","audience":"other","subject":"runner-child","scopes":["issues:write"],"ttl_seconds":60}`, http.StatusUnprocessableEntity},
		"operations": {`{"job_id":"j","purpose":"issue-api","audience":"runner.test","subject":"runner-child","scopes":["issues:write"],"ttl_seconds":60}`, http.StatusUnprocessableEntity},
	} {
		t.Run(name, func(t *testing.T) {
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, base, strings.NewReader(tc.body)))
			if response.Code != tc.status || strings.Contains(response.Body.String(), "token") {
				t.Fatalf("response = %d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestExchangeConcealsScopeAndRevokeJob(t *testing.T) {
	orgID, repoID := uuid.New(), uuid.New()
	service := &fakeService{issueErr: serverauth.ErrInsufficientScope}
	mux := testMux(t, service)
	body := `{"job_id":"j","purpose":"issue-api","audience":"runner.test","subject":"runner-child","scopes":["issues:write"],"operations":["artifact.write"],"ttl_seconds":60}`
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/orgs/"+orgID.String()+"/repos/"+repoID.String()+"/delegated-tokens/exchange", strings.NewReader(body)))
	if response.Code != http.StatusNotFound || strings.Contains(response.Body.String(), "scope") {
		t.Fatalf("concealed response = %d %s", response.Code, response.Body.String())
	}
	service.issueErr = nil
	response = httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/api/v1/orgs/"+orgID.String()+"/repos/"+repoID.String()+"/jobs/job-2/delegated-tokens", nil))
	if response.Code != http.StatusNoContent || response.Header().Get("Cache-Control") != "no-store" || service.revokedJob != "job-2" {
		t.Fatalf("revoke response = %d headers=%v job=%q", response.Code, response.Header(), service.revokedJob)
	}
}

func testMux(t *testing.T, service *fakeService) http.Handler {
	t.Helper()
	set, err := NewRouteSet(Dependencies{Service: service, Audience: "runner.test", Subject: "runner-child", Authenticate: func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal := serverauth.Principal{User: serverauth.User{ID: uuid.New(), Status: "active"}, Kind: serverauth.CredentialPAT,
				CredentialID: uuid.New(), Scopes: []string{"runner:delegate", "issues:write"}, RepoRestricted: true}
			next.ServeHTTP(w, r.WithContext(serverauth.WithPrincipal(r.Context(), principal)))
		})
	}})
	if err != nil {
		t.Fatal(err)
	}
	mux, err := routeset.NewMux(routeset.Policy{}, set)
	if err != nil {
		t.Fatal(err)
	}
	return mux
}

type fakeService struct {
	issue      delegation.IssueInput
	created    delegation.Created
	issueErr   error
	revokedJob string
	revokeErr  error
}

func (f *fakeService) Issue(_ context.Context, input delegation.IssueInput) (delegation.Created, error) {
	f.issue = input
	return f.created, f.issueErr
}

func (f *fakeService) RevokeJobAs(_ context.Context, _ serverauth.Principal, _ models.RepoScope, jobID, _ string) error {
	f.revokedJob = jobID
	return f.revokeErr
}
