package state

import (
	"testing"
	"time"
)

func terminalJob(id, idemKey string, finishedAt time.Time) Job {
	return Job{
		ID:                    id,
		Repo:                  "o/r",
		IssueNumber:           30,
		PublicSessionID:       "ps-1",
		AcpxRecordID:          "rec-1",
		CommandName:           "new",
		CommandPrompt:         "a long prompt that should not survive tombstoning",
		CommandIdempotencyKey: idemKey,
		StatusWritebackKey:    "wb-" + id,
		StatusCommentID:       501,
		Status:                StatusCompleted,
		CreatedAt:             finishedAt.Add(-time.Minute),
		FinishedAt:            finishedAt,
		Workspace:             WorkspaceMetadata{ID: "ws-1", Path: "/work/ws-1", Repo: "o/r"},
		Acpx:                  AcpxMetadata{StableRecordID: "rec-1", CWD: "/work/ws-1"},
		Diagnostics:           []string{"a diagnostic line"},
		CoordinatorSummary:    "some summary text",
	}
}

func TestCompactTombstonesTerminalJobButKeepsIdempotency(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	st := NewState()
	if _, _, err := st.CreateCommandJob(terminalJob("job-1", "cmd:o/r:101", now.Add(-time.Hour))); err != nil {
		t.Fatal(err)
	}

	report := st.Compact(now, DefaultRetentionPolicy())
	if report.JobsTombstoned != 1 || report.JobsPruned != 0 {
		t.Fatalf("unexpected compaction report: %+v", report)
	}

	job := st.Jobs["job-1"]
	// Heavy fields stripped...
	if job.CommandPrompt != "" || job.CoordinatorSummary != "" || len(job.Diagnostics) != 0 ||
		job.Workspace.ID != "" || job.Acpx.StableRecordID != "" {
		t.Fatalf("tombstone kept heavy fields: %+v", job)
	}
	// ...but idempotency-relevant fields retained.
	if job.CommandIdempotencyKey != "cmd:o/r:101" || job.Status != StatusCompleted || job.StatusCommentID != 501 {
		t.Fatalf("tombstone dropped idempotency fields: %+v", job)
	}

	// A re-delivered command with the same key must resolve to the existing job,
	// not create a new one and not error.
	existing, created, err := st.CreateCommandJob(terminalJob("job-2", "cmd:o/r:101", now))
	if err != nil {
		t.Fatalf("duplicate suppression errored after tombstoning: %v", err)
	}
	if created || existing.ID != "job-1" {
		t.Fatalf("duplicate suppression broken: existing=%s created=%v", existing.ID, created)
	}

	// Re-compacting must be idempotent (no double counting of already-tombstoned).
	if r := st.Compact(now, DefaultRetentionPolicy()); r.JobsTombstoned != 0 {
		t.Fatalf("re-compaction re-tombstoned: %+v", r)
	}
}

func TestCompactPrunesJobPastTTLWithIndex(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	policy := DefaultRetentionPolicy() // 7d TTL
	st := NewState()
	if _, _, err := st.CreateCommandJob(terminalJob("old", "cmd:old", now.Add(-8*24*time.Hour))); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CreateCommandJob(terminalJob("fresh", "cmd:fresh", now.Add(-time.Hour))); err != nil {
		t.Fatal(err)
	}

	report := st.Compact(now, policy)
	if report.JobsPruned != 1 {
		t.Fatalf("expected 1 pruned job, got %+v", report)
	}
	if _, ok := st.Jobs["old"]; ok {
		t.Fatalf("aged-out job was not pruned")
	}
	if _, ok := st.Jobs["fresh"]; !ok {
		t.Fatalf("fresh job was pruned")
	}
	// Index entry for the pruned job must be gone, and lookup must return a clean miss.
	if _, ok := st.Idempotency.CommandJobs["cmd:old"]; ok {
		t.Fatalf("dangling index for pruned job remained")
	}
	if _, ok := st.FindCommandJob("cmd:old"); ok {
		t.Fatalf("pruned job still resolvable via index")
	}
}

func TestCompactPrunesJobsOverCountCap(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	policy := RetentionPolicy{MaxTerminalJobs: 3} // no TTL, cap only
	st := NewState()
	for i := 0; i < 10; i++ {
		id := string(rune('a' + i))
		// All within any TTL; distinct finish times so ranking is deterministic.
		if _, _, err := st.CreateCommandJob(terminalJob(id, "cmd:"+id, now.Add(-time.Duration(i)*time.Minute))); err != nil {
			t.Fatal(err)
		}
	}
	st.Compact(now, policy)
	if len(st.Jobs) != 3 {
		t.Fatalf("expected 3 jobs kept by cap, got %d", len(st.Jobs))
	}
	// Newest three (smallest i => latest finish time) must be kept.
	for _, id := range []string{"a", "b", "c"} {
		if _, ok := st.Jobs[id]; !ok {
			t.Fatalf("cap pruned a newest job %q", id)
		}
	}
}

func TestCompactKeepsNonTerminalRecords(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	st := NewState()
	// Very old but still running: must never be pruned or tombstoned.
	running := terminalJob("run", "cmd:run", now.Add(-100*24*time.Hour))
	running.Status = StatusRunning
	running.FinishedAt = time.Time{}
	if err := st.UpsertJob(running); err != nil {
		t.Fatal(err)
	}
	report := st.Compact(now, DefaultRetentionPolicy())
	if report.JobsTombstoned != 0 || report.JobsPruned != 0 {
		t.Fatalf("non-terminal job was compacted: %+v", report)
	}
	if st.Jobs["run"].CommandPrompt == "" {
		t.Fatalf("non-terminal job lost heavy fields")
	}
}

func TestCompactWritebackLifecycle(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	st := NewState()

	// Job still running -> writeback kept intact (recovery needs URL).
	running := terminalJob("job-wb", "cmd:wb", now.Add(-time.Minute))
	running.Status = StatusRunning
	running.FinishedAt = time.Time{}
	if err := st.UpsertJob(running); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertStatusWriteback(StatusWriteback{
		IdempotencyKey: "wb:1", JobID: "job-wb", Repo: "o/r", IssueNumber: 30,
		CommentID: 501, URL: "https://github.com/o/r/issues/30#issuecomment-501",
		Status: StatusRunning, UpdatedAt: now.Add(-time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	st.Compact(now, DefaultRetentionPolicy())
	if wb := st.StatusWritebacks["wb:1"]; wb.URL == "" {
		t.Fatalf("writeback for live job lost recovery URL: %+v", wb)
	}

	// Job now terminal -> writeback shrinks but keeps CommentID for idempotency.
	job := st.Jobs["job-wb"]
	job.Status = StatusCompleted
	job.FinishedAt = now.Add(-time.Second)
	st.Jobs["job-wb"] = job
	report := st.Compact(now, DefaultRetentionPolicy())
	if report.WritebacksTombstoned != 1 {
		t.Fatalf("terminal writeback not tombstoned: %+v", report)
	}
	wb, ok := st.FindStatusWriteback("wb:1")
	if !ok || wb.CommentID != 501 || wb.URL != "" {
		t.Fatalf("writeback tombstone wrong shape: %+v ok=%v", wb, ok)
	}
}

func TestNormalizeDropsDanglingIndexes(t *testing.T) {
	st := NewState()
	// Simulate a corrupt/legacy state: index points at a record that is gone.
	st.Idempotency.CommandJobs["cmd:ghost"] = "ghost-job"
	st.Idempotency.CancelRequests["cancel:ghost"] = "ghost-cancel"
	st.Idempotency.StatusWritebacks["wb:ghost"] = "wb:ghost"

	st.Normalize()

	if _, ok := st.Idempotency.CommandJobs["cmd:ghost"]; ok {
		t.Fatalf("dangling command index survived normalize")
	}
	if _, ok := st.Idempotency.CancelRequests["cancel:ghost"]; ok {
		t.Fatalf("dangling cancel index survived normalize")
	}
	if _, ok := st.Idempotency.StatusWritebacks["wb:ghost"]; ok {
		t.Fatalf("dangling writeback index survived normalize")
	}
	// CreateCommandJob must not error on a formerly-dangling key.
	if _, created, err := st.CreateCommandJob(terminalJob("job-x", "cmd:ghost", time.Unix(1_700_000_000, 0).UTC())); err != nil || !created {
		t.Fatalf("create after dangling cleanup failed: created=%v err=%v", created, err)
	}
}
