package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	keyring "github.com/zalando/go-keyring"
)

const (
	serviceName            = "issue-spec"
	GitHubBackendEnv       = "ISSUE_SPEC_GITHUB_BACKEND"
	GitHubBackendAPIURLEnv = "ISSUE_SPEC_API_URL"
	GitHubBackendNameREST  = "rest"
	GitHubBackendNameGH    = "gh"
	GitHubBackendKindREST  = "rest"
	GitHubBackendKindCLI   = "external-cli"
	GitHubBackendModeAuto  = GitHubBackendMode("auto")
	GitHubBackendModeREST  = GitHubBackendMode("rest")
	GitHubBackendModeGH    = GitHubBackendMode("gh")
)

var ErrNoToken = errors.New("no issue-spec token is available")

type GitHubBackendMode string

type Token struct {
	Value   string                    `json:"-"`
	Source  string                    `json:"source"`
	User    string                    `json:"user,omitempty"`
	Scopes  []string                  `json:"scopes,omitempty"`
	Host    string                    `json:"host"`
	Backend *GitHubBackendDiagnostics `json:"backend,omitempty"`
}

type StoredCredential struct {
	Token string `json:"token"`
}

type GitHubBackendDiagnostics struct {
	Mode            string               `json:"mode"`
	Name            string               `json:"name,omitempty"`
	Kind            string               `json:"kind,omitempty"`
	Host            string               `json:"host"`
	SelectionSource string               `json:"selection_source,omitempty"`
	TokenSource     string               `json:"token_source,omitempty"`
	Probes          []GitHubBackendProbe `json:"probes,omitempty"`
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
}

type GitHubBackendSelectionOptions struct {
	GHAuthenticated func(context.Context, string) error
	Mode            *GitHubBackendMode
}

type credentialFile struct {
	Hosts map[string]StoredCredential `json:"hosts"`
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
	host = NormalizeHost(host)
	mode, err := gitHubBackendModeForSelection(opts)
	selection := GitHubBackendSelection{Mode: mode, Host: host}
	if err != nil {
		return selection, err
	}

	if mode == GitHubBackendModeGH {
		selection = selectGHBackend(mode, host, "override:gh")
		if customAPIURLActive() {
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
	if customAPIURLActive() {
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
	return GitHubBackendDiagnostics{
		Mode:            string(s.Mode),
		Name:            s.Name,
		Kind:            s.Kind,
		Host:            s.Host,
		SelectionSource: s.SelectionSource,
		TokenSource:     s.TokenSource,
		Probes:          s.Probes,
	}
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

func EnvTokenActive() string {
	for _, envName := range []string{"ISSUE_SPEC_TOKEN", "GH_TOKEN", "GITHUB_TOKEN"} {
		if strings.TrimSpace(os.Getenv(envName)) != "" {
			return envName
		}
	}
	return ""
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

func ConfigDir() (string, error) {
	if dir := strings.TrimSpace(os.Getenv("ISSUE_SPEC_CONFIG_DIR")); dir != "" {
		return dir, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "issue-spec"), nil
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
		return credentialFile{Hosts: map[string]StoredCredential{}}, nil
	}
	if err != nil {
		return credentialFile{}, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return credentialFile{Hosts: map[string]StoredCredential{}}, nil
	}
	var creds credentialFile
	if err := json.Unmarshal(data, &creds); err != nil {
		return credentialFile{}, fmt.Errorf("read issue-spec credentials %s: %w", path, err)
	}
	if creds.Hosts == nil {
		creds.Hosts = map[string]StoredCredential{}
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
