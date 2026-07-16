package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadDefaultsAndExplicitValues(t *testing.T) {
	resetEnv(t)
	t.Setenv(ListenAddrEnv, "127.0.0.1:8080")
	t.Setenv(DatabaseURLEnv, "postgres://user:password@db/issue_spec")
	t.Setenv(TrustedProxiesEnv, "10.0.0.9/8, 2001:db8::1/32,10.0.0.0/8")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Environment != EnvironmentDevelopment || cfg.MigrationsMode != MigrationsAuto || cfg.SearchMode != SearchDisabled {
		t.Fatalf("defaults = environment %q, migrations %q, search %q", cfg.Environment, cfg.MigrationsMode, cfg.SearchMode)
	}
	if cfg.GracefulShutdownTimeout != 30*time.Second || cfg.HealthReadTimeout != 5*time.Second || cfg.HealthWriteTimeout != 5*time.Second {
		t.Fatalf("duration defaults = %s, %s, %s", cfg.GracefulShutdownTimeout, cfg.HealthReadTimeout, cfg.HealthWriteTimeout)
	}
	if got := fmt.Sprint(cfg.TrustedProxies); got != "[10.0.0.0/8 2001:db8::/32]" {
		t.Fatalf("TrustedProxies = %s", got)
	}
}

func TestLoadValidation(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantErr string
	}{
		{name: "listen required", env: map[string]string{DatabaseURLEnv: "postgres://db/app"}, wantErr: ListenAddrEnv + " is required"},
		{name: "listen format", env: map[string]string{ListenAddrEnv: "8080", DatabaseURLEnv: "postgres://db/app"}, wantErr: "host:port"},
		{name: "database required", env: map[string]string{ListenAddrEnv: ":8080"}, wantErr: DatabaseURLEnv + " is required"},
		{name: "environment", env: baseEnv(EnvironmentEnv, "staging"), wantErr: "development, test, or production"},
		{name: "migration mode", env: baseEnv(MigrationsModeEnv, "maybe"), wantErr: "auto, validate, or off"},
		{name: "search mode", env: baseEnv(SearchModeEnv, "fallback"), wantErr: "disabled or postgres"},
		{name: "graceful duration", env: baseEnv(GracefulShutdownTimeoutEnv, "0s"), wantErr: "positive duration"},
		{name: "read duration", env: baseEnv(HealthReadTimeoutEnv, "later"), wantErr: "positive duration"},
		{name: "write duration", env: baseEnv(HealthWriteTimeoutEnv, "-1s"), wantErr: "positive duration"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetEnv(t)
			for name, value := range tt.env {
				t.Setenv(name, value)
			}
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Load error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestPublicURLValidation(t *testing.T) {
	tests := []struct {
		name  string
		value string
		ok    bool
	}{
		{name: "https", value: "https://api.example.test", ok: true},
		{name: "trailing slash", value: "https://api.example.test/", ok: true},
		{name: "http", value: "http://localhost:8080", ok: true},
		{name: "path", value: "https://api.example.test/base"},
		{name: "relative", value: "/api"},
		{name: "wrong scheme", value: "ftp://example.test"},
		{name: "userinfo", value: "https://user:pass@example.test"},
		{name: "query", value: "https://example.test?q=secret"},
		{name: "fragment", value: "https://example.test/#fragment"},
		{name: "missing host", value: "https:///api"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetEnv(t)
			for name, value := range baseEnv(APIPublicURLEnv, tt.value) {
				t.Setenv(name, value)
			}
			_, err := Load()
			if tt.ok && err != nil {
				t.Fatalf("Load error = %v", err)
			}
			if !tt.ok && err == nil {
				t.Fatal("Load succeeded, want URL validation error")
			}
		})
	}
}

func TestTrustedProxyCIDRs(t *testing.T) {
	for _, value := range []string{"10.0.0.1", "10.0.0.0/99", "10.0.0.0/8,,192.0.2.0/24"} {
		t.Run(value, func(t *testing.T) {
			resetEnv(t)
			for name, setting := range baseEnv(TrustedProxiesEnv, value) {
				t.Setenv(name, setting)
			}
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), "invalid CIDR") {
				t.Fatalf("Load error = %v", err)
			}
		})
	}
}

func TestSecretFilesAreSecureLoadedAndNotSerialized(t *testing.T) {
	resetEnv(t)
	for name, value := range baseEnv("", "") {
		if name != "" {
			t.Setenv(name, value)
		}
	}
	secret := "bootstrap-value-that-must-not-leak"
	path := writeSecret(t, secret+"\n", 0o600)
	t.Setenv(BootstrapSecretFileEnv, path)

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	got := cfg.BootstrapSecret.Bytes()
	if string(got) != secret {
		t.Fatalf("secret = %q", got)
	}
	got[0] = 'X'
	if string(cfg.BootstrapSecret.Bytes()) != secret {
		t.Fatal("Bytes exposed mutable configuration storage")
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, rendered := range []string{string(b), cfg.String(), fmt.Sprintf("%v", cfg.BootstrapSecret)} {
		if strings.Contains(rendered, secret) {
			t.Fatalf("serialized secret leaked in %q", rendered)
		}
	}
	if !strings.Contains(string(b), path) {
		t.Fatalf("secure file reference missing from JSON: %s", b)
	}
}

func TestSecretFileValidation(t *testing.T) {
	tests := []struct {
		name    string
		path    func(*testing.T) string
		wantErr string
	}{
		{name: "absolute", path: func(*testing.T) string { return "relative/secret" }, wantErr: "absolute path"},
		{name: "permissions", path: func(t *testing.T) string { return writeSecret(t, "secret", 0o644) }, wantErr: "permissions"},
		{name: "empty", path: func(t *testing.T) string { return writeSecret(t, "\n", 0o600) }, wantErr: "non-empty"},
		{name: "directory", path: func(t *testing.T) string { return t.TempDir() }, wantErr: "regular file"},
		{name: "symlink", path: func(t *testing.T) string {
			target := writeSecret(t, "secret", 0o600)
			link := filepath.Join(t.TempDir(), "secret-link")
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			return link
		}, wantErr: "symbolic link"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetEnv(t)
			for name, value := range baseEnv(BootstrapSecretFileEnv, tt.path(t)) {
				t.Setenv(name, value)
			}
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Load error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestProductionRequiresPublicURLsAndSecrets(t *testing.T) {
	resetEnv(t)
	for name, value := range baseEnv(EnvironmentEnv, string(EnvironmentProduction)) {
		t.Setenv(name, value)
	}
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), APIPublicURLEnv) {
		t.Fatalf("Load error = %v, want missing API public URL", err)
	}

	t.Setenv(APIPublicURLEnv, "https://api.example.test")
	t.Setenv(WebPublicURLEnv, "https://example.test")
	t.Setenv(BootstrapSecretFileEnv, writeSecret(t, "bootstrap", 0o600))
	t.Setenv(TokenPepperFileEnv, writeSecret(t, "pepper", 0o600))
	t.Setenv(EncryptionKeyFileEnv, writeSecret(t, "encryption", 0o600))
	if _, err := Load(); err != nil {
		t.Fatalf("complete production Load error = %v", err)
	}
}

func TestProductionRejectsPlainHTTPPublicURLs(t *testing.T) {
	resetEnv(t)
	for name, value := range baseEnv(EnvironmentEnv, string(EnvironmentProduction)) {
		t.Setenv(name, value)
	}
	t.Setenv(APIPublicURLEnv, "http://api.example.test")
	t.Setenv(WebPublicURLEnv, "https://example.test")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "must use https in production") {
		t.Fatalf("Load error = %v, want production HTTPS validation", err)
	}
}

func TestProductionTrustedInternalHTTPRequiresExplicitCoherentPosture(t *testing.T) {
	resetEnv(t)
	for name, value := range baseEnv(EnvironmentEnv, string(EnvironmentProduction)) {
		t.Setenv(name, value)
	}
	t.Setenv(APIPublicURLEnv, "http://10.0.0.8:8080")
	t.Setenv(WebPublicURLEnv, "http://issues.internal:8080")
	t.Setenv(BootstrapSecretFileEnv, writeSecret(t, "bootstrap", 0o600))
	t.Setenv(TokenPepperFileEnv, writeSecret(t, "pepper", 0o600))
	t.Setenv(EncryptionKeyFileEnv, writeSecret(t, "encryption", 0o600))
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "must use https in production") {
		t.Fatalf("default HTTP error = %v", err)
	}
	t.Setenv(TransportPostureEnv, string(TransportTrustedInternalHTTP))
	cfg, err := Load()
	if err != nil || cfg.TransportPosture != TransportTrustedInternalHTTP {
		t.Fatalf("trusted HTTP cfg=%+v err=%v", cfg, err)
	}
	t.Setenv(WebPublicURLEnv, "https://issues.internal")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "must use http in production") {
		t.Fatalf("mixed scheme error = %v", err)
	}
	t.Setenv(TransportPostureEnv, "auto")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), TransportPostureEnv) {
		t.Fatalf("malformed posture error = %v", err)
	}
}

func TestRedactError(t *testing.T) {
	resetEnv(t)
	databaseURL := "postgres://user:password@db/issue_spec"
	secret := "super-secret-file-value"
	t.Setenv(DatabaseURLEnv, databaseURL)
	t.Setenv(BootstrapSecretFileEnv, writeSecret(t, secret+"\n", 0o600))

	err := RedactError(fmt.Errorf("connect %s with %s: %w", databaseURL, secret, errors.New("failed")))
	if err == nil {
		t.Fatal("RedactError returned nil")
	}
	if strings.Contains(err.Error(), databaseURL) || strings.Contains(err.Error(), secret) {
		t.Fatalf("RedactError leaked sensitive value: %s", err)
	}
	if count := strings.Count(err.Error(), "[REDACTED]"); count < 2 {
		t.Fatalf("RedactError = %q, want two redactions", err)
	}
	if RedactError(nil) != nil {
		t.Fatal("RedactError(nil) must be nil")
	}
}

func TestLoadedConfigRedactsAfterSecretFileRemoval(t *testing.T) {
	resetEnv(t)
	for name, value := range baseEnv("", "") {
		if name != "" {
			t.Setenv(name, value)
		}
	}
	databaseURL := "postgres://user:password@db/issue_spec"
	secret := "loaded-secret-value"
	path := writeSecret(t, secret, 0o600)
	t.Setenv(DatabaseURLEnv, databaseURL)
	t.Setenv(TokenPepperFileEnv, path)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	redacted := cfg.RedactError(fmt.Errorf("connect %s using %s", databaseURL, secret))
	if strings.Contains(redacted.Error(), databaseURL) || strings.Contains(redacted.Error(), secret) {
		t.Fatalf("loaded config leaked sensitive material: %v", redacted)
	}
	for _, rendered := range []string{fmt.Sprintf("%+v", cfg), fmt.Sprintf("%#v", cfg)} {
		if strings.Contains(rendered, databaseURL) || strings.Contains(rendered, secret) {
			t.Fatalf("formatted config leaked sensitive material: %s", rendered)
		}
	}
}

func baseEnv(extraName, extraValue string) map[string]string {
	values := map[string]string{
		ListenAddrEnv:  ":8080",
		DatabaseURLEnv: "postgres://db/issue_spec",
	}
	if extraName != "" {
		values[extraName] = extraValue
	}
	return values
}

func resetEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		EnvironmentEnv, ListenAddrEnv, DatabaseURLEnv, APIPublicURLEnv, WebPublicURLEnv,
		TrustedProxiesEnv, BootstrapSecretFileEnv, TokenPepperFileEnv, EncryptionKeyFileEnv,
		MigrationsModeEnv, GracefulShutdownTimeoutEnv, HealthReadTimeoutEnv, HealthWriteTimeoutEnv,
		AuthProvidersFileEnv, WebhookKeysFileEnv, StaticDirectoryEnv, WebhookAllowedPrivateEnv,
		DeliveryConcurrencyEnv, DeliveryLeaseDurationEnv, DeliveryPollIntervalEnv,
		DelegationAudienceEnv, DelegationSubjectEnv, TransportPostureEnv, SearchModeEnv,
	} {
		t.Setenv(name, "")
	}
}

func writeSecret(t *testing.T, value string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte(value), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}
