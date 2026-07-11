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
		Materializer: credentials.Materializer{Root: credentialRoot}, GitProvider: gitProvider, TTL: 5 * time.Minute}
	dispatcher := &jobs.Dispatcher{Store: input.Store,
		Repositories: repository.NativeResolver{Bindings: native, Scopes: scopes.ByRepository},
		Workspaces:   workspace.Manager{Root: input.Runner.WorkspaceRoot, Retention: input.Runner.WorkspaceRetention.Duration},
		Sandbox: jobs.SandboxRunner{Config: sandbox.Config{UnsafeNoSandbox: input.Runner.UnsafeNoSandbox,
			BwrapPath: input.Runner.BwrapPath, HostGHConfigDir: input.Runner.GHConfigDir}},
		Acpx:       jobs.AcpxAdapterFactory{Config: jobs.NewAcpxConfig(input.Runner)},
		Artifacts:  &jobs.IssueSpecArtifactProvider{GitHub: compatibility},
		Writeback:  &writeback.Service{GitHub: compatibility, Store: input.Store},
		AcpxBinary: input.Runner.AcpxPath, IssueSpecBinary: issueSpecBinaryForRunner(), CredentialBroker: broker,
		CredentialScopes: scopes.ByRepository}
	return runnerserver.NewRuntime(runnerserver.RuntimeConfig{HTTP: input.HTTP, Reconciler: reconciler,
		Dispatcher: dispatcher, MaxConcurrentJobs: input.Runner.MaxConcurrentJobs})
}
