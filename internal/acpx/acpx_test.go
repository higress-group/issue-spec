package acpx

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

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

func TestNewSessionRecoversSummaryFromFlattenedAgentMessageText(t *testing.T) {
	userExample := "prompt example\nissue-spec-turn-correlation: turn-token-1\n```issue_spec_coordinator_summary\n" + validSummaryJSON + "\n```"
	agentOutput := "done\n```issue_spec_coordinator_summary{\n" + strings.TrimPrefix(validSummaryJSON, "{") + "\n```"
	sessionShow := `{
		"acpxRecordId":"rec-1",
		"acpxSessionId":"acpx-2",
		"lastTurnId":"turn-2",
		"messages.0.User.content.0.Text":` + strconv.Quote(userExample) + `,
		"messages.1.Agent.content.7.Text":` + strconv.Quote(agentOutput) + `
	}`
	runner := &fakeRunner{responses: []fakeResponse{
		{stdout: `{"acpxRecordId":"rec-1","acpxSessionId":"acpx-1","history":[{"id":"seed"}]}`},
		{stdout: `{}`},
		{stdout: "assistant output without coordinator summary"},
		{stdout: sessionShow},
	}}
	adapter := newTestAdapter(t, Config{CWD: "/workspace", Mode: "agent-full-access"}, runner)

	result, err := adapter.NewSession(context.Background(), NewSessionRequest{
		PublicSessionID:      "pub-1",
		Prompt:               "create test artifact",
		TurnCorrelationToken: "turn-token-1",
	})
	if err != nil {
		t.Fatalf("NewSession returned error: %v", err)
	}
	if !result.Output.SummaryFound || result.Output.Summary.Status != "completed" {
		t.Fatalf("flattened agent summary was not recovered: %+v", result.Output)
	}
	if result.Output.Summary.Artifacts[0].ID != "PROCESS-NC-010" {
		t.Fatalf("unexpected recovered summary: %+v", result.Output.Summary)
	}
	if !strings.Contains(result.Output.ReplyText, "done") {
		t.Fatalf("reply text missing recovered agent output: %q", result.Output.ReplyText)
	}
}

func TestNewSessionRetriesPromptQueueNotAcceptingError(t *testing.T) {
	withQueueBackoffs(t, []time.Duration{0})
	runner := &fakeRunner{responses: []fakeResponse{
		{stdout: `{"acpxRecordId":"rec-1","acpxSessionId":"acpx-1","agentSessionId":"codex-1","history":[{"id":"seed"}]}`},
		{stderr: "Session queue owner is running but not accepting queue requests", exitCode: 1},
		{stdout: "done\n```issue_spec_coordinator_summary\n" + validSummaryJSON + "\n```\n"},
		{stdout: `{"acpxRecordId":"rec-1","acpxSessionId":"acpx-2","lastTurnId":"turn-2","history":[{"id":"seed"},{"id":"turn-2"}]}`},
	}}
	adapter := newTestAdapter(t, Config{CWD: "/workspace"}, runner)

	result, err := adapter.NewSession(context.Background(), NewSessionRequest{
		PublicSessionID:      "pub-1",
		Prompt:               "create test artifact",
		TurnCorrelationToken: "turn-token-1",
	})
	if err != nil {
		t.Fatalf("NewSession returned error: %v", err)
	}
	if !result.Output.SummaryFound {
		t.Fatalf("summary not parsed after prompt retry: %+v", result.Output)
	}
	if len(runner.commands) != 4 {
		t.Fatalf("recorded %d commands, want create, prompt retry, prompt success, refresh", len(runner.commands))
	}
	assertArgs(t, runner.commands[1].Args, []string{"--cwd", "/workspace", "--format", "quiet", "--approve-reads", "codex", "--file", "-", "-s", "pub-1"})
	assertArgs(t, runner.commands[2].Args, []string{"--cwd", "/workspace", "--format", "quiet", "--approve-reads", "codex", "--file", "-", "-s", "pub-1"})
}

func TestResumeRetriesPromptQueueNotAcceptingUntilAccepted(t *testing.T) {
	withQueueBackoffs(t, []time.Duration{0, 0, 0})
	runner := &fakeRunner{responses: []fakeResponse{
		{stdout: `{"acpxRecordId":"rec-1","lastTurnId":"turn-1","messages":[{"User":{"content":[{"Text":"seed prompt"}]}}]}`},
		{stderr: "Session queue owner is running but not accepting queue requests", exitCode: 1},
		{stderr: "Session queue owner is running but not accepting queue requests", exitCode: 1},
		{stderr: "Session queue owner is running but not accepting queue requests", exitCode: 1},
		{stdout: "done\n```issue_spec_coordinator_summary\n" + validSummaryJSON + "\n```\n"},
		{stdout: `{"acpxRecordId":"rec-1","lastTurnId":"turn-2","messages":[{"User":{"content":[{"Text":"seed prompt"}]}},{"Agent":{"content":[{"Text":"done"}]}}]}`},
	}}
	adapter := newTestAdapter(t, Config{CWD: "/workspace"}, runner)

	result, err := adapter.Resume(context.Background(), ResumeRequest{
		PublicSessionID:   "pub-1",
		StableRecordID:    "rec-1",
		Prompt:            "continue",
		MinHistoryEntries: 1,
	})
	if err != nil {
		t.Fatalf("Resume returned error: %v", err)
	}
	if !result.Output.SummaryFound {
		t.Fatalf("summary not parsed after prompt retries: %+v", result.Output)
	}
	if len(runner.commands) != 6 {
		t.Fatalf("recorded %d commands, want pre-refresh, 4 prompt attempts, post-refresh", len(runner.commands))
	}
	for i := 1; i <= 4; i++ {
		assertArgs(t, runner.commands[i].Args, []string{"--cwd", "/workspace", "--format", "quiet", "--approve-reads", "codex", "--file", "-", "-s", "pub-1"})
	}
}

func TestRetryableQueueBackoffBudgetCoversQueueOwnerStartupWindow(t *testing.T) {
	var total time.Duration
	for _, backoff := range retryableQueueBackoffs {
		total += backoff
	}
	if total < 5*time.Minute {
		t.Fatalf("retryable queue backoff budget = %s, want at least 5m", total)
	}
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

func TestResumeAcceptsMessagesOnlySnapshotBeforeDispatch(t *testing.T) {
	runner := &fakeRunner{responses: []fakeResponse{
		{stdout: `{"acpxRecordId":"rec-1","lastTurnId":"turn-1","messages":[{"User":{"content":[{"Text":"seed prompt"}]}}]}`},
		{stdout: "done\n```issue_spec_coordinator_summary\n" + validSummaryJSON + "\n```\n"},
		{stdout: `{"acpxRecordId":"rec-1","lastTurnId":"turn-2","messages":[{"User":{"content":[{"Text":"seed prompt"}]}},{"Agent":{"content":[{"Text":"done"}]}}]}`},
	}}
	adapter := newTestAdapter(t, Config{CWD: "/workspace"}, runner)

	result, err := adapter.Resume(context.Background(), ResumeRequest{
		PublicSessionID:   "pub-1",
		StableRecordID:    "rec-1",
		Prompt:            "continue",
		MinHistoryEntries: 1,
	})
	if err != nil {
		t.Fatalf("Resume returned error: %v", err)
	}
	if result.Metadata.HistoryLength != 2 || result.Metadata.LastTurnID != "turn-2" {
		t.Fatalf("unexpected metadata: %+v", result.Metadata)
	}
	if len(runner.commands) != 3 {
		t.Fatalf("recorded %d commands, want pre-refresh, prompt, post-refresh", len(runner.commands))
	}
}

func TestResumeSkipsSetModeWhenSnapshotAlreadyHasDesiredMode(t *testing.T) {
	runner := &fakeRunner{responses: []fakeResponse{
		{stdout: `{"acpxRecordId":"rec-1","lastTurnId":"turn-1","acpx.desired_mode_id":"agent-full-access","messages":[{"User":{"content":[{"Text":"seed prompt"}]}}]}`},
		{stdout: "done\n```issue_spec_coordinator_summary\n" + validSummaryJSON + "\n```\n"},
		{stdout: `{"acpxRecordId":"rec-1","lastTurnId":"turn-2","acpx.desired_mode_id":"agent-full-access","messages":[{"User":{"content":[{"Text":"seed prompt"}]}},{"Agent":{"content":[{"Text":"done"}]}}]}`},
	}}
	adapter := newTestAdapter(t, Config{CWD: "/workspace", Mode: "agent-full-access"}, runner)

	result, err := adapter.Resume(context.Background(), ResumeRequest{
		PublicSessionID:   "pub-1",
		StableRecordID:    "rec-1",
		Prompt:            "continue",
		MinHistoryEntries: 1,
	})
	if err != nil {
		t.Fatalf("Resume returned error: %v", err)
	}
	if !result.Output.SummaryFound {
		t.Fatalf("summary not parsed: %+v", result.Output)
	}
	if len(runner.commands) != 3 {
		t.Fatalf("recorded %d commands, want pre-refresh, prompt, post-refresh", len(runner.commands))
	}
	for _, command := range runner.commands {
		if strings.Contains(strings.Join(command.Args, " "), "set-mode") {
			t.Fatalf("resume should not set already-applied mode: %+v", command.Args)
		}
	}
}

func TestResumeRetriesRetryableSetModeQueueError(t *testing.T) {
	withQueueBackoffs(t, []time.Duration{0})
	retryableQueue := `{"jsonrpc":"2.0","id":null,"error":{"code":-32603,"message":"Session queue owner is running but not accepting set_mode requests","data":{"detailCode":"QUEUE_NOT_ACCEPTING_REQUESTS","origin":"queue","retryable":true}}}`
	runner := &fakeRunner{responses: []fakeResponse{
		{stdout: `{"acpxRecordId":"rec-1","lastTurnId":"turn-1","messages":[{"User":{"content":[{"Text":"seed prompt"}]}}]}`},
		{stdout: retryableQueue, exitCode: 1},
		{stdout: `{}`},
		{stdout: "done\n```issue_spec_coordinator_summary\n" + validSummaryJSON + "\n```\n"},
		{stdout: `{"acpxRecordId":"rec-1","lastTurnId":"turn-2","messages":[{"User":{"content":[{"Text":"seed prompt"}]}},{"Agent":{"content":[{"Text":"done"}]}}]}`},
	}}
	adapter := newTestAdapter(t, Config{CWD: "/workspace", Mode: "agent-full-access"}, runner)

	result, err := adapter.Resume(context.Background(), ResumeRequest{
		PublicSessionID:   "pub-1",
		StableRecordID:    "rec-1",
		Prompt:            "continue",
		MinHistoryEntries: 1,
	})
	if err != nil {
		t.Fatalf("Resume returned error: %v", err)
	}
	if !result.Output.SummaryFound {
		t.Fatalf("summary not parsed after retry: %+v", result.Output)
	}
	if len(runner.commands) != 5 {
		t.Fatalf("recorded %d commands, want pre-refresh, set-mode retry, set-mode success, prompt, post-refresh", len(runner.commands))
	}
	assertArgs(t, runner.commands[1].Args, []string{"--cwd", "/workspace", "--format", "json", "--json-strict", "--approve-reads", "codex", "set-mode", "agent-full-access", "-s", "pub-1"})
	assertArgs(t, runner.commands[2].Args, []string{"--cwd", "/workspace", "--format", "json", "--json-strict", "--approve-reads", "codex", "set-mode", "agent-full-access", "-s", "pub-1"})
}

func TestResumeDoesNotRetryNonRetryableSetModeError(t *testing.T) {
	withQueueBackoffs(t, []time.Duration{0})
	nonRetryable := `{"jsonrpc":"2.0","id":null,"error":{"code":-32603,"message":"mode rejected","data":{"detailCode":"MODE_REJECTED","origin":"cli","retryable":false}}}`
	runner := &fakeRunner{responses: []fakeResponse{
		{stdout: `{"acpxRecordId":"rec-1","lastTurnId":"turn-1","messages":[{"User":{"content":[{"Text":"seed prompt"}]}}]}`},
		{stdout: nonRetryable, exitCode: 1},
	}}
	adapter := newTestAdapter(t, Config{CWD: "/workspace", Mode: "agent-full-access"}, runner)

	_, err := adapter.Resume(context.Background(), ResumeRequest{
		PublicSessionID:   "pub-1",
		StableRecordID:    "rec-1",
		Prompt:            "continue",
		MinHistoryEntries: 1,
	})
	if err == nil {
		t.Fatal("expected non-retryable set-mode error")
	}
	if len(runner.commands) != 2 {
		t.Fatalf("recorded %d commands, want pre-refresh and one set-mode only", len(runner.commands))
	}
}

func TestResumeRejectsEmptyMessagesSnapshotBeforeDispatch(t *testing.T) {
	runner := &fakeRunner{responses: []fakeResponse{
		{stdout: `{"acpxRecordId":"rec-1","messages":[]}`},
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
		t.Fatalf("resume dispatched after empty messages snapshot; commands=%d", len(runner.commands))
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

func TestResumeDoesNotRecoverStaleSummaryWhenCurrentTurnHasNoFence(t *testing.T) {
	oldOutput := "old turn done\n```issue_spec_coordinator_summary\n" + summaryJSONWithProcessID("PROCESS-OLD-001") + "\n```"
	before := `{
		"acpxRecordId":"rec-1",
		"lastTurnId":"turn-1",
		"messages":[
			{"User":{"content":[{"Text":"seed prompt"}]}},
			{"Agent":{"content":[{"Text":` + strconv.Quote(oldOutput) + `}]}}
		]
	}`
	after := `{
		"acpxRecordId":"rec-1",
		"lastTurnId":"turn-2",
		"messages":[
			{"User":{"content":[{"Text":"seed prompt"}]}},
			{"Agent":{"content":[{"Text":` + strconv.Quote(oldOutput) + `}]}},
			{"User":{"content":[{"Text":"current prompt\nissue-spec-turn-correlation: turn-token-current"}]}},
			{"Agent":{"content":[{"Text":"interrupted before emitting a coordinator summary"}]}}
		]
	}`
	runner := &fakeRunner{responses: []fakeResponse{
		{stdout: before},
		{stdout: "assistant output without coordinator summary"},
		{stdout: after},
	}}
	adapter := newTestAdapter(t, Config{CWD: "/workspace"}, runner)

	result, err := adapter.Resume(context.Background(), ResumeRequest{
		PublicSessionID:      "pub-1",
		StableRecordID:       "rec-1",
		Prompt:               "continue",
		MinHistoryEntries:    1,
		TurnCorrelationToken: "turn-token-current",
	})
	var partial *PartialDispatchError
	if !errors.As(err, &partial) || !errors.Is(err, ErrSummaryNotFound) {
		t.Fatalf("Resume error = %v, want PartialDispatchError wrapping ErrSummaryNotFound", err)
	}
	if result.Output.SummaryFound || partial.Result.Output.SummaryFound {
		t.Fatalf("stale summary should not be recovered: result=%+v partial=%+v", result.Output, partial.Result.Output)
	}
	if partial.Result.Metadata.LastTurnID != "turn-2" {
		t.Fatalf("partial metadata was not refreshed: %+v", partial.Result.Metadata)
	}
}

func TestResumeRecoversCurrentFlattenedMessageWithNumericOrdering(t *testing.T) {
	oldOutput := "old turn done\n```issue_spec_coordinator_summary\n" + summaryJSONWithProcessID("PROCESS-OLD-002") + "\n```"
	currentOutput := "current turn done\n```issue_spec_coordinator_summary\n" + summaryJSONWithProcessID("PROCESS-CURRENT-010") + "\n```"
	before := `{
		"acpxRecordId":"rec-1",
		"lastTurnId":"turn-9",
		"historyLength":9,
		"messages.1.User.content.0.Text":"seed prompt",
		"messages.2.Agent.content.0.Text":` + strconv.Quote(oldOutput) + `
	}`
	after := `{
		"acpxRecordId":"rec-1",
		"lastTurnId":"turn-10",
		"historyLength":11,
		"messages.1.User.content.0.Text":"seed prompt",
		"messages.2.Agent.content.0.Text":` + strconv.Quote(oldOutput) + `,
		"messages.9.User.content.0.Text":"current prompt\nissue-spec-turn-correlation: turn-token-10",
		"messages.10.Agent.content.0.Text":` + strconv.Quote(currentOutput) + `
	}`
	runner := &fakeRunner{responses: []fakeResponse{
		{stdout: before},
		{stdout: "assistant output without coordinator summary"},
		{stdout: after},
	}}
	adapter := newTestAdapter(t, Config{CWD: "/workspace"}, runner)

	result, err := adapter.Resume(context.Background(), ResumeRequest{
		PublicSessionID:      "pub-1",
		StableRecordID:       "rec-1",
		Prompt:               "continue",
		MinHistoryEntries:    1,
		TurnCorrelationToken: "turn-token-10",
	})
	if err != nil {
		t.Fatalf("Resume returned error: %v", err)
	}
	if !result.Output.SummaryFound {
		t.Fatalf("summary was not recovered: %+v", result.Output)
	}
	if got := result.Output.Summary.Artifacts[0].ID; got != "PROCESS-CURRENT-010" {
		t.Fatalf("recovered artifact id = %q, want current flattened message summary", got)
	}
	if !strings.Contains(result.Output.ReplyText, "current turn done") {
		t.Fatalf("recovered reply came from wrong message: %q", result.Output.ReplyText)
	}
}

func TestNewSessionReturnsPartialDispatchErrorWithMetadataOnSummaryFailure(t *testing.T) {
	runner := &fakeRunner{responses: []fakeResponse{
		{stdout: `{"acpxRecordId":"rec-1","acpxSessionId":"acpx-1","agentSessionId":"codex-1","history":[{"id":"seed"}]}`},
		{stdout: "assistant output without coordinator summary"},
		{stdout: `{"acpxRecordId":"rec-1","acpxSessionId":"acpx-2","agentSessionId":"codex-2","lastTurnId":"turn-2","history":[{"id":"seed"},{"id":"turn-2"}]}`},
	}}
	adapter := newTestAdapter(t, Config{CWD: "/workspace"}, runner)

	result, err := adapter.NewSession(context.Background(), NewSessionRequest{
		PublicSessionID:      "pub-1",
		Prompt:               "please implement TASK-015",
		TurnCorrelationToken: "turn-token-1",
	})
	var partial *PartialDispatchError
	if !errors.As(err, &partial) || !errors.Is(err, ErrSummaryNotFound) {
		t.Fatalf("NewSession error = %v, want PartialDispatchError wrapping ErrSummaryNotFound", err)
	}
	if result.Metadata.StableRecordID != "rec-1" || partial.Result.Metadata.LastTurnID != "turn-2" {
		t.Fatalf("partial metadata was not refreshed: result=%+v partial=%+v", result.Metadata, partial.Result.Metadata)
	}
	if result.Output.RawStdout == "" || result.Output.SummaryFound {
		t.Fatalf("partial output should preserve raw stdout without a parsed summary: %+v", result.Output)
	}
	if len(runner.commands) != 3 {
		t.Fatalf("recorded %d commands, want create, prompt, refresh", len(runner.commands))
	}
}

func TestNewSessionRecoversSummaryFromRefreshedAcpxMessagesWhenPromptStdoutMissing(t *testing.T) {
	agentText := "```issue_spec_coordinator_summary" + e2eSummaryJSON + "\n```"
	runner := &fakeRunner{responses: []fakeResponse{
		{stdout: `{"acpxRecordId":"rec-1","acpxSessionId":"acpx-1","agentSessionId":"codex-1","history":[{"id":"seed"}]}`},
		{stdout: "assistant output without coordinator summary"},
		{stdout: e2eSessionShowWithAgentContentText(agentText)},
	}}
	adapter := newTestAdapter(t, Config{CWD: "/workspace"}, runner)

	result, err := adapter.NewSession(context.Background(), NewSessionRequest{
		PublicSessionID:      "pub-1",
		Prompt:               "please implement TASK-015",
		TurnCorrelationToken: "turn-token-1",
	})
	if err != nil {
		t.Fatalf("NewSession returned error: %v", err)
	}
	if result.Metadata.StableRecordID != "rec-1" || result.Metadata.LastTurnID != "turn-2" {
		t.Fatalf("metadata was not refreshed: %+v", result.Metadata)
	}
	if !result.Output.SummaryFound || result.Output.Summary.Status != "completed" {
		t.Fatalf("summary was not recovered: %+v", result.Output)
	}
	if got := result.Output.Summary.Diagnostics[0].Message; got != "No native sub-agents were dispatched because the task was handled locally." {
		t.Fatalf("diagnostic message = %q", got)
	}
	if !strings.Contains(result.Output.RawStdout, "proposal #36") {
		t.Fatalf("recovered output did not come from Agent content Text: %q", result.Output.RawStdout)
	}
	if len(runner.commands) != 3 {
		t.Fatalf("recorded %d commands, want create, prompt, refresh", len(runner.commands))
	}
}

func TestParseMetadataDerivesHistoryLengthFromMessages(t *testing.T) {
	meta, err := ParseMetadata([]byte(`{"acpxRecordId":"rec-1","messages":[{"User":{}},{"Agent":{}},{"User":{}}]}`))
	if err != nil {
		t.Fatalf("ParseMetadata returned error: %v", err)
	}
	if meta.HistoryLength != 3 {
		t.Fatalf("HistoryLength = %d, want 3", meta.HistoryLength)
	}

	explicit, err := ParseMetadata([]byte(`{"acpxRecordId":"rec-1","historyLength":1,"messages":[{},{}]}`))
	if err != nil {
		t.Fatalf("ParseMetadata with explicit historyLength returned error: %v", err)
	}
	if explicit.HistoryLength != 1 {
		t.Fatalf("explicit HistoryLength = %d, want 1", explicit.HistoryLength)
	}

	history, err := ParseMetadata([]byte(`{"acpxRecordId":"rec-1","history":[{}],"messages":[{},{}]}`))
	if err != nil {
		t.Fatalf("ParseMetadata with history returned error: %v", err)
	}
	if history.HistoryLength != 1 {
		t.Fatalf("history-derived HistoryLength = %d, want 1", history.HistoryLength)
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

func TestParseTurnOutputAcceptsSummaryBodyPrefixOnFenceOpener(t *testing.T) {
	reply := "done\n```issue_spec_coordinator_summary{\n" + strings.TrimPrefix(validSummaryJSON, "{") + "\n```\n"
	output, err := ParseTurnOutput([]byte(reply), nil, contextbundle.SummaryBounds{})
	if err != nil {
		t.Fatalf("ParseTurnOutput returned error: %v", err)
	}
	if !output.SummaryFound || output.Summary.Status != "completed" {
		t.Fatalf("summary not parsed from malformed opener: %+v", output)
	}
	if output.ReplyText != "done" {
		t.Fatalf("reply text = %q, want done", output.ReplyText)
	}
}

func TestParseTurnOutputAcceptsE2EFragmentWithStringDiagnostics(t *testing.T) {
	reply := "```issue_spec_coordinator_summary" + e2eSummaryJSON + "\n```"
	output, err := ParseTurnOutput([]byte(reply), nil, contextbundle.SummaryBounds{})
	if err != nil {
		t.Fatalf("ParseTurnOutput returned error: %v", err)
	}
	if !output.SummaryFound || output.Summary.Status != "completed" {
		t.Fatalf("summary not parsed from E2E fragment: %+v", output)
	}
	if got := output.Summary.Diagnostics[0].Message; got != "No native sub-agents were dispatched because the task was handled locally." {
		t.Fatalf("diagnostic message = %q", got)
	}
	if output.Summary.Commands[0].ArtifactID != "" || output.Summary.Commands[0].ArtifactURL != "" {
		t.Fatalf("nullable command refs should decode as empty strings: %+v", output.Summary.Commands[0])
	}
}

func TestParseTurnOutputAcceptsMalformedFenceWithLevelDiagnostic(t *testing.T) {
	reply := "done```issue_spec_coordinator_summary{\n" +
		`"status":"completed",` +
		`"artifacts":[{"kind":"typed_comment","id":"PROCESS-937","url":"https://github.com/higress-group/issue-spec/issues/37#issuecomment-4878702092","action":"created"}],` +
		`"commands":[{"name":"/tmp/issue-spec","exit_code":0,"artifact_id":"PROCESS-937","artifact_url":"https://github.com/higress-group/issue-spec/issues/37#issuecomment-4878702092","stdout_summary":"created PROCESS-937","stderr_summary":""}],` +
		`"children":[],"processes":[{"process_id":"PROCESS-937","status":"done","evidence":"recorded"}],` +
		`"diagnostics":[{"level":"info","message":"sandbox read failed before execution"}]` +
		"\n}\n```"
	output, err := ParseTurnOutput([]byte(reply), nil, contextbundle.SummaryBounds{})
	if err != nil {
		t.Fatalf("ParseTurnOutput returned error: %v", err)
	}
	if !output.SummaryFound || output.Summary.Artifacts[0].ID != "PROCESS-937" {
		t.Fatalf("summary not parsed: %+v", output)
	}
	if got := output.Summary.Diagnostics[0].Severity; got != "info" {
		t.Fatalf("diagnostic severity = %q, want info", got)
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

func withQueueBackoffs(t *testing.T, backoffs []time.Duration) {
	t.Helper()
	original := retryableQueueBackoffs
	retryableQueueBackoffs = append([]time.Duration(nil), backoffs...)
	t.Cleanup(func() {
		retryableQueueBackoffs = original
	})
}

const validSummaryJSON = `{"status":"completed","artifacts":[{"kind":"typed_comment","id":"PROCESS-NC-010","url":"https://github.com/higress-group/issue-spec/issues/30#issuecomment-1","action":"updated"}],"commands":[{"name":"issue-spec comment upsert","exit_code":0,"artifact_id":"PROCESS-NC-010","stdout_summary":"updated","stderr_summary":""}],"children":[{"id":"child-1","native_id":"native-1","role":"worker","process_id":"PROCESS-NC-010","status":"done","evidence":"tests passed"}],"processes":[{"process_id":"PROCESS-NC-010","task_id":"TASK-015","status":"done","evidence":"adapter tests passed"}],"diagnostics":[]}`

func summaryJSONWithProcessID(processID string) string {
	return strings.ReplaceAll(validSummaryJSON, "PROCESS-NC-010", processID)
}

const e2eSummaryJSON = `{
  "status": "completed",
  "artifacts": [
    {"kind": "issue", "id": "36", "url": "https://github.com/higress-group/issue-spec/issues/36", "action": "created"}
  ],
  "commands": [
    {"name": "issue-spec proposal create", "exit_code": 0, "artifact_id": null, "artifact_url": null, "stdout_summary": "proposal #36", "stderr_summary": null}
  ],
  "children": [],
  "processes": [
    {"process_id": "NATIVE-023", "status": "done", "evidence": "recovered summary from refreshed acpx message history"}
  ],
  "diagnostics": ["No native sub-agents were dispatched because the task was handled locally."]
}`

func e2eSessionShowWithAgentContentText(agentText string) string {
	content := make([]string, 0, 19)
	for i := 0; i < 18; i++ {
		content = append(content, `{"Text":"working chunk `+strconv.Itoa(i)+`"}`)
	}
	content = append(content, `{"Text":`+strconv.Quote(agentText)+`}`)
	userExample := "Prompt example, not terminal output.\nissue-spec-turn-correlation: turn-token-1\n```issue_spec_coordinator_summary\n" + validSummaryJSON + "\n```"
	return `{
  "acpxRecordId": "rec-1",
  "acpxSessionId": "acpx-2",
  "agentSessionId": "codex-2",
  "lastTurnId": "turn-2",
  "messages": [
    {"User": {"content": [{"Text": ` + strconv.Quote(userExample) + `}]}},
    {"Agent": {"content": [` + strings.Join(content, ",") + `]}}
  ]
}`
}
