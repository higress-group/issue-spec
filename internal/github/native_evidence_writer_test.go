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

func TestAppendNativeReviewEvidenceReplaysStableIngestKey(t *testing.T) {
	orgID, repoID, issueID, evidenceID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	posts := 0
	created := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/context/repos/example/repository":
			_ = json.NewEncoder(w).Encode(map[string]any{"organization": map[string]any{"id": orgID, "name": "example"},
				"repository": map[string]any{"repository": map[string]any{"id": repoID, "organization_id": orgID, "name": "repository"}}, "authenticated": true})
		case "/api/v1/orgs/" + orgID.String() + "/repos/" + repoID.String() + "/issues/" + issueID.String() + "/evidence":
			item := map[string]any{"id": evidenceID.String(), "ingest_key": "review-submit:digest:FINDING-101",
				"provider_key": "code.example", "external_repository_id": "acme/widgets", "evidence_type": "review",
				"external_id": "FINDING-101", "normalized_state": "open", "subject_revision": "head-abc"}
			if r.Method == http.MethodGet {
				if got := r.URL.Query().Get("subject_revision"); got != "head-abc" {
					t.Fatalf("subject_revision=%q", got)
				}
				items := []any{}
				if created {
					items = append(items, item)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"evidence": items})
				return
			}
			posts++
			created = true
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["ingest_key"] != "review-submit:digest:FINDING-101" {
				t.Fatalf("append body=%v err=%v", body, err)
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(item)
		default:
			t.Fatalf("unexpected native request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()
	client, err := NewClientWithOptions(ClientOptions{Host: "issues.example.test", BaseURL: server.URL + "/api/v1",
		Token: "reviewer-token", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	input := NativeReviewEvidenceInput{OrganizationID: orgID.String(), RepositoryID: repoID.String(), IssueID: issueID.String(),
		ProviderKey: "code.example", ExternalRepository: "acme/widgets", ChangeID: "42",
		IngestKey: "review-submit:digest:FINDING-101", SubjectRevision: "head-abc", FindingID: "FINDING-101",
		ProcessID: "PROCESS-101", SpecID: "SPEC-002", Path: "review.go", Side: "RIGHT", Line: 10,
		Severity: "P1", Message: "fix", ReceiptID: "receipt-1", ReceiptDigest: strings.Repeat("a", 64)}
	first, err := client.AppendNativeReviewEvidence(t.Context(), "example/repository", 9, input)
	if err != nil || first.Replayed || first.EvidenceID != evidenceID.String() {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	second, err := client.AppendNativeReviewEvidence(t.Context(), "example/repository", 9, input)
	if err != nil || !second.Replayed || second.EvidenceID != evidenceID.String() || posts != 1 {
		t.Fatalf("second=%+v posts=%d err=%v", second, posts, err)
	}
}

func TestAppendNativeReviewEvidenceRecoversLostCreateResponse(t *testing.T) {
	orgID, repoID, issueID, evidenceID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	created := false
	path := "/api/v1/orgs/" + orgID.String() + "/repos/" + repoID.String() + "/issues/" + issueID.String() + "/evidence"
	item := map[string]any{"id": evidenceID.String(), "ingest_key": "review-submit:lost:FINDING-102",
		"provider_key": "code.example", "external_repository_id": "acme/widgets", "evidence_type": "review",
		"external_id": "FINDING-102", "normalized_state": "open", "subject_revision": "head-abc"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if r.Method == http.MethodGet {
			items := []any{}
			if created {
				items = append(items, item)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"evidence": items})
			return
		}
		created = true
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"truncated":`))
	}))
	defer server.Close()
	client, err := NewClientWithOptions(ClientOptions{Host: "issues.example.test", BaseURL: server.URL + "/api/v1",
		Token: "reviewer-token", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.AppendNativeReviewEvidence(t.Context(), "example/repository", 9, NativeReviewEvidenceInput{
		OrganizationID: orgID.String(), RepositoryID: repoID.String(), IssueID: issueID.String(), ProviderKey: "code.example",
		ExternalRepository: "acme/widgets", ChangeID: "42", IngestKey: "review-submit:lost:FINDING-102",
		SubjectRevision: "head-abc", FindingID: "FINDING-102", ProcessID: "PROCESS-101", SpecID: "SPEC-002",
		Path: "review.go", Side: "RIGHT", Line: 10, Severity: "P1", Message: "fix", ReceiptID: "receipt-1",
		ReceiptDigest: strings.Repeat("a", 64)})
	if err != nil || !result.Replayed || result.EvidenceID != evidenceID.String() {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}
