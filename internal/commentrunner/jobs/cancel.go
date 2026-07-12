package jobs

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/higress-group/issue-spec/internal/acpx"
	"github.com/higress-group/issue-spec/internal/commentrunner/state"
	"github.com/higress-group/issue-spec/internal/commentrunner/writeback"
	"github.com/higress-group/issue-spec/internal/processworkspace"
)

type TurnCanceller interface {
	Cancel(context.Context, acpx.SessionRef) (acpx.CancelResult, error)
}

var errProcessWorkspaceCleanupDeferred = errors.New("process workspace cleanup deferred until integration or retention")

// processWorkspaceCleanupPolicy is deliberately separate from CleanupAndRelease:
// cancellation must prove that releasing change-bearing work is allowed before
// it asks the allocator to mutate either the local lease or portable
// association. Implementations fail closed when that proof is unavailable.
type processWorkspaceCleanupPolicy interface {
	AllowProcessWorkspaceCleanup(context.Context, state.ProcessWorkspaceAssignment, time.Time) (bool, error)
}

// AllowProcessWorkspaceCleanup derives the cancellation cleanup decision from
// the exact durable reservation and its machine-local lease. IntegrationSHA is
// durable evidence that change-bearing work reached the integration checkout;
// an elapsed explicit retention deadline is the only alternative release path.
func (a *ManagerAllocator) AllowProcessWorkspaceCleanup(ctx context.Context, assignment state.ProcessWorkspaceAssignment, now time.Time) (bool, error) {
	if a == nil || a.State == nil {
		return false, errors.New("process workspace state store is required")
	}
	associations, err := a.State.LoadProcessWorkspaces(ctx)
	if err != nil {
		return false, err
	}
	association, ok := associations.Get(assignment.WorkspaceID)
	if !ok || association.ReservationID != assignment.ReservationID || association.ReservationIdentity != assignment.ReservationIdentity || association.ProcessID != assignment.ProcessID {
		return false, errors.New("process workspace cleanup reservation does not match durable assignment")
	}
	if association.Lifecycle == state.ProcessWorkspaceReleased || association.ExecutionClass != processworkspace.ExecutionChangeBearing {
		return true, nil
	}
	if a.Manager == nil {
		return false, errors.New("process workspace manager is required for change-bearing cleanup policy")
	}
	inspection, err := a.Manager.Inspect(ctx, association.WorkspaceID)
	if err != nil {
		return false, err
	}
	lease := inspection.Lease.Portable
	if err := lease.Validate(); err != nil {
		return false, fmt.Errorf("invalid local process workspace lease: %w", err)
	}
	if lease.WorkspaceID != association.WorkspaceID || lease.ProcessID != association.ProcessID ||
		lease.Repository != association.Repository || lease.ExecutionClass != association.ExecutionClass ||
		lease.Mode != association.Mode || lease.BaseSHA != association.BaseSHA || lease.Branch != association.Branch ||
		lease.IntegrationOwner != association.IntegrationOwner || lease.RuntimeNamespace != association.RuntimeNamespace ||
		!reflect.DeepEqual(lease.WriteOwnership, association.WriteOwnership) ||
		!reflect.DeepEqual(lease.SharedTouchpoints, association.SharedTouchpoints) ||
		!reflect.DeepEqual(lease.RuntimeResources, association.RuntimeResources) {
		return false, errors.New("local process workspace does not match the durable cleanup reservation")
	}
	if inspection.Dirty || len(inspection.Problems) != 0 ||
		(lease.State == processworkspace.StateCleaned && (inspection.Registered || inspection.Present)) ||
		(lease.State != processworkspace.StateCleaned && (!inspection.Registered || !inspection.Present)) {
		return false, errors.New("local process workspace does not have cleanup-safe physical state")
	}
	if strings.TrimSpace(lease.IntegrationSHA) != "" {
		return true, nil
	}
	return !lease.RetentionExpiresAt.IsZero() && !now.Before(lease.RetentionExpiresAt), nil
}

// defaultCancellationDrainBudget bounds how many queued cancellations a single
// poll cycle will process out-of-band before yielding back to the caller.
const defaultCancellationDrainBudget = 32

// DrainCancellations processes ONLY queued cancellations, one at a time, until
// none remain or the per-cycle budget is exhausted. It never falls through to
// job dispatch, so a blocked in-flight dispatch cannot starve cancellations.
func (d *Dispatcher) DrainCancellations(ctx context.Context, budget int) (Result, error) {
	if err := d.validate(); err != nil {
		return Result{}, err
	}
	if budget <= 0 {
		budget = defaultCancellationDrainBudget
	}
	aggregate := Result{}
	for processed := 0; processed < budget; processed++ {
		if err := ctx.Err(); err != nil {
			return aggregate, err
		}
		cancel, ok, err := d.nextQueuedCancellation(ctx)
		if err != nil {
			if aggregate.Error == "" {
				aggregate.Error = safeError(err)
			}
			return aggregate, err
		}
		if !ok {
			break
		}
		result, cancelErr := d.cancel(ctx, cancel)
		child := result
		child.Results = nil
		aggregate.Results = append(aggregate.Results, child)
		if result.Executed {
			aggregate.Executed = true
			aggregate.ExecutedCount++
		}
		if aggregate.CancellationID == "" && result.CancellationID != "" {
			aggregate.CancellationID = result.CancellationID
			aggregate.JobID = result.JobID
			aggregate.Status = result.Status
		}
		if cancelErr != nil {
			if aggregate.Error == "" {
				aggregate.Error = safeError(cancelErr)
			}
			return aggregate, cancelErr
		}
	}
	if !aggregate.Executed && aggregate.Reason == "" {
		aggregate.Reason = "no queued cancellations"
	}
	return aggregate, nil
}

func (d *Dispatcher) nextQueuedCancellation(ctx context.Context) (state.Cancellation, bool, error) {
	st, err := d.Store.Load(ctx)
	if err != nil {
		return state.Cancellation{}, false, err
	}
	st.Normalize()
	cancellations := make([]state.Cancellation, 0, len(st.Cancellations))
	for _, cancel := range st.Cancellations {
		if cancel.Status == state.StatusQueued {
			cancellations = append(cancellations, cancel)
		}
	}
	sort.Slice(cancellations, func(i, j int) bool {
		if cancellations[i].CreatedAt.Equal(cancellations[j].CreatedAt) {
			return cancellations[i].ID < cancellations[j].ID
		}
		return cancellations[i].CreatedAt.Before(cancellations[j].CreatedAt)
	})
	if len(cancellations) == 0 {
		return state.Cancellation{}, false, nil
	}
	return cancellations[0], true, nil
}

func (d *Dispatcher) cancel(ctx context.Context, cancel state.Cancellation) (Result, error) {
	if cancel.Status.Terminal() {
		result := Result{CancellationID: cancel.ID, Status: cancel.Status, Reason: "already_terminal"}
		if strings.TrimSpace(cancel.TargetJobID) != "" {
			if job, err := d.loadJob(ctx, cancel.TargetJobID); err == nil {
				if _, cleanupErr := d.cleanupTerminalProcessWorkspace(ctx, job); cleanupErr != nil {
					result.Error = safeError(cleanupErr)
					return result, cleanupErr
				}
			}
		}
		return result, nil
	}
	job, found, terminal, err := d.markCancellationRunning(ctx, cancel)
	if err != nil {
		return Result{Executed: true, CancellationID: cancel.ID, Status: state.StatusFailed, Error: safeError(err)}, err
	}
	if !found {
		return d.cancelRejected(ctx, cancel, "unknown_session", "cancellation target public session or active job was not found")
	}
	if terminal {
		return d.cancelAlreadyTerminal(ctx, cancel, job)
	}
	if job.Status == state.StatusQueued {
		if err := d.cancelQueuedJob(ctx, cancel, job); err != nil {
			return Result{Executed: true, JobID: job.ID, CancellationID: cancel.ID, Status: state.StatusFailed, Error: safeError(err)}, err
		}
		return Result{Executed: true, JobID: job.ID, CancellationID: cancel.ID, Status: state.StatusCancelled}, nil
	}

	coordinator, err := d.coordinatorForStoredJob(ctx, job)
	if err != nil {
		return d.cancelFailed(ctx, cancel, job, "cancel setup: "+safeError(err), err)
	}
	canceller, ok := coordinator.(TurnCanceller)
	if !ok {
		return d.cancelFailed(ctx, cancel, job, acpx.ErrUnsupportedCancel.Error(), nil)
	}
	ref, diagnostic, ok := cancelSessionRefForJob(job)
	if !ok {
		return d.cancelFailed(ctx, cancel, job, diagnostic, nil)
	}
	cancelResult, err := canceller.Cancel(ctx, ref)
	if err != nil || cancelResult.Unsupported || !cancelResult.Confirmed {
		diagnostic := firstNonEmpty(cancelResult.Diagnostics, safeError(err), "acpx cancellation was not confirmed")
		cause := err
		if cancelResult.Unsupported {
			cause = acpx.ErrUnsupportedCancel
		}
		return d.cancelFailed(ctx, cancel, job, diagnostic, cause)
	}
	if err := d.cancelConfirmed(ctx, cancel, job, cancelResult.Diagnostics); err != nil {
		return Result{Executed: true, JobID: job.ID, CancellationID: cancel.ID, Status: state.StatusFailed, Error: safeError(err)}, err
	}
	return Result{Executed: true, JobID: job.ID, CancellationID: cancel.ID, Status: state.StatusCancelled}, nil
}

func (d *Dispatcher) markCancellationRunning(ctx context.Context, cancel state.Cancellation) (state.Job, bool, bool, error) {
	now := d.now()
	var target state.Job
	var found bool
	var terminal bool
	err := d.Store.Update(ctx, func(st *state.RunnerState) error {
		st.Normalize()
		current, ok := st.Cancellations[cancel.ID]
		if !ok {
			return fmt.Errorf("cancellation %q not found", cancel.ID)
		}
		if current.Status.Terminal() {
			cancel = current
			terminal = true
			return nil
		}
		target, found, terminal = findCancellationTarget(st, current)
		if !found {
			current.Status = state.StatusRejected
			current.Diagnostics = append(current.Diagnostics, "public session or active job was not found")
			return st.UpsertCancellation(current)
		}
		current.TargetJobID = target.ID
		if terminal {
			current.Status = state.StatusCancelled
			current.CancelledAt = now
			current.Diagnostics = append(current.Diagnostics, "target job is already terminal")
			return st.UpsertCancellation(current)
		}
		current.Status = state.StatusRunning
		return st.UpsertCancellation(current)
	})
	return target, found, terminal, err
}

func findCancellationTarget(st *state.RunnerState, cancel state.Cancellation) (state.Job, bool, bool) {
	session, sessionOK := st.GetPublicSession(cancel.Repo, cancel.TargetPublicSessionID)
	var terminalJob state.Job
	if cancel.TargetJobID != "" {
		if job, ok := st.Jobs[cancel.TargetJobID]; ok && job.Repo == cancel.Repo && job.PublicSessionID == cancel.TargetPublicSessionID {
			if job.Status == state.StatusDispatched || job.Status == state.StatusRunning || job.Status == state.StatusQueued {
				return job, true, false
			}
			if job.Status.Terminal() {
				terminalJob = job
			}
		}
	}
	if sessionOK && session.LastJobID != "" {
		if job, ok := st.Jobs[session.LastJobID]; ok && job.Repo == cancel.Repo && job.PublicSessionID == cancel.TargetPublicSessionID {
			if job.Status == state.StatusDispatched || job.Status == state.StatusRunning || job.Status == state.StatusQueued {
				return job, true, false
			}
			if job.Status.Terminal() {
				terminalJob = job
			}
		}
	}
	jobs := st.ListJobs()
	for _, status := range []state.LifecycleStatus{state.StatusRunning, state.StatusDispatched, state.StatusQueued} {
		for _, job := range jobs {
			if job.Repo == cancel.Repo && job.PublicSessionID == cancel.TargetPublicSessionID && job.Status == status {
				return job, true, false
			}
		}
	}
	for _, job := range jobs {
		if job.Repo == cancel.Repo && job.PublicSessionID == cancel.TargetPublicSessionID && job.Status.Terminal() {
			return job, true, true
		}
	}
	if terminalJob.ID != "" {
		return terminalJob, true, true
	}
	return state.Job{}, false, false
}

func (d *Dispatcher) cancelQueuedJob(ctx context.Context, cancel state.Cancellation, job state.Job) error {
	now := d.now()
	var cancelled state.Job
	if err := d.Store.Update(ctx, func(st *state.RunnerState) error {
		next, err := st.UpdateJobStatus(job.ID, state.StatusCancelled, now, "cancelled before acpx dispatch")
		if err != nil {
			return err
		}
		MarkTerminalWorkspaceCleanupRequired(&next)
		if session, ok := st.GetPublicSession(next.Repo, next.PublicSessionID); ok {
			session.Queue.PendingJobIDs = removeString(session.Queue.PendingJobIDs, next.ID)
			session.LastUsedAt = now
			_ = st.UpsertPublicSession(session)
		}
		current := st.Cancellations[cancel.ID]
		current.Status = state.StatusCancelled
		current.TargetJobID = next.ID
		current.CancelledAt = now
		current.AcpxResult = "not_dispatched"
		if err := st.UpsertCancellation(current); err != nil {
			return err
		}
		cancelled = next
		return st.UpsertJob(next)
	}); err != nil {
		return err
	}
	cleanupErr := d.cleanupAssignedProcessWorkspace(ctx, cancelled)
	_ = d.recordJobWorkspaceCleanupResult(ctx, cancelled.ID, cleanupErr)
	if cleanupErr != nil {
		_ = d.appendDiagnostic(ctx, cancelled.ID, "process workspace cleanup pending: "+safeError(cleanupErr))
	}
	_, err := d.Writeback.Write(ctx, writeback.Request{
		Job:                cancelled,
		Status:             state.StatusCancelled,
		Phase:              "cancelled-before-dispatch",
		CancelingUserLogin: cancel.CancelingUserLogin,
	})
	return errors.Join(err, d.cleanupTerminalCredentials(ctx, cancelled))
}

func (d *Dispatcher) cancelConfirmed(ctx context.Context, cancel state.Cancellation, job state.Job, diagnostics string) error {
	now := d.now()
	var cancelled state.Job
	lock := d.storedLock(ctx, job)
	if err := d.Store.Update(ctx, func(st *state.RunnerState) error {
		next, err := st.UpdateJobStatus(job.ID, state.StatusCancelled, now, splitDiagnostic(diagnostics)...)
		if err != nil {
			return err
		}
		fillJobSessionRefs(&next)
		MarkTerminalWorkspaceCleanupRequired(&next)
		markWorkspaceUncertain(&next.Workspace)
		if err := upsertWorkspaceIfPresent(st, next.Workspace); err != nil {
			return err
		}
		if err := upsertSessionForReconciledJob(st, next, state.StatusCancelled, now, true); err != nil {
			return err
		}
		current := st.Cancellations[cancel.ID]
		current.Status = state.StatusCancelled
		current.TargetJobID = next.ID
		current.AcpxResult = "confirmed"
		current.CancelledAt = now
		current.DirtyWorkspace = true
		current.WorkspaceUncertain = true
		current.Diagnostics = append(current.Diagnostics, splitDiagnostic(diagnostics)...)
		if err := st.UpsertCancellation(current); err != nil {
			return err
		}
		cancelled = next
		return st.UpsertJob(next)
	}); err != nil {
		return err
	}
	d.releaseLock(ctx, cancelled.ID, lock)
	cleanupErr := d.cleanupAssignedProcessWorkspace(ctx, cancelled)
	_ = d.recordJobWorkspaceCleanupResult(ctx, cancelled.ID, cleanupErr)
	if cleanupErr != nil {
		_ = d.appendDiagnostic(ctx, cancelled.ID, "process workspace cleanup pending: "+safeError(cleanupErr))
	}
	_, err := d.Writeback.Write(ctx, writeback.Request{
		Job:                cancelled,
		Status:             state.StatusCancelled,
		Phase:              "cancelled",
		Diagnostics:        splitDiagnostic(diagnostics),
		CancelingUserLogin: cancel.CancelingUserLogin,
	})
	return errors.Join(err, d.cleanupTerminalCredentials(ctx, cancelled))
}

// cancelSessionRefForJob resolves the acpx session reference used to cancel an
// active turn. Unlike sessionRefForJob (used by reconciliation, which must have a
// stable acpx record id), cancellation only needs the public session id because
// the acpx cancel subprocess targets the session by name. This lets an in-flight
// /new job whose record id is not yet persisted still be cancelled.
func cancelSessionRefForJob(job state.Job) (acpx.SessionRef, string, bool) {
	publicID := strings.TrimSpace(firstNonEmpty(job.PublicSessionID, job.DispatchIntent.PublicSessionID))
	if publicID == "" {
		return acpx.SessionRef{}, "job is missing public session id", false
	}
	recordID := strings.TrimSpace(firstNonEmpty(job.AcpxRecordID, job.DispatchIntent.AcpxRecordID, job.Acpx.StableRecordID))
	return acpx.SessionRef{PublicSessionID: publicID, StableRecordID: recordID}, "", true
}

// cancelRejected records a rejected cancellation and posts a best-effort terminal
// status comment so an accepted /cancel that cannot find its target still surfaces
// visible feedback rather than silently disappearing.
func (d *Dispatcher) cancelRejected(ctx context.Context, cancel state.Cancellation, reason, diagnostic string) (Result, error) {
	if d.Writeback != nil {
		if job, ok := cancellationStatusJob(cancel); ok {
			_, _ = d.Writeback.Write(ctx, writeback.Request{
				Job:                job,
				Status:             state.StatusRejected,
				Phase:              "cancel-rejected",
				Diagnostics:        splitDiagnostic(diagnostic),
				CancelingUserLogin: cancel.CancelingUserLogin,
			})
		}
	}
	return Result{Executed: true, CancellationID: cancel.ID, Status: state.StatusRejected, Reason: reason}, nil
}

// cancelAlreadyTerminal records the terminal status comment for an accepted
// cancellation whose target job was already terminal. Like cancelRejected it
// posts a best-effort status comment through the synthetic cancellation job so
// every accepted cancellation reaches a visible terminal status (SPEC-004),
// without touching the target job's own completed/failed status comment.
func (d *Dispatcher) cancelAlreadyTerminal(ctx context.Context, cancel state.Cancellation, job state.Job) (Result, error) {
	refreshed, err := d.cleanupTerminalProcessWorkspace(ctx, job)
	if err != nil {
		return Result{Executed: true, JobID: job.ID, CancellationID: cancel.ID, Status: state.StatusFailed, Error: safeError(err)}, err
	}
	job = refreshed
	if d.Writeback != nil {
		if statusJob, ok := cancellationStatusJob(cancel); ok {
			_, _ = d.Writeback.Write(ctx, writeback.Request{
				Job:                statusJob,
				Status:             state.StatusCancelled,
				Phase:              "cancelled",
				Diagnostics:        splitDiagnostic("target job was already terminal"),
				CancelingUserLogin: cancel.CancelingUserLogin,
			})
		}
	}
	result := Result{Executed: true, JobID: job.ID, CancellationID: cancel.ID, Status: state.StatusCancelled, Reason: "target_already_terminal"}
	if err := d.cleanupTerminalCredentials(ctx, job); err != nil {
		result.Error = safeError(err)
		return result, err
	}
	return result, nil
}

// cleanupTerminalProcessWorkspace enforces the durable ordering shared by
// repeated cancellation and cancellation of an already-terminal target. The
// intent transaction and reload must finish before physical cleanup; cleanup's
// outcome must then be durably recorded before the path reports success.
func (d *Dispatcher) cleanupTerminalProcessWorkspace(ctx context.Context, job state.Job) (state.Job, error) {
	if err := d.markJobWorkspaceCleanupRequired(ctx, job.ID); err != nil {
		return job, err
	}
	refreshed, err := d.loadJob(ctx, job.ID)
	if err != nil {
		return job, err
	}
	cleanupErr := d.cleanupAssignedProcessWorkspace(ctx, refreshed)
	if recordErr := d.recordJobWorkspaceCleanupResult(ctx, refreshed.ID, cleanupErr); recordErr != nil {
		return refreshed, errors.Join(recordErr, cleanupErr)
	}
	if cleanupErr != nil {
		_ = d.appendDiagnostic(ctx, refreshed.ID, "process workspace cleanup pending: "+safeError(cleanupErr))
	}
	return refreshed, nil
}

func (d *Dispatcher) cleanupAssignedProcessWorkspace(ctx context.Context, job state.Job) error {
	assignment := job.ProcessWorkspace
	if assignment == nil {
		return nil
	}
	if assignment.CleanupState == state.ProcessWorkspaceAssignmentCleanupConfirmed && !assignment.CleanupRequired {
		return nil
	}
	if err := assignment.Validate(); err != nil {
		return fmt.Errorf("invalid durable process workspace assignment: %w", err)
	}
	provider, ok := d.Workspaces.(ProcessWorkspaceAllocatorProvider)
	if !ok {
		return errors.New("process workspace allocator provider is unavailable")
	}
	allocator, err := provider.ProcessWorkspaceAllocator(ctx, ProcessWorkspaceAllocatorRequest{IntegrationRoot: job.Workspace.Path})
	if err != nil {
		return err
	}
	policy, ok := allocator.(processWorkspaceCleanupPolicy)
	if !ok {
		return errors.New("process workspace cleanup policy is unavailable")
	}
	allowed, err := policy.AllowProcessWorkspaceCleanup(ctx, *assignment, d.now())
	if err != nil {
		return err
	}
	if !allowed {
		return errProcessWorkspaceCleanupDeferred
	}
	_, err = allocator.CleanupAndRelease(ctx, assignment.WorkspaceID, assignment.ReservationID)
	return err
}

func (d *Dispatcher) markJobWorkspaceCleanupRequired(ctx context.Context, jobID string) error {
	return d.Store.Update(ctx, func(st *state.RunnerState) error {
		job, ok := st.Jobs[jobID]
		if !ok {
			return fmt.Errorf("job %q not found", jobID)
		}
		MarkTerminalWorkspaceCleanupRequired(&job)
		return st.UpsertJob(job)
	})
}

func (d *Dispatcher) recordJobWorkspaceCleanupResult(ctx context.Context, jobID string, cleanupErr error) error {
	return d.Store.Update(ctx, func(st *state.RunnerState) error {
		job, ok := st.Jobs[jobID]
		if !ok {
			return fmt.Errorf("job %q not found", jobID)
		}
		if job.ProcessWorkspace == nil {
			return nil
		}
		if cleanupErr != nil {
			job.ProcessWorkspace.CleanupRequired = true
			job.ProcessWorkspace.CleanupState = state.ProcessWorkspaceAssignmentCleanupPending
			job.ProcessWorkspace.LastError = "cleanup_failed"
		} else {
			job.ProcessWorkspace.CleanupRequired = false
			job.ProcessWorkspace.CleanupState = state.ProcessWorkspaceAssignmentCleanupConfirmed
			job.ProcessWorkspace.LastError = ""
		}
		return st.UpsertJob(job)
	})
}

// cancellationStatusJob synthesizes the minimal job needed to render a status
// comment for a cancellation that has no resolvable target job. No phantom job
// record is created; the synthetic writeback record is pruned by retention.
func cancellationStatusJob(cancel state.Cancellation) (state.Job, bool) {
	if strings.TrimSpace(cancel.Repo) == "" || cancel.IssueNumber <= 0 || strings.TrimSpace(cancel.ID) == "" {
		return state.Job{}, false
	}
	return state.Job{
		ID:                  cancel.ID,
		Repo:                cancel.Repo,
		IssueNumber:         cancel.IssueNumber,
		PublicSessionID:     cancel.TargetPublicSessionID,
		TriggerCommentID:    cancel.TriggerCommentID,
		TriggeringUserLogin: cancel.CancelingUserLogin,
		StatusWritebackKey:  "cancel:" + cancel.ID,
	}, true
}

func (d *Dispatcher) cancelFailed(ctx context.Context, cancel state.Cancellation, job state.Job, diagnostic string, cause error) (Result, error) {
	if cause == nil && strings.Contains(strings.ToLower(diagnostic), "unsupported") {
		cause = acpx.ErrUnsupportedCancel
	}
	phase := "cancel-failed"
	if errors.Is(cause, acpx.ErrUnsupportedCancel) {
		phase = "cancel-unsupported"
	}
	var currentJob state.Job
	if err := d.Store.Update(ctx, func(st *state.RunnerState) error {
		current := st.Cancellations[cancel.ID]
		current.Status = state.StatusFailed
		current.TargetJobID = job.ID
		current.AcpxResult = "failed"
		current.Diagnostics = append(current.Diagnostics, safeString(diagnostic, 1024))
		if err := st.UpsertCancellation(current); err != nil {
			return err
		}
		currentJob = st.Jobs[job.ID]
		return nil
	}); err != nil {
		return Result{Executed: true, JobID: job.ID, CancellationID: cancel.ID, Status: state.StatusFailed, Error: safeError(err)}, err
	}
	_, err := d.Writeback.Write(ctx, writeback.Request{
		Job:                currentJob,
		Status:             currentJob.Status,
		Phase:              phase,
		Diagnostics:        splitDiagnostic(diagnostic),
		Err:                cause,
		CancelingUserLogin: cancel.CancelingUserLogin,
	})
	if err != nil {
		return Result{Executed: true, JobID: job.ID, CancellationID: cancel.ID, Status: state.StatusFailed, Error: safeError(err)}, err
	}
	return Result{Executed: true, JobID: job.ID, CancellationID: cancel.ID, Status: state.StatusFailed, Error: safeString(diagnostic, 1024)}, nil
}
