package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/server/auth/githuboauth"
	"github.com/higress-group/issue-spec/internal/server/config"
	"github.com/higress-group/issue-spec/internal/server/publicurl"
)

const authDocsRelative = "docs/self-hosting/authentication/v1"

func TestExternalAuthDocumentationExamplesUseProductionShape(t *testing.T) {
	for _, path := range authExamplePaths(t) {
		raw := renderAuthExample(t, path)
		var file providerFile
		decoder := json.NewDecoder(strings.NewReader(string(raw)))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&file); err != nil {
			t.Fatalf("%s does not decode through providerFile: %v", path, err)
		}
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			t.Fatalf("%s contains more than one JSON value", path)
		}
		if len(file.Providers) != 1 {
			t.Fatalf("%s must contain exactly one canonical provider", path)
		}
		provider := file.Providers[0]
		if provider.ID == uuid.Nil || !providerName(provider.Name) || strings.TrimSpace(provider.Issuer) == "" ||
			strings.TrimSpace(provider.ClientID) == "" || strings.TrimSpace(provider.ClientSecret) == "" {
			t.Fatalf("%s omits a production-required provider field", path)
		}
		switch provider.Kind {
		case "github-oauth":
			admission, err := githuboauth.NormalizeAdmission(provider.Admission, true, provider.Issuer, provider.UserURL)
			if err != nil {
				t.Fatalf("%s fails production admission normalization: %v", path, err)
			}
			_ = githuboauth.AdmissionScopes(provider.Scopes, admission.Mode)
			if (provider.AuthURL == "") != (provider.TokenURL == "") {
				t.Fatalf("%s must configure auth_url and token_url together", path)
			}
		case "oidc":
			if provider.Admission != nil {
				t.Fatalf("%s configures GitHub-only admission for OIDC", path)
			}
		default:
			t.Fatalf("%s uses unsupported kind %q", path, provider.Kind)
		}
		for _, rawOrigin := range provider.AvatarOrigins {
			origin, err := publicurl.ParseOrigin("avatar origin", rawOrigin)
			if err != nil || !strings.HasPrefix(origin.String(), "https://") {
				t.Fatalf("%s contains invalid avatar origin %q", path, rawOrigin)
			}
		}
	}
}

func TestExternalAuthDocumentationCallbacksMatchComposition(t *testing.T) {
	origins, err := publicurl.NewWithPosture("https://api.intra.example", "https://issues.intra.example", nil, publicurl.TransportHTTPS)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range authExamplePaths(t) {
		var file providerFile
		if err := json.Unmarshal(renderAuthExample(t, path), &file); err != nil {
			t.Fatal(err)
		}
		name := file.Providers[0].Name
		got := origins.API.MustURL("/api/v1/auth/" + url.PathEscape(name) + "/callback")
		want := "https://api.intra.example/api/v1/auth/" + name + "/callback"
		if got != want {
			t.Fatalf("%s callback = %q, want %q", path, got, want)
		}
	}
}

func TestExternalAuthDocumentationProviderFieldReferenceComplete(t *testing.T) {
	document := readRepoFile(t, filepath.Join(authDocsRelative, "provider-file.md"))
	for _, typ := range []reflect.Type{
		reflect.TypeOf(providerConfig{}),
		reflect.TypeOf(githuboauth.AdmissionConfig{}),
		reflect.TypeOf(githuboauth.ApprovedOrganization{}),
	} {
		for index := 0; index < typ.NumField(); index++ {
			field := typ.Field(index)
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if name == "" || name == "-" {
				continue
			}
			if !strings.Contains(document, "`"+name+"`") {
				t.Errorf("provider reference omits JSON field %q", name)
			}
		}
	}
}

func TestExternalAuthDocumentationPlaceholdersAreSecretSafe(t *testing.T) {
	placeholder := regexp.MustCompile(`__ISSUE_SPEC_[A-Z0-9_]+__`)
	for _, path := range authExamplePaths(t) {
		raw := string(readAbsoluteFile(t, path))
		if !placeholder.MatchString(raw) {
			t.Errorf("%s has no explicit placeholders", path)
		}
		var decoded map[string]any
		if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
			t.Fatalf("%s is not JSON: %v", path, err)
		}
		providers := decoded["providers"].([]any)
		provider := providers[0].(map[string]any)
		secret, _ := provider["client_secret"].(string)
		if !placeholder.MatchString(secret) {
			t.Errorf("%s client_secret is not a safe placeholder", path)
		}
		for _, forbidden := range []string{"ghp_", "github_pat_", "set -x", "?code=", "?state="} {
			if strings.Contains(raw, forbidden) {
				t.Errorf("%s contains forbidden secret-like material %q", path, forbidden)
			}
		}
	}
}

func TestExternalAuthDocumentationLocalLinksResolve(t *testing.T) {
	root := repoRoot(t)
	linkPattern := regexp.MustCompile(`\[[^]]+\]\(([^)]+)\)`)
	paths := []string{
		"README.md", "README.zh-CN.md", "deployments/dev/README.md",
		"docs/self-hosting/README.md", "docs/self-hosting/README.zh-CN.md",
		"docs/self-hosting/local-development.md", "docs/self-hosting/local-development.zh-CN.md",
		"docs/self-hosting/operations/deployment.md",
	}
	err := filepath.WalkDir(filepath.Join(root, "docs/self-hosting/authentication"), func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() && strings.HasSuffix(path, ".md") {
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			paths = append(paths, relative)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, relative := range paths {
		document := readRepoFile(t, relative)
		for _, match := range linkPattern.FindAllStringSubmatch(document, -1) {
			target := strings.TrimSpace(strings.Split(match[1], "#")[0])
			if target == "" || strings.HasPrefix(target, "https://") || strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "mailto:") {
				continue
			}
			resolved := filepath.Clean(filepath.Join(root, filepath.Dir(relative), filepath.FromSlash(target)))
			if !strings.HasPrefix(resolved, root+string(filepath.Separator)) && resolved != root {
				t.Errorf("%s link escapes repository: %s", relative, target)
				continue
			}
			if _, err := os.Stat(resolved); err != nil {
				t.Errorf("%s has broken local link %s: %v", relative, target, err)
			}
		}
	}
}

func TestExternalAuthDocumentationTrustedHTTPAndComposeFixture(t *testing.T) {
	secretDir := t.TempDir()
	writeSecret := func(name, value string) string {
		path := filepath.Join(secretDir, name)
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	t.Setenv(config.EnvironmentEnv, string(config.EnvironmentProduction))
	t.Setenv(config.TransportPostureEnv, string(config.TransportTrustedInternalHTTP))
	t.Setenv(config.ListenAddrEnv, ":18080")
	t.Setenv(config.DatabaseURLEnv, "postgres://issue-spec:fixture@127.0.0.1/issue-spec")
	t.Setenv(config.APIPublicURLEnv, "http://10.23.8.14:18080")
	t.Setenv(config.WebPublicURLEnv, "http://issues.intra.example:18080")
	t.Setenv(config.BootstrapSecretFileEnv, writeSecret("bootstrap", strings.Repeat("b", 32)))
	t.Setenv(config.TokenPepperFileEnv, writeSecret("token-pepper", strings.Repeat("p", 32)))
	t.Setenv(config.EncryptionKeyFileEnv, writeSecret("encryption-key", strings.Repeat("e", 32)))
	t.Setenv(config.AuthProvidersFileEnv, writeSecret("auth-providers.json", string(renderAuthExample(t, authExamplePath(t, "github-unrestricted.json")))))
	t.Setenv(config.StaticDirectoryEnv, "")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("documented production trusted HTTP configuration is rejected: %v", err)
	}
	origins, err := configuredOrigins(cfg)
	if err != nil {
		t.Fatal(err)
	}
	callback := origins.API.MustURL("/api/v1/auth/github/callback")
	if callback != "http://10.23.8.14:18080/api/v1/auth/github/callback" || origins.Posture != publicurl.TransportTrustedInternalHTTP {
		t.Fatalf("trusted HTTP callback/posture mismatch: %q, %q", callback, origins.Posture)
	}

	compose := readRepoFile(t, "deployments/dev/compose.auth.yaml")
	for _, required := range []string{
		"ENVIRONMENT: production",
		"TRANSPORT_POSTURE: trusted-internal-http",
		"AUTH_PROVIDERS_FILE: /run/issue-spec-secrets/auth-providers.json",
		"http://127.0.0.1:8080",
	} {
		if !strings.Contains(compose, required) {
			t.Errorf("compose auth fixture omits %q", required)
		}
	}
}

func authExamplePaths(t *testing.T) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(repoRoot(t), authDocsRelative, "examples", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(matches)
	if len(matches) != 4 {
		t.Fatalf("expected four canonical auth examples, found %d", len(matches))
	}
	return matches
}

func authExamplePath(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join(repoRoot(t), authDocsRelative, "examples", name)
}

func renderAuthExample(t *testing.T, path string) []byte {
	t.Helper()
	value := string(readAbsoluteFile(t, path))
	replacements := map[string]string{
		"__ISSUE_SPEC_PROVIDER_UUID__":        "6da18dd1-359a-4f29-8abc-068d077f9af0",
		"__ISSUE_SPEC_GITHUB_CLIENT_ID__":     "fixture-github-client",
		"__ISSUE_SPEC_GITHUB_CLIENT_SECRET__": "fixture-github-secret",
		"__ISSUE_SPEC_GITHUB_ORG_LOGIN__":     "higress-group",
		"__ISSUE_SPEC_GITHUB_ORG_ID__":        "12345",
		"__ISSUE_SPEC_OIDC_CLIENT_ID__":       "fixture-oidc-client",
		"__ISSUE_SPEC_OIDC_CLIENT_SECRET__":   "fixture-oidc-secret",
	}
	for placeholder, replacement := range replacements {
		value = strings.ReplaceAll(value, placeholder, replacement)
	}
	if strings.Contains(value, "__ISSUE_SPEC_") {
		t.Fatalf("%s contains an unrecognized placeholder", path)
	}
	return []byte(value)
}

func readRepoFile(t *testing.T, relative string) string {
	t.Helper()
	return string(readAbsoluteFile(t, filepath.Join(repoRoot(t), filepath.FromSlash(relative))))
}

func readAbsoluteFile(t *testing.T, path string) []byte {
	t.Helper()
	value, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}
