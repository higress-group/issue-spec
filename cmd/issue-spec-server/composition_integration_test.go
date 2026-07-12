package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/codereview"
	"github.com/higress-group/issue-spec/internal/server/config"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestComposeMountsAllRealRouteSets(t *testing.T) {
	databaseURL := strings.TrimSpace(os.Getenv("TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL for server composition integration test")
	}
	adminPool, err := pgxpool.New(t.Context(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer adminPool.Close()
	schema := "composition_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := adminPool.Exec(t.Context(), "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = adminPool.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+quoted+" CASCADE") })
	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	poolConfig.ConnConfig.RuntimeParams["search_path"] = schema

	t.Setenv(config.EnvironmentEnv, string(config.EnvironmentTest))
	// compose does not bind; use a valid non-zero port because production
	// configuration correctly rejects ephemeral listener ports.
	t.Setenv(config.ListenAddrEnv, "127.0.0.1:18080")
	t.Setenv(config.DatabaseURLEnv, poolConfig.ConnString())
	t.Setenv(config.APIPublicURLEnv, "http://127.0.0.1:8080")
	t.Setenv(config.WebPublicURLEnv, "http://127.0.0.1:8080")
	t.Setenv(config.BootstrapSecretFileEnv, testSecret(t, "bootstrap"))
	t.Setenv(config.TokenPepperFileEnv, testSecret(t, "pepper"))
	t.Setenv(config.EncryptionKeyFileEnv, testSecret(t, "encryption"))
	providerFile := filepath.Join(t.TempDir(), "providers.json")
	if err := os.WriteFile(providerFile, []byte(`{"version":1,"providers":{"code.example":{"path":"/bin/true","description":{"display_name":"Example Code","remote_authorities":["code.example"],"code_change_label":"Merge request","capabilities":["change.create"],"recommended_evidence":["change"]}}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(codereview.OperatorProvidersFileEnv, providerFile)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	app, err := compose(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer app.database.Close()

	for _, test := range []struct {
		method string
		path   string
		status int
	}{
		{http.MethodGet, "/api/v1/meta", http.StatusOK},
		{http.MethodGet, "/api/v1/not-a-route", http.StatusNotFound},
		{http.MethodHead, "/", http.StatusOK},
	} {
		response := httptest.NewRecorder()
		app.handler.ServeHTTP(response, httptest.NewRequest(test.method, test.path, nil))
		if response.Code != test.status {
			t.Fatalf("%s %s = %d, want %d; body=%q", test.method, test.path, response.Code, test.status, response.Body.String())
		}
	}
	metaResponse := httptest.NewRecorder()
	app.handler.ServeHTTP(metaResponse, httptest.NewRequest(http.MethodGet, "/api/v1/meta", nil))
	var meta struct {
		ServerInstanceID string `json:"server_instance_id"`
		APIURL           string `json:"api_url"`
		NativeAPIURL     string `json:"native_api_url"`
		WebURL           string `json:"web_url"`
		Transport        struct {
			Mode   string `json:"mode"`
			Secure bool   `json:"secure"`
		} `json:"transport"`
		Providers []struct {
			ProviderKey string `json:"provider_key"`
			DisplayName string `json:"display_name"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(metaResponse.Body.Bytes(), &meta); err != nil {
		t.Fatal(err)
	}
	if meta.ServerInstanceID == "" || meta.APIURL != "http://127.0.0.1:8080/api/v3" ||
		meta.NativeAPIURL != "http://127.0.0.1:8080/api/v1" || meta.WebURL != "http://127.0.0.1:8080" ||
		meta.Transport.Mode != "loopback-http" || meta.Transport.Secure || len(meta.Providers) != 1 ||
		meta.Providers[0].ProviderKey != "code.example" || meta.Providers[0].DisplayName != "Example Code" {
		t.Fatalf("meta composition = %+v", meta)
	}
}

func testSecret(t *testing.T, label string) string {
	t.Helper()
	name := filepath.Join(t.TempDir(), label)
	if err := os.WriteFile(name, []byte(strings.Repeat(label+"-", 8)), 0o600); err != nil {
		t.Fatal(err)
	}
	return name
}
