package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/requirements"
)

type requirementsAPI interface {
	GetNativeServerMetadata(context.Context) (github.NativeServerMetadata, error)
	GetNativeContext(context.Context) (github.NativeContext, error)
	ListNativeContextRepositories(context.Context, string) (github.NativeRepositoriesContext, error)
	GetUser(context.Context) (github.User, []string, error)
}

type requirementsClients struct {
	compatibility *github.Client
	native        *github.Client
}

func defaultNewRequirementsAPI(profile auth.Profile, token string) (requirementsAPI, error) {
	compatibility, err := github.NewClientWithOptions(github.ClientOptions{Host: profile.Hostname, BaseURL: profile.APIURL, Token: token, CAFile: profile.CAFile})
	if err != nil {
		return nil, err
	}
	native, err := github.NewClientWithOptions(github.ClientOptions{Host: profile.Hostname, BaseURL: profile.NativeAPIURL, Token: token, CAFile: profile.CAFile})
	if err != nil {
		return nil, err
	}
	return &requirementsClients{compatibility: compatibility, native: native}, nil
}

func (c *requirementsClients) GetNativeServerMetadata(ctx context.Context) (github.NativeServerMetadata, error) {
	return c.native.GetNativeServerMetadata(ctx)
}

func (c *requirementsClients) GetNativeContext(ctx context.Context) (github.NativeContext, error) {
	return c.native.GetNativeContext(ctx)
}

func (c *requirementsClients) ListNativeContextRepositories(ctx context.Context, organizationID string) (github.NativeRepositoriesContext, error) {
	return c.native.ListNativeContextRepositories(ctx, organizationID)
}

func (c *requirementsClients) GetUser(ctx context.Context) (github.User, []string, error) {
	return c.compatibility.GetUser(ctx)
}

func (a *app) runRequirements(ctx context.Context, args []string) int {
	if len(args) == 0 {
		a.errorf("usage: issue-spec requirements setup --server URL [--token-stdin] [--yes] [--json]\n")
		a.errorf("       issue-spec requirements status [--repo owner/repo] [--json]\n")
		return 2
	}
	switch args[0] {
	case "setup":
		return a.runRequirementsSetup(ctx, args[1:])
	case "status":
		return a.runRequirementsStatus(ctx, args[1:])
	default:
		a.errorf("unknown requirements command %q\n", args[0])
		return 2
	}
}

type requirementsRepositoryAccess struct {
	User           github.User
	Scopes         []string
	Repository     github.NativeRepositoryContext
	AllowedActions []string
	CanContribute  bool
}

type requirementsStatusResult struct {
	OK                     bool     `json:"ok"`
	Profile                string   `json:"profile"`
	ServerInstanceID       string   `json:"server_instance_id"`
	APIOrigin              string   `json:"api_origin"`
	Repository             string   `json:"repository,omitempty"`
	User                   string   `json:"user"`
	CredentialSource       string   `json:"credential_source"`
	Scopes                 []string `json:"scopes"`
	Visibility             string   `json:"visibility,omitempty"`
	ContributionPolicy     string   `json:"contribution_policy,omitempty"`
	EffectivePermission    string   `json:"effective_permission,omitempty"`
	AllowedActions         []string `json:"allowed_actions,omitempty"`
	CanContribute          bool     `json:"can_contribute,omitempty"`
	ReadOnly               bool     `json:"read_only,omitempty"`
	SearchAvailable        bool     `json:"search_available"`
	RequirementsOnboarding bool     `json:"requirements_onboarding"`
}

type requirementsSetupResult struct {
	OK               bool   `json:"ok"`
	Applied          bool   `json:"applied"`
	Profile          string `json:"profile"`
	ProfileCreated   bool   `json:"profile_created"`
	ServerInstanceID string `json:"server_instance_id"`
	APIOrigin        string `json:"api_origin"`
	TokenStored      bool   `json:"token_stored"`
	ContextChanged   bool   `json:"context_changed"`
	ContextPath      string `json:"context_path"`
	User             string `json:"user"`
}

func (a *app) runRequirementsSetup(ctx context.Context, args []string) int {
	fs := newFlagSet("requirements setup", a.err)
	serverFlag := fs.String("server", "", "self-hosted issue-spec server URL")
	profileFlag := fs.String("profile", a.profileName, "origin-bound profile name")
	tokenStdin := fs.Bool("token-stdin", false, "read the PAT from protected stdin")
	yes := fs.Bool("yes", false, "apply the displayed profile, context, and PAT plan")
	jsonOut := fs.Bool("json", false, "write JSON output")
	if ok, code := a.parseFlagSet(fs, args); !ok {
		return code
	}
	if *tokenStdin && a.stdinIsTerminal(a.in) {
		a.errorf("--token-stdin refuses terminal input; omit --token-stdin to use the hidden PAT prompt\n")
		return 1
	}
	serverRoot, host, err := canonicalRequirementsServer(*serverFlag)
	if err != nil {
		a.errorf("--server: %v\n", err)
		return 2
	}
	discovery, err := github.NewClientWithOptions(github.ClientOptions{Host: host, BaseURL: serverRoot + "/api/v1"})
	if err != nil {
		a.errorf("configure credential-free server discovery: %v\n", err)
		return 1
	}
	metadata, err := discovery.GetNativeServerMetadata(ctx)
	if err != nil {
		a.errorf("discover requirements server: %v\n", err)
		return 1
	}
	if !metadata.Features.RequirementsOnboarding {
		a.errorf("server does not advertise the requirements_onboarding feature\n")
		return 1
	}
	profileName := strings.TrimSpace(*profileFlag)
	if profileName == "" {
		profileName = defaultRequirementsProfileName(host)
	}
	candidate, err := (auth.Profile{Name: profileName, Kind: auth.ProfileKindHosted, Hostname: host,
		APIURL: metadata.APIURL, NativeAPIURL: metadata.NativeAPIURL, WebURL: metadata.WebURL,
		ServerInstanceID: metadata.ServerInstanceID}).Normalized()
	if err != nil {
		a.errorf("validate discovered server profile: %v\n", err)
		return 1
	}
	if candidate.APIOrigin() != serverRoot {
		a.errorf("discovered server endpoints do not match the requested server origin\n")
		return 1
	}
	existing, source, resolveErr := auth.ResolveProfile(profileName, "")
	profileCreated := false
	switch {
	case resolveErr == nil && source == "config":
		if err := validateRequirementsHandshake(existing, metadata); err != nil {
			a.errorf("requirements profile realm mismatch: %v\n", err)
			return 1
		}
		candidate = existing
	case resolveErr == nil:
		a.errorf("profile %q already resolves from %s and cannot be replaced by requirements setup\n", profileName, source)
		return 1
	case !isProfileNotConfigured(resolveErr, profileName):
		a.errorf("resolve requirements profile: %v\n", resolveErr)
		return 1
	default:
		profileCreated = true
	}

	token, tokenWasStored, err := a.requirementsSetupToken(ctx, candidate, *tokenStdin)
	if err != nil {
		a.errorf("acquire requirements PAT: %v\n", err)
		return 1
	}
	api, err := a.newRequirementsAPI(candidate, token)
	if err != nil {
		a.errorf("configure requirements clients: %v\n", safeRequirementsError(err, token))
		return 1
	}
	user, _, err := api.GetUser(ctx)
	if err != nil {
		a.errorf("validate requirements PAT identity: %v\n", safeRequirementsError(err, token))
		return 1
	}
	if strings.TrimSpace(user.Login) == "" {
		a.errorf("validate requirements PAT identity: response has no login\n")
		return 1
	}
	contextPath, err := requirements.ContextPath()
	if err != nil {
		a.errorf("resolve requirements context path: %v\n", err)
		return 1
	}
	result := requirementsSetupResult{OK: true, Applied: false, Profile: candidate.Name, ProfileCreated: profileCreated,
		ServerInstanceID: candidate.ServerInstanceID, APIOrigin: candidate.APIOrigin(), ContextPath: contextPath, User: user.Login}
	if !*yes {
		if *jsonOut {
			return a.outputJSON(result)
		}
		printRequirementsSetupPreview(a.out, result)
		fmt.Fprintln(a.out, "preview only; rerun with --yes to apply these sequential, idempotent steps")
		return 0
	}
	if profileCreated {
		if err := a.saveRequirementsProfile(candidate, false); err != nil {
			a.errorf("save requirements profile %s: %v\n", candidate.Name, safeRequirementsError(err, token))
			return 1
		}
	}
	if !tokenWasStored {
		if _, err := a.storeRequirementsToken(ctx, candidate, token, false); err != nil {
			a.errorf("store PAT in OS keyring for profile %s failed; no plaintext credential was written: %v\n", candidate.Name, keyringRequirementsError(err, token))
			return 1
		}
		result.TokenStored = true
	}
	contextChanged, err := requirements.SaveActiveContext(requirements.ActiveContext{Profile: candidate.Name,
		ServerInstanceID: candidate.ServerInstanceID})
	if err != nil {
		a.errorf("save requirements context: %v\n", safeRequirementsError(err, token))
		return 1
	}
	result.ContextChanged = contextChanged
	result.Applied = true
	if *jsonOut {
		return a.outputJSON(result)
	}
	printRequirementsSetupPreview(a.out, result)
	fmt.Fprintf(a.out, "setup applied; profile_created=%t token_stored=%t context_changed=%t\n",
		result.ProfileCreated, result.TokenStored, result.ContextChanged)
	return 0
}

func (a *app) runRequirementsStatus(ctx context.Context, args []string) int {
	fs := newFlagSet("requirements status", a.err)
	repoFlag := fs.String("repo", "", "optional repository owner/name for live authorization")
	jsonOut := fs.Bool("json", false, "write JSON output")
	if ok, code := a.parseFlagSet(fs, args); !ok {
		return code
	}
	repo := ""
	if strings.TrimSpace(*repoFlag) != "" {
		var ok bool
		repo, ok = a.validateRepo(*repoFlag)
		if !ok {
			return 2
		}
	}
	configured, err := requirements.LoadActiveContext()
	if err != nil {
		a.errorf("load requirements context: %v\n", err)
		return 1
	}
	profile, source, err := auth.ResolveProfile(configured.Profile, "")
	if err != nil || source != "config" {
		if err == nil {
			err = fmt.Errorf("profile resolves from %s, not saved config", source)
		}
		a.errorf("resolve requirements profile %s: %v\n", configured.Profile, err)
		return 1
	}
	if profile.ServerInstanceID != configured.ServerInstanceID {
		a.errorf("requirements context realm does not match profile %s\n", profile.Name)
		return 1
	}
	discovery, err := github.NewClientWithOptions(github.ClientOptions{Host: profile.Hostname, BaseURL: profile.NativeAPIURL, CAFile: profile.CAFile})
	if err != nil {
		a.errorf("configure credential-free server discovery: %v\n", err)
		return 1
	}
	metadata, err := discovery.GetNativeServerMetadata(ctx)
	if err != nil {
		a.errorf("discover requirements server: %v\n", err)
		return 1
	}
	if err := validateRequirementsHandshake(profile, metadata); err != nil {
		a.errorf("requirements profile realm mismatch: %v\n", err)
		return 1
	}
	if !metadata.Features.RequirementsOnboarding {
		a.errorf("server no longer advertises the requirements_onboarding feature\n")
		return 1
	}
	token, err := a.resolveRequirementsToken(ctx, profile)
	if err != nil {
		a.errorf("resolve requirements PAT: %v\n", err)
		return 1
	}
	api, err := a.newRequirementsAPI(profile, token.Value)
	if err != nil {
		a.errorf("configure requirements clients: %v\n", safeRequirementsError(err, token.Value))
		return 1
	}
	result := requirementsStatusResult{OK: true, Profile: profile.Name, ServerInstanceID: profile.ServerInstanceID,
		APIOrigin: profile.APIOrigin(), CredentialSource: token.Source, SearchAvailable: metadata.Features.Search,
		RequirementsOnboarding: metadata.Features.RequirementsOnboarding}
	if repo != "" {
		access, accessErr := resolveRequirementsRepositoryAccess(ctx, api, repo)
		if accessErr != nil {
			a.errorf("validate requirements access: %v\n", safeRequirementsError(accessErr, token.Value))
			return 1
		}
		result.User = access.User.Login
		result.Scopes = access.Scopes
		result.Repository = repo
		result.Visibility = access.Repository.Repository.Visibility
		result.ContributionPolicy = access.Repository.Repository.ContributionPolicy
		result.EffectivePermission = access.Repository.EffectivePermission
		result.AllowedActions = access.AllowedActions
		result.CanContribute = access.CanContribute
		result.ReadOnly = !access.CanContribute
	} else {
		user, scopes, identityErr := api.GetUser(ctx)
		if identityErr != nil {
			a.errorf("validate requirements PAT identity: %v\n", safeRequirementsError(identityErr, token.Value))
			return 1
		}
		if strings.TrimSpace(user.Login) == "" {
			a.errorf("validate requirements PAT identity: response has no login\n")
			return 1
		}
		result.User = user.Login
		result.Scopes = scopes
	}
	if *jsonOut {
		return a.outputJSON(result)
	}
	fmt.Fprintf(a.out, "profile: %s\nserver instance: %s\nAPI origin: %s\nuser: %s\ncredential source: %s\n",
		result.Profile, result.ServerInstanceID, result.APIOrigin, result.User, result.CredentialSource)
	fmt.Fprintf(a.out, "scopes: %s\nfeatures: requirements_onboarding=%t search=%t\n",
		strings.Join(result.Scopes, ", "), result.RequirementsOnboarding, result.SearchAvailable)
	if result.Repository != "" {
		fmt.Fprintf(a.out, "repository: %s\nvisibility: %s\ncontribution policy: %s\neffective permission: %s\nallowed actions: %s\n",
			result.Repository, result.Visibility, result.ContributionPolicy, result.EffectivePermission, strings.Join(result.AllowedActions, ", "))
		if result.ReadOnly {
			fmt.Fprintln(a.out, "mode: read-only (allowed_actions does not include contribute)")
		} else {
			fmt.Fprintln(a.out, "mode: contribute")
		}
	}
	return 0
}

func (a *app) requirementsSetupToken(ctx context.Context, profile auth.Profile, fromStdin bool) (string, bool, error) {
	if !fromStdin {
		stored, err := a.resolveRequirementsToken(ctx, profile)
		if err == nil && strings.TrimSpace(stored.Value) != "" {
			return stored.Value, true, nil
		}
		if err != nil && !errors.Is(err, auth.ErrNoToken) {
			return "", false, err
		}
	}
	var token string
	if fromStdin {
		data, readErr := io.ReadAll(io.LimitReader(a.in, 1<<20+1))
		if readErr != nil {
			return "", false, readErr
		}
		if len(data) > 1<<20 {
			return "", false, errors.New("stdin PAT exceeds the size limit")
		}
		token = strings.TrimSpace(string(data))
	} else {
		fmt.Fprintf(a.err, "Create a PAT at %s/settings/tokens?mode=requirements, then enter it below.\n", strings.TrimRight(profile.WebURL, "/"))
		var err error
		token, err = a.readRequirementsSecret(a.in, a.err)
		if err != nil {
			return "", false, err
		}
		token = strings.TrimSpace(token)
	}
	if token == "" || strings.ContainsAny(token, "\r\n\x00") {
		return "", false, errors.New("PAT must be one non-empty line")
	}
	return token, false, nil
}

func resolveRequirementsRepositoryAccess(ctx context.Context, api requirementsAPI, repo string) (requirementsRepositoryAccess, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok {
		return requirementsRepositoryAccess{}, errors.New("repository must be owner/name")
	}
	user, scopes, err := api.GetUser(ctx)
	if err != nil {
		return requirementsRepositoryAccess{}, fmt.Errorf("validate PAT identity: %w", err)
	}
	if strings.TrimSpace(user.Login) == "" {
		return requirementsRepositoryAccess{}, errors.New("PAT identity response has no login")
	}
	current, err := api.GetNativeContext(ctx)
	if err != nil {
		return requirementsRepositoryAccess{}, fmt.Errorf("read authenticated server context: %w", err)
	}
	var organizations []github.NativeOrganizationContext
	for _, organization := range current.Organizations {
		if strings.EqualFold(organization.Name, owner) {
			organizations = append(organizations, organization)
		}
	}
	if len(organizations) != 1 || strings.TrimSpace(organizations[0].ID) == "" {
		return requirementsRepositoryAccess{}, fmt.Errorf("repository organization %q is not uniquely visible to this PAT", owner)
	}
	repositories, err := api.ListNativeContextRepositories(ctx, organizations[0].ID)
	if err != nil {
		return requirementsRepositoryAccess{}, fmt.Errorf("read visible repositories: %w", err)
	}
	var matches []github.NativeRepositoryContext
	for _, repository := range repositories.Repositories {
		if strings.EqualFold(repository.Repository.Name, name) {
			matches = append(matches, repository)
		}
	}
	if len(matches) != 1 {
		return requirementsRepositoryAccess{}, fmt.Errorf("repository %q is not uniquely visible to this PAT", repo)
	}
	selected := matches[0]
	if strings.TrimSpace(selected.Repository.ID) == "" || strings.TrimSpace(selected.Repository.Visibility) == "" ||
		strings.TrimSpace(selected.Repository.ContributionPolicy) == "" || strings.TrimSpace(selected.EffectivePermission) == "" {
		return requirementsRepositoryAccess{}, errors.New("repository access response is incomplete")
	}
	actions := append([]string{}, selected.AllowedActions...)
	sort.Strings(actions)
	canContribute := false
	for _, action := range actions {
		if action == "contribute" {
			canContribute = true
			break
		}
	}
	return requirementsRepositoryAccess{User: user, Scopes: append([]string{}, scopes...), Repository: selected,
		AllowedActions: actions, CanContribute: canContribute}, nil
}

func canonicalRequirementsServer(value string) (string, string, error) {
	value = strings.TrimSpace(value)
	parsed, err := url.Parse(value)
	if err != nil || !parsed.IsAbs() || parsed.Host == "" || parsed.Opaque != "" {
		return "", "", errors.New("must be an absolute http(s) origin")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", "", errors.New("must use http or https")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return "", "", errors.New("must be one server origin without path, userinfo, query, or fragment")
	}
	probe, err := (auth.Profile{Name: "requirements-probe", Kind: auth.ProfileKindHosted, Hostname: parsed.Hostname(),
		APIURL: value, NativeAPIURL: strings.TrimRight(value, "/") + "/api/v1", WebURL: value,
		ServerInstanceID: "requirements-probe"}).Normalized()
	if err != nil {
		return "", "", err
	}
	return probe.APIOrigin(), probe.Hostname, nil
}

var requirementsProfileSanitizer = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func defaultRequirementsProfileName(host string) string {
	name := strings.Trim(requirementsProfileSanitizer.ReplaceAllString(strings.ToLower(host), "-"), "-._")
	if name == "" {
		name = "requirements"
	}
	if len(name) > 64 {
		name = name[:64]
	}
	return name
}

func validateRequirementsHandshake(profile auth.Profile, metadata github.NativeServerMetadata) error {
	return auth.ValidateServerHandshake(profile, auth.ServerHandshake{ServerInstanceID: metadata.ServerInstanceID,
		APIURL: metadata.APIURL, NativeAPIURL: metadata.NativeAPIURL, WebURL: metadata.WebURL})
}

func isProfileNotConfigured(err error, name string) bool {
	return err != nil && err.Error() == fmt.Sprintf("profile %q is not configured", name)
}

func safeRequirementsError(err error, secret string) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	if secret != "" {
		message = strings.ReplaceAll(message, secret, "[REDACTED]")
	}
	return errors.New(message)
}

func keyringRequirementsError(err error, secret string) error {
	message := safeRequirementsError(err, secret).Error()
	if before, _, found := strings.Cut(message, "; rerun with --insecure-storage"); found {
		message = before
	}
	return errors.New(message)
}

func printRequirementsSetupPreview(w io.Writer, result requirementsSetupResult) {
	fmt.Fprintf(w, "profile: %s (create=%t)\nserver instance: %s\nAPI origin: %s\nuser: %s\ncontext: %s\n",
		result.Profile, result.ProfileCreated, result.ServerInstanceID, result.APIOrigin, result.User, result.ContextPath)
}
