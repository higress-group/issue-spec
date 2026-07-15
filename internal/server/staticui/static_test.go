package staticui

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProductionAssetsContainRunnerScopePreset(t *testing.T) {
	const runnerScopes = "read:user, issues:read, issues:write, evidence:write"
	found := false
	if err := fs.WalkDir(production, "dist/assets", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(name, ".js") {
			return err
		}
		content, err := production.ReadFile(name)
		if err != nil {
			return err
		}
		found = found || strings.Contains(string(content), runnerScopes)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatalf("generated production assets do not contain runner scope preset %q", runnerScopes)
	}
}

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
		if !strings.Contains(response.Body.String(), `<link rel="icon" type="image/svg+xml" href="/favicon.svg" />`) {
			t.Fatalf("GET %s does not reference the favicon", target)
		}
		if got := response.Header().Get("Cache-Control"); got != "no-store" {
			t.Fatalf("GET %s cache = %q", target, got)
		}
	}
	favicon := httptest.NewRecorder()
	handler.ServeHTTP(favicon, httptest.NewRequest(http.MethodGet, "/favicon.svg", nil))
	if favicon.Code != http.StatusOK || favicon.Header().Get("Content-Type") != "image/svg+xml" ||
		favicon.Header().Get("Cache-Control") != "no-cache" || !strings.Contains(favicon.Body.String(), ">is</text>") {
		t.Fatalf("favicon response = %d headers=%v body=%q", favicon.Code, favicon.Header(), favicon.Body.String())
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
