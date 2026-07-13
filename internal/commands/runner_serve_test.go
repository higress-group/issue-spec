package commands

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/commentrunner"
	crstate "github.com/higress-group/issue-spec/internal/commentrunner/state"
)

func TestRunnerServeSelfHostedUsesNoGitHubTransportAndLeaksNoSecrets(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("ISSUE_SPEC_CONFIG_DIR", configDir)
	profile := auth.Profile{Name: "runner-staging", Kind: auth.ProfileKindHosted,
		APIURL: "https://issues.example.test/api/v3", NativeAPIURL: "https://issues.example.test/api/v1",
		WebURL: "https://issues.example.test", ServerInstanceID: "runner-instance"}
	if err := auth.SaveProfile(profile, false); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(t.TempDir(), "runner-state.json")
	currentSecret := strings.Repeat("c", 32)
	previousSecret := strings.Repeat("p", 32)
	parentToken := "origin-bound-parent-token"
	t.Setenv("RUNNER_CURRENT_SECRET", currentSecret)
	t.Setenv("RUNNER_PREVIOUS_SECRET", previousSecret)
	t.Setenv("ISSUE_SPEC_TOKEN", parentToken)
	previousExpiry := time.Now().UTC().Add(10 * time.Minute).Truncate(time.Second).Format(time.RFC3339)
	originalBuild, originalRun := runnerServeBuildRuntime, runnerServeRun
	called := false
	runnerServeBuildRuntime = func(_ context.Context, input runnerServeRuntimeInput) (runnerServeRuntime, error) {
		if input.ParentToken != parentToken || input.Profile.Name != profile.Name || len(input.Runner.Repositories) != 1 {
			t.Fatalf("runtime input=%+v", input)
		}
		return runnerServeRuntimeFunc(func(context.Context) error { return nil }), nil
	}
	runnerServeRun = func(ctx context.Context, runtime runnerServeRuntime) error {
		called = true
		return runtime.Run(ctx)
	}
	t.Cleanup(func() { runnerServeBuildRuntime, runnerServeRun = originalBuild, originalRun })
	var stdout, stderr bytes.Buffer
	app := newApp(strings.NewReader(""), &stdout, &stderr)
	app.profileName = profile.Name
	app.runnerPreflight = func(context.Context, commentrunner.Config) commentrunner.PreflightReport {
		t.Fatal("self-hosted serve called polling preflight")
		return commentrunner.PreflightReport{}
	}
	app.selectRunnerBackend = func(context.Context, string, auth.GitHubBackendMode) (auth.GitHubBackendSelection, error) {
		t.Fatal("self-hosted serve selected a GitHub backend")
		return auth.GitHubBackendSelection{}, nil
	}
	app.newRunnerNotificationBackend = func(context.Context, commentrunner.Config) (runnerNotificationBackend, error) {
		t.Fatal("self-hosted serve opened notification transport")
		return nil, nil
	}
	code := app.runRunner(context.Background(), []string{"serve", "--repo", "o/r", "--runner", "runner-bot",
		"--state", statePath, "--subscription-id", uuid.NewString(), "--secret-env", "RUNNER_CURRENT_SECRET",
		"--previous-secret-env", "RUNNER_PREVIOUS_SECRET", "--previous-secrets-valid-until", previousExpiry,
		"--git-credential-command", "/usr/bin/true"})
	if code != 0 || !called {
		t.Fatalf("serve code=%d called=%v stdout=%q stderr=%q", code, called, stdout.String(), stderr.String())
	}
	for _, secret := range []string{currentSecret, previousSecret} {
		if strings.Contains(stdout.String(), secret) || strings.Contains(stderr.String(), secret) {
			t.Fatalf("CLI output leaked secret %q", secret)
		}
	}
	if _, exists := os.LookupEnv("RUNNER_CURRENT_SECRET"); exists {
		t.Fatal("current secret remained in process environment")
	}
	if _, exists := os.LookupEnv("RUNNER_PREVIOUS_SECRET"); exists {
		t.Fatal("previous secret remained in process environment")
	}
	if _, exists := os.LookupEnv("ISSUE_SPEC_TOKEN"); exists {
		t.Fatal("parent token remained in process environment")
	}
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(fmt.Sprintf(`"schema_version": %d`, crstate.SchemaVersion))) || bytes.Contains(data, []byte(currentSecret)) ||
		bytes.Contains(data, []byte(previousSecret)) ||
		bytes.Contains(data, []byte("RUNNER_CURRENT_SECRET")) {
		t.Fatalf("state contains secret/config material: %s", data)
	}
}

type runnerServeRuntimeFunc func(context.Context) error

func (f runnerServeRuntimeFunc) Run(ctx context.Context) error { return f(ctx) }

func TestRunnerServeRejectsGitHubProfilesAndPlaintextSecretArguments(t *testing.T) {
	var stdout, stderr bytes.Buffer
	app := newApp(strings.NewReader(""), &stdout, &stderr)
	app.profileName = auth.DefaultProfileName
	if code := app.runRunner(context.Background(), []string{"serve", "--repo", "o/r", "--runner", "bot",
		"--subscription-id", uuid.NewString(), "--secret-env", "DOES_NOT_MATTER"}); code != 2 ||
		!strings.Contains(stderr.String(), "GitHub profiles use runner poll") {
		t.Fatalf("GitHub serve code=%d stderr=%q", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := app.runRunner(context.Background(), []string{"serve", "--secret", "plaintext-super-secret"}); code != 2 {
		t.Fatalf("plaintext secret flag code=%d", code)
	}
	if strings.Contains(stdout.String(), "plaintext-super-secret") || strings.Contains(stderr.String(), "plaintext-super-secret") {
		t.Fatal("unknown plaintext secret argument was echoed")
	}
}

func TestPreviousSecretAbsoluteExpiryDoesNotExtendAcrossRestart(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	expiry := now.Add(30 * time.Minute)
	first, err := parsePreviousExpiry(now, expiry.Format(time.RFC3339), 1)
	if err != nil {
		t.Fatal(err)
	}
	second, err := parsePreviousExpiry(now.Add(10*time.Minute), expiry.Format(time.RFC3339), 1)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Equal(expiry) || !second.Equal(expiry) {
		t.Fatalf("expiry changed across restart: %s %s want %s", first, second, expiry)
	}
	for _, invalid := range []time.Time{now, now.Add(-time.Second)} {
		if _, err := parsePreviousExpiry(now, invalid.Format(time.RFC3339), 1); err == nil {
			t.Fatalf("non-future previous expiry accepted: %s", invalid)
		}
	}
}

func TestRunnerServeHelpDocumentsSecurityAndCapacityControls(t *testing.T) {
	var stdout, stderr bytes.Buffer
	app := newApp(strings.NewReader(""), &stdout, &stderr)
	if code := app.runRunner(context.Background(), []string{"serve", "--help"}); code != 0 {
		t.Fatalf("help code=%d stderr=%q", code, stderr.String())
	}
	for _, required := range []string{"--listen", "--tls-cert", "--tls-key", "--subscription-id",
		"--secret-file", "--previous-secrets-valid-until", "--timestamp-window", "--max-body-bytes",
		"--max-header-bytes", "--max-queue-deliveries", "--max-queue-bytes", "--shutdown-timeout",
		"--workspace-root", "--max-concurrent-jobs", "--reconcile-workers", "--git-credential-command",
		"--allow-host-ssh"} {
		if !strings.Contains(stdout.String(), required) {
			t.Fatalf("help missing %s:\n%s", required, stdout.String())
		}
	}
}

func TestRunnerServeRejectsUnsafeHostSSHFlagCombinations(t *testing.T) {
	t.Setenv("ISSUE_SPEC_CONFIG_DIR", t.TempDir())
	profile := auth.Profile{Name: "runner-host-ssh", Kind: auth.ProfileKindHosted,
		APIURL: "https://issues.example.test/api/v3", NativeAPIURL: "https://issues.example.test/api/v1",
		WebURL: "https://issues.example.test", ServerInstanceID: "runner-instance"}
	if err := auth.SaveProfile(profile, false); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{name: "both credential modes", args: []string{"--allow-host-ssh", "--git-credential-command", "/usr/bin/true"}, want: "exactly one of --git-credential-command or --allow-host-ssh"},
		{name: "sandbox disabled", args: []string{"--allow-host-ssh", "--unsafe-no-sandbox"}, want: "requires the filesystem sandbox"},
		{name: "credential args without command", args: []string{"--allow-host-ssh", "--git-credential-arg", "value"}, want: "--git-credential-arg requires --git-credential-command"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			app := newApp(strings.NewReader(""), &stdout, &stderr)
			app.profileName = profile.Name
			args := []string{"serve", "--repo", "o/r", "--runner", "bot"}
			args = append(args, tc.args...)
			if code := app.runRunner(context.Background(), args); code != 2 || !strings.Contains(stderr.String(), tc.want) {
				t.Fatalf("code=%d stderr=%q want=%q", code, stderr.String(), tc.want)
			}
		})
	}
}

func TestRunnerServeAcceptsExplicitHostSSHMode(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("ISSUE_SPEC_CONFIG_DIR", configDir)
	profile := auth.Profile{Name: "runner-host-ssh", Kind: auth.ProfileKindHosted,
		APIURL: "https://issues.example.test/api/v3", NativeAPIURL: "https://issues.example.test/api/v1",
		WebURL: "https://issues.example.test", ServerInstanceID: "runner-instance"}
	if err := auth.SaveProfile(profile, false); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RUNNER_HOST_SSH_SECRET", strings.Repeat("s", 32))
	t.Setenv("ISSUE_SPEC_TOKEN", "origin-bound-parent-token")

	originalBuild, originalRun := runnerServeBuildRuntime, runnerServeRun
	called := false
	runnerServeBuildRuntime = func(_ context.Context, input runnerServeRuntimeInput) (runnerServeRuntime, error) {
		if !input.Runner.AllowHostSSH || input.GitCredentialCommand != "" {
			t.Fatalf("runtime input=%+v", input)
		}
		return runnerServeRuntimeFunc(func(context.Context) error { return nil }), nil
	}
	runnerServeRun = func(ctx context.Context, runtime runnerServeRuntime) error {
		called = true
		return runtime.Run(ctx)
	}
	t.Cleanup(func() { runnerServeBuildRuntime, runnerServeRun = originalBuild, originalRun })

	var stdout, stderr bytes.Buffer
	app := newApp(strings.NewReader(""), &stdout, &stderr)
	app.profileName = profile.Name
	code := app.runRunner(context.Background(), []string{"serve", "--repo", "o/r", "--runner", "runner-bot",
		"--state", filepath.Join(t.TempDir(), "state.json"), "--subscription-id", uuid.NewString(),
		"--secret-env", "RUNNER_HOST_SSH_SECRET", "--allow-host-ssh"})
	if code != 0 || !called {
		t.Fatalf("serve code=%d called=%v stdout=%q stderr=%q", code, called, stdout.String(), stderr.String())
	}
}
