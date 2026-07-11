package webhooks

import (
	"encoding/json"
	"net/http"
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
	if len(set.Routes) != 6 {
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
