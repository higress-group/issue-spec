package state

import (
	"path/filepath"
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
		ExactProcessID:        "PROCESS-020",
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
	if job.CommandIdempotencyKey != "cmd:o/r:101" || job.Status != StatusCompleted || job.StatusCommentID != 501 || job.ExactProcessID != "PROCESS-020" {
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

func TestCompactPreservesTerminalJobWithPendingCredentialCleanup(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	st := NewState()
	job := terminalJob("cleanup-pending", "cmd:cleanup-pending", now.Add(-30*24*time.Hour))
	job.CredentialCleanup = CredentialCleanup{Status: CredentialCleanupPending, RequestedAt: now.Add(-30 * 24 * time.Hour),
		Attempt: 3, LastAttemptAt: now.Add(-time.Hour), NextAttemptAt: now.Add(time.Minute), LastError: "retry"}
	if _, _, err := st.CreateCommandJob(job); err != nil {
		t.Fatal(err)
	}
	report := st.Compact(now, RetentionPolicy{TerminalTTL: time.Hour, MaxTerminalJobs: 0})
	kept, ok := st.Jobs[job.ID]
	if !ok || !kept.CredentialCleanup.Pending() || kept.CommandPrompt == "" ||
		report.JobsPruned != 0 || report.JobsTombstoned != 0 {
		t.Fatalf("pending cleanup was compacted: report=%+v job=%+v ok=%v", report, kept, ok)
	}
}

func TestCompactTerminalJobsPreserveExactProcessAndPendingWorkspaceCleanup(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	for _, status := range []LifecycleStatus{StatusCompleted, StatusFailed, StatusCancelled} {
		t.Run(string(status), func(t *testing.T) {
			st := NewState()
			association := testProcessWorkspaceAssociation("ws-retention-"+string(status), "PROCESS-020")
			if _, err := st.ProcessWorkspaces.Reserve(association); err != nil {
				t.Fatal(err)
			}
			if _, err := st.ProcessWorkspaces.Transition(association.WorkspaceID, association.ReservationID,
				ProcessWorkspaceAllocating, ProcessWorkspaceCleanupPending); err != nil {
				t.Fatal(err)
			}
			st.ProcessWorkspaces.Generation = 1
			assignment := ProcessWorkspaceAssignment{
				ProcessID: association.ProcessID, WorkspaceID: association.WorkspaceID, ReservationID: association.ReservationID,
				AssociationGeneration: 1, ReservationIdentity: association.ReservationIdentity, CleanupRequired: true,
				CleanupState: ProcessWorkspaceAssignmentCleanupPending, LastError: "cleanup_failed",
			}
			job := terminalJob("job-"+string(status), "cmd:"+string(status), now.Add(-30*24*time.Hour))
			job.Status = status
			job.ProcessWorkspace = &assignment
			if _, _, err := st.CreateCommandJob(job); err != nil {
				t.Fatal(err)
			}

			report := st.Compact(now, RetentionPolicy{TerminalTTL: time.Hour, MaxTerminalJobs: 1})
			kept, ok := st.Jobs[job.ID]
			if !ok || report.JobsTombstoned != 1 || report.JobsPruned != 0 {
				t.Fatalf("active cleanup terminal job compacted incorrectly: report=%+v job=%+v ok=%v", report, kept, ok)
			}
			if kept.ExactProcessID != "PROCESS-020" || kept.ProcessWorkspace == nil || *kept.ProcessWorkspace != assignment {
				t.Fatalf("terminal tombstone lost process target or cleanup assignment: %+v", kept)
			}
			if kept.CommandPrompt != "" || kept.Workspace.Path != "" || kept.Acpx.CWD != "" {
				t.Fatalf("terminal tombstone retained machine-local payload: %+v", kept)
			}
			if err := st.Validate(); err != nil {
				t.Fatalf("compacted state invalid: %v", err)
			}

			path := filepath.Join(t.TempDir(), "state.json")
			if err := SaveFile(path, st); err != nil {
				t.Fatal(err)
			}
			reopened, err := LoadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			got := reopened.Jobs[job.ID]
			if got.ExactProcessID != job.ExactProcessID || got.ProcessWorkspace == nil || *got.ProcessWorkspace != assignment {
				t.Fatalf("save/reopen changed cleanup tombstone: %+v", got)
			}
		})
	}
}

func TestCompactPreservesRequiredWorkspaceCleanupUntilConfirmed(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	st := NewState()
	association := testProcessWorkspaceAssociation("ws-retention-required", "PROCESS-020")
	if _, err := st.ProcessWorkspaces.Reserve(association); err != nil {
		t.Fatal(err)
	}
	st.ProcessWorkspaces.Generation = 1
	assignment := ProcessWorkspaceAssignment{ProcessID: association.ProcessID, WorkspaceID: association.WorkspaceID,
		ReservationID: association.ReservationID, AssociationGeneration: 1, ReservationIdentity: association.ReservationIdentity,
		CleanupRequired: true, CleanupState: ProcessWorkspaceAssignmentCleanupRequired}
	job := terminalJob("job-required", "cmd:required", now.Add(-30*24*time.Hour))
	job.ProcessWorkspace = &assignment
	if _, _, err := st.CreateCommandJob(job); err != nil {
		t.Fatal(err)
	}
	if report := st.Compact(now, RetentionPolicy{TerminalTTL: time.Hour}); report.JobsPruned != 0 {
		t.Fatalf("required cleanup crossed retention deletion boundary: %+v", report)
	}
	if got := st.Jobs[job.ID].ProcessWorkspace; got == nil || got.CleanupState != ProcessWorkspaceAssignmentCleanupRequired {
		t.Fatalf("required cleanup intent was lost: %+v", got)
	}
}

func TestProcessWorkspaceCleanupRetentionFailsClosedUntilConfirmed(t *testing.T) {
	confirmed := ProcessWorkspaceAssignment{CleanupState: ProcessWorkspaceAssignmentCleanupConfirmed}
	tests := []struct {
		name       string
		assignment *ProcessWorkspaceAssignment
		wantKeep   bool
	}{
		{name: "no assignment", wantKeep: false},
		{name: "unmarked", assignment: &ProcessWorkspaceAssignment{}, wantKeep: true},
		{name: "required", assignment: &ProcessWorkspaceAssignment{CleanupRequired: true, CleanupState: ProcessWorkspaceAssignmentCleanupRequired}, wantKeep: true},
		{name: "pending", assignment: &ProcessWorkspaceAssignment{CleanupRequired: true, CleanupState: ProcessWorkspaceAssignmentCleanupPending}, wantKeep: true},
		{name: "unknown", assignment: &ProcessWorkspaceAssignment{CleanupState: ProcessWorkspaceCleanupState("future")}, wantKeep: true},
		{name: "inconsistent confirmed", assignment: &ProcessWorkspaceAssignment{CleanupRequired: true, CleanupState: ProcessWorkspaceAssignmentCleanupConfirmed}, wantKeep: true},
		{name: "confirmed", assignment: &confirmed, wantKeep: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			job := Job{ProcessWorkspace: test.assignment}
			if got := job.processWorkspaceCleanupUnconfirmed(); got != test.wantKeep {
				t.Fatalf("processWorkspaceCleanupUnconfirmed()=%v want %v", got, test.wantKeep)
			}
		})
	}
}

func TestCompactUnmarkedWorkspaceAssignmentRetainedUntilConfirmed(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	st := NewState()
	association := testProcessWorkspaceAssociation("ws-retention-unmarked", "PROCESS-020")
	if _, err := st.ProcessWorkspaces.Reserve(association); err != nil {
		t.Fatal(err)
	}
	st.ProcessWorkspaces.Generation = 1
	assignment := ProcessWorkspaceAssignment{ProcessID: association.ProcessID, WorkspaceID: association.WorkspaceID,
		ReservationID: association.ReservationID, AssociationGeneration: 1, ReservationIdentity: association.ReservationIdentity}
	job := terminalJob("job-unmarked", "cmd:unmarked", now.Add(-30*24*time.Hour))
	job.ProcessWorkspace = &assignment
	if _, _, err := st.CreateCommandJob(job); err != nil {
		t.Fatal(err)
	}
	if err := st.Validate(); err != nil {
		t.Fatalf("valid unmarked assignment rejected: %v", err)
	}
	policy := RetentionPolicy{TerminalTTL: time.Hour, MaxTerminalJobs: 1}
	if report := st.Compact(now, policy); report.JobsPruned != 0 {
		t.Fatalf("unmarked assignment crossed TTL/cap deletion boundary: %+v", report)
	}
	if _, ok := st.Jobs[job.ID]; !ok {
		t.Fatal("unmarked assignment job was deleted")
	}
	if got := st.Idempotency.CommandJobs[job.CommandIdempotencyKey]; got != job.ID {
		t.Fatalf("unmarked assignment lost idempotency index: %q", got)
	}

	if _, err := st.ProcessWorkspaces.Transition(association.WorkspaceID, association.ReservationID,
		ProcessWorkspaceAllocating, ProcessWorkspaceCleanupPending); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ProcessWorkspaces.ConfirmReleased(association.WorkspaceID, association.ReservationID); err != nil {
		t.Fatal(err)
	}
	confirmedJob := st.Jobs[job.ID]
	confirmed := *confirmedJob.ProcessWorkspace
	confirmed.CleanupState = ProcessWorkspaceAssignmentCleanupConfirmed
	confirmedJob.ProcessWorkspace = &confirmed
	if err := st.UpsertJob(confirmedJob); err != nil {
		t.Fatal(err)
	}
	if err := st.Validate(); err != nil {
		t.Fatalf("confirmed historical assignment rejected: %v", err)
	}
	if report := st.Compact(now, policy); report.JobsPruned != 1 {
		t.Fatalf("confirmed assignment did not cross deletion boundary: %+v", report)
	}
	if _, ok := st.Jobs[job.ID]; ok {
		t.Fatal("confirmed assignment job remained past retention boundary")
	}
	if _, ok := st.Idempotency.CommandJobs[job.CommandIdempotencyKey]; ok {
		t.Fatal("confirmed assignment deletion left idempotency index")
	}
}

func TestCompactConfirmedWorkspaceTombstoneAllowsABAAndRetentionDeletion(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	st := NewState()
	old := testProcessWorkspaceAssociation("ws-retention-aba", "PROCESS-OLD")
	if _, err := st.ProcessWorkspaces.Reserve(old); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ProcessWorkspaces.Transition(old.WorkspaceID, old.ReservationID,
		ProcessWorkspaceAllocating, ProcessWorkspaceCleanupPending); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ProcessWorkspaces.ConfirmReleased(old.WorkspaceID, old.ReservationID); err != nil {
		t.Fatal(err)
	}
	st.ProcessWorkspaces.Generation = 1
	assignment := ProcessWorkspaceAssignment{ProcessID: old.ProcessID, WorkspaceID: old.WorkspaceID,
		ReservationID: old.ReservationID, AssociationGeneration: 1, ReservationIdentity: old.ReservationIdentity,
		CleanupState: ProcessWorkspaceAssignmentCleanupConfirmed}
	job := terminalJob("job-confirmed", "cmd:confirmed", now.Add(-time.Hour))
	job.ProcessWorkspace = &assignment
	if _, _, err := st.CreateCommandJob(job); err != nil {
		t.Fatal(err)
	}
	if report := st.Compact(now, DefaultRetentionPolicy()); report.JobsTombstoned != 1 || report.JobsPruned != 0 {
		t.Fatalf("confirmed assignment did not tombstone inside retention: %+v", report)
	}

	newAttempt := testProcessWorkspaceAssociation(old.WorkspaceID, "PROCESS-NEW")
	newAttempt.Branch = "process/new"
	finalizeAssociation(&newAttempt)
	newAttempt.ReservationID = "reservation:new-retention-attempt"
	if _, err := st.ProcessWorkspaces.Reserve(newAttempt); err != nil {
		t.Fatal(err)
	}
	st.ProcessWorkspaces.Generation++
	if err := st.Validate(); err != nil {
		t.Fatalf("historical tombstone rejected after workspace reuse: %v", err)
	}
	path := filepath.Join(t.TempDir(), "state.json")
	if err := SaveFile(path, st); err != nil {
		t.Fatal(err)
	}
	reopened, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.Jobs[job.ID]; got.ExactProcessID != "PROCESS-020" || got.ProcessWorkspace == nil || *got.ProcessWorkspace != assignment {
		t.Fatalf("historical tombstone changed across reopen: %+v", got)
	}
	if current := reopened.ProcessWorkspaces.ByWorkspace[old.WorkspaceID]; current.ReservationID != newAttempt.ReservationID {
		t.Fatalf("new reservation changed across reopen: %+v", current)
	}
	if report := reopened.Compact(now.Add(8*24*time.Hour), DefaultRetentionPolicy()); report.JobsPruned != 1 {
		t.Fatalf("confirmed tombstone did not cross retention deletion boundary: %+v", report)
	}
	if _, ok := reopened.Jobs[job.ID]; ok {
		t.Fatal("confirmed tombstone remained after retention deletion boundary")
	}
	if _, ok := reopened.Idempotency.CommandJobs[job.CommandIdempotencyKey]; ok {
		t.Fatal("confirmed tombstone deletion left a dangling idempotency index")
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
