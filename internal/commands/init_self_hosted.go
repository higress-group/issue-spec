package commands

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/codereview"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/server/models"
	"github.com/higress-group/issue-spec/internal/workflow"
	"gopkg.in/yaml.v3"
)

const (
	selfHostedInitConfigVersion  = 2
	selfHostedInitJournalVersion = 1
)

type selfHostedInitOptions struct {
	Repo, ServerOrg, ServerRepo                    string
	ProviderKey, ExternalRepo, SourceRemote        string
	SourceCloneURL, SourceWebURL, DefaultBranch    string
	Tools, Delivery, Language                      string
	CreateIfMissing, BindSource, SkipSourceBinding bool
	Yes, PlanOnly, CreateLabels, JSON              bool
}

func (o selfHostedInitOptions) hasSelfHostedOnlyFlags() bool {
	return strings.TrimSpace(o.ServerOrg) != "" || strings.TrimSpace(o.ServerRepo) != "" ||
		strings.TrimSpace(o.ProviderKey) != "" || strings.TrimSpace(o.ExternalRepo) != "" ||
		strings.TrimSpace(o.SourceRemote) != "" || strings.TrimSpace(o.SourceCloneURL) != "" ||
		strings.TrimSpace(o.SourceWebURL) != "" || strings.TrimSpace(o.DefaultBranch) != "" ||
		o.CreateIfMissing || o.BindSource || o.SkipSourceBinding || o.Yes || o.PlanOnly
}

type discoveredSource struct {
	RemoteName         string `json:"remote_name"`
	Authority          string `json:"authority"`
	ExternalRepository string `json:"external_repository"`
	CloneURL           string `json:"clone_url"`
	WebURL             string `json:"web_url"`
}

type selfHostedInitPlan struct {
	Mode         string                           `json:"mode"`
	Profile      string                           `json:"profile"`
	Registry     string                           `json:"registry_source,omitempty"`
	Server       github.NativeServerMetadata      `json:"server"`
	Organization github.NativeOrganizationContext `json:"organization"`
	Repository   selfHostedRepositoryPlan         `json:"repository"`
	Source       *discoveredSource                `json:"source,omitempty"`
	Provider     *workflow.ProviderPlan           `json:"provider,omitempty"`
	Mutations    []string                         `json:"mutations,omitempty"`
}

type selfHostedRepositoryPlan struct {
	Key           string `json:"key"`
	Name          string `json:"name"`
	ID            string `json:"id,omitempty"`
	DefaultBranch string `json:"default_branch"`
	Existing      bool   `json:"existing"`
	CreateAllowed bool   `json:"create_allowed"`
	BindSource    bool   `json:"bind_source"`
	BindingExists bool   `json:"binding_exists"`
}

type selfHostedInitJournal struct {
	Version          int                         `json:"version"`
	Profile          string                      `json:"profile"`
	ServerInstanceID string                      `json:"server_instance_id"`
	OrganizationID   string                      `json:"organization_id"`
	RepositoryName   string                      `json:"repository_name"`
	RepositoryID     string                      `json:"repository_id,omitempty"`
	Stages           map[string]initJournalStage `json:"stages"`
	UpdatedAt        time.Time                   `json:"updated_at"`
}

type initJournalStage struct {
	State  string `json:"state"`
	Detail string `json:"detail,omitempty"`
}

type selfHostedProjectConfig struct {
	Version          int                       `json:"version"`
	Repo             string                    `json:"repo"`
	Hostname         string                    `json:"hostname"`
	Profile          string                    `json:"profile"`
	ServerInstanceID string                    `json:"server_instance_id"`
	OrganizationID   string                    `json:"organization_id"`
	RepositoryID     string                    `json:"repository_id"`
	Provider         *selfHostedProviderConfig `json:"provider,omitempty"`
}

type selfHostedProviderConfig struct {
	Key                string   `json:"key"`
	ExternalRepository string   `json:"external_repository,omitempty"`
	Capabilities       []string `json:"capabilities"`
}

func (a *app) runSelfHostedInit(ctx context.Context, profile auth.Profile, options selfHostedInitOptions) int {
	if options.BindSource && options.SkipSourceBinding {
		a.errorf("--bind-source and --skip-source-binding cannot be combined\n")
		return 2
	}
	repoKey, ok := a.validateRepo(options.Repo)
	if !ok {
		return 2
	}
	owner, repoName, _ := strings.Cut(repoKey, "/")
	if strings.TrimSpace(options.ServerOrg) == "" {
		options.ServerOrg = owner
	}
	if strings.TrimSpace(options.ServerRepo) == "" {
		options.ServerRepo = repoName
	}
	if !repositorySegmentPattern.MatchString(options.ServerRepo) || options.ServerRepo == "." || options.ServerRepo == ".." {
		a.errorf("--server-repo must be a canonical repository name\n")
		return 2
	}
	options.DefaultBranch = normalizeDefaultBranch(options.DefaultBranch)
	if !validInitBranch(options.DefaultBranch) {
		a.errorf("--default-branch is not a canonical branch name\n")
		return 2
	}
	if !options.BindSource && !profile.OnboardingPolicy.AllowSourceBinding &&
		(strings.TrimSpace(options.ExternalRepo) != "" || strings.TrimSpace(options.SourceRemote) != "" ||
			strings.TrimSpace(options.SourceCloneURL) != "" || strings.TrimSpace(options.SourceWebURL) != "") {
		a.errorf("source coordinate flags require --bind-source or a profile source-binding policy\n")
		return 2
	}

	metadataClient, err := github.NewClientWithOptions(github.ClientOptions{Host: profile.Hostname,
		BaseURL: profile.NativeAPIURL, CAFile: profile.CAFile})
	if err != nil {
		return a.selfHostedInitError("configure metadata client", err)
	}
	metadata, err := metadataClient.GetNativeServerMetadata(ctx)
	if err != nil {
		return a.selfHostedInitError("read self-hosted server metadata", err)
	}
	if err := auth.ValidateServerHandshake(profile, auth.ServerHandshake{ServerInstanceID: metadata.ServerInstanceID,
		APIURL: metadata.APIURL, NativeAPIURL: metadata.NativeAPIURL, WebURL: metadata.WebURL}); err != nil {
		return a.selfHostedInitError("validate self-hosted server identity", err)
	}
	token, err := auth.ResolveProfileToken(ctx, profile)
	if err != nil {
		return a.selfHostedInitError("resolve origin-bound profile token", err)
	}
	native, err := github.NewClientWithOptions(github.ClientOptions{Host: profile.Hostname,
		BaseURL: profile.NativeAPIURL, Token: token.Value, CAFile: profile.CAFile})
	if err != nil {
		return a.selfHostedInitError("configure native onboarding client", err)
	}
	compatibility, err := github.NewClientWithOptions(github.ClientOptions{Host: profile.Hostname,
		BaseURL: profile.APIURL, Token: token.Value, CAFile: profile.CAFile})
	if err != nil {
		return a.selfHostedInitError("configure compatibility client", err)
	}
	user, scopes, err := compatibility.GetUser(ctx)
	if err != nil {
		return a.selfHostedInitError("validate profile credential", err)
	}
	current, err := native.GetNativeContext(ctx)
	if err != nil {
		return a.selfHostedInitError("read server context", err)
	}
	organization, err := selectNativeOrganization(current.Organizations, options.ServerOrg)
	if err != nil {
		return a.selfHostedInitError("resolve server organization", err)
	}
	serverRepoKey := organization.Name + "/" + options.ServerRepo
	repositories, err := native.ListNativeContextRepositories(ctx, organization.ID)
	if err != nil {
		return a.selfHostedInitError("read server repositories", err)
	}
	existing, exists, err := selectNativeRepository(repositories.Repositories, options.ServerRepo)
	if err != nil {
		return a.selfHostedInitError("resolve server repository", err)
	}
	createAllowed := options.CreateIfMissing || profile.OnboardingPolicy.AllowRepositoryCreate
	if !exists && !createAllowed {
		a.errorf("self-hosted repository %s/%s is absent; pass --create-if-missing or enable the profile repository-create policy\n",
			organization.Name, options.ServerRepo)
		return 1
	}

	bindSource := !options.SkipSourceBinding && (options.BindSource || profile.OnboardingPolicy.AllowSourceBinding)
	providerPlan, source, registrySource, _, err := resolveInitProvider(ctx, profile, metadata, options, bindSource)
	if err != nil {
		return a.selfHostedInitError("resolve external code provider", err)
	}
	plan := selfHostedInitPlan{Mode: "self-hosted", Profile: profile.Name, Registry: registrySource,
		Server: metadata, Organization: organization, Source: source, Provider: providerPlan,
		Repository: selfHostedRepositoryPlan{Key: organization.Name + "/" + options.ServerRepo,
			Name: options.ServerRepo, DefaultBranch: options.DefaultBranch, Existing: exists,
			CreateAllowed: createAllowed, BindSource: bindSource}}
	if exists {
		plan.Repository.ID = existing.Repository.ID
	} else {
		plan.Mutations = append(plan.Mutations, "register private server repository")
	}
	if bindSource {
		if exists {
			scope, scopeErr := repositoryScope(organization.ID, existing.Repository.ID)
			if scopeErr != nil {
				return a.selfHostedInitError("validate server repository identity", scopeErr)
			}
			binding, bindingErr := native.GetNativeActiveBinding(ctx, scope)
			switch {
			case bindingErr == nil:
				if !bindingMatches(binding, *providerPlan, *source, options.DefaultBranch) {
					return a.selfHostedInitError("resolve source binding", errors.New("an incompatible active source binding already exists"))
				}
				plan.Repository.BindingExists = true
			case isAPIStatus(bindingErr, 404):
				plan.Mutations = append(plan.Mutations, "create credential-free active source binding")
			default:
				return a.selfHostedInitError("read active source binding", bindingErr)
			}
		} else {
			plan.Mutations = append(plan.Mutations, "create credential-free active source binding")
		}
	}
	if options.CreateLabels {
		plan.Mutations = append(plan.Mutations, "ensure issue-spec labels")
	}
	plan.Mutations = uniqueStrings(plan.Mutations)
	if err := validateExistingSelfHostedConfig(filepath.Join(".issue-spec", "config.json"), serverRepoKey, profile); err != nil {
		return a.selfHostedInitError("validate existing project config", err)
	}
	if providerPlan != nil {
		if err := validateExistingWorkflowProvider(".", providerPlan.ProviderKey); err != nil {
			return a.selfHostedInitError("validate existing provider workflow config", err)
		}
	}
	if options.PlanOnly {
		return a.outputSelfHostedInitPlan(plan, user.Login, scopes, options.JSON)
	}
	if len(plan.Mutations) > 0 && !options.Yes && !profile.OnboardingPolicy.AllowUnattended {
		if options.JSON {
			a.errorf("self-hosted init requires --yes for JSON/non-interactive remote mutation\n")
			return 1
		}
		if !a.confirmSelfHostedInit(plan) {
			a.errorf("self-hosted init cancelled; re-run with --plan to inspect or --yes to approve\n")
			return 1
		}
	}

	journalPath := filepath.Join(".issue-spec", "init-state.json")
	journal, err := loadOrCreateInitJournal(journalPath, profile, organization, options.ServerRepo)
	if err != nil {
		return a.selfHostedInitError("open init resume journal", err)
	}
	markJournalStage(&journal, "handshake", "complete", metadata.ServerInstanceID)
	if providerPlan != nil {
		markJournalStage(&journal, "provider", "complete", providerPlan.ProviderKey)
	} else {
		markJournalStage(&journal, "provider", "skipped", "not selected")
	}
	if err := writeInitJournal(journalPath, journal); err != nil {
		return a.selfHostedInitError("write init resume journal", err)
	}

	repositoryID := plan.Repository.ID
	if !exists {
		ensured, ensureErr := native.EnsureNativeRepository(ctx, organization.ID, github.NativeEnsureRepositoryInput{
			Name: options.ServerRepo, DisplayName: options.ServerRepo, Description: "Registered by issue-spec init",
			DefaultBranch: options.DefaultBranch})
		if ensureErr != nil {
			return a.selfHostedInitError("register server repository", ensureErr)
		}
		repositoryID = ensured.Repository.ID
		markJournalStage(&journal, "repository", "complete", map[bool]string{true: "created", false: "reused"}[ensured.Created])
	} else {
		markJournalStage(&journal, "repository", "complete", "reused")
	}
	journal.RepositoryID = repositoryID
	if err := writeInitJournal(journalPath, journal); err != nil {
		return a.selfHostedInitError("record repository stage", err)
	}

	if bindSource && !plan.Repository.BindingExists {
		scope, scopeErr := repositoryScope(organization.ID, repositoryID)
		if scopeErr != nil {
			return a.selfHostedInitError("validate ensured repository identity", scopeErr)
		}
		ensured, ensureErr := native.EnsureNativeActiveBinding(ctx, scope, github.NativeEnsureBindingInput{
			ProviderKey: providerPlan.ProviderKey, ExternalRepositoryID: source.ExternalRepository,
			CloneURL: source.CloneURL, WebURL: source.WebURL, DefaultBranch: options.DefaultBranch})
		if ensureErr != nil {
			return a.selfHostedInitError("ensure active source binding", ensureErr)
		}
		markJournalStage(&journal, "binding", "complete", map[bool]string{true: "created", false: "reused"}[ensured.Created])
	} else if bindSource {
		markJournalStage(&journal, "binding", "complete", "reused")
	} else {
		markJournalStage(&journal, "binding", "skipped", "disabled")
	}
	if err := writeInitJournal(journalPath, journal); err != nil {
		return a.selfHostedInitError("record source binding stage", err)
	}

	var labels []github.LabelResult
	if options.CreateLabels {
		for _, label := range issueSpecLabels() {
			result, labelErr := compatibility.CreateLabel(ctx, serverRepoKey, label.name, label.color, label.description)
			if labelErr != nil {
				return a.selfHostedInitError("ensure label "+label.name, labelErr)
			}
			labels = append(labels, result)
		}
		markJournalStage(&journal, "labels", "complete", "ensured")
	} else {
		markJournalStage(&journal, "labels", "skipped", "disabled")
	}
	configPath := filepath.Join(".issue-spec", "config.json")
	projectConfig := selfHostedProjectConfig{Version: selfHostedInitConfigVersion, Repo: serverRepoKey,
		Hostname: profile.Hostname, Profile: profile.Name, ServerInstanceID: profile.ServerInstanceID,
		OrganizationID: organization.ID, RepositoryID: repositoryID}
	if providerPlan != nil {
		projectConfig.Provider = &selfHostedProviderConfig{Key: providerPlan.ProviderKey,
			Capabilities: capabilityNames(providerPlan.Capabilities)}
		if source != nil {
			projectConfig.Provider.ExternalRepository = source.ExternalRepository
		}
	}
	if err := writeAtomicJSON(configPath, projectConfig, 0o644); err != nil {
		return a.selfHostedInitError("write versioned project config", err)
	}
	markJournalStage(&journal, "config", "complete", filepath.ToSlash(configPath))
	if strings.TrimSpace(options.Language) != "" {
		if _, err := writeWorkflowLanguageConfig(".", options.Language); err != nil {
			return a.selfHostedInitError("write workflow language config", err)
		}
	}
	if providerPlan != nil {
		if err := writeExternalCodeWorkflowConfig(".", *providerPlan); err != nil {
			return a.selfHostedInitError("write provider workflow config", err)
		}
	}
	var workflows workflowGenerationResult
	if providerPlan != nil {
		workflows, err = writeWorkflowArtifactsWithProvider(".", serverRepoKey, options.Tools, options.Delivery, *providerPlan)
	} else {
		workflows, err = writeWorkflowArtifacts(".", serverRepoKey, options.Tools, options.Delivery)
	}
	if err != nil {
		return a.selfHostedInitError("generate workflow artifacts", err)
	}
	markJournalStage(&journal, "workflow", "complete", fmt.Sprintf("%d skills, %d commands", len(workflows.SkillFiles), len(workflows.CommandFiles)))
	if err := writeInitJournal(journalPath, journal); err != nil {
		return a.selfHostedInitError("complete init resume journal", err)
	}

	result := map[string]any{"ok": true, "mode": "self-hosted", "repo": serverRepoKey, "profile": profile.Name,
		"server_instance_id": profile.ServerInstanceID, "organization_id": organization.ID,
		"repository_id": repositoryID, "auth": map[string]any{"source": token.Source, "user": user.Login, "scopes": scopes},
		"plan": plan, "config": filepath.ToSlash(configPath), "journal": filepath.ToSlash(journalPath),
		"labels": labels, "workflows": workflows}
	if options.JSON {
		return a.outputJSON(result)
	}
	fmt.Fprintf(a.out, "initialized issue-spec for %s with self-hosted profile %s\n", serverRepoKey, profile.Name)
	fmt.Fprintf(a.out, "server repository: %s/%s (%s)\nconfig: %s\nresume journal: %s\n",
		organization.Name, options.ServerRepo, repositoryID, filepath.ToSlash(configPath), filepath.ToSlash(journalPath))
	if providerPlan != nil {
		fmt.Fprintf(a.out, "external code provider: %s (%s)\n", providerPlan.ProviderKey, providerPlan.DisplayName)
	}
	return 0
}

func (a *app) selfHostedInitError(action string, err error) int {
	a.errorf("%s: %v\n", action, err)
	return 1
}

func (a *app) outputSelfHostedInitPlan(plan selfHostedInitPlan, user string, scopes []string, jsonOutput bool) int {
	if jsonOutput {
		return a.outputJSON(map[string]any{"ok": true, "plan_only": true, "plan": plan,
			"auth": map[string]any{"user": user, "scopes": scopes}})
	}
	fmt.Fprintf(a.out, "self-hosted init plan for %s via profile %s\n", plan.Repository.Key, plan.Profile)
	fmt.Fprintf(a.out, "server: %s\nrepository: %s\n", plan.Server.ServerInstanceID,
		map[bool]string{true: "reuse", false: "create"}[plan.Repository.Existing])
	if plan.Provider != nil {
		fmt.Fprintf(a.out, "provider: %s (%s)\n", plan.Provider.ProviderKey, plan.Provider.DisplayName)
	}
	for _, mutation := range plan.Mutations {
		fmt.Fprintf(a.out, "mutation: %s\n", mutation)
	}
	return 0
}

func (a *app) confirmSelfHostedInit(plan selfHostedInitPlan) bool {
	fmt.Fprintf(a.err, "Self-hosted init will mutate %s on %s:\n", plan.Repository.Key, plan.Server.WebURL)
	for _, mutation := range plan.Mutations {
		fmt.Fprintf(a.err, "  - %s\n", mutation)
	}
	fmt.Fprint(a.err, "Continue? [y/N] ")
	line, err := bufio.NewReader(a.in).ReadString('\n')
	if err != nil && !errors.Is(err, os.ErrClosed) && strings.TrimSpace(line) == "" {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	default:
		return false
	}
}

func normalizeDefaultBranch(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "main"
	}
	return value
}

func validInitBranch(value string) bool {
	return value != "" && len(value) <= 255 && !strings.HasPrefix(value, "-") &&
		!strings.HasPrefix(value, ".") && !strings.HasSuffix(value, ".") && !strings.HasSuffix(value, "/") &&
		!strings.Contains(value, "..") && !strings.Contains(value, "@{") &&
		!strings.ContainsAny(value, "\x00\r\n\t ~^:?*[\\")
}

func selectNativeOrganization(items []github.NativeOrganizationContext, selector string) (github.NativeOrganizationContext, error) {
	selector = strings.TrimSpace(selector)
	var matches []github.NativeOrganizationContext
	for _, item := range items {
		if item.ID == selector || item.Name == selector {
			matches = append(matches, item)
		}
	}
	if len(matches) != 1 {
		return github.NativeOrganizationContext{}, fmt.Errorf("organization %q matched %d visible organizations", selector, len(matches))
	}
	return matches[0], nil
}

func selectNativeRepository(items []github.NativeRepositoryContext, name string) (github.NativeRepositoryContext, bool, error) {
	name = strings.TrimSpace(name)
	var matches []github.NativeRepositoryContext
	for _, item := range items {
		if item.Repository.Name == name {
			matches = append(matches, item)
		}
	}
	if len(matches) > 1 {
		return github.NativeRepositoryContext{}, false, fmt.Errorf("repository name %q is ambiguous", name)
	}
	if len(matches) == 0 {
		return github.NativeRepositoryContext{}, false, nil
	}
	return matches[0], true, nil
}

func repositoryScope(organizationID, repositoryID string) (models.RepoScope, error) {
	orgID, orgErr := uuid.Parse(organizationID)
	repoID, repoErr := uuid.Parse(repositoryID)
	if orgErr != nil || repoErr != nil {
		return models.RepoScope{}, errors.New("server returned invalid repository UUIDs")
	}
	return models.RepoScope{OrgID: orgID, RepoID: repoID}, nil
}

func isAPIStatus(err error, status int) bool {
	var apiErr *github.APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == status
}

func bindingMatches(binding github.NativeBinding, provider workflow.ProviderPlan, source discoveredSource, branch string) bool {
	return binding.ProviderKey == provider.ProviderKey && binding.ExternalRepositoryID == source.ExternalRepository &&
		binding.CloneURL == source.CloneURL && binding.WebURL == source.WebURL && binding.DefaultBranch == branch
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	return result
}

func capabilityNames(values []codereview.Capability) []string {
	result := make([]string, len(values))
	for i := range values {
		result[i] = string(values[i])
	}
	return result
}

func loadOrCreateInitJournal(path string, profile auth.Profile, organization github.NativeOrganizationContext, repositoryName string) (selfHostedInitJournal, error) {
	journal := selfHostedInitJournal{Version: selfHostedInitJournalVersion, Profile: profile.Name,
		ServerInstanceID: profile.ServerInstanceID, OrganizationID: organization.ID,
		RepositoryName: repositoryName, Stages: pendingInitStages()}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return journal, nil
	}
	if err != nil {
		return selfHostedInitJournal{}, err
	}
	var existing selfHostedInitJournal
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&existing); err != nil {
		return selfHostedInitJournal{}, fmt.Errorf("invalid existing journal: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return selfHostedInitJournal{}, errors.New("invalid existing journal: multiple JSON values")
	}
	if existing.Version != selfHostedInitJournalVersion || existing.Profile != profile.Name ||
		existing.ServerInstanceID != profile.ServerInstanceID || existing.OrganizationID != organization.ID ||
		existing.RepositoryName != repositoryName {
		return selfHostedInitJournal{}, errors.New("existing init journal belongs to a different server onboarding target")
	}
	if existing.Stages == nil {
		existing.Stages = pendingInitStages()
	} else {
		for stage, state := range pendingInitStages() {
			if _, ok := existing.Stages[stage]; !ok {
				existing.Stages[stage] = state
			}
		}
	}
	return existing, nil
}

func pendingInitStages() map[string]initJournalStage {
	return map[string]initJournalStage{
		"handshake": {State: "pending"}, "provider": {State: "pending"},
		"repository": {State: "pending"}, "binding": {State: "pending"},
		"labels": {State: "pending"}, "config": {State: "pending"},
		"workflow": {State: "pending"},
	}
}

func markJournalStage(journal *selfHostedInitJournal, stage, state, detail string) {
	journal.Stages[stage] = initJournalStage{State: state, Detail: detail}
	journal.UpdatedAt = time.Now().UTC().Truncate(time.Second)
}

func writeInitJournal(path string, journal selfHostedInitJournal) error {
	return writeAtomicJSON(path, journal, 0o644)
}

func writeAtomicJSON(path string, value any, mode os.FileMode) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".init-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(mode); err != nil {
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
	return os.Rename(temporaryPath, path)
}

func writeExternalCodeWorkflowConfig(root string, provider workflow.ProviderPlan) error {
	path := filepath.Join(root, "issue-spec", "config.yaml")
	config := map[string]any{}
	if raw, err := os.ReadFile(path); err == nil {
		if err := yaml.Unmarshal(raw, &config); err != nil {
			return fmt.Errorf("parse existing %s: %w", filepath.ToSlash(path), err)
		}
		if config == nil {
			config = map[string]any{}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	external := map[string]any{}
	if existing, ok := config["external_code"]; ok {
		var mappingOK bool
		external, mappingOK = existing.(map[string]any)
		if !mappingOK {
			return fmt.Errorf("existing %s external_code must be a mapping", filepath.ToSlash(path))
		}
	}
	if existing, ok := external["provider_key"]; ok {
		providerKey, stringOK := existing.(string)
		if !stringOK {
			return fmt.Errorf("existing %s external_code.provider_key must be a string", filepath.ToSlash(path))
		}
		if providerKey = strings.TrimSpace(providerKey); providerKey != "" && providerKey != provider.ProviderKey {
			return fmt.Errorf("existing %s selects external code provider %q, not %q", filepath.ToSlash(path), providerKey, provider.ProviderKey)
		}
	}
	external["provider_key"] = provider.ProviderKey

	evidence := map[string]any{}
	evidenceConfigured := false
	if existing, ok := external["evidence"]; ok {
		evidenceConfigured = true
		var mappingOK bool
		evidence, mappingOK = existing.(map[string]any)
		if !mappingOK {
			return fmt.Errorf("existing %s external_code.evidence must be a mapping", filepath.ToSlash(path))
		}
	}
	required := make([]string, 0, len(provider.RecommendedEvidence))
	for _, kind := range provider.RecommendedEvidence {
		required = append(required, string(kind))
	}
	if _, ok := evidence["required"]; !ok && len(required) > 0 {
		evidence["required"] = required
	}
	if _, ok := evidence["sync_before"]; !ok && provider.EvidenceSnapshot {
		evidence["sync_before"] = []string{"verify"}
	}
	if evidenceConfigured || len(evidence) > 0 {
		external["evidence"] = evidence
	}
	config["external_code"] = external
	raw, err := yaml.Marshal(config)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o644)
}

func validateExistingSelfHostedConfig(path, repo string, profile auth.Profile) error {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var existing struct {
		Repo             string `json:"repo"`
		Profile          string `json:"profile"`
		ServerInstanceID string `json:"server_instance_id"`
	}
	if err := json.Unmarshal(raw, &existing); err != nil {
		return fmt.Errorf("existing config is invalid: %w", err)
	}
	if strings.TrimSpace(existing.Repo) != "" && existing.Repo != repo {
		return fmt.Errorf("existing config selects repository %q", existing.Repo)
	}
	if existing.Profile != "" && existing.Profile != profile.Name {
		return fmt.Errorf("existing config selects profile %q", existing.Profile)
	}
	if existing.ServerInstanceID != "" && existing.ServerInstanceID != profile.ServerInstanceID {
		return errors.New("existing config is bound to a different server instance")
	}
	return nil
}

func validateExistingWorkflowProvider(root, providerKey string) error {
	path := filepath.Join(root, "issue-spec", "config.yaml")
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var config workflow.Config
	if err := yaml.Unmarshal(raw, &config); err != nil {
		return err
	}
	if config.ExternalCode != nil && strings.TrimSpace(config.ExternalCode.ProviderKey) != "" &&
		config.ExternalCode.ProviderKey != providerKey {
		return fmt.Errorf("existing workflow selects provider %q", config.ExternalCode.ProviderKey)
	}
	return nil
}

func resolveInitProvider(ctx context.Context, profile auth.Profile, metadata github.NativeServerMetadata,
	options selfHostedInitOptions, bindingRequested bool) (*workflow.ProviderPlan, *discoveredSource, string, codereview.Registry, error) {
	wantsProvider := bindingRequested || strings.TrimSpace(options.ProviderKey) != ""
	if !wantsProvider {
		registry, _ := codereview.NewRegistry(nil)
		return nil, nil, "none", registry, nil
	}
	registry, source, err := codereview.LoadOperatorRegistry(profile.OperatorRegistryFile)
	if err != nil {
		return nil, nil, source, codereview.Registry{}, err
	}
	descriptions := registry.Descriptions()
	selectedSource, providerKey, err := discoverInitSource(ctx, options, descriptions, bindingRequested)
	if err != nil {
		return nil, nil, source, registry, err
	}
	if explicit := strings.TrimSpace(options.ProviderKey); explicit != "" {
		providerKey = explicit
	}
	description, ok := findProviderDescription(descriptions, providerKey)
	if !ok {
		return nil, nil, source, registry, fmt.Errorf("provider %q is not registered by the selected operator registry", providerKey)
	}
	serverDescription, ok := findProviderDescription(metadata.Providers, providerKey)
	if !ok || !reflect.DeepEqual(description, serverDescription) {
		return nil, nil, source, registry, fmt.Errorf("provider %q metadata does not match the server handshake", providerKey)
	}
	provider, err := registry.Lookup(providerKey)
	if err != nil {
		return nil, nil, source, registry, err
	}
	capabilities, err := provider.Capabilities(ctx)
	if err != nil {
		return nil, nil, source, registry, err
	}
	plan, err := workflow.NewProviderPlan(description, capabilities)
	if err != nil {
		return nil, nil, source, registry, err
	}
	if selectedSource != nil && len(description.RemoteAuthorities) > 0 && !containsExactString(description.RemoteAuthorities, selectedSource.Authority) {
		return nil, nil, source, registry, fmt.Errorf("remote authority %q is not registered for provider %q", selectedSource.Authority, providerKey)
	}
	return &plan, selectedSource, source, registry, nil
}

func findProviderDescription(descriptions []codereview.ProviderDescription, key string) (codereview.ProviderDescription, bool) {
	for _, description := range descriptions {
		if description.ProviderKey == key {
			return description, true
		}
	}
	return codereview.ProviderDescription{}, false
}

func containsExactString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

var gitRemoteNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
var repositorySegmentPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func discoverInitSource(ctx context.Context, options selfHostedInitOptions, descriptions []codereview.ProviderDescription, required bool) (*discoveredSource, string, error) {
	remotes, err := readCanonicalGitRemotes(ctx, ".")
	if err != nil {
		if required {
			return nil, "", err
		}
		return nil, strings.TrimSpace(options.ProviderKey), nil
	}
	providerByAuthority := map[string][]string{}
	for _, description := range descriptions {
		for _, authority := range description.RemoteAuthorities {
			providerByAuthority[authority] = append(providerByAuthority[authority], description.ProviderKey)
		}
	}
	var candidates []struct {
		source   discoveredSource
		provider string
	}
	for _, remote := range remotes {
		if options.SourceRemote != "" && remote.RemoteName != options.SourceRemote {
			continue
		}
		if options.ExternalRepo != "" && remote.ExternalRepository != options.ExternalRepo {
			continue
		}
		providers := providerByAuthority[remote.Authority]
		if options.ProviderKey != "" {
			providers = nil
			if description, ok := findProviderDescription(descriptions, options.ProviderKey); ok &&
				(len(description.RemoteAuthorities) == 0 || containsExactString(description.RemoteAuthorities, remote.Authority)) {
				providers = []string{options.ProviderKey}
			}
		}
		for _, provider := range providers {
			candidates = append(candidates, struct {
				source   discoveredSource
				provider string
			}{source: remote, provider: provider})
		}
	}
	if len(candidates) != 1 {
		if !required && options.ProviderKey != "" && options.ExternalRepo == "" && options.SourceRemote == "" {
			return nil, options.ProviderKey, nil
		}
		return nil, "", fmt.Errorf("canonical git remote discovery matched %d provider/repository candidates; disambiguate with --provider, --source-remote, and --external-repo", len(candidates))
	}
	selected := candidates[0].source
	if options.ExternalRepo != "" {
		selected.ExternalRepository = options.ExternalRepo
	}
	if options.SourceCloneURL != "" {
		clone, err := canonicalCredentialFreeHTTPS(options.SourceCloneURL, true)
		if err != nil {
			return nil, "", fmt.Errorf("--source-clone-url: %w", err)
		}
		selected.CloneURL = clone
	}
	if options.SourceWebURL != "" {
		web, err := canonicalCredentialFreeHTTPS(options.SourceWebURL, false)
		if err != nil {
			return nil, "", fmt.Errorf("--source-web-url: %w", err)
		}
		selected.WebURL = web
	}
	for flag, coordinate := range map[string]string{"clone": selected.CloneURL, "web": selected.WebURL} {
		_, repository, err := parseCanonicalGitURL(coordinate)
		if err != nil || repository != selected.ExternalRepository {
			return nil, "", fmt.Errorf("selected %s URL does not match external repository %q", flag, selected.ExternalRepository)
		}
	}
	return &selected, candidates[0].provider, nil
}

func readCanonicalGitRemotes(ctx context.Context, root string) ([]discoveredSource, error) {
	command := exec.CommandContext(ctx, "git", "-C", root, "config", "--get-regexp", `^remote\..*\.url$`)
	output, err := command.Output()
	if err != nil {
		return nil, errors.New("read canonical git remotes: git config failed")
	}
	return parseCanonicalGitRemoteConfig(string(output))
}

func parseCanonicalGitRemoteConfig(output string) ([]discoveredSource, error) {
	seen := map[string]bool{}
	var result []discoveredSource
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || !strings.HasPrefix(fields[0], "remote.") || !strings.HasSuffix(fields[0], ".url") {
			return nil, errors.New("git remote configuration is not canonical")
		}
		name := strings.TrimSuffix(strings.TrimPrefix(fields[0], "remote."), ".url")
		if !gitRemoteNamePattern.MatchString(name) {
			return nil, fmt.Errorf("git remote name %q is invalid", name)
		}
		authority, repository, err := parseCanonicalGitURL(fields[1])
		if err != nil {
			return nil, fmt.Errorf("git remote %s: %w", name, err)
		}
		key := name + "\x00" + authority + "\x00" + repository
		if seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, discoveredSource{RemoteName: name, Authority: authority,
			ExternalRepository: repository, CloneURL: "https://" + authority + "/" + repository + ".git",
			WebURL: "https://" + authority + "/" + repository})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].RemoteName == result[j].RemoteName {
			return result[i].ExternalRepository < result[j].ExternalRepository
		}
		return result[i].RemoteName < result[j].RemoteName
	})
	if len(result) == 0 {
		return nil, errors.New("no canonical git remotes found")
	}
	return result, nil
}

func parseCanonicalGitURL(raw string) (string, string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.ContainsAny(raw, "\x00\r\n\t ") {
		return "", "", errors.New("URL is empty or contains whitespace")
	}
	if strings.HasPrefix(raw, "git@") && !strings.Contains(raw, "://") {
		hostPath := strings.TrimPrefix(raw, "git@")
		host, path, ok := strings.Cut(hostPath, ":")
		if !ok {
			return "", "", errors.New("scp-style URL is incomplete")
		}
		return canonicalRemoteIdentity(host, path)
	}
	parsed, err := url.Parse(raw)
	if err != nil || !parsed.IsAbs() || parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", "", errors.New("URL must be an absolute HTTPS or SSH remote without query or fragment")
	}
	if parsed.Scheme != "https" && parsed.Scheme != "ssh" {
		return "", "", errors.New("URL must use HTTPS or SSH")
	}
	if parsed.RawPath != "" || strings.Contains(parsed.EscapedPath(), "%") {
		return "", "", errors.New("URL path must not use encoded aliases")
	}
	if parsed.User != nil {
		if parsed.Scheme != "ssh" || parsed.User.Username() != "git" {
			return "", "", errors.New("URL contains credentials or an unsupported SSH user")
		}
		if _, set := parsed.User.Password(); set {
			return "", "", errors.New("URL contains credentials")
		}
	}
	return canonicalRemoteIdentity(parsed.Host, strings.TrimPrefix(parsed.EscapedPath(), "/"))
}

func canonicalRemoteIdentity(authority, remotePath string) (string, string, error) {
	authority = strings.ToLower(strings.TrimSpace(authority))
	host := authority
	if parsedHost, port, err := net.SplitHostPort(authority); err == nil {
		if port == "443" {
			host = parsedHost
		} else {
			host = net.JoinHostPort(strings.ToLower(parsedHost), port)
		}
	}
	if strings.ContainsAny(host, "/@?#\\") || strings.TrimSpace(host) == "" {
		return "", "", errors.New("remote authority is invalid")
	}
	if remotePath == "" || strings.Contains(remotePath, "%") || strings.Contains(remotePath, "\\") ||
		strings.HasPrefix(remotePath, "/") || strings.HasSuffix(remotePath, "/") {
		return "", "", errors.New("repository path is invalid")
	}
	repository := strings.TrimSuffix(remotePath, ".git")
	segments := strings.Split(repository, "/")
	if len(segments) < 2 {
		return "", "", errors.New("repository path must contain owner and name")
	}
	for _, segment := range segments {
		if !repositorySegmentPattern.MatchString(segment) || segment == "." || segment == ".." {
			return "", "", errors.New("repository path contains a non-canonical segment")
		}
	}
	return host, strings.Join(segments, "/"), nil
}

func canonicalCredentialFreeHTTPS(raw string, clone bool) (string, error) {
	raw = strings.TrimSpace(raw)
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("must be a credential-free canonical HTTPS URL without query or fragment")
	}
	authority, repository, err := canonicalRemoteIdentity(parsed.Host, strings.TrimPrefix(parsed.EscapedPath(), "/"))
	if err != nil {
		return "", err
	}
	suffix := ""
	if clone {
		suffix = ".git"
	}
	canonical := "https://" + authority + "/" + repository + suffix
	if raw != canonical {
		return "", errors.New("URL is not canonical")
	}
	return canonical, nil
}
