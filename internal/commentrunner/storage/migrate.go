package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/higress-group/issue-spec/internal/commentrunner/state"
)

// runtimeBackupName preserves the runtime metadata before the first applied
// migration, alongside the raw state and sidecar first-migration backups.
const runtimeBackupName = "runtime-first.json"

// migrationDestinationMarker names the existing shared-home file in a
// conflict's Sessions list. It can never collide with a session key, which
// always has the repo#id shape.
const migrationDestinationMarker = "(shared-home)"

// MigrateHomeOptions controls one legacy-home migration pass.
type MigrateHomeOptions struct {
	Scope RuntimeScope
	Apply bool
}

// MigrationConflict identifies one destination relpath produced with
// different content by multiple legacy sessions, or already present at the
// destination with different content. Sessions lists the conflicting session
// keys, plus migrationDestinationMarker when the destination participates.
type MigrationConflict struct {
	RelPath  string   `json:"rel_path"`
	Sessions []string `json:"sessions"`
}

// MigrateHomeReport summarizes one migration or retirement pass. It carries
// paths, counts, and session identities only — never file contents.
type MigrateHomeReport struct {
	ScopeHash        string              `json:"scope_hash"`
	LedgerState      MigrationState      `json:"ledger_state,omitempty"`
	ImportedSessions []string            `json:"imported_sessions,omitempty"`
	SkippedIdentical int                 `json:"skipped_identical,omitempty"`
	CopiedFiles      int                 `json:"copied_files,omitempty"`
	Conflicts        []MigrationConflict `json:"conflicts,omitempty"`
	RetiredLegacy    []string            `json:"retired_legacy,omitempty"`
	Diagnostics      []string            `json:"diagnostics,omitempty"`
}

// importSourceSpec maps one legacy runtime subtree into the shared home.
// Only the listed subtrees are ever imported; caches, logs, sockets, and
// per-job mirrored credentials are deliberately excluded.
type importSourceSpec struct {
	srcSub     string
	destSub    string
	excludeTop map[string]bool
	excludeDir map[string]bool
}

// codexMirroredTopLevel are refreshed from the host on every dispatch, so the
// legacy copies are never imported.
var codexMirroredTopLevel = map[string]bool{
	"auth.json": true, "config.toml": true, "version.json": true, "installation_id": true,
}

var claudeMirroredTopLevel = map[string]bool{
	"settings.json": true, "settings.local.json": true, ".credentials.json": true,
}

var qoderMirroredTopLevel = map[string]bool{"settings.json": true}

var legacyImportSources = []importSourceSpec{
	{srcSub: filepath.Join("home", ".acpx", "sessions"), destSub: filepath.Join("home", ".acpx", "sessions")},
	{srcSub: filepath.Join("home", ".acpx", "queues"), destSub: filepath.Join("home", ".acpx", "queues")},
	{srcSub: "codex", destSub: "codex", excludeTop: codexMirroredTopLevel},
	{srcSub: filepath.Join("home", ".codex"), destSub: filepath.Join("home", ".codex"), excludeTop: codexMirroredTopLevel},
	{srcSub: filepath.Join("home", ".claude"), destSub: filepath.Join("home", ".claude"), excludeTop: claudeMirroredTopLevel},
	{srcSub: filepath.Join("home", ".qoder"), destSub: filepath.Join("home", ".qoder"), excludeTop: qoderMirroredTopLevel, excludeDir: map[string]bool{".auth": true}},
}

// legacySessionSource is one selected session's verified legacy runtime root.
type legacySessionSource struct {
	key  string
	root string
}

// importCandidate is the chosen source file for one destination relpath.
type importCandidate struct {
	src     string
	digest  string
	exec    bool
	session string
}

// migrationPlan is the phase-A result: the exact copies to perform, the
// already-identical destinations, and every detected conflict.
type migrationPlan struct {
	copies    map[string]importCandidate
	skipped   int
	conflicts []MigrationConflict
}

// MigrateHome imports legacy per-session agent state into the runner-scoped
// shared runtime home. It runs only under the storage owner, backs up
// control-plane state first, and is two-phase: a full scan across every
// selected session builds the destination plan, and any conflict aborts
// before a single import write. Apply is idempotent; a dry run performs the
// full scan and reports without touching the filesystem or the ledger.
func (s *Service) MigrateHome(ctx context.Context, opts MigrateHomeOptions) (MigrateHomeReport, error) {
	if err := opts.Scope.Validate(); err != nil {
		return MigrateHomeReport{}, err
	}
	scopeHash, err := RuntimeScopeHash(opts.Scope)
	if err != nil {
		return MigrateHomeReport{}, err
	}
	report := MigrateHomeReport{ScopeHash: scopeHash}
	owner, release, err := EnsureOwner(ctx, s.root)
	if err != nil {
		return report, fmt.Errorf("migration owner: %w", err)
	}
	defer release()
	ctx = WithOwner(ctx, owner)

	if opts.Apply {
		if err := s.migrationBackups(); err != nil {
			return report, fmt.Errorf("migration backups: %w", err)
		}
	}
	st, err := s.stateLoader(ctx)
	if err != nil {
		return report, fmt.Errorf("load runner state for migration: %w", err)
	}
	st.Normalize()
	sources := s.selectLegacySessions(st, opts.Scope.Repo, &report)
	for _, source := range sources {
		report.ImportedSessions = append(report.ImportedSessions, source.key)
	}

	sharedRoot := filepath.Join(s.root, RunnerHomesDirName, scopeHash)
	if opts.Apply {
		if _, err := PrepareRuntimeHome(s.root, opts.Scope); err != nil {
			return report, err
		}
	} else {
		// Dry runs validate an existing binding but never create the tree.
		if _, statErr := os.Lstat(sharedRoot); statErr == nil {
			if err := checkRuntimeScopeBinding(sharedRoot, opts.Scope); err != nil {
				return report, err
			}
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return report, fmt.Errorf("inspect runtime home: %w", statErr)
		}
	}

	plan, err := buildMigrationPlan(sources, sharedRoot)
	if err != nil {
		return report, err
	}
	report.Conflicts = plan.conflicts
	if len(plan.conflicts) > 0 {
		return report, fmt.Errorf("migration has %d conflicting paths; resolve the legacy homes manually before importing", len(plan.conflicts))
	}
	report.SkippedIdentical = plan.skipped
	if !opts.Apply {
		report.CopiedFiles = len(plan.copies)
		report.Diagnostics = append(report.Diagnostics, "dry-run: no filesystem or ledger writes performed")
		return report, nil
	}
	if err := applyMigrationPlan(sharedRoot, plan, &report); err != nil {
		return report, err
	}
	// The ledger never regresses: a re-run after validation keeps the advanced
	// state while still union-merging the imported session set.
	markState := MigrationImported
	existing, has, err := s.RuntimeMigrationLedger(ctx, scopeHash)
	if err != nil {
		return report, err
	}
	if has && migrationStateRank(existing.State) > migrationStateRank(MigrationImported) {
		markState = existing.State
	}
	if err := s.MarkRuntimeMigration(ctx, MigrationRecord{
		ScopeHash:        scopeHash,
		State:            markState,
		ImportedSessions: report.ImportedSessions,
		ValidatedSession: existing.ValidatedSession,
	}); err != nil {
		return report, err
	}
	ledger, _, err := s.RuntimeMigrationLedger(ctx, scopeHash)
	if err != nil {
		return report, err
	}
	report.LedgerState = ledger.State
	return report, nil
}

// RetireLegacyHomes retires the legacy per-session runtime dirs of an
// already validated migration: the matching sidecar session_runtime records
// are marked retired_known (never a fresh orphan grace) and one engine
// reconcile pass removes them through the existing deletion transactions. The
// ledger advances to retired once the pass succeeds; re-runs are idempotent.
func (s *Service) RetireLegacyHomes(ctx context.Context, scope RuntimeScope, apply bool) (MigrateHomeReport, error) {
	if err := scope.Validate(); err != nil {
		return MigrateHomeReport{}, err
	}
	scopeHash, err := RuntimeScopeHash(scope)
	if err != nil {
		return MigrateHomeReport{}, err
	}
	report := MigrateHomeReport{ScopeHash: scopeHash}
	owner, release, err := EnsureOwner(ctx, s.root)
	if err != nil {
		return report, fmt.Errorf("retire legacy homes owner: %w", err)
	}
	defer release()
	ctx = WithOwner(ctx, owner)

	ledger, ok, err := s.RuntimeMigrationLedger(ctx, scopeHash)
	if err != nil {
		return report, err
	}
	if !ok {
		return report, fmt.Errorf("migration ledger for scope hash %q not found; run the import first", scopeHash)
	}
	if ledger.State != MigrationValidated && ledger.State != MigrationRetired {
		return report, fmt.Errorf("migration ledger for scope hash %q is %q; a validated resume through the shared home is required before retiring legacy homes", scopeHash, ledger.State)
	}
	report.LedgerState = ledger.State
	report.ImportedSessions = append([]string(nil), ledger.ImportedSessions...)

	st, err := s.stateLoader(ctx)
	if err != nil {
		return report, fmt.Errorf("load runner state for legacy retirement: %w", err)
	}
	st.Normalize()
	expectedRoots := s.legacyRootsBySession(st, ledger.ImportedSessions, &report)

	// Only sidecar records whose ownership names an imported session and whose
	// path matches that session's exact legacy runtime root may retire. For
	// sessions already pruned from state the sidecar's proven ownership fields
	// are the path evidence.
	sidecar := s.store.State()
	type retireTarget struct {
		id         string
		sessionKey string
		path       string
	}
	var targets []retireTarget
	for id, resource := range sidecar.Resources {
		if resource.Kind != ResourceKindSessionRuntime || !resource.Owned() {
			continue
		}
		key := state.PublicSessionKey(resource.Repo, resource.PublicSessionID)
		if !containsString(ledger.ImportedSessions, key) {
			continue
		}
		if expected, ok := expectedRoots[key]; ok && filepath.Clean(resource.Path) != expected {
			report.Diagnostics = append(report.Diagnostics, safeDiagnostic("sidecar record "+id+" path does not match the session runtime root; skipped"))
			continue
		}
		targets = append(targets, retireTarget{id: id, sessionKey: key, path: resource.Path})
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].id < targets[j].id })

	if !apply {
		for _, target := range targets {
			report.Diagnostics = append(report.Diagnostics, "would retire legacy runtime for session "+target.sessionKey)
		}
		report.Diagnostics = append(report.Diagnostics, "dry-run: no filesystem or ledger writes performed")
		return report, nil
	}
	if err := s.store.Update(func(st *StorageState) error {
		for _, target := range targets {
			current, ok := st.Resources[target.id]
			if !ok {
				continue
			}
			switch current.CleanupState {
			case CleanupDeleting, CleanupRemoved:
				continue
			}
			current.CleanupState = CleanupRetiredKnown
			current.CleanupAttemptID = ""
			current.LastCleanupError = ""
			st.Resources[target.id] = current
		}
		return nil
	}); err != nil {
		return report, fmt.Errorf("mark legacy runtimes retired: %w", err)
	}
	if _, err := s.ReconcileStorage(ctx, true, false); err != nil {
		return report, fmt.Errorf("retire reconcile pass: %w", err)
	}
	if err := s.MarkRuntimeMigration(ctx, MigrationRecord{
		ScopeHash:        scopeHash,
		State:            MigrationRetired,
		ImportedSessions: ledger.ImportedSessions,
		ValidatedSession: ledger.ValidatedSession,
	}); err != nil {
		return report, err
	}
	report.LedgerState = MigrationRetired
	for _, target := range targets {
		if _, err := os.Lstat(target.path); errors.Is(err, os.ErrNotExist) {
			report.RetiredLegacy = append(report.RetiredLegacy, target.sessionKey)
		} else {
			report.Diagnostics = append(report.Diagnostics, safeDiagnostic("legacy runtime for session "+target.sessionKey+" preserved by reconciliation"))
		}
	}
	sort.Strings(report.RetiredLegacy)
	return report, nil
}

// migrationBackups performs the first-migration preservation set: the raw
// pre-Normalize runner state (when configured), the sidecar, and the runtime
// metadata. All three are atomic, idempotent, and never overwritten.
func (s *Service) migrationBackups() error {
	if _, err := EnsureRawStateBackup(s.root, s.rawStatePath); err != nil {
		return err
	}
	if err := ensureMetadataBackup(s.root, s.store.Path(), sidecarBackupPrefix+".json"); err != nil {
		return err
	}
	runtimeStore, err := s.RuntimeStore()
	if err != nil {
		return err
	}
	return ensureMetadataBackup(s.root, runtimeStore.Path(), runtimeBackupName)
}

// ensureMetadataBackup atomically preserves the first copy of one metadata
// file under `.storage/backups/`, never overwriting an earlier copy.
func ensureMetadataBackup(workspaceRoot, sourcePath, backupName string) error {
	data, err := os.ReadFile(sourcePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s for migration backup: %w", backupName, err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	canonical, err := Canonicalize(workspaceRoot)
	if err != nil {
		return err
	}
	backupDir := filepath.Join(canonical, StorageDirName, backupDirName)
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		return err
	}
	target := filepath.Join(backupDir, backupName)
	if _, err := os.Lstat(target); err == nil {
		return nil
	}
	return state.WriteAtomic(target, data)
}

// selectLegacySessions picks the repo's retained public sessions whose legacy
// runtime root exists below this root with a home/ subdir. Sessions without
// workspace metadata or with roots outside this root are skipped with a
// diagnostic: foreign paths are never imported.
func (s *Service) selectLegacySessions(st state.RunnerState, repo string, report *MigrateHomeReport) []legacySessionSource {
	keys := make([]string, 0, len(st.PublicSessions))
	for key := range st.PublicSessions {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var sources []legacySessionSource
	for _, key := range keys {
		session := st.PublicSessions[key]
		if session.Repo != repo {
			continue
		}
		wsPath := strings.TrimSpace(session.Workspace.Path)
		if wsPath == "" {
			report.Diagnostics = append(report.Diagnostics, "session "+key+" lacks workspace metadata; skipped")
			continue
		}
		hash, err := SessionRuntimeHash(session.Repo, session.PublicSessionID, wsPath)
		if err != nil {
			report.Diagnostics = append(report.Diagnostics, safeDiagnostic("session "+key+" runtime hash failed: "+err.Error()))
			continue
		}
		legacyRoot, err := SessionRuntimeRoot(wsPath, session.Repo, session.PublicSessionID)
		if err != nil {
			report.Diagnostics = append(report.Diagnostics, safeDiagnostic("session "+key+" runtime root failed: "+err.Error()))
			continue
		}
		if expected := filepath.Join(s.root, SessionsDirName, hash); legacyRoot != expected {
			report.Diagnostics = append(report.Diagnostics, safeDiagnostic("session "+key+" legacy runtime root is outside this workspace root; skipped"))
			continue
		}
		if info, statErr := os.Lstat(legacyRoot); statErr != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			continue
		}
		if info, statErr := os.Lstat(filepath.Join(legacyRoot, "home")); statErr != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			continue
		}
		sources = append(sources, legacySessionSource{key: key, root: legacyRoot})
	}
	return sources
}

// legacyRootsBySession recomputes the exact legacy runtime root for imported
// sessions whose workspace metadata survives in state.
func (s *Service) legacyRootsBySession(st state.RunnerState, sessionKeys []string, report *MigrateHomeReport) map[string]string {
	roots := map[string]string{}
	for _, key := range sessionKeys {
		session, ok := st.PublicSessions[key]
		if !ok {
			continue
		}
		wsPath := strings.TrimSpace(session.Workspace.Path)
		if wsPath == "" {
			continue
		}
		hash, err := SessionRuntimeHash(session.Repo, session.PublicSessionID, wsPath)
		if err != nil {
			report.Diagnostics = append(report.Diagnostics, safeDiagnostic("session "+key+" runtime hash failed: "+err.Error()))
			continue
		}
		roots[key] = filepath.Join(s.root, SessionsDirName, hash)
	}
	return roots
}

// buildMigrationPlan scans every selected session's importable subtrees and
// resolves each destination relpath to one content digest. A relpath produced
// with different digests by two sessions, or already present at the
// destination with a different digest, is a conflict.
func buildMigrationPlan(sources []legacySessionSource, sharedRoot string) (migrationPlan, error) {
	plan := migrationPlan{copies: map[string]importCandidate{}}
	type occurrence struct {
		digest  string
		session string
	}
	occurrences := map[string][]occurrence{}
	chosen := map[string]importCandidate{}
	for _, source := range sources {
		for _, spec := range legacyImportSources {
			srcBase := filepath.Join(source.root, spec.srcSub)
			info, err := os.Lstat(srcBase)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return plan, fmt.Errorf("inspect legacy import source: %w", err)
			}
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				continue
			}
			walkErr := filepath.WalkDir(srcBase, func(path string, entry os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				rel, err := filepath.Rel(srcBase, path)
				if err != nil {
					return err
				}
				if rel == "." {
					return nil
				}
				slashRel := filepath.ToSlash(rel)
				top, _, _ := strings.Cut(slashRel, "/")
				depthOne := !strings.Contains(slashRel, "/")
				if entry.IsDir() {
					if spec.excludeDir[top] || (depthOne && spec.excludeTop[top]) {
						return filepath.SkipDir
					}
					return nil
				}
				// Symlinks, devices, sockets: never imported.
				if !entry.Type().IsRegular() {
					return nil
				}
				if depthOne && spec.excludeTop[top] {
					return nil
				}
				digest, _, err := digestFileBytes(path)
				if err != nil {
					return err
				}
				fileInfo, err := entry.Info()
				if err != nil {
					return err
				}
				destRel := filepath.Join(spec.destSub, rel)
				occurrences[destRel] = append(occurrences[destRel], occurrence{digest: digest, session: source.key})
				if _, ok := chosen[destRel]; !ok {
					chosen[destRel] = importCandidate{
						src:     path,
						digest:  digest,
						exec:    fileInfo.Mode().Perm()&0o111 != 0,
						session: source.key,
					}
				}
				return nil
			})
			if walkErr != nil {
				return plan, fmt.Errorf("scan legacy session %s: %w", source.key, walkErr)
			}
		}
	}

	rels := make([]string, 0, len(occurrences))
	for rel := range occurrences {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	for _, rel := range rels {
		occs := occurrences[rel]
		digests := map[string]bool{}
		sessions := map[string]bool{}
		for _, occ := range occs {
			digests[occ.digest] = true
			sessions[occ.session] = true
		}
		candidate := chosen[rel]
		conflictSessions := func(withDestination bool) []string {
			set := map[string]bool{}
			for session := range sessions {
				set[session] = true
			}
			if withDestination {
				set[migrationDestinationMarker] = true
			}
			list := make([]string, 0, len(set))
			for session := range set {
				list = append(list, session)
			}
			sort.Strings(list)
			return list
		}
		dest := filepath.Join(sharedRoot, rel)
		destInfo, statErr := os.Lstat(dest)
		switch {
		case errors.Is(statErr, os.ErrNotExist):
			// No destination yet.
		case statErr != nil:
			return plan, fmt.Errorf("inspect migration destination: %w", statErr)
		case destInfo.Mode().IsRegular() && destInfo.Mode()&os.ModeSymlink == 0:
			destDigest, _, err := digestFileBytes(dest)
			if err != nil {
				return plan, err
			}
			if len(digests) == 1 && digests[destDigest] {
				// Already imported identically: re-runs converge.
				plan.skipped++
				continue
			}
			plan.conflicts = append(plan.conflicts, MigrationConflict{RelPath: filepath.ToSlash(rel), Sessions: conflictSessions(true)})
			continue
		default:
			// A symlink, directory, or special file at the destination is
			// never overwritten.
			plan.conflicts = append(plan.conflicts, MigrationConflict{RelPath: filepath.ToSlash(rel), Sessions: conflictSessions(true)})
			continue
		}
		if len(digests) > 1 {
			plan.conflicts = append(plan.conflicts, MigrationConflict{RelPath: filepath.ToSlash(rel), Sessions: conflictSessions(false)})
			continue
		}
		plan.copies[rel] = candidate
	}
	return plan, nil
}

// applyMigrationPlan performs phase B: atomic temp-and-rename copies into the
// shared home, 0700 directories and 0600 files (0700 when the source is
// executable). Destinations are re-validated against the plan so a concurrent
// change fails closed instead of being overwritten.
func applyMigrationPlan(sharedRoot string, plan migrationPlan, report *MigrateHomeReport) error {
	rels := make([]string, 0, len(plan.copies))
	for rel := range plan.copies {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	for _, rel := range rels {
		candidate := plan.copies[rel]
		dest := filepath.Join(sharedRoot, rel)
		info, err := os.Lstat(dest)
		switch {
		case errors.Is(err, os.ErrNotExist):
		case err != nil:
			return fmt.Errorf("inspect migration destination: %w", err)
		default:
			if info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
				if digest, _, digestErr := digestFileBytes(dest); digestErr == nil && digest == candidate.digest {
					report.SkippedIdentical++
					continue
				}
			}
			return fmt.Errorf("migration destination %q changed during import; refusing to overwrite", dest)
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
			return fmt.Errorf("prepare migration destination directory: %w", err)
		}
		if err := copyFileAtomic(dest, candidate.src, candidate.exec); err != nil {
			return fmt.Errorf("copy migration file: %w", err)
		}
		report.CopiedFiles++
	}
	return nil
}

// copyFileAtomic writes src to dest through a temp file in the destination
// directory plus rename, so a concurrent reader never observes a partial
// import.
func copyFileAtomic(dest, src string, exec bool) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	dir := filepath.Dir(dest)
	tmp, err := os.CreateTemp(dir, ".migrate-tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			os.Remove(tmpName)
		}
	}()
	if _, err := io.Copy(tmp, in); err != nil {
		_ = tmp.Close()
		return err
	}
	perm := os.FileMode(0o600)
	if exec {
		perm = 0o700
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, dest); err != nil {
		return err
	}
	cleanup = false
	return nil
}

// digestFileBytes hashes one regular file without reading it into memory.
func digestFileBytes(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hash.Sum(nil)), size, nil
}

func containsString(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}
