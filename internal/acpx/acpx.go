package acpx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
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

	dispatch, err := a.dispatchPrompt(ctx, sessionName, req.Prompt, req.NoWait, req.TurnCorrelationToken)
	if err != nil {
		return DispatchResult{}, err
	}
	refreshed, refreshErr := a.Refresh(ctx, SessionRef{
		PublicSessionID: req.PublicSessionID,
		SessionName:     sessionName,
		StableRecordID:  meta.StableRecordID,
	})
	if refreshErr != nil {
		return DispatchResult{}, refreshErr
	}
	if refreshed.StableRecordID != meta.StableRecordID {
		return DispatchResult{}, fmt.Errorf("%w: refreshed record %q does not match new record %q", ErrResumeMismatch, refreshed.StableRecordID, meta.StableRecordID)
	}
	return DispatchResult{
		PublicSessionID: req.PublicSessionID,
		SessionName:     sessionName,
		NewSession:      !req.UseEnsure,
		EnsuredSession:  req.UseEnsure,
		NoWait:          dispatch.noWait,
		Queued:          dispatch.noWait,
		Metadata:        refreshed,
		Output:          dispatch.output,
	}, nil
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
	if err := a.applyMode(ctx, sessionName); err != nil {
		return DispatchResult{}, err
	}
	dispatch, err := a.dispatchPrompt(ctx, sessionName, req.Prompt, req.NoWait, req.TurnCorrelationToken)
	if err != nil {
		return DispatchResult{}, err
	}
	after, refreshErr := a.Refresh(ctx, SessionRef{
		PublicSessionID: req.PublicSessionID,
		SessionName:     sessionName,
		StableRecordID:  req.StableRecordID,
	})
	if refreshErr != nil {
		return DispatchResult{}, refreshErr
	}
	return DispatchResult{
		PublicSessionID: req.PublicSessionID,
		SessionName:     sessionName,
		NoWait:          dispatch.noWait,
		Queued:          dispatch.noWait,
		Metadata:        after,
		Output:          dispatch.output,
	}, nil
}

func (a *Adapter) Refresh(ctx context.Context, ref SessionRef) (Metadata, error) {
	cmd := a.BuildRefreshCommand(sessionName(ref.PublicSessionID, ref.SessionName))
	result, err := a.run(ctx, "sessions show", cmd)
	if err != nil {
		return Metadata{}, err
	}
	meta, err := ParseMetadata(result.Stdout)
	if err != nil {
		return Metadata{}, fmt.Errorf("parse session metadata: %w", err)
	}
	meta.SessionName = sessionName(ref.PublicSessionID, ref.SessionName)
	if err := validateStableMetadata(meta); err != nil {
		return Metadata{}, err
	}
	if ref.StableRecordID != "" && meta.StableRecordID != ref.StableRecordID {
		return Metadata{}, fmt.Errorf("%w: record %q does not match expected %q", ErrResumeMismatch, meta.StableRecordID, ref.StableRecordID)
	}
	return meta, nil
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
	_, err := a.run(ctx, "set-mode", a.BuildSetModeCommand(sessionName))
	return err
}

type promptDispatch struct {
	output TurnOutput
	noWait bool
}

func (a *Adapter) dispatchPrompt(ctx context.Context, sessionName, prompt string, noWait bool, token string) (promptDispatch, error) {
	effectiveNoWait := noWait || a.cfg.NoWait
	cmd := a.BuildPromptCommand(sessionName, []byte(prompt), effectiveNoWait, token)
	result, err := a.run(ctx, "prompt", cmd)
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
		return promptDispatch{}, err
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

func ParseMetadata(data []byte) (Metadata, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return Metadata{}, fmt.Errorf("empty acpx metadata")
	}
	now := time.Now().UTC()
	var values map[string]any
	if err := json.Unmarshal(trimmed, &values); err != nil {
		line := firstNonEmptyLine(string(trimmed))
		if line == "" {
			return Metadata{}, err
		}
		return Metadata{StableRecordID: line, RefreshedAt: now, Raw: map[string]string{"text": line}}, nil
	}
	raw := map[string]string{}
	flattenScalars("", values, raw)
	meta := Metadata{
		StableRecordID:    firstString(values, "acpxRecordId", "acpx_record_id", "recordId", "record_id", "id", "stableRecordId", "stable_record_id"),
		TrueSessionID:     firstString(values, "acpxSessionId", "acpx_session_id", "sessionId", "session_id", "trueSessionId", "true_session_id"),
		ProviderSessionID: firstString(values, "agentSessionId", "agent_session_id", "providerSessionId", "provider_session_id"),
		LastTurnID:        firstString(values, "lastTurnId", "last_turn_id", "turnId", "turn_id", "lastPromptId", "last_prompt_id"),
		Agent:             firstString(values, "agent", "agentCommand", "agent_command"),
		SessionName:       firstString(values, "name", "sessionName", "session_name"),
		CWD:               firstString(values, "cwd", "workingDirectory", "working_directory"),
		HistoryLength:     historyLength(values),
		RefreshedAt:       now,
		Raw:               raw,
	}
	return meta, nil
}

func ParseTurnOutput(stdout, stderr []byte, bounds contextbundle.SummaryBounds) (TurnOutput, error) {
	rawStdout := string(stdout)
	rawStderr := string(stderr)
	blocks, err := findSummaryBlocks(rawStdout)
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
	summaryJSON := strings.TrimSpace(block.body)
	summary, err := contextbundle.ParseCoordinatorSummary([]byte(summaryJSON), bounds)
	if err != nil {
		return TurnOutput{}, err
	}
	reply := strings.TrimSpace(rawStdout[:block.start] + rawStdout[block.end:])
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
		}
	}
}

func historyLength(values map[string]any) int {
	for _, key := range []string{"historyLength", "history_length", "turnCount", "turn_count"} {
		if value, ok := values[key].(float64); ok {
			return int(value)
		}
	}
	for _, key := range []string{"history", "entries", "turns", "turnHistory", "turn_history"} {
		if arr, ok := values[key].([]any); ok {
			return len(arr)
		}
	}
	if nested, ok := values["history"].(map[string]any); ok {
		if arr, ok := nested["entries"].([]any); ok {
			return len(arr)
		}
	}
	return 0
}

type fencedBlock struct {
	start int
	end   int
	body  string
}

func findSummaryBlocks(text string) ([]fencedBlock, error) {
	var blocks []fencedBlock
	offset := 0
	for offset < len(text) {
		lineEnd := strings.IndexByte(text[offset:], '\n')
		if lineEnd == -1 {
			lineEnd = len(text)
		} else {
			lineEnd += offset + 1
		}
		line := strings.TrimSpace(strings.TrimSuffix(text[offset:lineEnd], "\n"))
		if strings.HasPrefix(line, "```") && strings.TrimSpace(strings.TrimPrefix(line, "```")) == CoordinatorSummaryFence {
			bodyStart := lineEnd
			closeStart, closeEnd, ok := findClosingFence(text, bodyStart)
			if !ok {
				return nil, fmt.Errorf("coordinator summary fence is not closed")
			}
			blocks = append(blocks, fencedBlock{start: offset, end: closeEnd, body: text[bodyStart:closeStart]})
			offset = closeEnd
			continue
		}
		offset = lineEnd
	}
	return blocks, nil
}

func findClosingFence(text string, offset int) (int, int, bool) {
	for offset < len(text) {
		lineEnd := strings.IndexByte(text[offset:], '\n')
		if lineEnd == -1 {
			lineEnd = len(text)
		} else {
			lineEnd += offset + 1
		}
		line := strings.TrimSpace(strings.TrimSuffix(text[offset:lineEnd], "\n"))
		if strings.HasPrefix(line, "```") {
			return offset, lineEnd, true
		}
		offset = lineEnd
	}
	return 0, 0, false
}
