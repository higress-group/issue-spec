package commands

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/github"
)

func (a *app) runAuth(ctx context.Context, args []string) int {
	if len(args) == 0 {
		a.errorf("usage: issue-spec auth status|login|logout|token\n")
		return 2
	}
	switch args[0] {
	case "status":
		return a.runAuthStatus(ctx, args[1:])
	case "login":
		return a.runAuthLogin(ctx, args[1:])
	case "logout":
		return a.runAuthLogout(ctx, args[1:])
	case "token":
		return a.runAuthToken(ctx, args[1:])
	default:
		a.errorf("unknown auth command %q\n", args[0])
		return 2
	}
}

func (a *app) runAuthStatus(ctx context.Context, args []string) int {
	fs := newFlagSet("auth status", a.err)
	host := fs.String("hostname", "github.com", "GitHub hostname")
	profile := fs.String("profile", a.profileName, "named issue backend profile")
	jsonOut := fs.Bool("json", false, "write JSON output")
	if ok, code := a.parseFlagSet(fs, args); !ok {
		return code
	}
	client, token, err := a.clientForProfile(ctx, *host, *profile)
	if err != nil {
		if *jsonOut {
			return a.outputJSON(authErrorResult(token, err))
		}
		a.errorf("not authenticated for %s: %v\n", auth.NormalizeHost(*host), err)
		return 1
	}
	user, scopes, err := client.GetUser(ctx)
	if err != nil {
		if *jsonOut {
			return a.outputJSON(authErrorResult(token, err))
		}
		a.errorf("validate token for %s from %s: %v\n", token.Host, token.Source, err)
		return 1
	}
	token.User = user.Login
	token.Scopes = scopes
	if *jsonOut {
		return a.outputJSON(map[string]any{"ok": true, "auth": token, "backend": token.Backend})
	}
	if token.Profile != "" {
		fmt.Fprintf(a.out, "profile: %s (%s)\nserver instance: %s\nAPI origin: %s\n", token.Profile, token.ProfileKind, token.ServerInstanceID, token.APIOrigin)
	}
	fmt.Fprintf(a.out, "host: %s\nuser: %s\ntoken source: %s\n", token.Host, token.User, token.Source)
	if token.Backend != nil {
		fmt.Fprintf(a.out, "github backend: %s (%s)\n", token.Backend.Name, token.Backend.SelectionSource)
	}
	if len(token.Scopes) > 0 {
		fmt.Fprintf(a.out, "scopes: %s\n", strings.Join(token.Scopes, ", "))
	}
	return 0
}

func (a *app) runAuthLogin(ctx context.Context, args []string) int {
	fs := newFlagSet("auth login", a.err)
	host := fs.String("hostname", "github.com", "GitHub hostname")
	profileName := fs.String("profile", a.profileName, "named issue backend profile")
	kind := fs.String("kind", "", "profile kind: github or self-hosted")
	apiURL := fs.String("api-url", "", "GitHub-compatible API base URL")
	nativeAPIURL := fs.String("native-api-url", "", "native API base URL")
	webURL := fs.String("web-url", "", "browser web base URL")
	instanceID := fs.String("instance-id", "", "stable server instance id")
	caFile := fs.String("ca-file", "", "absolute custom CA PEM file")
	makeDefault := fs.Bool("make-default", false, "make this the default profile")
	withToken := fs.Bool("with-token", false, "read token from stdin")
	insecure := fs.Bool("insecure-storage", false, "store token in issue-spec plaintext config when keyring is unavailable or undesired")
	jsonOut := fs.Bool("json", false, "write JSON output")
	if ok, code := a.parseFlagSet(fs, args); !ok {
		return code
	}
	profileFlags := authLoginProfileFlags{
		Name: *profileName, Kind: *kind, APIURL: *apiURL, NativeAPIURL: *nativeAPIURL,
		WebURL: *webURL, InstanceID: *instanceID, CAFile: *caFile, MakeDefault: *makeDefault,
	}
	profile, configured, err := resolveAuthLoginProfile(profileFlags, *host)
	if err != nil {
		a.errorf("resolve profile: %v\n", err)
		return 1
	}
	if !*withToken {
		if !auth.IsBuiltinGitHubProfile(profile) {
			return a.runOriginBoundLoginAdvice(profile, *jsonOut)
		}
		return a.runAuthLoginAdvice(ctx, profile.Hostname, *jsonOut)
	}
	data, err := io.ReadAll(a.in)
	if err != nil {
		a.errorf("read token from stdin: %v\n", err)
		return 1
	}
	tokenValue := strings.TrimSpace(string(data))
	if tokenValue == "" {
		a.errorf("stdin token is empty\n")
		return 1
	}
	if profile.Ephemeral {
		a.errorf("%s is an ephemeral compatibility profile; export ISSUE_SPEC_TOKEN instead of persisting a login\n", auth.GitHubBackendAPIURLEnv)
		return 1
	}
	var client *github.Client
	if profile.Kind == auth.ProfileKindHosted || configured {
		client, err = github.NewClientWithOptions(github.ClientOptions{Host: profile.Hostname, BaseURL: profile.APIURL, Token: tokenValue, CAFile: profile.CAFile})
	} else {
		client = github.NewClient(profile.Hostname, tokenValue)
	}
	if err != nil {
		a.errorf("configure client for profile %s: %v\n", profile.Name, err)
		return 1
	}
	user, scopes, err := client.GetUser(ctx)
	if err != nil {
		a.errorf("validate token for profile %s: %v\n", profile.Name, err)
		return 1
	}
	if configured || profileFlags.MakeDefault {
		if err := auth.SaveProfile(profile, profileFlags.MakeDefault); err != nil {
			a.errorf("save profile %s: %v\n", profile.Name, err)
			return 1
		}
	}
	source, err := auth.StoreProfileToken(ctx, profile, tokenValue, *insecure)
	if err != nil {
		a.errorf("%v\n", err)
		return 1
	}
	result := map[string]any{
		"ok": true, "profile": profile.Name, "profile_kind": profile.Kind,
		"server_instance_id": profile.ServerInstanceID, "api_origin": profile.APIOrigin(),
		"host": profile.Hostname, "user": user.Login, "source": source, "scopes": scopes,
	}
	if *jsonOut {
		return a.outputJSON(result)
	}
	if *insecure {
		fmt.Fprintln(a.err, "warning: token stored in issue-spec plaintext config because --insecure-storage was set")
	}
	fmt.Fprintf(a.out, "logged in to profile %s (%s) as %s using %s storage\n", profile.Name, profile.APIOrigin(), user.Login, source)
	return 0
}

type authLoginProfileFlags struct {
	Name, Kind, APIURL, NativeAPIURL, WebURL, InstanceID, CAFile string
	MakeDefault                                                  bool
}

func (f authLoginProfileFlags) configProvided() bool {
	return strings.TrimSpace(f.Kind) != "" || strings.TrimSpace(f.APIURL) != "" || strings.TrimSpace(f.NativeAPIURL) != "" ||
		strings.TrimSpace(f.WebURL) != "" || strings.TrimSpace(f.InstanceID) != "" || strings.TrimSpace(f.CAFile) != ""
}

func resolveAuthLoginProfile(flags authLoginProfileFlags, host string) (auth.Profile, bool, error) {
	if !flags.configProvided() {
		profile, _, err := auth.ResolveProfile(flags.Name, host)
		return profile, false, err
	}
	name := strings.TrimSpace(flags.Name)
	if name == "" {
		return auth.Profile{}, false, errors.New("--profile is required when configuring profile fields")
	}
	kind := auth.ProfileKind(strings.ToLower(strings.TrimSpace(flags.Kind)))
	if kind == "" {
		kind = auth.ProfileKindHosted
	}
	if kind == auth.ProfileKindGitHub {
		profile := auth.BuiltinGitHubProfile(host)
		profile.Name = name
		if flags.APIURL != "" {
			profile.APIURL = flags.APIURL
		}
		if flags.WebURL != "" {
			profile.WebURL = flags.WebURL
		}
		if flags.InstanceID != "" {
			profile.ServerInstanceID = flags.InstanceID
		}
		profile.CAFile = flags.CAFile
		profile, err := profile.Normalized()
		return profile, true, err
	}
	profile := auth.Profile{
		Name: name, Kind: kind, APIURL: flags.APIURL,
		NativeAPIURL: flags.NativeAPIURL, WebURL: flags.WebURL,
		ServerInstanceID: flags.InstanceID, CAFile: flags.CAFile,
	}
	profile, err := profile.Normalized()
	return profile, true, err
}

func (a *app) runOriginBoundLoginAdvice(profile auth.Profile, jsonOut bool) int {
	command := fmt.Sprintf("issue-spec auth login --profile %s --with-token", profile.Name)
	result := map[string]any{
		"ok": true, "profile": profile.Name, "profile_kind": profile.Kind,
		"server_instance_id": profile.ServerInstanceID, "api_origin": profile.APIOrigin(),
		"mode": "origin-bound-token", "message": "This profile requires an explicit origin-bound issue-spec token.",
		"next_steps": []string{command},
	}
	if jsonOut {
		return a.outputJSON(result)
	}
	fmt.Fprintf(a.out, "Profile %s requires an explicit origin-bound issue-spec token.\n  %s\n", profile.Name, command)
	return 0
}

type authLoginAdvice struct {
	OK               bool     `json:"ok"`
	Host             string   `json:"host"`
	Backend          string   `json:"backend"`
	Mode             string   `json:"mode"`
	GitHubCLI        ghAdvice `json:"github_cli"`
	Message          string   `json:"message"`
	NextSteps        []string `json:"next_steps"`
	RESTLoginCommand string   `json:"rest_login_command,omitempty"`
	GHLoginCommand   string   `json:"gh_login_command,omitempty"`
	GHDownloadURL    string   `json:"gh_download_url,omitempty"`
}

type ghAdvice struct {
	Installed     bool   `json:"installed"`
	Authenticated bool   `json:"authenticated"`
	Error         string `json:"error,omitempty"`
}

const ghDownloadURL = "https://cli.github.com/"
const ghNotAuthenticatedError = "not_authenticated"

func (a *app) runAuthLoginAdvice(ctx context.Context, host string, jsonOut bool) int {
	advice := buildAuthLoginAdvice(ctx, host)
	if jsonOut {
		return a.outputJSON(advice)
	}
	fmt.Fprintln(a.out, advice.Message)
	for _, step := range advice.NextSteps {
		fmt.Fprintf(a.out, "  %s\n", step)
	}
	return 0
}

func buildAuthLoginAdvice(ctx context.Context, host string) authLoginAdvice {
	host = auth.NormalizeHost(host)
	restLoginCommand := issueSpecAuthLoginWithTokenCommand(host)
	statusCommand := issueSpecAuthStatusJSONCommand(host)
	ghLoginCommand := ghAuthLoginCommand(host)
	if _, err := ghLookPath("gh"); err != nil {
		return authLoginAdvice{
			OK:               true,
			Host:             host,
			Backend:          auth.GitHubBackendNameREST,
			Mode:             "rest-fallback",
			GitHubCLI:        ghAdvice{Installed: false},
			Message:          fmt.Sprintf("GitHub CLI was not found. issue-spec is using the fallback REST token login mode for %s.", host),
			NextSteps:        []string{restLoginCommand, "Install GitHub CLI from " + ghDownloadURL + " for the complete local workflow experience."},
			RESTLoginCommand: restLoginCommand,
			GHDownloadURL:    ghDownloadURL,
		}
	}

	if err := ghAuthenticated(ctx, host); err != nil {
		return authLoginAdvice{
			OK:               true,
			Host:             host,
			Backend:          auth.GitHubBackendNameGH,
			Mode:             "gh-needs-auth",
			GitHubCLI:        ghAdvice{Installed: true, Authenticated: false, Error: ghNotAuthenticatedError},
			Message:          fmt.Sprintf("GitHub CLI is installed but is not authenticated for %s. Authenticate gh first, then issue-spec can reuse that login.", host),
			NextSteps:        []string{ghLoginCommand, statusCommand, "For the REST token storage path instead, run: " + restLoginCommand},
			RESTLoginCommand: restLoginCommand,
			GHLoginCommand:   ghLoginCommand,
		}
	}

	return authLoginAdvice{
		OK:               true,
		Host:             host,
		Backend:          auth.GitHubBackendNameGH,
		Mode:             "gh-reuse",
		GitHubCLI:        ghAdvice{Installed: true, Authenticated: true},
		Message:          fmt.Sprintf("GitHub CLI is installed and authenticated for %s. issue-spec can reuse your gh CLI login directly; no issue-spec token login is required.", host),
		NextSteps:        []string{statusCommand, "For the REST token storage path instead, run: " + restLoginCommand},
		RESTLoginCommand: restLoginCommand,
	}
}

func issueSpecAuthLoginWithTokenCommand(host string) string {
	if isDefaultGitHubHost(host) {
		return "issue-spec auth login --with-token"
	}
	return fmt.Sprintf("issue-spec auth login --hostname %s --with-token", host)
}

func issueSpecAuthStatusJSONCommand(host string) string {
	if isDefaultGitHubHost(host) {
		return "issue-spec auth status --json"
	}
	return fmt.Sprintf("issue-spec auth status --hostname %s --json", host)
}

func ghAuthLoginCommand(host string) string {
	if isDefaultGitHubHost(host) {
		return "gh auth login"
	}
	return fmt.Sprintf("gh auth login --hostname %s", host)
}

func isDefaultGitHubHost(host string) bool {
	return strings.EqualFold(auth.NormalizeHost(host), "github.com")
}

func (a *app) runAuthLogout(ctx context.Context, args []string) int {
	fs := newFlagSet("auth logout", a.err)
	host := fs.String("hostname", "github.com", "GitHub hostname")
	profileName := fs.String("profile", a.profileName, "named issue backend profile")
	jsonOut := fs.Bool("json", false, "write JSON output")
	if ok, code := a.parseFlagSet(fs, args); !ok {
		return code
	}
	profile, _, err := auth.ResolveProfile(*profileName, *host)
	if err != nil {
		a.errorf("resolve profile: %v\n", err)
		return 1
	}
	hostName := profile.Hostname
	err = auth.DeleteProfileToken(ctx, profile)
	envActive := auth.EnvTokenActiveForProfile(profile)
	if err != nil {
		a.errorf("logout %s: %v\n", hostName, err)
		return 1
	}
	result := map[string]any{"ok": true, "profile": profile.Name, "profile_kind": profile.Kind, "server_instance_id": profile.ServerInstanceID, "api_origin": profile.APIOrigin(), "host": hostName, "env_token_active": envActive}
	if *jsonOut {
		return a.outputJSON(result)
	}
	fmt.Fprintf(a.out, "removed persisted issue-spec token for profile %s (%s)\n", profile.Name, hostName)
	if envActive != "" {
		fmt.Fprintf(a.out, "environment token %s is still active and was not unset\n", envActive)
	}
	return 0
}

func (a *app) runAuthToken(ctx context.Context, args []string) int {
	fs := newFlagSet("auth token", a.err)
	host := fs.String("hostname", "github.com", "GitHub hostname")
	profile := fs.String("profile", a.profileName, "named issue backend profile")
	plain := fs.Bool("plain", false, "print token in plain text")
	jsonOut := fs.Bool("json", false, "write JSON output")
	includeToken := fs.Bool("include-token", false, "include token in JSON output")
	if ok, code := a.parseFlagSet(fs, args); !ok {
		return code
	}
	selection, err := a.selectBackendForProfile(ctx, *host, *profile)
	if err != nil {
		token := selection.TokenWithDiagnostics()
		if *jsonOut {
			return a.outputJSON(authErrorResult(token, err))
		}
		if errors.Is(err, auth.ErrNoToken) {
			a.errorf("not authenticated for %s\n", auth.NormalizeHost(*host))
		} else {
			a.errorf("resolve token: %v\n", err)
		}
		return 1
	}
	token := selection.TokenWithDiagnostics()
	if !*plain && !*jsonOut {
		a.errorf("refusing to print token without --plain\n")
		return 2
	}
	if *jsonOut {
		out := map[string]any{
			"host": token.Host, "profile": token.Profile, "profile_kind": token.ProfileKind,
			"server_instance_id": token.ServerInstanceID, "api_origin": token.APIOrigin,
			"source": token.Source, "backend": token.Backend,
		}
		if *includeToken {
			tokenValue, err := a.tokenForSelection(ctx, selection)
			if err != nil {
				return a.outputJSON(authErrorResult(token, err))
			}
			out["token"] = tokenValue
		}
		return a.outputJSON(out)
	}
	tokenValue, err := a.tokenForSelection(ctx, selection)
	if err != nil {
		a.errorf("resolve token: %v\n", err)
		return 1
	}
	fmt.Fprintln(a.out, tokenValue)
	return 0
}

var _ = flag.ContinueOnError

func authErrorResult(token auth.Token, err error) map[string]any {
	result := map[string]any{
		"ok":    false,
		"host":  token.Host,
		"error": err.Error(),
	}
	if token.Source != "" {
		result["source"] = token.Source
	}
	if token.Backend != nil {
		result["backend"] = token.Backend
	}
	return result
}
