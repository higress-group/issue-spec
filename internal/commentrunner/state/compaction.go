package state

import (
	"time"
)

// CompactionOptions controls how state compaction is performed.
type CompactionOptions struct {
	DryRun        bool
	ExternalizeRaw bool
	Force         bool
	DiagnosticDir  string
}

// CompactionReport summarizes the results of compaction.
type CompactionReport struct {
	BeforeSize       int64
	AfterSize        int64
	BytesReclaimed   int64
	RecordsAffected  int
	JobsProcessed    int
	SessionsProcessed int
	RawFieldsDropped  int
	ExternalizedCount int
}

// Compact applies bounded metadata and retention policies to the state.
// It returns a report of what was done without actually modifying the state
// if DryRun is true.
func (s *RunnerState) Compact(opts CompactionOptions) (CompactionReport, error) {
	report := CompactionReport{}
	report.BeforeSize = s.EstimatedSize()

	// Track what would be done
	jobsProcessed, rawDropped := s.countJobsForCompaction()
	report.JobsProcessed = jobsProcessed
	report.RawFieldsDropped += rawDropped

	sessionsProcessed, rawDropped := s.countSessionsForCompaction()
	report.SessionsProcessed = sessionsProcessed
	report.RawFieldsDropped += rawDropped

	// If not dry run, apply the changes
	if !opts.DryRun {
		s.applyCompaction(opts)

		// Apply retention policy
		policy := RetentionPolicy{
			Now: time.Now().UTC(),
		}
		retentionReport, _ := policy.Apply(s)
		report.BytesReclaimed += retentionReport.BytesReclaimed
	}

	report.AfterSize = s.EstimatedSize()
	report.BytesReclaimed = report.BeforeSize - report.AfterSize
	report.RecordsAffected = report.JobsProcessed + report.SessionsProcessed

	return report, nil
}

// countJobsForCompaction counts jobs that would be compacted.
func (s *RunnerState) countJobsForCompaction() (processed int, rawFieldsDropped int) {
	for _, job := range s.Jobs {
		originalRawSize := len(job.Acpx.Raw)
		// Estimate bounded size (just a few control-plane fields)
		boundedSize := 5 * 20 // ~5 fields * ~20 bytes each
		dropped := originalRawSize - boundedSize
		if dropped > 0 {
			rawFieldsDropped += dropped
		}
		processed++
	}
	return processed, rawFieldsDropped
}

// countSessionsForCompaction counts sessions that would be compacted.
func (s *RunnerState) countSessionsForCompaction() (processed int, rawFieldsDropped int) {
	for _, session := range s.PublicSessions {
		originalRawSize := len(session.Acpx.Raw)
		boundedSize := 5 * 20
		dropped := originalRawSize - boundedSize
		if dropped > 0 {
			rawFieldsDropped += dropped
		}
		processed++
	}
	return processed, rawFieldsDropped
}

// applyCompaction applies bounded metadata to all jobs and sessions.
func (s *RunnerState) applyCompaction(opts CompactionOptions) {
	for id, job := range s.Jobs {
		// Apply bounded metadata to job
		job.Acpx = s.boundedAcpxMetadata(job.Acpx)
		s.Jobs[id] = job
	}

	for key, session := range s.PublicSessions {
		// Apply bounded metadata to session
		session.Acpx = s.boundedAcpxMetadata(session.Acpx)
		s.PublicSessions[key] = session
	}
}

