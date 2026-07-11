package auth

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func hostedProfile(name, instance, api string) Profile {
	return Profile{
		Name: name, Kind: ProfileKindHosted, APIURL: api,
		NativeAPIURL: strings.TrimRight(api, "/") + "/api/v1",
		WebURL:       "https://issues.example.test", ServerInstanceID: instance,
	}
}

func TestProfileCredentialsAreBoundToInstanceAndFullAPIBase(t *testing.T) {
	clearAuthEnv(t)
	t.Setenv("ISSUE_SPEC_CONFIG_DIR", t.TempDir())
	first := hostedProfile("local", "instance-a", "https://issues.example.test/tenant-a")
	otherInstance := hostedProfile("local", "instance-b", "https://issues.example.test/tenant-a")
	otherPath := hostedProfile("local", "instance-a", "https://issues.example.test/tenant-b")
	for _, profile := range []Profile{first, otherInstance, otherPath} {
		if _, err := profile.Normalized(); err != nil {
			t.Fatal(err)
		}
	}
	if first.RealmKey() == otherInstance.RealmKey() || first.RealmKey() == otherPath.RealmKey() {
		t.Fatal("instance or API path change did not change credential realm")
	}
	if _, err := StoreProfileToken(context.Background(), first, "first-secret", true); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveProfileToken(context.Background(), first)
	if err != nil || got.Value != "first-secret" {
		t.Fatalf("first token = %+v err=%v", got, err)
	}
	for _, profile := range []Profile{otherInstance, otherPath} {
		if got, err := ResolveProfileToken(context.Background(), profile); !errors.Is(err, ErrNoToken) || got.Value != "" {
			t.Fatalf("realm %s reused token: %+v err=%v", profile.RealmKey(), got, err)
		}
	}
}

func TestSelfHostedProfileNeverReadsGitHubCredentials(t *testing.T) {
	clearAuthEnv(t)
	t.Setenv("ISSUE_SPEC_CONFIG_DIR", t.TempDir())
	t.Setenv("GH_TOKEN", "gh-secret")
	t.Setenv("GITHUB_TOKEN", "github-secret")
	if _, err := StoreToken(context.Background(), "issues.example.test", "host-secret", true); err != nil {
		t.Fatal(err)
	}
	profile := hostedProfile("local", "instance-a", "https://issues.example.test")
	token, err := ResolveProfileToken(context.Background(), profile)
	if !errors.Is(err, ErrNoToken) || token.Value != "" {
		t.Fatalf("self-hosted resolution crossed realm: %+v err=%v", token, err)
	}
	t.Setenv("ISSUE_SPEC_TOKEN", "explicit-secret")
	token, err = ResolveProfileToken(context.Background(), profile)
	if err != nil || token.Value != "explicit-secret" || token.Source != "env:ISSUE_SPEC_TOKEN" {
		t.Fatalf("explicit token = %+v err=%v", token, err)
	}
}

func TestSelfHostedProfileReadsOnlyPrivateTokenFile(t *testing.T) {
	clearAuthEnv(t)
	t.Setenv("ISSUE_SPEC_CONFIG_DIR", t.TempDir())
	profile := hostedProfile("local", "instance-a", "https://issues.example.test")
	path := filepath.Join(t.TempDir(), "issue.token")
	if err := os.WriteFile(path, []byte("delegated-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(IssueSpecTokenFileEnv, path)
	token, err := ResolveProfileToken(context.Background(), profile)
	if err != nil || token.Value != "delegated-secret" || token.Source != "env:"+IssueSpecTokenFileEnv {
		t.Fatalf("file token = %+v err=%v", token, err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if token, err := ResolveProfileToken(context.Background(), profile); err == nil || token.Value != "" {
		t.Fatalf("world-readable token accepted: %+v err=%v", token, err)
	}
}

func TestNamedGitHubProfileAlsoUsesIsolatedRealm(t *testing.T) {
	clearAuthEnv(t)
	t.Setenv("ISSUE_SPEC_CONFIG_DIR", t.TempDir())
	profile := Profile{
		Name: "enterprise", Kind: ProfileKindGitHub, Hostname: "ghe.example.test",
		APIURL: "https://ghe.example.test/api/v3", WebURL: "https://ghe.example.test", ServerInstanceID: "ghe-instance",
	}
	if _, err := StoreToken(context.Background(), profile.Hostname, "host-token", true); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GH_TOKEN", "gh-env-token")
	if token, err := ResolveProfileToken(context.Background(), profile); !errors.Is(err, ErrNoToken) || token.Value != "" {
		t.Fatalf("named GitHub profile reused host credential: %+v err=%v", token, err)
	}
	if _, err := StoreProfileToken(context.Background(), profile, "profile-token", true); err != nil {
		t.Fatal(err)
	}
	token, err := ResolveProfileToken(context.Background(), profile)
	if err != nil || token.Value != "profile-token" || token.Source != "config" {
		t.Fatalf("profile token = %+v err=%v", token, err)
	}
}

func TestLegacyAPIURLIsEphemeralAndRequiresExplicitIssueSpecToken(t *testing.T) {
	clearAuthEnv(t)
	t.Setenv("ISSUE_SPEC_CONFIG_DIR", t.TempDir())
	t.Setenv(GitHubBackendAPIURLEnv, "https://issues.example.test/custom/")
	t.Setenv("GH_TOKEN", "must-not-cross")
	profile, source, err := ResolveProfile("", "github.com")
	if err != nil {
		t.Fatal(err)
	}
	if !profile.Ephemeral || profile.Kind != ProfileKindHosted || source != "env:"+GitHubBackendAPIURLEnv {
		t.Fatalf("legacy profile = %+v source=%q", profile, source)
	}
	if !sameEndpointOrigin(profile.APIURL, profile.NativeAPIURL) || !sameEndpointOrigin(profile.APIURL, profile.WebURL) {
		t.Fatalf("legacy profile endpoints escaped API origin: %+v", profile)
	}
	if _, err := ResolveProfileToken(context.Background(), profile); !errors.Is(err, ErrNoToken) {
		t.Fatalf("legacy token error = %v", err)
	}
	if err := SaveProfile(profile, false); err == nil {
		t.Fatal("ephemeral profile persisted")
	}
	if _, err := StoreProfileToken(context.Background(), profile, "token", true); err == nil {
		t.Fatal("ephemeral token persisted")
	}
}

func TestProfilePersistenceAndRedactedDiagnostics(t *testing.T) {
	clearAuthEnv(t)
	dir := t.TempDir()
	t.Setenv("ISSUE_SPEC_CONFIG_DIR", dir)
	profile := hostedProfile("staging", "instance-staging", "https://issues.example.test")
	profile.CAFile = filepath.Join(dir, "ca.pem")
	if err := SaveProfile(profile, true); err != nil {
		t.Fatal(err)
	}
	resolved, source, err := ResolveProfile("", "github.com")
	if err != nil || source != "config" || resolved.Name != "staging" || resolved.CAFile != profile.CAFile {
		t.Fatalf("resolved = %+v source=%q err=%v", resolved, source, err)
	}
	t.Setenv("ISSUE_SPEC_TOKEN", "diagnostic-secret")
	selection, err := SelectProfileBackendWithOptions(context.Background(), "staging", "github.com", GitHubBackendSelectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(selection.TokenWithDiagnostics())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "diagnostic-secret") {
		t.Fatalf("secret leaked: %s", data)
	}
	for _, want := range []string{`"profile":"staging"`, `"profile_kind":"self-hosted"`, `"server_instance_id":"instance-staging"`, `"api_origin":"https://issues.example.test"`} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("missing %s in %s", want, data)
		}
	}
}

func TestPublicBackendSelectorHonorsIssueSpecProfile(t *testing.T) {
	clearAuthEnv(t)
	t.Setenv("ISSUE_SPEC_CONFIG_DIR", t.TempDir())
	profile := hostedProfile("selected", "selected-instance", "https://issues.example.test/selected")
	if err := SaveProfile(profile, false); err != nil {
		t.Fatal(err)
	}
	t.Setenv(ProfileEnv, "selected")
	t.Setenv("ISSUE_SPEC_TOKEN", "selected-secret")
	selection, err := SelectGitHubBackendWithOptions(context.Background(), "github.com", GitHubBackendSelectionOptions{
		GHAuthenticated: func(context.Context, string) error {
			t.Fatal("self-hosted profile must not probe gh")
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if selection.Profile.Name != "selected" || selection.Name != GitHubBackendNameREST || selection.Token.Value != "selected-secret" || selection.Host != "issues.example.test" {
		t.Fatalf("selection = %+v", selection)
	}
}

func TestProfileValidationRejectsCredentialAndURLInjection(t *testing.T) {
	for _, apiURL := range []string{
		"https://user:secret@issues.example.test",
		"https://issues.example.test?token=secret",
		"https://issues.example.test#fragment",
		"https://issues.example.test\\@evil.example",
		"https://issues.example.test\r\nX-Evil: yes",
	} {
		profile := hostedProfile("local", "instance", apiURL)
		if _, err := profile.Normalized(); err == nil {
			t.Fatalf("API URL %q accepted", apiURL)
		}
	}
}

func TestSelfHostedProfileRequiresTrustedSameOriginEndpoints(t *testing.T) {
	tests := []struct {
		name         string
		nativeAPIURL string
		webURL       string
		want         string
	}{
		{
			name:         "native host",
			nativeAPIURL: "https://attacker.example.test/api/v1",
			webURL:       "https://issues.example.test",
			want:         "native API URL must use the same origin as API URL",
		},
		{
			name:         "native scheme",
			nativeAPIURL: "http://issues.example.test/api/v1",
			webURL:       "https://issues.example.test",
			want:         "native API URL must use the same origin as API URL",
		},
		{
			name:         "native port",
			nativeAPIURL: "https://issues.example.test:8443/api/v1",
			webURL:       "https://issues.example.test",
			want:         "native API URL must use the same origin as API URL",
		},
		{
			name:         "web host",
			nativeAPIURL: "https://issues.example.test/api/v1",
			webURL:       "https://attacker.example.test",
			want:         "web URL must use the same origin as API URL",
		},
		{
			name:         "web scheme",
			nativeAPIURL: "https://issues.example.test/api/v1",
			webURL:       "http://issues.example.test",
			want:         "web URL must use the same origin as API URL",
		},
		{
			name:         "web port",
			nativeAPIURL: "https://issues.example.test/api/v1",
			webURL:       "https://issues.example.test:8443",
			want:         "web URL must use the same origin as API URL",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			profile := Profile{
				Name: "local", Kind: ProfileKindHosted,
				APIURL: "https://issues.example.test/api/v3", NativeAPIURL: tt.nativeAPIURL,
				WebURL: tt.webURL, ServerInstanceID: "instance-a",
			}
			if _, err := profile.Normalized(); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Normalized error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestSelfHostedProfileSameOriginAllowsIndependentPathsAndDefaultPorts(t *testing.T) {
	profile := Profile{
		Name: "local", Kind: ProfileKindHosted,
		APIURL:       "https://ISSUES.EXAMPLE.TEST:443/tenant/api/v3",
		NativeAPIURL: "https://issues.example.test/tenant/api/v1",
		WebURL:       "https://issues.example.test/tenant", ServerInstanceID: "instance-a",
	}
	normalized, err := profile.Normalized()
	if err != nil {
		t.Fatal(err)
	}
	if normalized.APIURL != "https://issues.example.test/tenant/api/v3" ||
		normalized.NativeAPIURL != "https://issues.example.test/tenant/api/v1" ||
		normalized.WebURL != "https://issues.example.test/tenant" {
		t.Fatalf("normalized endpoints = %+v", normalized)
	}
}

func TestSaveProfileRejectsCrossOriginUpdateWithoutReplacingExisting(t *testing.T) {
	clearAuthEnv(t)
	t.Setenv("ISSUE_SPEC_CONFIG_DIR", t.TempDir())
	profile := Profile{
		Name: "local", Kind: ProfileKindHosted,
		APIURL: "https://issues.example.test/api/v3", NativeAPIURL: "https://issues.example.test/api/v1",
		WebURL: "https://issues.example.test", ServerInstanceID: "instance-a",
	}
	if err := SaveProfile(profile, true); err != nil {
		t.Fatal(err)
	}
	profile.NativeAPIURL = "https://attacker.example.test/api/v1"
	if err := SaveProfile(profile, true); err == nil || !strings.Contains(err.Error(), "same origin") {
		t.Fatalf("cross-origin update error = %v", err)
	}
	resolved, source, err := ResolveProfile("local", "github.com")
	if err != nil || source != "config" {
		t.Fatalf("ResolveProfile = %+v source=%q err=%v", resolved, source, err)
	}
	if resolved.NativeAPIURL != "https://issues.example.test/api/v1" {
		t.Fatalf("rejected update replaced profile: %+v", resolved)
	}
}
