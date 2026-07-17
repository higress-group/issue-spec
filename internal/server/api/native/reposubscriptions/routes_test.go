package reposubscriptions

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/server/api/github/conditional"
	"github.com/higress-group/issue-spec/internal/server/authz"
	"github.com/higress-group/issue-spec/internal/server/models"
	"github.com/higress-group/issue-spec/internal/server/publicurl"
)

type fakeService struct {
	resource   models.RepositoryResource
	item       models.RepositorySubscription
	setValue   bool
	setInvoked int
}

func (f *fakeService) GetByName(context.Context, string, string, authz.Subject) (models.RepositoryResource, models.RepositorySubscription, error) {
	return f.resource, f.item, nil
}
func (f *fakeService) GetByScope(context.Context, models.RepoScope, authz.Subject) (models.RepositoryResource, models.RepositorySubscription, error) {
	return f.resource, f.item, nil
}
func (f *fakeService) SetByName(_ context.Context, _, _ string, _ authz.Subject, subscribed bool) (models.RepositoryResource, models.RepositorySubscription, bool, error) {
	f.setValue, f.setInvoked = subscribed, f.setInvoked+1
	f.item.Subscribed, f.item.Reason = subscribed, "manual"
	return f.resource, f.item, true, nil
}
func (f *fakeService) SetByScope(_ context.Context, _ models.RepoScope, _ authz.Subject, subscribed bool) (models.RepositoryResource, models.RepositorySubscription, bool, error) {
	return f.SetByName(context.Background(), "", "", authz.Subject{}, subscribed)
}

func TestRouteSetOwnsCompatibleAndNativeGetPutDelete(t *testing.T) {
	service, origins := routeFixture(t)
	set, err := NewRouteSet(Dependencies{Service: service, Origins: origins})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"GET /repos/{owner}/{repo}/subscription": false, "PUT /repos/{owner}/{repo}/subscription": false,
		"DELETE /repos/{owner}/{repo}/subscription":           false,
		"GET /api/v1/orgs/{org}/repos/{repo}/subscription":    false,
		"PUT /api/v1/orgs/{org}/repos/{repo}/subscription":    false,
		"DELETE /api/v1/orgs/{org}/repos/{repo}/subscription": false,
	}
	for _, route := range set.Routes {
		want[route.Method+" "+route.Pattern] = true
	}
	for route, found := range want {
		if !found {
			t.Fatalf("missing route %s", route)
		}
	}
}

func TestCompatibleGetKeepsShapeAndCollectionValidator(t *testing.T) {
	service, origins := routeFixture(t)
	h := handlers{service: service, origins: origins, conditional: conditional.Policy{}}
	request := httptest.NewRequest(http.MethodGet, "/repos/acme/widgets/subscription", nil)
	request.SetPathValue("owner", "acme")
	request.SetPathValue("repo", "widgets")
	response := httptest.NewRecorder()
	h.compatGet(response, request)
	if response.Code != http.StatusOK || response.Header().Get("ETag") == "" ||
		!contains(response.Body.String(), `"subscribed":true`, `"reason":"manual"`, `/repos/acme/widgets/subscription`) {
		t.Fatalf("compatible response = %d %v %s", response.Code, response.Header(), response.Body.String())
	}
	request = httptest.NewRequest(http.MethodGet, "/repos/acme/widgets/subscription", nil)
	request.SetPathValue("owner", "acme")
	request.SetPathValue("repo", "widgets")
	request.Header.Set("If-None-Match", response.Header().Get("ETag"))
	notModified := httptest.NewRecorder()
	h.compatGet(notModified, request)
	if notModified.Code != http.StatusNotModified {
		t.Fatalf("conditional status = %d body=%s", notModified.Code, notModified.Body.String())
	}
}

func TestCompatibleMutationsAreExplicitAndIdempotentAtServiceBoundary(t *testing.T) {
	service, origins := routeFixture(t)
	h := handlers{service: service, origins: origins, conditional: conditional.Policy{}}
	put := httptest.NewRequest(http.MethodPut, "/repos/acme/widgets/subscription", nil)
	put.SetPathValue("owner", "acme")
	put.SetPathValue("repo", "widgets")
	putResponse := httptest.NewRecorder()
	h.compatPut(putResponse, put)
	if putResponse.Code != http.StatusOK || !service.setValue {
		t.Fatalf("PUT = %d state=%v", putResponse.Code, service.setValue)
	}
	deleteRequest := httptest.NewRequest(http.MethodDelete, "/repos/acme/widgets/subscription", nil)
	deleteRequest.SetPathValue("owner", "acme")
	deleteRequest.SetPathValue("repo", "widgets")
	deleteResponse := httptest.NewRecorder()
	h.compatDelete(deleteResponse, deleteRequest)
	if deleteResponse.Code != http.StatusNoContent || service.setValue || service.setInvoked != 2 {
		t.Fatalf("DELETE = %d state=%v calls=%d", deleteResponse.Code, service.setValue, service.setInvoked)
	}
}

func routeFixture(t *testing.T) (*fakeService, publicurl.Origins) {
	t.Helper()
	origins, err := publicurl.New("https://api.example.test", "https://issues.example.test", nil)
	if err != nil {
		t.Fatal(err)
	}
	scope := models.RepoScope{OrgID: uuid.New(), RepoID: uuid.New()}
	service := &fakeService{resource: models.RepositoryResource{Scope: scope, Owner: "acme", Name: "widgets"},
		item: models.RepositorySubscription{UserID: uuid.New(), Subscribed: true, Reason: "manual",
			RepresentationVersion: 1, CollectionVersion: 3, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}}
	return service, origins
}

func contains(value string, items ...string) bool {
	for _, item := range items {
		if !strings.Contains(value, item) {
			return false
		}
	}
	return true
}
