package commands

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/higress-group/issue-spec/internal/commentrunner"
)

func (a *app) runRunner(ctx context.Context, args []string) int {
	if len(args) == 0 {
		a.errorf("usage: issue-spec runner poll [flags]\n")
		return 2
	}
	switch args[0] {
	case "poll":
		return a.runRunnerPoll(ctx, args[1:])
	default:
		a.errorf("unknown runner command %q\n", args[0])
		return 2
	}
}

func (a *app) runRunnerPoll(_ context.Context, args []string) int {
	fs := newFlagSet("runner poll", a.err)
	repos := fs.String("repo", "", "comma-separated repository owner/name list")
	backendMode := fs.String("backend-mode", "gh", "GitHub backend mode")
	statePath := fs.String("state-path", "", "runner state path")
	workspaceRoot := fs.String("workspace-root", "", "workspace root")
	bwrapPath := fs.String("bwrap-path", "bwrap", "bubblewrap path")
	unsafeNoSandbox := fs.Bool("unsafe-no-sandbox", false, "disable sandboxing")
	acpxPath := fs.String("acpx-path", "acpx", "acpx path")
	agent := fs.String("agent", "codex", "agent kind")
	model := fs.String("model", "", "agent model")
	concurrency := fs.Int("concurrency", 1, "maximum concurrent jobs")
	cancelEnabled := fs.Bool("cancel", false, "enable turn-level cancellation")
	once := fs.Bool("once", false, "run a single poll iteration")
	dryRun := fs.Bool("dry-run", false, "print planned actions without writes")
	jsonOut := fs.Bool("json", false, "write JSON output")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg := commentrunner.Config{
		BackendMode:     strings.TrimSpace(*backendMode),
		Repositories:    splitCSV(*repos),
		StatePath:       strings.TrimSpace(*statePath),
		WorkspaceRoot:   strings.TrimSpace(*workspaceRoot),
		BwrapPath:       strings.TrimSpace(*bwrapPath),
		UnsafeNoSandbox: *unsafeNoSandbox,
		AcpxPath:        strings.TrimSpace(*acpxPath),
		Agent:           strings.TrimSpace(*agent),
		Model:           strings.TrimSpace(*model),
		Concurrency:     *concurrency,
		CancelEnabled:   *cancelEnabled,
	}
	if !*once || !*dryRun {
		a.errorf("runner poll is only implemented for --once --dry-run in this build\n")
		return 2
	}
	results := commentrunner.ProviderSet{
		fakePreflightProvider{name: "github-auth", category: commentrunner.PreflightGitHubAuth, message: "GitHub auth preflight is represented by a fake provider"},
		fakePreflightProvider{name: "repository-watch", category: commentrunner.PreflightRepositoryWatch, message: "repository watch preflight is represented by a fake provider"},
		fakePreflightProvider{name: "bwrap", category: commentrunner.PreflightBwrap, message: bwrapPreflightMessage(cfg)},
		fakePreflightProvider{name: "unsafe-mode", category: commentrunner.PreflightUnsafeMode, message: unsafeModeMessage(cfg)},
		fakePreflightProvider{name: "temp-gh-config-dir", category: commentrunner.PreflightTempGHConfigDir, message: "temporary GH_CONFIG_DIR issue-spec CLI auth is represented by a fake provider"},
		fakePreflightProvider{name: "acpx", category: commentrunner.PreflightAcpx, message: acpxPreflightMessage(cfg)},
		fakePreflightProvider{name: "codex", category: commentrunner.PreflightCodex, message: "Codex preflight is represented by a fake provider"},
		fakePreflightProvider{name: "claude", category: commentrunner.PreflightClaude, message: "Claude preflight is represented by a fake provider"},
	}.Collect(cfg)

	plan := map[string]any{
		"ok":        true,
		"mode":      "runner poll",
		"dry_run":   true,
		"once":      true,
		"config":    cfg,
		"preflight": results,
		"actions":   plannedRunnerActions(cfg, results),
	}
	if *jsonOut {
		return a.outputJSON(plan)
	}
	fmt.Fprintln(a.out, "planned runner poll dry-run")
	fmt.Fprintf(a.out, "backend mode: %s\n", cfg.BackendMode)
	if len(cfg.Repositories) > 0 {
		fmt.Fprintf(a.out, "repositories: %s\n", strings.Join(cfg.Repositories, ", "))
	}
	for _, action := range plannedRunnerActions(cfg, results) {
		fmt.Fprintf(a.out, "- %s\n", action)
	}
	for _, result := range results {
		status := "ok"
		if !result.OK {
			status = "blocked"
		}
		fmt.Fprintf(a.out, "[%s] %s: %s\n", status, result.Category, result.Message)
	}
	return 0
}

type fakePreflightProvider struct {
	name     string
	category commentrunner.PreflightCategory
	message  string
}

func (f fakePreflightProvider) Category() commentrunner.PreflightCategory { return f.category }

func (f fakePreflightProvider) Check(commentrunner.Config) commentrunner.PreflightResult {
	return commentrunner.PreflightResult{Category: f.category, Provider: f.name, OK: true, Message: f.message}
}

func splitCSV(value string) []string {
	var out []string
	for _, token := range strings.Split(value, ",") {
		token = strings.TrimSpace(token)
		if token != "" {
			out = append(out, token)
		}
	}
	return out
}

func bwrapPreflightMessage(cfg commentrunner.Config) string {
	if cfg.BwrapPath == "" {
		return "missing bwrap path: configure --bwrap-path to enable sandbox preflight"
	}
	return fmt.Sprintf("bwrap path configured: %s", cfg.BwrapPath)
}

func unsafeModeMessage(cfg commentrunner.Config) string {
	if cfg.UnsafeNoSandbox {
		return "unsafe no-sandbox mode requested"
	}
	return "sandbox remains enabled"
}

func acpxPreflightMessage(cfg commentrunner.Config) string {
	if strings.TrimSpace(cfg.AcpxPath) == "" {
		return "missing acpx path: configure --acpx-path to enable acpx dispatch"
	}
	return fmt.Sprintf("acpx path configured: %s", cfg.AcpxPath)
}

func plannedRunnerActions(cfg commentrunner.Config, results []commentrunner.PreflightResult) []string {
	actions := []string{"load runner config"}
	for _, repo := range cfg.Repositories {
		actions = append(actions, fmt.Sprintf("poll repository %s", repo))
	}
	sort.SliceStable(results, func(i, j int) bool { return results[i].Category < results[j].Category })
	for _, result := range results {
		actions = append(actions, fmt.Sprintf("preflight %s", result.Category))
	}
	if cfg.WorkspaceRoot != "" {
		actions = append(actions, filepath.Clean(cfg.WorkspaceRoot))
	}
	return actions
}
