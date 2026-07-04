package commands

import (
	"fmt"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/commentrunner"
	"github.com/higress-group/issue-spec/internal/commentrunner/diagnostics"
	crjobs "github.com/higress-group/issue-spec/internal/commentrunner/jobs"
	"github.com/higress-group/issue-spec/internal/commentrunner/intake"
	"github.com/higress-group/issue-spec/internal/workspace"
)

// runnerLogger holds the diagnostic logger for the runner
type runnerLogger struct {
	logger      *diagnostics.Logger
	correlation diagnostics.Correlation
	scope       diagnostics.Scope
	cycleID     string
}

// newRunnerLogger creates a new runner logger from the runner config
func newRunnerLogger(cfg commentrunner.Config) (*runnerLogger, error) {
	// Build logging config from runner config
	logCfg := diagnostics.Config{
		LogDir:        cfg.LogDir,
		MaxSize:       int64(cfg.LogMaxSizeMB) * 1024 * 1024,
		MaxFiles:      cfg.LogMaxFiles,
		RetentionDays: cfg.LogRetentionDays,
		RawCaptureKB:  cfg.LogRawCaptureKB,
	}

	// Apply default log directory if not set
	if logCfg.LogDir == "" && cfg.StatePath != "" {
		logCfg = logCfg.ApplyDefaults(cfg.StatePath)
	}

	// Create logger
	logger, err := diagnostics.NewLogger(logCfg)
	if err != nil {
		return nil, fmt.Errorf("create logger: %w", err)
	}

	// Build scope from config
	hostname := cfg.Hostname
	if hostname == "" {
		hostname = "github.com"
	}

	var repo string
	if len(cfg.Repositories) > 0 {
		repo = cfg.Repositories[0]
	}

	scope := diagnostics.Scope{
		Host:        hostname,
		Repo:        repo,
		RunnerLogin: cfg.RunnerIdentity,
	}

	logger.WithScope(hostname, repo, cfg.RunnerIdentity)

	return &runnerLogger{
		logger: logger,
		scope:  scope,
	}, nil
}

// close closes the logger
func (rl *runnerLogger) close() error {
	if rl == nil || rl.logger == nil {
		return nil
	}
	return rl.logger.Close()
}

// sync flushes the logger
func (rl *runnerLogger) sync() error {
	if rl == nil || rl.logger == nil {
		return nil
	}
	return rl.logger.Sync()
}

// cleanup performs log cleanup
func (rl *runnerLogger) cleanup() error {
	if rl == nil || rl.logger == nil {
		return nil
	}
	return rl.logger.Cleanup()
}

// newCycle starts a new poll cycle and returns the cycle ID
func (rl *runnerLogger) newCycle() string {
	cycleID := uuid.New().String()
	rl.cycleID = cycleID
	rl.correlation.CycleID = cycleID
	rl.logger.WithCorrelation(rl.correlation)

	rl.logger.Info("runner", "cycle_start", "starting poll cycle")

	return cycleID
}

// logPreflight logs preflight results
func (rl *runnerLogger) logPreflight(report commentrunner.PreflightReport) {
	if rl == nil {
		return
	}

	details := make(map[string]interface{})
	details["ok"] = report.OK

	checks := make([]map[string]interface{}, 0, len(report.Checks))
	for _, check := range report.Checks {
		checks = append(checks, map[string]interface{}{
			"name":    check.Name,
			"status":  check.Status,
			"detail":  check.Detail,
			"hint":    check.Hint,
		})
	}
	details["checks"] = checks

	if report.OK {
		rl.logger.Info("preflight", "preflight_complete", "preflight checks passed")
	} else {
		rl.logger.Error("preflight", "preflight_failed", "preflight checks failed")
	}

	rl.logger.LogEventWithDetails(diagnostics.LevelInfo, "preflight", "preflight_result", "preflight check completed", details)
}

// logIntake logs intake results
func (rl *runnerLogger) logIntake(result *intake.Result) {
	if rl == nil {
		return
	}

	details := make(map[string]interface{})
	details["commands_count"] = len(result.Commands)
	details["jobs_count"] = len(result.Jobs)
	details["cancellations_count"] = len(result.Cancellations)

	rl.logger.LogEventWithDetails(diagnostics.LevelInfo, "intake", "intake_complete", "command intake completed", details)
}

// logReconcile logs reconcile results
func (rl *runnerLogger) logReconcile(result *crjobs.ReconcileResult) {
	if rl == nil {
		return
	}

	details := make(map[string]interface{})
	details["reconciled"] = result.Reconciled
	details["running"] = result.Running
	details["completed"] = result.Completed
	details["failed"] = result.Failed
	details["cancelled"] = result.Cancelled
	details["interrupted"] = result.Interrupted
	details["queued"] = result.Queued

	rl.logger.LogEventWithDetails(diagnostics.LevelInfo, "reconcile", "reconcile_complete", "job reconciliation completed", details)
}

// logDispatch logs dispatch results
func (rl *runnerLogger) logDispatch(result *crjobs.Result) {
	if rl == nil {
		return
	}

	details := make(map[string]interface{})
	details["executed"] = result.Executed
	details["job_id"] = result.JobID
	details["status"] = result.Status
	details["reason"] = result.Reason

	if result.Error != "" {
		details["error"] = result.Error
		rl.logger.Error("dispatch", "dispatch_error", "job dispatch failed")
	} else {
		rl.logger.Info("dispatch", "dispatch_complete", "job dispatch completed")
	}

	rl.logger.LogEventWithDetails(diagnostics.LevelInfo, "dispatch", "dispatch_result", "job dispatch result", details)
}

// logCancellation logs cancellation processing
func (rl *runnerLogger) logCancellation(count int) {
	if rl == nil {
		return
	}

	details := map[string]interface{}{
		"cancellations_processed": count,
	}

	rl.logger.LogEventWithDetails(diagnostics.LevelInfo, "cancellation", "cancellation_complete", "cancellation processing completed", details)
}

// logWorkspaceCleanup logs workspace cleanup results
func (rl *runnerLogger) logWorkspaceCleanup(results []workspace.CleanupResult) {
	if rl == nil {
		return
	}

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

	details := map[string]interface{}{
		"removed": removed,
		"kept":     kept,
		"failed":   failed,
	}

	rl.logger.LogEventWithDetails(diagnostics.LevelInfo, "cleanup", "cleanup_complete", "workspace cleanup completed", details)
}

// logError logs an error event
func (rl *runnerLogger) logError(component, message string) {
	if rl == nil {
		return
	}

	rl.logger.Error(component, "error", message)
}

// logWarn logs a warning event
func (rl *runnerLogger) logWarn(component, message string) {
	if rl == nil {
		return
	}

	rl.logger.Warn(component, "warning", message)
}

// logInfo logs an info event
func (rl *runnerLogger) logInfo(component, event, message string) {
	if rl == nil {
		return
	}

	rl.logger.Info(component, event, message)
}

// withJobID adds a job ID to the correlation context
func (rl *runnerLogger) withJobID(jobID string) {
	if rl == nil {
		return
	}

	rl.correlation.JobID = jobID
	rl.logger.WithCorrelation(rl.correlation)
}

// jobLogger returns a job-specific logger
func (rl *runnerLogger) jobLogger(jobID string) (*diagnostics.JobLogger, error) {
	if rl == nil {
		return nil, fmt.Errorf("runner logger not initialized")
	}

	jl, err := rl.logger.JobLogger(jobID)
	if err != nil {
		return nil, fmt.Errorf("create job logger: %w", err)
	}

	// Initialize with job ID
	if err := jl.Initialize(jobID); err != nil {
		return nil, fmt.Errorf("initialize job logger: %w", err)
	}

	// Update correlation with job ID
	rl.withJobID(jobID)

	return jl, nil
}

// config returns the diagnostics config
func (rl *runnerLogger) config() diagnostics.Config {
	if rl == nil || rl.logger == nil {
		return diagnostics.Config{}
	}
	return rl.logger.Config()
}
