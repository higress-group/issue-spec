package commands

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/commentrunner"
	"github.com/higress-group/issue-spec/internal/commentrunner/intake"
	"github.com/higress-group/issue-spec/internal/commentrunner/jobs"
	crstate "github.com/higress-group/issue-spec/internal/commentrunner/state"
	"github.com/higress-group/issue-spec/internal/commentrunner/writeback"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/sandbox"
	"github.com/higress-group/issue-spec/internal/workspace"
)

type runnerCommandOptions struct {
	Once   bool
	DryRun bool
	JSON   bool
}

type runnerDryRunResult struct {
	OK        bool                          `json:"ok"`
	Mode      string                        `json:"mode"`
	Once      bool                          `json:"once"`
	Actions   []string                      `json:"actions"`
	Config    commentrunner.Config          `json:"config"`
	Preflight commentrunner.PreflightReport `json:"preflight"`
	Reconcile *jobs.ReconcileResult         `json:"reconcile,omitempty"`
	Intake    *intake.Result                `json:"intake,omitempty"`
	Dispatch  *jobs.Result                  `json:"dispatch,omitempty"`
	Error     string                        `json:"error,omitempty"`
}

var runnerExecutable = os.Executable

func (a *app) runRunner(ctx context.Context, args []string) int {
	if len(args) == 0 {
		a.errorf("usage: issue-spec runner poll|preflight ...\n")
		return 2
	}
	switch args[0] {
	case "poll":
		return a.runRunnerPoll(ctx, args[1:])
	case "preflight":
		return a.runRunnerPreflightCommand(ctx, args[1:])
	default:
		a.errorf("unknown runner command %q\n", args[0])
		return 2
	}
}

func (a *app) runRunnerPoll(ctx context.Context, args []string) int {
	cfg, opts, ok := a.parseRunnerOptions(args, true)
	if !ok {
		return 2
	}
	if !opts.DryRun {
		report := a.runRunnerPreflight(ctx, cfg)
		if !report.OK {
			result := runnerDryRunResult{
				OK:        false,
				Mode:      "run",
				Once:      opts.Once,
				Actions:   actualRunnerPollActions(cfg, opts.Once),
				Config:    cfg,
				Preflight: report,
			}
			if code := a.printRunnerPollResult(result, opts.JSON); code != 0 {
				return code
			}
			return 1
		}
		for {
			if err := ctx.Err(); err != nil {
				return 0
			}
			result := a.runRunnerPollCycle(ctx, cfg, opts, report)
			if code := a.printRunnerPollResult(result, opts.JSON); code != 0 {
				return code
			}
			if !result.OK {
				if ctx.Err() != nil {
					return 0
				}
				if !opts.Once && runnerPollRecoverableIntakeFailure(result) {
					if !waitForNextRunnerPoll(ctx, result.Intake) {
						return 0
					}
					continue
				}
				return 1
			}
			if opts.Once {
				return 0
			}
			if !waitForNextRunnerPoll(ctx, result.Intake) {
				return 0
			}
		}
	}
	report := a.runRunnerPreflight(ctx, cfg)
	var intakeResult *intake.Result
	intakeErr := ""
	if report.OK {
		result, err := a.runRunnerIntake(ctx, cfg, intake.Options{DryRun: true})
		if err != nil {
			intakeErr = err.Error()
		} else {
			intakeResult = &result
			if !result.OK {
				intakeErr = "intake reported failure"
			}
		}
	}
	result := runnerDryRunResult{
		OK:        report.OK,
		Mode:      "dry-run",
		Once:      opts.Once,
		Actions:   plannedRunnerPollActions(cfg, opts.Once),
		Config:    cfg,
		Preflight: report,
		Intake:    intakeResult,
		Error:     intakeErr,
	}
	if intakeErr != "" {
		result.OK = false
	}
	if opts.JSON {
		if code := a.outputJSON(result); code != 0 {
			return code
		}
	} else {
		a.printRunnerDryRun(result)
	}
	if result.OK {
		return 0
	}
	return 1
}

func runnerPollRecoverableIntakeFailure(result runnerDryRunResult) bool {
	return result.Intake != nil && !result.Intake.OK
}

func (a *app) runRunnerPollCycle(ctx context.Context, cfg commentrunner.Config, opts runnerCommandOptions, report commentrunner.PreflightReport) runnerDryRunResult {
	var reconcileResult *jobs.ReconcileResult
	var intakeResult *intake.Result
	var dispatchResult *jobs.Result
	runErr := ""
	reconcile, err := a.runRunnerReconcile(ctx, cfg)
	if err != nil {
		runErr = err.Error()
	} else {
		reconcileResult = &reconcile
		result, err := a.runRunnerIntake(ctx, cfg, intake.Options{})
		if err != nil {
			runErr = err.Error()
		} else {
			intakeResult = &result
			if !result.OK {
				runErr = "intake reported failure"
			} else {
				dispatch, err := a.runRunnerDispatch(ctx, cfg)
				if err != nil {
					runErr = err.Error()
				}
				dispatchResult = &dispatch
			}
		}
	}
	return runnerDryRunResult{
		OK:        report.OK && runErr == "",
		Mode:      "run",
		Once:      opts.Once,
		Actions:   actualRunnerPollActions(cfg, opts.Once),
		Config:    cfg,
		Preflight: report,
		Reconcile: reconcileResult,
		Intake:    intakeResult,
		Dispatch:  dispatchResult,
		Error:     runErr,
	}
}

func (a *app) printRunnerPollResult(result runnerDryRunResult, jsonOut bool) int {
	if jsonOut {
		return a.outputJSON(result)
	}
	a.printRunnerPoll(result)
	return 0
}

func waitForNextRunnerPoll(ctx context.Context, result *intake.Result) bool {
	delay := time.Duration(0)
	if result != nil {
		delay = result.Next.PollAfter
		if delay <= 0 && !result.Next.PollAt.IsZero() {
			delay = time.Until(result.Next.PollAt)
		}
	}
	if delay <= 0 {
		select {
		case <-ctx.Done():
			return false
		default:
			return true
		}
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (a *app) runRunnerPreflightCommand(ctx context.Context, args []string) int {
	cfg, opts, ok := a.parseRunnerOptions(args, false)
	if !ok {
		return 2
	}
	report := a.runRunnerPreflight(ctx, cfg)
	if opts.JSON {
		if code := a.outputJSON(report); code != 0 {
			return code
		}
	} else {
		a.printPreflightReport(report)
	}
	if report.OK {
		return 0
	}
	return 1
}

func (a *app) parseRunnerOptions(args []string, includePollFlags bool) (commentrunner.Config, runnerCommandOptions, bool) {
	fs := newFlagSet("runner", a.err)
	var repoValues stringListFlag
	var claudeTools stringListFlag
	host := fs.String("hostname", "", "GitHub hostname")
	backend := fs.String("backend", "", "GitHub backend mode: auto, gh, or rest")
	runner := fs.String("runner", "", "GitHub login for the polling runner identity")
	statePath := fs.String("state", "", "runner state path")
	pollInterval := fs.Duration("poll-interval", 0, "notification poll interval")
	fallbackInterval := fs.Duration("fallback-interval", 0, "repository comments fallback interval")
	maxConcurrency := fs.Int("max-concurrency", 0, "maximum concurrent runner jobs")
	acpxPath := fs.String("acpx-path", "", "acpx binary path")
	agent := fs.String("agent", "", "coordinator code agent: codex or claude")
	model := fs.String("model", "", "coordinator model/profile name")
	workspaceRoot := fs.String("workspace-root", "", "managed workspace root")
	workspaceRetention := fs.Duration("workspace-retention", 0, "managed workspace retention duration")
	bwrapPath := fs.String("bwrap-path", "", "bubblewrap binary path")
	unsafeNoSandbox := fs.Bool("unsafe-no-sandbox", false, "explicitly disable the default bubblewrap filesystem boundary")
	ghConfigDir := fs.String("gh-config-dir", "", "host gh config directory to mirror for sandboxed issue-spec CLI auth")
	allowCancel := fs.Bool("allow-cancel", true, "allow authorized cancellation commands")
	codexFullAccess := fs.Bool("codex-agent-full-access", true, "require Codex agent-full-access policy for workflow CLI/shell work")
	claudeIncludeSettings := fs.Bool("claude-include-user-settings", true, "set ACPX_CLAUDE_INCLUDE_USER_SETTINGS for Claude Code")
	jsonOut := fs.Bool("json", false, "write JSON output")
	fs.Var(&repoValues, "repo", "repository owner/name; repeat or comma-separate for multiple repositories")
	fs.Var(&claudeTools, "claude-allowed-tools", "Claude allowed tools; repeat or comma-separate, usually Task,Bash")

	opts := runnerCommandOptions{}
	var once *bool
	var dryRun *bool
	if includePollFlags {
		once = fs.Bool("once", false, "run one poll cycle")
		dryRun = fs.Bool("dry-run", false, "print planned polling and preflight actions without GitHub writes or acpx dispatch")
	}
	if err := fs.Parse(args); err != nil {
		return commentrunner.Config{}, opts, false
	}
	seen := visitedFlags(fs)
	if includePollFlags {
		opts.Once = *once
		opts.DryRun = *dryRun
	}
	opts.JSON = *jsonOut

	cfg, err := commentrunner.DefaultConfigFromEnv()
	if err != nil {
		a.errorf("%v\n", err)
		return commentrunner.Config{}, opts, false
	}
	if seen["hostname"] {
		cfg.Hostname = *host
	}
	if seen["backend"] {
		mode, err := auth.ParseGitHubBackendMode(*backend)
		if err != nil {
			a.errorf("%v\n", err)
			return commentrunner.Config{}, opts, false
		}
		cfg.GitHubBackend = mode
	}
	if seen["repo"] {
		cfg.Repositories = repoValues.Values()
	}
	if seen["runner"] {
		cfg.RunnerIdentity = *runner
	}
	if seen["state"] {
		cfg.StatePath = *statePath
	}
	if seen["poll-interval"] {
		cfg.PollInterval = commentrunner.NewDuration(*pollInterval)
	}
	if seen["fallback-interval"] {
		cfg.FallbackInterval = commentrunner.NewDuration(*fallbackInterval)
	}
	if seen["max-concurrency"] {
		cfg.MaxConcurrentJobs = *maxConcurrency
	}
	if seen["acpx-path"] {
		cfg.AcpxPath = *acpxPath
	}
	if seen["agent"] {
		cfg.Agent.Kind = *agent
	}
	if seen["model"] {
		cfg.Agent.Model = *model
	}
	if seen["workspace-root"] {
		cfg.WorkspaceRoot = *workspaceRoot
	}
	if seen["workspace-retention"] {
		cfg.WorkspaceRetention = commentrunner.NewDuration(*workspaceRetention)
	}
	if seen["bwrap-path"] {
		cfg.BwrapPath = *bwrapPath
	}
	if seen["unsafe-no-sandbox"] {
		cfg.UnsafeNoSandbox = *unsafeNoSandbox
	}
	if seen["gh-config-dir"] {
		cfg.GHConfigDir = *ghConfigDir
	}
	if seen["allow-cancel"] {
		cfg.CancellationEnabled = *allowCancel
	}
	if seen["codex-agent-full-access"] {
		cfg.Agent.CodexAgentFullAccess = *codexFullAccess
	}
	if seen["claude-include-user-settings"] {
		cfg.Agent.ClaudeIncludeUserSettings = *claudeIncludeSettings
	}
	if seen["claude-allowed-tools"] {
		cfg.Agent.ClaudeAllowedTools = claudeTools.Values()
	}
	cfg = cfg.Normalized()
	if err := cfg.Validate(); err != nil {
		a.errorf("%v\n", err)
		return commentrunner.Config{}, opts, false
	}
	return cfg, opts, true
}

func (a *app) runRunnerPreflight(ctx context.Context, cfg commentrunner.Config) commentrunner.PreflightReport {
	if a.runnerPreflight != nil {
		return a.runnerPreflight(ctx, cfg)
	}
	return commentrunner.RunPreflight(ctx, cfg, commentrunner.PreflightDependencies{
		SelectBackend: func(ctx context.Context, _ string) (auth.GitHubBackendSelection, error) {
			return a.selectBackendForRunner(ctx, cfg)
		},
		OpenBackend: func(ctx context.Context, selection auth.GitHubBackendSelection) (commentrunner.PreflightRunnerBackend, error) {
			backend, err := a.backendForSelection(ctx, selection)
			if err != nil {
				return nil, err
			}
			runnerBackend, ok := backend.(commentrunner.PreflightRunnerBackend)
			if !ok {
				return nil, fmt.Errorf("selected GitHub backend does not support runner preflight")
			}
			return runnerBackend, nil
		},
	})
}

func (a *app) runRunnerIntake(ctx context.Context, cfg commentrunner.Config, opts intake.Options) (intake.Result, error) {
	if a.runnerIntake != nil {
		return a.runnerIntake(ctx, cfg, opts)
	}
	selection, err := a.selectBackendForRunner(ctx, cfg)
	if err != nil {
		return intake.Result{}, err
	}
	backend, err := a.backendForSelection(ctx, selection)
	if err != nil {
		return intake.Result{}, err
	}
	runnerBackend, ok := backend.(intake.Backend)
	if !ok {
		return intake.Result{}, fmt.Errorf("selected GitHub backend does not support runner intake")
	}
	store, err := crstate.OpenFileStore(cfg.StatePath)
	if err != nil {
		return intake.Result{}, err
	}
	defer store.Close()
	return intake.RunOnce(ctx, cfg, runnerBackend, store, opts)
}

func (a *app) runRunnerReconcile(ctx context.Context, cfg commentrunner.Config) (jobs.ReconcileResult, error) {
	if a.runnerReconcile != nil {
		return a.runnerReconcile(ctx, cfg)
	}
	selection, err := a.selectBackendForRunner(ctx, cfg)
	if err != nil {
		return jobs.ReconcileResult{}, err
	}
	backend, err := a.backendForSelection(ctx, selection)
	if err != nil {
		return jobs.ReconcileResult{}, err
	}
	runnerBackend, ok := backend.(github.RunnerOperations)
	if !ok {
		return jobs.ReconcileResult{}, fmt.Errorf("selected GitHub backend does not support runner status writeback")
	}
	store, err := crstate.OpenFileStore(cfg.StatePath)
	if err != nil {
		return jobs.ReconcileResult{}, err
	}
	defer store.Close()
	dispatcher := jobs.Dispatcher{
		Store: store,
		Workspaces: workspace.Manager{
			Root:      cfg.WorkspaceRoot,
			Retention: cfg.WorkspaceRetention.Duration,
		},
		Sandbox: jobs.SandboxRunner{Config: sandbox.Config{
			UnsafeNoSandbox: cfg.UnsafeNoSandbox,
			BwrapPath:       cfg.BwrapPath,
			HostGHConfigDir: cfg.GHConfigDir,
		}},
		Acpx:      jobs.AcpxAdapterFactory{Config: jobs.NewAcpxConfig(cfg)},
		Writeback: &writeback.Service{GitHub: runnerBackend, Store: store},
	}
	return dispatcher.Reconcile(ctx)
}

func (a *app) runRunnerDispatch(ctx context.Context, cfg commentrunner.Config) (jobs.Result, error) {
	if a.runnerDispatch != nil {
		return a.runnerDispatch(ctx, cfg)
	}
	selection, err := a.selectBackendForRunner(ctx, cfg)
	if err != nil {
		return jobs.Result{}, err
	}
	backend, err := a.backendForSelection(ctx, selection)
	if err != nil {
		return jobs.Result{}, err
	}
	runnerBackend, ok := backend.(github.RunnerOperations)
	if !ok {
		return jobs.Result{}, fmt.Errorf("selected GitHub backend does not support runner status writeback")
	}
	store, err := crstate.OpenFileStore(cfg.StatePath)
	if err != nil {
		return jobs.Result{}, err
	}
	defer store.Close()
	dispatcher := jobs.Dispatcher{
		Store:        store,
		Repositories: jobs.StaticRepositoryResolver{Hostname: cfg.Hostname},
		Workspaces: workspace.Manager{
			Root:      cfg.WorkspaceRoot,
			Retention: cfg.WorkspaceRetention.Duration,
		},
		Sandbox: jobs.SandboxRunner{Config: sandbox.Config{
			UnsafeNoSandbox: cfg.UnsafeNoSandbox,
			BwrapPath:       cfg.BwrapPath,
			HostGHConfigDir: cfg.GHConfigDir,
		}},
		Acpx:            jobs.AcpxAdapterFactory{Config: jobs.NewAcpxConfig(cfg)},
		Artifacts:       &jobs.IssueSpecArtifactProvider{GitHub: runnerBackend},
		Writeback:       &writeback.Service{GitHub: runnerBackend, Store: store},
		IssueSpecBinary: issueSpecBinaryForRunner(),
	}
	if cfg.MaxConcurrentJobs > 1 {
		return dispatcher.RunReady(ctx, cfg.MaxConcurrentJobs)
	}
	return dispatcher.RunNext(ctx)
}

func issueSpecBinaryForRunner() string {
	path, err := runnerExecutable()
	if err == nil && strings.TrimSpace(path) != "" {
		return strings.TrimSpace(path)
	}
	return "issue-spec"
}

func plannedRunnerPollActions(cfg commentrunner.Config, once bool) []string {
	cfg = cfg.Normalized()
	cycle := "poll configured repositories continuously"
	if once {
		cycle = "poll configured repositories once"
	}
	return []string{
		"load trusted runner config",
		"run preflight checks",
		cycle + ": " + strings.Join(cfg.Repositories, ", "),
		"on real runs: reconcile in-flight jobs before polling new comments",
		"check notification intake and repository comments fallback",
		"dry-run only: skip GitHub writes, state persistence, workspace changes, sandbox execution, and acpx dispatch",
	}
}

func actualRunnerPollActions(cfg commentrunner.Config, once bool) []string {
	cfg = cfg.Normalized()
	cycle := "poll configured repositories continuously"
	if once {
		cycle = "poll configured repositories once"
	}
	dispatchAction := "process one cancellation or dispatch one ready job"
	if cfg.MaxConcurrentJobs > 1 {
		dispatchAction = fmt.Sprintf("process one cancellation or dispatch up to %d ready jobs", cfg.MaxConcurrentJobs)
	}
	return []string{
		"load trusted runner config",
		"run preflight checks",
		"reconcile in-flight jobs before polling",
		cycle + ": " + strings.Join(cfg.Repositories, ", "),
		dispatchAction,
	}
}

func (a *app) printRunnerDryRun(result runnerDryRunResult) {
	fmt.Fprintln(a.out, "runner poll dry-run")
	fmt.Fprintf(a.out, "repositories: %s\n", strings.Join(result.Config.Repositories, ", "))
	fmt.Fprintf(a.out, "runner: %s\n", result.Config.RunnerIdentity)
	fmt.Fprintf(a.out, "backend: %s\n", result.Config.GitHubBackend)
	fmt.Fprintln(a.out, "planned actions:")
	for _, action := range result.Actions {
		fmt.Fprintf(a.out, "- %s\n", action)
	}
	a.printPreflightReport(result.Preflight)
	if result.Intake != nil {
		fmt.Fprintf(a.out, "intake: commands=%d jobs=%d cancellations=%d next_poll=%s\n", len(result.Intake.Commands), len(result.Intake.Jobs), len(result.Intake.Cancellations), result.Intake.Next.PollAt.Format(time.RFC3339))
	}
	if result.Error != "" {
		fmt.Fprintf(a.out, "intake error: %s\n", result.Error)
	}
}

func (a *app) printRunnerPoll(result runnerDryRunResult) {
	fmt.Fprintln(a.out, "runner poll")
	fmt.Fprintf(a.out, "repositories: %s\n", strings.Join(result.Config.Repositories, ", "))
	a.printPreflightReport(result.Preflight)
	if result.Reconcile != nil {
		fmt.Fprintf(a.out, "reconcile: reconciled=%d running=%d completed=%d failed=%d cancelled=%d interrupted=%d queued=%d\n", result.Reconcile.Reconciled, result.Reconcile.Running, result.Reconcile.Completed, result.Reconcile.Failed, result.Reconcile.Cancelled, result.Reconcile.Interrupted, result.Reconcile.Queued)
	}
	if result.Intake != nil {
		fmt.Fprintf(a.out, "intake: commands=%d jobs=%d cancellations=%d next_poll=%s\n", len(result.Intake.Commands), len(result.Intake.Jobs), len(result.Intake.Cancellations), result.Intake.Next.PollAt.Format(time.RFC3339))
	}
	if result.Dispatch != nil {
		if result.Dispatch.ExecutedCount > 1 {
			fmt.Fprintf(a.out, "dispatch: executed=%v jobs=%d first_job=%s status=%s reason=%s\n", result.Dispatch.Executed, result.Dispatch.ExecutedCount, result.Dispatch.JobID, result.Dispatch.Status, result.Dispatch.Reason)
		} else {
			fmt.Fprintf(a.out, "dispatch: executed=%v job=%s status=%s reason=%s\n", result.Dispatch.Executed, result.Dispatch.JobID, result.Dispatch.Status, result.Dispatch.Reason)
		}
	}
	if result.Error != "" {
		fmt.Fprintf(a.out, "runner error: %s\n", result.Error)
	}
}

func (a *app) printPreflightReport(report commentrunner.PreflightReport) {
	fmt.Fprintln(a.out, "preflight:")
	for _, check := range report.Checks {
		fmt.Fprintf(a.out, "- %s: %s", check.Name, check.Status)
		if check.Detail != "" {
			fmt.Fprintf(a.out, " - %s", check.Detail)
		}
		if check.Hint != "" {
			fmt.Fprintf(a.out, " (%s)", check.Hint)
		}
		fmt.Fprintln(a.out)
	}
}

type stringListFlag []string

func (f *stringListFlag) Set(value string) error {
	*f = append(*f, value)
	return nil
}

func (f *stringListFlag) String() string {
	return strings.Join(f.Values(), ",")
}

func (f *stringListFlag) Values() []string {
	var values []string
	seen := map[string]bool{}
	for _, raw := range *f {
		for _, part := range strings.Split(raw, ",") {
			value := strings.TrimSpace(part)
			if value == "" || seen[value] {
				continue
			}
			values = append(values, value)
			seen[value] = true
		}
	}
	return values
}

func visitedFlags(fs *flag.FlagSet) map[string]bool {
	seen := map[string]bool{}
	fs.Visit(func(f *flag.Flag) {
		seen[f.Name] = true
	})
	return seen
}
