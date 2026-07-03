package commands

import (
	"context"
	"flag"
	"fmt"
	"strings"

	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/commentrunner"
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
}

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
		a.errorf("runner poll dispatch is not implemented by the command/config/preflight foundation; rerun with --dry-run to inspect planned work\n")
		return 1
	}
	report := a.runRunnerPreflight(ctx, cfg)
	result := runnerDryRunResult{
		OK:        report.OK,
		Mode:      "dry-run",
		Once:      opts.Once,
		Actions:   plannedRunnerPollActions(cfg, opts.Once),
		Config:    cfg,
		Preflight: report,
	}
	if opts.JSON {
		if code := a.outputJSON(result); code != 0 {
			return code
		}
	} else {
		a.printRunnerDryRun(result)
	}
	if report.OK {
		return 0
	}
	return 1
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
	ghConfigDir := fs.String("gh-config-dir", "", "temporary gh config directory for sandboxed issue-spec CLI auth")
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
		SelectBackend: a.selectBackend,
	})
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
		"check notification intake and repository comments fallback",
		"dry-run only: skip GitHub writes, state mutation, workspace changes, sandbox execution, and acpx dispatch",
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
