package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
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
}

func testSecret(t *testing.T, label string) string {
	t.Helper()
	name := filepath.Join(t.TempDir(), label)
	if err := os.WriteFile(name, []byte(strings.Repeat(label+"-", 8)), 0o600); err != nil {
		t.Fatal(err)
	}
	return name
}
