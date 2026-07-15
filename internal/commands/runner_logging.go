package commands

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/acpx"
	"github.com/higress-group/issue-spec/internal/commentrunner"
	runnercontext "github.com/higress-group/issue-spec/internal/commentrunner/context"
	"github.com/higress-group/issue-spec/internal/commentrunner/diagnostics"
	"github.com/higress-group/issue-spec/internal/commentrunner/intake"
	webhook "github.com/higress-group/issue-spec/internal/commentrunner/intake/webhook"
	crjobs "github.com/higress-group/issue-spec/internal/commentrunner/jobs"
	crstate "github.com/higress-group/issue-spec/internal/commentrunner/state"
	"github.com/higress-group/issue-spec/internal/commentrunner/writeback"
	"github.com/higress-group/issue-spec/internal/workspace"
)

// runnerLogger owns one process-wide diagnostic logger. Every concurrent
// event uses an immutable correlation snapshot; job and session events never
// mutate shared logger correlation state.
type runnerLogger struct {
	logger  *diagnostics.Logger
	cycleMu sync.RWMutex
	cycleID string
}

const (
	maxCoordinatorDiagnosticEntries       = 16
	maxCoordinatorDiagnosticSeverityBytes = 32
	maxCoordinatorDiagnosticMessageBytes  = 1024
	maxCoordinatorDiagnosticsTotalBytes   = 8192
)

func newRunnerLogger(cfg commentrunner.Config) (*runnerLogger, error) {
	logCfg := diagnostics.Config{
		LogDir:        cfg.LogDir,
		MaxSize:       int64(cfg.LogMaxSizeMB) * 1024 * 1024,
		MaxFiles:      cfg.LogMaxFiles,
		RetentionDays: cfg.LogRetentionDays,
		RawCaptureKB:  cfg.LogRawCaptureKB,
	}
	if logCfg.LogDir == "" && cfg.StatePath != "" {
		logCfg = logCfg.ApplyDefaults(cfg.StatePath)
	}
	logger, err := diagnostics.NewLogger(logCfg)
	if err != nil {
		return nil, fmt.Errorf("create logger: %w", err)
	}
	hostname := cfg.Hostname
	if hostname == "" {
		hostname = "github.com"
	}
	repository := ""
	if len(cfg.Repositories) > 0 {
		repository = cfg.Repositories[0]
	}
	logger.WithScope(hostname, repository, cfg.RunnerIdentity)
	return &runnerLogger{logger: logger}, nil
}

func (rl *runnerLogger) close() error {
	if rl == nil || rl.logger == nil {
		return nil
	}
	return rl.logger.Close()
}

func (rl *runnerLogger) sync() error {
	if rl == nil || rl.logger == nil {
		return nil
	}
	return rl.logger.Sync()
}

func (rl *runnerLogger) cleanup() error {
	if rl == nil || rl.logger == nil {
		return nil
	}
	return rl.logger.Cleanup()
}

func (rl *runnerLogger) shutdown(mode string) error {
	if rl == nil {
		return nil
	}
	var errs []error
	if err := rl.write(diagnostics.LevelInfo, "runner", "shutdown_start", "runner shutdown started",
		diagnostics.Correlation{}, map[string]interface{}{"mode": mode}); err != nil {
		errs = append(errs, err)
	}
	if err := rl.cleanup(); err != nil {
		errs = append(errs, fmt.Errorf("cleanup diagnostics: %w", err))
		_ = rl.write(diagnostics.LevelWarn, "cleanup", "log_cleanup_failed", "diagnostic retention cleanup failed",
			diagnostics.Correlation{}, map[string]interface{}{"error": err.Error()})
	}
	if err := rl.sync(); err != nil {
		errs = append(errs, fmt.Errorf("sync diagnostics: %w", err))
	}
	if err := rl.close(); err != nil {
		errs = append(errs, fmt.Errorf("close diagnostics: %w", err))
	}
	return errors.Join(errs...)
}

func (rl *runnerLogger) logStartup(mode string) error {
	return rl.write(diagnostics.LevelInfo, "runner", "startup", "runner starting", diagnostics.Correlation{},
		map[string]interface{}{"mode": mode, "log_dir": rl.config().LogDir})
}

func (rl *runnerLogger) newCycle() (string, error) {
	if rl == nil {
		return "", nil
	}
	cycleID := uuid.NewString()
	rl.cycleMu.Lock()
	rl.cycleID = cycleID
	rl.cycleMu.Unlock()
	return cycleID, rl.write(diagnostics.LevelInfo, "runner", "cycle_start", "starting runner cycle",
		diagnostics.Correlation{CycleID: cycleID}, nil)
}

func (rl *runnerLogger) currentCorrelation(correlation diagnostics.Correlation) diagnostics.Correlation {
	if correlation.CycleID == "" && rl != nil {
		rl.cycleMu.RLock()
		correlation.CycleID = rl.cycleID
		rl.cycleMu.RUnlock()
	}
	return correlation
}

func (rl *runnerLogger) write(level diagnostics.Level, component, event, message string,
	correlation diagnostics.Correlation, details map[string]interface{}) error {
	if rl == nil || rl.logger == nil {
		return nil
	}
	return rl.logger.WriteEventWithCorrelation(level, component, event, message,
		rl.currentCorrelation(correlation), details)
}

func (rl *runnerLogger) logPreflight(report commentrunner.PreflightReport) error {
	checks := make([]interface{}, 0, len(report.Checks))
	for _, check := range report.Checks {
		checks = append(checks, map[string]interface{}{
			"name": check.Name, "status": check.Status, "detail": check.Detail, "hint": check.Hint,
		})
	}
	level, event, message := diagnostics.LevelInfo, "preflight_complete", "preflight checks passed"
	if !report.OK {
		level, event, message = diagnostics.LevelError, "preflight_failed", "preflight checks failed"
	}
	return rl.write(level, "preflight", event, message, diagnostics.Correlation{},
		map[string]interface{}{"ok": report.OK, "checks": checks})
}

func (rl *runnerLogger) logIntake(result *intake.Result) error {
	if rl == nil || result == nil {
		return nil
	}
	var errs []error
	errs = append(errs, rl.write(diagnostics.LevelInfo, "intake", "intake_complete", "command intake completed",
		diagnostics.Correlation{}, map[string]interface{}{
			"commands_count": len(result.Commands), "jobs_count": len(result.Jobs),
			"cancellations_count": len(result.Cancellations), "ok": result.OK,
		}))
	for _, command := range result.Commands {
		level := diagnostics.LevelInfo
		if command.Status == intake.CommandStatusRejected || command.Status == intake.CommandStatusUnauthorized {
			level = diagnostics.LevelWarn
		}
		correlation := diagnostics.Correlation{JobID: command.JobID, CancellationID: command.CancellationID,
			PublicSessionID: command.PublicSessionID, TriggerCommentID: command.CommentID}
		errs = append(errs, rl.write(level, "intake", "command_"+command.Status, "command intake decision recorded",
			correlation, map[string]interface{}{"repo": command.Repo, "issue": command.Issue,
				"verb": command.Verb, "reason": command.Reason, "created": command.Created}))
	}
	for _, candidate := range result.Jobs {
		job := crstate.Job{ID: candidate.JobID, Repo: candidate.Repo, IssueNumber: candidate.Issue,
			PublicSessionID: candidate.PublicSessionID, TriggerCommentID: candidate.TriggerComment,
			CommandID: candidate.CommandID, CommandName: string(candidate.Verb), Status: crstate.StatusQueued}
		event, message := "job_queued", "runner job queued"
		if !candidate.Created {
			event, message = "job_duplicate", "duplicate runner job observed"
		}
		errs = append(errs, rl.logJobEvent(job, diagnostics.Correlation{}, diagnostics.LevelInfo, event, message,
			map[string]interface{}{"created": candidate.Created}, "", ""))
	}
	for _, candidate := range result.Cancellations {
		event := "cancellation_queued"
		if !candidate.Created {
			event = "cancellation_duplicate"
		}
		errs = append(errs, rl.write(diagnostics.LevelInfo, "cancellation", event, "cancellation intake decision recorded",
			diagnostics.Correlation{CancellationID: candidate.CancellationID, PublicSessionID: candidate.PublicSessionID,
				TriggerCommentID: candidate.TriggerComment}, map[string]interface{}{"repo": candidate.Repo, "created": candidate.Created}))
	}
	return errors.Join(errs...)
}

func (rl *runnerLogger) logReconcile(result *crjobs.ReconcileResult) error {
	if rl == nil || result == nil {
		return nil
	}
	var errs []error
	errs = append(errs, rl.write(diagnostics.LevelInfo, "reconcile", "reconcile_complete", "job reconciliation completed",
		diagnostics.Correlation{}, map[string]interface{}{
			"reconciled": result.Reconciled, "running": result.Running, "completed": result.Completed,
			"failed": result.Failed, "cancelled": result.Cancelled, "interrupted": result.Interrupted,
			"queued": result.Queued,
		}))
	for _, job := range result.Jobs {
		level := lifecycleLogLevel(job.Status)
		errs = append(errs, rl.write(level, "reconcile", "job_reconciled", "job restart reconciliation recorded",
			diagnostics.Correlation{JobID: job.JobID, PublicSessionID: job.PublicSessionID},
			map[string]interface{}{"previous_status": job.PreviousStatus, "status": job.Status,
				"action": job.Action, "diagnostic": job.Diagnostic}))
	}
	return errors.Join(errs...)
}

func (rl *runnerLogger) logDispatch(result *crjobs.Result) error {
	if rl == nil || result == nil {
		return nil
	}
	level, event := diagnostics.LevelInfo, "dispatch_complete"
	if result.Error != "" {
		level, event = diagnostics.LevelError, "dispatch_error"
	}
	return rl.write(level, "dispatch", event, "job dispatch result recorded",
		diagnostics.Correlation{JobID: result.JobID, CancellationID: result.CancellationID},
		map[string]interface{}{"executed": result.Executed, "executed_count": result.ExecutedCount,
			"status": result.Status, "reason": result.Reason, "error": result.Error})
}

func (rl *runnerLogger) logCancellation(count int) error {
	return rl.write(diagnostics.LevelInfo, "cancellation", "cancellation_complete", "cancellation processing completed",
		diagnostics.Correlation{}, map[string]interface{}{"cancellations_processed": count})
}

func (rl *runnerLogger) logWorkspaceCleanup(results []workspace.CleanupResult) error {
	removed, kept, failed := 0, 0, 0
	for _, result := range results {
		switch result.Action {
		case "removed", "would_remove":
			removed++
		case "failed", "rejected":
			failed++
		default:
			kept++
		}
	}
	return rl.write(diagnostics.LevelInfo, "cleanup", "cleanup_complete", "workspace cleanup completed",
		diagnostics.Correlation{}, map[string]interface{}{"removed": removed, "kept": kept, "failed": failed})
}

func (rl *runnerLogger) logError(component, message string) error {
	return rl.write(diagnostics.LevelError, component, "error", message, diagnostics.Correlation{}, nil)
}

func (rl *runnerLogger) logWarn(component, message string) error {
	return rl.write(diagnostics.LevelWarn, component, "warning", message, diagnostics.Correlation{}, nil)
}

func (rl *runnerLogger) logInfo(component, event, message string) error {
	return rl.write(diagnostics.LevelInfo, component, event, message, diagnostics.Correlation{}, nil)
}

// ObserveWebhook implements webhook.HandlerObserver.
func (rl *runnerLogger) ObserveWebhook(result webhook.HandlerDiagnostic) {
	level, event, message := diagnostics.LevelWarn, "webhook_rejected", "webhook request rejected"
	if result.Outcome == "accepted" {
		level, event, message = diagnostics.LevelInfo, "webhook_accepted", "signed webhook accepted"
		if result.Duplicate {
			event, message = "webhook_duplicate", "duplicate signed webhook accepted"
		}
	}
	err := rl.write(level, "webhook", event, message,
		diagnostics.Correlation{DeliveryID: result.DeliveryID, EventID: result.EventID},
		map[string]interface{}{"request_id": result.RequestID, "status_code": result.StatusCode,
			"outcome": result.Outcome, "duplicate": result.Duplicate})
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to persist webhook diagnostics: %v\n", err)
	}
}

// ObserveWebhookReconcile implements webhook.ReconcileObserver. An observer
// failure stops the serve runtime so accepted deliveries are not processed
// without the required correlation trail.
func (rl *runnerLogger) ObserveWebhookReconcile(result webhook.ReconcileResult) error {
	correlation := diagnostics.Correlation{DeliveryID: result.DeliveryID, EventID: result.EventID,
		JobID: result.JobID, CancellationID: result.CancellationID, PublicSessionID: result.PublicSessionID,
		TriggerCommentID: result.TriggerCommentID}
	level, event := diagnostics.LevelInfo, "webhook_reconciled"
	if result.Failed {
		level, event = diagnostics.LevelError, "webhook_reconcile_failed"
	} else if result.Retried {
		level, event = diagnostics.LevelWarn, "webhook_reconcile_retry"
	}
	var errs []error
	errs = append(errs, rl.write(level, "intake", event, "webhook reconciliation decision recorded", correlation,
		map[string]interface{}{"repo": result.Repository, "outcome": result.Outcome,
			"reason": result.Reason, "completed": result.Completed, "retried": result.Retried,
			"idempotent": result.Idempotent}))
	if result.JobID != "" {
		job := crstate.Job{ID: result.JobID, Repo: result.Repository, TriggerCommentID: result.TriggerCommentID,
			Status: crstate.StatusQueued}
		jobEvent, jobMessage := "job_queued", "runner job queued"
		if result.Idempotent {
			jobEvent, jobMessage = "job_duplicate", "idempotent runner job observed"
		}
		errs = append(errs, rl.logJobEvent(job, correlation, diagnostics.LevelInfo, jobEvent, jobMessage,
			map[string]interface{}{"delivery_outcome": result.Outcome, "idempotent": result.Idempotent}, "", ""))
	}
	if result.CancellationID != "" {
		cancelEvent, cancelMessage := "cancellation_queued", "runner cancellation queued"
		if result.Idempotent {
			cancelEvent, cancelMessage = "cancellation_duplicate", "idempotent runner cancellation observed"
		}
		errs = append(errs, rl.write(diagnostics.LevelInfo, "cancellation", cancelEvent,
			cancelMessage, correlation, map[string]interface{}{"delivery_outcome": result.Outcome,
				"idempotent": result.Idempotent}))
	}
	return errors.Join(errs...)
}

type runnerDiagnosticWriteback struct {
	next   crjobs.Writeback
	logger *runnerLogger
}

func wrapRunnerWriteback(next crjobs.Writeback, logger *runnerLogger) crjobs.Writeback {
	if next == nil || logger == nil {
		return next
	}
	return &runnerDiagnosticWriteback{next: next, logger: logger}
}

func (w *runnerDiagnosticWriteback) Write(ctx context.Context, request writeback.Request) (writeback.Result, error) {
	result, writeErr := w.next.Write(ctx, request)
	logErr := w.logger.logJobLifecycle(request, result, writeErr)
	return result, errors.Join(writeErr, logErr)
}

func (rl *runnerLogger) logJobLifecycle(request writeback.Request, result writeback.Result, writeErr error) error {
	job := request.Job
	if result.Comment.ID != 0 {
		job.StatusCommentID = result.Comment.ID
		job.StatusCommentURL = result.Comment.HTMLURL
	} else if result.Writeback.CommentID != 0 {
		job.StatusCommentID = result.Writeback.CommentID
		job.StatusCommentURL = result.Writeback.URL
	}
	level, event, message := lifecycleEvent(request.Status)
	details := map[string]interface{}{
		"repo": job.Repo, "issue": job.IssueNumber, "status": request.Status, "phase": request.Phase,
		"command": job.CommandName, "writeback_created": result.Created, "writeback_updated": result.Updated,
	}
	if len(request.Diagnostics) > 0 {
		details["diagnostic"] = strings.Join(request.Diagnostics, "; ")
	}
	if request.Err != nil {
		details["lifecycle_error"] = request.Err.Error()
	}
	if writeErr != nil {
		details["writeback_error"] = writeErr.Error()
		level = diagnostics.LevelError
	}
	if request.CoordinatorSummary != nil {
		details["summary_status"] = request.CoordinatorSummary.Status
		details["summary_artifacts"] = len(request.CoordinatorSummary.Artifacts)
		details["summary_commands"] = len(request.CoordinatorSummary.Commands)
		details["summary_children"] = len(request.CoordinatorSummary.Children)
		details["summary_processes"] = len(request.CoordinatorSummary.Processes)
		details["summary_diagnostics"] = len(request.CoordinatorSummary.Diagnostics)
		entries, truncated := boundedCoordinatorDiagnostics(request.CoordinatorSummary.Diagnostics)
		if len(entries) > 0 {
			details["summary_diagnostic_entries"] = entries
		}
		if truncated {
			details["summary_diagnostics_truncated"] = true
		}
	}
	stdout, stderr := diagnosticAcpxStreams(request)
	return rl.logJobEvent(job, diagnostics.Correlation{}, level, event, message, details, stdout, stderr)
}

func boundedCoordinatorDiagnostics(input []runnercontext.DiagnosticSummary) ([]interface{}, bool) {
	entries := make([]interface{}, 0, min(len(input), maxCoordinatorDiagnosticEntries))
	remaining := maxCoordinatorDiagnosticsTotalBytes
	truncated := false
	for index, diagnostic := range input {
		if index >= maxCoordinatorDiagnosticEntries {
			truncated = true
			break
		}
		severity, severityTruncated := truncateDiagnosticUTF8(strings.TrimSpace(diagnostic.Severity),
			maxCoordinatorDiagnosticSeverityBytes)
		message, messageTruncated := truncateDiagnosticUTF8(strings.TrimSpace(diagnostic.Message),
			maxCoordinatorDiagnosticMessageBytes)
		if severityTruncated || messageTruncated {
			truncated = true
		}
		entryBytes := len([]byte(severity)) + len([]byte(message))
		if entryBytes > remaining {
			messageBudget := remaining - len([]byte(severity))
			if messageBudget <= 0 {
				truncated = true
				break
			}
			message, _ = truncateDiagnosticUTF8(message, messageBudget)
			entryBytes = len([]byte(severity)) + len([]byte(message))
			truncated = true
		}
		entry := map[string]interface{}{"message": message}
		if severity != "" {
			entry["severity"] = severity
		}
		entries = append(entries, entry)
		remaining -= entryBytes
		if remaining <= 0 && index+1 < len(input) {
			truncated = true
			break
		}
	}
	return entries, truncated
}

func truncateDiagnosticUTF8(value string, maxBytes int) (string, bool) {
	if maxBytes <= 0 || len([]byte(value)) <= maxBytes {
		return value, false
	}
	if maxBytes <= 3 {
		return strings.Repeat(".", maxBytes), true
	}
	for len([]byte(value)) > maxBytes-3 {
		_, size := utf8.DecodeLastRuneInString(value)
		if size <= 0 {
			return "...", true
		}
		value = value[:len(value)-size]
	}
	return value + "...", true
}

func diagnosticAcpxStreams(request writeback.Request) (string, string) {
	stdout, stderr := request.AcpxStdout, request.AcpxStderr
	if stdout != "" || stderr != "" || request.Err == nil {
		return stdout, stderr
	}
	var partial *acpx.PartialDispatchError
	if errors.As(request.Err, &partial) {
		return partial.Result.Output.RawStdout, partial.Result.Output.RawStderr
	}
	var command *acpx.CommandError
	if errors.As(request.Err, &command) {
		return string(command.Result.Stdout), string(command.Result.Stderr)
	}
	return "", ""
}

func (rl *runnerLogger) logJobEvent(job crstate.Job, extra diagnostics.Correlation, level diagnostics.Level,
	event, message string, details map[string]interface{}, stdout, stderr string) error {
	job.ID = strings.TrimSpace(job.ID)
	if !safeDiagnosticID(job.ID) {
		return fmt.Errorf("unsafe diagnostic job id")
	}
	correlation := mergeDiagnosticCorrelation(jobCorrelation(job), extra)
	correlation = rl.currentCorrelation(correlation)
	var errs []error
	errs = append(errs, rl.logger.WriteEventWithCorrelation(level, "jobs", event, message, correlation, details))

	jobLogger, err := rl.logger.JobLoggerWithCorrelation(correlation)
	if err != nil {
		return errors.Join(errors.Join(errs...), fmt.Errorf("create job logger: %w", err))
	}
	if err := jobLogger.Initialize(job.ID); err != nil {
		return errors.Join(errors.Join(errs...), fmt.Errorf("initialize job logger: %w", err))
	}
	errs = append(errs, jobLogger.WriteEventWithDetails(level, "jobs", event, message, details))
	if stdout != "" {
		_, err = jobLogger.WriteStdout([]byte(stdout))
		errs = append(errs, err)
	}
	if stderr != "" {
		_, err = jobLogger.WriteStderr([]byte(stderr))
		errs = append(errs, err)
	}
	errs = append(errs, jobLogger.Sync())

	turnID := strings.TrimSpace(correlation.TurnCorrelationID)
	if turnID == "" {
		turnID = strings.TrimSpace(correlation.AcpxLastTurnID)
	}
	var sessionLogger *diagnostics.SessionLogger
	if safeDiagnosticID(correlation.PublicSessionID) && safeDiagnosticID(turnID) {
		sessionLogger, err = rl.logger.SessionLoggerWithCorrelation(correlation)
		if err != nil {
			errs = append(errs, fmt.Errorf("create session logger: %w", err))
		} else {
			sessionLogger.SetSessionID(correlation.PublicSessionID)
			errs = append(errs, sessionLogger.WriteTurnWithDetails(turnID, level, "jobs", event, message, details))
			errs = append(errs, sessionLogger.Sync())
		}
	}

	now := time.Now().UTC()
	jobPath := filepath.ToSlash(filepath.Join("jobs", job.ID+".ndjson"))
	errs = append(errs, rl.logIndex("job", job.ID, jobPath, now))
	if correlation.PublicSessionID != "" && turnID != "" && safeDiagnosticID(correlation.PublicSessionID) && safeDiagnosticID(turnID) {
		sessionPath := filepath.ToSlash(filepath.Join("sessions", correlation.PublicSessionID, turnID+".ndjson"))
		errs = append(errs, rl.logIndex("session", correlation.PublicSessionID, sessionPath, now))
	}
	if correlation.TriggerCommentID != 0 {
		errs = append(errs, rl.logIndex("comment", fmt.Sprint(correlation.TriggerCommentID), jobPath, now))
	}
	if correlation.StatusCommentID != 0 {
		errs = append(errs, rl.logIndex("comment", fmt.Sprint(correlation.StatusCommentID), jobPath, now))
	}
	if correlation.AcpxRecordID != "" {
		errs = append(errs, rl.logIndex("acpx_record", correlation.AcpxRecordID, jobPath, now))
	}
	if correlation.DeliveryID != "" {
		errs = append(errs, rl.logIndex("delivery", correlation.DeliveryID, jobPath, now))
	}
	if correlation.WorkspaceID != "" {
		errs = append(errs, rl.logIndex("workspace", correlation.WorkspaceID, jobPath, now))
	}
	if sessionLogger != nil {
		errs = append(errs, sessionLogger.Close())
	}
	errs = append(errs, jobLogger.Close())
	return errors.Join(errs...)
}

func (rl *runnerLogger) logIndex(kind, id, path string, at time.Time) error {
	if strings.TrimSpace(id) == "" {
		return nil
	}
	return rl.logger.LogIndex(diagnostics.IndexEntry{Type: kind, ID: id, FilePath: path, CreatedAt: at, UpdatedAt: at})
}

func jobCorrelation(job crstate.Job) diagnostics.Correlation {
	return diagnostics.Correlation{
		JobID:             job.ID,
		PublicSessionID:   firstDiagnosticValue(job.PublicSessionID, job.DispatchIntent.PublicSessionID),
		TriggerCommentID:  job.TriggerCommentID,
		StatusCommentID:   firstDiagnosticInt64(job.StatusCommentID, job.DispatchIntent.StatusCommentID),
		WorkspaceID:       job.Workspace.ID,
		AcpxRecordID:      firstDiagnosticValue(job.AcpxRecordID, job.DispatchIntent.AcpxRecordID, job.Acpx.StableRecordID),
		AcpxLastTurnID:    job.Acpx.LastTurnID,
		TurnCorrelationID: job.DispatchIntent.TurnCorrelationToken,
	}
}

func mergeDiagnosticCorrelation(base, extra diagnostics.Correlation) diagnostics.Correlation {
	if extra.DeliveryID != "" {
		base.DeliveryID = extra.DeliveryID
	}
	if extra.EventID != "" {
		base.EventID = extra.EventID
	}
	if extra.CycleID != "" {
		base.CycleID = extra.CycleID
	}
	if extra.JobID != "" {
		base.JobID = extra.JobID
	}
	if extra.CancellationID != "" {
		base.CancellationID = extra.CancellationID
	}
	if extra.PublicSessionID != "" {
		base.PublicSessionID = extra.PublicSessionID
	}
	if extra.TriggerCommentID != 0 {
		base.TriggerCommentID = extra.TriggerCommentID
	}
	if extra.StatusCommentID != 0 {
		base.StatusCommentID = extra.StatusCommentID
	}
	if extra.WorkspaceID != "" {
		base.WorkspaceID = extra.WorkspaceID
	}
	if extra.AcpxRecordID != "" {
		base.AcpxRecordID = extra.AcpxRecordID
	}
	if extra.AcpxLastTurnID != "" {
		base.AcpxLastTurnID = extra.AcpxLastTurnID
	}
	if extra.TurnCorrelationID != "" {
		base.TurnCorrelationID = extra.TurnCorrelationID
	}
	return base
}

func lifecycleEvent(status crstate.LifecycleStatus) (diagnostics.Level, string, string) {
	switch status {
	case crstate.StatusRunning, crstate.StatusDispatched:
		return diagnostics.LevelInfo, "job_started", "runner job started"
	case crstate.StatusCompleted:
		return diagnostics.LevelInfo, "job_completed", "runner job completed"
	case crstate.StatusCancelled:
		return diagnostics.LevelWarn, "job_cancelled", "runner job cancelled"
	case crstate.StatusInterrupted:
		return diagnostics.LevelError, "job_interrupted", "runner job interrupted"
	case crstate.StatusRejected:
		return diagnostics.LevelWarn, "job_rejected", "runner job rejected"
	case crstate.StatusFailed:
		return diagnostics.LevelError, "job_failed", "runner job failed"
	default:
		return diagnostics.LevelInfo, "job_lifecycle", "runner job lifecycle updated"
	}
}

func lifecycleLogLevel(status crstate.LifecycleStatus) diagnostics.Level {
	level, _, _ := lifecycleEvent(status)
	return level
}

func safeDiagnosticID(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && value != "." && value != ".." && filepath.Base(value) == value &&
		!strings.ContainsAny(value, `/\\`)
}

func firstDiagnosticValue(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func firstDiagnosticInt64(values ...int64) int64 {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

func (rl *runnerLogger) config() diagnostics.Config {
	if rl == nil || rl.logger == nil {
		return diagnostics.Config{}
	}
	return rl.logger.Config()
}
