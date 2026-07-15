package commentrunner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/higress-group/issue-spec/internal/acpx"
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
	AgentRuntimeFailureTimeout = "timeout"
	AgentRuntimeFailureAdapter = "adapter"
	AgentRuntimeFailureModel   = "model"
	AgentRuntimeFailureRuntime = "runtime"
)

// AgentRuntimeProbeError carries only a bounded failure category into the
// preflight report. Raw adapter output remains in local diagnostics and is
// never copied into reports or issue comments.
type AgentRuntimeProbeError struct {
	Kind string
	Err  error
}

func NewAgentRuntimeProbeError(kind string, err error) error {
	switch kind {
	case AgentRuntimeFailureTimeout, AgentRuntimeFailureAdapter, AgentRuntimeFailureModel:
	default:
		kind = AgentRuntimeFailureRuntime
	}
	return &AgentRuntimeProbeError{Kind: kind, Err: err}
}

func (e *AgentRuntimeProbeError) Error() string { return "agent runtime probe " + e.Kind }
func (e *AgentRuntimeProbeError) Unwrap() error { return e.Err }

const (
	bwrapInstallHint = "Install or upgrade bubblewrap, or explicitly rerun with --unsafe-no-sandbox to disable the filesystem boundary."
	codexACPPackage  = "@agentclientprotocol/codex-acp@^0.0.44"
	acpxInstallHint  = "Install Node.js/npm, then run `npm install -g acpx@latest`; Codex sessions also need npx access to " + codexACPPackage + " (allow npm registry access or pre-cache it with `npm cache add " + codexACPPackage + "`)."
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

type PreflightTransport string

const (
	PreflightTransportPoll  PreflightTransport = "poll"
	PreflightTransportServe PreflightTransport = "serve"
)

type PreflightDependencies struct {
	SelectBackend             func(context.Context, string) (auth.GitHubBackendSelection, error)
	OpenBackend               func(context.Context, auth.GitHubBackendSelection) (PreflightRunnerBackend, error)
	OpenEvidenceWriterBackend func(context.Context, auth.GitHubBackendSelection) (PreflightEvidenceWriterBackend, error)
	OpenNotificationBackend   func(context.Context, Config) (PreflightNotificationBackend, error)
	LookPath                  func(string) (string, error)
	RunCommand                func(context.Context, string, ...string) ([]byte, error)
	RunAgentCommand           func(context.Context, string, ...string) ([]byte, error)
	AgentRuntimeHome          func() (string, error)
}

// PreflightOptions controls opt-in checks that contact an external runtime.
// They are deliberately separate from the default preflight: operators should
// be able to inspect configuration without creating an ACP session.
type PreflightOptions struct {
	VerifyAgentRuntime bool
}

type PreflightRunnerBackend interface {
	BackendInfo() github.BackendInfo
	GetRepositorySubscription(context.Context, string) (github.RepositorySubscriptionResult, error)
}

type PreflightNotificationBackend interface {
	PreflightRunnerBackend
	GetUser(context.Context) (github.User, []string, error)
}

// PreflightEvidenceWriterBackend is intentionally read-only and native-only.
// PAT scopes and capability probes cannot implement this designation check.
type PreflightEvidenceWriterBackend interface {
	GetNativeEvidenceWriterStatus(context.Context, string) (github.NativeEvidenceWriterStatus, error)
}

func RunPreflight(ctx context.Context, cfg Config, deps PreflightDependencies) PreflightReport {
	return RunPreflightForTransportWithOptions(ctx, cfg, PreflightTransportPoll, deps, PreflightOptions{})
}

// RunPreflightForTransport validates the prerequisites shared by both runner
// transports and applies transport-specific checks only when they are
// meaningful. Self-hosted serve intake is webhook based, so probing GitHub
// notification identities or repository watches would reject a healthy
// deployment for a polling prerequisite it neither uses nor exposes.
func RunPreflightForTransport(ctx context.Context, cfg Config, transport PreflightTransport, deps PreflightDependencies) PreflightReport {
	return RunPreflightForTransportWithOptions(ctx, cfg, transport, deps, PreflightOptions{})
}

// RunPreflightForTransportWithOptions validates runner prerequisites and can
// optionally create a minimal ACP session to verify the selected agent
// runtime. The runtime probe never grants tools to the agent.
func RunPreflightForTransportWithOptions(ctx context.Context, cfg Config, transport PreflightTransport, deps PreflightDependencies, options PreflightOptions) PreflightReport {
	cfg = cfg.Normalized()
	deps = deps.withDefaults()
	report := PreflightReport{Config: cfg}
	if err := cfg.Validate(); err != nil {
		report.add(PreflightCheck{Name: "config", Status: CheckError, Detail: err.Error()})
		report.finish()
		return report
	}
	if transport != PreflightTransportPoll && transport != PreflightTransportServe {
		report.add(PreflightCheck{Name: "transport", Status: CheckError, Detail: fmt.Sprintf("unsupported runner preflight transport %q", transport)})
		report.finish()
		return report
	}
	if cfg.AllowHostSSH && transport != PreflightTransportServe {
		report.add(PreflightCheck{
			Name:   "host-ssh-transport",
			Status: CheckError,
			Detail: "--allow-host-ssh is available only for self-hosted runner serve preflight",
			Hint:   "Remove --allow-host-ssh for GitHub notification polling, or select the matching self-hosted profile used by runner serve.",
		})
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

	if transport == PreflightTransportServe {
		report.add(PreflightCheck{Name: "command-intake-transport", Status: CheckOK, Detail: "self-hosted webhook intake via runner serve"})
		report.add(PreflightCheck{Name: "notification-backend", Status: CheckSkipped, Detail: "self-hosted profiles use runner serve; notification polling and repository watches are not applicable"})
		var evidenceBackend PreflightEvidenceWriterBackend
		var evidenceBackendErr error
		if backendErr != nil {
			evidenceBackendErr = backendErr
		} else {
			evidenceBackend, evidenceBackendErr = deps.OpenEvidenceWriterBackend(ctx, selection)
			if evidenceBackendErr == nil && evidenceBackend == nil {
				evidenceBackendErr = errors.New("native Evidence Writer backend was not configured")
			}
		}
		if evidenceBackendErr != nil {
			report.add(PreflightCheck{Name: "evidence-writer-backend", Status: CheckError,
				Detail: "cannot verify repository Evidence Writer designation",
				Hint:   "Use the origin-bound self-hosted profile and Runner PAT so preflight can query the native evidence authority."})
		} else {
			report.add(PreflightCheck{Name: "evidence-writer-backend", Status: CheckOK,
				Detail: "native read-only Evidence Writer designation lookup is available"})
		}
		for _, repo := range cfg.Repositories {
			report.add(evidenceWriterCheck(ctx, cfg, repo, evidenceBackend, evidenceBackendErr))
		}
	} else {
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
	}

	if cfg.GHConfigDir == "" {
		report.add(PreflightCheck{Name: "sandbox-gh-config", Status: CheckSkipped, Detail: "host gh auth config will be mirrored into the dispatch sandbox"})
	} else {
		report.add(PreflightCheck{Name: "sandbox-gh-config", Status: CheckOK, Detail: "host GH_CONFIG_DIR source: " + cfg.GHConfigDir})
	}
	if transport == PreflightTransportServe {
		report.add(PreflightCheck{Name: "agent-credential-mode", Status: CheckOK,
			Detail: "strict operator-owned issuer is enforced again before each workspace and worker allocation"})
	} else if cfg.StrictAgentCapabilities {
		report.add(PreflightCheck{Name: "agent-credential-mode", Status: CheckError,
			Detail: "legacy_long_lived mirrored gh credentials cannot satisfy strict agent capability policy",
			Hint:   "Configure an operator-owned short-lived issuer; strict mode never downgrades to host gh authentication."})
	} else {
		report.add(PreflightCheck{Name: "agent-credential-mode", Status: CheckWarning,
			Detail: "legacy_long_lived mirrored gh credentials are compatibility-only and non-compliant with strict delegated operation policy",
			Hint:   "Migrate to an operator-owned short-lived issuer before enabling strict agent capabilities."})
	}

	report.add(PreflightCheck{Name: "unsafe-no-sandbox", Status: unsafeSandboxStatus(cfg), Detail: unsafeSandboxDetail(cfg)})
	if cfg.UnsafeNoSandbox {
		report.add(PreflightCheck{Name: "bwrap", Status: CheckSkipped, Detail: "skipped because --unsafe-no-sandbox is set"})
	} else {
		report.add(bwrapCheck(ctx, cfg, deps))
	}

	report.add(binaryCheck(deps, "acpx", cfg.AcpxPath, acpxInstallHint))
	addAgentChecks(&report, cfg, deps)
	if options.VerifyAgentRuntime {
		report.add(agentRuntimeProbeCheck(ctx, cfg, deps))
	}
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
	if d.OpenEvidenceWriterBackend == nil {
		d.OpenEvidenceWriterBackend = defaultPreflightEvidenceWriterBackend
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
	if d.RunAgentCommand == nil {
		d.RunAgentCommand = d.RunCommand
	}
	if d.AgentRuntimeHome == nil {
		d.AgentRuntimeHome = func() (string, error) { return hostHomeDir(), nil }
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

func defaultPreflightEvidenceWriterBackend(_ context.Context, selection auth.GitHubBackendSelection) (PreflightEvidenceWriterBackend, error) {
	profile, err := selection.Profile.Normalized()
	if err != nil || profile.Kind != auth.ProfileKindHosted || strings.TrimSpace(profile.NativeAPIURL) == "" {
		return nil, errors.New("self-hosted profile native API is unavailable")
	}
	if strings.TrimSpace(selection.Token.Value) == "" {
		return nil, errors.New("self-hosted Runner PAT is unavailable")
	}
	return github.NewClientWithOptions(github.ClientOptions{Host: profile.Hostname, BaseURL: profile.NativeAPIURL,
		Token: selection.Token.Value, CAFile: profile.CAFile})
}

func defaultPreflightNotificationBackend(_ context.Context, cfg Config) (PreflightNotificationBackend, error) {
	cfg = cfg.Normalized()
	if cfg.NotificationTokenEnv == "" {
		return nil, nil
	}
	rawToken, ok := os.LookupEnv(cfg.NotificationTokenEnv)
	if !ok {
		return nil, fmt.Errorf("%s is unset", cfg.NotificationTokenEnv)
	}
	token := strings.TrimSpace(rawToken)
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

func evidenceWriterCheck(ctx context.Context, cfg Config, repo string, backend PreflightEvidenceWriterBackend, backendErr error) PreflightCheck {
	check := PreflightCheck{Name: "evidence-writer:" + repo}
	if backendErr != nil || backend == nil {
		check.Status = CheckError
		check.Detail = "Evidence Writer designation was not verified"
		check.Hint = "Configure the native self-hosted profile and explicitly designate the Runner identity for this repository."
		return check
	}
	status, err := backend.GetNativeEvidenceWriterStatus(ctx, repo)
	if err != nil {
		check.Status = CheckError
		check.Detail = "Evidence Writer designation lookup failed"
		var apiErr *github.APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode > 0 {
			check.Detail += fmt.Sprintf(" (HTTP %d)", apiErr.StatusCode)
		}
		check.Hint = "Confirm the Runner PAT can read this exact repository and the profile native API is reachable."
		return check
	}
	if !strings.EqualFold(strings.TrimSpace(status.Login), cfg.RunnerIdentity) {
		check.Status = CheckError
		check.Detail = fmt.Sprintf("Runner PAT authenticates as %q, but --runner is %q", status.Login, cfg.RunnerIdentity)
		check.Hint = "Use the PAT for the configured Runner identity; Evidence Writer designation belongs to the authenticated identity, not the token scope."
		return check
	}
	if !status.Active {
		check.Status = CheckError
		check.Detail = "authenticated Runner identity is not an active Evidence Writer for this repository"
		check.Hint = "Using a separate repository operator credential, activate this identity as an Evidence Writer before dispatch."
		return check
	}
	check.Status = CheckOK
	check.Detail = "authenticated Runner identity is an active Evidence Writer for this repository"
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

func addAgentChecks(report *PreflightReport, cfg Config, deps PreflightDependencies) {
	report.add(PreflightCheck{Name: "configured-agent", Status: CheckOK, Detail: configuredAgentDetail(cfg)})
	switch cfg.Agent.Kind {
	case AgentCodex:
		report.add(codexAccessCheck(cfg))
		report.add(codexACPCheck(deps))
		report.add(codexAuthCheck())
		report.add(PreflightCheck{Name: "claude-agent-full-access", Status: CheckSkipped, Detail: "configured agent is codex"})
		report.add(PreflightCheck{Name: "claude-user-settings", Status: CheckSkipped, Detail: "configured agent is codex"})
		report.add(PreflightCheck{Name: "claude-auth", Status: CheckSkipped, Detail: "configured agent is codex"})
		report.add(PreflightCheck{Name: "claude-allowed-tools", Status: CheckSkipped, Detail: "configured agent is codex"})
	case AgentClaude:
		report.add(PreflightCheck{Name: "codex-agent-full-access", Status: CheckSkipped, Detail: "configured agent is claude"})
		report.add(PreflightCheck{Name: "codex-acp", Status: CheckSkipped, Detail: "configured agent is claude"})
		report.add(PreflightCheck{Name: "codex-auth", Status: CheckSkipped, Detail: "configured agent is claude"})
		report.add(claudeAgentFullAccessCheck(cfg))
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

func claudeAgentFullAccessCheck(cfg Config) PreflightCheck {
	if cfg.Agent.ClaudeAgentFullAccess {
		return PreflightCheck{Name: "claude-agent-full-access", Status: CheckOK, Detail: "enabled"}
	}
	return PreflightCheck{Name: "claude-agent-full-access", Status: CheckWarning, Detail: "disabled; Claude child CLI/shell workflow work may fail"}
}

func codexACPCheck(deps PreflightDependencies) PreflightCheck {
	npxPath, npxErr := deps.LookPath("npx")
	npmPath, npmErr := deps.LookPath("npm")
	var missing []string
	if npxErr != nil {
		missing = append(missing, "npx")
	}
	if npmErr != nil {
		missing = append(missing, "npm")
	}
	if len(missing) > 0 {
		return PreflightCheck{
			Name:   "codex-acp",
			Status: CheckError,
			Detail: fmt.Sprintf("acpx codex provider spawns `npx -y %s`, but %s not found", codexACPPackage, strings.Join(missing, " and ")),
			Hint:   acpxInstallHint,
		}
	}
	detail := fmt.Sprintf("npx=%s npm=%s package=%s", npxPath, npmPath, codexACPPackage)
	home, err := deps.AgentRuntimeHome()
	if err != nil || strings.TrimSpace(home) == "" {
		return PreflightCheck{Name: "codex-acp", Status: CheckError, Detail: "cannot resolve the Runner host HOME used for ACPX", Hint: acpxInstallHint}
	}
	if override, ok, err := acpx.LoadAgentOverride(home, acpx.AgentCodex); err != nil {
		return PreflightCheck{Name: "codex-acp", Status: CheckError, Detail: "invalid host acpx Codex agent override", Hint: acpxInstallHint}
	} else if ok {
		detail = fmt.Sprintf("npx=%s npm=%s agent_override=%s", npxPath, npmPath, acpx.AgentOverrideDescription(override))
	}
	return PreflightCheck{
		Name:   "codex-acp",
		Status: CheckOK,
		Detail: detail,
	}
}

func agentRuntimeProbeCheck(ctx context.Context, cfg Config, deps PreflightDependencies) PreflightCheck {
	if cfg.Agent.Kind != AgentCodex {
		return PreflightCheck{Name: "agent-runtime-probe", Status: CheckSkipped, Detail: "live runtime probe is currently available for codex only"}
	}
	probeCtx, cancel := context.WithTimeout(ctx, 75*time.Second)
	defer cancel()
	args := []string{"--verbose", "--timeout", "60", "--deny-all", "--format", "json"}
	if model := strings.TrimSpace(cfg.Agent.Model); model != "" {
		args = append(args, "--model", model)
	}
	args = append(args, "codex", "exec", "Reply with exactly OK and do not use tools.")
	if _, err := deps.RunAgentCommand(probeCtx, cfg.AcpxPath, args...); err != nil {
		kind := AgentRuntimeFailureRuntime
		var classified *AgentRuntimeProbeError
		if errors.As(err, &classified) {
			kind = classified.Kind
		} else if errors.Is(err, context.DeadlineExceeded) || errors.Is(probeCtx.Err(), context.DeadlineExceeded) {
			kind = AgentRuntimeFailureTimeout
		}
		check := PreflightCheck{Name: "agent-runtime-probe", Status: CheckError}
		switch kind {
		case AgentRuntimeFailureTimeout:
			check.Detail = "Codex ACP runtime probe timed out"
			check.Hint = "Confirm the selected ACP adapter is already available to the runner account and can start without a cold package download."
		case AgentRuntimeFailureAdapter:
			check.Detail = "Codex ACP adapter failed to start or initialize"
			check.Hint = "Confirm the operator-selected ACPX agent override and adapter package are available to the runner account."
		case AgentRuntimeFailureModel:
			check.Detail = "Codex ACP runtime rejected the requested model"
			check.Hint = "Confirm the exact --model identifier is supported by the selected adapter and authenticated Codex account."
		default:
			check.Detail = "Codex ACP runtime probe failed"
			check.Hint = "Inspect the bounded runner diagnostic logs as the runner service user; do not copy raw runtime output into issue comments."
		}
		return check
	}
	detail := "Codex ACP runtime probe succeeded with tools denied"
	if model := strings.TrimSpace(cfg.Agent.Model); model != "" {
		detail += " model=" + model
	}
	return PreflightCheck{Name: "agent-runtime-probe", Status: CheckOK, Detail: detail}
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

// claudeAuthEnvNames are the settings.json env keys that let Claude Code
// authenticate against an Anthropic-compatible API (including third-party
// gateways) without an interactive `claude login`, i.e. without
// ~/.claude/.credentials.json.
var claudeAuthEnvNames = []string{"ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY"}

func claudeAuthCheck() PreflightCheck {
	home := hostHomeDir()
	if strings.TrimSpace(home) == "" {
		return PreflightCheck{
			Name:   "claude-auth",
			Status: CheckError,
			Detail: "Claude Code auth unavailable: cannot resolve host HOME",
			Hint:   "Run claude login with a normal HOME, or set an Anthropic API key in ~/.claude/settings.json env, before starting the runner.",
		}
	}
	claudeDir := filepath.Join(home, ".claude")
	credentials := filepath.Join(claudeDir, ".credentials.json")
	if err := requireReadableRegularFile(credentials); err == nil {
		return PreflightCheck{Name: "claude-auth", Status: CheckOK, Detail: "host Claude Code auth source: " + credentials}
	}
	// Third-party API users authenticate via ~/.claude/settings.json env
	// (ANTHROPIC_AUTH_TOKEN/ANTHROPIC_API_KEY, usually with ANTHROPIC_BASE_URL)
	// and never run `claude login`, so .credentials.json is absent. settings.json
	// is mirrored into the sandbox HOME and read by Claude Code, so it is a valid
	// auth source. Host env vars are not, because the sandbox scrubs them.
	if source, ok := claudeSettingsAuthSource(claudeDir); ok {
		return PreflightCheck{Name: "claude-auth", Status: CheckOK, Detail: "host Claude Code auth source: " + source}
	}
	return PreflightCheck{
		Name:   "claude-auth",
		Status: CheckError,
		Detail: fmt.Sprintf("Claude Code auth unavailable: no readable %s and no Anthropic API key in %s env", credentials, filepath.Join(claudeDir, "settings.json")),
		Hint:   "Run claude login with the same HOME that starts the runner, or configure ANTHROPIC_AUTH_TOKEN/ANTHROPIC_API_KEY (with ANTHROPIC_BASE_URL) in ~/.claude/settings.json env for a third-party API. Host env vars are stripped by the sandbox, so they must live in settings.json.",
	}
}

// claudeSettingsAuthSource reports whether settings.json or settings.local.json
// in claudeDir provides Anthropic API auth via their env block, returning a
// human-readable description of the source that satisfied the check.
//
// Only the env block is honored: the settings files themselves are mirrored into
// the sandbox (see claudeRuntimeDirFiles) and Claude Code reads their env inline,
// so it is genuinely available at runtime. apiKeyHelper is deliberately NOT
// accepted here — it points to an external script that the sandbox does not carry
// in, so trusting it would let preflight pass while the runner fails at runtime.
func claudeSettingsAuthSource(claudeDir string) (string, bool) {
	// settings.local.json overrides settings.json in Claude Code, but either one
	// on its own is enough to authenticate, so accept whichever provides a key.
	for _, name := range []string{"settings.local.json", "settings.json"} {
		path := filepath.Join(claudeDir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var settings struct {
			Env map[string]string `json:"env"`
		}
		if err := json.Unmarshal(data, &settings); err != nil {
			continue
		}
		for _, key := range claudeAuthEnvNames {
			if strings.TrimSpace(settings.Env[key]) != "" {
				return fmt.Sprintf("%s (%s)", path, key), true
			}
		}
	}
	return "", false
}

func claudeAllowedToolsCheck(cfg Config) PreflightCheck {
	// Empty (nil) means no tool restrictions - all tools allowed
	if len(cfg.Agent.ClaudeAllowedTools) == 0 {
		return PreflightCheck{Name: "claude-allowed-tools", Status: CheckOK, Detail: "all tools allowed"}
	}
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
