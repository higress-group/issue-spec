package staticui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProductionAssetsAndSPAFallback(t *testing.T) {
	handler, err := New(Options{Production: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"/", "/acme/widgets/issues/17"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `<div id="root"></div>`) {
			t.Fatalf("GET %s = %d %q", target, response.Code, response.Body.String())
		}
		if got := response.Header().Get("Cache-Control"); got != "no-store" {
			t.Fatalf("GET %s cache = %q", target, got)
		}
	}
	var hashed string
	for name, asset := range manifest {
		if asset.Immutable {
			hashed = name
			break
		}
	}
	if hashed == "" {
		t.Fatal("generated manifest contains no immutable Vite asset")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/"+hashed, nil))
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" ||
		response.Header().Get("ETag") == "" {
		t.Fatalf("hashed asset response = %d headers=%v", response.Code, response.Header())
	}
}

func TestAPINeverFallsBackToSPA(t *testing.T) {
	handler, err := New(Options{Production: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"/api/v1/unknown", "/repos/acme/widgets/pulls", "/user", "/notifications"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
		if response.Code != http.StatusNotFound || strings.Contains(response.Body.String(), `<div id="root"></div>`) {
			t.Fatalf("GET %s = %d %q", target, response.Code, response.Body.String())
		}
	}
}

func TestProductionRejectsExternalAssetDirectory(t *testing.T) {
	if _, err := New(Options{Production: true, DevelopmentDirectory: t.TempDir()}); err == nil {
		t.Fatal("production accepted external static directory")
	}
}
