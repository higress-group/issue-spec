package jobs

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/higress-group/issue-spec/internal/acpx"
	"github.com/higress-group/issue-spec/internal/commentrunner/state"
	"github.com/higress-group/issue-spec/internal/commentrunner/writeback"
)

type TurnCanceller interface {
	Cancel(context.Context, acpx.SessionRef) (acpx.CancelResult, error)
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
		return Result{CancellationID: cancel.ID, Status: cancel.Status, Reason: "already_terminal"}, nil
	}
	job, found, terminal, err := d.markCancellationRunning(ctx, cancel)
	if err != nil {
		return Result{Executed: true, CancellationID: cancel.ID, Status: state.StatusFailed, Error: safeError(err)}, err
	}
	if !found {
		return Result{Executed: true, CancellationID: cancel.ID, Status: state.StatusRejected, Reason: "unknown_session"}, nil
	}
	if terminal {
		return Result{Executed: true, JobID: job.ID, CancellationID: cancel.ID, Status: state.StatusCancelled, Reason: "target_already_terminal"}, nil
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
	ref, diagnostic, ok := sessionRefForJob(job)
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
	_, err := d.Writeback.Write(ctx, writeback.Request{
		Job:                cancelled,
		Status:             state.StatusCancelled,
		Phase:              "cancelled-before-dispatch",
		CancelingUserLogin: cancel.CancelingUserLogin,
	})
	return err
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
	_, err := d.Writeback.Write(ctx, writeback.Request{
		Job:                cancelled,
		Status:             state.StatusCancelled,
		Phase:              "cancelled",
		Diagnostics:        splitDiagnostic(diagnostics),
		CancelingUserLogin: cancel.CancelingUserLogin,
	})
	return err
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
