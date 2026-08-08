package storage

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/higress-group/issue-spec/internal/commentrunner/state"
)

type migrateFixture struct {
	t         *testing.T
	root      string
	svc       *Service
	st        state.RunnerState
	scope     RuntimeScope
	scopeHash string
	rawPath   string
}

func writeContent(t *testing.T, path, content string, perm os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), perm); err != nil {
		t.Fatalf("write %q: %v", path, err)
	}
}

func newMigrateFixture(t *testing.T) *migrateFixture {
	t.Helper()
	root := testRoot(t)
	scope := testScope()
	hash, err := RuntimeScopeHash(scope)
	if err != nil {
		t.Fatalf("scope hash: %v", err)
	}
	f := &migrateFixture{
		t:         t,
		root:      root,
		st:        state.NewState(),
		scope:     scope,
		scopeHash: hash,
		rawPath:   filepath.Join(root, "runner-state.json"),
	}
	writeContent(t, f.rawPath, `{"schema_version":6}`, 0o600)
	svc, err := NewService(ServiceConfig{
		WorkspaceRoot: root,
		StateLoader:   func(context.Context) (state.RunnerState, error) { return f.st, nil },
		RawStatePath:  f.rawPath,
		OrphanGrace:   DefaultOrphanGrace,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	f.svc = svc
	return f
}

func (f *migrateFixture) addSession(sid string) string {
	f.t.Helper()
	wsPath := filepath.Join(f.root, "ws-"+sid)
	if err := os.MkdirAll(wsPath, 0o700); err != nil {
		f.t.Fatalf("mkdir workspace: %v", err)
	}
	session := terminalSession(f.scope.Repo, sid, "ws-"+sid, wsPath, time.Now().Add(-time.Hour))
	if err := f.st.UpsertPublicSession(session); err != nil {
		f.t.Fatalf("upsert session: %v", err)
	}
	return wsPath
}

func (f *migrateFixture) legacyRoot(sid string) string {
	f.t.Helper()
	session := f.st.PublicSessions[state.PublicSessionKey(f.scope.Repo, sid)]
	root, err := SessionRuntimeRoot(session.Workspace.Path, f.scope.Repo, sid)
	if err != nil {
		f.t.Fatalf("legacy root: %v", err)
	}
	return root
}

func (f *migrateFixture) sharedHome() string {
	return filepath.Join(f.root, RunnerHomesDirName, f.scopeHash)
}

func (f *migrateFixture) sessionKeys(sids ...string) []string {
	keys := make([]string, 0, len(sids))
	for _, sid := range sids {
		keys = append(keys, state.PublicSessionKey(f.scope.Repo, sid))
	}
	return keys
}

// seedLegacyHome writes the standard import fixture into one session's legacy
// runtime root: importable agent state, per-job mirrored files that must be
// skipped, and caches that must never be imported.
func seedLegacyHome(t *testing.T, root, indexContent string) {
	t.Helper()
	writeContent(t, filepath.Join(root, "home", ".acpx", "sessions", "index.json"), indexContent, 0o600)
	writeContent(t, filepath.Join(root, "home", ".acpx", "queues", "q1", "item.json"), "queue-item", 0o600)
	writeContent(t, filepath.Join(root, "codex", "sessions", "s1.json"), "codex-session", 0o600)
	writeContent(t, filepath.Join(root, "codex", "tools", "run.sh"), "#!/bin/sh\n", 0o755)
	writeContent(t, filepath.Join(root, "home", ".claude", "projects", "p.json"), "claude-project", 0o600)
	writeContent(t, filepath.Join(root, "home", ".qoder", "mcp.json"), "qoder-mcp", 0o600)
	// Mirrored per job from the host: never imported.
	writeContent(t, filepath.Join(root, "codex", "auth.json"), "codex-auth", 0o600)
	writeContent(t, filepath.Join(root, "codex", "config.toml"), "codex-config", 0o600)
	writeContent(t, filepath.Join(root, "home", ".codex", "version.json"), "codex-version", 0o600)
	writeContent(t, filepath.Join(root, "home", ".codex", "installation_id"), "codex-install", 0o600)
	writeContent(t, filepath.Join(root, "home", ".claude", "settings.json"), "claude-settings", 0o600)
	writeContent(t, filepath.Join(root, "home", ".claude", "settings.local.json"), "claude-local", 0o600)
	writeContent(t, filepath.Join(root, "home", ".claude", ".credentials.json"), "claude-creds", 0o600)
	writeContent(t, filepath.Join(root, "home", ".qoder", "settings.json"), "qoder-settings", 0o600)
	writeContent(t, filepath.Join(root, "home", ".qoder", ".auth", "token"), "qoder-token", 0o600)
	// Rebuildable or runtime-local state: never imported.
	writeContent(t, filepath.Join(root, "home", ".cache", "big"), "cache", 0o600)
	writeContent(t, filepath.Join(root, "home", "go", "pkg", "mod", "m.zip"), "mod", 0o600)
	writeContent(t, filepath.Join(root, "home", ".npm", "registry.tgz"), "npm", 0o600)
	writeContent(t, filepath.Join(root, "gh", "hosts.yml"), "gh", 0o600)
	writeContent(t, filepath.Join(root, "acpx-runtime", "sock"), "runtime", 0o600)
}

func readContent(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %q: %v", path, err)
	}
	return string(data)
}

func TestMigrateHomeHappyImport(t *testing.T) {
	f := newMigrateFixture(t)
	f.addSession("ps-1")
	f.addSession("ps-2")
	seedLegacyHome(t, f.legacyRoot("ps-1"), "shared-index")
	writeContent(t, filepath.Join(f.legacyRoot("ps-1"), "home", ".acpx", "sessions", "sess-a.json"), "session-a", 0o600)
	seedLegacyHome(t, f.legacyRoot("ps-2"), "shared-index")
	writeContent(t, filepath.Join(f.legacyRoot("ps-2"), "home", ".acpx", "sessions", "sess-b.json"), "session-b", 0o600)
	// ps-2 carries no claude/qoder/codex extras; identical index dedupes.

	report, err := f.svc.MigrateHome(context.Background(), MigrateHomeOptions{Scope: f.scope, Apply: true})
	if err != nil {
		t.Fatalf("MigrateHome: %v", err)
	}
	if report.ScopeHash != f.scopeHash {
		t.Fatalf("scope hash = %q, want %q", report.ScopeHash, f.scopeHash)
	}
	if report.LedgerState != MigrationImported {
		t.Fatalf("ledger state = %q, want %q", report.LedgerState, MigrationImported)
	}
	if len(report.Conflicts) != 0 {
		t.Fatalf("unexpected conflicts: %+v", report.Conflicts)
	}
	for _, key := range f.sessionKeys("ps-1", "ps-2") {
		if !contains(report.ImportedSessions, key) {
			t.Fatalf("imported sessions %v missing %q", report.ImportedSessions, key)
		}
	}
	// Unique destinations: index deduped, sess-a/sess-b merged, queue item,
	// codex session + executable tool, claude project, qoder mcp.
	if report.CopiedFiles != 8 {
		t.Fatalf("copied = %d, want 8", report.CopiedFiles)
	}
	home := f.sharedHome()
	wantFiles := map[string]string{
		"home/.acpx/sessions/index.json":  "shared-index",
		"home/.acpx/sessions/sess-a.json": "session-a",
		"home/.acpx/sessions/sess-b.json": "session-b",
		"home/.acpx/queues/q1/item.json":  "queue-item",
		"codex/sessions/s1.json":          "codex-session",
		"home/.claude/projects/p.json":    "claude-project",
		"home/.qoder/mcp.json":            "qoder-mcp",
	}
	for rel, content := range wantFiles {
		got := readContent(t, filepath.Join(home, filepath.FromSlash(rel)))
		if got != content {
			t.Fatalf("%s = %q, want %q", rel, got, content)
		}
	}
	// Private perms: 0600 files, 0700 for executables.
	info, err := os.Lstat(filepath.Join(home, "home", ".acpx", "sessions", "sess-a.json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("imported file perm = %v, err=%v; want 0600", info.Mode(), err)
	}
	tool := filepath.Join(home, "codex", "tools", "run.sh")
	info, err = os.Lstat(tool)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("imported executable perm = %v, err=%v; want 0700", info.Mode(), err)
	}
	// Mirrored and cache content must not be imported.
	absent := []string{
		"codex/auth.json", "codex/config.toml",
		"home/.codex/version.json", "home/.codex/installation_id",
		"home/.claude/settings.json", "home/.claude/settings.local.json", "home/.claude/.credentials.json",
		"home/.qoder/settings.json", "home/.qoder/.auth",
		"home/.cache", "home/go", "home/.npm",
		"gh/hosts.yml", "acpx-runtime/sock",
	}
	for _, rel := range absent {
		if _, err := os.Lstat(filepath.Join(home, filepath.FromSlash(rel))); !os.IsNotExist(err) {
			t.Fatalf("%s must not be imported, err=%v", rel, err)
		}
	}
	// Scope binding written; ledger recorded; raw state backup taken.
	if _, err := os.Lstat(filepath.Join(home, "scope.json")); err != nil {
		t.Fatalf("scope.json missing: %v", err)
	}
	ledger, ok, err := f.svc.RuntimeMigrationLedger(context.Background(), f.scopeHash)
	if err != nil || !ok {
		t.Fatalf("ledger missing: %v ok=%v", err, ok)
	}
	if ledger.State != MigrationImported || len(ledger.ImportedSessions) != 2 {
		t.Fatalf("ledger = %+v", ledger)
	}
	if _, err := os.Lstat(filepath.Join(f.root, StorageDirName, backupDirName, rawStateBackupName)); err != nil {
		t.Fatalf("raw state backup missing: %v", err)
	}
	// Legacy homes stay in place until validated retirement.
	if _, err := os.Lstat(f.legacyRoot("ps-1")); err != nil {
		t.Fatalf("legacy home must be preserved after import: %v", err)
	}
}

func TestMigrateHomeConflictFailsClosed(t *testing.T) {
	f := newMigrateFixture(t)
	f.addSession("ps-1")
	f.addSession("ps-2")
	seedLegacyHome(t, f.legacyRoot("ps-1"), "shared-index")
	seedLegacyHome(t, f.legacyRoot("ps-2"), "shared-index")
	// Conflicting relpath: different bytes in each legacy home.
	writeContent(t, filepath.Join(f.legacyRoot("ps-1"), "home", ".claude", "projects", "p.json"), "project-A", 0o600)
	writeContent(t, filepath.Join(f.legacyRoot("ps-2"), "home", ".claude", "projects", "p.json"), "project-B", 0o600)

	report, err := f.svc.MigrateHome(context.Background(), MigrateHomeOptions{Scope: f.scope, Apply: true})
	if err == nil {
		t.Fatalf("conflicting legacy content must fail migration")
	}
	if len(report.Conflicts) != 1 {
		t.Fatalf("conflicts = %+v, want exactly 1", report.Conflicts)
	}
	conflict := report.Conflicts[0]
	if conflict.RelPath != "home/.claude/projects/p.json" {
		t.Fatalf("conflict path = %q", conflict.RelPath)
	}
	for _, key := range f.sessionKeys("ps-1", "ps-2") {
		if !contains(conflict.Sessions, key) {
			t.Fatalf("conflict sessions %v missing %q", conflict.Sessions, key)
		}
	}
	// No import writes happened.
	home := f.sharedHome()
	if _, statErr := os.Lstat(filepath.Join(home, "home", ".claude", "projects", "p.json")); !os.IsNotExist(statErr) {
		t.Fatalf("conflicting file must not be imported, err=%v", statErr)
	}
	if _, statErr := os.Lstat(filepath.Join(home, "codex", "sessions", "s1.json")); !os.IsNotExist(statErr) {
		t.Fatalf("no file may be imported when any conflict exists, err=%v", statErr)
	}
	if _, ok, _ := f.svc.RuntimeMigrationLedger(context.Background(), f.scopeHash); ok {
		t.Fatalf("conflicted migration must not write the ledger")
	}
}

func TestMigrateHomeDestinationConflictFailsClosed(t *testing.T) {
	f := newMigrateFixture(t)
	f.addSession("ps-1")
	seedLegacyHome(t, f.legacyRoot("ps-1"), "shared-index")
	if _, err := f.svc.MigrateHome(context.Background(), MigrateHomeOptions{Scope: f.scope, Apply: true}); err != nil {
		t.Fatalf("first MigrateHome: %v", err)
	}
	// The destination changed underneath (e.g. a concurrent writer): the next
	// import of diverging legacy content must fail, never overwrite.
	writeContent(t, filepath.Join(f.sharedHome(), "home", ".qoder", "mcp.json"), "locally-modified", 0o600)
	report, err := f.svc.MigrateHome(context.Background(), MigrateHomeOptions{Scope: f.scope, Apply: true})
	if err == nil {
		t.Fatalf("diverged destination must fail migration")
	}
	found := false
	for _, conflict := range report.Conflicts {
		if conflict.RelPath == "home/.qoder/mcp.json" && contains(conflict.Sessions, migrationDestinationMarker) {
			found = true
		}
	}
	if !found {
		t.Fatalf("destination conflict must name the shared home marker: %+v", report.Conflicts)
	}
	if got := readContent(t, filepath.Join(f.sharedHome(), "home", ".qoder", "mcp.json")); got != "locally-modified" {
		t.Fatalf("destination must not be overwritten, got %q", got)
	}
}

func TestMigrateHomeApplyTwiceIdempotent(t *testing.T) {
	f := newMigrateFixture(t)
	f.addSession("ps-1")
	seedLegacyHome(t, f.legacyRoot("ps-1"), "shared-index")
	first, err := f.svc.MigrateHome(context.Background(), MigrateHomeOptions{Scope: f.scope, Apply: true})
	if err != nil {
		t.Fatalf("first MigrateHome: %v", err)
	}
	if first.CopiedFiles != 6 || first.SkippedIdentical != 0 {
		t.Fatalf("first run copied=%d skipped=%d, want 6/0", first.CopiedFiles, first.SkippedIdentical)
	}
	second, err := f.svc.MigrateHome(context.Background(), MigrateHomeOptions{Scope: f.scope, Apply: true})
	if err != nil {
		t.Fatalf("second MigrateHome: %v", err)
	}
	if second.CopiedFiles != 0 || second.SkippedIdentical != 6 {
		t.Fatalf("second run copied=%d skipped=%d, want 0/6", second.CopiedFiles, second.SkippedIdentical)
	}
	if second.LedgerState != MigrationImported {
		t.Fatalf("ledger = %q, want imported", second.LedgerState)
	}
	// The second run found runtime.json on disk and preserved it once.
	if _, err := os.Lstat(filepath.Join(f.root, StorageDirName, backupDirName, runtimeBackupName)); err != nil {
		t.Fatalf("runtime metadata backup missing after second run: %v", err)
	}
}

func TestMigrateHomeDryRunDoesNotMutate(t *testing.T) {
	f := newMigrateFixture(t)
	f.addSession("ps-1")
	seedLegacyHome(t, f.legacyRoot("ps-1"), "shared-index")
	report, err := f.svc.MigrateHome(context.Background(), MigrateHomeOptions{Scope: f.scope, Apply: false})
	if err != nil {
		t.Fatalf("MigrateHome dry-run: %v", err)
	}
	if report.CopiedFiles != 6 {
		t.Fatalf("dry-run would-copy = %d, want 6", report.CopiedFiles)
	}
	if len(report.ImportedSessions) != 1 {
		t.Fatalf("dry-run imported sessions = %v", report.ImportedSessions)
	}
	if _, err := os.Lstat(filepath.Join(f.root, RunnerHomesDirName)); !os.IsNotExist(err) {
		t.Fatalf("dry-run must not create %s, err=%v", RunnerHomesDirName, err)
	}
	if _, ok, _ := f.svc.RuntimeMigrationLedger(context.Background(), f.scopeHash); ok {
		t.Fatalf("dry-run must not write the ledger")
	}
	if _, err := os.Lstat(filepath.Join(f.root, StorageDirName, backupDirName, rawStateBackupName)); !os.IsNotExist(err) {
		t.Fatalf("dry-run must not write backups, err=%v", err)
	}
}

func TestMigrateHomeSkipsSessionsWithoutWorkspace(t *testing.T) {
	f := newMigrateFixture(t)
	f.addSession("ps-1")
	seedLegacyHome(t, f.legacyRoot("ps-1"), "shared-index")
	// A session without workspace metadata cannot locate its legacy home.
	key := state.PublicSessionKey(f.scope.Repo, "ps-3")
	f.st.PublicSessions[key] = state.PublicSession{
		Repo: f.scope.Repo, PublicSessionID: "ps-3", AcpxRecordID: "rec-ps-3", Status: state.StatusCompleted,
	}
	report, err := f.svc.MigrateHome(context.Background(), MigrateHomeOptions{Scope: f.scope, Apply: true})
	if err != nil {
		t.Fatalf("MigrateHome: %v", err)
	}
	if contains(report.ImportedSessions, key) {
		t.Fatalf("session without workspace must not import: %v", report.ImportedSessions)
	}
	joined := strings.Join(report.Diagnostics, "\n")
	if !strings.Contains(joined, key) {
		t.Fatalf("diagnostics must explain the skipped session: %v", report.Diagnostics)
	}
}

func TestMigrateHomeLedgerNeverRegresses(t *testing.T) {
	f := newMigrateFixture(t)
	f.addSession("ps-1")
	seedLegacyHome(t, f.legacyRoot("ps-1"), "shared-index")
	if _, err := f.svc.MigrateHome(context.Background(), MigrateHomeOptions{Scope: f.scope, Apply: true}); err != nil {
		t.Fatalf("MigrateHome: %v", err)
	}
	if err := f.svc.MarkRuntimeMigration(context.Background(), MigrationRecord{
		ScopeHash: f.scopeHash, State: MigrationValidated, ValidatedSession: state.PublicSessionKey(f.scope.Repo, "ps-1"),
	}); err != nil {
		t.Fatalf("mark validated: %v", err)
	}
	// Direct regression is rejected.
	if err := f.svc.MarkRuntimeMigration(context.Background(), MigrationRecord{
		ScopeHash: f.scopeHash, State: MigrationImported,
	}); err == nil {
		t.Fatalf("ledger regression must be rejected")
	}
	// A migration re-run stays idempotent and keeps the advanced ledger state.
	report, err := f.svc.MigrateHome(context.Background(), MigrateHomeOptions{Scope: f.scope, Apply: true})
	if err != nil {
		t.Fatalf("re-run MigrateHome: %v", err)
	}
	if report.LedgerState != MigrationValidated {
		t.Fatalf("ledger = %q, want validated after re-run", report.LedgerState)
	}
	ledger, _, err := f.svc.RuntimeMigrationLedger(context.Background(), f.scopeHash)
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	if ledger.ValidatedSession != state.PublicSessionKey(f.scope.Repo, "ps-1") {
		t.Fatalf("validated session lost: %+v", ledger)
	}
}

func TestRetireLegacyHomesRequiresValidated(t *testing.T) {
	f := newMigrateFixture(t)
	f.addSession("ps-1")
	seedLegacyHome(t, f.legacyRoot("ps-1"), "shared-index")
	if _, err := f.svc.MigrateHome(context.Background(), MigrateHomeOptions{Scope: f.scope, Apply: true}); err != nil {
		t.Fatalf("MigrateHome: %v", err)
	}
	if _, err := f.svc.RetireLegacyHomes(context.Background(), f.scope, true); err == nil || !strings.Contains(err.Error(), "validated") {
		t.Fatalf("retire with imported ledger must require validated, err=%v", err)
	}
	other := RuntimeScope{Hostname: "host-1", Repo: "o/other", Runner: "runner-1"}
	if _, err := f.svc.RetireLegacyHomes(context.Background(), other, true); err == nil {
		t.Fatalf("retire without a ledger must fail")
	}
}

func TestRetireLegacyHomes(t *testing.T) {
	f := newMigrateFixture(t)
	ws1 := f.addSession("ps-1")
	ws2 := f.addSession("ps-2")
	seedLegacyHome(t, f.legacyRoot("ps-1"), "shared-index")
	seedLegacyHome(t, f.legacyRoot("ps-2"), "shared-index")
	if _, err := f.svc.MigrateHome(context.Background(), MigrateHomeOptions{Scope: f.scope, Apply: true}); err != nil {
		t.Fatalf("MigrateHome: %v", err)
	}
	if err := f.svc.MarkRuntimeMigration(context.Background(), MigrationRecord{
		ScopeHash: f.scopeHash, State: MigrationValidated, ValidatedSession: state.PublicSessionKey(f.scope.Repo, "ps-1"),
	}); err != nil {
		t.Fatalf("mark validated: %v", err)
	}
	for _, sid := range []string{"ps-1", "ps-2"} {
		session := f.st.PublicSessions[state.PublicSessionKey(f.scope.Repo, sid)]
		if err := f.svc.RecordSessionResources(context.Background(), f.scope.Repo, sid, session.Workspace.Path); err != nil {
			t.Fatalf("RecordSessionResources %s: %v", sid, err)
		}
	}
	// The session clones are gone, so the engine can retire the runtimes.
	for _, ws := range []string{ws1, ws2} {
		if err := os.RemoveAll(ws); err != nil {
			t.Fatalf("remove workspace: %v", err)
		}
	}

	dry, err := f.svc.RetireLegacyHomes(context.Background(), f.scope, false)
	if err != nil {
		t.Fatalf("dry-run retire: %v", err)
	}
	if len(dry.RetiredLegacy) != 0 {
		t.Fatalf("dry-run must not retire: %+v", dry)
	}
	for _, sid := range []string{"ps-1", "ps-2"} {
		if _, err := os.Lstat(f.legacyRoot(sid)); err != nil {
			t.Fatalf("dry-run must not delete legacy home: %v", err)
		}
	}

	report, err := f.svc.RetireLegacyHomes(context.Background(), f.scope, true)
	if err != nil {
		t.Fatalf("RetireLegacyHomes: %v", err)
	}
	if report.LedgerState != MigrationRetired {
		t.Fatalf("ledger = %q, want retired", report.LedgerState)
	}
	for _, key := range f.sessionKeys("ps-1", "ps-2") {
		if !contains(report.RetiredLegacy, key) {
			t.Fatalf("retired legacy %v missing %q", report.RetiredLegacy, key)
		}
	}
	for _, sid := range []string{"ps-1", "ps-2"} {
		if _, err := os.Lstat(f.legacyRoot(sid)); !os.IsNotExist(err) {
			t.Fatalf("legacy home for %s must be deleted, err=%v", sid, err)
		}
	}
	// The shared home survives retirement.
	if got := readContent(t, filepath.Join(f.sharedHome(), "home", ".acpx", "sessions", "index.json")); got != "shared-index" {
		t.Fatalf("shared home damaged by retirement: %q", got)
	}
	// Idempotent re-run.
	second, err := f.svc.RetireLegacyHomes(context.Background(), f.scope, true)
	if err != nil {
		t.Fatalf("second RetireLegacyHomes: %v", err)
	}
	if second.LedgerState != MigrationRetired {
		t.Fatalf("second run ledger = %q, want retired", second.LedgerState)
	}
}

func TestV1EngineIgnoresRuntimeHomeResources(t *testing.T) {
	f := newMigrateFixture(t)
	// The upgraded root carries shared homes, job scratch, and runtime.json.
	paths, err := PrepareRuntimeHome(f.root, f.scope)
	if err != nil {
		t.Fatalf("PrepareRuntimeHome: %v", err)
	}
	if err := f.svc.RecordRuntimeHome(context.Background(), f.scope, paths); err != nil {
		t.Fatalf("RecordRuntimeHome: %v", err)
	}
	scratch, err := PrepareJobScratch(f.root, scratchJobActive)
	if err != nil {
		t.Fatalf("PrepareJobScratch: %v", err)
	}
	if err := f.svc.RecordJobScratch(context.Background(), scratchJobActive, scratch.Root); err != nil {
		t.Fatalf("RecordJobScratch: %v", err)
	}
	writeFile(t, filepath.Join(paths.Home, ".acpx", "sessions", "index.json"), 128)
	writeFile(t, filepath.Join(scratch.Tmp, "payload"), 64)
	// Plus one legacy orphan the v1 engine must still reclaim.
	orphanHash := strings.Repeat("ef", 16)
	orphanDir := filepath.Join(f.root, SessionsDirName, orphanHash)
	writeFile(t, filepath.Join(orphanDir, "home", "stale"), 32)

	engine, err := NewEngine(EngineConfig{
		WorkspaceRoot: f.root,
		StateLoader:   func(context.Context) (state.RunnerState, error) { return state.NewState(), nil },
	})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	if _, err := engine.Reconcile(context.Background(), ReconcileOptions{Apply: true, OrphanGrace: 0}); err != nil {
		t.Fatalf("first Reconcile: %v", err)
	}
	report, err := engine.Reconcile(context.Background(), ReconcileOptions{Apply: true, OrphanGrace: 0})
	if err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	orphanID := ResourceID(ResourceKindSessionRuntime, "", "", orphanHash)
	got := reportByID(t, report, orphanID)
	if got.Action != ActionDeleted {
		t.Fatalf("v1 engine must still delete the legacy orphan, got %+v", got)
	}
	if _, err := os.Lstat(orphanDir); !os.IsNotExist(err) {
		t.Fatalf("legacy orphan must be gone, err=%v", err)
	}
	// The v1 engine neither inventories nor deletes the new roots.
	for _, resource := range report.Resources {
		if strings.Contains(resource.ID, RunnerHomesDirName) || strings.Contains(resource.ID, JobScratchDirName) ||
			strings.Contains(resource.Hash, RunnerHomesDirName) {
			t.Fatalf("v1 engine must never inventory runner-home resources: %+v", resource)
		}
	}
	if _, err := os.Lstat(filepath.Join(paths.Home, ".acpx", "sessions", "index.json")); err != nil {
		t.Fatalf("shared runtime home damaged by v1 engine: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(scratch.Tmp, "payload")); err != nil {
		t.Fatalf("job scratch damaged by v1 engine: %v", err)
	}
	// The sidecar stays kind-valid and runtime.json is untouched.
	store, err := OpenStore(f.root)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()
	for id, resource := range store.State().Resources {
		if !resource.Kind.Valid() {
			t.Fatalf("sidecar resource %q has invalid kind %q", id, resource.Kind)
		}
	}
	runtimeStore, err := OpenRuntimeStore(f.root)
	if err != nil {
		t.Fatalf("OpenRuntimeStore: %v", err)
	}
	defer runtimeStore.Close()
	runtimeState := runtimeStore.State()
	if _, ok := runtimeState.Homes[f.scopeHash]; !ok {
		t.Fatalf("runtime home record lost: %+v", runtimeState.Homes)
	}
	if _, ok := runtimeState.Scratch[scratchJobActive]; !ok {
		t.Fatalf("job scratch record lost: %+v", runtimeState.Scratch)
	}
}
