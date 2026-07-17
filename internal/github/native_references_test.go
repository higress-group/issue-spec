package github

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/server/models"
)

func TestNativeReferenceUpsertForwardsConditionalFieldsAndValidatesResponse(t *testing.T) {
	orgID, repoID, issueID, referenceID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/api/v1/orgs/"+orgID.String()+"/repos/"+repoID.String()+"/issues/"+issueID.String()+"/references" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer native-token" || r.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("request headers=%v", r.Header)
		}
		var request map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if string(request["refresh"]) != "true" || string(request["expected_version"]) != "4" || request["issue_id"] != nil {
			t.Fatalf("conditional request=%s", request)
		}
		_ = json.NewEncoder(w).Encode(NativeReference{ID: referenceID.String(), IssueID: issueID.String(),
			ProviderKey: "aone-bridge", RelationKind: "code_change", ExternalRepositoryID: "acme/widgets",
			ExternalID: "42", CanonicalURL: "https://code.example/acme/widgets/changes/42/revisions/def456",
			LifecycleState: "active", Visibility: "repository", Metadata: json.RawMessage(`{"head_revision":"def456"}`),
			RepresentationVersion: 5, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()})
	}))
	defer server.Close()
	client, err := NewClientWithOptions(ClientOptions{Host: "issues.example.test", BaseURL: server.URL + "/api/v1",
		Token: "native-token", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	expected := int64(4)
	result, err := client.UpsertNativeReference(t.Context(), models.RepoScope{OrgID: orgID, RepoID: repoID}, issueID,
		NativeUpsertReferenceInput{ProviderKey: "aone-bridge", RelationKind: "code_change",
			ExternalRepositoryID: "acme/widgets", ExternalID: "42",
			CanonicalURL:   "https://code.example/acme/widgets/changes/42/revisions/def456",
			LifecycleState: "active", Visibility: "repository", Metadata: json.RawMessage(`{"head_revision":"def456"}`),
			Refresh: true, ExpectedVersion: &expected})
	if err != nil || result.ID != referenceID.String() || result.RepresentationVersion != 5 {
		t.Fatalf("native reference result=%+v error=%v", result, err)
	}
}

func TestNativeReferenceListValidatesProviderNeutralRelationships(t *testing.T) {
	orgID, repoID, issueID := uuid.New(), uuid.New(), uuid.New()
	requests := 0
	valid := NativeReference{ID: uuid.NewString(), IssueID: issueID.String(), ProviderKey: "aone-bridge",
		RelationKind: "code_change", ExternalRepositoryID: "acme/widgets", ExternalID: "42",
		CanonicalURL: "https://code.example/acme/widgets/changes/42", LifecycleState: "active",
		Visibility: "repository", Metadata: json.RawMessage(`{"head_revision":"abc"}`), RepresentationVersion: 3}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/orgs/"+orgID.String()+"/repos/"+repoID.String()+"/issues/"+issueID.String()+"/references" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer native-token" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"references": []NativeReference{valid}})
	}))
	defer server.Close()
	client, err := NewClientWithOptions(ClientOptions{Host: "issues.example.test", BaseURL: server.URL + "/api/v1",
		Token: "native-token", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	items, err := client.ListNativeReferences(t.Context(), models.RepoScope{OrgID: orgID, RepoID: repoID}, issueID)
	if err != nil || len(items) != 1 || items[0].CanonicalURL != valid.CanonicalURL {
		t.Fatalf("items=%+v error=%v", items, err)
	}
	if requests != 1 {
		t.Fatalf("requests = %d", requests)
	}

	valid.CanonicalURL += "?token=secret"
	if _, err := client.ListNativeReferences(t.Context(), models.RepoScope{OrgID: orgID, RepoID: repoID}, issueID); err == nil ||
		!strings.Contains(err.Error(), "incomplete or invalid") {
		t.Fatalf("unsafe list response error = %v", err)
	}
	if _, err := client.ListNativeReferences(t.Context(), models.RepoScope{}, issueID); err == nil || requests != 2 {
		t.Fatalf("invalid scope error=%v requests=%d", err, requests)
	}
}

func TestNativeReferenceUpsertPreservesGenericRequestShape(t *testing.T) {
	orgID, repoID, issueID, referenceID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request["refresh"] != nil || request["expected_version"] != nil {
			t.Fatalf("generic request gained conditional fields: %s", request)
		}
		_ = json.NewEncoder(w).Encode(NativeReference{ID: referenceID.String(), IssueID: issueID.String(),
			ProviderKey: "build.example", RelationKind: "build", ExternalRepositoryID: "acme/widgets",
			ExternalID: "build-1", CanonicalURL: "https://build.example/acme/widgets/builds/1",
			LifecycleState: "passed", Visibility: "repository", Metadata: json.RawMessage(`{"safe":true}`),
			RepresentationVersion: 1})
	}))
	defer server.Close()
	client, err := NewClientWithOptions(ClientOptions{Host: "issues.example.test", BaseURL: server.URL + "/api/v1",
		Token: "native-token", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.UpsertNativeReference(t.Context(), models.RepoScope{OrgID: orgID, RepoID: repoID}, issueID,
		NativeUpsertReferenceInput{ProviderKey: "build.example", RelationKind: "build",
			ExternalRepositoryID: "acme/widgets", ExternalID: "build-1",
			CanonicalURL: "https://build.example/acme/widgets/builds/1", LifecycleState: "passed",
			Visibility: "repository", Metadata: json.RawMessage(`{"safe":true}`)})
	if err != nil {
		t.Fatal(err)
	}
}

func TestNativeReferenceUpsertDecodesOnlyStructuredCodeChangeConflicts(t *testing.T) {
	orgID, repoID, issueID, referenceID, otherReferenceID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	problemCode := "code_change_conflict"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"type": "https://issue-spec.dev/problems/" + problemCode, "title": "conflict", "status": 409,
			"code": problemCode, "request_id": "request-123", "meta": map[string]any{
				"reason": NativeCodeChangeConflictAmbiguousActiveReferences,
				"references": []NativeReferenceIdentity{{ID: referenceID.String(), ProviderKey: "aone-bridge",
					ExternalRepositoryID: "acme/widgets", ExternalID: "42", RepresentationVersion: 3},
					{ID: otherReferenceID.String(), ProviderKey: "aone-bridge", ExternalRepositoryID: "acme/widgets",
						ExternalID: "41", RepresentationVersion: 1}},
			},
		})
	}))
	defer server.Close()
	client, err := NewClientWithOptions(ClientOptions{Host: "issues.example.test", BaseURL: server.URL + "/api/v1",
		Token: "native-token", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	input := NativeUpsertReferenceInput{ProviderKey: "aone-bridge", RelationKind: "code_change",
		ExternalRepositoryID: "acme/widgets", ExternalID: "43",
		CanonicalURL: "https://code.example/acme/widgets/changes/43", LifecycleState: "active",
		Visibility: "repository", Metadata: json.RawMessage(`{"head_revision":"next"}`)}
	_, err = client.UpsertNativeReference(t.Context(), models.RepoScope{OrgID: orgID, RepoID: repoID}, issueID, input)
	var conflict *NativeCodeChangeConflictError
	var apiErr *APIError
	if !errors.As(err, &conflict) || !errors.As(err, &apiErr) ||
		conflict.Reason != NativeCodeChangeConflictAmbiguousActiveReferences || conflict.RequestID != "request-123" ||
		len(conflict.References) != 2 || conflict.References[0].ID != referenceID.String() || apiErr.StatusCode != 409 {
		t.Fatalf("native conflict=%#v api=%#v error=%v", conflict, apiErr, err)
	}

	problemCode = "conflict"
	_, err = client.UpsertNativeReference(t.Context(), models.RepoScope{OrgID: orgID, RepoID: repoID}, issueID, input)
	conflict = nil
	apiErr = nil
	if errors.As(err, &conflict) || !errors.As(err, &apiErr) || apiErr.StatusCode != 409 {
		t.Fatalf("generic conflict changed type: conflict=%#v api=%#v error=%v", conflict, apiErr, err)
	}
}

func TestNativeReferenceUpsertDecodesRedactedHiddenConflictWithoutIdentities(t *testing.T) {
	orgID, repoID, issueID := uuid.New(), uuid.New(), uuid.New()
	meta := map[string]any{
		"reason": NativeCodeChangeConflictHiddenActiveReferences,
		"action": "contact_maintainer",
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"type": "https://issue-spec.dev/problems/code_change_conflict", "title": "conflict", "status": 409,
			"code": "code_change_conflict", "request_id": "hidden-request-901", "meta": meta,
		})
	}))
	defer server.Close()
	client, err := NewClientWithOptions(ClientOptions{Host: "issues.example.test", BaseURL: server.URL + "/api/v1",
		Token: "native-token", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	input := NativeUpsertReferenceInput{ProviderKey: "request-provider-901", RelationKind: "code_change",
		ExternalRepositoryID: "request/repository-901", ExternalID: "request-change-901",
		CanonicalURL: "https://request.example.test/changes/901", LifecycleState: "active",
		Visibility: "repository", Metadata: json.RawMessage(`{"head_revision":"request-revision-901"}`)}

	_, err = client.UpsertNativeReference(t.Context(), models.RepoScope{OrgID: orgID, RepoID: repoID}, issueID, input)
	var conflict *NativeCodeChangeConflictError
	var apiErr *APIError
	if !errors.As(err, &conflict) || !errors.As(err, &apiErr) ||
		conflict.Reason != NativeCodeChangeConflictHiddenActiveReferences || conflict.Action != "contact_maintainer" ||
		conflict.RequestID != "hidden-request-901" || len(conflict.References) != 0 || apiErr.StatusCode != http.StatusConflict {
		t.Fatalf("hidden native conflict=%#v api=%#v error=%v", conflict, apiErr, err)
	}
	for _, forbidden := range []string{input.ProviderKey, input.ExternalRepositoryID, input.ExternalID,
		input.CanonicalURL, "request-revision-901", "representation_version"} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatalf("hidden native conflict exposed %q: %v", forbidden, err)
		}
	}

	for _, test := range []struct {
		name string
		meta map[string]any
	}{
		{name: "identity is rejected", meta: map[string]any{
			"reason": NativeCodeChangeConflictHiddenActiveReferences,
			"action": "contact_maintainer",
			"references": []NativeReferenceIdentity{{ID: uuid.NewString(), ProviderKey: "hidden-provider",
				ExternalRepositoryID: "hidden/repository", ExternalID: "hidden-change", RepresentationVersion: 1}},
		}},
		{name: "missing action is rejected", meta: map[string]any{
			"reason": NativeCodeChangeConflictHiddenActiveReferences,
		}},
		{name: "wrong action is rejected", meta: map[string]any{
			"reason": NativeCodeChangeConflictHiddenActiveReferences,
			"action": "inspect_references_delete_unwanted_then_retry",
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			meta = test.meta
			_, err := client.UpsertNativeReference(t.Context(), models.RepoScope{OrgID: orgID, RepoID: repoID}, issueID, input)
			conflict = nil
			apiErr = nil
			if errors.As(err, &conflict) || !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusConflict {
				t.Fatalf("malformed hidden conflict changed type: conflict=%#v api=%#v error=%v", conflict, apiErr, err)
			}
		})
	}
}

func TestNativeReferenceUpsertRejectsInvalidInputBeforeRequestAndMismatchedResponse(t *testing.T) {
	requests := 0
	orgID, repoID, issueID := uuid.New(), uuid.New(), uuid.New()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		_ = json.NewEncoder(w).Encode(NativeReference{ID: uuid.NewString(), IssueID: uuid.NewString(),
			ProviderKey: "aone-bridge", RelationKind: "code_change", ExternalRepositoryID: "acme/widgets",
			ExternalID: "42", CanonicalURL: "https://code.example/acme/widgets/changes/42",
			LifecycleState: "active", Visibility: "repository", Metadata: json.RawMessage(`{}`), RepresentationVersion: 1})
	}))
	defer server.Close()
	client, err := NewClientWithOptions(ClientOptions{Host: "issues.example.test", BaseURL: server.URL + "/api/v1",
		Token: "native-token", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	base := NativeUpsertReferenceInput{ProviderKey: "aone-bridge", RelationKind: "code_change",
		ExternalRepositoryID: "acme/widgets", ExternalID: "42",
		CanonicalURL: "https://code.example/acme/widgets/changes/42", LifecycleState: "active",
		Visibility: "repository", Metadata: json.RawMessage(`{"head_revision":"abc"}`)}
	invalid := []NativeUpsertReferenceInput{base, base, base}
	invalid[0].Refresh = true
	zero := int64(0)
	invalid[1].Refresh, invalid[1].ExpectedVersion = true, &zero
	invalid[2].CanonicalURL = "http://code.example/acme/widgets/changes/42"
	for _, input := range invalid {
		if _, err := client.UpsertNativeReference(t.Context(), models.RepoScope{OrgID: orgID, RepoID: repoID}, issueID, input); err == nil ||
			!strings.Contains(err.Error(), "input is invalid") {
			t.Fatalf("invalid input accepted: %+v error=%v", input, err)
		}
	}
	if requests != 0 {
		t.Fatalf("invalid inputs issued %d requests", requests)
	}
	if _, err := client.UpsertNativeReference(t.Context(), models.RepoScope{OrgID: orgID, RepoID: repoID}, issueID, base); err == nil ||
		!strings.Contains(err.Error(), "mismatched") {
		t.Fatalf("mismatched response accepted: %v", err)
	}
}
