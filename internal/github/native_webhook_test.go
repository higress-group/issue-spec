package github

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

func TestVerifyNativeRunnerSubscriptionUsesExactSubscriptionCredential(t *testing.T) {
	organizationID, subscriptionID := uuid.New(), uuid.New()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/orgs/"+organizationID.String()+
			"/webhooks/"+subscriptionID.String()+"/runner-verification" {
			t.Fatalf("method=%s path=%q", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer subscription-secret" {
			t.Fatalf("authorization=%q", r.Header.Get("Authorization"))
		}
		_ = json.NewEncoder(w).Encode(NativeWebhookSubscription{ID: subscriptionID,
			OrganizationID: organizationID, ScopeType: "organization", Active: true,
			EventTypes:     []string{"issue_comment.created", "issue_comment.edited"},
			DeliveryFormat: "issue-spec.v1", SigningMode: "bearer"})
	}))
	defer server.Close()
	client, err := NewClientWithOptions(ClientOptions{Host: "issues.test", BaseURL: server.URL + "/api/v1",
		Token: "subscription-secret", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.VerifyNativeRunnerSubscription(t.Context(), organizationID, subscriptionID)
	if err != nil || result.ID != subscriptionID || result.RepositoryID != nil || !result.Active {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestVerifyNativeRunnerSubscriptionRejectsIncompleteResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"active":true}`))
	}))
	defer server.Close()
	client, _ := NewClientWithOptions(ClientOptions{Host: "issues.test", BaseURL: server.URL,
		Token: "subscription-secret", HTTPClient: server.Client()})
	if _, err := client.VerifyNativeRunnerSubscription(t.Context(), uuid.New(), uuid.New()); err == nil {
		t.Fatal("incomplete response accepted")
	}
}
