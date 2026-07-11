package jobs

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/higress-group/issue-spec/internal/commentrunner/state"
)

const (
	credentialCleanupInitialBackoff = time.Second
	credentialCleanupMaxBackoff     = 5 * time.Minute
	credentialCleanupCallTimeout    = 20 * time.Second
)

func (d *Dispatcher) beginCredentialCleanup(ctx context.Context, jobID string) error {
	if d == nil || d.CredentialBroker == nil {
		return nil
	}
	now := d.now()
	return d.Store.Update(ctx, func(st *state.RunnerState) error {
		st.Normalize()
		job, ok := st.Jobs[jobID]
		if !ok {
			return fmt.Errorf("job %q not found", jobID)
		}
		if job.CredentialCleanup.Status == state.CredentialCleanupComplete {
			return errors.New("credential cleanup was already completed before acquisition")
		}
		if job.CredentialCleanup.Pending() {
			return nil
		}
		job.CredentialCleanup = state.CredentialCleanup{Status: state.CredentialCleanupPending, RequestedAt: now}
		return st.UpsertJob(job)
	})
}

func (d *Dispatcher) recordCredentialCleanupAttempt(ctx context.Context, jobID string, cleanupErr error) (state.Job, error) {
	now := d.now()
	var updated state.Job
	err := d.Store.Update(ctx, func(st *state.RunnerState) error {
		st.Normalize()
		job, ok := st.Jobs[jobID]
		if !ok {
			return fmt.Errorf("job %q not found", jobID)
		}
		if job.CredentialCleanup.Status == state.CredentialCleanupComplete {
			updated = job
			return nil
		}
		if !job.CredentialCleanup.Pending() {
			job.CredentialCleanup = state.CredentialCleanup{Status: state.CredentialCleanupPending, RequestedAt: now}
		}
		job.CredentialCleanup.Attempt++
		job.CredentialCleanup.LastAttemptAt = now
		if cleanupErr == nil {
			job.CredentialCleanup.Status = state.CredentialCleanupComplete
			job.CredentialCleanup.CompletedAt = now
			job.CredentialCleanup.NextAttemptAt = time.Time{}
			job.CredentialCleanup.LastError = ""
		} else {
			job.CredentialCleanup.Status = state.CredentialCleanupPending
			job.CredentialCleanup.CompletedAt = time.Time{}
			job.CredentialCleanup.NextAttemptAt = now.Add(credentialCleanupBackoff(job.CredentialCleanup.Attempt))
			job.CredentialCleanup.LastError = safeString(safeError(cleanupErr), 1024)
		}
		updated = job
		return st.UpsertJob(job)
	})
	return updated, err
}

func credentialCleanupBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := credentialCleanupInitialBackoff
	for index := 1; index < attempt && delay < credentialCleanupMaxBackoff; index++ {
		delay *= 2
		if delay > credentialCleanupMaxBackoff {
			delay = credentialCleanupMaxBackoff
		}
	}
	return delay
}

func (d *Dispatcher) attemptCredentialCleanup(ctx context.Context, job state.Job) (state.Job, error, error) {
	if d == nil || d.CredentialBroker == nil || !job.CredentialCleanup.Pending() {
		return job, nil, nil
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), credentialCleanupCallTimeout)
	defer cancel()
	cleanupErr := d.revokeJobCredentials(cleanupCtx, job)
	updated, stateErr := d.recordCredentialCleanupAttempt(cleanupCtx, job.ID, cleanupErr)
	if stateErr == nil && updated.CredentialCleanup.Status == state.CredentialCleanupComplete {
		// A concurrent idempotent cleanup may already have confirmed every leg;
		// never regress or surface that durable success as pending.
		cleanupErr = nil
	}
	return updated, cleanupErr, stateErr
}

func (d *Dispatcher) retryNextCredentialCleanup(ctx context.Context) (Result, bool, error) {
	if d == nil || d.CredentialBroker == nil {
		return Result{}, false, nil
	}
	st, err := d.Store.Load(ctx)
	if err != nil {
		return Result{}, false, err
	}
	now := d.now()
	jobs := st.ListJobs()
	sort.SliceStable(jobs, func(i, j int) bool {
		left, right := jobs[i].CredentialCleanup.NextAttemptAt, jobs[j].CredentialCleanup.NextAttemptAt
		if left.Equal(right) {
			return jobs[i].ID < jobs[j].ID
		}
		if left.IsZero() {
			return true
		}
		if right.IsZero() {
			return false
		}
		return left.Before(right)
	})
	for _, job := range jobs {
		cleanup := job.CredentialCleanup
		if !cleanup.Pending() || (!cleanup.NextAttemptAt.IsZero() && now.Before(cleanup.NextAttemptAt)) {
			continue
		}
		updated, cleanupErr, stateErr := d.attemptCredentialCleanup(ctx, job)
		result := Result{Executed: true, JobID: job.ID, Status: updated.Status}
		if cleanupErr == nil {
			result.Reason = "credential_cleanup_complete"
		} else {
			result.Reason = "credential_cleanup_pending"
			result.Error = safeError(cleanupErr)
		}
		if stateErr != nil {
			result.Error = safeError(errors.Join(cleanupErr, stateErr))
			return result, true, stateErr
		}
		return result, true, nil
	}
	return Result{}, false, nil
}

func (d *Dispatcher) cleanupTerminalCredentials(ctx context.Context, job state.Job) error {
	if d == nil || d.CredentialBroker == nil {
		return nil
	}
	if job.CredentialCleanup.Status == "" {
		// A genuinely never-dispatched queued job did not cross the credential
		// acquisition boundary. Older active/terminal state has no intent field,
		// so conservatively create one before revoking it.
		if job.DispatchedAt.IsZero() && job.StartedAt.IsZero() && job.DispatchIntent.RunnerJobID == "" &&
			!job.RepositoryBinding.Complete() {
			return nil
		}
		if err := d.beginCredentialCleanup(ctx, job.ID); err != nil {
			return err
		}
		var err error
		job, err = d.loadJob(ctx, job.ID)
		if err != nil {
			return err
		}
	}
	if !job.CredentialCleanup.Pending() {
		return nil
	}
	_, cleanupErr, stateErr := d.attemptCredentialCleanup(ctx, job)
	return errors.Join(cleanupErr, stateErr)
}

func credentialCleanupDiagnostic(job state.Job, err error) string {
	if err == nil {
		return ""
	}
	return safeString("credential cleanup "+strings.TrimSpace(string(job.Status))+": "+safeError(err), 1024)
}
