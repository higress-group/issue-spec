package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/higress-group/issue-spec/internal/acpx"
	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/commentrunner"
	"github.com/higress-group/issue-spec/internal/commentrunner/credentials"
	"github.com/higress-group/issue-spec/internal/commentrunner/intake"
	"github.com/higress-group/issue-spec/internal/commentrunner/jobs"
	"github.com/higress-group/issue-spec/internal/commentrunner/state"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/sandbox"
)

func TestRootUsageDocumentsRunnerCommand(t *testing.T) {
	var out, errOut bytes.Buffer
	code := Execute([]string{"--help"}, strings.NewReader(""), &out, &errOut)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%q", code, errOut.String())
	}
	if !strings.Contains(out.String(), "issue-spec runner poll") || !strings.Contains(out.String(), "issue-spec runner preflight") {
		t.Fatalf("runner usage missing from help:\n%s", out.String())
	}
}

func TestRunnerHelpDocumentsSubcommands(t *testing.T) {
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)

	code := app.runRunner(context.Background(), []string{"-h"})
	if code != 0 {
		t.Fatalf("exit code = %d, stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	text := out.String()
	for _, want := range []string{
		"Usage:",
		"issue-spec runner poll [options]",
		"issue-spec runner preflight [options]",
		"Use \"issue-spec runner <subcommand> -h\"",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("runner help missing %q:\n%s", want, text)
		}
	}
	if errOut.Len() != 0 {
		t.Fatalf("runner help wrote stderr: %q", errOut.String())
	}
}

func TestRunnerPollHelpShowsOptionsAndDefaults(t *testing.T) {
	clearCommandAuthEnv(t)
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)

	code := app.runRunner(context.Background(), []string{"poll", "-h"})
	if code != 0 {
		t.Fatalf("exit code = %d, stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	text := out.String()
	for _, want := range []string{
		"Usage:",
		"issue-spec runner poll [options]",
		"--max-concurrency int",
		"maximum concurrent runner jobs (default: 3)",
		"--poll-interval duration",
		"notification poll interval (default: 1m0s)",
		"--fallback-initial-lookback duration",
		"initial repository comments fallback lookback; 0 scans all historical comments (default: 720h0m0s)",
		"--async-dispatch",
		"(default: true)",
		"--dry-run",
		"(default: false)",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("runner poll help missing %q:\n%s", want, text)
		}
	}
	if errOut.Len() != 0 {
		t.Fatalf("runner poll help wrote stderr: %q", errOut.String())
	}
}

func TestRunnerPreflightHelpShowsSharedOptionsOnly(t *testing.T) {
	clearCommandAuthEnv(t)
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)

	code := app.runRunner(context.Background(), []string{"preflight", "-h"})
	if code != 0 {
		t.Fatalf("exit code = %d, stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	text := out.String()
	for _, want := range []string{
		"issue-spec runner preflight [options]",
		"--max-concurrency int",
		"maximum concurrent runner jobs (default: 3)",
		"--workspace-retention duration",
		"(default: 168h0m0s)",
		"--verify-agent-runtime",
		"create a tools-denied Codex ACP session",
		"--allow-host-ssh",
		"--git-author-name",
		"--git-author-email",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("runner preflight help missing %q:\n%s", want, text)
		}
	}
	for _, notWant := range []string{"--dry-run", "--once", "--async-dispatch", "--sync-dispatch"} {
		if strings.Contains(text, notWant) {
			t.Fatalf("runner preflight help unexpectedly included %q:\n%s", notWant, text)
		}
	}
	if errOut.Len() != 0 {
		t.Fatalf("runner preflight help wrote stderr: %q", errOut.String())
	}
}

func TestIssueSpecBinaryForRunnerUsesCurrentExecutable(t *testing.T) {
	old := runnerExecutable
	t.Cleanup(func() { runnerExecutable = old })
	runnerExecutable = func() (string, error) {
		return "/tmp/issue-spec-runner-e2e-001/bin/issue-spec", nil
	}

	if got := issueSpecBinaryForRunner(); got != "/tmp/issue-spec-runner-e2e-001/bin/issue-spec" {
		t.Fatalf("issueSpecBinaryForRunner() = %q", got)
	}
}

func TestIssueSpecBinaryForRunnerFallsBackToCommandName(t *testing.T) {
	old := runnerExecutable
	t.Cleanup(func() { runnerExecutable = old })
	runnerExecutable = func() (string, error) {
		return "", errors.New("executable unavailable")
	}

	if got := issueSpecBinaryForRunner(); got != "issue-spec" {
		t.Fatalf("issueSpecBinaryForRunner() = %q", got)
	}
}

func TestRunSandboxedAgentRuntimeCommandUsesIsolatedRuntimePaths(t *testing.T) {
	hostHome := t.TempDir()
	t.Setenv("HOME", hostHome)
	t.Setenv("CODEX_HOME", filepath.Join(hostHome, "missing-codex"))
	ghConfig := filepath.Join(hostHome, "gh")
	if err := os.MkdirAll(ghConfig, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ghConfig, "hosts.yml"), []byte("github.com:\n  oauth_token: test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	probe := filepath.Join(t.TempDir(), "probe.sh")
	if err := os.WriteFile(probe, []byte("#!/bin/sh\nprintf '%s|%s|%s' \"$HOME\" \"$CODEX_HOME\" \"$PWD\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := commentrunner.Config{UnsafeNoSandbox: true, GHConfigDir: ghConfig, Agent: commentrunner.DefaultAgentConfig()}
	output, err := runSandboxedAgentRuntimeCommand(t.Context(), cfg, probe, "--ignored")
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(string(output), "|")
	if len(parts) != 3 {
		t.Fatalf("probe output = %q", output)
	}
	if parts[0] == hostHome || !strings.Contains(parts[0], "issue-spec-runner-preflight-") ||
		!strings.Contains(parts[1], "issue-spec-runner-preflight-") || !strings.HasSuffix(parts[2], string(filepath.Separator)+"workspace") {
		t.Fatalf("probe runtime paths = %#v, host home = %q", parts, hostHome)
	}
}

func TestRunSandboxedAgentRuntimeCommandMatchesUnsafeHostSSHRuntime(t *testing.T) {
	sshHome := t.TempDir()
	sshDir := filepath.Join(sshHome, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(sshHome, ".acpx"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sshHome, ".acpx", "config.json"), []byte(`{"agents":{"codex":{"command":"npx","args":["-y","@agentclientprotocol/codex-acp@1.1.2"]}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	oldCurrent, oldNew := currentRunnerHostSSHConfig, newRunnerHostSSHProvider
	currentRunnerHostSSHConfig = func(string) (credentials.HostSSHGitProviderConfig, error) {
		return credentials.HostSSHGitProviderConfig{SSHDir: sshDir}, nil
	}
	newRunnerHostSSHProvider = credentials.NewHostSSHGitProvider
	t.Cleanup(func() { currentRunnerHostSSHConfig, newRunnerHostSSHProvider = oldCurrent, oldNew })
	t.Setenv("HOME", t.TempDir())
	t.Setenv("HTTP_PROXY", "http://proxy.example.test:8080")
	ghConfig := t.TempDir()
	if err := os.WriteFile(filepath.Join(ghConfig, "hosts.yml"), []byte("github.com:\n  oauth_token: test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	probe := filepath.Join(t.TempDir(), "probe.sh")
	if err := os.WriteFile(probe, []byte("#!/bin/sh\nprintf '%s|%s|%s|%s' \"$HOME\" \"$CODEX_HOME\" \"$HTTP_PROXY\" \"$(grep -c codex-acp \"$HOME/.acpx/config.json\")\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := commentrunner.Config{UnsafeNoSandbox: true, AllowHostSSH: true, GHConfigDir: ghConfig, Agent: commentrunner.DefaultAgentConfig()}
	output, err := runSandboxedAgentRuntimeCommand(t.Context(), cfg, probe)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(string(output), "|")
	if len(parts) != 4 || parts[0] != sshHome || !strings.Contains(parts[1], "issue-spec-runner-preflight-") ||
		parts[2] != "http://proxy.example.test:8080" || parts[3] != "1" {
		t.Fatalf("host SSH runtime = %#v, want HOME=%q isolated CODEX_HOME inherited proxy and host ACPX override", parts, sshHome)
	}
}

func TestRunnerRuntimeHostHomePrefersExplicitHostSSHAccount(t *testing.T) {
	sshHome := t.TempDir()
	environmentHome := t.TempDir()
	home, err := runnerRuntimeHostHome(sandbox.Config{
		HostSSHDir: filepath.Join(sshHome, ".ssh"),
		HostEnv:    []string{"HOME=" + environmentHome},
	})
	if err != nil {
		t.Fatal(err)
	}
	if home != sshHome {
		t.Fatalf("runtime host HOME = %q, want host SSH account %q", home, sshHome)
	}
}

func TestRunSandboxedAgentRuntimeCommandClassifiesPrepareAdapterError(t *testing.T) {
	hostHome := t.TempDir()
	if err := os.MkdirAll(filepath.Join(hostHome, ".acpx"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hostHome, ".acpx", "config.json"), []byte(`{must-not-leak`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := runSandboxedAgentRuntimeCommandWithConfig(t.Context(),
		commentrunner.Config{UnsafeNoSandbox: true, Agent: commentrunner.DefaultAgentConfig()},
		sandbox.Config{UnsafeNoSandbox: true, HostEnv: []string{"HOME=" + hostHome}},
		"/bin/true")
	var classified *commentrunner.AgentRuntimeProbeError
	if !errors.As(err, &classified) || classified.Kind != commentrunner.AgentRuntimeFailureAdapter {
		t.Fatalf("prepare error = %v (%T), want bounded adapter category", err, err)
	}
	if strings.Contains(err.Error(), "must-not-leak") || err.Error() != "agent runtime probe adapter" {
		t.Fatalf("prepare error exposed raw adapter diagnostics: %q", err.Error())
	}
}

func TestRunnerPreflightParsesServeParityAndGitAuthorFlags(t *testing.T) {
	clearCommandAuthEnv(t)
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	cfg, _, ok := app.parseRunnerOptions([]string{"--repo", "o/r", "--runner", "runner-bot",
		"--state", filepath.Join(t.TempDir(), "state.json"), "--workspace-root", t.TempDir(),
		"--unsafe-no-sandbox", "--allow-host-ssh", "--git-author-name", "Issue Spec Runner",
		"--git-author-email", "runner@example.test"}, false)
	if !ok {
		t.Fatalf("parse failed: %s", errOut.String())
	}
	if !cfg.AllowHostSSH || !cfg.UnsafeNoSandbox || cfg.GitAuthorName != "Issue Spec Runner" || cfg.GitAuthorEmail != "runner@example.test" {
		t.Fatalf("parsed config = %+v", cfg)
	}
}

func TestRunnerPreflightRejectsHostSSHForGitHubBeforeRuntimeResolution(t *testing.T) {
	clearCommandAuthEnv(t)
	oldCurrent := currentRunnerHostSSHConfig
	currentRunnerHostSSHConfig = func(string) (credentials.HostSSHGitProviderConfig, error) {
		t.Fatal("GitHub poll preflight must reject host SSH before resolving host credentials")
		return credentials.HostSSHGitProviderConfig{}, nil
	}
	t.Cleanup(func() { currentRunnerHostSSHConfig = oldCurrent })

	app := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	cfg := commentrunner.Config{
		Profile: auth.DefaultProfileName, Hostname: "github.com", Repositories: []string{"o/r"}, RunnerIdentity: "runner-bot",
		StatePath: filepath.Join(t.TempDir(), "state.json"), PollInterval: commentrunner.NewDuration(time.Minute),
		FallbackInterval: commentrunner.NewDuration(time.Hour), MaxConcurrentJobs: 1, AcpxPath: "acpx",
		Agent: commentrunner.DefaultAgentConfig(), WorkspaceRoot: t.TempDir(), WorkspaceRetention: commentrunner.NewDuration(time.Hour),
		UnsafeNoSandbox: true, AllowHostSSH: true,
	}
	report := app.runRunnerPreflightWithOptions(t.Context(), cfg, commentrunner.PreflightOptions{VerifyAgentRuntime: true})
	if report.OK {
		t.Fatalf("GitHub poll preflight unexpectedly accepted host SSH: %+v", report)
	}
	check := runnerPreflightCheck(t, report, "host-ssh-transport")
	if check.Status != commentrunner.CheckError || !strings.Contains(check.Detail, "runner serve") {
		t.Fatalf("unexpected host SSH transport report: %+v", report)
	}
	for _, check := range report.Checks {
		if check.Name == "agent-runtime-probe" {
			t.Fatalf("runtime probe ran before host SSH transport rejection: %+v", report)
		}
	}
}

func TestClassifyAgentRuntimeProbeFailure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result acpx.CommandResult
		err    error
		want   string
	}{
		{name: "timeout", result: acpx.CommandResult{Stderr: []byte("adapter timed out")}, err: errors.New("exit 1"), want: commentrunner.AgentRuntimeFailureTimeout},
		{name: "model", result: acpx.CommandResult{Stdout: []byte(`{"error":"model unknown"}`)}, err: errors.New("exit 1"), want: commentrunner.AgentRuntimeFailureModel},
		{name: "adapter", result: acpx.CommandResult{Stderr: []byte("npx could not initialize ACP")}, err: errors.New("exit 1"), want: commentrunner.AgentRuntimeFailureAdapter},
		{name: "adapter error without output", err: errors.New("materialize host acpx codex agent override: token=must-not-leak"), want: commentrunner.AgentRuntimeFailureAdapter},
		{name: "runtime", result: acpx.CommandResult{Stderr: []byte("request failed")}, err: errors.New("exit 1"), want: commentrunner.AgentRuntimeFailureRuntime},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyAgentRuntimeProbeFailure(t.Context(), tc.result, tc.err); got != tc.want {
				t.Fatalf("classification = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRunnerPollDryRunJSONUsesTrustedConfigAndPreflight(t *testing.T) {
	clearCommandAuthEnv(t)
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	var captured commentrunner.Config
	app.runnerPreflight = func(_ context.Context, cfg commentrunner.Config) commentrunner.PreflightReport {
		captured = cfg
		return commentrunner.PreflightReport{
			OK:     true,
			Config: cfg,
			Checks: []commentrunner.PreflightCheck{{
				Name:   "acpx",
				Status: commentrunner.CheckOK,
				Detail: "/test/bin/acpx",
			}},
		}
	}
	app.runnerIntake = func(_ context.Context, cfg commentrunner.Config, opts intake.Options) (intake.Result, error) {
		if !opts.DryRun {
			t.Fatal("runner dry-run must call intake with dry-run enabled")
		}
		if cfg.RunnerIdentity != "issue-spec-bot" {
			t.Fatalf("runner config not passed to intake: %+v", cfg)
		}
		return intake.Result{
			OK:     true,
			DryRun: true,
			Next:   intake.NextStep{PollAt: time.Date(2026, 7, 3, 12, 1, 0, 0, time.UTC)},
		}, nil
	}
	app.runnerReconcile = func(context.Context, commentrunner.Config) (jobs.ReconcileResult, error) {
		t.Fatal("runner dry-run must not reconcile or clean workspaces")
		return jobs.ReconcileResult{}, nil
	}
	app.runnerDispatch = func(context.Context, commentrunner.Config) (jobs.Result, error) {
		t.Fatal("runner dry-run must not dispatch jobs")
		return jobs.Result{}, nil
	}

	code := app.runRunner(context.Background(), []string{
		"poll",
		"--repo", "o/r,other/repo",
		"--runner", "issue-spec-bot",
		"--backend", "gh",
		"--state", "/tmp/state.json",
		"--workspace-root", "/tmp/workspaces",
		"--once",
		"--dry-run",
		"--unsafe-no-sandbox",
		"--json",
	})
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%q stdout=%q", code, errOut.String(), out.String())
	}
	if captured.GitHubBackend != auth.GitHubBackendModeGH || !captured.UnsafeNoSandbox || len(captured.Repositories) != 2 {
		t.Fatalf("runner config not passed to preflight: %+v", captured)
	}
	if captured.StatePath != "/tmp/state.json" || captured.WorkspaceRoot != "/tmp/workspaces" {
		t.Fatalf("explicit runner paths were rewritten: %+v", captured)
	}
	var got runnerDryRunResult
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.OK || got.Mode != "dry-run" || !got.Once || got.Config.RunnerIdentity != "issue-spec-bot" || got.Intake == nil {
		t.Fatalf("unexpected dry-run output: %+v", got)
	}
	if len(got.Actions) == 0 || !strings.Contains(strings.Join(got.Actions, "\n"), "skip GitHub writes") {
		t.Fatalf("dry-run output missing planned actions: %+v", got.Actions)
	}
}

func TestRunnerRejectsInvalidConfig(t *testing.T) {
	clearCommandAuthEnv(t)
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	code := app.runRunner(context.Background(), []string{
		"poll",
		"--repo", "not-a-repo",
		"--runner", "issue-spec-bot",
		"--dry-run",
	})
	if code != 2 {
		t.Fatalf("exit code = %d, want 2, stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
}

func TestRunnerPollUsesDefaultStateAndWorkspaceRoot(t *testing.T) {
	clearCommandAuthEnv(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), "cache"))

	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	var captured commentrunner.Config
	app.runnerPreflight = func(_ context.Context, cfg commentrunner.Config) commentrunner.PreflightReport {
		captured = cfg
		return commentrunner.PreflightReport{OK: true, Config: cfg}
	}
	app.runnerIntake = func(context.Context, commentrunner.Config, intake.Options) (intake.Result, error) {
		return intake.Result{OK: true, DryRun: true}, nil
	}
	app.runnerDispatch = func(context.Context, commentrunner.Config) (jobs.Result, error) {
		t.Fatal("dry-run must not dispatch jobs")
		return jobs.Result{}, nil
	}

	code := app.runRunner(context.Background(), []string{
		"poll",
		"--repo", "o/r",
		"--runner", "issue-spec-bot",
		"--dry-run",
		"--json",
	})
	if code != 0 {
		t.Fatalf("exit code = %d, stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	root := filepath.Join(home, ".issue-spec", "runners", "github.com", "o", "r", "issue-spec-bot")
	if captured.StatePath != filepath.Join(root, "state.json") {
		t.Fatalf("StatePath = %q", captured.StatePath)
	}
	if captured.WorkspaceRoot != filepath.Join(root, "workspaces") {
		t.Fatalf("WorkspaceRoot = %q", captured.WorkspaceRoot)
	}
	if captured.WorkspaceRetention.Duration != 7*24*time.Hour {
		t.Fatalf("WorkspaceRetention = %s, want 168h", captured.WorkspaceRetention.Duration)
	}
	if captured.MaxConcurrentJobs != 3 {
		t.Fatalf("MaxConcurrentJobs = %d, want 3", captured.MaxConcurrentJobs)
	}
}

func TestRunnerPollDefaultPathsIsolateReposAndRunners(t *testing.T) {
	clearCommandAuthEnv(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), "cache"))

	cfgA := captureRunnerPollConfig(t, "--repo", "o/one", "--runner", "bot")
	cfgB := captureRunnerPollConfig(t, "--repo", "o/two", "--runner", "bot")
	cfgC := captureRunnerPollConfig(t, "--repo", "o/one", "--runner", "other-bot")

	if cfgA.StatePath == cfgB.StatePath || cfgA.WorkspaceRoot == cfgB.WorkspaceRoot {
		t.Fatalf("different repos share paths: cfgA=%+v cfgB=%+v", cfgA, cfgB)
	}
	if cfgA.StatePath == cfgC.StatePath || cfgA.WorkspaceRoot == cfgC.WorkspaceRoot {
		t.Fatalf("different runners share paths: cfgA=%+v cfgC=%+v", cfgA, cfgC)
	}
}

func TestRunnerPollMultipleReposUsesStableSharedDefaultScope(t *testing.T) {
	clearCommandAuthEnv(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), "cache"))

	cfgA := captureRunnerPollConfig(t, "--repo", "b/two", "--repo", "a/one", "--runner", "bot")
	cfgB := captureRunnerPollConfig(t, "--repo", "a/one", "--repo", "b/two", "--runner", "bot")

	if cfgA.StatePath != cfgB.StatePath || cfgA.WorkspaceRoot != cfgB.WorkspaceRoot {
		t.Fatalf("multi repo scope is not stable: cfgA=%+v cfgB=%+v", cfgA, cfgB)
	}
	multiRoot := filepath.Join(home, ".issue-spec", "runners", "github.com", "multi")
	if !strings.HasPrefix(cfgA.StatePath, multiRoot+string(filepath.Separator)) {
		t.Fatalf("StatePath = %q, want under %q", cfgA.StatePath, multiRoot)
	}
	if !strings.HasPrefix(cfgA.WorkspaceRoot, multiRoot+string(filepath.Separator)) {
		t.Fatalf("WorkspaceRoot = %q, want under %q", cfgA.WorkspaceRoot, multiRoot)
	}
}

func TestRunnerPollParsesAllowedUsers(t *testing.T) {
	clearCommandAuthEnv(t)
	cfg := captureRunnerPollConfig(t,
		"--repo", "o/r",
		"--runner", "bot",
		"--allowed-user", "alice",
		"--allowed-user", "bob,charlie",
	)

	if got := strings.Join(cfg.AllowedUsers, ","); got != "alice,bob,charlie" {
		t.Fatalf("AllowedUsers = %q, want alice,bob,charlie", got)
	}
}

func TestRunnerPollParsesFallbackInitialLookback(t *testing.T) {
	clearCommandAuthEnv(t)
	cfg := captureRunnerPollConfig(t,
		"--repo", "o/r",
		"--runner", "bot",
		"--fallback-initial-lookback", "168h",
	)

	if cfg.FallbackInitialLookback.Duration != 7*24*time.Hour {
		t.Fatalf("FallbackInitialLookback = %s, want 168h", cfg.FallbackInitialLookback.Duration)
	}
}

func TestRunnerPollParsesNotificationRunnerWithDefaultTokenEnv(t *testing.T) {
	clearCommandAuthEnv(t)
	cfg := captureRunnerPollConfig(t,
		"--repo", "o/r",
		"--runner", "maintainer",
		"--notification-runner", "notify-bot",
	)

	if cfg.NotificationIdentity != "notify-bot" {
		t.Fatalf("NotificationIdentity = %q, want notify-bot", cfg.NotificationIdentity)
	}
	if cfg.NotificationTokenEnv != commentrunner.DefaultNotificationTokenEnv {
		t.Fatalf("NotificationTokenEnv = %q, want %q", cfg.NotificationTokenEnv, commentrunner.DefaultNotificationTokenEnv)
	}
}

func TestRunnerPollParsesNotificationTokenEnvWithoutIdentity(t *testing.T) {
	clearCommandAuthEnv(t)
	cfg := captureRunnerPollConfig(t,
		"--repo", "o/r",
		"--runner", "maintainer",
		"--notification-token-env", "CUSTOM_NOTIFY_TOKEN",
	)

	if cfg.NotificationIdentity != "" {
		t.Fatalf("NotificationIdentity = %q, want empty", cfg.NotificationIdentity)
	}
	if cfg.NotificationTokenEnv != "CUSTOM_NOTIFY_TOKEN" {
		t.Fatalf("NotificationTokenEnv = %q, want CUSTOM_NOTIFY_TOKEN", cfg.NotificationTokenEnv)
	}
}

func TestRunnerPreflightParsesAllowedUsers(t *testing.T) {
	clearCommandAuthEnv(t)
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	var captured commentrunner.Config
	app.runnerPreflight = func(_ context.Context, cfg commentrunner.Config) commentrunner.PreflightReport {
		captured = cfg
		return commentrunner.PreflightReport{OK: true, Config: cfg}
	}

	code := app.runRunner(context.Background(), []string{
		"preflight",
		"--repo", "o/r",
		"--runner", "bot",
		"--allowed-user", "alice,bob",
		"--json",
	})
	if code != 0 {
		t.Fatalf("exit code = %d, stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	if got := strings.Join(captured.AllowedUsers, ","); got != "alice,bob" {
		t.Fatalf("AllowedUsers = %q, want alice,bob", got)
	}
}

func TestRunnerIntakeUsesAllowedUsersAuthorizationPolicy(t *testing.T) {
	clearCommandAuthEnv(t)
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	app.runnerPreflight = func(_ context.Context, cfg commentrunner.Config) commentrunner.PreflightReport {
		return commentrunner.PreflightReport{OK: true, Config: cfg}
	}
	var captured intake.Options
	app.runnerIntake = func(_ context.Context, _ commentrunner.Config, opts intake.Options) (intake.Result, error) {
		captured = opts
		return intake.Result{OK: true, DryRun: true}, nil
	}
	app.runnerDispatch = func(context.Context, commentrunner.Config) (jobs.Result, error) {
		t.Fatal("dry-run must not dispatch jobs")
		return jobs.Result{}, nil
	}

	code := app.runRunner(context.Background(), []string{
		"poll",
		"--repo", "o/r",
		"--runner", "bot",
		"--allowed-user", "alice,bob",
		"--dry-run",
		"--json",
	})
	if code != 0 {
		t.Fatalf("exit code = %d, stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	policy := captured.AuthorizationPolicy
	if !captured.DryRun || policy.RunnerLogin != "bot" || strings.Join(policy.AllowedUsers, ",") != "alice,bob" || policy.AllowAuthenticatedUser {
		t.Fatalf("intake options = %+v, want dry-run policy for bot with alice,bob only", captured)
	}
}

func TestRunnerIntakeInjectsNotificationBackendWhenConfigured(t *testing.T) {
	clearCommandAuthEnv(t)
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	mainBackend := &runnerPhaseBackend{fakeGitHubBackend: fakeGitHubBackend{info: github.BackendInfo{Name: "rest", Kind: "rest", Host: "github.com"}}}
	notificationBackend := &runnerPhaseBackend{fakeGitHubBackend: fakeGitHubBackend{info: github.BackendInfo{Name: "rest", Kind: "rest", Host: "github.com"}}}
	app.selectRunnerBackend = func(_ context.Context, host string, mode auth.GitHubBackendMode) (auth.GitHubBackendSelection, error) {
		return auth.GitHubBackendSelection{
			Mode:            mode,
			Name:            auth.GitHubBackendNameREST,
			Kind:            auth.GitHubBackendKindREST,
			Host:            host,
			SelectionSource: "test",
			Token:           auth.Token{Value: "main-token", Host: host},
		}, nil
	}
	app.newGitHubBackend = func(context.Context, auth.GitHubBackendSelection) (github.Backend, error) {
		return mainBackend, nil
	}
	app.newRunnerNotificationBackend = func(context.Context, commentrunner.Config) (runnerNotificationBackend, error) {
		return notificationBackend, nil
	}
	cfg := commentrunner.Config{
		Hostname:             "github.com",
		Repositories:         []string{"o/r"},
		RunnerIdentity:       "maintainer",
		NotificationIdentity: "notify-bot",
		NotificationTokenEnv: "BOT_TOKEN",
		GitHubBackend:        auth.GitHubBackendModeREST,
		StatePath:            filepath.Join(t.TempDir(), "state.json"),
		PollInterval:         commentrunner.NewDuration(time.Minute),
		FallbackInterval:     commentrunner.NewDuration(time.Hour),
		MaxConcurrentJobs:    1,
		AcpxPath:             "acpx",
		Agent:                commentrunner.DefaultAgentConfig(),
		WorkspaceRoot:        t.TempDir(),
		WorkspaceRetention:   commentrunner.NewDuration(time.Hour),
		CancellationEnabled:  true,
	}

	result, err := app.runRunnerIntake(context.Background(), cfg, intake.Options{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK {
		t.Fatalf("intake result not OK: %+v", result)
	}
	if len(notificationBackend.notificationOpts) != 1 {
		t.Fatalf("notification backend poll calls = %d, want 1", len(notificationBackend.notificationOpts))
	}
	if len(mainBackend.notificationOpts) != 0 {
		t.Fatalf("main backend should not poll notifications: %+v", mainBackend.notificationOpts)
	}
}

func TestDefaultRunnerNotificationBackendFailsClosedWhenTokenEmpty(t *testing.T) {
	t.Setenv("BOT_TOKEN", " ")
	backend, err := defaultRunnerNotificationBackend(context.Background(), commentrunner.Config{NotificationTokenEnv: "BOT_TOKEN"})
	if err == nil || !strings.Contains(err.Error(), "BOT_TOKEN is empty") {
		t.Fatalf("defaultRunnerNotificationBackend error = %v, backend=%T", err, backend)
	}
}

func TestDefaultRunnerNotificationBackendFailsClosedWhenTokenUnset(t *testing.T) {
	unsetEnvForTest(t, "BOT_TOKEN")
	backend, err := defaultRunnerNotificationBackend(context.Background(), commentrunner.Config{NotificationTokenEnv: "BOT_TOKEN"})
	if err == nil || !strings.Contains(err.Error(), "BOT_TOKEN is unset") {
		t.Fatalf("defaultRunnerNotificationBackend error = %v, backend=%T", err, backend)
	}
}

func unsetEnvForTest(t *testing.T, name string) {
	t.Helper()
	oldValue, hadValue := os.LookupEnv(name)
	if err := os.Unsetenv(name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if hadValue {
			_ = os.Setenv(name, oldValue)
			return
		}
		_ = os.Unsetenv(name)
	})
}

func captureRunnerPollConfig(t *testing.T, args ...string) commentrunner.Config {
	t.Helper()
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	var captured commentrunner.Config
	app.runnerPreflight = func(_ context.Context, cfg commentrunner.Config) commentrunner.PreflightReport {
		captured = cfg
		return commentrunner.PreflightReport{OK: true, Config: cfg}
	}
	app.runnerIntake = func(_ context.Context, _ commentrunner.Config, opts intake.Options) (intake.Result, error) {
		if !opts.DryRun {
			t.Fatal("config capture must run intake in dry-run mode")
		}
		return intake.Result{OK: true, DryRun: true}, nil
	}
	app.runnerDispatch = func(context.Context, commentrunner.Config) (jobs.Result, error) {
		t.Fatal("dry-run must not dispatch jobs")
		return jobs.Result{}, nil
	}

	runArgs := append([]string{"poll", "--dry-run", "--json"}, args...)
	code := app.runRunner(context.Background(), runArgs)
	if code != 0 {
		t.Fatalf("exit code = %d, stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	return captured
}

func TestRunnerPreflightProfileFlagFlowsIntoUnifiedSelectionConfig(t *testing.T) {
	clearCommandAuthEnv(t)
	profile := auth.Profile{
		Name: "runner-staging", Kind: auth.ProfileKindHosted,
		APIURL: "https://issues.example.test/compat", NativeAPIURL: "https://issues.example.test/api/v1",
		WebURL: "https://issues.example.test", ServerInstanceID: "runner-instance",
	}
	if err := auth.SaveProfile(profile, false); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	var cfg commentrunner.Config
	app.runnerPreflight = func(_ context.Context, input commentrunner.Config) commentrunner.PreflightReport {
		cfg = input
		return commentrunner.PreflightReport{OK: true, Config: input}
	}
	code := app.runRunner(context.Background(), []string{
		"preflight", "--profile", "runner-staging", "--repo", "o/r", "--runner", "runner-bot",
		"--state", filepath.Join(t.TempDir(), "state.json"), "--workspace-root", t.TempDir(), "--json",
	})
	if code != 0 {
		t.Fatalf("exit code = %d, stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	if cfg.Profile != "runner-staging" {
		t.Fatalf("profile = %q", cfg.Profile)
	}
	t.Setenv("ISSUE_SPEC_TOKEN", "runner-profile-token")
	selection, err := app.selectBackendForRunner(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if selection.Profile.Name != "runner-staging" || selection.Token.Value != "runner-profile-token" || selection.Name != auth.GitHubBackendNameREST {
		t.Fatalf("selection = %+v", selection)
	}
}

func TestRunnerGlobalProfileFlagSurvivesConfigReload(t *testing.T) {
	clearCommandAuthEnv(t)
	if err := auth.SaveProfile(auth.Profile{
		Name: "runner-staging", Kind: auth.ProfileKindGitHub, Hostname: "github.com",
		APIURL: "https://api.github.com", WebURL: "https://github.com",
	}, false); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name             string
		subcommand       string
		includePollFlags bool
	}{
		{name: "preflight", subcommand: "preflight"},
		{name: "poll", subcommand: "poll", includePollFlags: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			statePath := filepath.Join(t.TempDir(), "state.json")
			workspaceRoot := t.TempDir()
			profileName, args, err := extractGlobalProfile([]string{
				"runner", tc.subcommand, "--profile", "runner-staging",
				"--repo", "o/r", "--runner", "runner-bot",
				"--state", statePath, "--workspace-root", workspaceRoot,
			})
			if err != nil {
				t.Fatal(err)
			}

			var out, errOut bytes.Buffer
			app := newApp(strings.NewReader(""), &out, &errOut)
			app.profileName = profileName
			cfg, _, ok := app.parseRunnerOptions(args[2:], tc.includePollFlags)
			if !ok {
				t.Fatalf("parse failed: stdout=%q stderr=%q", out.String(), errOut.String())
			}
			if cfg.Profile != "runner-staging" {
				t.Fatalf("profile = %q, want global profile %q", cfg.Profile, "runner-staging")
			}
		})
	}
}

func TestRunnerPreflightSelfHostedSkipsNotificationAndWatchCalls(t *testing.T) {
	clearCommandAuthEnv(t)
	profile := auth.Profile{
		Name: "runner-staging", Kind: auth.ProfileKindHosted,
		APIURL: "https://issues.example.test/compat", NativeAPIURL: "https://issues.example.test/api/v1",
		WebURL: "https://issues.example.test", ServerInstanceID: "runner-instance",
	}
	if err := auth.SaveProfile(profile, false); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ISSUE_SPEC_TOKEN", "runner-profile-token")
	codexHome := filepath.Join(t.TempDir(), "codex")
	if err := os.MkdirAll(codexHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(codexHome, "auth.json"), []byte(`{"token":"test"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", codexHome)
	binDir := t.TempDir()
	for _, name := range []string{"acpx", "npm", "npx"} {
		if err := os.WriteFile(filepath.Join(binDir, name), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", binDir)

	backend := &runnerPhaseBackend{fakeGitHubBackend: fakeGitHubBackend{info: github.BackendInfo{Name: "rest", Kind: "rest", Host: "issues.example.test"}}}
	app := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	app.newGitHubBackend = func(context.Context, auth.GitHubBackendSelection) (github.Backend, error) {
		return backend, nil
	}
	app.newRunnerNotificationBackend = func(context.Context, commentrunner.Config) (runnerNotificationBackend, error) {
		t.Fatal("self-hosted preflight called notification backend")
		return nil, nil
	}
	cfg := commentrunner.Config{
		Profile: "runner-staging", Hostname: "github.com", Repositories: []string{"o/r"}, RunnerIdentity: "runner-bot",
		NotificationIdentity: "notify-bot", NotificationTokenEnv: "BOT_TOKEN", GitHubBackend: auth.GitHubBackendModeREST,
		StatePath: filepath.Join(t.TempDir(), "state.json"), PollInterval: commentrunner.NewDuration(time.Minute),
		FallbackInterval: commentrunner.NewDuration(time.Hour), MaxConcurrentJobs: 1, AcpxPath: "acpx",
		Agent: commentrunner.DefaultAgentConfig(), WorkspaceRoot: t.TempDir(), WorkspaceRetention: commentrunner.NewDuration(time.Hour),
		UnsafeNoSandbox: true, CancellationEnabled: true,
	}

	report := app.runRunnerPreflight(context.Background(), cfg)
	if !report.OK {
		t.Fatalf("self-hosted preflight failed shared prerequisites: %+v", report)
	}
	if backend.repositorySubscriptionCalls != 0 {
		t.Fatalf("repository subscription calls = %d, want 0", backend.repositorySubscriptionCalls)
	}
	if check := runnerPreflightCheck(t, report, "command-intake-transport"); check.Status != commentrunner.CheckOK || !strings.Contains(check.Detail, "runner serve") {
		t.Fatalf("unexpected transport check: %+v", check)
	}
	for _, check := range report.Checks {
		if strings.HasPrefix(check.Name, "evidence-writer:") || check.Name == "evidence-writer-backend" {
			t.Fatalf("self-hosted preflight emitted designation check: %+v", check)
		}
	}
}

func TestRunnerPollRejectsSelfHostedBeforePreflightOrIntake(t *testing.T) {
	clearCommandAuthEnv(t)
	profile := auth.Profile{
		Name: "runner-staging", Kind: auth.ProfileKindHosted,
		APIURL: "https://issues.example.test/compat", NativeAPIURL: "https://issues.example.test/api/v1",
		WebURL: "https://issues.example.test", ServerInstanceID: "runner-instance",
	}
	if err := auth.SaveProfile(profile, false); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		args []string
	}{
		{name: "dry-run", args: []string{"--dry-run", "--json"}},
		{name: "live", args: []string{"--once"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			app := newApp(strings.NewReader(""), &out, &errOut)
			app.runnerPreflight = func(context.Context, commentrunner.Config) commentrunner.PreflightReport {
				t.Fatal("self-hosted poll called preflight")
				return commentrunner.PreflightReport{}
			}
			app.runnerIntake = func(context.Context, commentrunner.Config, intake.Options) (intake.Result, error) {
				t.Fatal("self-hosted poll called notification intake")
				return intake.Result{}, nil
			}
			app.newRunnerNotificationBackend = func(context.Context, commentrunner.Config) (runnerNotificationBackend, error) {
				t.Fatal("self-hosted poll opened notification backend")
				return nil, nil
			}
			args := []string{
				"poll", "--profile", "runner-staging", "--repo", "o/r", "--runner", "runner-bot",
				"--state", filepath.Join(t.TempDir(), "state.json"), "--workspace-root", t.TempDir(),
			}
			args = append(args, tc.args...)
			code := app.runRunner(context.Background(), args)
			if code != 2 {
				t.Fatalf("exit code = %d, want 2, stdout=%q stderr=%q", code, out.String(), errOut.String())
			}
			want := `runner poll requires a GitHub profile; self-hosted profile "runner-staging" uses runner serve`
			if !strings.Contains(errOut.String(), want) {
				t.Fatalf("stderr = %q, want stable rejection %q", errOut.String(), want)
			}
			if out.Len() != 0 {
				t.Fatalf("stdout = %q, want empty before poll startup", out.String())
			}
		})
	}
}

func TestRunnerPollDryRunIntakeErrorReturnsFailure(t *testing.T) {
	clearCommandAuthEnv(t)
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	app.runnerPreflight = func(_ context.Context, cfg commentrunner.Config) commentrunner.PreflightReport {
		return commentrunner.PreflightReport{OK: true, Config: cfg}
	}
	app.runnerIntake = func(context.Context, commentrunner.Config, intake.Options) (intake.Result, error) {
		return intake.Result{}, errors.New("intake failed")
	}
	code := app.runRunner(context.Background(), []string{
		"poll",
		"--repo", "o/r",
		"--runner", "issue-spec-bot",
		"--state", "/tmp/state.json",
		"--workspace-root", "/tmp/workspaces",
		"--dry-run",
		"--json",
	})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1, stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	var got runnerDryRunResult
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.OK || !strings.Contains(got.Error, "intake failed") {
		t.Fatalf("unexpected dry-run result: %+v", got)
	}
}

func TestRunnerPollDryRunIntakeNotOKReturnsFailure(t *testing.T) {
	clearCommandAuthEnv(t)
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	app.runnerPreflight = func(_ context.Context, cfg commentrunner.Config) commentrunner.PreflightReport {
		return commentrunner.PreflightReport{OK: true, Config: cfg}
	}
	app.runnerIntake = func(context.Context, commentrunner.Config, intake.Options) (intake.Result, error) {
		return intake.Result{OK: false, Diagnostics: []intake.Diagnostic{{Message: "notification failed"}}}, nil
	}
	app.runnerDispatch = func(context.Context, commentrunner.Config) (jobs.Result, error) {
		t.Fatal("dry-run must not dispatch jobs")
		return jobs.Result{}, nil
	}
	code := app.runRunner(context.Background(), []string{
		"poll",
		"--repo", "o/r",
		"--runner", "issue-spec-bot",
		"--state", "/tmp/state.json",
		"--workspace-root", "/tmp/workspaces",
		"--dry-run",
		"--json",
	})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1, stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	var got runnerDryRunResult
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.OK || got.Intake == nil || !strings.Contains(got.Error, "intake reported failure") {
		t.Fatalf("unexpected dry-run result: %+v", got)
	}
}

func TestRunnerPollWithoutDryRunRunsIntakeAndOneDispatch(t *testing.T) {
	clearCommandAuthEnv(t)
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	app.runnerPreflight = func(_ context.Context, cfg commentrunner.Config) commentrunner.PreflightReport {
		return commentrunner.PreflightReport{OK: true, Config: cfg}
	}
	var callOrder []string
	app.runnerReconcile = func(_ context.Context, cfg commentrunner.Config) (jobs.ReconcileResult, error) {
		callOrder = append(callOrder, "reconcile")
		if cfg.WorkspaceRoot != "/tmp/workspaces" {
			t.Fatalf("config not passed to reconcile: %+v", cfg)
		}
		if cfg.WorkspaceRetention.Duration != 7*24*time.Hour {
			t.Fatalf("default workspace retention not passed to reconcile: %s", cfg.WorkspaceRetention.Duration)
		}
		return jobs.ReconcileResult{Reconciled: 1, Running: 1}, nil
	}
	intakeCalled := false
	app.runnerIntake = func(_ context.Context, cfg commentrunner.Config, opts intake.Options) (intake.Result, error) {
		callOrder = append(callOrder, "intake")
		intakeCalled = true
		if opts.DryRun {
			t.Fatal("non-dry-run poll must persist intake")
		}
		if cfg.RunnerIdentity != "issue-spec-bot" {
			t.Fatalf("config not passed to intake: %+v", cfg)
		}
		return intake.Result{OK: true}, nil
	}
	dispatchCalled := false
	app.runnerDispatch = func(_ context.Context, cfg commentrunner.Config) (jobs.Result, error) {
		callOrder = append(callOrder, "dispatch")
		dispatchCalled = true
		if cfg.WorkspaceRoot != "/tmp/workspaces" {
			t.Fatalf("config not passed to dispatch: %+v", cfg)
		}
		if cfg.WorkspaceRetention.Duration != 7*24*time.Hour {
			t.Fatalf("default workspace retention not passed to dispatch: %s", cfg.WorkspaceRetention.Duration)
		}
		return jobs.Result{Executed: true, JobID: "job-1", Status: state.StatusCompleted}, nil
	}
	code := app.runRunner(context.Background(), []string{
		"poll",
		"--repo", "o/r",
		"--runner", "issue-spec-bot",
		"--state", "/tmp/state.json",
		"--workspace-root", "/tmp/workspaces",
		"--once",
		"--json",
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0, stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	if !intakeCalled || !dispatchCalled {
		t.Fatalf("expected intake and dispatch to run: intake=%v dispatch=%v", intakeCalled, dispatchCalled)
	}
	if strings.Join(callOrder, ",") != "reconcile,intake,dispatch" {
		t.Fatalf("unexpected call order: %v", callOrder)
	}
	var got runnerDryRunResult
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Mode != "run" || got.Reconcile == nil || got.Dispatch == nil || got.Dispatch.Status != state.StatusCompleted {
		t.Fatalf("unexpected run output: %+v", got)
	}
}

func TestRunnerPollTextOutputPrintsStartupAndPreflightOnce(t *testing.T) {
	clearCommandAuthEnv(t)
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	app.runnerPreflight = func(_ context.Context, cfg commentrunner.Config) commentrunner.PreflightReport {
		return commentrunner.PreflightReport{
			OK:     true,
			Config: cfg,
			Checks: []commentrunner.PreflightCheck{{
				Name:   "github-backend",
				Status: commentrunner.CheckOK,
				Detail: "gh backend selected",
			}},
		}
	}
	app.runnerReconcile = func(context.Context, commentrunner.Config) (jobs.ReconcileResult, error) {
		return jobs.ReconcileResult{}, nil
	}
	app.runnerIntake = func(context.Context, commentrunner.Config, intake.Options) (intake.Result, error) {
		return intake.Result{OK: true, Next: intake.NextStep{PollAfter: 0}}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dispatchCalls := 0
	app.runnerDispatch = func(context.Context, commentrunner.Config) (jobs.Result, error) {
		dispatchCalls++
		if dispatchCalls == 2 {
			cancel()
		}
		return jobs.Result{Reason: "no ready queued job"}, nil
	}

	code := app.runRunner(ctx, []string{
		"poll",
		"--repo", "o/r",
		"--runner", "issue-spec-bot",
		"--state", "/tmp/state.json",
		"--workspace-root", "/tmp/workspaces",
		"--sync-dispatch",
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0, stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	text := out.String()
	for _, want := range []string{
		"runner poll starting",
		"state: /tmp/state.json",
		"workspace_root: /tmp/workspaces",
		"dispatch: sync",
		"preflight: running",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("output missing %q:\n%s", want, text)
		}
	}
	if got := strings.Count(text, "github-backend: ok"); got != 1 {
		t.Fatalf("preflight checks printed %d times, want 1:\n%s", got, text)
	}
	if got := strings.Count(text, "poll cycle: completed"); got != 2 {
		t.Fatalf("poll cycles printed %d times, want 2:\n%s", got, text)
	}
}

func TestRunnerPollTextOutputIncludesNotificationDiagnostics(t *testing.T) {
	clearCommandAuthEnv(t)
	t.Setenv("BOT_TOKEN", "secret-token-value")
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	app.runnerPreflight = func(_ context.Context, cfg commentrunner.Config) commentrunner.PreflightReport {
		return commentrunner.PreflightReport{OK: true, Config: cfg}
	}
	app.runnerReconcile = func(context.Context, commentrunner.Config) (jobs.ReconcileResult, error) {
		return jobs.ReconcileResult{}, nil
	}
	app.runnerIntake = func(context.Context, commentrunner.Config, intake.Options) (intake.Result, error) {
		now := time.Date(2026, 7, 4, 10, 0, 0, 0, time.UTC)
		return intake.Result{
			OK: true,
			Notification: intake.NotificationPoll{
				Poller:                     "notification_runner",
				ConfiguredIdentity:         "notify-bot",
				TokenEnv:                   "BOT_TOKEN",
				StatusCode:                 http.StatusNotModified,
				NotModified:                true,
				ConditionalETag:            true,
				MatchedNotifications:       0,
				MatchedNotificationThreads: 0,
				Cursor: intake.CursorReport{
					Resource:             "notifications",
					LastStatusCode:       http.StatusNotModified,
					ETagSet:              true,
					LastPollAt:           now,
					XPollIntervalSeconds: 90,
				},
				Message: "HTTP 304 Not Modified means GitHub reported no notification changes for this poller and stored cursor; repository comments fallback still runs when due",
			},
			Repositories: []intake.RepositoryCycle{{
				Repo:             "o/r",
				FallbackDue:      true,
				FallbackExecuted: true,
				FallbackNextAt:   now.Add(5 * time.Minute),
				RepositoryCommentsCursor: intake.CursorReport{
					Resource:       "repo-comments:o/r",
					LastStatusCode: http.StatusOK,
					ETagSet:        true,
					LastSeenID:     123,
				},
				FallbackMessage: "repository comments fallback executed because fallback interval was due",
			}},
			Next: intake.NextStep{PollAt: now.Add(time.Minute)},
		}, nil
	}
	app.runnerDispatch = func(context.Context, commentrunner.Config) (jobs.Result, error) {
		return jobs.Result{Reason: "no ready queued job"}, nil
	}

	code := app.runRunner(context.Background(), []string{
		"poll",
		"--repo", "o/r",
		"--runner", "maintainer",
		"--notification-runner", "notify-bot",
		"--notification-token-env", "BOT_TOKEN",
		"--state", "/tmp/state.json",
		"--workspace-root", "/tmp/workspaces",
		"--once",
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0, stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	text := out.String()
	for _, want := range []string{
		"notification_runner: notify-bot token_env=BOT_TOKEN token=set",
		"notification poll: poller=notification_runner identity=notify-bot token_env=BOT_TOKEN status=304 Not Modified",
		"HTTP 304 Not Modified means",
		"fallback_due=true fallback_executed=true",
		"repo_comments_cursor=status=200 etag=set",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("output missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "secret-token-value") {
		t.Fatalf("output leaked token value:\n%s", text)
	}
}

func TestRunnerPollStopsBeforeDispatchWhenIntakeNotOK(t *testing.T) {
	clearCommandAuthEnv(t)
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	app.runnerPreflight = func(_ context.Context, cfg commentrunner.Config) commentrunner.PreflightReport {
		return commentrunner.PreflightReport{OK: true, Config: cfg}
	}
	app.runnerReconcile = func(context.Context, commentrunner.Config) (jobs.ReconcileResult, error) {
		return jobs.ReconcileResult{}, nil
	}
	app.runnerIntake = func(context.Context, commentrunner.Config, intake.Options) (intake.Result, error) {
		return intake.Result{OK: false, Diagnostics: []intake.Diagnostic{{Message: "rate limited"}}}, nil
	}
	app.runnerDispatch = func(context.Context, commentrunner.Config) (jobs.Result, error) {
		t.Fatal("dispatch must not run after intake reported failure")
		return jobs.Result{}, nil
	}
	code := app.runRunner(context.Background(), []string{
		"poll",
		"--repo", "o/r",
		"--runner", "issue-spec-bot",
		"--state", "/tmp/state.json",
		"--workspace-root", "/tmp/workspaces",
		"--once",
		"--json",
	})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1, stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	var got runnerDryRunResult
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.OK || got.Dispatch != nil || got.Intake == nil || !strings.Contains(got.Error, "intake reported failure") {
		t.Fatalf("unexpected run output: %+v", got)
	}
}

func TestRunnerPollWithoutOnceBacksOffAfterIntakeNotOK(t *testing.T) {
	clearCommandAuthEnv(t)
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	app.runnerPreflight = func(_ context.Context, cfg commentrunner.Config) commentrunner.PreflightReport {
		return commentrunner.PreflightReport{OK: true, Config: cfg}
	}
	var callOrder []string
	reconcileCalls := 0
	app.runnerReconcile = func(context.Context, commentrunner.Config) (jobs.ReconcileResult, error) {
		callOrder = append(callOrder, "reconcile")
		reconcileCalls++
		return jobs.ReconcileResult{}, nil
	}
	retryAfter := 25 * time.Millisecond
	var firstFailureAt time.Time
	intakeCalls := 0
	app.runnerIntake = func(context.Context, commentrunner.Config, intake.Options) (intake.Result, error) {
		callOrder = append(callOrder, "intake")
		intakeCalls++
		switch intakeCalls {
		case 1:
			firstFailureAt = time.Now()
			return intake.Result{
				OK:          false,
				Diagnostics: []intake.Diagnostic{{Source: "notification", Message: "rate limited"}},
				Next:        intake.NextStep{PollAfter: retryAfter, PollAt: firstFailureAt.Add(retryAfter)},
			}, nil
		case 2:
			if waited := time.Since(firstFailureAt); waited < retryAfter {
				t.Fatalf("second intake ran before retry backoff elapsed: waited=%s want>=%s", waited, retryAfter)
			}
			return intake.Result{OK: true, Next: intake.NextStep{PollAfter: 0}}, nil
		default:
			t.Fatalf("unexpected intake call %d", intakeCalls)
			return intake.Result{}, nil
		}
	}
	dispatchCalls := 0
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	app.runnerDispatch = func(context.Context, commentrunner.Config) (jobs.Result, error) {
		callOrder = append(callOrder, "dispatch")
		dispatchCalls++
		cancel()
		return jobs.Result{Reason: "no ready job"}, nil
	}
	code := app.runRunner(ctx, []string{
		"poll",
		"--repo", "o/r",
		"--runner", "issue-spec-bot",
		"--state", "/tmp/state.json",
		"--workspace-root", "/tmp/workspaces",
		"--sync-dispatch",
		"--json",
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0, stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	if reconcileCalls != 2 || intakeCalls != 2 || dispatchCalls != 1 {
		t.Fatalf("loop calls reconcile=%d intake=%d dispatch=%d", reconcileCalls, intakeCalls, dispatchCalls)
	}
	if got := strings.Join(callOrder, ","); got != "reconcile,intake,reconcile,intake,dispatch" {
		t.Fatalf("unexpected call order: %s", got)
	}
	if !strings.Contains(out.String(), "rate limited") || !strings.Contains(out.String(), "intake reported failure") {
		t.Fatalf("failed intake diagnostics missing from output:\n%s", out.String())
	}
}

func TestRunnerPollWithoutOnceLoopsUntilContextCancellation(t *testing.T) {
	clearCommandAuthEnv(t)
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	app.runnerPreflight = func(_ context.Context, cfg commentrunner.Config) commentrunner.PreflightReport {
		return commentrunner.PreflightReport{OK: true, Config: cfg}
	}
	reconcileCalls := 0
	app.runnerReconcile = func(context.Context, commentrunner.Config) (jobs.ReconcileResult, error) {
		reconcileCalls++
		return jobs.ReconcileResult{}, nil
	}
	intakeCalls := 0
	app.runnerIntake = func(context.Context, commentrunner.Config, intake.Options) (intake.Result, error) {
		intakeCalls++
		return intake.Result{OK: true, Next: intake.NextStep{PollAfter: 0}}, nil
	}
	dispatchCalls := 0
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	app.runnerDispatch = func(context.Context, commentrunner.Config) (jobs.Result, error) {
		dispatchCalls++
		if dispatchCalls == 2 {
			cancel()
		}
		return jobs.Result{Reason: "no ready job"}, nil
	}
	code := app.runRunner(ctx, []string{
		"poll",
		"--repo", "o/r",
		"--runner", "issue-spec-bot",
		"--state", "/tmp/state.json",
		"--workspace-root", "/tmp/workspaces",
		"--sync-dispatch",
		"--json",
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0, stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	if reconcileCalls != 2 || intakeCalls != 2 || dispatchCalls != 2 {
		t.Fatalf("loop calls reconcile=%d intake=%d dispatch=%d", reconcileCalls, intakeCalls, dispatchCalls)
	}
}

func TestRunnerPollDefaultsToAsyncDispatchAndDoesNotBlockNextIntake(t *testing.T) {
	clearCommandAuthEnv(t)
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	stubRunnerCancellationDrain(app)
	app.runnerPreflight = func(_ context.Context, cfg commentrunner.Config) commentrunner.PreflightReport {
		return commentrunner.PreflightReport{OK: true, Config: cfg}
	}
	var reconcileCalls int32
	app.runnerReconcile = func(context.Context, commentrunner.Config) (jobs.ReconcileResult, error) {
		atomic.AddInt32(&reconcileCalls, 1)
		return jobs.ReconcileResult{Reconciled: 1}, nil
	}
	dispatchStarted := make(chan struct{}, 1)
	releaseDispatch := make(chan struct{})
	var intakeCalls int32
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	app.runnerIntake = func(context.Context, commentrunner.Config, intake.Options) (intake.Result, error) {
		call := atomic.AddInt32(&intakeCalls, 1)
		switch call {
		case 1:
			return intake.Result{OK: true, Next: intake.NextStep{PollAfter: 25 * time.Millisecond}}, nil
		case 2:
			select {
			case <-dispatchStarted:
			case <-ctx.Done():
				t.Fatalf("dispatch did not start before second intake: %v", ctx.Err())
			}
			close(releaseDispatch)
			cancel()
			return intake.Result{OK: true}, nil
		default:
			t.Fatalf("unexpected intake call %d", call)
			return intake.Result{}, nil
		}
	}
	var dispatchCalls int32
	app.runnerDispatch = func(ctx context.Context, cfg commentrunner.Config) (jobs.Result, error) {
		atomic.AddInt32(&dispatchCalls, 1)
		select {
		case dispatchStarted <- struct{}{}:
		default:
		}
		select {
		case <-releaseDispatch:
			return jobs.Result{Executed: true, JobID: "job-1", Status: state.StatusCompleted}, nil
		case <-ctx.Done():
			return jobs.Result{}, ctx.Err()
		}
	}

	code := app.runRunner(ctx, []string{
		"poll",
		"--repo", "o/r",
		"--runner", "issue-spec-bot",
		"--state", filepath.Join(t.TempDir(), "state.json"),
		"--workspace-root", t.TempDir(),
		"--json",
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0, stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	if got := atomic.LoadInt32(&intakeCalls); got < 2 {
		t.Fatalf("intake calls = %d, want at least 2; stdout=%q stderr=%q", got, out.String(), errOut.String())
	}
	if got := atomic.LoadInt32(&dispatchCalls); got == 0 {
		t.Fatalf("dispatch was not triggered")
	}
	if got := atomic.LoadInt32(&reconcileCalls); got != 1 {
		t.Fatalf("async reconcile calls = %d, want only startup reconcile while dispatch is busy", got)
	}
}

func TestRunnerPollAsyncRunsPeriodicReconcileWhenDispatchIdle(t *testing.T) {
	clearCommandAuthEnv(t)
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	stubRunnerCancellationDrain(app)
	app.runnerPreflight = func(_ context.Context, cfg commentrunner.Config) commentrunner.PreflightReport {
		return commentrunner.PreflightReport{OK: true, Config: cfg}
	}
	var reconcileCalls int32
	app.runnerReconcile = func(context.Context, commentrunner.Config) (jobs.ReconcileResult, error) {
		atomic.AddInt32(&reconcileCalls, 1)
		return jobs.ReconcileResult{Reconciled: 1}, nil
	}
	var intakeCalls int32
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	app.runnerIntake = func(context.Context, commentrunner.Config, intake.Options) (intake.Result, error) {
		call := atomic.AddInt32(&intakeCalls, 1)
		if call == 2 {
			cancel()
		}
		return intake.Result{OK: true, Next: intake.NextStep{PollAfter: 25 * time.Millisecond}}, nil
	}
	app.runnerDispatch = func(context.Context, commentrunner.Config) (jobs.Result, error) {
		return jobs.Result{Reason: "no ready queued job"}, nil
	}

	code := app.runRunner(ctx, []string{
		"poll",
		"--repo", "o/r",
		"--runner", "issue-spec-bot",
		"--state", filepath.Join(t.TempDir(), "state.json"),
		"--workspace-root", t.TempDir(),
		"--json",
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0, stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	if got := atomic.LoadInt32(&intakeCalls); got < 2 {
		t.Fatalf("intake calls = %d, want at least 2", got)
	}
	if got := atomic.LoadInt32(&reconcileCalls); got < 2 {
		t.Fatalf("async reconcile calls = %d, want startup and periodic reconcile when dispatch is idle", got)
	}
}

func TestRunnerPollAsyncDispatchCleansWorkspacesAfterStartup(t *testing.T) {
	skipWithoutRealGit(t)
	clearCommandAuthEnv(t)
	statePath := filepath.Join(t.TempDir(), "state.json")
	workspaceRoot := t.TempDir()
	expiredPath := filepath.Join(workspaceRoot, "expired")
	if err := os.MkdirAll(expiredPath, 0o700); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("git", "init", "-b", "main")
	command.Dir = expiredPath
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("initialize expired Git workspace: %v: %s", err, output)
	}
	st := state.NewState()
	now := time.Now().UTC()
	if err := st.UpsertWorkspace(state.WorkspaceMetadata{
		ID:           "ws-expired",
		Path:         expiredPath,
		Repo:         "o/r",
		CreatedAt:    now.Add(-3 * time.Hour),
		LastUsedAt:   now.Add(-3 * time.Hour),
		CleanupAfter: now.Add(-2 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if err := state.SaveFile(statePath, st); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	stubRunnerCancellationDrain(app)
	app.runnerPreflight = func(_ context.Context, cfg commentrunner.Config) commentrunner.PreflightReport {
		return commentrunner.PreflightReport{OK: true, Config: cfg}
	}
	var reconcileCalls int32
	app.runnerReconcile = func(context.Context, commentrunner.Config) (jobs.ReconcileResult, error) {
		atomic.AddInt32(&reconcileCalls, 1)
		return jobs.ReconcileResult{}, nil
	}
	var intakeCalls int32
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	app.runnerIntake = func(context.Context, commentrunner.Config, intake.Options) (intake.Result, error) {
		call := atomic.AddInt32(&intakeCalls, 1)
		if call >= 2 {
			cancel()
		}
		return intake.Result{OK: true, Next: intake.NextStep{PollAfter: 50 * time.Millisecond}}, nil
	}
	app.runnerDispatch = func(ctx context.Context, cfg commentrunner.Config) (jobs.Result, error) {
		<-ctx.Done()
		return jobs.Result{}, ctx.Err()
	}

	code := app.runRunner(ctx, []string{
		"poll",
		"--repo", "o/r",
		"--runner", "issue-spec-bot",
		"--state", statePath,
		"--workspace-root", workspaceRoot,
		"--workspace-retention", "1h",
		"--json",
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0, stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	if got := atomic.LoadInt32(&reconcileCalls); got != 1 {
		t.Fatalf("async startup reconcile calls = %d, want 1", got)
	}
	if got := atomic.LoadInt32(&intakeCalls); got < 2 {
		t.Fatalf("intake calls = %d, want at least 2", got)
	}
	if _, err := os.Stat(expiredPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expired workspace still exists or unexpected stat error: %v", err)
	}
	loaded, err := state.LoadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := loaded.GetWorkspace("ws-expired"); ok {
		t.Fatalf("removed workspace still indexed: %+v", loaded.Workspaces["ws-expired"])
	}
}

func stubRunnerCancellationDrain(app *app) {
	app.runnerCancellationDrain = func(context.Context, commentrunner.Config) (jobs.Result, error) {
		return jobs.Result{}, nil
	}
}

func TestRunnerPollRejectsAsyncDispatchWithOnce(t *testing.T) {
	clearCommandAuthEnv(t)
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	code := app.runRunner(context.Background(), []string{
		"poll",
		"--repo", "o/r",
		"--runner", "issue-spec-bot",
		"--state", filepath.Join(t.TempDir(), "state.json"),
		"--workspace-root", t.TempDir(),
		"--async-dispatch",
		"--once",
	})
	if code != 2 {
		t.Fatalf("exit code = %d, want 2, stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	if !strings.Contains(errOut.String(), "--async-dispatch cannot be combined with --once") {
		t.Fatalf("stderr missing async/once error: %q", errOut.String())
	}
}

func TestRunnerPollRejectsAsyncAndSyncDispatch(t *testing.T) {
	clearCommandAuthEnv(t)
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	code := app.runRunner(context.Background(), []string{
		"poll",
		"--repo", "o/r",
		"--runner", "issue-spec-bot",
		"--state", filepath.Join(t.TempDir(), "state.json"),
		"--workspace-root", t.TempDir(),
		"--async-dispatch",
		"--sync-dispatch",
	})
	if code != 2 {
		t.Fatalf("exit code = %d, want 2, stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	if !strings.Contains(errOut.String(), "--async-dispatch cannot be combined with --sync-dispatch") {
		t.Fatalf("stderr missing async/sync error: %q", errOut.String())
	}
}

// requireHostCodexAuth skips the caller when the host Codex auth file is
// unavailable. The runner preflight includes a codex-auth check that reads
// $CODEX_HOME/auth.json (or ~/.codex/auth.json), so tests that expect preflight
// to succeed are only meaningful on a machine where that credential exists.
// This keeps `go test ./...` hermetic on clean CI runners that have no Codex
// login, matching internal/commentrunner.hostCodexConfigDir resolution.
func requireHostCodexAuth(t *testing.T) {
	t.Helper()
	dir := strings.TrimSpace(os.Getenv("CODEX_HOME"))
	if dir == "" {
		home := strings.TrimSpace(os.Getenv("HOME"))
		if home == "" {
			var err error
			if home, err = os.UserHomeDir(); err != nil {
				t.Skipf("codex auth unavailable: cannot resolve home directory: %v", err)
			}
		}
		dir = filepath.Join(home, ".codex")
	}
	authPath := filepath.Join(dir, "auth.json")
	if info, err := os.Stat(authPath); err != nil || info.IsDir() {
		t.Skipf("codex auth (%s) not available; skipping runner preflight-dependent test", authPath)
	}
}

func TestRunnerBackendFlagOverridesEnvForEveryPhase(t *testing.T) {
	requireHostCodexAuth(t)
	clearCommandAuthEnv(t)
	t.Setenv(auth.ProfileEnv, "")
	t.Chdir(t.TempDir())
	t.Setenv(auth.GitHubBackendEnv, "gh")
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	app.selectGitHubBackend = func(context.Context, string) (auth.GitHubBackendSelection, error) {
		t.Fatal("runner must not use env-only backend selector")
		return auth.GitHubBackendSelection{}, nil
	}
	var selectedModes []auth.GitHubBackendMode
	app.selectRunnerBackend = func(_ context.Context, host string, mode auth.GitHubBackendMode) (auth.GitHubBackendSelection, error) {
		selectedModes = append(selectedModes, mode)
		return auth.GitHubBackendSelection{
			Mode:            mode,
			Name:            auth.GitHubBackendNameREST,
			Kind:            auth.GitHubBackendKindREST,
			Host:            host,
			SelectionSource: "test",
			Token:           auth.Token{Value: "token", Host: host},
		}, nil
	}
	app.newGitHubBackend = func(context.Context, auth.GitHubBackendSelection) (github.Backend, error) {
		return &runnerPhaseBackend{fakeGitHubBackend: fakeGitHubBackend{info: github.BackendInfo{Name: "rest", Kind: "rest", Host: "github.com"}}}, nil
	}
	code := app.runRunner(context.Background(), []string{
		"poll",
		"--repo", "o/r",
		"--runner", "issue-spec-bot",
		"--backend", "rest",
		"--state", t.TempDir() + "/state.json",
		"--workspace-root", t.TempDir(),
		"--once",
		"--unsafe-no-sandbox",
		"--acpx-path", os.Args[0],
		"--json",
	})
	if code != 0 {
		t.Fatalf("exit code = %d, stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	if len(selectedModes) < 4 {
		t.Fatalf("selected modes = %v, want preflight/intake/reconcile/dispatch", selectedModes)
	}
	for _, mode := range selectedModes {
		if mode != auth.GitHubBackendModeREST {
			t.Fatalf("selected mode = %s, want flag rest; all modes=%v", mode, selectedModes)
		}
	}
}

type runnerPhaseBackend struct {
	fakeGitHubBackend
	notificationOpts            []github.NotificationListOptions
	repositorySubscriptionCalls int
}

func (b *runnerPhaseBackend) PollNotifications(_ context.Context, opts github.NotificationListOptions) (github.NotificationListResult, error) {
	b.notificationOpts = append(b.notificationOpts, opts)
	return github.NotificationListResult{Metadata: github.ResponseMetadata{StatusCode: http.StatusNotModified, NotModified: true}}, nil
}

func (b *runnerPhaseBackend) GetRepositorySubscription(context.Context, string) (github.RepositorySubscriptionResult, error) {
	b.repositorySubscriptionCalls++
	return github.RepositorySubscriptionResult{Subscription: github.RepositorySubscription{Subscribed: true, Reason: "subscribed"}}, nil
}

func runnerPreflightCheck(t *testing.T, report commentrunner.PreflightReport, name string) commentrunner.PreflightCheck {
	t.Helper()
	for _, check := range report.Checks {
		if check.Name == name {
			return check
		}
	}
	t.Fatalf("missing preflight check %q in %+v", name, report.Checks)
	return commentrunner.PreflightCheck{}
}

func (b *runnerPhaseBackend) GetIssueContext(context.Context, string, int, github.ConditionalRequest) (github.IssueContextResult, error) {
	return github.IssueContextResult{}, nil
}

func (b *runnerPhaseBackend) ListIssueCommentsPage(context.Context, string, int, github.CommentListOptions) (github.IssueCommentsResult, error) {
	return github.IssueCommentsResult{Metadata: github.ResponseMetadata{StatusCode: http.StatusOK}}, nil
}

func (b *runnerPhaseBackend) ListRepositoryIssueCommentsPage(context.Context, string, github.CommentListOptions) (github.IssueCommentsResult, error) {
	return github.IssueCommentsResult{Metadata: github.ResponseMetadata{StatusCode: http.StatusOK}}, nil
}

func (b *runnerPhaseBackend) ListCommentReactionsPage(context.Context, string, int64, github.RunnerPageOptions) (github.CommentReactionsResult, error) {
	return github.CommentReactionsResult{Metadata: github.ResponseMetadata{StatusCode: http.StatusOK}}, nil
}

func (b *runnerPhaseBackend) GetCollaboratorPermission(context.Context, string, string) (github.CollaboratorPermissionResult, error) {
	return github.CollaboratorPermissionResult{Permission: github.CollaboratorPermission{Permission: "write"}, CanWrite: true}, nil
}

func (b *runnerPhaseBackend) CreateRunnerComment(context.Context, string, int, string) (github.RunnerCommentResult, error) {
	return github.RunnerCommentResult{}, nil
}

func (b *runnerPhaseBackend) UpdateRunnerComment(context.Context, string, int64, string) (github.RunnerCommentResult, error) {
	return github.RunnerCommentResult{}, nil
}

func (b *runnerPhaseBackend) AddCommentReaction(context.Context, string, int64, string) (github.RunnerReactionResult, error) {
	return github.RunnerReactionResult{}, nil
}
