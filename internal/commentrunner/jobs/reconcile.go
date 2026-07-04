package jobs

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/higress-group/issue-spec/internal/acpx"
	"github.com/higress-group/issue-spec/internal/commentrunner/state"
	"github.com/higress-group/issue-spec/internal/commentrunner/writeback"
	"github.com/higress-group/issue-spec/internal/workspace"
)

var ErrReconciliationUnsupported = errors.New("acpx turn reconciliation unsupported")

type TurnReconciler interface {
	ReconcileTurn(context.Context, acpx.TurnReconcileRequest) (acpx.TurnReconcileResult, error)
}

type MetadataRefresher interface {
	Refresh(context.Context, acpx.SessionRef) (acpx.Metadata, error)
}

type WorkspaceCleaner interface {
	Cleanup(context.Context, workspace.CleanupRequest) ([]workspace.CleanupResult, error)
}

type ReconcileResult struct {
	Reconciled       int                       `json:"reconciled"`
	Queued           int                       `json:"queued"`
	Running          int                       `json:"running"`
	Completed        int                       `json:"completed"`
	Failed           int                       `json:"failed"`
	Cancelled        int                       `json:"cancelled"`
	Interrupted      int                       `json:"interrupted"`
	Jobs             []ReconcileJob            `json:"jobs,omitempty"`
	WorkspaceCleanup []workspace.CleanupResult `json:"workspace_cleanup,omitempty"`
	Diagnostics      []string                  `json:"diagnostics,omitempty"`
}

type ReconcileJob struct {
	JobID           string                `json:"job_id"`
	PublicSessionID string                `json:"public_session_id,omitempty"`
	PreviousStatus  state.LifecycleStatus `json:"previous_status"`
	Status          state.LifecycleStatus `json:"status"`
	Action          string                `json:"action"`
	Diagnostic      string                `json:"diagnostic,omitempty"`
}

func (d *Dispatcher) Reconcile(ctx context.Context) (ReconcileResult, error) {
	if err := d.validateReconcile(); err != nil {
		return ReconcileResult{}, err
	}
	st, err := d.Store.Load(ctx)
	if err != nil {
		return ReconcileResult{}, err
	}
	var result ReconcileResult
	for _, job := range st.ListJobs() {
		switch {
		case job.Status == state.StatusQueued:
			result.Queued++
			result.Jobs = append(result.Jobs, ReconcileJob{
				JobID:           job.ID,
				PublicSessionID: job.PublicSessionID,
				PreviousStatus:  job.Status,
				Status:          job.Status,
				Action:          "left_queued",
			})
		case job.Status.NeedsReconciliation():
			item, err := d.reconcileJob(ctx, job)
			if err != nil {
				return result, err
			}
			result.Reconciled++
			result.add(item)
			if item.Diagnostic != "" {
				result.Diagnostics = append(result.Diagnostics, item.Diagnostic)
			}
		}
	}
	cleanup, diagnostics := d.cleanupExpiredWorkspaces(ctx)
	result.WorkspaceCleanup = cleanup
	result.Diagnostics = append(result.Diagnostics, diagnostics...)
	return result, nil
}

func (d *Dispatcher) CleanupWorkspaces(ctx context.Context) (ReconcileResult, error) {
	if d == nil {
		return ReconcileResult{}, fmt.Errorf("job dispatcher is required")
	}
	if d.Store == nil {
		return ReconcileResult{}, fmt.Errorf("job dispatcher state store is required")
	}
	cleanup, diagnostics := d.cleanupExpiredWorkspaces(ctx)
	return ReconcileResult{WorkspaceCleanup: cleanup, Diagnostics: diagnostics}, nil
}

func (d *Dispatcher) validateReconcile() error {
	if d == nil {
		return fmt.Errorf("job dispatcher is required")
	}
	if d.Store == nil {
		return fmt.Errorf("job dispatcher state store is required")
	}
	if d.Sandbox == nil {
		return fmt.Errorf("job dispatcher sandbox preparer is required")
	}
	if d.Acpx == nil {
		return fmt.Errorf("job dispatcher acpx factory is required")
	}
	if d.Writeback == nil {
		return fmt.Errorf("job dispatcher writeback service is required")
	}
	return nil
}

func (r *ReconcileResult) add(item ReconcileJob) {
	r.Jobs = append(r.Jobs, item)
	switch item.Status {
	case state.StatusRunning:
		r.Running++
	case state.StatusCompleted:
		r.Completed++
	case state.StatusFailed:
		r.Failed++
	case state.StatusCancelled:
		r.Cancelled++
	case state.StatusInterrupted:
		r.Interrupted++
	}
}

func (d *Dispatcher) cleanupExpiredWorkspaces(ctx context.Context) ([]workspace.CleanupResult, []string) {
	cleaner, ok := d.Workspaces.(WorkspaceCleaner)
	if !ok || cleaner == nil {
		return nil, nil
	}
	st, err := d.Store.Load(ctx)
	if err != nil {
		return nil, []string{"workspace cleanup state load: " + safeError(err)}
	}
	workspaces, activeIDs := cleanupWorkspacesFromState(st)
	if len(workspaces) == 0 {
		return nil, nil
	}
	results, err := cleaner.Cleanup(ctx, workspace.CleanupRequest{
		Workspaces: workspaces,
		ActiveIDs:  activeIDs,
	})
	diagnostics := workspaceCleanupDiagnostics(results, err)
	removedIDs := removedWorkspaceIDs(results)
	if len(removedIDs) > 0 {
		if err := d.removeCleanedWorkspaces(ctx, removedIDs); err != nil {
			diagnostics = append(diagnostics, "workspace cleanup state update: "+safeError(err))
		}
	}
	return results, diagnostics
}

func cleanupWorkspacesFromState(st state.RunnerState) ([]state.WorkspaceMetadata, map[string]bool) {
	st.Normalize()
	byID := map[string]state.WorkspaceMetadata{}
	addWorkspace := func(workspace state.WorkspaceMetadata) {
		id := strings.TrimSpace(workspace.ID)
		if id == "" || strings.TrimSpace(workspace.Path) == "" {
			return
		}
		workspace.ID = id
		if _, exists := byID[id]; !exists {
			byID[id] = workspace
		}
	}
	protect := func(activeIDs map[string]bool, workspace state.WorkspaceMetadata) {
		id := strings.TrimSpace(workspace.ID)
		if id != "" {
			activeIDs[id] = true
		}
	}

	for _, workspace := range st.Workspaces {
		addWorkspace(workspace)
	}
	for _, job := range st.Jobs {
		addWorkspace(job.Workspace)
	}
	for _, session := range st.PublicSessions {
		addWorkspace(session.Workspace)
	}

	activeIDs := map[string]bool{}
	for _, job := range st.Jobs {
		switch job.Status {
		case state.StatusQueued, state.StatusDispatched, state.StatusRunning, state.StatusInterrupted:
			protect(activeIDs, job.Workspace)
			if session, ok := st.GetPublicSession(job.Repo, job.PublicSessionID); ok {
				protect(activeIDs, session.Workspace)
			}
		}
	}
	for _, session := range st.PublicSessions {
		if !session.Status.Terminal() || strings.TrimSpace(session.Lock.OwnerJobID) != "" || len(session.Queue.PendingJobIDs) > 0 {
			protect(activeIDs, session.Workspace)
		}
	}

	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	workspaces := make([]state.WorkspaceMetadata, 0, len(ids))
	for _, id := range ids {
		workspaces = append(workspaces, byID[id])
	}
	return workspaces, activeIDs
}

func workspaceCleanupDiagnostics(results []workspace.CleanupResult, err error) []string {
	var diagnostics []string
	for _, result := range results {
		if result.Action == "failed" || result.Action == "rejected" {
			diagnostics = append(diagnostics, fmt.Sprintf("workspace cleanup %s %s: %s", result.Action, result.WorkspaceID, result.Reason))
		}
	}
	if err != nil && len(diagnostics) == 0 {
		diagnostics = append(diagnostics, "workspace cleanup: "+safeError(err))
	}
	return diagnostics
}

func removedWorkspaceIDs(results []workspace.CleanupResult) map[string]bool {
	removed := map[string]bool{}
	for _, result := range results {
		if result.Removed && strings.TrimSpace(result.WorkspaceID) != "" {
			removed[strings.TrimSpace(result.WorkspaceID)] = true
		}
	}
	if len(removed) == 0 {
		return nil
	}
	return removed
}

func (d *Dispatcher) removeCleanedWorkspaces(ctx context.Context, removedIDs map[string]bool) error {
	if len(removedIDs) == 0 {
		return nil
	}
	return d.Store.Update(ctx, func(st *state.RunnerState) error {
		st.Normalize()
		for id := range removedIDs {
			delete(st.Workspaces, id)
		}
		return nil
	})
}

func (d *Dispatcher) reconcileJob(ctx context.Context, job state.Job) (ReconcileJob, error) {
	previous := job.Status
	ref, diagnostic, ok := sessionRefForJob(job)
	if !ok {
		return d.interrupt(ctx, job, previous, diagnostic)
	}
	coordinator, err := d.coordinatorForStoredJob(ctx, job)
	if err != nil {
		return d.interrupt(ctx, job, previous, "restart reconciliation setup: "+safeError(err))
	}
	if reconciler, ok := coordinator.(TurnReconciler); ok {
		reconciled, err := reconciler.ReconcileTurn(ctx, acpx.TurnReconcileRequest{
			PublicSessionID:      ref.PublicSessionID,
			StableRecordID:       ref.StableRecordID,
			TurnCorrelationToken: job.DispatchIntent.TurnCorrelationToken,
			LastTurnID:           job.Acpx.LastTurnID,
		})
		if err != nil {
			return d.interrupt(ctx, job, previous, "restart reconciliation query: "+safeError(err))
		}
		return d.applyReconcile(ctx, job, previous, reconciled)
	}
	if refresher, ok := coordinator.(MetadataRefresher); ok {
		meta, err := refresher.Refresh(ctx, ref)
		if err != nil {
			return d.interrupt(ctx, job, previous, "restart metadata refresh: "+safeError(err))
		}
		return d.recoveredRunning(ctx, job, previous, meta, "active turn refreshed; terminal output unavailable")
	}
	return d.interrupt(ctx, job, previous, ErrReconciliationUnsupported.Error())
}

func (d *Dispatcher) coordinatorForStoredJob(ctx context.Context, job state.Job) (Coordinator, error) {
	if strings.TrimSpace(job.Workspace.Path) == "" {
		return nil, fmt.Errorf("job %s is missing workspace path", job.ID)
	}
	env, err := d.Sandbox.Prepare(ctx, SandboxRequest{
		WorkspacePath:        job.Workspace.Path,
		AcpxWorkingDirectory: job.Workspace.Path,
		AcpxBinary:           firstNonEmpty(d.AcpxBinary, acpx.DefaultBinary),
		ExtraEnv:             d.CoordinatorExtraEnv,
	})
	if err != nil {
		return nil, err
	}
	return d.Acpx.NewCoordinator(env)
}

func (d *Dispatcher) applyReconcile(ctx context.Context, job state.Job, previous state.LifecycleStatus, reconciled acpx.TurnReconcileResult) (ReconcileJob, error) {
	diagnostic := strings.TrimSpace(reconciled.Diagnostics)
	status := state.LifecycleStatus(reconciled.Status)
	if reconciled.Ambiguous || status == state.StatusInterrupted {
		if diagnostic == "" {
			diagnostic = "turn state is ambiguous after runner restart"
		}
		return d.interrupt(ctx, job, previous, diagnostic)
	}
	switch status {
	case state.StatusRunning, state.StatusDispatched:
		return d.recoveredRunning(ctx, job, previous, reconciled.Metadata, diagnostic)
	case state.StatusCompleted, state.StatusFailed, state.StatusCancelled:
		return d.recoveredTerminal(ctx, job, previous, status, reconciled, diagnostic)
	default:
		if diagnostic == "" {
			diagnostic = fmt.Sprintf("invalid reconciled status %q", reconciled.Status)
		}
		return d.interrupt(ctx, job, previous, diagnostic)
	}
}

func (d *Dispatcher) recoveredRunning(ctx context.Context, job state.Job, previous state.LifecycleStatus, meta acpx.Metadata, diagnostic string) (ReconcileJob, error) {
	now := d.now()
	var running state.Job
	if err := d.Store.Update(ctx, func(st *state.RunnerState) error {
		st.Normalize()
		next, err := st.UpdateJobStatus(job.ID, state.StatusRunning, now)
		if err != nil {
			return err
		}
		next.Restart = state.RestartMetadata{
			ReconciledAt:    now,
			RecoveredStatus: state.StatusRunning,
			Diagnostics:     safeString(diagnostic, 1024),
		}
		fillJobSessionRefs(&next)
		mergeAcpxMetadata(&next, meta, now)
		if err := upsertWorkspaceIfPresent(st, next.Workspace); err != nil {
			return err
		}
		if err := upsertSessionForReconciledJob(st, next, state.StatusRunning, now, false); err != nil {
			return err
		}
		running = next
		return st.UpsertJob(next)
	}); err != nil {
		return ReconcileJob{}, err
	}
	if _, err := d.Writeback.Write(ctx, writeback.Request{Job: running, Status: state.StatusRunning, Phase: "reconciled-running", Diagnostics: splitDiagnostic(diagnostic)}); err != nil {
		return ReconcileJob{}, err
	}
	return ReconcileJob{JobID: job.ID, PublicSessionID: running.PublicSessionID, PreviousStatus: previous, Status: state.StatusRunning, Action: "running", Diagnostic: diagnostic}, nil
}

func (d *Dispatcher) recoveredTerminal(ctx context.Context, job state.Job, previous, terminal state.LifecycleStatus, reconciled acpx.TurnReconcileResult, diagnostic string) (ReconcileJob, error) {
	now := d.now()
	var final state.Job
	lock := d.storedLock(ctx, job)
	if err := d.Store.Update(ctx, func(st *state.RunnerState) error {
		st.Normalize()
		next, err := st.UpdateJobStatus(job.ID, terminal, now, splitDiagnostic(diagnostic)...)
		if err != nil {
			return err
		}
		fillJobSessionRefs(&next)
		next.Restart = state.RestartMetadata{
			ReconciledAt:    now,
			RecoveredStatus: terminal,
			Diagnostics:     safeString(diagnostic, 1024),
		}
		mergeAcpxMetadata(&next, reconciled.Metadata, now)
		if reconciled.Output.SummaryFound {
			next.CoordinatorSummary = summaryJSON(reconciled.Output.Summary)
			next.CLIDirect = cliDirect(reconciled.Output.Summary)
		}
		next.Workspace.LastUsedAt = now
		if err := upsertWorkspaceIfPresent(st, next.Workspace); err != nil {
			return err
		}
		if err := upsertSessionForReconciledJob(st, next, terminal, now, false); err != nil {
			return err
		}
		final = next
		return st.UpsertJob(next)
	}); err != nil {
		return ReconcileJob{}, err
	}
	d.releaseLock(ctx, final.ID, lock)
	req := writeback.Request{
		Job:                  final,
		Status:               terminal,
		Phase:                "reconciled-" + string(terminal),
		CoordinatorReplyBody: reconciled.Output.ReplyText,
		Diagnostics:          splitDiagnostic(diagnostic),
	}
	if reconciled.Output.SummaryFound {
		req.CoordinatorSummary = &reconciled.Output.Summary
	}
	if terminal == state.StatusFailed && diagnostic != "" {
		req.Err = errors.New(diagnostic)
	}
	if _, err := d.Writeback.Write(ctx, req); err != nil {
		return ReconcileJob{}, err
	}
	return ReconcileJob{JobID: job.ID, PublicSessionID: final.PublicSessionID, PreviousStatus: previous, Status: terminal, Action: string(terminal), Diagnostic: diagnostic}, nil
}

func (d *Dispatcher) interrupt(ctx context.Context, job state.Job, previous state.LifecycleStatus, diagnostic string) (ReconcileJob, error) {
	now := d.now()
	var interrupted state.Job
	lock := d.storedLock(ctx, job)
	if err := d.Store.Update(ctx, func(st *state.RunnerState) error {
		st.Normalize()
		next, err := st.UpdateJobStatus(job.ID, state.StatusInterrupted, now, safeString(diagnostic, 1024))
		if err != nil {
			return err
		}
		fillJobSessionRefs(&next)
		next.Restart = state.RestartMetadata{
			ReconciledAt:         now,
			RecoveredStatus:      state.StatusInterrupted,
			Ambiguous:            true,
			WorkspaceMarkedDirty: true,
			Diagnostics:          safeString(diagnostic, 1024),
		}
		markWorkspaceUncertain(&next.Workspace)
		if err := upsertWorkspaceIfPresent(st, next.Workspace); err != nil {
			return err
		}
		if err := upsertSessionForReconciledJob(st, next, state.StatusInterrupted, now, true); err != nil {
			return err
		}
		interrupted = next
		return st.UpsertJob(next)
	}); err != nil {
		return ReconcileJob{}, err
	}
	d.releaseLock(ctx, interrupted.ID, lock)
	if _, err := d.Writeback.Write(ctx, writeback.Request{
		Job:         interrupted,
		Status:      state.StatusInterrupted,
		Phase:       "restart-reconcile-interrupted",
		Diagnostics: splitDiagnostic(diagnostic),
		Err:         errors.New(firstNonEmpty(diagnostic, "turn state is ambiguous after runner restart")),
	}); err != nil {
		return ReconcileJob{}, err
	}
	return ReconcileJob{JobID: job.ID, PublicSessionID: interrupted.PublicSessionID, PreviousStatus: previous, Status: state.StatusInterrupted, Action: "interrupted", Diagnostic: diagnostic}, nil
}

func sessionRefForJob(job state.Job) (acpx.SessionRef, string, bool) {
	publicID := strings.TrimSpace(firstNonEmpty(job.PublicSessionID, job.DispatchIntent.PublicSessionID))
	recordID := strings.TrimSpace(firstNonEmpty(job.AcpxRecordID, job.DispatchIntent.AcpxRecordID, job.Acpx.StableRecordID))
	switch {
	case publicID == "":
		return acpx.SessionRef{}, "job is missing public session id", false
	case recordID == "":
		return acpx.SessionRef{}, "job is missing stable acpx record id", false
	default:
		return acpx.SessionRef{PublicSessionID: publicID, StableRecordID: recordID}, "", true
	}
}

func mergeAcpxMetadata(job *state.Job, meta acpx.Metadata, at time.Time) {
	if strings.TrimSpace(meta.StableRecordID) == "" {
		return
	}
	merged := acpxMetadata(meta, at)
	job.AcpxRecordID = merged.StableRecordID
	job.Acpx = merged
	job.DispatchIntent.AcpxRecordID = merged.StableRecordID
}

func fillJobSessionRefs(job *state.Job) {
	if job == nil {
		return
	}
	job.PublicSessionID = firstNonEmpty(job.PublicSessionID, job.DispatchIntent.PublicSessionID)
	job.AcpxRecordID = firstNonEmpty(job.AcpxRecordID, job.DispatchIntent.AcpxRecordID, job.Acpx.StableRecordID)
	if job.DispatchIntent.PublicSessionID == "" {
		job.DispatchIntent.PublicSessionID = job.PublicSessionID
	}
	if job.DispatchIntent.AcpxRecordID == "" {
		job.DispatchIntent.AcpxRecordID = job.AcpxRecordID
	}
}

func upsertWorkspaceIfPresent(st *state.RunnerState, workspace state.WorkspaceMetadata) error {
	if strings.TrimSpace(workspace.ID) == "" || strings.TrimSpace(workspace.Path) == "" || strings.TrimSpace(workspace.Repo) == "" {
		return nil
	}
	return st.UpsertWorkspace(workspace)
}

func upsertSessionForReconciledJob(st *state.RunnerState, job state.Job, status state.LifecycleStatus, at time.Time, markUncertain bool) error {
	if strings.TrimSpace(job.PublicSessionID) == "" || strings.TrimSpace(job.AcpxRecordID) == "" {
		return nil
	}
	session, _ := st.GetPublicSession(job.Repo, job.PublicSessionID)
	if session.Repo == "" {
		session.Repo = job.Repo
	}
	if session.PublicSessionID == "" {
		session.PublicSessionID = job.PublicSessionID
	}
	if session.IssueNumber == 0 {
		session.IssueNumber = job.IssueNumber
	}
	if session.CreatorLogin == "" {
		session.CreatorLogin = job.SessionCreatorLogin
	}
	if session.CreatedAt.IsZero() {
		session.CreatedAt = firstTime(job.CreatedAt, at)
	}
	session.AcpxRecordID = job.AcpxRecordID
	session.Acpx = job.Acpx
	session.Status = status
	session.LastUsedAt = at
	session.LastJobID = job.ID
	session.Workspace = job.Workspace
	if markUncertain {
		markWorkspaceUncertain(&session.Workspace)
	}
	if status.Terminal() {
		session.Lock = state.SessionLock{}
		session.Queue.PendingJobIDs = removeString(session.Queue.PendingJobIDs, job.ID)
	} else {
		session.Queue.PendingJobIDs = appendUnique(session.Queue.PendingJobIDs, job.ID)
		if session.Queue.AcceptedSequence < job.DispatchIntent.TurnSequence {
			session.Queue.AcceptedSequence = job.DispatchIntent.TurnSequence
		}
	}
	return st.UpsertPublicSession(session)
}

func (d *Dispatcher) storedLock(ctx context.Context, job state.Job) state.SessionLock {
	if d.Workspaces == nil {
		return state.SessionLock{}
	}
	st, err := d.Store.Load(ctx)
	if err != nil {
		return state.SessionLock{}
	}
	session, ok := st.GetPublicSession(job.Repo, job.PublicSessionID)
	if !ok || strings.TrimSpace(session.Lock.OwnerJobID) == "" {
		return state.SessionLock{}
	}
	return session.Lock
}

func (d *Dispatcher) releaseLock(ctx context.Context, jobID string, lock state.SessionLock) {
	if d.Workspaces == nil || strings.TrimSpace(lock.OwnerJobID) == "" {
		return
	}
	if err := d.Workspaces.ReleaseLock(lock); err != nil {
		_ = d.appendDiagnostic(ctx, jobID, "workspace lock release: "+safeError(err))
	}
}

func markWorkspaceUncertain(workspace *state.WorkspaceMetadata) {
	if workspace == nil {
		return
	}
	workspace.Dirty = true
	workspace.Uncertain = true
}

func splitDiagnostic(diagnostic string) []string {
	diagnostic = strings.TrimSpace(diagnostic)
	if diagnostic == "" {
		return nil
	}
	return []string{safeString(diagnostic, 1024)}
}
