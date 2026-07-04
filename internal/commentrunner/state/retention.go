package state

import (
	"sort"
	"time"
)

// Default retention values
const (
	DefaultTerminalJobsRetention     = 30 * 24 * time.Hour // 30 days
	DefaultTerminalJobsMaxCount     = 1000
	DefaultTerminalSessionsRetention = 30 * 24 * time.Hour // 30 days
	DefaultTerminalSessionsMaxCount = 500
	DefaultCancellationsRetention   = 7 * 24 * time.Hour // 7 days
	DefaultCancellationsMaxCount   = 100
	DefaultWritebacksRetention      = 7 * 24 * time.Hour // 7 days
	DefaultWritebacksMaxCount      = 100
	DefaultIdempotencyRetention     = 30 * 24 * time.Hour // 30 days
)

// RetentionConfig defines retention policies for terminal records.
type RetentionConfig struct {
	// Terminal job retention
	TerminalJobsMaxAge  *time.Duration `json:"terminal_jobs_max_age,omitempty"`
	TerminalJobsMaxCount int           `json:"terminal_jobs_max_count,omitempty"`

	// Terminal session retention
	TerminalSessionsMaxAge  *time.Duration `json:"terminal_sessions_max_age,omitempty"`
	TerminalSessionsMaxCount int           `json:"terminal_sessions_max_count,omitempty"`

	// Cancellation retention
	CancellationsMaxAge  *time.Duration `json:"cancellations_max_age,omitempty"`
	CancellationsMaxCount int           `json:"cancellations_max_count,omitempty"`

	// Status writeback retention
	WritebacksMaxAge  *time.Duration `json:"writebacks_max_age,omitempty"`
	WritebacksMaxCount int           `json:"writebacks_max_count,omitempty"`

	// Idempotency tombstone retention
	IdempotencyTombstonesMaxAge *time.Duration `json:"idempotency_tombstones_max_age,omitempty"`
}

// RetentionReport summarizes the results of applying retention policies.
type RetentionReport struct {
	JobsPruned          int
	JobsTombstoned      int
	SessionsPruned      int
	SessionsTombstoned  int
	CancellationsPruned int
	WritebacksPruned    int
	TombstonesPruned    int
	BytesReclaimed      int64
}

// RetentionPolicy applies retention rules to runner state.
type RetentionPolicy struct {
	Config RetentionConfig
	Now    time.Time
}

// Apply applies the retention policy to the runner state.
func (p *RetentionPolicy) Apply(s *RunnerState) (RetentionReport, error) {
	var report RetentionReport
	originalSize := s.EstimatedSize()

	// Apply terminal job retention
	jobsPruned, jobsTombstoned := p.applyTerminalJobsRetention(s)
	report.JobsPruned = jobsPruned
	report.JobsTombstoned = jobsTombstoned

	// Apply terminal session retention
	sessionsPruned, sessionsTombstoned := p.applyTerminalSessionsRetention(s)
	report.SessionsPruned = sessionsPruned
	report.SessionsTombstoned = sessionsTombstoned

	// Apply cancellation retention
	report.CancellationsPruned = p.applyCancellationsRetention(s)

	// Apply writeback retention
	report.WritebacksPruned = p.applyWritebacksRetention(s)

	// Apply idempotency tombstone retention
	report.TombstonesPruned = p.applyIdempotencyRetention(s)

	newSize := s.EstimatedSize()
	report.BytesReclaimed = originalSize - newSize

	return report, nil
}

func (p *RetentionPolicy) applyTerminalJobsRetention(s *RunnerState) (pruned, tombstoned int) {
	maxAge := p.getMaxAge(p.Config.TerminalJobsMaxAge, DefaultTerminalJobsRetention)
	maxCount := p.getMaxCount(p.Config.TerminalJobsMaxCount, DefaultTerminalJobsMaxCount)

	// Group terminal jobs by finish time
	var terminalJobs []Job
	for _, job := range s.Jobs {
		if job.Status.Terminal() && !job.Status.NeedsReconciliation() {
			terminalJobs = append(terminalJobs, job)
		}
	}

	// Sort by finish time (newest first)
	sort.Slice(terminalJobs, func(i, j int) bool {
		return terminalJobs[i].FinishedAt.After(terminalJobs[j].FinishedAt)
	})

	// Keep most recent maxCount jobs
	toKeep := make(map[string]bool)
	for i, job := range terminalJobs {
		if i < maxCount {
			toKeep[job.ID] = true
		}
	}

	// Apply retention
	for _, job := range terminalJobs {
		
		if toKeep[job.ID] {
			continue // Within count limit
		}
		if !job.FinishedAt.IsZero() && p.Now.Sub(job.FinishedAt) <= maxAge {
			continue // Within age limit
		}
			

		// Prune or tombstone
		if job.CommandIdempotencyKey != "" || job.StatusWritebackKey != "" {
			// Create tombstone to preserve idempotency
			p.createJobTombstone(s, job)
			tombstoned++
		} else {
			// Full prune
			delete(s.Jobs, job.ID)
			pruned++
		}
	}

	return pruned, tombstoned
}

func (p *RetentionPolicy) applyTerminalSessionsRetention(s *RunnerState) (pruned, tombstoned int) {
	maxAge := p.getMaxAge(p.Config.TerminalSessionsMaxAge, DefaultTerminalSessionsRetention)
	maxCount := p.getMaxCount(p.Config.TerminalSessionsMaxCount, DefaultTerminalSessionsMaxCount)

	// Group terminal sessions by last used time
	var terminalSessions []PublicSession
	for _, session := range s.PublicSessions {
		if session.Status.Terminal() && !session.Status.NeedsReconciliation() {
			terminalSessions = append(terminalSessions, session)
		}
	}

	// Sort by last used time (newest first)
	sort.Slice(terminalSessions, func(i, j int) bool {
		return terminalSessions[i].LastUsedAt.After(terminalSessions[j].LastUsedAt)
	})

	// Keep most recent maxCount sessions
	toKeep := make(map[string]bool)
	for i, session := range terminalSessions {
		if i < maxCount {
			key := PublicSessionKey(session.Repo, session.PublicSessionID)
			toKeep[key] = true
		}
	}

	// Apply retention
	for _, session := range terminalSessions {
		key := PublicSessionKey(session.Repo, session.PublicSessionID)
		if toKeep[key] {
			continue // Within count limit
		}
		if !session.LastUsedAt.IsZero() && p.Now.Sub(session.LastUsedAt) <= maxAge {
			continue // Within age limit
		}

		// For sessions, we just prune since they don't have idempotency keys
		delete(s.PublicSessions, key)
		pruned++
	}

	return pruned, tombstoned
}

func (p *RetentionPolicy) applyCancellationsRetention(s *RunnerState) (pruned int) {
	maxAge := p.getMaxAge(p.Config.CancellationsMaxAge, DefaultCancellationsRetention)
	maxCount := p.getMaxCount(p.Config.CancellationsMaxCount, DefaultCancellationsMaxCount)

	// Group completed cancellations by created time
	var completed []Cancellation
	for _, cancel := range s.Cancellations {
		if cancel.Status.Terminal() {
			completed = append(completed, cancel)
		}
	}

	// Sort by created time (newest first)
	sort.Slice(completed, func(i, j int) bool {
		return completed[i].CreatedAt.After(completed[j].CreatedAt)
	})

	// Keep most recent maxCount
	if len(completed) > maxCount {
		for i := maxCount; i < len(completed); i++ {
			if !completed[i].CreatedAt.IsZero() && p.Now.Sub(completed[i].CreatedAt) > maxAge {
				delete(s.Cancellations, completed[i].ID)
				// Clean up idempotency index
				if completed[i].IdempotencyKey != "" {
					delete(s.Idempotency.CancelRequests, completed[i].IdempotencyKey)
				}
				pruned++
			}
		}
	}

	return pruned
}

func (p *RetentionPolicy) applyWritebacksRetention(s *RunnerState) (pruned int) {
	maxAge := p.getMaxAge(p.Config.WritebacksMaxAge, DefaultWritebacksRetention)
	maxCount := p.getMaxCount(p.Config.WritebacksMaxCount, DefaultWritebacksMaxCount)

	// Group writebacks by last attempt time
	var completed []StatusWriteback
	for _, wb := range s.StatusWritebacks {
		if wb.Status.Terminal() {
			completed = append(completed, wb)
		}
	}

	// Sort by last attempt time (newest first)
	sort.Slice(completed, func(i, j int) bool {
		return completed[i].LastAttemptAt.After(completed[j].LastAttemptAt)
	})

	// Keep most recent maxCount
	if len(completed) > maxCount {
		for i := maxCount; i < len(completed); i++ {
			if !completed[i].LastAttemptAt.IsZero() && p.Now.Sub(completed[i].LastAttemptAt) > maxAge {
				delete(s.StatusWritebacks, completed[i].IdempotencyKey)
				// Clean up idempotency index
				delete(s.Idempotency.StatusWritebacks, completed[i].IdempotencyKey)
				pruned++
			}
		}
	}

	return pruned
}

func (p *RetentionPolicy) applyIdempotencyRetention(s *RunnerState) (pruned int) {

	// Clean up orphaned idempotency entries
	// These are entries where the target job/cancellation/writeback no longer exists
	for key, jobID := range s.Idempotency.CommandJobs {
		if _, exists := s.Jobs[jobID]; !exists {
			// Job is gone, check if it's been a while
			// For simplicity, we'll just remove orphaned entries
			delete(s.Idempotency.CommandJobs, key)
			pruned++
		}
	}

	for key, cancelID := range s.Idempotency.CancelRequests {
		if _, exists := s.Cancellations[cancelID]; !exists {
			delete(s.Idempotency.CancelRequests, key)
			pruned++
		}
	}

	return pruned
}

func (p *RetentionPolicy) createJobTombstone(s *RunnerState, job Job) {
	// Create minimal tombstone record
	tombstone := Job{
		ID:                    job.ID,
		Status:                job.Status,
		CreatedAt:             job.CreatedAt,
		FinishedAt:            job.FinishedAt,
		CommandIdempotencyKey: job.CommandIdempotencyKey,
		StatusWritebackKey:    job.StatusWritebackKey,
		Diagnostics:           []string{"Terminal record pruned by retention policy"},
	}
	s.Jobs[job.ID] = tombstone
}

func (p *RetentionPolicy) getMaxAge(ptr *time.Duration, defaultValue time.Duration) time.Duration {
	if ptr != nil && *ptr > 0 {
		return *ptr
	}
	return defaultValue
}

func (p *RetentionPolicy) getMaxCount(val int, defaultValue int) int {
	if val >= 0 {
		return val
	}
	return defaultValue
}
