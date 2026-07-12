package routeset

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func route(name, method, pattern, body string) Route {
	return Route{Name: name, Method: method, Pattern: pattern, Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	})}
}

func TestComposeIsDeterministicAndMountsMethodRoutes(t *testing.T) {
	sets := []RouteSet{
		{Name: "issues", Routes: []Route{route("issues.create", http.MethodPost, "/repos/{owner}/{repo}/issues", "created")}},
		{Name: "identity", Routes: []Route{route("user.get", http.MethodGet, "/user", "user")}},
	}
	routes, err := Compose(sets...)
	if err != nil {
		t.Fatal(err)
	}
	if got := routes[0].Name; got != "issues.create" {
		t.Fatalf("first route = %q", got)
	}
	mux, err := NewMux(SelfHostedPolicy(), sets...)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/user", nil)
	res := httptest.NewRecorder()
	mux.ServeHTTP(res, req)
	if got := strings.TrimSpace(res.Body.String()); got != "user" {
		t.Fatalf("body = %q", got)
	}
}

func TestComposeReportsCollisionsDeterministically(t *testing.T) {
	sets := []RouteSet{
		{Name: "z", Routes: []Route{route("z.get", http.MethodGet, "/items/{id}", "z")}},
		{Name: "a", Routes: []Route{route("a.get", http.MethodGet, "/items/{id}", "a")}},
	}
	_, first := Compose(sets...)
	_, second := Compose(sets[1], sets[0])
	if first == nil || second == nil || first.Error() != second.Error() {
		t.Fatalf("non-deterministic errors: %v / %v", first, second)
	}
	if !strings.Contains(first.Error(), `route collision "GET /items/{id}" between "a.get" and "z.get"`) {
		t.Fatalf("unexpected error: %v", first)
	}
}

func TestComposeDetectsEquivalentWildcardConflicts(t *testing.T) {
	_, err := Compose(RouteSet{Name: "conflict", Routes: []Route{
		route("one", http.MethodGet, "/repos/{owner}", "one"),
		route("two", http.MethodGet, "/repos/{name}", "two"),
	}})
	if err == nil || !strings.Contains(err.Error(), "incompatible ServeMux patterns") {
		t.Fatalf("error = %v", err)
	}
}

func TestSelfHostedPolicyProvesNotificationsAbsent(t *testing.T) {
	_, err := ComposeWithPolicy(SelfHostedPolicy(), RouteSet{Name: "runner", Routes: []Route{
		route("notifications.list", http.MethodGet, "/notifications", "bad"),
	}})
	if err == nil || !strings.Contains(err.Error(), `forbidden path prefix "/notifications"`) {
		t.Fatalf("error = %v", err)
	}
	_, err = ComposeWithPolicy(SelfHostedPolicy(), RouteSet{Name: "runner", Routes: []Route{
		route("notifications.detail", http.MethodGet, "/notifications/{id}", "bad"),
	}})
	if err == nil || !strings.Contains(err.Error(), `forbidden path prefix "/notifications"`) {
		t.Fatalf("descendant error = %v", err)
	}
}
