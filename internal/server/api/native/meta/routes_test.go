package meta

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMetaDefaultsEveryFeatureFalse(t *testing.T) {
	set, err := NewRouteSet(Dependencies{})
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
		APIVersion string   `json:"api_version"`
		Features   Features `json:"features"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.APIVersion != "v1" || payload.Features != (Features{}) {
		t.Fatalf("payload = %+v", payload)
	}
}

func TestMetaReportsOnlyInjectedFeatures(t *testing.T) {
	set, err := NewRouteSet(Dependencies{Features: Features{Organizations: true, RecoveryExchange: true}})
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
	if features["organizations"] != true || features["recovery_exchange"] != true || features["webhooks"] != false {
		t.Fatalf("features = %#v", features)
	}
}
