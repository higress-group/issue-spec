package credentials

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestCommandGitProviderAcquireAndRevokeProtocol(t *testing.T) {
	provider := commandGitProviderForTest(t, "ok", 1<<20)
	lease, err := provider.Acquire(t.Context(), GitRequest{JobID: "job-protocol", Purpose: "git.clone", Binding: testBinding()})
	if err != nil {
		t.Fatal(err)
	}
	if lease.Credential.Username != "runner" || lease.Credential.Password != "short-lived-secret" || lease.Revoke == nil ||
		lease.ExpiresAt.Before(time.Now().UTC()) {
		t.Fatalf("lease=%+v", lease)
	}
	if err := lease.Revoke(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := lease.Revoke(t.Context()); err != nil {
		t.Fatalf("idempotent revoke: %v", err)
	}
	if err := provider.RevokeJob(t.Context(), "job-protocol"); err != nil {
		t.Fatal(err)
	}
}

func TestCommandGitProviderBoundsAndDoesNotSurfaceAdapterSecrets(t *testing.T) {
	for _, test := range []struct {
		mode  string
		limit int64
	}{
		{mode: "duplicate", limit: 1 << 20},
		{mode: "overflow", limit: 1024},
		{mode: "failure-secret", limit: 1 << 20},
	} {
		t.Run(test.mode, func(t *testing.T) {
			provider := commandGitProviderForTest(t, test.mode, test.limit)
			_, err := provider.Acquire(t.Context(), GitRequest{JobID: "job-bounds", Purpose: "git.clone", Binding: testBinding()})
			if err == nil || strings.Contains(err.Error(), "adapter-super-secret") {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestCommandGitProviderRequiresExactResponseIdentityAndIsolatesEnvironment(t *testing.T) {
	t.Setenv("ISSUE_SPEC_TOKEN", "ambient-parent-secret")
	t.Setenv("GH_TOKEN", "ambient-github-secret")
	provider := commandGitProviderForTest(t, "ambient", 1<<20)
	if _, err := provider.Acquire(t.Context(), GitRequest{JobID: "job-isolated", Purpose: "git.clone", Binding: testBinding()}); err != nil {
		t.Fatal(err)
	}
	provider = commandGitProviderForTest(t, "identity-mismatch", 1<<20)
	_, err := provider.Acquire(t.Context(), GitRequest{JobID: "job-identity", Purpose: "git.clone", Binding: testBinding()})
	if err == nil || !strings.Contains(err.Error(), "identity mismatch") {
		t.Fatalf("identity mismatch error=%v", err)
	}
	provider = commandGitProviderForTest(t, "provider-error", 1<<20)
	_, err = provider.Acquire(t.Context(), GitRequest{JobID: "job-provider-error", Purpose: "git.clone", Binding: testBinding()})
	if err == nil || !strings.Contains(err.Error(), "temporarily_unavailable") {
		t.Fatalf("provider error=%v", err)
	}
}

func commandGitProviderForTest(t *testing.T, mode string, maxOutput int64) *CommandGitProvider {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	provider, err := NewCommandGitProvider(CommandGitProviderConfig{Path: executable,
		Args: []string{"-test.run=^TestCommandGitProviderHelper$", "--", mode}, Timeout: 10 * time.Second,
		MaxOutput: maxOutput, MaxConcurrent: 1})
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

func commandGitProviderForTestArgs(t *testing.T, mode string, maxOutput int64, extra ...string) *CommandGitProvider {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"-test.run=^TestCommandGitProviderHelper$", "--", mode}
	args = append(args, extra...)
	provider, err := NewCommandGitProvider(CommandGitProviderConfig{Path: executable, Args: args,
		Timeout: 10 * time.Second, MaxOutput: maxOutput, MaxConcurrent: 1})
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

func TestCommandGitProviderHelper(t *testing.T) {
	mode := ""
	var helperArgs []string
	for index, argument := range os.Args {
		if argument == "--" && index+1 < len(os.Args) {
			mode = os.Args[index+1]
			helperArgs = os.Args[index+2:]
			break
		}
	}
	if mode == "" {
		return
	}
	var request gitCommandRequest
	decoder := json.NewDecoder(os.Stdin)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || request.Protocol != commandGitProtocol || request.RequestID == "" || request.Action == "" {
		os.Exit(2)
	}
	if mode == "ambient" && (os.Getenv("ISSUE_SPEC_TOKEN") != "" || os.Getenv("GH_TOKEN") != "") {
		_, _ = os.Stderr.WriteString("ambient credential leaked")
		os.Exit(4)
	}
	if mode == "audit" {
		if len(helperArgs) != 1 {
			os.Exit(5)
		}
		file, err := os.OpenFile(helperArgs[0], os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			os.Exit(5)
		}
		_, err = fmt.Fprintf(file, "%s %s\n", request.Action, request.Identity.JobID)
		if closeErr := file.Close(); err != nil || closeErr != nil {
			os.Exit(5)
		}
	}
	switch mode {
	case "overflow":
		_, _ = os.Stdout.WriteString(strings.Repeat("x", 4096))
		os.Exit(0)
	case "failure-secret":
		_, _ = os.Stderr.WriteString("adapter-super-secret")
		os.Exit(3)
	case "duplicate":
		_, _ = fmt.Fprintf(os.Stdout, `{"protocol":%q,"request_id":%q,"request_id":%q,"action":%q,"identity":{}}`,
			commandGitProtocol, request.RequestID, request.RequestID, request.Action)
		os.Exit(0)
	}
	identity := request.Identity
	response := map[string]interface{}{"protocol": commandGitProtocol, "request_id": request.RequestID,
		"action": request.Action, "identity": &identity}
	if request.Action == "acquire" {
		if mode == "provider-error" {
			response["error"] = map[string]string{"code": "temporarily_unavailable"}
			_ = json.NewEncoder(os.Stdout).Encode(response)
			os.Exit(0)
		}
		identity.LeaseID = "lease-1"
		if mode == "identity-mismatch" {
			identity.JobID = "another-job"
		}
		response["lease"] = map[string]interface{}{"lease_id": "lease-1", "username": "runner",
			"password": "short-lived-secret", "expires_at": time.Now().UTC().Add(time.Minute)}
	} else {
		response["revoked"] = true
	}
	_ = json.NewEncoder(os.Stdout).Encode(response)
	os.Exit(0)
}
