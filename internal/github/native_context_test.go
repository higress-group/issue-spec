package github

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNativeContextOperationsUseOnlyNativeProfilePaths(t *testing.T) {
	requests := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.Path)
		if got := r.Header.Get("Authorization"); got != "Bearer origin-parent-token" {
			t.Fatalf("authorization=%q", got)
		}
		switch r.URL.Path {
		case "/api/v1/context":
			_ = json.NewEncoder(w).Encode(NativeContext{User: NativeContextUser{ID: "user-id", Login: "runner"},
				Organizations: []NativeOrganizationContext{{ID: "org-id", Name: "owner"}}})
		case "/api/v1/context/orgs/org-id/repos":
			_ = json.NewEncoder(w).Encode(NativeRepositoriesContext{Repositories: []NativeRepositoryContext{{
				Repository: NativeRepositorySummary{ID: "repo-id", OrganizationID: "org-id", Name: "repo"},
			}}})
		default:
			t.Fatalf("unexpected native request %s", r.URL.Path)
		}
	}))
	defer server.Close()
	client, err := NewClientWithOptions(ClientOptions{Host: "issues.test", BaseURL: server.URL + "/api/v1",
		Token: "origin-parent-token", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	current, err := client.GetNativeContext(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	repositories, err := client.ListNativeContextRepositories(t.Context(), current.Organizations[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 || len(repositories.Repositories) != 1 ||
		repositories.Repositories[0].Repository.OrganizationID != "org-id" {
		t.Fatalf("requests=%v current=%+v repositories=%+v", requests, current, repositories)
	}
	for _, path := range requests {
		if path == "/notifications" {
			t.Fatal("native context resolution called notifications")
		}
	}
}
