package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/buildinfo"
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
		a.errorf("usage: issue-spec requirements setup --server URL --repo owner/repo --agent codex|claude [--token-stdin] [--skill-archive PATH --skill-archive-sha256 SHA256] [--skill-conflict cancel|replace|alternate] [--yes] [--json]\n")
		a.errorf("       issue-spec requirements status [--json]\n")
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
	User             github.User
	Scopes           []string
	Repository       github.NativeRepositoryContext
	AllowedActions   []string
	CanContribute    bool
	OrganizationID   string
	OrganizationName string
}

type requirementsStatusResult struct {
	OK                     bool                     `json:"ok"`
	Profile                string                   `json:"profile"`
	ServerInstanceID       string                   `json:"server_instance_id"`
	APIOrigin              string                   `json:"api_origin"`
	Repository             string                   `json:"repository"`
	Agent                  requirements.Target      `json:"agent"`
	User                   string                   `json:"user"`
	CredentialSource       string                   `json:"credential_source"`
	Scopes                 []string                 `json:"scopes"`
	Visibility             string                   `json:"visibility"`
	ContributionPolicy     string                   `json:"contribution_policy"`
	EffectivePermission    string                   `json:"effective_permission"`
	AllowedActions         []string                 `json:"allowed_actions"`
	CanContribute          bool                     `json:"can_contribute"`
	ReadOnly               bool                     `json:"read_only"`
	SearchAvailable        bool                     `json:"search_available"`
	RequirementsOnboarding bool                     `json:"requirements_onboarding"`
	Skill                  requirements.InstallPlan `json:"skill"`
}

type requirementsSetupResult struct {
	OK               bool                        `json:"ok"`
	Applied          bool                        `json:"applied"`
	Profile          string                      `json:"profile"`
	ProfileCreated   bool                        `json:"profile_created"`
	TokenStored      bool                        `json:"token_stored"`
	ContextChanged   bool                        `json:"context_changed"`
	ContextPath      string                      `json:"context_path"`
	Repository       string                      `json:"repository"`
	Agent            requirements.Target         `json:"agent"`
	User             string                      `json:"user"`
	Visibility       string                      `json:"visibility"`
	Policy           string                      `json:"contribution_policy"`
	AllowedActions   []string                    `json:"allowed_actions"`
	CanContribute    bool                        `json:"can_contribute"`
	ReadOnly         bool                        `json:"read_only"`
	SkillPlan        requirements.InstallPlan    `json:"skill_plan"`
	SkillSource      string                      `json:"skill_source"`
	Compatibility    string                      `json:"skill_compatibility"`
	ConflictDecision string                      `json:"skill_conflict_decision"`
	SkillResult      *requirements.InstallResult `json:"skill_result,omitempty"`
}

func (a *app) runRequirementsSetup(ctx context.Context, args []string) int {
	fs := newFlagSet("requirements setup", a.err)
	serverFlag := fs.String("server", "", "self-hosted issue-spec server URL")
	repoFlag := fs.String("repo", "", "repository owner/name")
	agentFlag := fs.String("agent", "", "agent target: codex or claude")
	profileFlag := fs.String("profile", a.profileName, "origin-bound profile name")
	tokenStdin := fs.Bool("token-stdin", false, "read the PAT from protected stdin")
	skillArchive := fs.String("skill-archive", "", "verified standalone requirements skill archive")
	skillArchiveSHA256 := fs.String("skill-archive-sha256", "", "required SHA-256 for --skill-archive")
	skillConflict := fs.String("skill-conflict", "cancel", "user-modified target choice: cancel, replace, or alternate")
	skillAlternateTarget := fs.String("skill-alternate-target", "", "alternate skill directory used with --skill-conflict alternate")
	yes := fs.Bool("yes", false, "apply the displayed profile, context, and skill plan")
	jsonOut := fs.Bool("json", false, "write JSON output")
	if ok, code := a.parseFlagSet(fs, args); !ok {
		return code
	}
	if *tokenStdin && a.stdinIsTerminal(a.in) {
		a.errorf("--token-stdin refuses terminal input; omit --token-stdin to use the hidden PAT prompt\n")
		return 1
	}
	conflictChoice, err := parseRequirementsConflictChoice(*skillConflict)
	if err != nil {
		a.errorf("%v\n", err)
		return 2
	}
	if strings.TrimSpace(*skillArchive) == "" && strings.TrimSpace(*skillArchiveSHA256) != "" {
		a.errorf("--skill-archive-sha256 requires --skill-archive\n")
		return 2
	}
	if strings.TrimSpace(*skillArchive) != "" && strings.TrimSpace(*skillArchiveSHA256) == "" {
		a.errorf("--skill-archive requires --skill-archive-sha256\n")
		return 2
	}
	if conflictChoice == requirements.ConflictAlternate && strings.TrimSpace(*skillAlternateTarget) == "" {
		a.errorf("--skill-conflict alternate requires --skill-alternate-target\n")
		return 2
	}
	if conflictChoice != requirements.ConflictAlternate && strings.TrimSpace(*skillAlternateTarget) != "" {
		a.errorf("--skill-alternate-target requires --skill-conflict alternate\n")
		return 2
	}
	repo, ok := a.validateRepo(*repoFlag)
	if !ok {
		return 2
	}
	agent, err := parseRequirementsAgent(*agentFlag)
	if err != nil {
		a.errorf("%v\n", err)
		return 2
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
	access, err := resolveRequirementsRepositoryAccess(ctx, api, repo)
	if err != nil {
		a.errorf("validate requirements access: %v\n", safeRequirementsError(err, token))
		return 1
	}
	bundle, plan, skillSource, err := a.requirementsInstallPlanFrom(agent, *skillArchive, *skillArchiveSHA256)
	if err != nil {
		a.errorf("preview requirements skill installation: %v\n", err)
		return 1
	}
	userModified := plan.Action == requirements.ActionUserModified
	if userModified {
		switch conflictChoice {
		case requirements.ConflictCancel:
		case requirements.ConflictReplace, requirements.ConflictAlternate:
			plan, err = requirements.ApplyConflictDecision(bundle, plan, requirementsCLIVersion(), conflictChoice, *skillAlternateTarget)
			if err != nil {
				a.errorf("apply requirements skill conflict choice: %v\n", err)
				return 1
			}
			if conflictChoice == requirements.ConflictAlternate && plan.Action == requirements.ActionUserModified {
				a.errorf("alternate requirements skill target %s is also user-modified; choose an empty or managed alternate target\n", plan.Path)
				return 1
			}
		}
	} else if conflictChoice != requirements.ConflictCancel {
		a.errorf("--skill-conflict %s requires a user-modified target\n", conflictChoice)
		return 2
	}
	contextPath, err := requirements.ContextPath()
	if err != nil {
		a.errorf("resolve requirements context path: %v\n", err)
		return 1
	}
	result := requirementsSetupResult{OK: true, Applied: false, Profile: candidate.Name, ProfileCreated: profileCreated,
		ContextPath: contextPath, Repository: repo, Agent: agent, User: access.User.Login,
		Visibility: access.Repository.Repository.Visibility, Policy: access.Repository.Repository.ContributionPolicy,
		AllowedActions: access.AllowedActions, CanContribute: access.CanContribute, ReadOnly: !access.CanContribute, SkillPlan: plan,
		SkillSource: skillSource, Compatibility: "compatible with CLI " + requirementsCLIVersion(), ConflictDecision: requirementsConflictChoiceName(conflictChoice)}
	if !*yes {
		if *jsonOut {
			return a.outputJSON(result)
		}
		printRequirementsSetupPreview(a.out, result)
		fmt.Fprintln(a.out, "preview only; rerun with --yes to apply these sequential, idempotent steps")
		return 0
	}
	if userModified && conflictChoice == requirements.ConflictCancel {
		a.errorf("requirements skill installation cancelled for user-modified target %s; choose replace or alternate explicitly\n", plan.Path)
		return 1
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
		ServerInstanceID: candidate.ServerInstanceID, Repository: repo, Agent: agent})
	if err != nil {
		a.errorf("save requirements context: %v\n", safeRequirementsError(err, token))
		return 1
	}
	result.ContextChanged = contextChanged
	installed, err := a.installRequirements(bundle, plan)
	if err != nil {
		a.errorf("install requirements skill: %v\n", safeRequirementsError(err, token))
		return 1
	}
	result.Applied = true
	result.SkillResult = &installed
	if *jsonOut {
		return a.outputJSON(result)
	}
	printRequirementsSetupPreview(a.out, result)
	fmt.Fprintf(a.out, "setup applied; profile_created=%t token_stored=%t context_changed=%t skill_changed=%t\n",
		result.ProfileCreated, result.TokenStored, result.ContextChanged, installed.Changed)
	if result.ReadOnly {
		fmt.Fprintln(a.out, "repository is read-only for this PAT because allowed_actions does not include contribute")
	}
	return 0
}

func (a *app) runRequirementsStatus(ctx context.Context, args []string) int {
	fs := newFlagSet("requirements status", a.err)
	jsonOut := fs.Bool("json", false, "write JSON output")
	if ok, code := a.parseFlagSet(fs, args); !ok {
		return code
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
	access, err := resolveRequirementsRepositoryAccess(ctx, api, configured.Repository)
	if err != nil {
		a.errorf("validate requirements access: %v\n", safeRequirementsError(err, token.Value))
		return 1
	}
	_, skill, err := a.requirementsInstallPlan(configured.Agent)
	if err != nil {
		a.errorf("inspect requirements skill: %v\n", err)
		return 1
	}
	result := requirementsStatusResult{OK: true, Profile: profile.Name, ServerInstanceID: profile.ServerInstanceID,
		APIOrigin: profile.APIOrigin(), Repository: configured.Repository, Agent: configured.Agent, User: access.User.Login,
		CredentialSource: token.Source, Scopes: access.Scopes, Visibility: access.Repository.Repository.Visibility,
		ContributionPolicy: access.Repository.Repository.ContributionPolicy, EffectivePermission: access.Repository.EffectivePermission,
		AllowedActions: access.AllowedActions, CanContribute: access.CanContribute, ReadOnly: !access.CanContribute,
		SearchAvailable: metadata.Features.Search, RequirementsOnboarding: metadata.Features.RequirementsOnboarding, Skill: skill}
	if *jsonOut {
		return a.outputJSON(result)
	}
	fmt.Fprintf(a.out, "profile: %s\nserver instance: %s\nrepository: %s\nagent: %s\nuser: %s\n",
		result.Profile, result.ServerInstanceID, result.Repository, result.Agent, result.User)
	fmt.Fprintf(a.out, "visibility: %s\ncontribution policy: %s\neffective permission: %s\nallowed actions: %s\n",
		result.Visibility, result.ContributionPolicy, result.EffectivePermission, strings.Join(result.AllowedActions, ", "))
	if result.ReadOnly {
		fmt.Fprintln(a.out, "mode: read-only (allowed_actions does not include contribute)")
	} else {
		fmt.Fprintln(a.out, "mode: contribute")
	}
	fmt.Fprintf(a.out, "skill: %s (%s)\n", result.Skill.Path, result.Skill.Action)
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

func (a *app) requirementsInstallPlan(agent requirements.Target) (requirements.Bundle, requirements.InstallPlan, error) {
	bundle, plan, _, err := a.requirementsInstallPlanFrom(agent, "", "")
	return bundle, plan, err
}

func (a *app) requirementsInstallPlanFrom(agent requirements.Target, archivePath, archiveSHA256 string) (requirements.Bundle, requirements.InstallPlan, string, error) {
	identity := buildinfo.Current()
	var bundle requirements.Bundle
	source := "embedded"
	if strings.TrimSpace(archivePath) != "" {
		absolute, err := filepath.Abs(strings.TrimSpace(archivePath))
		if err != nil {
			return requirements.Bundle{}, requirements.InstallPlan{}, "", fmt.Errorf("resolve requirements skill archive: %w", err)
		}
		raw, err := os.ReadFile(filepath.Clean(absolute))
		if err != nil {
			return requirements.Bundle{}, requirements.InstallPlan{}, "", fmt.Errorf("read requirements skill archive: %w", err)
		}
		bundle, err = requirements.VerifyArchive(raw, archiveSHA256)
		if err != nil {
			return requirements.Bundle{}, requirements.InstallPlan{}, "", err
		}
		source = "archive:" + filepath.Clean(absolute)
	} else {
		distribution := requirements.Distribution{}
		if identity.Channel != "development" {
			distribution = requirements.Distribution{Channel: identity.Channel, SourceRevision: identity.Revision, CLIBuild: identity.Version}
		}
		var err error
		bundle, err = requirements.Canonical(distribution)
		if err != nil {
			return requirements.Bundle{}, requirements.InstallPlan{}, "", err
		}
		if bundle.Manifest.ContentID != identity.RequirementsSkillContentID {
			return requirements.Bundle{}, requirements.InstallPlan{}, "", errors.New("embedded requirements skill does not match the CLI build identity")
		}
	}
	options := requirements.TargetOptions{Home: strings.TrimSpace(os.Getenv("HOME")),
		CodexHome: strings.TrimSpace(os.Getenv("CODEX_HOME")), ClaudeConfigDir: strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR"))}
	plan, err := a.previewRequirementsInstall(bundle, agent, options, requirementsCLIVersion())
	return bundle, plan, source, err
}

func requirementsCLIVersion() string {
	identity := buildinfo.Current()
	if identity.Channel == "development" {
		return "development"
	}
	return identity.Version
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
		AllowedActions: actions, CanContribute: canContribute, OrganizationID: organizations[0].ID,
		OrganizationName: organizations[0].Name}, nil
}

func parseRequirementsAgent(value string) (requirements.Target, error) {
	target := requirements.Target(strings.ToLower(strings.TrimSpace(value)))
	if target != requirements.TargetCodex && target != requirements.TargetClaude {
		return "", errors.New("--agent must be codex or claude")
	}
	return target, nil
}

func parseRequirementsConflictChoice(value string) (requirements.ConflictDecision, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "cancel":
		return requirements.ConflictCancel, nil
	case "replace":
		return requirements.ConflictReplace, nil
	case "alternate":
		return requirements.ConflictAlternate, nil
	default:
		return "", errors.New("--skill-conflict must be cancel, replace, or alternate")
	}
}

func requirementsConflictChoiceName(choice requirements.ConflictDecision) string {
	if choice == requirements.ConflictAlternate {
		return "alternate"
	}
	return string(choice)
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
	fmt.Fprintf(w, "profile: %s (create=%t)\nrepository: %s\nagent: %s\nuser: %s\n", result.Profile, result.ProfileCreated, result.Repository, result.Agent, result.User)
	fmt.Fprintf(w, "visibility: %s\ncontribution policy: %s\nallowed actions: %s\n", result.Visibility, result.Policy, strings.Join(result.AllowedActions, ", "))
	fmt.Fprintf(w, "context: %s\nskill source: %s\nskill compatibility: %s\nskill target: %s\nskill path: %s\nskill action: %s\nskill reason: %s\nskill content ID: %s\nskill conflict decision: %s\n",
		result.ContextPath, result.SkillSource, result.Compatibility, result.SkillPlan.Target, result.SkillPlan.Path,
		result.SkillPlan.Action, result.SkillPlan.Reason, result.SkillPlan.ContentID, result.ConflictDecision)
	if result.ReadOnly {
		fmt.Fprintln(w, "access: read-only (allowed_actions does not include contribute)")
	} else {
		fmt.Fprintln(w, "access: contribute")
	}
}
