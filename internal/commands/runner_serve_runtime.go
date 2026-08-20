package commands

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/capability"
	"github.com/higress-group/issue-spec/internal/commentrunner"
	"github.com/higress-group/issue-spec/internal/commentrunner/credentials"
	webhook "github.com/higress-group/issue-spec/internal/commentrunner/intake/webhook"
	"github.com/higress-group/issue-spec/internal/commentrunner/jobs"
	repository "github.com/higress-group/issue-spec/internal/commentrunner/repository"
	runnerserver "github.com/higress-group/issue-spec/internal/commentrunner/server"
	crstate "github.com/higress-group/issue-spec/internal/commentrunner/state"
	"github.com/higress-group/issue-spec/internal/commentrunner/writeback"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/server/models"
)

type runnerServeRuntime interface{ Run(context.Context) error }

type runnerServeRuntimeInput struct {
	Profile                  auth.Profile
	ProfileToken             string
	SubscriptionSecret       []byte
	Runner                   commentrunner.Config
	Queue                    *webhook.Queue
	Store                    crstate.StateStore
	HTTP                     *runnerserver.Service
	Storage                  jobs.StorageLifecycle
	GitCredentialCommand     string
	GitCredentialArgs        []string
	GitCredentialTimeout     time.Duration
	GitCredentialMaxOutput   int64
	GitCredentialConcurrency int
	ReconcileWorkers         int
	ReconcileLease           time.Duration
	Diagnostics              *runnerLogger
	Dependencies             *runnerServeRuntimeDependencies
}

// runnerServeRuntimeDependencies is a hermetic composition seam used by the
// command-level test. Production leaves it nil and always receives the concrete
// workspace, sandbox, acpx, artifact and writeback implementations below.
type runnerServeRuntimeDependencies struct {
	Workspaces      jobs.WorkspaceManager
	Sandbox         jobs.SandboxPreparer
	Acpx            jobs.AcpxFactory
	Artifacts       jobs.ArtifactProvider
	Writeback       jobs.Writeback
	IssueSpecBinary string
}

var runnerServeBuildRuntime = defaultBuildRunnerServeRuntime
var runnerServeRun = func(ctx context.Context, runtime runnerServeRuntime) error { return runtime.Run(ctx) }

func defaultBuildRunnerServeRuntime(ctx context.Context, input runnerServeRuntimeInput) (runnerServeRuntime, error) {
	profile, err := input.Profile.Normalized()
	if err != nil || profile.Kind != auth.ProfileKindHosted || strings.TrimSpace(input.ProfileToken) == "" {
		return nil, fmt.Errorf("runner serve runtime requires a self-hosted profile and profile PAT")
	}
	compatibility, err := github.NewClientWithOptions(github.ClientOptions{Host: profile.Hostname, BaseURL: profile.APIURL,
		Token: input.ProfileToken, CAFile: profile.CAFile})
	if err != nil {
		return nil, err
	}
	native, err := github.NewClientWithOptions(github.ClientOptions{Host: profile.Hostname, BaseURL: profile.NativeAPIURL,
		Token: input.ProfileToken, CAFile: profile.CAFile})
	if err != nil {
		return nil, err
	}
	var subscriptionVerifier *github.Client
	if strings.TrimSpace(input.Runner.Organization) != "" {
		if len(input.SubscriptionSecret) == 0 {
			return nil, fmt.Errorf("organization runner requires its webhook subscription secret for startup verification")
		}
		subscriptionVerifier, err = github.NewClientWithOptions(github.ClientOptions{Host: profile.Hostname,
			BaseURL: profile.NativeAPIURL, Token: string(input.SubscriptionSecret), CAFile: profile.CAFile})
		if err != nil {
			return nil, err
		}
		defer func() { subscriptionVerifier.Token = "" }()
	}
	registry, legacyCredentialScopes, err := buildRunnerRepositoryRegistry(ctx, native, subscriptionVerifier, input.Runner)
	if err != nil {
		return nil, err
	}
	sandboxConfig, hostSSHProvider, err := runnerSandboxRuntimeConfig(input.Runner)
	if err != nil {
		return nil, fmt.Errorf("runner serve host SSH: %w", err)
	}
	var gitProvider credentials.GitProvider
	if input.Runner.AllowHostSSH {
		gitProvider = hostSSHProvider
	} else {
		gitProvider, err = credentials.NewCommandGitProvider(credentials.CommandGitProviderConfig{Path: input.GitCredentialCommand,
			Args: input.GitCredentialArgs, Timeout: input.GitCredentialTimeout, MaxOutput: input.GitCredentialMaxOutput,
			MaxConcurrent: input.GitCredentialConcurrency})
	}
	if err != nil {
		return nil, err
	}
	var reconcileObserver webhook.ReconcileObserver
	if input.Diagnostics != nil {
		reconcileObserver = input.Diagnostics
	}
	reconciler, err := webhook.NewReconciler(webhook.ReconcilerConfig{Queue: input.Queue, Store: input.Store,
		Backend: compatibility, Registry: registry, Runner: input.Runner,
		AuthorizationPolicy: commentrunner.AuthorizationPolicy{RunnerLogin: input.Runner.RunnerIdentity,
			AllowedUsers: input.Runner.AllowedUsers}, WorkerID: "runner-serve", Workers: input.ReconcileWorkers,
		LeaseDuration: input.ReconcileLease, Observer: reconcileObserver})
	if err != nil {
		return nil, err
	}
	credentialRoot, err := runnerServeCredentialRoot(input.Runner.StatePath)
	if err != nil {
		return nil, err
	}
	materializer := credentials.Materializer{Root: credentialRoot}
	profileToken, err := materializer.WriteProfileToken(input.ProfileToken)
	if err != nil {
		return nil, fmt.Errorf("runner serve profile credential: %w", err)
	}
	broker := &credentials.Broker{Profile: profile, ProfileToken: &profileToken,
		Materializer: materializer, GitProvider: gitProvider,
		ProfileProbe: runnerProfileCapabilityProbe{native: native, compatibility: compatibility,
			runnerLogin: input.Runner.RunnerIdentity, registry: registry}}
	workspaces := jobs.WorkspaceManager(runnerWorkspaceManager(input.Runner))
	sandboxer := jobs.SandboxPreparer(jobs.SandboxRunner{Config: sandboxConfig})
	acpxFactory := jobs.AcpxFactory(jobs.AcpxAdapterFactory{Config: jobs.NewAcpxConfig(input.Runner), RunnerConfig: input.Runner})
	artifacts := jobs.ArtifactProvider(&jobs.IssueSpecArtifactProvider{GitHub: compatibility})
	writebacks := jobs.Writeback(&writeback.Service{GitHub: compatibility, Store: input.Store})
	issueSpecBinary := issueSpecBinaryForRunner()
	if deps := input.Dependencies; deps != nil {
		if deps.Workspaces != nil {
			workspaces = deps.Workspaces
		}
		if deps.Sandbox != nil {
			sandboxer = deps.Sandbox
		}
		if deps.Acpx != nil {
			acpxFactory = deps.Acpx
		}
		if deps.Artifacts != nil {
			artifacts = deps.Artifacts
		}
		if deps.Writeback != nil {
			writebacks = deps.Writeback
		}
		if strings.TrimSpace(deps.IssueSpecBinary) != "" {
			issueSpecBinary = strings.TrimSpace(deps.IssueSpecBinary)
		}
	}
	// Wrap the selected writeback boundary, including hermetic test or operator
	// dependencies, so every live lifecycle transition reaches diagnostics.
	writebacks = wrapRunnerWriteback(writebacks, input.Diagnostics)
	runtimeIdentity, err := jobs.RuntimeIdentityFromProfile(input.Runner.Hostname, profile, input.Runner.RunnerIdentity)
	if err != nil {
		return nil, fmt.Errorf("runner runtime identity: %w", err)
	}
	dispatcher := &jobs.Dispatcher{Store: input.Store, Storage: input.Storage,
		Repositories: repository.NativeResolver{Registry: registry},
		Workspaces:   workspaces, Sandbox: sandboxer, Acpx: acpxFactory, Artifacts: artifacts, Writeback: writebacks,
		AcpxBinary: input.Runner.AcpxPath, IssueSpecBinary: issueSpecBinary, CredentialBroker: broker,
		CredentialScopes:    legacyCredentialScopes,
		CapabilityPreflight: broker, CapabilityHost: profile.Hostname,
		OperatorSkillDirs: input.Runner.OperatorSkillDirs,
		RuntimeIdentity:   runtimeIdentity,
		RequiredOperations: []capability.Operation{capability.OperationIssueRead, capability.OperationIssueCommentWrite,
			capability.OperationArtifactWrite, capability.OperationGitClone, capability.OperationGitPush},
	}
	return runnerserver.NewRuntime(runnerserver.RuntimeConfig{HTTP: input.HTTP, Reconciler: reconciler,
		Dispatcher: dispatcher, MaxConcurrentJobs: input.Runner.MaxConcurrentJobs})
}

func buildRunnerRepositoryRegistry(ctx context.Context, native, subscriptionVerifier *github.Client,
	config commentrunner.Config) (repository.Registry, map[string]models.RepoScope, error) {
	if organization := strings.TrimSpace(config.Organization); organization != "" {
		if subscriptionVerifier == nil {
			return nil, nil, fmt.Errorf("organization runner subscription verifier is required")
		}
		current, err := native.GetNativeContext(ctx)
		if err != nil {
			return nil, nil, fmt.Errorf("resolve runner organization: %w", err)
		}
		var matches []github.NativeOrganizationContext
		for _, candidate := range current.Organizations {
			if strings.EqualFold(strings.TrimSpace(candidate.Name), organization) {
				matches = append(matches, candidate)
			}
		}
		if len(matches) != 1 {
			return nil, nil, fmt.Errorf("configured organization %q resolved to %d visible organizations", organization, len(matches))
		}
		organizationID, err := uuid.Parse(strings.TrimSpace(matches[0].ID))
		if err != nil || organizationID == uuid.Nil {
			return nil, nil, fmt.Errorf("configured organization %q returned an invalid UUID", organization)
		}
		subscriptionID, err := uuid.Parse(strings.TrimSpace(config.SubscriptionID))
		if err != nil || subscriptionID == uuid.Nil {
			return nil, nil, fmt.Errorf("organization runner subscription id is invalid")
		}
		subscription, err := subscriptionVerifier.VerifyNativeRunnerSubscription(ctx, organizationID, subscriptionID)
		if err != nil {
			return nil, nil, fmt.Errorf("read organization runner subscription: %w", err)
		}
		if err := validateOrganizationRunnerSubscription(subscription, organizationID, subscriptionID); err != nil {
			return nil, nil, err
		}
		registry, err := repository.NewOrganizationRegistry(native, native, organizationID, matches[0].Name)
		return registry, nil, err
	}
	scopes, err := webhook.ResolveRepositoryScopes(ctx, native, config.Repositories, nil)
	if err != nil {
		return nil, nil, err
	}
	registryScopes := cloneRunnerRepositoryScopes(scopes.ByRepository)
	return &repository.StaticRegistry{Bindings: native, Scopes: registryScopes},
		cloneRunnerRepositoryScopes(scopes.ByRepository), nil
}

func cloneRunnerRepositoryScopes(input map[string]models.RepoScope) map[string]models.RepoScope {
	if len(input) == 0 {
		return nil
	}
	result := make(map[string]models.RepoScope, len(input))
	for repository, scope := range input {
		result[repository] = scope
	}
	return result
}

func validateOrganizationRunnerSubscription(subscription github.NativeWebhookSubscription,
	organizationID, subscriptionID uuid.UUID) error {
	if subscription.ID != subscriptionID || subscription.OrganizationID != organizationID || subscription.RepositoryID != nil ||
		!strings.EqualFold(strings.TrimSpace(subscription.ScopeType), "organization") || !subscription.Active ||
		subscription.RevokedAt != nil || !strings.EqualFold(strings.TrimSpace(subscription.DeliveryFormat), "issue-spec.v1") ||
		!strings.EqualFold(strings.TrimSpace(subscription.SigningMode), "bearer") {
		return fmt.Errorf("configured webhook subscription is not an active organization-scoped issue-spec.v1 bearer subscription")
	}
	wanted := map[string]bool{"issue_comment.created": false, "issue_comment.edited": false}
	for _, eventType := range subscription.EventTypes {
		key := strings.ToLower(strings.TrimSpace(eventType))
		if _, ok := wanted[key]; !ok {
			return fmt.Errorf("configured organization runner subscription contains unsupported event type %q", eventType)
		}
		wanted[key] = true
	}
	if !wanted["issue_comment.created"] || !wanted["issue_comment.edited"] {
		return fmt.Errorf("configured organization runner subscription must include comment created and edited events")
	}
	return nil
}

func runnerServeCredentialRoot(statePath string) (string, error) {
	statePath = strings.TrimSpace(statePath)
	if statePath == "" {
		return "", fmt.Errorf("runner serve state path is required")
	}
	return filepath.Abs(statePath + ".credentials")
}
