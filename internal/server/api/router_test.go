package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSecurityHeadersAndCredentialedCORS(t *testing.T) {
	base := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusAccepted) })
	handler := securityHeaders(credentialedCORS("https://api.example.test", "https://web.example.test", base))

	request := httptest.NewRequest(http.MethodGet, "https://api.example.test/api/v1/meta", nil)
	request.Header.Set("Origin", "https://web.example.test")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || response.Header().Get("Access-Control-Allow-Origin") != "https://web.example.test" ||
		response.Header().Get("Access-Control-Allow-Credentials") != "true" || !strings.Contains(response.Header().Get("Vary"), "Origin") {
		t.Fatalf("credentialed CORS = %d headers=%v", response.Code, response.Header())
	}
	if csp := response.Header().Get("Content-Security-Policy"); strings.Contains(csp, "unsafe-inline") || strings.Contains(csp, "unsafe-eval") || !strings.Contains(csp, "frame-ancestors 'none'") {
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
