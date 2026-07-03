package acpx

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"

	contextbundle "github.com/higress-group/issue-spec/internal/commentrunner/context"
)

func TestNewSessionCodexDispatchesWithStableRecordAndSummary(t *testing.T) {
	runner := &fakeRunner{responses: []fakeResponse{
		{stdout: `{"acpxRecordId":"rec-1","acpxSessionId":"acpx-1","agentSessionId":"codex-1","history":[{"id":"seed"}]}`},
		{stdout: `{}`},
		{stdout: "Implemented the requested workflow.\n\n```issue_spec_coordinator_summary\n" + validSummaryJSON + "\n```\n"},
		{stdout: `{"acpxRecordId":"rec-1","acpxSessionId":"acpx-2","agentSessionId":"codex-2","lastTurnId":"turn-2","history":[{"id":"seed"},{"id":"turn-2"}]}`},
	}}
	adapter := newTestAdapter(t, Config{
		CWD:                       "/workspace",
		Agent:                     AgentCodex,
		Model:                     "gpt-5.5[xhigh]",
		Mode:                      "agent-full-access",
		MaxPermissions:            PermissionApproveAll,
		NonInteractivePermissions: NonInteractiveFail,
	}, runner)

	result, err := adapter.NewSession(context.Background(), NewSessionRequest{
		PublicSessionID:      "pub-1",
		Prompt:               "please implement TASK-015",
		TurnCorrelationToken: "turn-token-1",
	})
	if err != nil {
		t.Fatalf("NewSession returned error: %v", err)
	}
	if result.Metadata.StableRecordID != "rec-1" || result.Metadata.LastTurnID != "turn-2" {
		t.Fatalf("unexpected metadata: %+v", result.Metadata)
	}
	if !result.Output.SummaryFound || result.Output.Summary.Status != "completed" {
		t.Fatalf("summary not parsed: %+v", result.Output)
	}
	if !strings.Contains(result.Output.ReplyText, "Implemented the requested workflow") {
		t.Fatalf("reply text missing assistant output: %q", result.Output.ReplyText)
	}

	if len(runner.commands) != 4 {
		t.Fatalf("recorded %d commands, want 4", len(runner.commands))
	}
	assertArgs(t, runner.commands[0].Args, []string{"--cwd", "/workspace", "--format", "json", "--json-strict", "--model", "gpt-5.5[xhigh]", "--approve-all", "--non-interactive-permissions", "fail", "codex", "sessions", "new", "--name", "pub-1"})
	assertArgs(t, runner.commands[1].Args, []string{"--cwd", "/workspace", "--format", "json", "--json-strict", "--model", "gpt-5.5[xhigh]", "--approve-all", "--non-interactive-permissions", "fail", "codex", "set-mode", "agent-full-access", "-s", "pub-1"})
	assertArgs(t, runner.commands[2].Args, []string{"--cwd", "/workspace", "--format", "quiet", "--model", "gpt-5.5[xhigh]", "--approve-all", "--non-interactive-permissions", "fail", "codex", "--file", "-", "-s", "pub-1"})
	for _, arg := range runner.commands[2].Args {
		if strings.Contains(arg, "TASK-015") {
			t.Fatalf("prompt was unexpectedly shell-expanded into argv: %#v", runner.commands[2].Args)
		}
	}
	stdin := string(runner.commands[2].Stdin)
	if !strings.Contains(stdin, "please implement TASK-015") || !strings.Contains(stdin, "turn-token-1") {
		t.Fatalf("prompt stdin missing expected content: %q", stdin)
	}
	assertArgs(t, runner.commands[3].Args, []string{"--cwd", "/workspace", "--format", "json", "--json-strict", "--model", "gpt-5.5[xhigh]", "--approve-all", "--non-interactive-permissions", "fail", "codex", "sessions", "show", "pub-1"})
}

func TestResumeValidatesStableRecordBeforeDispatch(t *testing.T) {
	runner := &fakeRunner{responses: []fakeResponse{
		{stdout: `{"acpxRecordId":"other-rec","history":[{"id":"seed"}]}`},
	}}
	adapter := newTestAdapter(t, Config{CWD: "/workspace"}, runner)

	_, err := adapter.Resume(context.Background(), ResumeRequest{
		PublicSessionID:   "pub-1",
		StableRecordID:    "rec-1",
		Prompt:            "continue",
		MinHistoryEntries: 1,
	})
	if !errors.Is(err, ErrResumeMismatch) {
		t.Fatalf("Resume error = %v, want ErrResumeMismatch", err)
	}
	if len(runner.commands) != 1 {
		t.Fatalf("resume dispatched after mismatch; commands=%d", len(runner.commands))
	}
}

func TestResumeNoWaitQueuesPromptWithoutSummaryRequirement(t *testing.T) {
	runner := &fakeRunner{responses: []fakeResponse{
		{stdout: `{"acpxRecordId":"rec-1","history":[{"id":"seed"}]}`},
		{stdout: "queued"},
		{stdout: `{"acpxRecordId":"rec-1","lastTurnId":"turn-queued","history":[{"id":"seed"},{"id":"turn-queued"}]}`},
	}}
	adapter := newTestAdapter(t, Config{CWD: "/workspace", MaxPermissions: PermissionDenyAll}, runner)

	result, err := adapter.Resume(context.Background(), ResumeRequest{
		PublicSessionID:   "pub-1",
		StableRecordID:    "rec-1",
		Prompt:            "queue this",
		NoWait:            true,
		MinHistoryEntries: 1,
	})
	if err != nil {
		t.Fatalf("Resume returned error: %v", err)
	}
	if !result.NoWait || !result.Queued {
		t.Fatalf("no-wait result not marked queued: %+v", result)
	}
	assertArgs(t, runner.commands[1].Args, []string{"--cwd", "/workspace", "--format", "quiet", "--deny-all", "codex", "--file", "-", "-s", "pub-1", "--no-wait"})
}

func TestAdapterDoesNotAddHardTurnDeadline(t *testing.T) {
	runner := &fakeRunner{responses: []fakeResponse{
		{stdout: `{"acpxRecordId":"rec-1","history":[{"id":"seed"}]}`},
		{stdout: "queued"},
		{stdout: `{"acpxRecordId":"rec-1","lastTurnId":"turn-queued","history":[{"id":"seed"},{"id":"turn-queued"}]}`},
	}}
	adapter := newTestAdapter(t, Config{CWD: "/workspace"}, runner)

	_, err := adapter.Resume(context.Background(), ResumeRequest{
		PublicSessionID:   "pub-1",
		StableRecordID:    "rec-1",
		Prompt:            "long running work stays externally visible",
		NoWait:            true,
		MinHistoryEntries: 1,
	})
	if err != nil {
		t.Fatalf("Resume returned error: %v", err)
	}
	for i, hasDeadline := range runner.contextDeadlines {
		if hasDeadline {
			t.Fatalf("acpx command %d received an adapter-added deadline: %+v", i, runner.contextDeadlines)
		}
	}
}

func TestResumeRejectsStableRecordOnlyPostDispatchRefresh(t *testing.T) {
	runner := &fakeRunner{responses: []fakeResponse{
		{stdout: `{"acpxRecordId":"rec-1","lastTurnId":"turn-1","history":[{"id":"turn-1"}]}`},
		{stdout: "done\n```issue_spec_coordinator_summary\n" + validSummaryJSON + "\n```\n"},
		{stdout: `{"acpxRecordId":"rec-1","lastTurnId":"turn-1","history":[{"id":"turn-1"}]}`},
	}}
	adapter := newTestAdapter(t, Config{CWD: "/workspace"}, runner)

	_, err := adapter.Resume(context.Background(), ResumeRequest{
		PublicSessionID:      "pub-1",
		StableRecordID:       "rec-1",
		Prompt:               "continue",
		MinHistoryEntries:    1,
		TurnCorrelationToken: "turn-token-stale",
	})
	if !errors.Is(err, ErrResumeMismatch) {
		t.Fatalf("Resume error = %v, want ErrResumeMismatch", err)
	}
}

func TestResumeAcceptsCorrelationTokenEvidenceInHistory(t *testing.T) {
	runner := &fakeRunner{responses: []fakeResponse{
		{stdout: `{"acpxRecordId":"rec-1","lastTurnId":"turn-1","history":[{"id":"turn-1"}]}`},
		{stdout: "done\n```issue_spec_coordinator_summary\n" + validSummaryJSON + "\n```\n"},
		{stdout: `{"acpxRecordId":"rec-1","lastTurnId":"turn-1","history":[{"id":"turn-1"},{"id":"turn-1b","prompt":"issue-spec-turn-correlation: turn-token-ok"}]}`},
	}}
	adapter := newTestAdapter(t, Config{CWD: "/workspace"}, runner)

	result, err := adapter.Resume(context.Background(), ResumeRequest{
		PublicSessionID:      "pub-1",
		StableRecordID:       "rec-1",
		Prompt:               "continue",
		MinHistoryEntries:    1,
		TurnCorrelationToken: "turn-token-ok",
	})
	if err != nil {
		t.Fatalf("Resume returned error: %v", err)
	}
	if result.Metadata.StableRecordID != "rec-1" {
		t.Fatalf("unexpected result metadata: %+v", result.Metadata)
	}
}

func TestClaudeCommandShapeSetsUserSettingsAndAllowedTools(t *testing.T) {
	adapter := newTestAdapter(t, Config{
		CWD:                       "/workspace",
		Agent:                     AgentClaude,
		Model:                     "claude-sonnet-4",
		MaxPermissions:            PermissionApproveReads,
		ClaudeIncludeUserSettings: true,
		ClaudeAllowedTools:        []string{"Task", "Bash", "Task"},
		ExtraEnv:                  []string{"PATH=/usr/bin"},
	}, &fakeRunner{})

	cmd := adapter.BuildPromptCommand("pub-claude", []byte("work"), false, "")
	assertArgs(t, cmd.Args, []string{"--cwd", "/workspace", "--format", "quiet", "--model", "claude-sonnet-4", "--approve-reads", "claude", "--allowed-tools", "Task,Bash", "--file", "-", "-s", "pub-claude"})
	env := envMap(cmd.Env)
	if env["ACPX_CLAUDE_INCLUDE_USER_SETTINGS"] != "1" {
		t.Fatalf("Claude user settings env missing: %v", cmd.Env)
	}
	if env["PATH"] != "/usr/bin" {
		t.Fatalf("extra env not preserved: %v", cmd.Env)
	}
}

func TestParseTurnOutputRejectsMissingMalformedAndAmbiguousSummary(t *testing.T) {
	_, err := ParseTurnOutput([]byte("plain assistant reply"), nil, contextbundle.SummaryBounds{})
	if !errors.Is(err, ErrSummaryNotFound) {
		t.Fatalf("missing summary error = %v, want ErrSummaryNotFound", err)
	}

	_, err = ParseTurnOutput([]byte("```issue_spec_coordinator_summary\n{\"status\":\"queued\"}\n```"), nil, contextbundle.SummaryBounds{})
	if err == nil || !strings.Contains(err.Error(), "summary status") {
		t.Fatalf("malformed summary error = %v", err)
	}

	ambiguous := "```issue_spec_coordinator_summary\n" + validSummaryJSON + "\n```\n```issue_spec_coordinator_summary\n" + validSummaryJSON + "\n```"
	_, err = ParseTurnOutput([]byte(ambiguous), nil, contextbundle.SummaryBounds{})
	if !errors.Is(err, ErrAmbiguousSummary) {
		t.Fatalf("ambiguous summary error = %v, want ErrAmbiguousSummary", err)
	}
}

func TestCancelProbeAndCancelUseCommandRunner(t *testing.T) {
	runner := &fakeRunner{responses: []fakeResponse{
		{stdout: "cancel help"},
		{stdout: "cancel help"},
		{stdout: `{"status":"cancelled"}`},
	}}
	adapter := newTestAdapter(t, Config{CWD: "/workspace"}, runner)

	caps, err := adapter.ProbeCapabilities(context.Background())
	if err != nil {
		t.Fatalf("ProbeCapabilities returned error: %v", err)
	}
	if !caps.CancelTurnSupported {
		t.Fatalf("cancel should be supported: %+v", caps)
	}
	cancel, err := adapter.Cancel(context.Background(), SessionRef{PublicSessionID: "pub-1"})
	if err != nil {
		t.Fatalf("Cancel returned error: %v", err)
	}
	if !cancel.Confirmed {
		t.Fatalf("cancel was not confirmed: %+v", cancel)
	}
	assertArgs(t, runner.commands[0].Args, []string{"--cwd", "/workspace", "--format", "text", "--approve-reads", "codex", "cancel", "--help"})
	assertArgs(t, runner.commands[2].Args, []string{"--cwd", "/workspace", "--format", "json", "--json-strict", "--approve-reads", "codex", "cancel", "-s", "pub-1"})
}

func TestCancelUnsupportedDoesNotPretendCancelled(t *testing.T) {
	runner := &fakeRunner{responses: []fakeResponse{
		{stdout: "unknown command", exitCode: 2},
	}}
	adapter := newTestAdapter(t, Config{CWD: "/workspace"}, runner)

	result, err := adapter.Cancel(context.Background(), SessionRef{PublicSessionID: "pub-1"})
	if !errors.Is(err, ErrUnsupportedCancel) {
		t.Fatalf("Cancel error = %v, want ErrUnsupportedCancel", err)
	}
	if !result.Unsupported || result.Confirmed {
		t.Fatalf("unsupported cancel result not explicit: %+v", result)
	}
}

func TestReconcileTurnRecoversTerminalOutputFromCorrelatedHistory(t *testing.T) {
	output := "Recovered.\n\n```issue_spec_coordinator_summary\n" + validSummaryJSON + "\n```"
	sessionJSON := `{
		"acpxRecordId":"rec-1",
		"lastTurnId":"turn-2",
		"history":[
			{"id":"turn-1","output":"previous"},
			{"id":"turn-2","prompt":"issue-spec-turn-correlation: turn-token-2","output":` + strconv.Quote(output) + `}
		]
	}`
	runner := &fakeRunner{responses: []fakeResponse{{stdout: sessionJSON}}}
	adapter := newTestAdapter(t, Config{CWD: "/workspace"}, runner)

	result, err := adapter.ReconcileTurn(context.Background(), TurnReconcileRequest{
		PublicSessionID:      "pub-1",
		StableRecordID:       "rec-1",
		TurnCorrelationToken: "turn-token-2",
		LastTurnID:           "turn-1",
	})
	if err != nil {
		t.Fatalf("ReconcileTurn returned error: %v", err)
	}
	if result.Status != ReconcileStatusCompleted || !result.Output.SummaryFound || result.Ambiguous {
		t.Fatalf("terminal turn was not recovered: %+v", result)
	}
	if result.Metadata.LastTurnID != "turn-2" {
		t.Fatalf("metadata not refreshed: %+v", result.Metadata)
	}
}

func TestReconcileTurnMarksAmbiguousWhenTerminalCannotBeProven(t *testing.T) {
	sessionJSON := `{
		"acpxRecordId":"rec-1",
		"lastTurnId":"turn-2",
		"history":[
			{"id":"turn-2","prompt":"issue-spec-turn-correlation: turn-token-2","output":"assistant output without summary"}
		]
	}`
	runner := &fakeRunner{responses: []fakeResponse{{stdout: sessionJSON}}}
	adapter := newTestAdapter(t, Config{CWD: "/workspace"}, runner)

	result, err := adapter.ReconcileTurn(context.Background(), TurnReconcileRequest{
		PublicSessionID:      "pub-1",
		StableRecordID:       "rec-1",
		TurnCorrelationToken: "turn-token-2",
		LastTurnID:           "turn-1",
	})
	if err != nil {
		t.Fatalf("ReconcileTurn returned error: %v", err)
	}
	if result.Status != ReconcileStatusInterrupted || !result.Ambiguous {
		t.Fatalf("ambiguous turn should be interrupted, got %+v", result)
	}
	if !strings.Contains(result.Diagnostics, "terminal coordinator summary was not recoverable") {
		t.Fatalf("diagnostics did not explain ambiguity: %q", result.Diagnostics)
	}
}

func newTestAdapter(t *testing.T, cfg Config, runner CommandRunner) *Adapter {
	t.Helper()
	adapter, err := NewAdapter(cfg, runner)
	if err != nil {
		t.Fatalf("NewAdapter returned error: %v", err)
	}
	return adapter
}

func assertArgs(t *testing.T, got, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
}

func envMap(entries []string) map[string]string {
	out := map[string]string{}
	for _, entry := range entries {
		name, value, ok := strings.Cut(entry, "=")
		if ok {
			out[name] = value
		}
	}
	return out
}

type fakeResponse struct {
	stdout   string
	stderr   string
	exitCode int
	err      error
}

type fakeRunner struct {
	commands         []Command
	responses        []fakeResponse
	contextDeadlines []bool
}

func (f *fakeRunner) Run(ctx context.Context, command Command) (CommandResult, error) {
	f.commands = append(f.commands, command)
	_, hasDeadline := ctx.Deadline()
	f.contextDeadlines = append(f.contextDeadlines, hasDeadline)
	if len(f.responses) == 0 {
		return CommandResult{}, nil
	}
	response := f.responses[0]
	f.responses = f.responses[1:]
	return CommandResult{
		Stdout:   []byte(response.stdout),
		Stderr:   []byte(response.stderr),
		ExitCode: response.exitCode,
	}, response.err
}

const validSummaryJSON = `{"status":"completed","artifacts":[{"kind":"typed_comment","id":"PROCESS-NC-010","url":"https://github.com/higress-group/issue-spec/issues/30#issuecomment-1","action":"updated"}],"commands":[{"name":"issue-spec comment upsert","exit_code":0,"artifact_id":"PROCESS-NC-010","stdout_summary":"updated","stderr_summary":""}],"children":[{"id":"child-1","native_id":"native-1","role":"worker","process_id":"PROCESS-NC-010","status":"done","evidence":"tests passed"}],"processes":[{"process_id":"PROCESS-NC-010","task_id":"TASK-015","status":"done","evidence":"adapter tests passed"}],"diagnostics":[]}`
