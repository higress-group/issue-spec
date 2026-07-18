package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	adminapi "github.com/higress-group/issue-spec/internal/server/api/native/admin"
	metaapi "github.com/higress-group/issue-spec/internal/server/api/native/meta"
	profilemailservice "github.com/higress-group/issue-spec/internal/server/profilemail"
	reponotificationservice "github.com/higress-group/issue-spec/internal/server/reponotifications"
	"github.com/higress-group/issue-spec/internal/server/store"
)

func TestMentionCandidateCapabilityAndRoutesDoNotRequireEmail(t *testing.T) {
	deps := Dependencies{WebOrigin: "https://web.example.test", MentionDirectory: routerMentionDirectory{},
		ProfileMail:                  (*profilemailservice.Service)(nil),
		RepositoryEmailSubscriptions: (*reponotificationservice.SubscriptionService)(nil)}
	features := configuredFeatures(deps)
	if features.EmailNotifications || !features.MentionCandidates || features.RepositoryEmailSubscriptions {
		t.Fatalf("SMTP-disabled features = %+v", features)
	}
	authenticate := adminapi.Authenticate(func(next http.Handler) http.Handler { return next })
	set, enabled, err := composeMentionCandidateRoutes(deps, features, authenticate)
	if err != nil || !enabled || set.Name != "native-mentions" || len(set.Routes) != 1 ||
		set.Routes[0].Pattern != "/api/v1/mentions/candidates" {
		t.Fatalf("mention route composition = %+v enabled=%v err=%v", set, enabled, err)
	}

	metadata, err := metaapi.NewServerMetadata("issue-spec:router-test", "https://api.example.test",
		"https://web.example.test", nil)
	if err != nil {
		t.Fatal(err)
	}
	metaSet, err := metaapi.NewRouteSet(metaapi.Dependencies{Features: features, Metadata: metadata})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	metaSet.Routes[0].Handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/meta", nil))
	var payload struct {
		Features metaapi.Features `json:"features"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Features.EmailNotifications || !payload.Features.MentionCandidates ||
		payload.Features.RepositoryEmailSubscriptions {
		t.Fatalf("SMTP-disabled meta features = %+v", payload.Features)
	}

	deps.EmailNotifications = true
	features = configuredFeatures(deps)
	if !features.EmailNotifications || !features.MentionCandidates || !features.RepositoryEmailSubscriptions {
		t.Fatalf("SMTP-enabled features = %+v", features)
	}
	deps.MentionDirectory = nil
	features = configuredFeatures(deps)
	if features.MentionCandidates || !features.EmailNotifications || !features.RepositoryEmailSubscriptions {
		t.Fatalf("directory-disabled features = %+v", features)
	}
	if _, enabled, err := composeMentionCandidateRoutes(deps, features, authenticate); err != nil || enabled {
		t.Fatalf("directory-disabled route enabled=%v err=%v", enabled, err)
	}
}

type routerMentionDirectory struct{}

func (routerMentionDirectory) MentionCandidates(context.Context, uuid.UUID, string, int) ([]store.MentionCandidate, error) {
	return []store.MentionCandidate{}, nil
}

func TestSecurityHeadersAndCredentialedCORS(t *testing.T) {
	base := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusAccepted) })
	handler := securityHeaders("https://api.example.test", "https://web.example.test", credentialedCORS("https://api.example.test", "https://web.example.test", base))

	request := httptest.NewRequest(http.MethodGet, "https://api.example.test/api/v1/meta", nil)
	request.Header.Set("Origin", "https://web.example.test")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || response.Header().Get("Access-Control-Allow-Origin") != "https://web.example.test" ||
		response.Header().Get("Access-Control-Allow-Credentials") != "true" || !strings.Contains(response.Header().Get("Vary"), "Origin") {
		t.Fatalf("credentialed CORS = %d headers=%v", response.Code, response.Header())
	}
	if csp := response.Header().Get("Content-Security-Policy"); strings.Contains(csp, "unsafe-inline") || strings.Contains(csp, "unsafe-eval") || !strings.Contains(csp, "frame-ancestors 'none'") || !strings.Contains(csp, "connect-src 'self' https://api.example.test") {
		t.Fatalf("CSP = %q", csp)
	}

	preflight := httptest.NewRequest(http.MethodOptions, "https://api.example.test/api/v1/pats", nil)
	preflight.Header.Set("Origin", "https://web.example.test")
	preflight.Header.Set("Access-Control-Request-Method", http.MethodPost)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, preflight)
	if response.Code != http.StatusNoContent || !strings.Contains(response.Header().Get("Access-Control-Allow-Headers"), "X-CSRF-Token") {
		t.Fatalf("preflight = %d headers=%v", response.Code, response.Header())
	}
}

func TestCORSRejectsOtherOriginsButAllowsSameOriginAndNoOrigin(t *testing.T) {
	base := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusAccepted) })
	handler := credentialedCORS("https://api.example.test", "https://web.example.test", base)
	for _, origin := range []string{"", "https://api.example.test"} {
		request := httptest.NewRequest(http.MethodPost, "https://api.example.test/api/v1/pats", nil)
		if origin != "" {
			request.Header.Set("Origin", origin)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusAccepted {
			t.Fatalf("origin %q status = %d", origin, response.Code)
		}
	}
	request := httptest.NewRequest(http.MethodGet, "https://api.example.test/api/v1/meta", nil)
	request.Header.Set("Origin", "https://evil.example")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || !strings.Contains(response.Header().Get("Vary"), "Origin") {
		t.Fatalf("evil origin = %d headers=%v", response.Code, response.Header())
	}
}

func TestRequestObserverPreservesResponseControllerCapabilities(t *testing.T) {
	handler := observeRequests(&metrics{}, nil, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Errorf("Flush through observer: %v", err)
		}
	}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if !response.Flushed {
		t.Fatal("underlying flusher was not reached through Unwrap")
	}
}

func TestMountedFeaturesAdvertiseRequirementsOnboarding(t *testing.T) {
	withoutSearch := mountedFeatures(false)
	if !withoutSearch.RequirementsOnboarding || withoutSearch.Search {
		t.Fatalf("features without search=%+v", withoutSearch)
	}
	withSearch := mountedFeatures(true)
	if !withSearch.RequirementsOnboarding || !withSearch.Search {
		t.Fatalf("features with search=%+v", withSearch)
	}
}
