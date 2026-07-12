package commands

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/higress-group/issue-spec/internal/auth"
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
	dispatcher := &jobs.Dispatcher{Store: input.Store,
		Repositories: repository.NativeResolver{Bindings: native, Scopes: scopes.ByRepository},
		Workspaces:   workspaces, Sandbox: sandboxer, Acpx: acpxFactory, Artifacts: artifacts, Writeback: writebacks,
		AcpxBinary: input.Runner.AcpxPath, IssueSpecBinary: issueSpecBinary, CredentialBroker: broker,
		CredentialScopes: scopes.ByRepository, EvidencePreGate: newRunnerEvidencePreGate(profile)}
	return runnerserver.NewRuntime(runnerserver.RuntimeConfig{HTTP: input.HTTP, Reconciler: reconciler,
		Dispatcher: dispatcher, MaxConcurrentJobs: input.Runner.MaxConcurrentJobs})
}
