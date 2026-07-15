package github

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestNativeEvidenceWriterStatusResolvesExactRepositoryAndUsesReadOnlyRoutes(t *testing.T) {
	orgID, repoID, userID := uuid.New(), uuid.New(), uuid.New()
	requests := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		if r.Method != http.MethodGet {
			t.Fatalf("unexpected mutation request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer runner-token" {
			t.Fatalf("authorization=%q", got)
		}
		switch r.URL.Path {
		case "/api/v1/context/repos/example/repository":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"organization": map[string]any{"id": orgID, "name": "example"},
				"repository": map[string]any{"repository": map[string]any{
					"id": repoID, "organization_id": orgID, "name": "repository",
				}},
				"authenticated": true,
			})
		case "/api/v1/orgs/" + orgID.String() + "/repos/" + repoID.String() + "/evidence/writers/me":
			_ = json.NewEncoder(w).Encode(NativeEvidenceWriterStatus{UserID: userID.String(), Login: "runner", Active: true})
		default:
			t.Fatalf("unexpected native request %s", r.URL.Path)
		}
	}))
	defer server.Close()
	client, err := NewClientWithOptions(ClientOptions{Host: "issues.example.test", BaseURL: server.URL + "/api/v1",
		Token: "runner-token", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	status, err := client.GetNativeEvidenceWriterStatus(t.Context(), "example/repository")
	if err != nil || status.UserID != userID.String() || status.Login != "runner" || !status.Active {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	if len(requests) != 2 || !strings.HasSuffix(requests[1], "/evidence/writers/me") {
		t.Fatalf("requests=%v", requests)
	}
}

func TestNativeEvidenceWriterStatusRejectsMismatchedRepositoryContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"organization": map[string]any{"id": uuid.New(), "name": "other"},
			"repository": map[string]any{"repository": map[string]any{
				"id": uuid.New(), "organization_id": uuid.New(), "name": "repository",
			}},
			"authenticated": true,
		})
	}))
	defer server.Close()
	client, err := NewClientWithOptions(ClientOptions{Host: "issues.example.test", BaseURL: server.URL + "/api/v1",
		Token: "runner-token", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.GetNativeEvidenceWriterStatus(t.Context(), "example/repository"); err == nil ||
		!strings.Contains(err.Error(), "mismatched") {
		t.Fatalf("mismatched repository context error=%v", err)
	}
}
