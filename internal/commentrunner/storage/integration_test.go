package storage_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/higress-group/issue-spec/internal/commentrunner/state"
	"github.com/higress-group/issue-spec/internal/commentrunner/storage"
	"github.com/higress-group/issue-spec/internal/processworkspace"
	"github.com/higress-group/issue-spec/internal/workspace"
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
}

func gitHead(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func initClone(t *testing.T, root, id string) string {
	t.Helper()
	clone := filepath.Join(root, id)
	if err := os.MkdirAll(clone, 0o700); err != nil {
		t.Fatal(err)
	}
	gitRun(t, clone, "init", "-b", "main")
	gitRun(t, clone, "config", "user.name", "Test User")
	gitRun(t, clone, "config", "user.email", "test@example.com")
	gitRun(t, clone, "remote", "add", "origin", "https://github.com/o/r.git")
	if err := os.WriteFile(filepath.Join(clone, "README.md"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, clone, "add", "README.md")
	gitRun(t, clone, "commit", "-m", "base")
	return clone
}

func terminalSession(repo, sid, wsPath string) state.PublicSession {
	return state.PublicSession{
		Repo: repo, PublicSessionID: sid, AcpxRecordID: "rec-" + sid,
		Status:    state.StatusCompleted,
		Workspace: state.WorkspaceMetadata{ID: filepath.Base(wsPath), Path: wsPath, Repo: repo},
		Acpx:      state.AcpxMetadata{StableRecordID: "rec-" + sid, CWD: wsPath},
		CreatedAt: time.Now().Add(-72 * time.Hour), LastUsedAt: time.Now().Add(-48 * time.Hour),
	}
}

func engineFor(t *testing.T, root string, st state.RunnerState) *storage.Engine {
	t.Helper()
	engine, err := storage.NewEngine(storage.EngineConfig{
		WorkspaceRoot: root,
		StateLoader:   func(context.Context) (state.RunnerState, error) { return st, nil },
		PoolInspector: func(ctx context.Context, integrationRoot, poolRoot string) (storage.PoolInspection, error) {
			return processworkspace.InspectPool(ctx, integrationRoot, poolRoot, processworkspace.ManagerOptions{})
		},
		PoolRemover: func(ctx context.Context, integrationRoot, poolRoot string) (storage.PoolInspection, bool, error) {
			return processworkspace.RemoveEmptyPool(ctx, integrationRoot, poolRoot, processworkspace.ManagerOptions{})
		},
	})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	return engine
}

func TestEngineWithRealProcessPoolInspection(t *testing.T) {
	requireGit(t)
	root := t.TempDir()
	clone := initClone(t, root, "ws-clone")
	st := state.NewState()
	if err := st.UpsertPublicSession(terminalSession("o/r", "ps-1", clone)); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	runtimeHash, err := storage.SessionRuntimeHash("o/r", "ps-1", clone)
	if err != nil {
		t.Fatalf("runtime hash: %v", err)
	}
	runtimeDir := filepath.Join(root, ".sessions", runtimeHash)
	if err := os.MkdirAll(runtimeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	poolHash, err := storage.SessionProcessPoolHash("o/r", "ps-1", clone)
	if err != nil {
		t.Fatalf("pool hash: %v", err)
	}
	poolDir := filepath.Join(root, ".process-workspaces", poolHash)
	if err := os.MkdirAll(poolDir, 0o700); err != nil {
		t.Fatal(err)
	}

	engine := engineFor(t, root, st)
	report, err := engine.Reconcile(context.Background(), storage.ReconcileOptions{Apply: true, OrphanGrace: storage.DefaultOrphanGrace})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	// Runtime: terminal session but workspace present -> protected anchor.
	if _, err := os.Lstat(runtimeDir); err != nil {
		t.Fatalf("valid-workspace runtime must remain: %v", err)
	}
	// Pool: retired session, real inspection proves empty -> removed.
	if _, err := os.Lstat(poolDir); !os.IsNotExist(err) {
		t.Fatalf("proven-empty pool must be removed, err=%v", err)
	}
	var poolReport *storage.ResourceReport
	for i := range report.Resources {
		if report.Resources[i].Kind == storage.ResourceKindSessionProcessPool {
			poolReport = &report.Resources[i]
		}
	}
	if poolReport == nil || poolReport.Action != storage.ActionDeleted {
		t.Fatalf("pool report=%+v", poolReport)
	}
}

func TestEnginePreservesPoolWithRealActiveLease(t *testing.T) {
	requireGit(t)
	root := t.TempDir()
	clone := initClone(t, root, "ws-clone")
	st := state.NewState()
	if err := st.UpsertPublicSession(terminalSession("o/r", "ps-1", clone)); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	poolHash, err := storage.SessionProcessPoolHash("o/r", "ps-1", clone)
	if err != nil {
		t.Fatalf("pool hash: %v", err)
	}
	poolDir := filepath.Join(root, ".process-workspaces", poolHash)
	manager, err := processworkspace.OpenManager(context.Background(), clone, poolDir, processworkspace.ManagerOptions{})
	if err != nil {
		t.Fatalf("OpenManager: %v", err)
	}
	now := time.Now().UTC()
	lease := processworkspace.LocalLease{
		Portable: processworkspace.PortableLease{
			SchemaVersion: processworkspace.LeaseSchemaVersion,
			WorkspaceID:   "pw-a", Repository: "o/r", ProcessID: "PROCESS-001",
			ExecutionClass: processworkspace.ExecutionChangeBearing, Mode: processworkspace.ModeWritable,
			BaseSHA: gitHead(t, clone), Branch: "process-a", WriteOwnership: []string{"docs/**"},
			RuntimeNamespace: "pw-a", State: processworkspace.StatePreparing,
			CreatedAt: now, UpdatedAt: now,
		},
		Owner:         processworkspace.LeaseOwner{CoordinatorID: "coordinator", Token: "token-pw-a", AcquiredAt: now},
		LocalRevision: 1,
	}
	if _, err := manager.Prepare(context.Background(), processworkspace.PrepareRequest{Lease: lease}); err != nil {
		t.Fatalf("Prepare lease: %v", err)
	}

	engine := engineFor(t, root, st)
	report, err := engine.Reconcile(context.Background(), storage.ReconcileOptions{Apply: true, OrphanGrace: storage.DefaultOrphanGrace})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if _, err := os.Lstat(poolDir); err != nil {
		t.Fatalf("active pool must be preserved: %v", err)
	}
	foundPreserved := false
	for _, r := range report.Resources {
		if r.Kind == storage.ResourceKindSessionProcessPool && r.Action == storage.ActionPreserved {
			foundPreserved = true
		}
	}
	if !foundPreserved {
		t.Fatalf("active pool must be preserved with remediation: %+v", report.Resources)
	}
}

func TestResumeFailsBeforeAndAfterRuntimeRemoval(t *testing.T) {
	requireGit(t)
	root := t.TempDir()
	missingWS := filepath.Join(root, "ws-gone")
	session := terminalSession("o/r", "ps-1", missingWS)
	st := state.NewState()
	if err := st.UpsertPublicSession(session); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	runtimeHash, err := storage.SessionRuntimeHash("o/r", "ps-1", missingWS)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	runtimeDir := filepath.Join(root, ".sessions", runtimeHash)
	if err := os.MkdirAll(runtimeDir, 0o700); err != nil {
		t.Fatal(err)
	}

	manager := workspace.Manager{Root: root, Retention: 7 * 24 * time.Hour}
	_, resumeErrBefore := manager.ResolveResume(context.Background(), workspace.ResumeRequest{
		Repo: "o/r", Workspace: session.Workspace,
	})
	if resumeErrBefore == nil {
		t.Fatalf("ResolveResume must already fail for the missing workspace before runtime removal")
	}

	engine := engineFor(t, root, st)
	report, err := engine.Reconcile(context.Background(), storage.ReconcileOptions{Apply: true, OrphanGrace: storage.DefaultOrphanGrace})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if _, err := os.Lstat(runtimeDir); !os.IsNotExist(err) {
		t.Fatalf("retired runtime must be removed, err=%v", err)
	}
	if report.ReclaimedBytes != 0 {
		t.Fatalf("reclaimed=%d", report.ReclaimedBytes)
	}
	// The session is no more resumable afterward, and its ACPX identity in
	// state is untouched (TTL pruning remains the only session lifecycle).
	_, resumeErrAfter := manager.ResolveResume(context.Background(), workspace.ResumeRequest{
		Repo: "o/r", Workspace: session.Workspace,
	})
	if resumeErrAfter == nil {
		t.Fatalf("ResolveResume must still fail after runtime removal")
	}
	reloaded, ok := st.GetPublicSession("o/r", "ps-1")
	if !ok || reloaded.AcpxRecordID != session.AcpxRecordID {
		t.Fatalf("session identity changed: %+v ok=%v", reloaded, ok)
	}
}
