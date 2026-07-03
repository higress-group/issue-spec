package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/higress-group/issue-spec/internal/commentrunner"
)

func TestRunnerPollDryRunReportsPreflightPlan(t *testing.T) {
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	code := app.runRunnerPoll(context.Background(), []string{
		"--repo", "o/r, o/s",
		"--once",
		"--dry-run",
		"--backend-mode", "gh",
		"--state-path", "/tmp/state.json",
		"--workspace-root", "/tmp/work",
		"--bwrap-path", "",
		"--acpx-path", "",
		"--agent", "codex",
		"--model", "gpt-5",
		"--concurrency", "2",
		"--cancel",
	})
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%q", code, errOut.String())
	}
	got := out.String()
	for _, want := range []string{
		"planned runner poll dry-run",
		"backend mode: gh",
		"repositories: o/r, o/s",
		"missing bwrap path: configure --bwrap-path to enable sandbox preflight",
		"missing acpx path: configure --acpx-path to enable acpx dispatch",
		"[ok] github-auth",
		"[ok] repository-watch",
		"[ok] unsafe-mode",
		"[ok] temp-gh-config-dir",
		"[ok] codex",
		"[ok] claude",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("dry-run output missing %q:\n%s", want, got)
		}
	}
}

func TestRunnerPollDryRunJSONIncludesConfigAndPreflight(t *testing.T) {
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	code := app.runRunnerPoll(context.Background(), []string{
		"--repo", "o/r",
		"--once",
		"--dry-run",
		"--json",
	})
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%q", code, errOut.String())
	}
	var got struct {
		OK        bool                            `json:"ok"`
		Config    commentrunner.Config            `json:"config"`
		Preflight []commentrunner.PreflightResult `json:"preflight"`
		Actions   []string                        `json:"actions"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.OK || got.Config.BackendMode != "gh" || len(got.Config.Repositories) != 1 || got.Config.Repositories[0] != "o/r" {
		t.Fatalf("unexpected runner config: %+v", got.Config)
	}
	if len(got.Preflight) != 8 || len(got.Actions) == 0 {
		t.Fatalf("unexpected preflight/actions: %+v", got)
	}
}

func TestRunnerPollDryRunUnsafeModeAndNoWatchdogFlags(t *testing.T) {
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	code := app.runRunnerPoll(context.Background(), []string{
		"--repo", "o/r",
		"--once",
		"--dry-run",
		"--unsafe-no-sandbox",
		"--bwrap-path", "",
		"--acpx-path", "",
	})
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%q", code, errOut.String())
	}
	got := out.String()
	for _, want := range []string{
		"[ok] unsafe-mode: unsafe no-sandbox mode requested",
		"[ok] bwrap: missing bwrap path: configure --bwrap-path to enable sandbox preflight",
		"[ok] acpx: missing acpx path: configure --acpx-path to enable acpx dispatch",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("dry-run output missing %q:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"watchdog", "process guard", "hard timeout"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("dry-run output unexpectedly mentions %q:\n%s", unwanted, got)
		}
	}
}

func TestRunnerPollRequiresOnceDryRun(t *testing.T) {
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	if code := app.runRunnerPoll(context.Background(), []string{"--repo", "o/r"}); code != 2 {
		t.Fatalf("exit code = %d, stderr=%q", code, errOut.String())
	}
}
