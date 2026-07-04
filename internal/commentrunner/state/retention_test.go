package state

import (
	"testing"
	"time"
)

func TestRetentionPolicyApplyTerminalJobs(t *testing.T) {
	now := time.Now().UTC()

	tests := []struct {
		name           string
		state          RunnerState
		config         RetentionConfig
		expectedPruned int
		expectedTomb   int
	}{
		{
			name: "prunes old jobs beyond max count",
			state: RunnerState{
				Jobs: map[string]Job{
					"job-1": {ID: "job-1", Status: StatusCompleted, FinishedAt: now.Add(-50 * 24 * time.Hour), CommandIdempotencyKey: "key-1"},
					"job-2": {ID: "job-2", Status: StatusCompleted, FinishedAt: now.Add(-40 * 24 * time.Hour), CommandIdempotencyKey: "key-2"},
				},
				Idempotency: IdempotencyIndex{
					CommandJobs: map[string]string{
						"key-1": "job-1",
						"key-2": "job-2",
					},
				},
			},
			config: RetentionConfig{
				TerminalJobsMaxCount: 1,
				TerminalJobsMaxAge:  func() *time.Duration { d := 30 * 24 * time.Hour; return &d }(),
			},
			expectedPruned: 0,
			expectedTomb:   1,
		},
		{
			name: "keeps recent jobs within age limit",
			state: RunnerState{
				Jobs: map[string]Job{
					"job-1": {ID: "job-1", Status: StatusCompleted, FinishedAt: now.Add(-5 * 24 * time.Hour)},
					"job-2": {ID: "job-2", Status: StatusCompleted, FinishedAt: now.Add(-3 * 24 * time.Hour)},
				},
			},
			config: RetentionConfig{
				TerminalJobsMaxAge: func() *time.Duration { d := 10 * 24 * time.Hour; return &d }(),
			},
			expectedPruned: 0,
			expectedTomb:   0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			policy := RetentionPolicy{
				Config: tt.config,
				Now:    now,
			}
			report, _ := policy.Apply(&tt.state)

			if report.JobsPruned != tt.expectedPruned {
				t.Errorf("JobsPruned = %v, want %v", report.JobsPruned, tt.expectedPruned)
			}
			if report.JobsTombstoned != tt.expectedTomb {
				t.Errorf("JobsTombstoned = %v, want %v", report.JobsTombstoned, tt.expectedTomb)
			}
		})
	}
}

func TestRetentionPolicyPreservesIdempotency(t *testing.T) {
	now := time.Now().UTC()
	idempotencyKey := "cmd-123"

	state := RunnerState{
		Jobs: map[string]Job{
			"job-old": {
				ID:                    "job-old",
				Status:                StatusCompleted,
				FinishedAt:            now.Add(-100 * 24 * time.Hour),
				CommandIdempotencyKey: idempotencyKey,
				CreatedAt:             now.Add(-110 * 24 * time.Hour),
			},
			"job-new": {
				ID:                    "job-new",
				Status:                StatusCompleted,
				FinishedAt:            now.Add(-5 * 24 * time.Hour),
				CreatedAt:             now.Add(-10 * 24 * time.Hour),
			},
		},
		Idempotency: IdempotencyIndex{
			CommandJobs: map[string]string{
				idempotencyKey: "job-old",
			},
		},
	}

	policy := RetentionPolicy{
		Config: RetentionConfig{
			TerminalJobsMaxCount: 1, // Keep only 1 job by count
			TerminalJobsMaxAge:   func() *time.Duration { d := 30 * 24 * time.Hour; return &d }(),
		},
		Now: now,
	}

	report, _ := policy.Apply(&state)

	// job-new should be kept (newest, within count limit)
	_, exists := state.Jobs["job-new"]
	if !exists {
		t.Error("Newest job should still exist")
	}

	// job-old should be tombstoned (beyond count limit, has idempotency key)
	if report.JobsTombstoned != 1 {
		t.Errorf("JobsTombstoned = %d, want 1", report.JobsTombstoned)
	}

	tombstone, exists := state.Jobs["job-old"]
	if !exists {
		t.Fatal("Old job should still exist as tombstone")
	}
	if tombstone.CommandIdempotencyKey != idempotencyKey {
		t.Errorf("Tombstone should preserve idempotency key, got %v", tombstone.CommandIdempotencyKey)
	}
	if len(tombstone.Diagnostics) == 0 {
		t.Error("Tombstone should have diagnostics")
	}
}

func TestRetentionPolicyAppliesToSessions(t *testing.T) {
	now := time.Now().UTC()

	state := RunnerState{
		PublicSessions: map[string]PublicSession{
			"higress-group/issue-spec#session-1": {
				Repo:          "higress-group/issue-spec",
				PublicSessionID: "session-1",
				Status:        StatusCompleted,
				LastUsedAt:    now.Add(-50 * 24 * time.Hour),
			},
			"higress-group/issue-spec#session-2": {
				Repo:          "higress-group/issue-spec",
				PublicSessionID: "session-2",
				Status:        StatusCompleted,
				LastUsedAt:    now.Add(-5 * 24 * time.Hour),
			},
		},
	}

	policy := RetentionPolicy{
		Config: RetentionConfig{
			TerminalSessionsMaxCount: 1,
		},
		Now: now,
	}

	report, _ := policy.Apply(&state)

	if report.SessionsPruned != 1 {
		t.Errorf("SessionsPruned = %v, want 1", report.SessionsPruned)
	}

	// Newer session should still exist
	_, exists := state.PublicSessions["higress-group/issue-spec#session-2"]
	if !exists {
		t.Error("Newer session should still exist")
	}
}

func TestDefaultRetentionValues(t *testing.T) {
	if DefaultTerminalJobsRetention != 30*24*time.Hour {
		t.Errorf("DefaultTerminalJobsRetention = %v, want 720h", DefaultTerminalJobsRetention)
	}
	if DefaultTerminalJobsMaxCount != 1000 {
		t.Errorf("DefaultTerminalJobsMaxCount = %v, want 1000", DefaultTerminalJobsMaxCount)
	}
	if DefaultTerminalSessionsRetention != 30*24*time.Hour {
		t.Errorf("DefaultTerminalSessionsRetention = %v, want 720h", DefaultTerminalSessionsRetention)
	}
	if DefaultCancellationsRetention != 7*24*time.Hour {
		t.Errorf("DefaultCancellationsRetention = %v, want 168h", DefaultCancellationsRetention)
	}
}
