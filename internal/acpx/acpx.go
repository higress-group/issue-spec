package acpx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	contextbundle "github.com/higress-group/issue-spec/internal/commentrunner/context"
	"github.com/higress-group/issue-spec/internal/sandbox"
)

const (
	DefaultBinary = "acpx"

	AgentCodex  = "codex"
	AgentClaude = "claude"

	PermissionApproveAll   = "approve-all"
	PermissionApproveReads = "approve-reads"
	PermissionDenyAll      = "deny-all"

	NonInteractiveDeny = "deny"
	NonInteractiveFail = "fail"

	CoordinatorSummaryFence = "issue_spec_coordinator_summary"
)

var retryableQueueBackoffs = []time.Duration{
	250 * time.Millisecond,
	500 * time.Millisecond,
	time.Second,
	2 * time.Second,
	4 * time.Second,
	8 * time.Second,
	15 * time.Second,
	30 * time.Second,
	30 * time.Second,
	30 * time.Second,
	30 * time.Second,
	30 * time.Second,
	30 * time.Second,
	30 * time.Second,
	30 * time.Second,
	30 * time.Second,
}

var (
	ErrInvalidConfig     = errors.New("invalid acpx config")
	ErrCommandFailed     = errors.New("acpx command failed")
	ErrResumeMismatch    = errors.New("acpx resume validation failed")
	ErrSummaryNotFound   = errors.New("coordinator summary not found")
	ErrAmbiguousSummary  = errors.New("multiple coordinator summaries found")
	ErrUnsupportedCancel = errors.New("acpx turn cancellation unsupported")
)

type Command = sandbox.Command
type CommandResult = sandbox.Result

type CommandRunner interface {
	Run(context.Context, Command) (CommandResult, error)
}

type Config struct {
	Binary                    string
	Agent                     string
	Model                     string
	Mode                      string
	MaxPermissions            string
	NonInteractivePermissions string
	CWD                       string
	NoWait                    bool
	ClaudeIncludeUserSettings bool
	ClaudeAllowedTools        []string
	HostEnv                   []string
	ExtraEnv                  []string
	SummaryBounds             contextbundle.SummaryBounds
}

type Adapter struct {
	cfg    Config
	runner CommandRunner
}

type NewSessionRequest struct {
	PublicSessionID      string
	SessionName          string
	Prompt               string
	UseEnsure            bool
	NoWait               bool
	TurnCorrelationToken string
}

type ResumeRequest struct {
	PublicSessionID      string
	SessionName          string
	StableRecordID       string
	Prompt               string
	NoWait               bool
	MinHistoryEntries    int
	TurnCorrelationToken string
}

type SessionRef struct {
	PublicSessionID string
	SessionName     string
	StableRecordID  string
}

type DispatchResult struct {
	PublicSessionID string
	SessionName     string
	NewSession      bool
	EnsuredSession  bool
	NoWait          bool
	Queued          bool
	Metadata        Metadata
	Output          TurnOutput
}

type Metadata struct {
	StableRecordID    string
	TrueSessionID     string
	ProviderSessionID string
	LastTurnID        string
	Agent             string
	SessionName       string
	CWD               string
	HistoryLength     int
	RefreshedAt       time.Time
	Raw               map[string]string
}

type TurnOutput struct {
	ReplyText    string
	SummaryJSON  string
	Summary      contextbundle.CoordinatorSummary
	Diagnostics  string
	RawStdout    string
	RawStderr    string
	SummaryFound bool
}

const (
	ReconcileStatusRunning     = "running"
	ReconcileStatusCompleted   = "completed"
	ReconcileStatusFailed      = "failed"
	ReconcileStatusCancelled   = "cancelled"
	ReconcileStatusInterrupted = "interrupted"
)

type TurnReconcileRequest struct {
	PublicSessionID      string
	SessionName          string
	StableRecordID       string
	TurnCorrelationToken string
	LastTurnID           string
}

type TurnReconcileResult struct {
	Status      string
	Metadata    Metadata
	Output      TurnOutput
	Ambiguous   bool
	Diagnostics string
}

type Capabilities struct {
	CancelTurnSupported bool
	Diagnostics         string
}

type CancelResult struct {
	Confirmed   bool
	Unsupported bool
	Diagnostics string
}

type CommandError struct {
	Name     string
	Command  Command
	Result   CommandResult
	RunError error
}

func (e *CommandError) Error() string {
	var detail strings.Builder
	if e.Result.ExitCode != 0 {
		fmt.Fprintf(&detail, " exit=%d", e.Result.ExitCode)
	}
	if len(e.Result.Stderr) > 0 {
		fmt.Fprintf(&detail, " stderr=%q", truncateForError(string(e.Result.Stderr)))
	}
	if len(e.Result.Stdout) > 0 {
		fmt.Fprintf(&detail, " stdout=%q", truncateForError(string(e.Result.Stdout)))
	}
	if e.RunError != nil {
		fmt.Fprintf(&detail, " error=%v", e.RunError)
	}
	return fmt.Sprintf("%s: %s%s", ErrCommandFailed, e.Name, detail.String())
}

func (e *CommandError) Unwrap() error {
	return ErrCommandFailed
}

type OutputSummaryError struct {
	Err error
}

func (e *OutputSummaryError) Error() string {
	if e == nil || e.Err == nil {
		return "coordinator summary parse failed"
	}
	return e.Err.Error()
}

func (e *OutputSummaryError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

type PartialDispatchError struct {
	Result DispatchResult
	Err    error
}

func (e *PartialDispatchError) Error() string {
	if e == nil || e.Err == nil {
		return "partial acpx dispatch failed"
	}
	return e.Err.Error()
}

func (e *PartialDispatchError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func NewAdapter(cfg Config, runner CommandRunner) (*Adapter, error) {
	cfg = normalizeConfig(cfg)
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}
	if runner == nil {
		runner = sandbox.ExecRunner{}
	}
	return &Adapter{cfg: cfg, runner: runner}, nil
}

func (a *Adapter) NewSession(ctx context.Context, req NewSessionRequest) (DispatchResult, error) {
	sessionName := sessionName(req.PublicSessionID, req.SessionName)
	if strings.TrimSpace(req.PublicSessionID) == "" {
		return DispatchResult{}, fmt.Errorf("%w: public session id is required", ErrInvalidConfig)
	}
	if strings.TrimSpace(req.Prompt) == "" {
		return DispatchResult{}, fmt.Errorf("%w: prompt is required", ErrInvalidConfig)
	}

	create := a.BuildNewSessionCommand(sessionName, req.UseEnsure)
	createResult, err := a.run(ctx, "sessions new", create)
	if err != nil {
		return DispatchResult{}, err
	}
	meta, err := ParseMetadata(createResult.Stdout)
	if err != nil {
		return DispatchResult{}, fmt.Errorf("parse new session metadata: %w", err)
	}
	meta.SessionName = sessionName
	if err := validateStableMetadata(meta); err != nil {
		return DispatchResult{}, err
	}
	if err := a.applyMode(ctx, sessionName); err != nil {
		return DispatchResult{}, err
	}

	dispatch, dispatchErr := a.dispatchPrompt(ctx, sessionName, req.Prompt, req.NoWait, req.TurnCorrelationToken)
	if dispatchErr != nil {
		var summaryErr *OutputSummaryError
		if !errors.As(dispatchErr, &summaryErr) {
			return DispatchResult{}, dispatchErr
		}
	}
	snapshot, refreshErr := a.refreshSnapshot(ctx, SessionRef{
		PublicSessionID: req.PublicSessionID,
		SessionName:     sessionName,
		StableRecordID:  meta.StableRecordID,
	})
	if refreshErr != nil {
		return DispatchResult{}, refreshErr
	}
	if snapshot.Metadata.StableRecordID != meta.StableRecordID {
		return DispatchResult{}, fmt.Errorf("%w: refreshed record %q does not match new record %q", ErrResumeMismatch, snapshot.Metadata.StableRecordID, meta.StableRecordID)
	}
	result := DispatchResult{
		PublicSessionID: req.PublicSessionID,
		SessionName:     sessionName,
		NewSession:      !req.UseEnsure,
		EnsuredSession:  req.UseEnsure,
		NoWait:          dispatch.noWait,
		Queued:          dispatch.noWait,
		Metadata:        snapshot.Metadata,
		Output:          dispatch.output,
	}
	if dispatchErr != nil {
		if output, ok := recoverPromptOutputFromSnapshot(snapshot, dispatch.output, req.TurnCorrelationToken, meta.LastTurnID, a.cfg.SummaryBounds); ok {
			result.Output = output
			return result, nil
		}
		return result, &PartialDispatchError{Result: result, Err: dispatchErr}
	}
	return result, nil
}

func (a *Adapter) Resume(ctx context.Context, req ResumeRequest) (DispatchResult, error) {
	sessionName := sessionName(req.PublicSessionID, req.SessionName)
	if strings.TrimSpace(req.PublicSessionID) == "" {
		return DispatchResult{}, fmt.Errorf("%w: public session id is required", ErrInvalidConfig)
	}
	if strings.TrimSpace(req.StableRecordID) == "" {
		return DispatchResult{}, fmt.Errorf("%w: stable acpx record id is required", ErrInvalidConfig)
	}
	if strings.TrimSpace(req.Prompt) == "" {
		return DispatchResult{}, fmt.Errorf("%w: prompt is required", ErrInvalidConfig)
	}

	before, err := a.Refresh(ctx, SessionRef{
		PublicSessionID: req.PublicSessionID,
		SessionName:     sessionName,
		StableRecordID:  req.StableRecordID,
	})
	if err != nil {
		return DispatchResult{}, err
	}
	if before.HistoryLength < req.MinHistoryEntries {
		return DispatchResult{}, fmt.Errorf("%w: history length %d is below required %d", ErrResumeMismatch, before.HistoryLength, req.MinHistoryEntries)
	}
	if !metadataHasMode(before, a.cfg.Mode) {
		if err := a.applyMode(ctx, sessionName); err != nil {
			return DispatchResult{}, err
		}
	}
	dispatch, dispatchErr := a.dispatchPrompt(ctx, sessionName, req.Prompt, req.NoWait, req.TurnCorrelationToken)
	if dispatchErr != nil {
		var summaryErr *OutputSummaryError
		if !errors.As(dispatchErr, &summaryErr) {
			return DispatchResult{}, dispatchErr
		}
	}
	afterSnapshot, refreshErr := a.refreshSnapshot(ctx, SessionRef{
		PublicSessionID: req.PublicSessionID,
		SessionName:     sessionName,
		StableRecordID:  req.StableRecordID,
	})
	if refreshErr != nil {
		return DispatchResult{}, refreshErr
	}
	if err := validateResumeContinuity(before, afterSnapshot.Metadata, req.TurnCorrelationToken); err != nil {
		return DispatchResult{}, err
	}
	result := DispatchResult{
		PublicSessionID: req.PublicSessionID,
		SessionName:     sessionName,
		NoWait:          dispatch.noWait,
		Queued:          dispatch.noWait,
		Metadata:        afterSnapshot.Metadata,
		Output:          dispatch.output,
	}
	if dispatchErr != nil {
		if output, ok := recoverPromptOutputFromSnapshot(afterSnapshot, dispatch.output, req.TurnCorrelationToken, before.LastTurnID, a.cfg.SummaryBounds); ok {
			result.Output = output
			return result, nil
		}
		return result, &PartialDispatchError{Result: result, Err: dispatchErr}
	}
	return result, nil
}

func (a *Adapter) Refresh(ctx context.Context, ref SessionRef) (Metadata, error) {
	snapshot, err := a.refreshSnapshot(ctx, ref)
	if err != nil {
		return Metadata{}, err
	}
	return snapshot.Metadata, nil
}

func (a *Adapter) refreshSnapshot(ctx context.Context, ref SessionRef) (sessionSnapshot, error) {
	cmd := a.BuildRefreshCommand(sessionName(ref.PublicSessionID, ref.SessionName))
	result, err := a.run(ctx, "sessions show", cmd)
	if err != nil {
		return sessionSnapshot{}, err
	}
	snapshot, err := parseSessionSnapshot(result.Stdout)
	if err != nil {
		return sessionSnapshot{}, fmt.Errorf("parse session metadata: %w", err)
	}
	snapshot.Metadata.SessionName = sessionName(ref.PublicSessionID, ref.SessionName)
	if err := validateStableMetadata(snapshot.Metadata); err != nil {
		return sessionSnapshot{}, err
	}
	if ref.StableRecordID != "" && snapshot.Metadata.StableRecordID != ref.StableRecordID {
		return sessionSnapshot{}, fmt.Errorf("%w: record %q does not match expected %q", ErrResumeMismatch, snapshot.Metadata.StableRecordID, ref.StableRecordID)
	}
	return snapshot, nil
}

func (a *Adapter) ProbeCapabilities(ctx context.Context) (Capabilities, error) {
	result, err := a.runner.Run(ctx, a.BuildCancelProbeCommand())
	if err != nil || result.ExitCode != 0 {
		return Capabilities{
			CancelTurnSupported: false,
			Diagnostics:         commandDiagnostics(result, err),
		}, nil
	}
	return Capabilities{CancelTurnSupported: true, Diagnostics: strings.TrimSpace(string(result.Stdout))}, nil
}

func (a *Adapter) Cancel(ctx context.Context, ref SessionRef) (CancelResult, error) {
	caps, err := a.ProbeCapabilities(ctx)
	if err != nil {
		return CancelResult{}, err
	}
	if !caps.CancelTurnSupported {
		return CancelResult{Unsupported: true, Diagnostics: caps.Diagnostics}, ErrUnsupportedCancel
	}
	result, err := a.run(ctx, "cancel", a.BuildCancelCommand(sessionName(ref.PublicSessionID, ref.SessionName)))
	if err != nil {
		return CancelResult{}, err
	}
	return CancelResult{Confirmed: true, Diagnostics: commandDiagnostics(result, nil)}, nil
}

func (a *Adapter) ReconcileTurn(ctx context.Context, req TurnReconcileRequest) (TurnReconcileResult, error) {
	sessionName := sessionName(req.PublicSessionID, req.SessionName)
	if strings.TrimSpace(req.PublicSessionID) == "" {
		return TurnReconcileResult{}, fmt.Errorf("%w: public session id is required", ErrInvalidConfig)
	}
	if strings.TrimSpace(req.StableRecordID) == "" {
		return TurnReconcileResult{}, fmt.Errorf("%w: stable acpx record id is required", ErrInvalidConfig)
	}
	snapshot, err := a.refreshSnapshot(ctx, SessionRef{
		PublicSessionID: req.PublicSessionID,
		SessionName:     sessionName,
		StableRecordID:  req.StableRecordID,
	})
	if err != nil {
		return TurnReconcileResult{}, err
	}
	return reconcileSnapshot(snapshot, req, a.cfg.SummaryBounds), nil
}

func (a *Adapter) BuildNewSessionCommand(sessionName string, ensure bool) Command {
	action := "new"
	if ensure {
		action = "ensure"
	}
	args := a.globalArgs("json")
	args = append(args, a.cfg.Agent, "sessions", action)
	if strings.TrimSpace(sessionName) != "" {
		args = append(args, "--name", strings.TrimSpace(sessionName))
	}
	return Command{Binary: a.cfg.Binary, Args: args, Dir: a.cfg.CWD, Env: a.commandEnv()}
}

func (a *Adapter) BuildPromptCommand(sessionName string, prompt []byte, noWait bool, turnCorrelationToken string) Command {
	args := a.globalArgs("quiet")
	args = append(args, a.cfg.Agent)
	if a.cfg.Agent == AgentClaude && len(a.cfg.ClaudeAllowedTools) > 0 {
		args = append(args, "--allowed-tools", strings.Join(a.cfg.ClaudeAllowedTools, ","))
	}
	args = append(args, "--file", "-")
	if strings.TrimSpace(sessionName) != "" {
		args = append(args, "-s", strings.TrimSpace(sessionName))
	}
	if noWait || a.cfg.NoWait {
		args = append(args, "--no-wait")
	}
	stdin := append([]byte(nil), prompt...)
	if strings.TrimSpace(turnCorrelationToken) != "" {
		stdin = append(stdin, []byte("\n\n<!-- issue-spec-turn-correlation: "+strings.TrimSpace(turnCorrelationToken)+" -->\n")...)
	}
	return Command{Binary: a.cfg.Binary, Args: args, Dir: a.cfg.CWD, Env: a.commandEnv(), Stdin: stdin}
}

func (a *Adapter) BuildRefreshCommand(sessionName string) Command {
	args := a.globalArgs("json")
	args = append(args, a.cfg.Agent, "sessions", "show")
	if strings.TrimSpace(sessionName) != "" {
		args = append(args, strings.TrimSpace(sessionName))
	}
	return Command{Binary: a.cfg.Binary, Args: args, Dir: a.cfg.CWD, Env: a.commandEnv()}
}

func (a *Adapter) BuildSetModeCommand(sessionName string) Command {
	args := a.globalArgs("json")
	args = append(args, a.cfg.Agent, "set-mode", a.cfg.Mode)
	if strings.TrimSpace(sessionName) != "" {
		args = append(args, "-s", strings.TrimSpace(sessionName))
	}
	return Command{Binary: a.cfg.Binary, Args: args, Dir: a.cfg.CWD, Env: a.commandEnv()}
}

func (a *Adapter) BuildCancelCommand(sessionName string) Command {
	args := a.globalArgs("json")
	args = append(args, a.cfg.Agent, "cancel")
	if strings.TrimSpace(sessionName) != "" {
		args = append(args, "-s", strings.TrimSpace(sessionName))
	}
	return Command{Binary: a.cfg.Binary, Args: args, Dir: a.cfg.CWD, Env: a.commandEnv()}
}

func (a *Adapter) BuildCancelProbeCommand() Command {
	args := a.globalArgs("text")
	args = append(args, a.cfg.Agent, "cancel", "--help")
	return Command{Binary: a.cfg.Binary, Args: args, Dir: a.cfg.CWD, Env: a.commandEnv()}
}

func (a *Adapter) EnvironmentOverrides() map[string]string {
	env := map[string]string{}
	for _, entry := range a.cfg.ExtraEnv {
		name, value, ok := strings.Cut(entry, "=")
		if ok && strings.TrimSpace(name) != "" {
			env[name] = value
		}
	}
	if a.cfg.Agent == AgentClaude && a.cfg.ClaudeIncludeUserSettings {
		env["ACPX_CLAUDE_INCLUDE_USER_SETTINGS"] = "1"
	}
	if len(env) == 0 {
		return nil
	}
	return env
}

func (a *Adapter) globalArgs(format string) []string {
	var args []string
	if a.cfg.CWD != "" {
		args = append(args, "--cwd", a.cfg.CWD)
	}
	switch format {
	case "json":
		args = append(args, "--format", "json", "--json-strict")
	case "quiet":
		args = append(args, "--format", "quiet")
	case "text":
		args = append(args, "--format", "text")
	}
	if a.cfg.Model != "" {
		args = append(args, "--model", a.cfg.Model)
	}
	switch a.cfg.MaxPermissions {
	case PermissionApproveAll:
		args = append(args, "--approve-all")
	case PermissionApproveReads:
		args = append(args, "--approve-reads")
	case PermissionDenyAll:
		args = append(args, "--deny-all")
	}
	if a.cfg.NonInteractivePermissions != "" {
		args = append(args, "--non-interactive-permissions", a.cfg.NonInteractivePermissions)
	}
	return args
}

func (a *Adapter) commandEnv() []string {
	overrides := a.EnvironmentOverrides()
	if len(overrides) == 0 {
		return nil
	}
	env := append([]string(nil), a.cfg.HostEnv...)
	if a.cfg.HostEnv == nil {
		env = os.Environ()
	}
	for name, value := range overrides {
		env = appendOrReplaceEnv(env, name, value)
	}
	return env
}

func (a *Adapter) applyMode(ctx context.Context, sessionName string) error {
	if strings.TrimSpace(a.cfg.Mode) == "" {
		return nil
	}
	_, err := a.runWithRetryableQueue(ctx, "set-mode", a.BuildSetModeCommand(sessionName))
	return err
}

type promptDispatch struct {
	output TurnOutput
	noWait bool
}

func (a *Adapter) dispatchPrompt(ctx context.Context, sessionName, prompt string, noWait bool, token string) (promptDispatch, error) {
	effectiveNoWait := noWait || a.cfg.NoWait
	cmd := a.BuildPromptCommand(sessionName, []byte(prompt), effectiveNoWait, token)
	result, err := a.runWithRetryableQueue(ctx, "prompt", cmd)
	if err != nil {
		return promptDispatch{}, err
	}
	if effectiveNoWait {
		return promptDispatch{noWait: true, output: TurnOutput{
			Diagnostics: commandDiagnostics(result, nil),
			RawStdout:   string(result.Stdout),
			RawStderr:   string(result.Stderr),
		}}, nil
	}
	output, err := ParseTurnOutput(result.Stdout, result.Stderr, a.cfg.SummaryBounds)
	if err != nil {
		return promptDispatch{output: TurnOutput{
			Diagnostics: commandDiagnostics(result, nil),
			RawStdout:   string(result.Stdout),
			RawStderr:   string(result.Stderr),
		}}, &OutputSummaryError{Err: err}
	}
	return promptDispatch{output: output}, nil
}

func (a *Adapter) run(ctx context.Context, name string, command Command) (CommandResult, error) {
	result, err := a.runner.Run(ctx, command)
	if err != nil || result.ExitCode != 0 {
		return result, &CommandError{Name: name, Command: command, Result: result, RunError: err}
	}
	return result, nil
}

func (a *Adapter) runWithRetryableQueue(ctx context.Context, name string, command Command) (CommandResult, error) {
	var lastResult CommandResult
	var lastErr error
	for attempt := 0; ; attempt++ {
		result, err := a.run(ctx, name, command)
		if err == nil {
			return result, nil
		}
		lastResult = result
		lastErr = err
		if !isRetryableAcpxQueueError(err) || attempt >= len(retryableQueueBackoffs) {
			return lastResult, lastErr
		}
		if err := sleepContext(ctx, retryableQueueBackoffs[attempt]); err != nil {
			return lastResult, fmt.Errorf("wait before retrying %s: %w", name, err)
		}
	}
}

func sleepContext(ctx context.Context, duration time.Duration) error {
	if duration <= 0 {
		return nil
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func isRetryableAcpxQueueError(err error) bool {
	var commandErr *CommandError
	if !errors.As(err, &commandErr) {
		return false
	}
	if isRetryableAcpxQueueJSON(commandErr.Result.Stdout) {
		return true
	}
	text := strings.ToLower(string(commandErr.Result.Stdout) + "\n" + string(commandErr.Result.Stderr))
	return strings.Contains(text, "queue owner is running but not accepting")
}

func isRetryableAcpxQueueJSON(data []byte) bool {
	var envelope struct {
		Error struct {
			Data struct {
				Retryable  bool   `json:"retryable"`
				DetailCode string `json:"detailCode"`
				Origin     string `json:"origin"`
			} `json:"data"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &envelope) != nil {
		return false
	}
	if !envelope.Error.Data.Retryable {
		return false
	}
	return strings.EqualFold(envelope.Error.Data.DetailCode, "QUEUE_NOT_ACCEPTING_REQUESTS") || strings.EqualFold(envelope.Error.Data.Origin, "queue")
}

func ParseMetadata(data []byte) (Metadata, error) {
	snapshot, err := parseSessionSnapshot(data)
	if err != nil {
		return Metadata{}, err
	}
	return snapshot.Metadata, nil
}

type sessionSnapshot struct {
	Metadata Metadata
	History  []historyEntry
	Values   map[string]any
	Text     []string
}

type historyEntry struct {
	ID   string
	Text []string
	Raw  map[string]any
}

func parseSessionSnapshot(data []byte) (sessionSnapshot, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return sessionSnapshot{}, fmt.Errorf("empty acpx metadata")
	}
	now := time.Now().UTC()
	var values map[string]any
	if err := json.Unmarshal(trimmed, &values); err != nil {
		line := firstNonEmptyLine(string(trimmed))
		if line == "" {
			return sessionSnapshot{}, err
		}
		return sessionSnapshot{
			Metadata: Metadata{StableRecordID: line, RefreshedAt: now, Raw: map[string]string{"text": line}},
			Text:     []string{line},
		}, nil
	}
	meta := metadataFromValues(values, now)
	return sessionSnapshot{
		Metadata: meta,
		History:  historyEntries(values),
		Values:   values,
		Text:     collectStrings(values),
	}, nil
}

func metadataFromValues(values map[string]any, refreshedAt time.Time) Metadata {
	raw := map[string]string{}
	flattenScalars("", values, raw)
	return Metadata{
		StableRecordID:    firstString(values, "acpxRecordId", "acpx_record_id", "recordId", "record_id", "id", "stableRecordId", "stable_record_id"),
		TrueSessionID:     firstString(values, "acpxSessionId", "acpx_session_id", "sessionId", "session_id", "trueSessionId", "true_session_id"),
		ProviderSessionID: firstString(values, "agentSessionId", "agent_session_id", "providerSessionId", "provider_session_id"),
		LastTurnID:        firstString(values, "lastTurnId", "last_turn_id", "turnId", "turn_id", "lastPromptId", "last_prompt_id"),
		Agent:             firstString(values, "agent", "agentCommand", "agent_command"),
		SessionName:       firstString(values, "name", "sessionName", "session_name"),
		CWD:               firstString(values, "cwd", "workingDirectory", "working_directory"),
		HistoryLength:     historyLength(values),
		RefreshedAt:       refreshedAt,
		Raw:               raw,
	}
}

func ParseTurnOutput(stdout, stderr []byte, bounds contextbundle.SummaryBounds) (TurnOutput, error) {
	rawStdout := string(stdout)
	rawStderr := string(stderr)
	blocks, err := contextbundle.FindCoordinatorSummaryBlocks(rawStdout)
	if err != nil {
		return TurnOutput{}, err
	}
	if len(blocks) == 0 {
		trimmed := strings.TrimSpace(rawStdout)
		if strings.HasPrefix(trimmed, "{") {
			summary, err := contextbundle.ParseCoordinatorSummary([]byte(trimmed), bounds)
			if err != nil {
				return TurnOutput{}, err
			}
			return TurnOutput{SummaryJSON: trimmed, Summary: summary, RawStdout: rawStdout, RawStderr: rawStderr, SummaryFound: true}, nil
		}
		return TurnOutput{}, ErrSummaryNotFound
	}
	if len(blocks) > 1 {
		return TurnOutput{}, ErrAmbiguousSummary
	}
	block := blocks[0]
	summaryJSON := strings.TrimSpace(block.Body)
	summary, err := contextbundle.ParseCoordinatorSummary([]byte(summaryJSON), bounds)
	if err != nil {
		return TurnOutput{}, err
	}
	reply := strings.TrimSpace(rawStdout[:block.Start] + rawStdout[block.End:])
	return TurnOutput{
		ReplyText:    reply,
		SummaryJSON:  summaryJSON,
		Summary:      summary,
		Diagnostics:  strings.TrimSpace(rawStderr),
		RawStdout:    rawStdout,
		RawStderr:    rawStderr,
		SummaryFound: true,
	}, nil
}

func validateResumeContinuity(before, after Metadata, token string) error {
	var evidence []string
	if after.HistoryLength > before.HistoryLength {
		evidence = append(evidence, fmt.Sprintf("history length advanced from %d to %d", before.HistoryLength, after.HistoryLength))
	}
	if strings.TrimSpace(after.LastTurnID) != "" && after.LastTurnID != before.LastTurnID {
		evidence = append(evidence, fmt.Sprintf("last turn advanced from %q to %q", before.LastTurnID, after.LastTurnID))
	}
	if strings.TrimSpace(token) != "" && metadataContains(after, token) {
		evidence = append(evidence, "turn correlation token was found in refreshed metadata")
	}
	if len(evidence) > 0 {
		return nil
	}
	return fmt.Errorf("%w: resume dispatch did not produce history, turn id, or correlation evidence in refreshed metadata", ErrResumeMismatch)
}

func metadataContains(meta Metadata, needle string) bool {
	needle = strings.TrimSpace(needle)
	if needle == "" {
		return false
	}
	for key, value := range meta.Raw {
		if strings.Contains(key, needle) || strings.Contains(value, needle) {
			return true
		}
	}
	return false
}

func metadataHasMode(meta Metadata, mode string) bool {
	mode = strings.TrimSpace(mode)
	if mode == "" {
		return true
	}
	for _, key := range []string{"acpx.desired_mode_id", "acpx.current_mode_id", "desiredMode", "desired_mode", "mode"} {
		if strings.TrimSpace(meta.Raw[key]) == mode {
			return true
		}
	}
	return false
}

func reconcileSnapshot(snapshot sessionSnapshot, req TurnReconcileRequest, bounds contextbundle.SummaryBounds) TurnReconcileResult {
	selection := selectTurnOutputCandidates(snapshot, req.TurnCorrelationToken, req.LastTurnID)
	output, found, parseErr := firstTerminalOutput(selection.Candidates, bounds)
	if found {
		return TurnReconcileResult{
			Status:      statusFromTurnOutput(output),
			Metadata:    snapshot.Metadata,
			Output:      output,
			Diagnostics: reconciliationEvidence(selection.TokenEvidence, selection.TurnEvidence, "terminal coordinator summary recovered"),
		}
	}

	diagnostic := reconciliationEvidence(selection.TokenEvidence, selection.TurnEvidence, "terminal coordinator summary was not recoverable")
	if parseErr != nil {
		diagnostic += ": " + parseErr.Error()
	}
	if !selection.TokenEvidence && !selection.TurnEvidence {
		diagnostic = "turn correlation token was not found in acpx history and last turn id did not advance"
	}
	return TurnReconcileResult{
		Status:      ReconcileStatusInterrupted,
		Metadata:    snapshot.Metadata,
		Ambiguous:   true,
		Diagnostics: diagnostic,
	}
}

func statusFromTurnOutput(output TurnOutput) string {
	if output.Summary.Status == "completed" {
		return ReconcileStatusCompleted
	}
	return ReconcileStatusFailed
}

func reconciliationEvidence(tokenEvidence, turnEvidence bool, suffix string) string {
	var parts []string
	if tokenEvidence {
		parts = append(parts, "turn correlation token found")
	}
	if turnEvidence {
		parts = append(parts, "last turn id advanced")
	}
	parts = append(parts, suffix)
	return strings.Join(parts, "; ")
}

func firstTerminalOutput(candidates []string, bounds contextbundle.SummaryBounds) (TurnOutput, bool, error) {
	var parseErr error
	for _, candidate := range uniqueStrings(candidates) {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		output, err := ParseTurnOutput([]byte(candidate), nil, bounds)
		if err == nil && output.SummaryFound {
			return output, true, nil
		}
		if err != nil && !errors.Is(err, ErrSummaryNotFound) {
			parseErr = err
		}
	}
	return TurnOutput{}, false, parseErr
}

type turnOutputCandidateSelection struct {
	Candidates    []string
	TokenEvidence bool
	TurnEvidence  bool
}

func selectTurnOutputCandidates(snapshot sessionSnapshot, token, lastTurn string) turnOutputCandidateSelection {
	token = strings.TrimSpace(token)
	lastTurn = strings.TrimSpace(lastTurn)
	selection := turnOutputCandidateSelection{
		TokenEvidence: token != "" && snapshotContains(snapshot, token),
		TurnEvidence:  lastTurn != "" && strings.TrimSpace(snapshot.Metadata.LastTurnID) != "" && snapshot.Metadata.LastTurnID != lastTurn,
	}
	if !selection.TokenEvidence && !selection.TurnEvidence {
		return selection
	}

	for i := len(snapshot.History) - 1; i >= 0; i-- {
		entry := snapshot.History[i]
		switch {
		case token != "" && stringsContain(entry.Text, token):
			selection.Candidates = append(selection.Candidates, outputCandidates(entry.Raw, entry.Text)...)
		case !selection.TokenEvidence && selection.TurnEvidence && entry.ID != "" && entry.ID == snapshot.Metadata.LastTurnID:
			selection.Candidates = append(selection.Candidates, outputCandidates(entry.Raw, entry.Text)...)
		}
	}
	if selection.TokenEvidence {
		selection.Candidates = append(selection.Candidates, tokenAnchoredAgentMessageTextCandidates(snapshot.Values, token)...)
	}
	selection.Candidates = append(selection.Candidates, directOutputCandidates(snapshot.Values)...)
	return selection
}

func recoverPromptOutputFromSnapshot(snapshot sessionSnapshot, original TurnOutput, token, lastTurn string, bounds contextbundle.SummaryBounds) (TurnOutput, bool) {
	selection := selectTurnOutputCandidates(snapshot, token, lastTurn)
	if !selection.TokenEvidence && !selection.TurnEvidence {
		return TurnOutput{}, false
	}
	for _, candidate := range uniqueStrings(selection.Candidates) {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		output, err := ParseTurnOutput([]byte(candidate), []byte(original.RawStderr), bounds)
		if err == nil && output.SummaryFound {
			return output, true
		}
		if errors.Is(err, ErrAmbiguousSummary) {
			output, err = parseLatestCoordinatorSummaryFromText(candidate, original.RawStderr, bounds)
			if err == nil && output.SummaryFound {
				return output, true
			}
		}
	}
	return TurnOutput{}, false
}

func parseLatestCoordinatorSummaryFromText(text, stderr string, bounds contextbundle.SummaryBounds) (TurnOutput, error) {
	blocks, err := contextbundle.FindCoordinatorSummaryBlocks(text)
	if err != nil {
		return TurnOutput{}, err
	}
	if len(blocks) == 0 {
		return TurnOutput{}, ErrSummaryNotFound
	}
	var parseErr error
	for i := len(blocks) - 1; i >= 0; i-- {
		block := blocks[i]
		summaryJSON := strings.TrimSpace(block.Body)
		summary, err := contextbundle.ParseCoordinatorSummary([]byte(summaryJSON), bounds)
		if err != nil {
			parseErr = err
			continue
		}
		return TurnOutput{
			ReplyText:    strings.TrimSpace(text[:block.Start] + text[block.End:]),
			SummaryJSON:  summaryJSON,
			Summary:      summary,
			Diagnostics:  strings.TrimSpace(stderr),
			RawStdout:    text,
			RawStderr:    stderr,
			SummaryFound: true,
		}, nil
	}
	if parseErr == nil {
		parseErr = ErrSummaryNotFound
	}
	return TurnOutput{}, parseErr
}

func flatAgentMessageTextCandidates(values map[string]any) []string {
	var candidates []string
	for _, entry := range flatMessageTextEntries(values) {
		if entry.Role != "agent" || !strings.Contains(entry.Text, CoordinatorSummaryFence) {
			continue
		}
		candidates = append(candidates, entry.Text)
	}
	return candidates
}

type messageTextEntry struct {
	Index int
	Role  string
	Text  string
	Key   string
}

func tokenAnchoredAgentMessageTextCandidates(values map[string]any, token string) []string {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil
	}
	var candidates []string
	candidates = append(candidates, tokenAnchoredStructuredAgentMessageTextCandidates(values, token)...)
	candidates = append(candidates, tokenAnchoredFlatAgentMessageTextCandidates(values, token)...)
	return candidates
}

func tokenAnchoredStructuredAgentMessageTextCandidates(values map[string]any, token string) []string {
	messages, ok := values["messages"].([]any)
	if !ok {
		return nil
	}
	entries := structuredMessageTextEntries(messages)
	anchor := latestTokenMessageIndex(entries, token)
	if anchor < 0 {
		return nil
	}
	var candidates []string
	for i := len(entries) - 1; i >= 0; i-- {
		entry := entries[i]
		if entry.Index <= anchor || entry.Role != "agent" {
			continue
		}
		candidates = append(candidates, entry.Text)
	}
	return candidates
}

func tokenAnchoredFlatAgentMessageTextCandidates(values map[string]any, token string) []string {
	entries := flatMessageTextEntries(values)
	anchor := latestTokenMessageIndex(entries, token)
	if anchor < 0 {
		return nil
	}
	var candidates []string
	for i := len(entries) - 1; i >= 0; i-- {
		entry := entries[i]
		if entry.Index <= anchor || entry.Role != "agent" {
			continue
		}
		candidates = append(candidates, entry.Text)
	}
	return candidates
}

func latestTokenMessageIndex(entries []messageTextEntry, token string) int {
	anchor := -1
	for _, entry := range entries {
		if strings.Contains(entry.Text, token) && entry.Index > anchor {
			anchor = entry.Index
		}
	}
	return anchor
}

func structuredMessageTextEntries(messages []any) []messageTextEntry {
	var entries []messageTextEntry
	for i, message := range messages {
		messageMap, ok := message.(map[string]any)
		if !ok {
			continue
		}
		if userValue, ok := valueForKeyFold(messageMap, "User"); ok {
			for _, text := range contentTextEntries(userValue) {
				entries = append(entries, messageTextEntry{Index: i, Role: "user", Text: text})
			}
		}
		if agentValue, ok := valueForKeyFold(messageMap, "Agent"); ok {
			for _, text := range contentTextEntries(agentValue) {
				entries = append(entries, messageTextEntry{Index: i, Role: "agent", Text: text})
			}
		}
	}
	return entries
}

func flatMessageTextEntries(values map[string]any) []messageTextEntry {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sortMessageAwareKeys(keys)
	var entries []messageTextEntry
	for _, key := range keys {
		text, ok := values[key].(string)
		if !ok || strings.TrimSpace(text) == "" {
			continue
		}
		entry, ok := parseFlatMessageTextKey(key)
		if !ok {
			continue
		}
		entry.Text = text
		entry.Key = key
		entries = append(entries, entry)
	}
	return entries
}

func parseFlatMessageTextKey(key string) (messageTextEntry, bool) {
	parts := strings.Split(key, ".")
	if len(parts) < 4 || !strings.EqualFold(parts[0], "messages") || !strings.EqualFold(parts[len(parts)-1], "text") {
		return messageTextEntry{}, false
	}
	index, err := strconv.Atoi(parts[1])
	if err != nil {
		return messageTextEntry{}, false
	}
	for _, part := range parts[2 : len(parts)-1] {
		switch strings.ToLower(part) {
		case "user":
			return messageTextEntry{Index: index, Role: "user"}, true
		case "agent":
			return messageTextEntry{Index: index, Role: "agent"}, true
		}
	}
	return messageTextEntry{}, false
}

func contentTextEntries(value any) []string {
	var out []string
	appendContentTextEntries(value, &out)
	return out
}

func appendContentTextEntries(value any, out *[]string) {
	switch typed := value.(type) {
	case string:
		if strings.TrimSpace(typed) != "" {
			*out = append(*out, typed)
		}
	case []any:
		for _, item := range typed {
			appendContentTextEntries(item, out)
		}
	case map[string]any:
		if text, ok := stringForKeyFold(typed, "Text"); ok {
			*out = append(*out, text)
			return
		}
		if content, ok := valueForKeyFold(typed, "content"); ok {
			appendContentTextEntries(content, out)
		}
	}
}

func outputCandidates(value any, fallback []string) []string {
	var out []string
	collectOutputCandidates(value, &out)
	if len(out) == 0 && len(fallback) > 0 {
		out = append(out, strings.Join(fallback, "\n"))
	}
	return out
}

func directOutputCandidates(value any) []string {
	var out []string
	collectDirectOutputCandidates(value, &out)
	return out
}

func collectDirectOutputCandidates(value any, out *[]string) {
	switch typed := value.(type) {
	case map[string]any:
		for _, key := range outputCandidateKeys(typed) {
			collectText(typed[key], out)
		}
	case []any:
		for _, item := range typed {
			collectDirectOutputCandidates(item, out)
		}
	}
}

func collectOutputCandidates(value any, out *[]string) {
	switch typed := value.(type) {
	case map[string]any:
		for _, key := range outputCandidateKeys(typed) {
			collectText(typed[key], out)
		}
		for _, text := range flatAgentMessageTextCandidates(typed) {
			*out = append(*out, text)
		}
	case []any:
		for _, item := range typed {
			collectOutputCandidates(item, out)
		}
	}
}

func outputCandidateKeys(values map[string]any) []string {
	preferred := []string{
		"output", "lastOutput", "last_output", "stdout", "rawStdout", "raw_stdout",
		"reply", "response", "assistant", "message", "content", "text", "transcript",
	}
	seen := map[string]bool{}
	var keys []string
	for _, key := range preferred {
		if _, ok := values[key]; ok {
			keys = append(keys, key)
			seen[key] = true
		}
	}
	var dynamic []string
	for key := range values {
		lower := strings.ToLower(key)
		if !seen[key] && (strings.Contains(lower, "output") || strings.Contains(lower, "response") || strings.Contains(lower, "reply")) {
			dynamic = append(dynamic, key)
		}
	}
	sort.Strings(dynamic)
	return append(keys, dynamic...)
}

func historyEntries(values map[string]any) []historyEntry {
	rawEntries := historyValue(values)
	out := make([]historyEntry, 0, len(rawEntries))
	for _, raw := range rawEntries {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, historyEntry{
			ID:   firstString(entry, "id", "turnId", "turn_id", "lastTurnId", "last_turn_id"),
			Text: collectStrings(entry),
			Raw:  entry,
		})
	}
	return out
}

func historyValue(values map[string]any) []any {
	for _, key := range []string{"history", "entries", "turns", "turnHistory", "turn_history"} {
		if arr, ok := values[key].([]any); ok {
			return arr
		}
	}
	if nested, ok := values["history"].(map[string]any); ok {
		if arr, ok := nested["entries"].([]any); ok {
			return arr
		}
	}
	return nil
}

func snapshotContains(snapshot sessionSnapshot, needle string) bool {
	return stringsContain(snapshot.Text, needle)
}

func stringsContain(values []string, needle string) bool {
	needle = strings.TrimSpace(needle)
	if needle == "" {
		return false
	}
	for _, value := range values {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}

func collectStrings(value any) []string {
	var out []string
	collectText(value, &out)
	return out
}

func collectText(value any, out *[]string) {
	switch typed := value.(type) {
	case string:
		if strings.TrimSpace(typed) != "" {
			*out = append(*out, typed)
		}
	case float64, bool:
		if text := stringValue(typed); text != "" {
			*out = append(*out, text)
		}
	case []any:
		for _, item := range typed {
			collectText(item, out)
		}
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sortMessageAwareKeys(keys)
		for _, key := range keys {
			collectText(typed[key], out)
		}
	}
}

func sortMessageAwareKeys(keys []string) {
	sort.SliceStable(keys, func(i, j int) bool {
		return compareDottedKeys(keys[i], keys[j]) < 0
	})
}

func compareDottedKeys(left, right string) int {
	leftParts := strings.Split(left, ".")
	rightParts := strings.Split(right, ".")
	for i := 0; i < len(leftParts) && i < len(rightParts); i++ {
		if leftParts[i] == rightParts[i] {
			continue
		}
		leftInt, leftOK := dottedKeyInt(leftParts[i])
		rightInt, rightOK := dottedKeyInt(rightParts[i])
		if leftOK && rightOK {
			if leftInt < rightInt {
				return -1
			}
			return 1
		}
		if leftParts[i] < rightParts[i] {
			return -1
		}
		return 1
	}
	switch {
	case len(leftParts) < len(rightParts):
		return -1
	case len(leftParts) > len(rightParts):
		return 1
	default:
		return 0
	}
}

func dottedKeyInt(value string) (int, bool) {
	if value == "" {
		return 0, false
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, false
	}
	return parsed, true
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func normalizeConfig(cfg Config) Config {
	cfg.Binary = strings.TrimSpace(cfg.Binary)
	if cfg.Binary == "" {
		cfg.Binary = DefaultBinary
	}
	cfg.Agent = strings.ToLower(strings.TrimSpace(cfg.Agent))
	if cfg.Agent == "" {
		cfg.Agent = AgentCodex
	}
	cfg.Model = strings.TrimSpace(cfg.Model)
	cfg.Mode = strings.TrimSpace(cfg.Mode)
	cfg.MaxPermissions = strings.TrimSpace(cfg.MaxPermissions)
	if cfg.MaxPermissions == "" {
		cfg.MaxPermissions = PermissionApproveReads
	}
	cfg.NonInteractivePermissions = strings.TrimSpace(cfg.NonInteractivePermissions)
	cfg.CWD = strings.TrimSpace(cfg.CWD)
	cfg.ClaudeAllowedTools = normalizeList(cfg.ClaudeAllowedTools)
	return cfg
}

func validateConfig(cfg Config) error {
	switch cfg.Agent {
	case AgentCodex, AgentClaude:
	default:
		return fmt.Errorf("%w: unsupported agent %q", ErrInvalidConfig, cfg.Agent)
	}
	switch cfg.MaxPermissions {
	case PermissionApproveAll, PermissionApproveReads, PermissionDenyAll:
	default:
		return fmt.Errorf("%w: unsupported permission mode %q", ErrInvalidConfig, cfg.MaxPermissions)
	}
	switch cfg.NonInteractivePermissions {
	case "", NonInteractiveDeny, NonInteractiveFail:
	default:
		return fmt.Errorf("%w: unsupported non-interactive permission mode %q", ErrInvalidConfig, cfg.NonInteractivePermissions)
	}
	return nil
}

func validateStableMetadata(meta Metadata) error {
	if strings.TrimSpace(meta.StableRecordID) == "" {
		return fmt.Errorf("%w: stable acpx record id missing", ErrResumeMismatch)
	}
	return nil
}

func sessionName(publicID, explicit string) string {
	if strings.TrimSpace(explicit) != "" {
		return strings.TrimSpace(explicit)
	}
	return strings.TrimSpace(publicID)
}

func appendOrReplaceEnv(env []string, name, value string) []string {
	prefix := name + "="
	for i, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			env[i] = prefix + value
			return env
		}
	}
	return append(env, prefix+value)
}

func normalizeList(values []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			item := strings.TrimSpace(part)
			if item == "" || seen[item] {
				continue
			}
			seen[item] = true
			out = append(out, item)
		}
	}
	return out
}

func commandDiagnostics(result CommandResult, err error) string {
	var parts []string
	if strings.TrimSpace(string(result.Stdout)) != "" {
		parts = append(parts, "stdout="+truncateForError(string(result.Stdout)))
	}
	if strings.TrimSpace(string(result.Stderr)) != "" {
		parts = append(parts, "stderr="+truncateForError(string(result.Stderr)))
	}
	if result.ExitCode != 0 {
		parts = append(parts, fmt.Sprintf("exit=%d", result.ExitCode))
	}
	if err != nil {
		parts = append(parts, "error="+err.Error())
	}
	return strings.Join(parts, " ")
}

func truncateForError(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= 300 {
		return value
	}
	return value[:300] + "...(truncated)"
}

func firstNonEmptyLine(value string) string {
	for _, line := range strings.Split(value, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return ""
}

func firstString(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := stringValue(values[key]); value != "" {
			return value
		}
	}
	return ""
}

func valueForKeyFold(values map[string]any, want string) (any, bool) {
	for key, value := range values {
		if strings.EqualFold(key, want) {
			return value, true
		}
	}
	return nil, false
}

func stringForKeyFold(values map[string]any, want string) (string, bool) {
	value, ok := valueForKeyFold(values, want)
	if !ok {
		return "", false
	}
	text, ok := value.(string)
	if !ok || strings.TrimSpace(text) == "" {
		return "", false
	}
	return text, true
}

func stringValue(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case float64:
		if typed == float64(int64(typed)) {
			return strconv.FormatInt(int64(typed), 10)
		}
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(typed)
	default:
		return ""
	}
}

func flattenScalars(prefix string, values map[string]any, out map[string]string) {
	for key, value := range values {
		name := key
		if prefix != "" {
			name = prefix + "." + key
		}
		if s := stringValue(value); s != "" {
			out[name] = s
			continue
		}
		if nested, ok := value.(map[string]any); ok {
			flattenScalars(name, nested, out)
			continue
		}
		if arr, ok := value.([]any); ok {
			for i, item := range arr {
				itemName := name + "." + strconv.Itoa(i)
				if s := stringValue(item); s != "" {
					out[itemName] = s
					continue
				}
				if nested, ok := item.(map[string]any); ok {
					flattenScalars(itemName, nested, out)
				}
			}
		}
	}
}

func historyLength(values map[string]any) int {
	for _, key := range []string{"historyLength", "history_length", "turnCount", "turn_count"} {
		if value, ok := metadataInt(values[key]); ok {
			return value
		}
	}
	if entries := historyValue(values); entries != nil {
		return len(entries)
	}
	if messages, ok := values["messages"].([]any); ok {
		return len(messages)
	}
	return 0
}

func metadataInt(value any) (int, bool) {
	switch typed := value.(type) {
	case float64:
		return int(typed), true
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case json.Number:
		if parsed, err := typed.Int64(); err == nil {
			return int(parsed), true
		}
		if parsed, err := strconv.ParseFloat(typed.String(), 64); err == nil {
			return int(parsed), true
		}
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return 0, false
		}
		if parsed, err := strconv.Atoi(trimmed); err == nil {
			return parsed, true
		}
	}
	return 0, false
}
