package commands

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/capability"
	"github.com/higress-group/issue-spec/internal/codereview"
	"github.com/higress-group/issue-spec/internal/commentrunner"
	"github.com/higress-group/issue-spec/internal/commentrunner/credentials"
	webhook "github.com/higress-group/issue-spec/internal/commentrunner/intake/webhook"
	"github.com/higress-group/issue-spec/internal/commentrunner/jobs"
	repository "github.com/higress-group/issue-spec/internal/commentrunner/repository"
	runnerserver "github.com/higress-group/issue-spec/internal/commentrunner/server"
	crstate "github.com/higress-group/issue-spec/internal/commentrunner/state"
	"github.com/higress-group/issue-spec/internal/commentrunner/writeback"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/sandbox"
	"github.com/higress-group/issue-spec/internal/workspace"
)

type runnerServeRuntime interface{ Run(context.Context) error }

type runnerServeRuntimeInput struct {
	Profile                  auth.Profile
	ParentToken              string
	Runner                   commentrunner.Config
	Queue                    *webhook.Queue
	Store                    crstate.StateStore
	HTTP                     *runnerserver.Service
	GitCredentialCommand     string
	GitCredentialArgs        []string
	GitCredentialTimeout     time.Duration
	GitCredentialMaxOutput   int64
	GitCredentialConcurrency int
	ReconcileWorkers         int
	ReconcileLease           time.Duration
	Dependencies             *runnerServeRuntimeDependencies
}

// runnerServeRuntimeDependencies is a hermetic composition seam used by the
// command-level test. Production leaves it nil and always receives the concrete
// workspace, sandbox, acpx, artifact and writeback implementations below.
type runnerServeRuntimeDependencies struct {
	Workspaces               jobs.WorkspaceManager
	Sandbox                  jobs.SandboxPreparer
	Acpx                     jobs.AcpxFactory
	Artifacts                jobs.ArtifactProvider
	Writeback                jobs.Writeback
	IssueSpecBinary          string
	ProcessWorkspaceAdapters map[string]jobs.NoCheckoutLifecycle
}

var runnerServeBuildRuntime = defaultBuildRunnerServeRuntime
var runnerServeRun = func(ctx context.Context, runtime runnerServeRuntime) error { return runtime.Run(ctx) }

func defaultBuildRunnerServeRuntime(ctx context.Context, input runnerServeRuntimeInput) (runnerServeRuntime, error) {
	profile, err := input.Profile.Normalized()
	if err != nil || profile.Kind != auth.ProfileKindHosted || strings.TrimSpace(input.ParentToken) == "" {
		return nil, fmt.Errorf("runner serve runtime requires a self-hosted profile and parent PAT")
	}
	compatibility, err := github.NewClientWithOptions(github.ClientOptions{Host: profile.Hostname, BaseURL: profile.APIURL,
		Token: input.ParentToken, CAFile: profile.CAFile})
	if err != nil {
		return nil, err
	}
	native, err := github.NewClientWithOptions(github.ClientOptions{Host: profile.Hostname, BaseURL: profile.NativeAPIURL,
		Token: input.ParentToken, CAFile: profile.CAFile})
	if err != nil {
		return nil, err
	}
	scopes, err := webhook.ResolveRepositoryScopes(ctx, native, input.Runner.Repositories, nil)
	if err != nil {
		return nil, err
	}
	gitProvider, err := credentials.NewCommandGitProvider(credentials.CommandGitProviderConfig{Path: input.GitCredentialCommand,
		Args: input.GitCredentialArgs, Timeout: input.GitCredentialTimeout, MaxOutput: input.GitCredentialMaxOutput,
		MaxConcurrent: input.GitCredentialConcurrency})
	if err != nil {
		return nil, err
	}
	reconciler, err := webhook.NewReconciler(webhook.ReconcilerConfig{Queue: input.Queue, Store: input.Store,
		Backend: compatibility, Scopes: scopes, Runner: input.Runner,
		AuthorizationPolicy: commentrunner.AuthorizationPolicy{RunnerLogin: input.Runner.RunnerIdentity,
			AllowedUsers: input.Runner.AllowedUsers}, WorkerID: "runner-serve", Workers: input.ReconcileWorkers,
		LeaseDuration: input.ReconcileLease})
	if err != nil {
		return nil, err
	}
	credentialRoot, err := filepath.Abs(filepath.Join(filepath.Dir(input.Runner.StatePath), "credentials"))
	if err != nil {
		return nil, err
	}
	nativeHTTPClient, ok := native.HTTPClient.(*http.Client)
	if !ok {
		return nil, fmt.Errorf("runner serve native profile requires an HTTP client")
	}
	broker := &credentials.Broker{Profile: profile, Audience: profile.ServerInstanceID,
		Subject: input.Runner.RunnerIdentity, ParentToken: input.ParentToken, HTTPClient: nativeHTTPClient,
		Materializer: credentials.Materializer{Root: credentialRoot}, GitProvider: gitProvider, TTL: 5 * time.Minute,
		Scopes: []string{"read:user", "issues:read", "issues:write", "evidence:write"}}
	workspaces := jobs.WorkspaceManager(workspace.Manager{Root: input.Runner.WorkspaceRoot, Retention: input.Runner.WorkspaceRetention.Duration})
	sandboxer := jobs.SandboxPreparer(jobs.SandboxRunner{Config: sandbox.Config{UnsafeNoSandbox: input.Runner.UnsafeNoSandbox,
		BwrapPath: input.Runner.BwrapPath, HostGHConfigDir: input.Runner.GHConfigDir}})
	acpxFactory := jobs.AcpxFactory(jobs.AcpxAdapterFactory{Config: jobs.NewAcpxConfig(input.Runner)})
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
	processWorkspaceStore, err := crstate.NewProcessWorkspaceStoreAdapter(input.Store)
	if err != nil {
		return nil, err
	}
	var processWorkspaceAdapters map[string]jobs.NoCheckoutLifecycle
	if input.Dependencies != nil {
		processWorkspaceAdapters = input.Dependencies.ProcessWorkspaceAdapters
	} else {
		processWorkspaceAdapters, err = runnerOperatorProcessWorkspaceAdapters(ctx, profile)
		if err != nil {
			return nil, err
		}
	}
	processWorkspaceRuntime, err := jobs.NewProcessWorkspaceRuntime(workspaces, processWorkspaceStore, input.Runner.WorkspaceRoot, processWorkspaceAdapters)
	if err != nil {
		return nil, err
	}
	dispatcher := &jobs.Dispatcher{Store: input.Store,
		Repositories: repository.NativeResolver{Bindings: native, Scopes: scopes.ByRepository},
		Workspaces:   processWorkspaceRuntime, Sandbox: sandboxer, Acpx: acpxFactory, Artifacts: artifacts, Writeback: writebacks,
		AcpxBinary: input.Runner.AcpxPath, IssueSpecBinary: issueSpecBinary, CredentialBroker: broker,
		CredentialScopes: scopes.ByRepository, CapabilityPreflight: broker, CapabilityHost: profile.Hostname,
		RequiredOperations: []capability.Operation{capability.OperationIssueRead, capability.OperationIssueCommentWrite,
			capability.OperationArtifactWrite, capability.OperationGitClone, capability.OperationGitPush},
		EvidencePreGate: newRunnerEvidencePreGate(profile)}
	return runnerserver.NewRuntime(runnerserver.RuntimeConfig{HTTP: input.HTTP, Reconciler: reconciler,
		Dispatcher: dispatcher, MaxConcurrentJobs: input.Runner.MaxConcurrentJobs})
}

type operatorProcessWorkspaceLifecycle struct {
	provider        codereview.WorkspaceLifecycleProvider
	identity        crstate.ProcessWorkspaceProviderIdentity
	remoteAuthority string
}

func runnerOperatorProcessWorkspaceAdapters(ctx context.Context, profile auth.Profile) (map[string]jobs.NoCheckoutLifecycle, error) {
	registry, source, err := codereview.LoadOperatorRegistry(profile.OperatorRegistryFile)
	if err != nil {
		return nil, fmt.Errorf("load runner operator workspace adapters from %s: %w", source, err)
	}
	adapters := make(map[string]jobs.NoCheckoutLifecycle)
	for _, description := range registry.Descriptions() {
		if !providerDescriptionHasCapability(description, codereview.CapabilityWorkspaceReady) ||
			!providerDescriptionHasCapability(description, codereview.CapabilityWorkspaceCleanup) {
			return nil, fmt.Errorf("runner workspace adapter %q: %w: operator description must declare workspace.ready and workspace.cleanup",
				description.ProviderKey, codereview.ErrCapabilityMissing)
		}
		provider, err := registry.ResolveWorkspaceLifecycleProvider(ctx, description.ProviderKey)
		if err != nil {
			return nil, fmt.Errorf("resolve runner workspace adapter %q: %w", description.ProviderKey, err)
		}
		serverInstance := repository.SourceServer
		if description.ProviderKey == "github" {
			serverInstance = "public"
		}
		for _, authority := range description.RemoteAuthorities {
			host := authority
			if splitHost, _, splitErr := net.SplitHostPort(authority); splitErr == nil {
				host = splitHost
			}
			host = strings.ToLower(strings.Trim(strings.TrimSpace(host), "[]"))
			identity := crstate.ProcessWorkspaceProviderIdentity{ProviderKey: description.ProviderKey,
				ServerInstance: serverInstance, Host: host}
			key, err := jobs.ProcessWorkspaceAdapterKey(identity)
			if err != nil {
				return nil, fmt.Errorf("runner workspace adapter %q remote authority %q: %w", description.ProviderKey, authority, err)
			}
			if _, exists := adapters[key]; exists {
				return nil, fmt.Errorf("duplicate runner workspace adapter identity %s/%s/%s", identity.ProviderKey, identity.ServerInstance, identity.Host)
			}
			adapters[key] = operatorProcessWorkspaceLifecycle{provider: provider, identity: identity, remoteAuthority: authority}
		}
	}
	return adapters, nil
}

func providerDescriptionHasCapability(description codereview.ProviderDescription, required codereview.Capability) bool {
	for _, capability := range description.Capabilities {
		if capability == required {
			return true
		}
	}
	return false
}

func (a operatorProcessWorkspaceLifecycle) ValidateAssociation(ctx context.Context, association crstate.ProcessWorkspaceAssociation) error {
	if association.Provider != a.identity {
		return errors.New("operator workspace adapter provider identity mismatch")
	}
	_, err := codereview.RequireCapabilities(ctx, a.provider, codereview.CapabilityWorkspaceReady, codereview.CapabilityWorkspaceCleanup)
	return err
}

func (a operatorProcessWorkspaceLifecycle) Ready(ctx context.Context, association crstate.ProcessWorkspaceAssociation) (bool, error) {
	if err := a.ValidateAssociation(ctx, association); err != nil {
		return false, err
	}
	result, err := codereview.RunWorkspaceLifecycle(ctx, a.provider, operatorWorkspaceLifecycleRequest(codereview.WorkspaceReady, a.remoteAuthority, association))
	return err == nil && result.Confirmed, err
}

func (a operatorProcessWorkspaceLifecycle) Cleanup(ctx context.Context, association crstate.ProcessWorkspaceAssociation) (bool, error) {
	if err := a.ValidateAssociation(ctx, association); err != nil {
		return false, err
	}
	result, err := codereview.RunWorkspaceLifecycle(ctx, a.provider, operatorWorkspaceLifecycleRequest(codereview.WorkspaceCleanup, a.remoteAuthority, association))
	return err == nil && result.Confirmed, err
}

func operatorWorkspaceLifecycleRequest(kind codereview.WorkspaceLifecycleKind, remoteAuthority string, association crstate.ProcessWorkspaceAssociation) codereview.WorkspaceLifecycleRequest {
	return codereview.WorkspaceLifecycleRequest{Kind: kind, ProviderKey: association.Provider.ProviderKey,
		ServerInstance: association.Provider.ServerInstance, ProviderHost: association.Provider.Host, RemoteAuthority: remoteAuthority,
		Repository: association.Repository, ProcessID: association.ProcessID, WorkspaceID: association.WorkspaceID,
		ReservationID: association.ReservationID, ReservationIdentity: association.ReservationIdentity,
		RuntimeNamespace: association.RuntimeNamespace}
}
