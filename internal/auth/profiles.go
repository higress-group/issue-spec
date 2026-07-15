package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	pathpkg "path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

const (
	ProfileEnv         = "ISSUE_SPEC_PROFILE"
	DefaultProfileName = "github"
	ProfileKindGitHub  = ProfileKind("github")
	ProfileKindHosted  = ProfileKind("self-hosted")
)

var profileNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

type ProfileKind string

type OnboardingPolicy struct {
	AllowRepositoryCreate bool `json:"allow_repository_create,omitempty"`
	AllowSourceBinding    bool `json:"allow_source_binding,omitempty"`
	AllowUnattended       bool `json:"allow_unattended,omitempty"`
}

// Profile is a credential and API-origin realm. Credentials are never stored
// in this object or profiles.json.
type Profile struct {
	Name                 string           `json:"name"`
	Kind                 ProfileKind      `json:"kind"`
	Hostname             string           `json:"hostname,omitempty"`
	APIURL               string           `json:"api_url"`
	NativeAPIURL         string           `json:"native_api_url,omitempty"`
	WebURL               string           `json:"web_url"`
	ServerInstanceID     string           `json:"server_instance_id"`
	OperatorRegistryFile string           `json:"operator_registry_file,omitempty"`
	OnboardingPolicy     OnboardingPolicy `json:"onboarding_policy,omitempty"`
	CAFile               string           `json:"ca_file,omitempty"`
	Ephemeral            bool             `json:"-"`
}

type profileFile struct {
	Version        int                `json:"version"`
	DefaultProfile string             `json:"default_profile,omitempty"`
	Profiles       map[string]Profile `json:"profiles"`
}

func ProfileNameFromEnv() string { return strings.TrimSpace(os.Getenv(ProfileEnv)) }

func (p Profile) Validate() error {
	if !profileNamePattern.MatchString(p.Name) {
		return fmt.Errorf("profile name %q must match %s", p.Name, profileNamePattern.String())
	}
	switch p.Kind {
	case ProfileKindGitHub, ProfileKindHosted:
	default:
		return fmt.Errorf("profile %q kind must be github or self-hosted", p.Name)
	}
	apiURL, err := canonicalEndpoint(p.APIURL)
	if err != nil {
		return fmt.Errorf("profile %q API URL: %w", p.Name, err)
	}
	webURL, err := canonicalEndpoint(p.WebURL)
	if err != nil {
		return fmt.Errorf("profile %q web URL: %w", p.Name, err)
	}
	if p.Kind == ProfileKindHosted {
		if strings.TrimSpace(p.ServerInstanceID) == "" {
			return fmt.Errorf("profile %q server instance id is required", p.Name)
		}
		if p.NativeAPIURL == "" {
			return fmt.Errorf("profile %q native API URL is required", p.Name)
		}
		nativeAPIURL, err := canonicalEndpoint(p.NativeAPIURL)
		if err != nil {
			return fmt.Errorf("profile %q native API URL: %w", p.Name, err)
		}
		if !sameEndpointOrigin(apiURL, nativeAPIURL) {
			return fmt.Errorf("profile %q native API URL must use the same origin as API URL", p.Name)
		}
		if !sameEndpointOrigin(apiURL, webURL) {
			return fmt.Errorf("profile %q web URL must use the same origin as API URL", p.Name)
		}
		if p.OperatorRegistryFile != "" && (!filepath.IsAbs(p.OperatorRegistryFile) || filepath.Clean(p.OperatorRegistryFile) != p.OperatorRegistryFile) {
			return fmt.Errorf("profile %q operator registry file must be a clean absolute path", p.Name)
		}
	} else if NormalizeHost(p.Hostname) == "" {
		return fmt.Errorf("profile %q GitHub hostname is required", p.Name)
	} else if p.OperatorRegistryFile != "" || p.OnboardingPolicy != (OnboardingPolicy{}) {
		return fmt.Errorf("profile %q GitHub profiles cannot define self-hosted onboarding configuration", p.Name)
	}
	if p.CAFile != "" && !filepath.IsAbs(p.CAFile) {
		return fmt.Errorf("profile %q CA file must be an absolute path", p.Name)
	}
	return nil
}

func (p Profile) Normalized() (Profile, error) {
	p.Name = strings.TrimSpace(p.Name)
	p.Kind = ProfileKind(strings.ToLower(strings.TrimSpace(string(p.Kind))))
	if strings.TrimSpace(p.Hostname) != "" {
		p.Hostname = NormalizeHost(p.Hostname)
	}
	p.ServerInstanceID = strings.TrimSpace(p.ServerInstanceID)
	p.OperatorRegistryFile = strings.TrimSpace(p.OperatorRegistryFile)
	p.CAFile = strings.TrimSpace(p.CAFile)
	var err error
	if p.APIURL, err = canonicalEndpoint(p.APIURL); err != nil {
		return Profile{}, fmt.Errorf("profile %q API URL: %w", p.Name, err)
	}
	if p.WebURL, err = canonicalEndpoint(p.WebURL); err != nil {
		return Profile{}, fmt.Errorf("profile %q web URL: %w", p.Name, err)
	}
	if p.NativeAPIURL != "" {
		if p.NativeAPIURL, err = canonicalEndpoint(p.NativeAPIURL); err != nil {
			return Profile{}, fmt.Errorf("profile %q native API URL: %w", p.Name, err)
		}
	}
	if p.Hostname == "" {
		parsed, _ := url.Parse(p.APIURL)
		p.Hostname = parsed.Hostname()
	}
	if err := p.Validate(); err != nil {
		return Profile{}, err
	}
	return p, nil
}

// ServerHandshake is the credential-free identity returned by /api/v1/meta.
// A saved profile must never silently follow a different self-hosted server.
type ServerHandshake struct {
	ServerInstanceID string `json:"server_instance_id"`
	APIURL           string `json:"api_url"`
	NativeAPIURL     string `json:"native_api_url"`
	WebURL           string `json:"web_url"`
}

func ValidateServerHandshake(profile Profile, handshake ServerHandshake) error {
	profile, err := profile.Normalized()
	if err != nil {
		return err
	}
	if profile.Kind != ProfileKindHosted {
		return fmt.Errorf("profile %q does not use the self-hosted handshake", profile.Name)
	}
	handshake.ServerInstanceID = strings.TrimSpace(handshake.ServerInstanceID)
	apiURL, apiErr := canonicalEndpoint(handshake.APIURL)
	nativeURL, nativeErr := canonicalEndpoint(handshake.NativeAPIURL)
	webURL, webErr := canonicalEndpoint(handshake.WebURL)
	if apiErr != nil || nativeErr != nil || webErr != nil {
		return fmt.Errorf("profile %q server handshake contains invalid public endpoints", profile.Name)
	}
	if handshake.ServerInstanceID != profile.ServerInstanceID || apiURL != profile.APIURL || nativeURL != profile.NativeAPIURL || webURL != profile.WebURL {
		return fmt.Errorf("profile %q server handshake does not match the saved immutable server realm", profile.Name)
	}
	return nil
}

func (p Profile) APIOrigin() string {
	u, err := url.Parse(p.APIURL)
	if err != nil {
		return ""
	}
	u.Path, u.RawPath, u.RawQuery, u.Fragment = "", "", "", ""
	return u.String()
}

// RealmKey binds credentials to the profile, immutable server identity and
// full canonical API base (including path). Redirect safety compares origins,
// but credential persistence must not silently follow an API path change.
func (p Profile) RealmKey() string {
	material := p.Name + "\x00" + p.ServerInstanceID + "\x00" + p.APIURL
	digest := sha256.Sum256([]byte(material))
	return "profile:" + p.Name + ":" + hex.EncodeToString(digest[:16])
}

func BuiltinGitHubProfile(host string) Profile {
	host = NormalizeHost(host)
	apiURL := "https://" + host + "/api/v3"
	webURL := "https://" + host
	if host == "github.com" {
		apiURL = "https://api.github.com"
		webURL = "https://github.com"
	}
	profile, _ := (Profile{
		Name: DefaultProfileName, Kind: ProfileKindGitHub, Hostname: host,
		APIURL: apiURL, WebURL: webURL, ServerInstanceID: "github:" + host,
	}).Normalized()
	return profile
}

func legacyAPIProfile(raw string) (Profile, error) {
	apiURL, err := canonicalEndpoint(raw)
	if err != nil {
		return Profile{}, fmt.Errorf("%s: %w", GitHubBackendAPIURLEnv, err)
	}
	u, _ := url.Parse(apiURL)
	origin := u.Scheme + "://" + u.Host
	digest := sha256.Sum256([]byte(apiURL))
	return (Profile{
		Name: "legacy-api-url", Kind: ProfileKindHosted, Hostname: u.Hostname(), APIURL: apiURL,
		NativeAPIURL: origin + "/api/v1", WebURL: origin,
		ServerInstanceID: "ephemeral-" + hex.EncodeToString(digest[:8]), Ephemeral: true,
	}).Normalized()
}

// ResolveProfile applies explicit name, ISSUE_SPEC_PROFILE, legacy URL,
// a repository-root GitHub project marker, saved default, then the built-in
// GitHub profile in that order.
func ResolveProfile(name, host string) (Profile, string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		cwd = ""
	}
	return ResolveProfileAt(name, host, cwd)
}

// ResolveProfileAt is ResolveProfile with an explicit working directory. It is
// primarily useful for callers and tests that need deterministic project
// discovery without changing the process working directory.
func ResolveProfileAt(name, host, cwd string) (Profile, string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = ProfileNameFromEnv()
	}
	if name == "" {
		if raw := strings.TrimSpace(os.Getenv(GitHubBackendAPIURLEnv)); raw != "" {
			profile, err := legacyAPIProfile(raw)
			return profile, "env:" + GitHubBackendAPIURLEnv, err
		}
	}
	profiles, err := readProfileFile()
	if err != nil {
		return Profile{}, "", err
	}
	if name == "" {
		projectProfile, found, err := projectGitHubProfile(cwd)
		if err != nil {
			return Profile{}, "", err
		}
		if found {
			return BuiltinGitHubProfile(projectProfile.Hostname), "project", nil
		}
		name = strings.TrimSpace(profiles.DefaultProfile)
	}
	if name == "" {
		name = DefaultProfileName
	}
	if profile, ok := profiles.Profiles[name]; ok {
		if strings.TrimSpace(profile.Name) != name {
			return Profile{}, "", fmt.Errorf("profile map key %q does not match embedded name %q", name, profile.Name)
		}
		normalized, err := profile.Normalized()
		return normalized, "config", err
	}
	if name == DefaultProfileName {
		return BuiltinGitHubProfile(host), "builtin", nil
	}
	return Profile{}, "", fmt.Errorf("profile %q is not configured", name)
}

type projectProfileConfig struct {
	Version  int    `json:"version"`
	Profile  string `json:"profile"`
	Hostname string `json:"hostname"`
}

// projectGitHubProfile reads only the repository-root marker. This narrow
// delivery deliberately activates only the credential-free built-in GitHub
// profile; named self-hosted profiles remain explicit/global configuration.
func projectGitHubProfile(cwd string) (projectProfileConfig, bool, error) {
	root, ok := repositoryRoot(cwd)
	if !ok {
		return projectProfileConfig{}, false, nil
	}
	path := filepath.Join(root, ".issue-spec", "config.json")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return projectProfileConfig{}, false, nil
	}
	if err != nil {
		return projectProfileConfig{}, false, fmt.Errorf("read project profile config %s: %w", path, err)
	}
	var config projectProfileConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return projectProfileConfig{}, false, fmt.Errorf("parse project profile config %s: %w", path, err)
	}
	if strings.TrimSpace(config.Profile) != DefaultProfileName {
		return projectProfileConfig{}, false, nil
	}
	if NormalizeHost(config.Hostname) != "github.com" {
		return projectProfileConfig{}, false, fmt.Errorf("project profile config %s selects github with unsupported hostname %q", path, config.Hostname)
	}
	return projectProfileConfig{Version: config.Version, Profile: DefaultProfileName, Hostname: "github.com"}, true, nil
}

func repositoryRoot(cwd string) (string, bool) {
	if strings.TrimSpace(cwd) == "" {
		return "", false
	}
	path, err := filepath.Abs(cwd)
	if err != nil {
		return "", false
	}
	for {
		if _, err := os.Lstat(filepath.Join(path, ".git")); err == nil {
			return path, true
		}
		parent := filepath.Dir(path)
		if parent == path {
			return "", false
		}
		path = parent
	}
}

func SaveProfile(profile Profile, makeDefault bool) error {
	profile, err := profile.Normalized()
	if err != nil {
		return err
	}
	if profile.Ephemeral {
		return errors.New("ephemeral legacy API profile cannot be persisted")
	}
	profiles, err := readProfileFile()
	if err != nil {
		return err
	}
	if profiles.Profiles == nil {
		profiles.Profiles = map[string]Profile{}
	}
	profiles.Version = 1
	profiles.Profiles[profile.Name] = profile
	if makeDefault {
		profiles.DefaultProfile = profile.Name
	}
	return writeProfileFile(profiles)
}

func profilePath() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "profiles.json"), nil
}

func readProfileFile() (profileFile, error) {
	path, err := profilePath()
	if err != nil {
		return profileFile{}, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return profileFile{Version: 1, Profiles: map[string]Profile{}}, nil
	}
	if err != nil {
		return profileFile{}, err
	}
	var result profileFile
	if err := json.Unmarshal(data, &result); err != nil {
		return profileFile{}, fmt.Errorf("read profiles %s: %w", path, err)
	}
	if result.Version != 0 && result.Version != 1 {
		return profileFile{}, fmt.Errorf("unsupported profiles file version %d", result.Version)
	}
	if result.Profiles == nil {
		result.Profiles = map[string]Profile{}
	}
	return result, nil
}

func writeProfileFile(profiles profileFile) error {
	path, err := profilePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(profiles, "", "  ")
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".profiles-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(append(data, '\n')); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryName, path)
}

func canonicalEndpoint(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.ContainsAny(raw, "\\\r\n\t") {
		return "", errors.New("must be an absolute http(s) URL without forbidden characters")
	}
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || u.Host == "" || u.Opaque != "" {
		return "", errors.New("must be an absolute http(s) URL")
	}
	u.Scheme = strings.ToLower(u.Scheme)
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", errors.New("must use http or https")
	}
	if u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", errors.New("must not contain userinfo, query, or fragment")
	}
	port := u.Port()
	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return "", errors.New("contains an invalid port")
		}
	}
	host := strings.ToLower(u.Hostname())
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if (u.Scheme == "https" && port == "443") || (u.Scheme == "http" && port == "80") {
		port = ""
	}
	if port != "" {
		host = net.JoinHostPort(strings.Trim(host, "[]"), port)
	}
	u.Host = host
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = strings.TrimRight(u.RawPath, "/")
	if cleaned := pathpkg.Clean("/" + strings.TrimPrefix(u.Path, "/")); cleaned != u.Path && !(cleaned == "/" && u.Path == "") {
		return "", errors.New("path must be canonical")
	}
	return u.String(), nil
}

// sameEndpointOrigin compares canonical HTTP origins. canonicalEndpoint has
// already folded host casing and default ports, so paths can differ without
// weakening the credential boundary while scheme, host, and effective port
// must remain identical.
func sameEndpointOrigin(first, second string) bool {
	firstURL, firstErr := url.Parse(first)
	secondURL, secondErr := url.Parse(second)
	return firstErr == nil && secondErr == nil && firstURL.Scheme == secondURL.Scheme && firstURL.Host == secondURL.Host
}

// SelectProfileBackendWithOptions resolves a named realm before backend and
// token selection. Self-hosted profiles are REST-only and never call gh.
func SelectProfileBackendWithOptions(ctx context.Context, profileName, host string, opts GitHubBackendSelectionOptions) (GitHubBackendSelection, error) {
	profile, source, err := ResolveProfile(profileName, host)
	if err != nil {
		return GitHubBackendSelection{}, err
	}
	if profile.Kind == ProfileKindGitHub && (source == "builtin" || source == "project") {
		selection, err := selectGitHubBackendCore(ctx, profile.Hostname, opts, true)
		if source == "project" {
			selection.SelectionSource = "profile:project"
		}
		selection.Profile = profile
		selection.ProfileSource = source
		selection.Token = withProfile(selection.Token, profile)
		return selection, err
	}
	mode, modeErr := gitHubBackendModeForSelection(opts)
	if modeErr != nil {
		return GitHubBackendSelection{Profile: profile, ProfileSource: source}, modeErr
	}
	if mode == GitHubBackendModeGH {
		selection := selectGHBackend(mode, profile.Hostname, "profile:"+source)
		selection.Profile = profile
		selection.ProfileSource = source
		selection.Token = withProfile(selection.Token, profile)
		if profile.Ephemeral {
			return selection, fmt.Errorf("%s selects an ephemeral self-hosted profile that cannot use the gh backend", GitHubBackendAPIURLEnv)
		}
		if profile.Kind == ProfileKindGitHub {
			return selection, fmt.Errorf("named GitHub profile %q cannot use shared gh credentials; use an origin-bound REST token", profile.Name)
		}
		return selection, fmt.Errorf("self-hosted profile %q cannot use the gh backend", profile.Name)
	}
	token, tokenErr := ResolveProfileToken(ctx, profile)
	selection := selectRESTBackend(mode, profile.Hostname, "profile:"+source, token)
	selection.Profile = profile
	selection.ProfileSource = source
	selection.Token = withProfile(selection.Token, profile)
	if tokenErr != nil {
		selection.Probes = append(selection.Probes, GitHubBackendProbe{Name: GitHubBackendNameREST, Status: "unavailable", Error: tokenErr.Error()})
		return selection, tokenErr
	}
	return selection, nil
}

// IsBuiltinGitHubProfile reports whether legacy host-scoped GitHub credential
// behavior applies. Every other profile uses an isolated realm.
func IsBuiltinGitHubProfile(profile Profile) bool {
	if profile.Kind != ProfileKindGitHub || profile.Name != DefaultProfileName {
		return false
	}
	builtin := BuiltinGitHubProfile(profile.Hostname)
	return profile.APIURL == builtin.APIURL && profile.WebURL == builtin.WebURL &&
		profile.ServerInstanceID == builtin.ServerInstanceID && profile.CAFile == ""
}

func withProfile(token Token, profile Profile) Token {
	token.Profile = profile.Name
	token.ProfileKind = string(profile.Kind)
	token.ServerInstanceID = profile.ServerInstanceID
	token.APIOrigin = profile.APIOrigin()
	return token
}
