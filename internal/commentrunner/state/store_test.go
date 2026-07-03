package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/higress-group/issue-spec/internal/model"
)

func TestAtomicSaveLoadAndIdempotency(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	s := New(path)
	st := model.RunnerState{Version: 1, Repos: map[string]*model.RepoState{}}
	rs := EnsureRepo(&st, RepoKey("o", "r"))
	rs.Idempotency["cmd:/new:1"] = model.IdempotencyRecord{Key: "cmd:/new:1", Kind: "command", ResourceID: "job-1"}
	rs.PublicSessions[PublicSessionKey("o/r", "abc")] = &model.PublicSessionState{RepoID: "o/r", PublicSessionID: "abc", JobID: "job-1"}
	rs.Jobs["job-1"] = &model.JobState{ID: "job-1", Status: "queued"}
	if err := s.Save(st); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := LookupIdempotency(got.Repos[RepoKey("o", "r")], "cmd:/new:1"); !ok {
		t.Fatal("idempotency lookup failed")
	}
	if got.Repos[RepoKey("o", "r")].PublicSessions[PublicSessionKey("o/r", "abc")].JobID != "job-1" {
		t.Fatal("session lost")
	}
}

func TestLoadCorruptFileDiagnostics(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := New(path).Load()
	if err == nil || !strings.Contains(err.Error(), "decode runner state") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLockContention(t *testing.T) {
	dir := t.TempDir()
	s := New(filepath.Join(dir, "state.json"))
	var entered bool
	err := s.WithLock(func() error {
		entered = true
		return s.WithLock(func() error { return nil })
	})
	if err == nil || !strings.Contains(err.Error(), "lock busy") {
		t.Fatalf("unexpected err: %v", err)
	}
	if !entered {
		t.Fatal("lock callback not entered")
	}
}

func TestLifecycleHelpersAndZeroValues(t *testing.T) {
	st := model.RunnerState{}
	rs := EnsureRepo(&st, "o/r")
	if rs == nil || st.Repos["o/r"] == nil {
		t.Fatal("repo not created")
	}
	if _, ok := LookupIdempotency(rs, "missing"); ok {
		t.Fatal("expected miss")
	}
	if rs.FirstObservedComments == nil || rs.Jobs == nil || rs.PublicSessions == nil || rs.Workspaces == nil || rs.Sandboxes == nil || rs.Acpx == nil || rs.StatusComments == nil || rs.CLIProvenance == nil || rs.Idempotency == nil || rs.Cancellation == nil {
		t.Fatal("zero value maps not initialized")
	}
	rs.Jobs["job-1"] = &model.JobState{ID: "job-1", Status: "queued", CreatedAt: time.Now()}
	rs.Jobs["job-1"].Status = "running"
	if rs.Jobs["job-1"].Status != "running" {
		t.Fatal("transition failed")
	}
}
