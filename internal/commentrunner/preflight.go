package commentrunner

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/github"
)

const (
	CheckOK      = "ok"
	CheckWarning = "warning"
	CheckError   = "error"
	CheckSkipped = "skipped"

	BwrapPathEnv = "ISSUE_SPEC_BWRAP_PATH"
)

const (
	bwrapInstallHint = "Install or upgrade bubblewrap, or explicitly rerun with --unsafe-no-sandbox to disable the filesystem boundary."
	acpxInstallHint  = "npm install -g acpx@latest"
)

type PreflightCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
	Hint   string `json:"hint,omitempty"`
}

type PreflightReport struct {
	OK     bool             `json:"ok"`
	Config Config           `json:"config"`
	Checks []PreflightCheck `json:"checks"`
}

type PreflightDependencies struct {
	SelectBackend           func(context.Context, string) (auth.GitHubBackendSelection, error)
	OpenBackend             func(context.Context, auth.GitHubBackendSelection) (PreflightRunnerBackend, error)
	OpenNotificationBackend func(context.Context, Config) (PreflightNotificationBackend, error)
	LookPath                func(string) (string, error)
	RunCommand              func(context.Context, string, ...string) ([]byte, error)
}

type PreflightRunnerBackend interface {
	BackendInfo() github.BackendInfo
	GetRepositorySubscription(context.Context, string) (github.RepositorySubscriptionResult, error)
}

type PreflightNotificationBackend interface {
	PreflightRunnerBackend
	GetUser(context.Context) (github.User, []string, error)
}

func RunPreflight(ctx context.Context, cfg Config, deps PreflightDependencies) PreflightReport {
	cfg = cfg.Normalized()
	deps = deps.withDefaults()
	report := PreflightReport{Config: cfg}
	if err := cfg.Validate(); err != nil {
		report.add(PreflightCheck{Name: "config", Status: CheckError, Detail: err.Error()})
		report.finish()
		return report
	}

	selection, backendErr := deps.SelectBackend(ctx, cfg.Hostname)
	var runnerBackend PreflightRunnerBackend
	if backendErr != nil {
		report.add(PreflightCheck{Name: "github-backend", Status: CheckError, Detail: backendErr.Error(), Hint: "Run issue-spec auth status --json or configure ISSUE_SPEC_GITHUB_BACKEND."})
	} else {
		report.add(PreflightCheck{
			Name:   "github-backend",
			Status: CheckOK,
			Detail: fmt.Sprintf("%s backend selected for %s (%s)", selection.Name, selection.Host, selection.SelectionSource),
		})
		var err error
		runnerBackend, err = deps.OpenBackend(ctx, selection)
		if err != nil {
			report.add(PreflightCheck{Name: "runner-backend", Status: CheckError, Detail: err.Error(), Hint: "Selected GitHub backend must support runner repository subscription checks."})
		} else {
			info := runnerBackend.BackendInfo()
			report.add(PreflightCheck{Name: "runner-backend", Status: CheckOK, Detail: fmt.Sprintf("%s backend ready for %s", info.Name, info.Host)})
		}
	}

	if needsGHCheck(cfg, selection, backendErr) {
		report.add(binaryCheck(deps, "gh-cli", "gh", "Install GitHub CLI or use ISSUE_SPEC_GITHUB_BACKEND=rest with an issue-spec token."))
	} else {
		report.add(PreflightCheck{Name: "gh-cli", Status: CheckSkipped, Detail: "selected backend does not require gh"})
	}

	watchBackend := runnerBackend
	watchErr := backendErr
	watchCheckPrefix := "repository-watch:"
	if cfg.NotificationTokenEnv != "" {
		watchCheckPrefix = "notification-watch:"
		notificationBackend, notificationErr := deps.OpenNotificationBackend(ctx, cfg)
		if notificationErr != nil {
			report.add(PreflightCheck{Name: "notification-backend", Status: CheckError, Detail: notificationErr.Error(), Hint: "Set a readable bot token in " + cfg.NotificationTokenEnv + " or omit --notification-runner."})
			watchBackend = nil
			watchErr = notificationErr
		} else if notificationBackend == nil {
			report.add(PreflightCheck{Name: "notification-backend", Status: CheckError, Detail: "notification backend was not configured", Hint: "Set --notification-token-env or omit --notification-runner."})
			watchBackend = nil
			watchErr = fmt.Errorf("notification backend was not configured")
		} else {
			info := notificationBackend.BackendInfo()
			report.add(PreflightCheck{Name: "notification-backend", Status: CheckOK, Detail: fmt.Sprintf("%s backend ready for notification polling on %s", info.Name, info.Host)})
			report.add(notificationIdentityCheck(ctx, cfg, notificationBackend))
			watchBackend = notificationBackend
			watchErr = nil
		}
	} else {
		report.add(PreflightCheck{Name: "notification-backend", Status: CheckSkipped, Detail: "using main runner backend for notification polling"})
	}

	for _, repo := range cfg.Repositories {
		report.add(repositoryWatchCheck(ctx, watchCheckPrefix+repo, repo, watchBackend, watchErr))
	}

	if cfg.GHConfigDir == "" {
		report.add(PreflightCheck{Name: "sandbox-gh-config", Status: CheckSkipped, Detail: "host gh auth config will be mirrored into the dispatch sandbox"})
	} else {
		report.add(PreflightCheck{Name: "sandbox-gh-config", Status: CheckOK, Detail: "host GH_CONFIG_DIR source: " + cfg.GHConfigDir})
	}

	report.add(PreflightCheck{Name: "unsafe-no-sandbox", Status: unsafeSandboxStatus(cfg), Detail: unsafeSandboxDetail(cfg)})
	if cfg.UnsafeNoSandbox {
		report.add(PreflightCheck{Name: "bwrap", Status: CheckSkipped, Detail: "skipped because --unsafe-no-sandbox is set"})
	} else {
		report.add(bwrapCheck(ctx, cfg, deps))
	}

	report.add(binaryCheck(deps, "acpx", cfg.AcpxPath, acpxInstallHint))
	addAgentChecks(&report, cfg)
	report.finish()
	return report
}

func (d PreflightDependencies) withDefaults() PreflightDependencies {
	if d.SelectBackend == nil {
		d.SelectBackend = func(ctx context.Context, host string) (auth.GitHubBackendSelection, error) {
			return auth.SelectGitHubBackend(ctx, host)
		}
	}
	if d.OpenBackend == nil {
		d.OpenBackend = defaultPreflightRunnerBackend
	}
	if d.OpenNotificationBackend == nil {
		d.OpenNotificationBackend = defaultPreflightNotificationBackend
	}
	if d.LookPath == nil {
		d.LookPath = exec.LookPath
	}
	if d.RunCommand == nil {
		d.RunCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			cmd := exec.CommandContext(ctx, name, args...)
			return cmd.CombinedOutput()
		}
	}
	return d
}

func defaultPreflightRunnerBackend(_ context.Context, selection auth.GitHubBackendSelection) (PreflightRunnerBackend, error) {
	switch selection.Name {
	case auth.GitHubBackendNameREST:
		if strings.TrimSpace(selection.Token.Value) == "" {
			return nil, fmt.Errorf("rest GitHub backend selected without a token")
		}
		return github.NewClient(selection.Host, selection.Token.Value), nil
	case auth.GitHubBackendNameGH:
		return github.NewGHBackend(github.GHBackendOptions{Host: selection.Host})
	default:
		return nil, fmt.Errorf("unsupported GitHub backend %q", selection.Name)
	}
}

func defaultPreflightNotificationBackend(_ context.Context, cfg Config) (PreflightNotificationBackend, error) {
	cfg = cfg.Normalized()
	if cfg.NotificationTokenEnv == "" {
		return nil, nil
	}
	token := strings.TrimSpace(os.Getenv(cfg.NotificationTokenEnv))
	if token == "" {
		return nil, fmt.Errorf("%s is empty", cfg.NotificationTokenEnv)
	}
	return github.NewClient(cfg.Hostname, token), nil
}

func notificationIdentityCheck(ctx context.Context, cfg Config, backend PreflightNotificationBackend) PreflightCheck {
	check := PreflightCheck{Name: "notification-identity"}
	user, _, err := backend.GetUser(ctx)
	if err != nil {
		check.Status = CheckError
		check.Detail = "notification identity lookup failed: " + err.Error()
		check.Hint = "Ensure the notification bot token can read its authenticated user."
		return check
	}
	login := strings.TrimSpace(user.Login)
	if login == "" {
		check.Status = CheckError
		check.Detail = "notification identity lookup returned an empty login"
		return check
	}
	if cfg.NotificationIdentity != "" && !strings.EqualFold(login, cfg.NotificationIdentity) {
		check.Status = CheckError
		check.Detail = fmt.Sprintf("notification token authenticates as %q, want %q", login, cfg.NotificationIdentity)
		check.Hint = "Use a token for the configured notification runner account."
		return check
	}
	check.Status = CheckOK
	check.Detail = "notification token authenticates as " + login
	return check
}

func repositoryWatchCheck(ctx context.Context, name, repo string, backend PreflightRunnerBackend, backendErr error) PreflightCheck {
	check := PreflightCheck{Name: name}
	if backendErr != nil {
		check.Status = CheckError
		check.Detail = "cannot verify repository subscription because the GitHub backend is unavailable: " + backendErr.Error()
		return check
	}
	if backend == nil {
		check.Status = CheckError
		check.Detail = "cannot verify repository subscription because runner backend is unavailable"
		check.Hint = "Configure a runner-capable GitHub backend before polling."
		return check
	}
	result, err := backend.GetRepositorySubscription(ctx, repo)
	if err != nil {
		check.Status = CheckError
		check.Detail = "subscription lookup failed: " + err.Error()
		check.Hint = "Ensure the runner identity can read the repository subscription and has watched the repository."
		return check
	}
	if !result.Subscription.Subscribed {
		check.Status = CheckError
		check.Detail = "runner identity is not subscribed to repository notifications"
		check.Hint = "Watch the repository with notifications enabled before starting the runner."
		return check
	}
	if result.Subscription.Ignored {
		check.Status = CheckError
		check.Detail = "runner identity is ignoring repository notifications"
		check.Hint = "Unignore repository notifications before starting the runner."
		return check
	}
	check.Status = CheckOK
	check.Detail = "repository notifications are watched"
	if result.Subscription.Reason != "" {
		check.Detail += " (" + result.Subscription.Reason + ")"
	}
	return check
}

func (r *PreflightReport) add(check PreflightCheck) {
	r.Checks = append(r.Checks, check)
}

func (r *PreflightReport) finish() {
	r.OK = true
	for _, check := range r.Checks {
		if check.Status == CheckError {
			r.OK = false
			return
		}
	}
}

func needsGHCheck(cfg Config, selection auth.GitHubBackendSelection, backendErr error) bool {
	if cfg.GitHubBackend == auth.GitHubBackendModeGH {
		return true
	}
	if backendErr != nil && cfg.GitHubBackend == auth.GitHubBackendModeAuto {
		return true
	}
	return selection.Name == auth.GitHubBackendNameGH
}

func binaryCheck(deps PreflightDependencies, name, binary, hint string) PreflightCheck {
	binary = strings.TrimSpace(binary)
	if binary == "" {
		binary = name
	}
	path, err := deps.LookPath(binary)
	if err != nil {
		return PreflightCheck{Name: name, Status: CheckError, Detail: fmt.Sprintf("%s not found", binary), Hint: hint}
	}
	return PreflightCheck{Name: name, Status: CheckOK, Detail: path}
}

func bwrapCheck(ctx context.Context, cfg Config, deps PreflightDependencies) PreflightCheck {
	path, source, err := resolveBwrapPath(cfg, deps)
	if err != nil {
		return PreflightCheck{Name: "bwrap", Status: CheckError, Detail: err.Error(), Hint: bwrapInstallHint}
	}
	output, err := deps.RunCommand(ctx, path, "--help")
	if err != nil {
		return PreflightCheck{Name: "bwrap", Status: CheckError, Detail: fmt.Sprintf("%s --help failed: %v", path, err), Hint: bwrapInstallHint}
	}
	if !bytes.Contains(output, []byte("--perms")) {
		return PreflightCheck{Name: "bwrap", Status: CheckError, Detail: fmt.Sprintf("%s does not advertise --perms support", path), Hint: bwrapInstallHint}
	}
	return PreflightCheck{Name: "bwrap", Status: CheckOK, Detail: fmt.Sprintf("%s (%s, --perms supported)", path, source)}
}

func resolveBwrapPath(cfg Config, deps PreflightDependencies) (string, string, error) {
	if cfg.BwrapPath != "" {
		path, err := deps.LookPath(cfg.BwrapPath)
		if err != nil {
			return "", "config", fmt.Errorf("configured bwrap %q not found", cfg.BwrapPath)
		}
		return path, "config", nil
	}
	if envPath := strings.TrimSpace(os.Getenv(BwrapPathEnv)); envPath != "" {
		path, err := deps.LookPath(envPath)
		if err != nil {
			return "", "env:" + BwrapPathEnv, fmt.Errorf("%s %q not found", BwrapPathEnv, envPath)
		}
		return path, "env:" + BwrapPathEnv, nil
	}
	path, err := deps.LookPath("bwrap")
	if err != nil {
		return "", "PATH", fmt.Errorf("bwrap not found")
	}
	return path, "PATH", nil
}

func unsafeSandboxStatus(cfg Config) string {
	if cfg.UnsafeNoSandbox {
		return CheckWarning
	}
	return CheckOK
}

func unsafeSandboxDetail(cfg Config) string {
	if cfg.UnsafeNoSandbox {
		return "sandbox_provider=none fs_boundary=disabled"
	}
	return "default bubblewrap sandbox remains required"
}

func addAgentChecks(report *PreflightReport, cfg Config) {
	report.add(PreflightCheck{Name: "configured-agent", Status: CheckOK, Detail: configuredAgentDetail(cfg)})
	switch cfg.Agent.Kind {
	case AgentCodex:
		report.add(codexAccessCheck(cfg))
		report.add(codexAuthCheck())
		report.add(PreflightCheck{Name: "claude-user-settings", Status: CheckSkipped, Detail: "configured agent is codex"})
		report.add(PreflightCheck{Name: "claude-auth", Status: CheckSkipped, Detail: "configured agent is codex"})
		report.add(PreflightCheck{Name: "claude-allowed-tools", Status: CheckSkipped, Detail: "configured agent is codex"})
	case AgentClaude:
		report.add(PreflightCheck{Name: "codex-agent-full-access", Status: CheckSkipped, Detail: "configured agent is claude"})
		report.add(PreflightCheck{Name: "codex-auth", Status: CheckSkipped, Detail: "configured agent is claude"})
		report.add(claudeUserSettingsCheck(cfg))
		report.add(claudeAuthCheck())
		report.add(claudeAllowedToolsCheck(cfg))
	}
}

func configuredAgentDetail(cfg Config) string {
	if cfg.Agent.Model == "" {
		return cfg.Agent.Kind
	}
	return fmt.Sprintf("%s model=%s", cfg.Agent.Kind, cfg.Agent.Model)
}

func codexAccessCheck(cfg Config) PreflightCheck {
	if cfg.Agent.CodexAgentFullAccess {
		return PreflightCheck{Name: "codex-agent-full-access", Status: CheckOK, Detail: "enabled"}
	}
	return PreflightCheck{Name: "codex-agent-full-access", Status: CheckWarning, Detail: "disabled; Codex child CLI/shell workflow work may fail"}
}

func codexAuthCheck() PreflightCheck {
	dir := hostCodexConfigDir()
	if strings.TrimSpace(dir) == "" {
		return PreflightCheck{
			Name:   "codex-auth",
			Status: CheckError,
			Detail: "Codex auth unavailable: cannot resolve host Codex config directory",
			Hint:   "Run codex login with a normal HOME, or set CODEX_HOME to the Codex config directory before starting the runner.",
		}
	}
	authPath := filepath.Join(dir, "auth.json")
	if err := requireReadableRegularFile(authPath); err != nil {
		return PreflightCheck{
			Name:   "codex-auth",
			Status: CheckError,
			Detail: "Codex auth unavailable: " + err.Error(),
			Hint:   "Run codex login, or set CODEX_HOME to the Codex config directory before starting the runner.",
		}
	}
	return PreflightCheck{Name: "codex-auth", Status: CheckOK, Detail: "host Codex auth source: " + authPath}
}

func claudeUserSettingsCheck(cfg Config) PreflightCheck {
	if cfg.Agent.ClaudeIncludeUserSettings {
		return PreflightCheck{Name: "claude-user-settings", Status: CheckOK, Detail: "ACPX_CLAUDE_INCLUDE_USER_SETTINGS enabled by runner config"}
	}
	return PreflightCheck{Name: "claude-user-settings", Status: CheckWarning, Detail: "disabled; Claude auth/settings may not be visible to acpx"}
}

func claudeAuthCheck() PreflightCheck {
	home := hostHomeDir()
	if strings.TrimSpace(home) == "" {
		return PreflightCheck{
			Name:   "claude-auth",
			Status: CheckError,
			Detail: "Claude Code auth unavailable: cannot resolve host HOME",
			Hint:   "Run claude login with a normal HOME before starting the runner.",
		}
	}
	credentials := filepath.Join(home, ".claude", ".credentials.json")
	if err := requireReadableRegularFile(credentials); err != nil {
		return PreflightCheck{
			Name:   "claude-auth",
			Status: CheckError,
			Detail: "Claude Code auth unavailable: " + err.Error(),
			Hint:   "Run claude login with the same HOME that starts the runner. Claude Code uses host ~/.claude/.credentials.json, not CODEX_HOME.",
		}
	}
	return PreflightCheck{Name: "claude-auth", Status: CheckOK, Detail: "host Claude Code auth source: " + credentials}
}

func claudeAllowedToolsCheck(cfg Config) PreflightCheck {
	have := map[string]bool{}
	for _, tool := range cfg.Agent.ClaudeAllowedTools {
		have[strings.ToLower(tool)] = true
	}
	if have["task"] && have["bash"] {
		return PreflightCheck{Name: "claude-allowed-tools", Status: CheckOK, Detail: strings.Join(cfg.Agent.ClaudeAllowedTools, ", ")}
	}
	return PreflightCheck{Name: "claude-allowed-tools", Status: CheckWarning, Detail: strings.Join(cfg.Agent.ClaudeAllowedTools, ", "), Hint: "Include Task and Bash for issue-spec DAG workers and CLI-direct artifact writes."}
}

func hostCodexConfigDir() string {
	if value := strings.TrimSpace(os.Getenv("CODEX_HOME")); value != "" {
		return filepath.Clean(value)
	}
	if home := hostHomeDir(); home != "" {
		return filepath.Join(home, ".codex")
	}
	return ""
}

func hostHomeDir() string {
	if value := strings.TrimSpace(os.Getenv("HOME")); value != "" {
		return filepath.Clean(value)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	home = strings.TrimSpace(home)
	if home == "" {
		return ""
	}
	return filepath.Clean(home)
}

func requireReadableRegularFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("%s is missing", path)
	}
	if info.IsDir() {
		return fmt.Errorf("%s is a directory", path)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("%s is not readable: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("%s close failed: %w", path, err)
	}
	return nil
}
