package github

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/server/models"
)

func TestNativeOnboardingOperationsUseNativeProfilePaths(t *testing.T) {
	orgID := uuid.New()
	repoID := uuid.New()
	bindingID := uuid.New()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/meta":
			if r.Header.Get("Authorization") != "Bearer realm-token" {
				t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"api_version": "v1", "server_instance_id": "issue-spec:test",
				"api_url": onboardingServerURL(r), "native_api_url": onboardingServerURL(r) + "/api/v1",
				"web_url": onboardingServerURL(r), "transport": map[string]any{"mode": "loopback-http", "secure": false},
				"features": map[string]any{"requirements_onboarding": true},
				"providers": []map[string]any{{"provider_key": "aone", "display_name": "Aone Code",
					"code_change_label": "Merge request", "capabilities": []string{"evidence.snapshot"}}}})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/orgs/"+orgID.String()+"/repos/ensure":
			if r.Header.Get("Authorization") != "Bearer realm-token" {
				t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"created": true, "repository": map[string]any{
				"id": repoID, "organization_id": orgID, "name": "httpbin", "display_name": "httpbin",
				"visibility": "private", "default_branch": "main", "contribution_policy": "members"}})
		case r.Method == http.MethodPut && r.URL.Path == "/api/v1/orgs/"+orgID.String()+"/repos/"+repoID.String()+"/bindings/active":
			if r.Header.Get("Authorization") != "Bearer realm-token" {
				t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"created": true, "binding": map[string]any{
				"id": bindingID, "provider_key": "aone", "external_repository_id": "Ingress/httpbin",
				"clone_url": "https://gitlab.alibaba-inc.com/Ingress/httpbin.git",
				"web_url":   "https://code.alibaba-inc.com/Ingress/httpbin", "default_branch": "main",
				"version": 1, "active": true, "created_at": "2026-07-12T00:00:00Z", "updated_at": "2026-07-12T00:00:00Z"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, err := NewClientWithOptions(ClientOptions{Host: "127.0.0.1", BaseURL: server.URL + "/api/v1",
		Token: "realm-token", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := client.GetNativeServerMetadata(t.Context())
	if err != nil || metadata.ServerInstanceID != "issue-spec:test" || metadata.APIURL != server.URL ||
		metadata.NativeAPIURL != server.URL+"/api/v1" || len(metadata.Providers) != 1 || !metadata.Features.RequirementsOnboarding {
		t.Fatalf("metadata = %+v, %v", metadata, err)
	}
	repository, err := client.EnsureNativeRepository(t.Context(), orgID.String(), NativeEnsureRepositoryInput{
		Name: "httpbin", DisplayName: "httpbin", DefaultBranch: "main"})
	if err != nil || !repository.Created || repository.Repository.ID != repoID.String() {
		t.Fatalf("repository = %+v, %v", repository, err)
	}
	binding, err := client.EnsureNativeActiveBinding(t.Context(), models.RepoScope{OrgID: orgID, RepoID: repoID}, NativeEnsureBindingInput{
		ProviderKey: "aone", ExternalRepositoryID: "Ingress/httpbin",
		CloneURL: "https://gitlab.alibaba-inc.com/Ingress/httpbin.git",
		WebURL:   "https://code.alibaba-inc.com/Ingress/httpbin", DefaultBranch: "main"})
	if err != nil || !binding.Created || binding.Binding.ID != bindingID.String() {
		t.Fatalf("binding = %+v, %v", binding, err)
	}
}

func TestNativeServerMetadataDiscoveryDoesNotSendCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "" {
			t.Fatalf("credential-free discovery sent Authorization=%q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"api_version": "v1", "server_instance_id": "issue-spec:test",
			"api_url": onboardingServerURL(r), "native_api_url": onboardingServerURL(r) + "/api/v1",
			"web_url": onboardingServerURL(r), "transport": map[string]any{"mode": "loopback-http", "secure": false},
			"features": map[string]any{"requirements_onboarding": true}})
	}))
	defer server.Close()
	client, err := NewClientWithOptions(ClientOptions{Host: "127.0.0.1", BaseURL: server.URL + "/api/v1", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := client.GetNativeServerMetadata(t.Context())
	if err != nil || !metadata.Features.RequirementsOnboarding {
		t.Fatalf("metadata=%+v err=%v", metadata, err)
	}
}

func onboardingServerURL(r *http.Request) string { return "http://" + r.Host }
