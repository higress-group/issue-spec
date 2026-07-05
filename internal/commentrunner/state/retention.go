package state

import (
	"sort"
	"time"
)

// RetentionPolicy governs how terminal control-plane records are compacted and
// eventually pruned so state.json stays bounded regardless of how long the
// runner runs or how many comments it processes.
//
// Two independent bounds are applied to each terminal category:
//   - TerminalTTL: records whose effective finish time is older than now-TTL are
//     pruned (record and idempotency index removed atomically).
//   - Max*: at most N most-recent terminal records are kept per category; older
//     ones beyond the cap are pruned even if still within the TTL window.
//
// Non-terminal records (queued/dispatched/running/interrupted) are always kept
// in full — they are bounded by concurrency and are the only records restart
// reconciliation needs.
type RetentionPolicy struct {
	TerminalTTL              time.Duration
	SessionTTL               time.Duration
	MaxTerminalJobs          int
	MaxTerminalCancellations int
	MaxTerminalWritebacks    int
	MaxTerminalSessions      int
}

// DefaultRetentionPolicy is applied automatically on every save.
func DefaultRetentionPolicy() RetentionPolicy {
	return RetentionPolicy{
		TerminalTTL:              7 * 24 * time.Hour,
		SessionTTL:               30 * 24 * time.Hour,
		MaxTerminalJobs:          200,
		MaxTerminalCancellations: 200,
		MaxTerminalWritebacks:    200,
		MaxTerminalSessions:      200,
	}
}

// CompactionReport summarizes what Compact changed. It is returned for the
// explicit dry-run/observability path and ignored on the automatic save hook.
type CompactionReport struct {
	JobsTombstoned          int
	JobsPruned              int
	CancellationsTombstoned int
	CancellationsPruned     int
	WritebacksTombstoned    int
	WritebacksPruned        int
	SessionsPruned          int
	DanglingIndexesDropped  int
}

// Compact tombstones terminal records (stripping heavy payloads while keeping
// the fields duplicate-suppression and recovery actually read) and prunes
// terminal records that have aged out or exceeded the per-category cap. Record
// and idempotency index entries are always removed together, and dangling index
// entries are dropped, so duplicate-command suppression stays consistent.
func (s *RunnerState) Compact(now time.Time, policy RetentionPolicy) CompactionReport {
	s.Normalize()
	var report CompactionReport

	report.JobsTombstoned = s.tombstoneTerminalJobs()
	report.CancellationsTombstoned = s.tombstoneTerminalCancellations()
	report.WritebacksTombstoned = s.tombstoneTerminalWritebacks()

	report.JobsPruned = s.pruneTerminalJobs(now, policy)
	report.CancellationsPruned = s.pruneTerminalCancellations(now, policy)
	report.WritebacksPruned = s.pruneTerminalWritebacks(now, policy)
	report.SessionsPruned = s.pruneTerminalSessions(now, policy)

	report.DanglingIndexesDropped = s.dropDanglingIndexes()
	return report
}

// --- tombstoning (in-place shrink of terminal records) ---

func (s *RunnerState) tombstoneTerminalJobs() int {
	count := 0
	for id, job := range s.Jobs {
		if !job.Status.Terminal() || !job.hasHeavyFields() {
			continue
		}
		s.Jobs[id] = job.tombstone()
		count++
	}
	return count
}

func (s *RunnerState) tombstoneTerminalCancellations() int {
	count := 0
	for id, cancel := range s.Cancellations {
		if !cancel.Status.Terminal() || !cancel.hasHeavyFields() {
			continue
		}
		s.Cancellations[id] = cancel.tombstone()
		count++
	}
	return count
}

// tombstoneTerminalWritebacks shrinks writebacks whose owning job is terminal
// (or gone). While the job is non-terminal the writeback is kept intact because
// recovery still needs URL/error context to edit the same status comment.
func (s *RunnerState) tombstoneTerminalWritebacks() int {
	count := 0
	for key, writeback := range s.StatusWritebacks {
		if !s.jobTerminalOrMissing(writeback.JobID) || !writeback.hasHeavyFields() {
			continue
		}
		s.StatusWritebacks[key] = writeback.tombstone()
		count++
	}
	return count
}

func (s *RunnerState) jobTerminalOrMissing(jobID string) bool {
	if jobID == "" {
		return true
	}
	job, ok := s.Jobs[jobID]
	if !ok {
		return true
	}
	return job.Status.Terminal()
}

// --- pruning (atomic record + index removal past retention) ---

func (s *RunnerState) pruneTerminalJobs(now time.Time, policy RetentionPolicy) int {
	type entry struct {
		id string
		at time.Time
	}
	var terminal []entry
	for id, job := range s.Jobs {
		if !job.Status.Terminal() {
			continue
		}
		terminal = append(terminal, entry{id: id, at: job.effectiveTime()})
	}
	sortByTimeDesc(len(terminal), func(i int) time.Time { return terminal[i].at }, func(i, j int) {
		terminal[i], terminal[j] = terminal[j], terminal[i]
	})
	cutoff := ttlCutoff(now, policy.TerminalTTL)
	count := 0
	for rank, e := range terminal {
		if !shouldPrune(rank, e.at, cutoff, policy.MaxTerminalJobs) {
			continue
		}
		s.PruneJob(e.id)
		count++
	}
	return count
}

func (s *RunnerState) pruneTerminalCancellations(now time.Time, policy RetentionPolicy) int {
	type entry struct {
		id string
		at time.Time
	}
	var terminal []entry
	for id, cancel := range s.Cancellations {
		if !cancel.Status.Terminal() {
			continue
		}
		terminal = append(terminal, entry{id: id, at: cancel.effectiveTime()})
	}
	sortByTimeDesc(len(terminal), func(i int) time.Time { return terminal[i].at }, func(i, j int) {
		terminal[i], terminal[j] = terminal[j], terminal[i]
	})
	cutoff := ttlCutoff(now, policy.TerminalTTL)
	count := 0
	for rank, e := range terminal {
		if !shouldPrune(rank, e.at, cutoff, policy.MaxTerminalCancellations) {
			continue
		}
		s.PruneCancellation(e.id)
		count++
	}
	return count
}

func (s *RunnerState) pruneTerminalWritebacks(now time.Time, policy RetentionPolicy) int {
	type entry struct {
		key string
		at  time.Time
	}
	var terminal []entry
	for key, writeback := range s.StatusWritebacks {
		if !s.jobTerminalOrMissing(writeback.JobID) {
			continue
		}
		terminal = append(terminal, entry{key: key, at: writeback.effectiveTime()})
	}
	sortByTimeDesc(len(terminal), func(i int) time.Time { return terminal[i].at }, func(i, j int) {
		terminal[i], terminal[j] = terminal[j], terminal[i]
	})
	cutoff := ttlCutoff(now, policy.TerminalTTL)
	count := 0
	for rank, e := range terminal {
		if !shouldPrune(rank, e.at, cutoff, policy.MaxTerminalWritebacks) {
			continue
		}
		s.PruneStatusWriteback(e.key)
		count++
	}
	return count
}

// pruneTerminalSessions drops old terminal public sessions. Sessions are the
// /resume anchor and already tiny once Raw is gone, so they use a separate,
// generous TTL and are never tombstoned.
func (s *RunnerState) pruneTerminalSessions(now time.Time, policy RetentionPolicy) int {
	type entry struct {
		key string
		at  time.Time
	}
	var terminal []entry
	for key, session := range s.PublicSessions {
		if !session.Status.Terminal() {
			continue
		}
		terminal = append(terminal, entry{key: key, at: session.effectiveTime()})
	}
	sortByTimeDesc(len(terminal), func(i int) time.Time { return terminal[i].at }, func(i, j int) {
		terminal[i], terminal[j] = terminal[j], terminal[i]
	})
	cutoff := ttlCutoff(now, policy.SessionTTL)
	count := 0
	for rank, e := range terminal {
		if !shouldPrune(rank, e.at, cutoff, policy.MaxTerminalSessions) {
			continue
		}
		delete(s.PublicSessions, e.key)
		count++
	}
	return count
}

// dropDanglingIndexes removes idempotency index entries whose target record no
// longer exists. This is a backstop against the "index points to missing record"
// class of bug that would otherwise break CreateCommandJob duplicate handling.
func (s *RunnerState) dropDanglingIndexes() int {
	count := 0
	for key, id := range s.Idempotency.CommandJobs {
		if _, ok := s.Jobs[id]; !ok {
			delete(s.Idempotency.CommandJobs, key)
			count++
		}
	}
	for key, id := range s.Idempotency.CancelRequests {
		if _, ok := s.Cancellations[id]; !ok {
			delete(s.Idempotency.CancelRequests, key)
			count++
		}
	}
	for key, target := range s.Idempotency.StatusWritebacks {
		if _, ok := s.StatusWritebacks[target]; !ok {
			delete(s.Idempotency.StatusWritebacks, key)
			count++
		}
	}
	return count
}

// PruneJob removes a job and every idempotency index entry pointing at it.
func (s *RunnerState) PruneJob(id string) {
	delete(s.Jobs, id)
	for key, target := range s.Idempotency.CommandJobs {
		if target == id {
			delete(s.Idempotency.CommandJobs, key)
		}
	}
}

// PruneCancellation removes a cancellation and its idempotency index entries.
func (s *RunnerState) PruneCancellation(id string) {
	delete(s.Cancellations, id)
	for key, target := range s.Idempotency.CancelRequests {
		if target == id {
			delete(s.Idempotency.CancelRequests, key)
		}
	}
}

// PruneStatusWriteback removes a status writeback and its idempotency index entries.
func (s *RunnerState) PruneStatusWriteback(key string) {
	delete(s.StatusWritebacks, key)
	delete(s.Idempotency.StatusWritebacks, key)
	for indexKey, target := range s.Idempotency.StatusWritebacks {
		if target == key {
			delete(s.Idempotency.StatusWritebacks, indexKey)
		}
	}
}

// --- helpers ---

func ttlCutoff(now time.Time, ttl time.Duration) time.Time {
	if ttl <= 0 {
		return time.Time{}
	}
	return now.Add(-ttl)
}

func shouldPrune(rank int, at, cutoff time.Time, maxKept int) bool {
	if maxKept > 0 && rank >= maxKept {
		return true
	}
	if !cutoff.IsZero() && !at.IsZero() && at.Before(cutoff) {
		return true
	}
	return false
}

func sortByTimeDesc(n int, at func(int) time.Time, swap func(i, j int)) {
	sort.Sort(descByTime{n: n, at: at, swap: swap})
}

type descByTime struct {
	n    int
	at   func(int) time.Time
	swap func(i, j int)
}

func (d descByTime) Len() int           { return d.n }
func (d descByTime) Less(i, j int) bool { return d.at(i).After(d.at(j)) }
func (d descByTime) Swap(i, j int)      { d.swap(i, j) }

func (j Job) effectiveTime() time.Time {
	if !j.FinishedAt.IsZero() {
		return j.FinishedAt
	}
	if !j.UpdatedAt.IsZero() {
		return j.UpdatedAt
	}
	return j.CreatedAt
}

func (c Cancellation) effectiveTime() time.Time {
	if !c.CancelledAt.IsZero() {
		return c.CancelledAt
	}
	return c.CreatedAt
}

func (w StatusWriteback) effectiveTime() time.Time {
	if !w.UpdatedAt.IsZero() {
		return w.UpdatedAt
	}
	return w.LastAttemptAt
}

func (p PublicSession) effectiveTime() time.Time {
	if !p.LastUsedAt.IsZero() {
		return p.LastUsedAt
	}
	return p.CreatedAt
}

func (j Job) hasHeavyFields() bool {
	return j.Acpx.StableRecordID != "" || j.Acpx.CWD != "" || j.CommandPrompt != "" ||
		len(j.Diagnostics) > 0 || j.Workspace.ID != "" || len(j.CLIDirect) > 0 ||
		j.ContextBundle.Hash != "" || j.CoordinatorSummary != "" ||
		j.FirstObservedComment.CommentID != 0 || j.DispatchIntent.RunnerJobID != "" ||
		j.Sandbox.Enabled
}

// tombstone keeps only the fields duplicate suppression and operator inspection
// read for a terminal job, dropping the large ACPX/workspace/context payloads.
func (j Job) tombstone() Job {
	return Job{
		ID:                    j.ID,
		Repo:                  j.Repo,
		IssueNumber:           j.IssueNumber,
		PublicSessionID:       j.PublicSessionID,
		CommandID:             j.CommandID,
		CommandName:           j.CommandName,
		CommandIdempotencyKey: j.CommandIdempotencyKey,
		StatusWritebackKey:    j.StatusWritebackKey,
		TriggerCommentID:      j.TriggerCommentID,
		StatusCommentID:       j.StatusCommentID,
		StatusCommentURL:      j.StatusCommentURL,
		Status:                j.Status,
		CreatedAt:             j.CreatedAt,
		UpdatedAt:             j.UpdatedAt,
		DispatchedAt:          j.DispatchedAt,
		StartedAt:             j.StartedAt,
		FinishedAt:            j.FinishedAt,
	}
}

func (c Cancellation) hasHeavyFields() bool {
	return c.AcpxResult != "" || len(c.Diagnostics) > 0
}

func (c Cancellation) tombstone() Cancellation {
	return Cancellation{
		ID:                    c.ID,
		IdempotencyKey:        c.IdempotencyKey,
		Repo:                  c.Repo,
		TriggerCommentID:      c.TriggerCommentID,
		TargetPublicSessionID: c.TargetPublicSessionID,
		TargetJobID:           c.TargetJobID,
		Status:                c.Status,
		CreatedAt:             c.CreatedAt,
		CancelledAt:           c.CancelledAt,
	}
}

func (w StatusWriteback) hasHeavyFields() bool {
	return w.URL != "" || w.LastError != ""
}

// tombstone keeps only what a re-delivered trigger needs to be answered
// idempotently: the key, owning job, terminal status, and the comment id.
func (w StatusWriteback) tombstone() StatusWriteback {
	return StatusWriteback{
		IdempotencyKey: w.IdempotencyKey,
		JobID:          w.JobID,
		Repo:           w.Repo,
		IssueNumber:    w.IssueNumber,
		CommentID:      w.CommentID,
		Status:         w.Status,
		UpdatedAt:      w.UpdatedAt,
	}
}
