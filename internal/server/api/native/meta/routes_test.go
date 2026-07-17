package meta

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/higress-group/issue-spec/internal/server/publicurl"
)

func TestMetaDefaultsEveryFeatureFalse(t *testing.T) {
	metadata, err := NewServerMetadata("issue-spec:test", "http://127.0.0.1:18080", "http://127.0.0.1:18080", nil)
	if err != nil {
		t.Fatal(err)
	}
	set, err := NewRouteSet(Dependencies{Metadata: metadata})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/meta", nil)
	response := httptest.NewRecorder()
	set.Routes[0].Handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	var payload struct {
		APIVersion       string    `json:"api_version"`
		Features         Features  `json:"features"`
		ServerInstanceID string    `json:"server_instance_id"`
		Transport        Transport `json:"transport"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.APIVersion != "v1" || payload.Features != (Features{}) || payload.ServerInstanceID == "" || payload.Transport.Mode != "loopback-http" {
		t.Fatalf("payload = %+v", payload)
	}
}

func TestMetaReportsOnlyInjectedFeatures(t *testing.T) {
	metadata, err := NewServerMetadata("issue-spec:test", "https://api.example.test", "https://web.example.test", nil)
	if err != nil {
		t.Fatal(err)
	}
	set, err := NewRouteSet(Dependencies{Features: Features{Organizations: true, RecoveryExchange: true, Search: true,
		EmailNotifications: true, MentionCandidates: true, RepositoryEmailSubscriptions: true}, Metadata: metadata})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	set.Routes[0].Handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/meta", nil))
	var payload map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	features := payload["features"].(map[string]any)
	if features["organizations"] != true || features["recovery_exchange"] != true || features["search"] != true ||
		features["email_notifications"] != true || features["mention_candidates"] != true ||
		features["repository_email_subscriptions"] != true || features["webhooks"] != false {
		t.Fatalf("features = %#v", features)
	}
}

func TestServerMetadataRejectsPublicHTTPAndKeepsIdentityStable(t *testing.T) {
	if _, err := NewServerMetadata("issue-spec:test", "http://api.example.test", "http://api.example.test", nil); err == nil {
		t.Fatal("public HTTP metadata accepted")
	}
	first, err := NewServerMetadata("issue-spec:database-a", "https://api.example.test", "https://web.example.test", nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewServerMetadata("issue-spec:database-a", "https://api.example.test", "https://other.example.test", nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.ServerInstanceID != second.ServerInstanceID || first.APIURL != "https://api.example.test" || first.NativeAPIURL != "https://api.example.test/api/v1" {
		t.Fatalf("metadata = %+v second=%+v", first, second)
	}
	freshDatabase, err := NewServerMetadata("issue-spec:database-b", "https://api.example.test", "https://web.example.test", nil)
	if err != nil {
		t.Fatal(err)
	}
	if freshDatabase.ServerInstanceID == first.ServerInstanceID {
		t.Fatalf("fresh database reused credential realm: first=%q fresh=%q", first.ServerInstanceID, freshDatabase.ServerInstanceID)
	}
	if _, err := NewServerMetadata("", "https://api.example.test", "https://web.example.test", nil); err == nil {
		t.Fatal("empty database identity accepted")
	}
}

func TestServerMetadataReportsExplicitTrustedInternalPosture(t *testing.T) {
	metadata, err := NewServerMetadataWithPosture("issue-spec:test", "http://10.0.0.8:8080", "http://issues.internal:8080", nil, publicurl.TransportTrustedInternalHTTP)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.TransportPosture != publicurl.TransportTrustedInternalHTTP || metadata.Transport.Secure || metadata.Transport.Mode != "trusted-internal-http" {
		t.Fatalf("metadata = %+v", metadata)
	}
	if metadata.APIURL != "http://10.0.0.8:8080" || metadata.NativeAPIURL != "http://10.0.0.8:8080/api/v1" {
		t.Fatalf("metadata endpoints = %+v", metadata)
	}
}
