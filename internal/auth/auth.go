package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	keyring "github.com/zalando/go-keyring"
)

const (
	serviceName            = "issue-spec"
	ConfigDirEnv           = "ISSUE_SPEC_CONFIG_DIR"
	CacheDirEnv            = "ISSUE_SPEC_CACHE_DIR"
	GitHubBackendEnv       = "ISSUE_SPEC_GITHUB_BACKEND"
	GitHubBackendAPIURLEnv = "ISSUE_SPEC_API_URL"
	GitHubBackendNameREST  = "rest"
	GitHubBackendNameGH    = "gh"
	GitHubBackendKindREST  = "rest"
	GitHubBackendKindCLI   = "external-cli"
	GitHubBackendModeAuto  = GitHubBackendMode("auto")
	GitHubBackendModeREST  = GitHubBackendMode("rest")
	GitHubBackendModeGH    = GitHubBackendMode("gh")
	IssueSpecTokenFileEnv  = "ISSUE_SPEC_TOKEN_FILE"
)

var ErrNoToken = errors.New("no issue-spec token is available")

type GitHubBackendMode string

type Token struct {
	Value            string                    `json:"-"`
	Source           string                    `json:"source"`
	User             string                    `json:"user,omitempty"`
	Scopes           []string                  `json:"scopes,omitempty"`
	Host             string                    `json:"host"`
	Profile          string                    `json:"profile,omitempty"`
	ProfileKind      string                    `json:"profile_kind,omitempty"`
	ServerInstanceID string                    `json:"server_instance_id,omitempty"`
	APIOrigin        string                    `json:"api_origin,omitempty"`
	Backend          *GitHubBackendDiagnostics `json:"backend,omitempty"`
}

type StoredCredential struct {
	Token string `json:"token"`
}

type GitHubBackendDiagnostics struct {
	Mode             string               `json:"mode"`
	Name             string               `json:"name,omitempty"`
	Kind             string               `json:"kind,omitempty"`
	Host             string               `json:"host"`
	SelectionSource  string               `json:"selection_source,omitempty"`
	TokenSource      string               `json:"token_source,omitempty"`
	Probes           []GitHubBackendProbe `json:"probes,omitempty"`
	Profile          string               `json:"profile,omitempty"`
	ProfileKind      string               `json:"profile_kind,omitempty"`
	ServerInstanceID string               `json:"server_instance_id,omitempty"`
	APIOrigin        string               `json:"api_origin,omitempty"`
}

type GitHubBackendProbe struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

type GitHubBackendSelection struct {
	Mode            GitHubBackendMode
	Name            string
	Kind            string
	Host            string
	SelectionSource string
	TokenSource     string
	Probes          []GitHubBackendProbe
	Token           Token
	Profile         Profile
	ProfileSource   string
}

type GitHubBackendSelectionOptions struct {
	GHAuthenticated func(context.Context, string) error
	Mode            *GitHubBackendMode
}

// WithProfile attaches resolved profile metadata without changing the backend
// that was selected. Callers that preserve an injected backend selector can
// use this to add profile diagnostics after selection.
func (s GitHubBackendSelection) WithProfile(profile Profile, source string) GitHubBackendSelection {
	s.Profile = profile
	s.ProfileSource = source
	s.Token = withProfile(s.Token, profile)
	return s
}

type credentialFile struct {
	Hosts  map[string]StoredCredential `json:"hosts"`
	Realms map[string]StoredCredential `json:"realms,omitempty"`
}

func ParseGitHubBackendMode(value string) (GitHubBackendMode, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return GitHubBackendModeAuto, nil
	}
	switch GitHubBackendMode(value) {
	case GitHubBackendModeAuto, GitHubBackendModeREST, GitHubBackendModeGH:
		return GitHubBackendMode(value), nil
	default:
		return "", fmt.Errorf("invalid %s %q (want auto, rest, or gh)", GitHubBackendEnv, value)
	}
}

func GitHubBackendModeFromEnv() (GitHubBackendMode, error) {
	return ParseGitHubBackendMode(os.Getenv(GitHubBackendEnv))
}

func SelectGitHubBackend(ctx context.Context, host string) (GitHubBackendSelection, error) {
	return SelectGitHubBackendWithOptions(ctx, host, GitHubBackendSelectionOptions{})
}

func SelectGitHubBackendWithOptions(ctx context.Context, host string, opts GitHubBackendSelectionOptions) (GitHubBackendSelection, error) {
	if customAPIURLActive() || ProfileNameFromEnv() != "" {
		return SelectProfileBackendWithOptions(ctx, "", host, opts)
	}
	return selectGitHubBackendCore(ctx, host, opts, false)
}

func selectGitHubBackendCore(ctx context.Context, host string, opts GitHubBackendSelectionOptions, ignoreCustomAPI bool) (GitHubBackendSelection, error) {
	host = NormalizeHost(host)
	mode, err := gitHubBackendModeForSelection(opts)
	selection := GitHubBackendSelection{Mode: mode, Host: host}
	if err != nil {
		return selection, err
	}

	if mode == GitHubBackendModeGH {
		selection = selectGHBackend(mode, host, "override:gh")
		if !ignoreCustomAPI && customAPIURLActive() {
			return selection, fmt.Errorf("%s is only supported by the rest GitHub backend; unset it or use %s=rest", GitHubBackendAPIURLEnv, GitHubBackendEnv)
		}
		return selection, nil
	}

	token, err := ResolveToken(ctx, host)
	if err == nil {
		source := "auto:token"
		if mode == GitHubBackendModeREST {
			source = "override:rest"
		}
		return selectRESTBackend(mode, host, source, token), nil
	}
	if !errors.Is(err, ErrNoToken) {
		return selection, err
	}

	selection.Token = Token{Host: host}
	if mode == GitHubBackendModeREST {
		selection = selectRESTBackend(mode, host, "override:rest", Token{Host: host})
		selection.Probes = append(selection.Probes, GitHubBackendProbe{Name: GitHubBackendNameREST, Status: "unavailable", Error: ErrNoToken.Error()})
		return selection, fmt.Errorf("rest GitHub backend selected but %w", err)
	}

	selection.Probes = append(selection.Probes, GitHubBackendProbe{Name: GitHubBackendNameREST, Status: "unavailable", Error: ErrNoToken.Error()})
	if !ignoreCustomAPI && customAPIURLActive() {
		return selection, fmt.Errorf("%w; %s requires a rest token because the gh backend cannot use custom API URLs", err, GitHubBackendAPIURLEnv)
	}
	if opts.GHAuthenticated == nil {
		selection.Probes = append(selection.Probes, GitHubBackendProbe{Name: GitHubBackendNameGH, Status: "not_configured", Error: "gh authentication probe is not configured"})
		return selection, fmt.Errorf("%w; gh authentication probe is not configured", err)
	}
	if probeErr := opts.GHAuthenticated(ctx, host); probeErr != nil {
		selection.Probes = append(selection.Probes, GitHubBackendProbe{Name: GitHubBackendNameGH, Status: "unavailable", Error: probeErr.Error()})
		return selection, fmt.Errorf("%w; gh authentication probe failed for %s: %v", err, host, probeErr)
	}
	return selectGHBackend(mode, host, "auto:gh"), nil
}

func gitHubBackendModeForSelection(opts GitHubBackendSelectionOptions) (GitHubBackendMode, error) {
	if opts.Mode != nil {
		mode := *opts.Mode
		if _, err := ParseGitHubBackendMode(string(mode)); err != nil {
			return mode, err
		}
		return mode, nil
	}
	return GitHubBackendModeFromEnv()
}

func (s GitHubBackendSelection) Diagnostics() GitHubBackendDiagnostics {
	diagnostics := GitHubBackendDiagnostics{
		Mode:            string(s.Mode),
		Name:            s.Name,
		Kind:            s.Kind,
		Host:            s.Host,
		SelectionSource: s.SelectionSource,
		TokenSource:     s.TokenSource,
		Probes:          s.Probes,
	}
	if s.Profile.Name != "" {
		diagnostics.Profile = s.Profile.Name
		diagnostics.ProfileKind = string(s.Profile.Kind)
		diagnostics.ServerInstanceID = s.Profile.ServerInstanceID
		diagnostics.APIOrigin = s.Profile.APIOrigin()
	}
	return diagnostics
}

func (s GitHubBackendSelection) TokenWithDiagnostics() Token {
	token := s.Token
	if token.Host == "" {
		token.Host = s.Host
	}
	if token.Source == "" && s.Name == GitHubBackendNameGH {
		token.Source = "gh"
	}
	diagnostics := s.Diagnostics()
	token.Backend = &diagnostics
	return token
}

func selectRESTBackend(mode GitHubBackendMode, host, selectionSource string, token Token) GitHubBackendSelection {
	token.Host = host
	return GitHubBackendSelection{
		Mode:            mode,
		Name:            GitHubBackendNameREST,
		Kind:            GitHubBackendKindREST,
		Host:            host,
		SelectionSource: selectionSource,
		TokenSource:     token.Source,
		Token:           token,
	}
}

func selectGHBackend(mode GitHubBackendMode, host, selectionSource string) GitHubBackendSelection {
	return GitHubBackendSelection{
		Mode:            mode,
		Name:            GitHubBackendNameGH,
		Kind:            GitHubBackendKindCLI,
		Host:            host,
		SelectionSource: selectionSource,
		Token:           Token{Source: "gh", Host: host},
	}
}

func customAPIURLActive() bool {
	return strings.TrimSpace(os.Getenv(GitHubBackendAPIURLEnv)) != ""
}

func ResolveToken(_ context.Context, host string) (Token, error) {
	host = NormalizeHost(host)
	for _, envName := range []string{"ISSUE_SPEC_TOKEN", "GH_TOKEN", "GITHUB_TOKEN"} {
		if value := strings.TrimSpace(os.Getenv(envName)); value != "" {
			return Token{Value: value, Source: "env:" + envName, Host: host}, nil
		}
	}

	if value, err := keyring.Get(serviceName, host); err == nil && strings.TrimSpace(value) != "" {
		return Token{Value: strings.TrimSpace(value), Source: "keyring", Host: host}, nil
	}

	creds, err := readCredentialFile()
	if err != nil {
		return Token{}, err
	}
	if stored, ok := creds.Hosts[host]; ok && strings.TrimSpace(stored.Token) != "" {
		return Token{Value: strings.TrimSpace(stored.Token), Source: "config", Host: host}, nil
	}

	return Token{Host: host}, ErrNoToken
}

// ResolveProfileToken enforces the selected profile realm. A self-hosted
// profile never reads GH_TOKEN, GITHUB_TOKEN, GitHub host credentials, or
// credentials stored for another profile.
func ResolveProfileToken(_ context.Context, profile Profile) (Token, error) {
	profile, err := profile.Normalized()
	if err != nil {
		return Token{}, err
	}
	if IsBuiltinGitHubProfile(profile) {
		token, err := ResolveToken(context.Background(), profile.Hostname)
		return withProfile(token, profile), err
	}
	if path := strings.TrimSpace(os.Getenv(IssueSpecTokenFileEnv)); path != "" {
		value, err := readPrivateTokenFile(path)
		if err != nil {
			return withProfile(Token{Host: profile.Hostname}, profile), err
		}
		return withProfile(Token{Value: value, Source: "env:" + IssueSpecTokenFileEnv, Host: profile.Hostname}, profile), nil
	}
	if value := strings.TrimSpace(os.Getenv("ISSUE_SPEC_TOKEN")); value != "" {
		return withProfile(Token{Value: value, Source: "env:ISSUE_SPEC_TOKEN", Host: profile.Hostname}, profile), nil
	}
	if profile.Ephemeral {
		return withProfile(Token{Host: profile.Hostname}, profile), fmt.Errorf("%w for origin-bound profile %q; set ISSUE_SPEC_TOKEN explicitly", ErrNoToken, profile.Name)
	}
	realm := profile.RealmKey()
	if value, err := keyring.Get(serviceName, realm); err == nil && strings.TrimSpace(value) != "" {
		return withProfile(Token{Value: strings.TrimSpace(value), Source: "keyring", Host: profile.Hostname}, profile), nil
	}
	creds, err := readCredentialFile()
	if err != nil {
		return Token{}, err
	}
	if stored, ok := creds.Realms[realm]; ok && strings.TrimSpace(stored.Token) != "" {
		return withProfile(Token{Value: strings.TrimSpace(stored.Token), Source: "config", Host: profile.Hostname}, profile), nil
	}
	return withProfile(Token{Host: profile.Hostname}, profile), fmt.Errorf("%w for origin-bound profile %q; run issue-spec auth login --profile %s --with-token", ErrNoToken, profile.Name, profile.Name)
}

func readPrivateTokenFile(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("%s must name an absolute private file", IssueSpecTokenFileEnv)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("read %s: credential file unavailable", IssueSpecTokenFileEnv)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || !singleLink(info) || info.Size() > 1<<20 {
		return "", fmt.Errorf("read %s: credential file is not a private regular file", IssueSpecTokenFileEnv)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: credential file unavailable", IssueSpecTokenFileEnv)
	}
	value := strings.TrimSpace(string(data))
	if value == "" || strings.ContainsAny(value, "\r\n\x00") {
		return "", fmt.Errorf("read %s: credential file is invalid", IssueSpecTokenFileEnv)
	}
	return value, nil
}

func StoreToken(_ context.Context, host, token string, insecureStorage bool) (string, error) {
	host = NormalizeHost(host)
	token = strings.TrimSpace(token)
	if token == "" {
		return "", errors.New("token is empty")
	}

	if !insecureStorage {
		if err := keyring.Set(serviceName, host, token); err != nil {
			return "", fmt.Errorf("store token in OS keyring for %s: %w; rerun with --insecure-storage to use explicit plaintext fallback", host, err)
		}
		return "keyring", nil
	}

	creds, err := readCredentialFile()
	if err != nil {
		return "", err
	}
	if creds.Hosts == nil {
		creds.Hosts = map[string]StoredCredential{}
	}
	creds.Hosts[host] = StoredCredential{Token: token}
	if err := writeCredentialFile(creds); err != nil {
		return "", err
	}
	return "config", nil
}

func StoreProfileToken(_ context.Context, profile Profile, token string, insecureStorage bool) (string, error) {
	profile, err := profile.Normalized()
	if err != nil {
		return "", err
	}
	if IsBuiltinGitHubProfile(profile) {
		return StoreToken(context.Background(), profile.Hostname, token, insecureStorage)
	}
	if profile.Ephemeral {
		return "", errors.New("ephemeral legacy API profile credentials cannot be persisted; use ISSUE_SPEC_TOKEN")
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return "", errors.New("token is empty")
	}
	realm := profile.RealmKey()
	if !insecureStorage {
		if err := keyring.Set(serviceName, realm, token); err != nil {
			return "", fmt.Errorf("store token in OS keyring for profile %s: %w; rerun with --insecure-storage to use explicit plaintext fallback", profile.Name, err)
		}
		return "keyring", nil
	}
	creds, err := readCredentialFile()
	if err != nil {
		return "", err
	}
	if creds.Realms == nil {
		creds.Realms = map[string]StoredCredential{}
	}
	creds.Realms[realm] = StoredCredential{Token: token}
	if err := writeCredentialFile(creds); err != nil {
		return "", err
	}
	return "config", nil
}

func DeleteToken(_ context.Context, host string) error {
	host = NormalizeHost(host)
	var errs []error
	if err := keyring.Delete(serviceName, host); err != nil && !errors.Is(err, keyring.ErrNotFound) {
		errs = append(errs, err)
	}

	creds, err := readCredentialFile()
	if err != nil {
		errs = append(errs, err)
	} else if _, ok := creds.Hosts[host]; ok {
		delete(creds.Hosts, host)
		if err := writeCredentialFile(creds); err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}

func DeleteProfileToken(_ context.Context, profile Profile) error {
	profile, err := profile.Normalized()
	if err != nil {
		return err
	}
	if IsBuiltinGitHubProfile(profile) {
		return DeleteToken(context.Background(), profile.Hostname)
	}
	if profile.Ephemeral {
		return nil
	}
	realm := profile.RealmKey()
	var errs []error
	if err := keyring.Delete(serviceName, realm); err != nil && !errors.Is(err, keyring.ErrNotFound) {
		errs = append(errs, err)
	}
	creds, err := readCredentialFile()
	if err != nil {
		errs = append(errs, err)
	} else if _, ok := creds.Realms[realm]; ok {
		delete(creds.Realms, realm)
		if err := writeCredentialFile(creds); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func EnvTokenActive() string {
	for _, envName := range []string{"ISSUE_SPEC_TOKEN", "GH_TOKEN", "GITHUB_TOKEN"} {
		if strings.TrimSpace(os.Getenv(envName)) != "" {
			return envName
		}
	}
	return ""
}

func EnvTokenActiveForProfile(profile Profile) string {
	if !IsBuiltinGitHubProfile(profile) {
		if strings.TrimSpace(os.Getenv("ISSUE_SPEC_TOKEN")) != "" {
			return "ISSUE_SPEC_TOKEN"
		}
		return ""
	}
	return EnvTokenActive()
}

func NormalizeHost(host string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return "github.com"
	}
	host = strings.TrimPrefix(host, "https://")
	host = strings.TrimPrefix(host, "http://")
	host = strings.TrimSuffix(host, "/")
	return host
}

// EnvLookup resolves an environment variable, reporting whether it was present.
// It mirrors os.LookupEnv so callers can inject a hermetic environment in tests
// and production code can pass os.LookupEnv for the real process environment.
type EnvLookup func(string) (string, bool)

// ErrHomeUnset indicates the HOME variable was not present in the environment.
// It is distinct from ErrHomeInvalid so callers (and tests) can tell an unset
// HOME apart from an empty or non-absolute HOME.
var ErrHomeUnset = errors.New("HOME is not set")

// ErrHomeInvalid indicates HOME was present but empty or not an absolute path.
// An invalid HOME must never silently resolve to a wrong path.
var ErrHomeInvalid = errors.New("HOME is empty or not an absolute path")

// ConfigDir returns the issue-spec configuration directory using the platform
// resolution order: the ISSUE_SPEC_CONFIG_DIR override, then XDG_CONFIG_HOME,
// then the native OS default. It reads the real process environment.
func ConfigDir() (string, error) {
	return resolveConfigDir(os.LookupEnv)
}

// CacheDir returns the issue-spec cache directory using the platform resolution
// order: the ISSUE_SPEC_CACHE_DIR override, then XDG_CACHE_HOME, then the native
// OS default. It reads the real process environment.
func CacheDir() (string, error) {
	return resolveCacheDir(os.LookupEnv)
}

func resolveConfigDir(env EnvLookup) (string, error) {
	return resolveConfigDirFor(env, runtime.GOOS)
}

func resolveCacheDir(env EnvLookup) (string, error) {
	return resolveCacheDirFor(env, runtime.GOOS)
}

func resolveConfigDirFor(env EnvLookup, goos string) (string, error) {
	return resolveDir(env, goos, ConfigDirEnv, "XDG_CONFIG_HOME", nativeConfigDir)
}

func resolveCacheDirFor(env EnvLookup, goos string) (string, error) {
	return resolveDir(env, goos, CacheDirEnv, "XDG_CACHE_HOME", nativeCacheDir)
}

// resolveDir applies the shared override -> XDG -> native default order on every
// platform. The override, when set to a non-empty value, is returned verbatim
// (matching the historical ISSUE_SPEC_CONFIG_DIR behavior). An XDG variable is
// honored only when it is set to an absolute path; a relative or empty XDG value
// is ignored so it cannot resolve outside the user's home tree. os.UserConfigDir
// ignores XDG on macOS, so honoring it here explicitly closes that gap.
func resolveDir(env EnvLookup, goos, overrideEnv, xdgEnv string, native func(EnvLookup, string) (string, error)) (string, error) {
	if env == nil {
		env = os.LookupEnv
	}
	if value, ok := env(overrideEnv); ok {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed, nil
		}
	}
	if value, ok := env(xdgEnv); ok {
		if trimmed := strings.TrimSpace(value); trimmed != "" && filepath.IsAbs(trimmed) {
			return filepath.Join(trimmed, "issue-spec"), nil
		}
	}
	return native(env, goos)
}

// nativeConfigDir mirrors os.UserConfigDir's native default per platform (with
// XDG handled by resolveDir) so paths are byte-for-byte identical to today when
// no override or XDG variable is set.
func nativeConfigDir(env EnvLookup, goos string) (string, error) {
	switch goos {
	case "windows":
		return nativeWindowsDir(env, "AppData")
	case "darwin", "ios":
		home, err := resolveHome(env)
		if err != nil {
			return "", err
		}
		return filepath.Join(home, "Library", "Application Support", "issue-spec"), nil
	default:
		home, err := resolveHome(env)
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".config", "issue-spec"), nil
	}
}

// nativeCacheDir mirrors os.UserCacheDir's native default per platform.
func nativeCacheDir(env EnvLookup, goos string) (string, error) {
	switch goos {
	case "windows":
		return nativeWindowsDir(env, "LocalAppData")
	case "darwin", "ios":
		home, err := resolveHome(env)
		if err != nil {
			return "", err
		}
		return filepath.Join(home, "Library", "Caches", "issue-spec"), nil
	default:
		home, err := resolveHome(env)
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".cache", "issue-spec"), nil
	}
}

func nativeWindowsDir(env EnvLookup, key string) (string, error) {
	value, ok := env(key)
	if !ok {
		return "", fmt.Errorf("%%%s%% is not set", key)
	}
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("%%%s%% is empty", key)
	}
	return filepath.Join(value, "issue-spec"), nil
}

// resolveHome returns the HOME directory, distinguishing an unset HOME
// (ErrHomeUnset) from an empty or non-absolute HOME (ErrHomeInvalid) so an
// invalid value never silently resolves to a wrong path. It never falls back to
// reading the host account through any other channel.
func resolveHome(env EnvLookup) (string, error) {
	value, ok := env("HOME")
	if !ok {
		return "", ErrHomeUnset
	}
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("%w: HOME is empty", ErrHomeInvalid)
	}
	if !filepath.IsAbs(value) {
		return "", fmt.Errorf("%w: HOME=%q is not absolute", ErrHomeInvalid, value)
	}
	return value, nil
}

func credentialPath() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "credentials.json"), nil
}

func readCredentialFile() (credentialFile, error) {
	path, err := credentialPath()
	if err != nil {
		return credentialFile{}, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return credentialFile{Hosts: map[string]StoredCredential{}, Realms: map[string]StoredCredential{}}, nil
	}
	if err != nil {
		return credentialFile{}, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return credentialFile{Hosts: map[string]StoredCredential{}, Realms: map[string]StoredCredential{}}, nil
	}
	var creds credentialFile
	if err := json.Unmarshal(data, &creds); err != nil {
		return credentialFile{}, fmt.Errorf("read issue-spec credentials %s: %w", path, err)
	}
	if creds.Hosts == nil {
		creds.Hosts = map[string]StoredCredential{}
	}
	if creds.Realms == nil {
		creds.Realms = map[string]StoredCredential{}
	}
	return creds, nil
}

func writeCredentialFile(creds credentialFile) error {
	path, err := credentialPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}
