package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/commentrunner"
	"github.com/higress-group/issue-spec/internal/commentrunner/intake"
	"github.com/higress-group/issue-spec/internal/commentrunner/jobs"
	crstate "github.com/higress-group/issue-spec/internal/commentrunner/state"
	"github.com/higress-group/issue-spec/internal/commentrunner/storage"
)

func seedStorageFixture(t *testing.T) (statePath, workspaceRoot, retiredHash string) {
	t.Helper()
	base := t.TempDir()
	statePath = filepath.Join(base, "state.json")
	workspaceRoot = filepath.Join(base, "workspaces")
	if err := os.MkdirAll(workspaceRoot, 0o700); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}
	wsPath := filepath.Join(workspaceRoot, "ws-gone")
	st := crstate.NewState()
	if err := st.UpsertPublicSession(crstate.PublicSession{
		Repo: "o/r", PublicSessionID: "ps-1", AcpxRecordID: "rec-1",
		Status:    crstate.StatusCompleted,
		Workspace: crstate.WorkspaceMetadata{ID: "ws-gone", Path: wsPath, Repo: "o/r"},
		CreatedAt: time.Now().Add(-72 * time.Hour), LastUsedAt: time.Now().Add(-48 * time.Hour),
	}); err != nil {
		t.Fatalf("upsert session: %v", err)
	}
	if err := crstate.SaveFile(statePath, st); err != nil {
		t.Fatalf("save state: %v", err)
	}
	hash, err := storage.SessionRuntimeHash("o/r", "ps-1", wsPath)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(workspaceRoot, ".sessions", hash), 0o700); err != nil {
		t.Fatalf("mkdir runtime: %v", err)
	}
	return statePath, workspaceRoot, hash
}

func TestRunnerStorageReconcileDryRun(t *testing.T) {
	statePath, workspaceRoot, hash := seedStorageFixture(t)
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	code := app.runRunner(context.Background(), []string{"storage", "reconcile",
		"--state", statePath, "--workspace-root", workspaceRoot, "--dry-run"})
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, errOut.String())
	}
	text := out.String()
	if !strings.Contains(text, "retired_known") || !strings.Contains(text, "would_delete") {
		t.Fatalf("dry-run output missing classification:\n%s", text)
	}
	if _, err := os.Lstat(filepath.Join(workspaceRoot, ".sessions", hash)); err != nil {
		t.Fatalf("dry-run must not delete: %v", err)
	}
}

func TestRunnerStorageReconcileApplyDeletesRetired(t *testing.T) {
	statePath, workspaceRoot, hash := seedStorageFixture(t)
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	code := app.runRunner(context.Background(), []string{"storage", "reconcile",
		"--state", statePath, "--workspace-root", workspaceRoot, "--apply"})
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, errOut.String())
	}
	if _, err := os.Lstat(filepath.Join(workspaceRoot, ".sessions", hash)); !os.IsNotExist(err) {
		t.Fatalf("apply must delete retired runtime, err=%v", err)
	}
	text := out.String()
	if !strings.Contains(text, "deleted") {
		t.Fatalf("apply output missing deleted action:\n%s", text)
	}
	// Idempotent second run.
	var out2, errOut2 bytes.Buffer
	app2 := newApp(strings.NewReader(""), &out2, &errOut2)
	if code := app2.runRunner(context.Background(), []string{"storage", "reconcile",
		"--state", statePath, "--workspace-root", workspaceRoot, "--apply"}); code != 0 {
		t.Fatalf("second apply exit=%d stderr=%q", code, errOut2.String())
	}
}

func TestRunnerStorageReconcileRequiresMode(t *testing.T) {
	statePath, workspaceRoot, _ := seedStorageFixture(t)
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	if code := app.runRunner(context.Background(), []string{"storage", "reconcile",
		"--state", statePath, "--workspace-root", workspaceRoot}); code != 2 {
		t.Fatalf("exit=%d, want 2 without --dry-run/--apply", code)
	}
	var out2, errOut2 bytes.Buffer
	app2 := newApp(strings.NewReader(""), &out2, &errOut2)
	if code := app2.runRunner(context.Background(), []string{"storage", "reconcile",
		"--state", statePath, "--workspace-root", workspaceRoot, "--dry-run", "--apply"}); code != 2 {
		t.Fatalf("exit=%d, want 2 with both modes", code)
	}
}

func TestRunnerStorageReconcileJSON(t *testing.T) {
	statePath, workspaceRoot, hash := seedStorageFixture(t)
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	code := app.runRunner(context.Background(), []string{"storage", "reconcile",
		"--state", statePath, "--workspace-root", workspaceRoot, "--dry-run", "--json"})
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, errOut.String())
	}
	var report storage.Report
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("decode report: %v\n%s", err, out.String())
	}
	if !report.DryRun {
		t.Fatalf("report not marked dry-run")
	}
	found := false
	for _, r := range report.Resources {
		if r.Class == storage.ClassRetiredKnown && r.Action == storage.ActionWouldDelete && r.Hash == hash {
			found = true
		}
	}
	if !found {
		t.Fatalf("retired runtime missing from JSON report: %+v", report.Resources)
	}
}

func TestRunnerStorageReconcileOwnerConflict(t *testing.T) {
	statePath, workspaceRoot, _ := seedStorageFixture(t)
	owner, err := storage.AcquireOwner(workspaceRoot)
	if err != nil {
		t.Fatalf("AcquireOwner: %v", err)
	}
	defer owner.Release()
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	code := app.runRunner(context.Background(), []string{"storage", "reconcile",
		"--state", statePath, "--workspace-root", workspaceRoot, "--apply"})
	if code == 0 {
		t.Fatalf("owner conflict must fail, stdout=%q", out.String())
	}
	if !strings.Contains(errOut.String(), "stop the old runner") {
		t.Fatalf("missing stop-old-runner diagnostic: %q", errOut.String())
	}
}

func TestRunnerPollOwnerConflictFails(t *testing.T) {
	statePath, workspaceRoot, _ := seedStorageFixture(t)
	owner, err := storage.AcquireOwner(workspaceRoot)
	if err != nil {
		t.Fatalf("AcquireOwner: %v", err)
	}
	defer owner.Release()
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	app.runnerPreflight = func(context.Context, commentrunner.Config) commentrunner.PreflightReport {
		return commentrunner.PreflightReport{OK: true}
	}
	app.runnerReconcile = func(context.Context, commentrunner.Config) (jobs.ReconcileResult, error) {
		return jobs.ReconcileResult{}, nil
	}
	app.runnerIntake = func(context.Context, commentrunner.Config, intake.Options) (intake.Result, error) {
		return intake.Result{OK: true}, nil
	}
	app.runnerDispatch = func(context.Context, commentrunner.Config) (jobs.Result, error) {
		return jobs.Result{}, nil
	}
	code := app.runRunner(context.Background(), []string{"poll", "--repo", "o/r", "--runner", "bot",
		"--state", statePath, "--workspace-root", workspaceRoot, "--once", "--json"})
	if code == 0 {
		t.Fatalf("poll with conflicting owner must fail, stdout=%q", out.String())
	}
	if !strings.Contains(errOut.String(), "stop the old runner") && !strings.Contains(out.String(), "stop the old runner") {
		t.Fatalf("missing stop-old-runner diagnostic: stdout=%q stderr=%q", out.String(), errOut.String())
	}
}

func TestRunnerPollAcquiresOwnerAndReleases(t *testing.T) {
	statePath, workspaceRoot, _ := seedStorageFixture(t)
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	app.runnerPreflight = func(context.Context, commentrunner.Config) commentrunner.PreflightReport {
		return commentrunner.PreflightReport{OK: true}
	}
	reconcileCalled := false
	app.runnerReconcile = func(context.Context, commentrunner.Config) (jobs.ReconcileResult, error) {
		reconcileCalled = true
		return jobs.ReconcileResult{}, nil
	}
	app.runnerIntake = func(context.Context, commentrunner.Config, intake.Options) (intake.Result, error) {
		return intake.Result{OK: true}, nil
	}
	app.runnerDispatch = func(context.Context, commentrunner.Config) (jobs.Result, error) {
		return jobs.Result{}, nil
	}
	code := app.runRunner(context.Background(), []string{"poll", "--repo", "o/r", "--runner", "bot",
		"--state", statePath, "--workspace-root", workspaceRoot, "--once", "--json"})
	if code != 0 {
		t.Fatalf("poll exit=%d stderr=%q", code, errOut.String())
	}
	if !reconcileCalled {
		t.Fatalf("poll reconcile was not invoked")
	}
	// Poll exited: the root is ownable again.
	owner, err := storage.AcquireOwner(workspaceRoot)
	if err != nil {
		t.Fatalf("owner must be released after poll: %v", err)
	}
	defer owner.Release()
}

func TestRunnerPollSharesStorageServiceAcrossSyncCycles(t *testing.T) {
	statePath, workspaceRoot, _ := seedStorageFixture(t)
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	app.runnerPreflight = func(context.Context, commentrunner.Config) commentrunner.PreflightReport {
		return commentrunner.PreflightReport{OK: true}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var services []*storage.Service
	var missing []int
	calls := 0
	app.runnerReconcile = func(callCtx context.Context, cfg commentrunner.Config) (jobs.ReconcileResult, error) {
		calls++
		svc, ok := storage.ServiceFromContext(callCtx, cfg.WorkspaceRoot)
		if !ok {
			missing = append(missing, calls)
		} else {
			services = append(services, svc)
		}
		if calls == 2 {
			cancel()
		}
		return jobs.ReconcileResult{}, nil
	}
	app.runnerIntake = func(context.Context, commentrunner.Config, intake.Options) (intake.Result, error) {
		return intake.Result{OK: true}, nil
	}
	app.runnerDispatch = func(context.Context, commentrunner.Config) (jobs.Result, error) {
		return jobs.Result{}, nil
	}
	code := app.runRunner(ctx, []string{"poll", "--repo", "o/r", "--runner", "bot",
		"--state", statePath, "--workspace-root", workspaceRoot, "--sync-dispatch", "--json"})
	if code != 0 {
		t.Fatalf("poll exit=%d stderr=%q", code, errOut.String())
	}
	if calls < 2 {
		t.Fatalf("want at least two sync cycles, got %d", calls)
	}
	if len(missing) > 0 {
		t.Fatalf("cycles %v ran without a shared storage service; cooldown and backoff state reset per cycle", missing)
	}
	if services[0] != services[1] {
		t.Fatalf("each sync cycle built its own storage service; cooldown and backoff state lost")
	}
}

func TestAsyncBusyCleanupUsesStorageEngine(t *testing.T) {
	statePath, workspaceRoot, hash := seedStorageFixture(t)
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	cfg := commentrunner.Config{
		Hostname:           "github.com",
		Repositories:       []string{"o/r"},
		RunnerIdentity:     "bot",
		StatePath:          statePath,
		WorkspaceRoot:      workspaceRoot,
		WorkspaceRetention: commentrunner.NewDuration(7 * 24 * time.Hour),
		StorageOrphanGrace: commentrunner.NewDuration(storage.DefaultOrphanGrace),
	}
	result, err := app.runRunnerAsyncReconcileWithStore(context.Background(), cfg, nil, true)
	if err != nil {
		t.Fatalf("async-busy reconcile: %v stderr=%q", err, errOut.String())
	}
	if result.StorageCleanup == nil {
		t.Fatalf("async-busy cleanup must include the storage report")
	}
	found := false
	for _, r := range result.StorageCleanup.Resources {
		if r.Class == storage.ClassRetiredKnown && r.Action == storage.ActionDeleted && r.Hash == hash {
			found = true
		}
	}
	if !found {
		t.Fatalf("async-busy cleanup made different decisions: %+v", result.StorageCleanup.Resources)
	}
	if _, err := os.Lstat(filepath.Join(workspaceRoot, ".sessions", hash)); !os.IsNotExist(err) {
		t.Fatalf("async-busy cleanup must delete the retired runtime, err=%v", err)
	}
}

func TestAsyncReconcileMergePreservesStorageCleanup(t *testing.T) {
	statePath, workspaceRoot, hash := seedStorageFixture(t)
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	app.runnerReconcile = func(context.Context, commentrunner.Config) (jobs.ReconcileResult, error) {
		return jobs.ReconcileResult{Reconciled: 1}, nil
	}
	cfg := commentrunner.Config{
		Hostname:           "github.com",
		Repositories:       []string{"o/r"},
		RunnerIdentity:     "bot",
		StatePath:          statePath,
		WorkspaceRoot:      workspaceRoot,
		WorkspaceRetention: commentrunner.NewDuration(7 * 24 * time.Hour),
		StorageOrphanGrace: commentrunner.NewDuration(storage.DefaultOrphanGrace),
	}
	result, err := app.runRunnerAsyncReconcileWithStore(context.Background(), cfg, nil, false)
	if err != nil {
		t.Fatalf("async reconcile: %v stderr=%q", err, errOut.String())
	}
	if result.Reconciled != 1 {
		t.Fatalf("reconcile-hook result lost: %+v", result)
	}
	if result.StorageCleanup == nil {
		t.Fatalf("merged result dropped the cleanup pass storage report")
	}
	found := false
	for _, r := range result.StorageCleanup.Resources {
		if r.Class == storage.ClassRetiredKnown && r.Action == storage.ActionDeleted && r.Hash == hash {
			found = true
		}
	}
	if !found {
		t.Fatalf("merged storage report made different decisions: %+v", result.StorageCleanup.Resources)
	}
	if _, err := os.Lstat(filepath.Join(workspaceRoot, ".sessions", hash)); !os.IsNotExist(err) {
		t.Fatalf("cleanup pass must still delete the retired runtime, err=%v", err)
	}
}

func serveTestProfile(t *testing.T) auth.Profile {
	t.Helper()
	t.Setenv("ISSUE_SPEC_CONFIG_DIR", t.TempDir())
	profile := auth.Profile{Name: "runner-storage-test", Kind: auth.ProfileKindHosted,
		APIURL: "https://issues.example.test/api/v3", NativeAPIURL: "https://issues.example.test/api/v1",
		WebURL: "https://issues.example.test", ServerInstanceID: "runner-instance"}
	if err := auth.SaveProfile(profile, false); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RUNNER_SERVE_SECRET", strings.Repeat("s", 32))
	t.Setenv("ISSUE_SPEC_TOKEN", "origin-bound-profile-token")
	return profile
}

func TestRunnerServeOwnerConflictFails(t *testing.T) {
	profile := serveTestProfile(t)
	statePath, workspaceRoot, _ := seedStorageFixture(t)
	owner, err := storage.AcquireOwner(workspaceRoot)
	if err != nil {
		t.Fatalf("AcquireOwner: %v", err)
	}
	defer owner.Release()

	originalBuild := runnerServeBuildRuntime
	runnerServeBuildRuntime = func(context.Context, runnerServeRuntimeInput) (runnerServeRuntime, error) {
		t.Fatal("runtime must not build when ownership conflicts")
		return nil, nil
	}
	t.Cleanup(func() { runnerServeBuildRuntime = originalBuild })

	var stdout, stderr bytes.Buffer
	app := newApp(strings.NewReader(""), &stdout, &stderr)
	app.profileName = profile.Name
	app.runnerPreflight = func(_ context.Context, cfg commentrunner.Config) commentrunner.PreflightReport {
		return commentrunner.PreflightReport{OK: true, Config: cfg}
	}
	code := app.runRunner(context.Background(), []string{"serve", "--repo", "o/r", "--runner", "runner-bot",
		"--state", statePath, "--workspace-root", workspaceRoot,
		"--subscription-id", "11111111-1111-1111-1111-111111111111", "--secret-env", "RUNNER_SERVE_SECRET",
		"--git-credential-command", "/usr/bin/true"})
	if code == 0 {
		t.Fatalf("serve with conflicting owner must fail, stdout=%q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "stop the old runner") {
		t.Fatalf("missing stop-old-runner diagnostic: %q", stderr.String())
	}
}

func TestRunnerServeHoldsOwnerAndWiresStorage(t *testing.T) {
	profile := serveTestProfile(t)
	statePath, workspaceRoot, _ := seedStorageFixture(t)

	originalBuild, originalRun := runnerServeBuildRuntime, runnerServeRun
	runnerServeBuildRuntime = func(_ context.Context, input runnerServeRuntimeInput) (runnerServeRuntime, error) {
		if input.Storage == nil {
			t.Fatal("serve runtime missing storage lifecycle")
		}
		// The owner lock is held for the serve lifetime.
		if _, err := storage.AcquireOwner(workspaceRoot); err == nil {
			t.Fatal("serve must hold the root owner while running")
		}
		return runnerServeRuntimeFunc(func(context.Context) error { return nil }), nil
	}
	runnerServeRun = func(ctx context.Context, runtime runnerServeRuntime) error { return runtime.Run(ctx) }
	t.Cleanup(func() { runnerServeBuildRuntime, runnerServeRun = originalBuild, originalRun })

	var stdout, stderr bytes.Buffer
	app := newApp(strings.NewReader(""), &stdout, &stderr)
	app.profileName = profile.Name
	app.runnerPreflight = func(_ context.Context, cfg commentrunner.Config) commentrunner.PreflightReport {
		return commentrunner.PreflightReport{OK: true, Config: cfg}
	}
	code := app.runRunner(context.Background(), []string{"serve", "--repo", "o/r", "--runner", "runner-bot",
		"--state", statePath, "--workspace-root", workspaceRoot,
		"--subscription-id", "11111111-1111-1111-1111-111111111111", "--secret-env", "RUNNER_SERVE_SECRET",
		"--git-credential-command", "/usr/bin/true"})
	if code != 0 {
		t.Fatalf("serve exit=%d stderr=%q", code, stderr.String())
	}
	// Serve exited: the root is ownable again.
	owner, err := storage.AcquireOwner(workspaceRoot)
	if err != nil {
		t.Fatalf("owner must be released after serve: %v", err)
	}
	defer owner.Release()
}

func TestRunnerStorageLifecycleFailsClosed(t *testing.T) {
	// A workspace root that is a regular file cannot host the sidecar:
	// construction fails and the lifecycle must surface errors, never nil.
	fileRoot := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(fileRoot, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := commentrunner.Config{WorkspaceRoot: fileRoot, StatePath: filepath.Join(t.TempDir(), "state.json")}
	lifecycle := runnerStorageLifecycle(context.Background(), cfg, nil)
	if lifecycle == nil {
		t.Fatal("lifecycle must fail closed, not disable storage")
	}
	if err := lifecycle.AdmitDispatch(context.Background()); err == nil {
		t.Fatal("admission must surface the construction error")
	}
	if err := lifecycle.RecordSessionResources(context.Background(), "o/r", "ps-1", "/tmp/ws"); err == nil {
		t.Fatal("recording must surface the construction error")
	}
	if _, err := lifecycle.ReconcileStorage(context.Background(), true, false); err == nil {
		t.Fatal("reconcile must surface the construction error")
	}
}
