package webhooks

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/higress-group/issue-spec/internal/server/events/subscriptions"
)

func TestRouteSetIsExplicitAndFailClosed(t *testing.T) {
	if _, err := NewRouteSet(Dependencies{}); err == nil {
		t.Fatal("missing dependencies were accepted")
	}
	set, err := NewRouteSet(Dependencies{Service: &subscriptions.Service{},
		Authenticate: func(next http.Handler) http.Handler { return next }})
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Routes) != 7 {
		t.Fatalf("routes = %d", len(set.Routes))
	}
	seen := map[string]bool{}
	for _, route := range set.Routes {
		key := route.Method + " " + route.Pattern
		if seen[key] {
			t.Fatalf("duplicate route %s", key)
		}
		seen[key] = true
	}
	for _, key := range []string{
		"GET /api/v1/orgs/{org}/webhooks",
		"POST /api/v1/orgs/{org}/webhooks",
		"GET /api/v1/orgs/{org}/webhooks/{webhook}",
		"PATCH /api/v1/orgs/{org}/webhooks/{webhook}",
		"DELETE /api/v1/orgs/{org}/webhooks/{webhook}",
		"POST /api/v1/orgs/{org}/webhooks/{webhook}/rotate-secret",
		"GET /api/v1/orgs/{org}/webhooks/{webhook}/suppressions",
	} {
		if !seen[key] {
			t.Fatalf("missing route %s", key)
		}
	}
}

func TestReadViewsNeverExposeSecretMaterial(t *testing.T) {
	item := subscriptions.Subscription{URL: "https://runner.example.test/hook"}
	read, _ := json.Marshal(subscriptionView(item))
	if strings.Contains(string(read), "secret") {
		t.Fatalf("read view contains secret field: %s", read)
	}
	created, _ := json.Marshal(secretView(subscriptions.SecretResult{Subscription: item,
		Secret: "show-once", SecretVersion: 1}))
	if !strings.Contains(string(created), `"secret":"show-once"`) {
		t.Fatalf("create view omitted show-once secret: %s", created)
	}
}

func TestValidationProblemsExposeOnlyStableSafeMetadata(t *testing.T) {
	tests := []struct {
		reason subscriptions.ValidationReason
		field  subscriptions.ValidationField
	}{
		{subscriptions.ValidationInvalidDestinationURL, subscriptions.ValidationFieldURL},
		{subscriptions.ValidationDestinationDenied, subscriptions.ValidationFieldURL},
		{subscriptions.ValidationInvalidEventType, subscriptions.ValidationFieldEventTypes},
		{subscriptions.ValidationInvalidDeliveryPolicy, subscriptions.ValidationFieldContentPolicy},
		{subscriptions.ValidationInvalidRetryPolicy, subscriptions.ValidationFieldRetryMaxBackoff},
		{subscriptions.ValidationInvalidDestinationQuery, subscriptions.ValidationFieldClearDestinationQuery},
	}
	for _, test := range tests {
		t.Run(string(test.reason), func(t *testing.T) {
			response := httptest.NewRecorder()
			response.Header().Set("X-Request-ID", "request-222")
			privateCause := "access_token=top-secret resolved=10.0.0.7 destination=https://private.example/hook?token=top-secret"
			writeError(response, fmt.Errorf("%s: %w", privateCause,
				&subscriptions.ValidationError{Reason: test.reason, Field: test.field}))

			if response.Code != http.StatusUnprocessableEntity ||
				response.Header().Get("Content-Type") != "application/problem+json" {
				t.Fatalf("status=%d content-type=%q body=%s", response.Code,
					response.Header().Get("Content-Type"), response.Body.String())
			}
			var problem struct {
				Type      string         `json:"type"`
				Status    int            `json:"status"`
				Code      string         `json:"code"`
				RequestID string         `json:"request_id"`
				Meta      map[string]any `json:"meta"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil {
				t.Fatal(err)
			}
			if problem.Type != "https://issue-spec.dev/problems/"+string(test.reason) ||
				problem.Status != http.StatusUnprocessableEntity || problem.Code != string(test.reason) ||
				problem.RequestID != "request-222" ||
				problem.Meta["field"] != string(test.field) {
				t.Fatalf("problem=%+v", problem)
			}
			for _, forbidden := range []string{"top-secret", "access_token", "10.0.0.7", "private.example"} {
				if strings.Contains(response.Body.String(), forbidden) {
					t.Fatalf("response leaked %q: %s", forbidden, response.Body.String())
				}
			}
		})
	}
}

func TestParseRetryIdentifiesTheInvalidField(t *testing.T) {
	for _, test := range []struct {
		name    string
		request retryRequest
		field   subscriptions.ValidationField
	}{
		{"attempts", retryRequest{MaxAttempts: pointer(0)}, subscriptions.ValidationFieldRetryMaxAttempts},
		{"initial", retryRequest{InitialBackoff: pointer("not-a-duration")}, subscriptions.ValidationFieldRetryInitialBackoff},
		{"zero initial", retryRequest{InitialBackoff: pointer("0s")}, subscriptions.ValidationFieldRetryInitialBackoff},
		{"maximum", retryRequest{MaxBackoff: pointer("not-a-duration")}, subscriptions.ValidationFieldRetryMaxBackoff},
		{"zero maximum", retryRequest{MaxBackoff: pointer("0s")}, subscriptions.ValidationFieldRetryMaxBackoff},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseRetry(test.request)
			var validation *subscriptions.ValidationError
			if !errors.As(err, &validation) || validation.Reason != subscriptions.ValidationInvalidRetryPolicy ||
				validation.Field != test.field {
				t.Fatalf("error=%v validation=%+v", err, validation)
			}
		})
	}
}

func pointer[T any](value T) *T { return &value }
