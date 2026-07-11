package commands

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/commentrunner"
	runnerserver "github.com/higress-group/issue-spec/internal/commentrunner/server"
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
	t.Setenv("RUNNER_CURRENT_SECRET", currentSecret)
	t.Setenv("RUNNER_PREVIOUS_SECRET", previousSecret)
	previousExpiry := time.Now().UTC().Add(10 * time.Minute).Truncate(time.Second).Format(time.RFC3339)
	originalRun := runnerServeRun
	called := false
	runnerServeRun = func(context.Context, *runnerserver.Service) error {
		called = true
		return nil
	}
	t.Cleanup(func() { runnerServeRun = originalRun })
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
		"--previous-secret-env", "RUNNER_PREVIOUS_SECRET", "--previous-secrets-valid-until", previousExpiry})
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
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(`"schema_version": 3`)) || bytes.Contains(data, []byte(currentSecret)) ||
		bytes.Contains(data, []byte(previousSecret)) ||
		bytes.Contains(data, []byte("RUNNER_CURRENT_SECRET")) {
		t.Fatalf("state contains secret/config material: %s", data)
	}
}

func TestRunnerServeRejectsGitHubProfilesAndPlaintextSecretArguments(t *testing.T) {
	var stdout, stderr bytes.Buffer
	app := newApp(strings.NewReader(""), &stdout, &stderr)
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
		"--max-header-bytes", "--max-queue-deliveries", "--max-queue-bytes", "--shutdown-timeout"} {
		if !strings.Contains(stdout.String(), required) {
			t.Fatalf("help missing %s:\n%s", required, stdout.String())
		}
	}
}
