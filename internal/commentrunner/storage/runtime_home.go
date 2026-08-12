package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/higress-group/issue-spec/internal/commentrunner/state"
	"github.com/higress-group/issue-spec/internal/workspace"
)

const (
	// RunnerHomesDirName holds the persistent runner-scoped shared runtime
	// HOME roots, one per scope hash.
	RunnerHomesDirName = ".runner-home"
	// JobScratchDirName holds per-job disposable scratch directories.
	JobScratchDirName = ".job-scratch"

	runtimeScopeFileName = "scope.json"
	runtimeScopeVersion  = 1
)

// jobScratchIDPattern is the exact scratch identity shape: the dispatcher's
// stable job IDs are "job-" plus 16 lowercase hex characters.
var jobScratchIDPattern = regexp.MustCompile(`^job-[0-9a-f]{16}$`)

// RuntimeScope identifies the one shared runtime HOME of a runner scope:
// hostname, backend profile realm (empty for the builtin GitHub profile),
// canonical repo "owner/name", and runner identity.
type RuntimeScope struct {
	Hostname string
	Realm    string
	Repo     string
	Runner   string
}

// Key is the NUL-joined scope identity used as the hash preimage. NUL cannot
// appear in any field, so the join is unambiguous.
func (s RuntimeScope) Key() string {
	return s.Hostname + "\x00" + s.Realm + "\x00" + s.Repo + "\x00" + s.Runner
}

// Validate requires hostname, repo, and runner; realm may stay empty.
func (s RuntimeScope) Validate() error {
	if strings.TrimSpace(s.Hostname) == "" {
		return fmt.Errorf("runtime scope hostname is required")
	}
	if strings.TrimSpace(s.Repo) == "" {
		return fmt.Errorf("runtime scope repo is required")
	}
	if !strings.Contains(s.Repo, "/") {
		return fmt.Errorf("runtime scope repo %q must be canonical owner/name", s.Repo)
	}
	if strings.TrimSpace(s.Runner) == "" {
		return fmt.Errorf("runtime scope runner identity is required")
	}
	return nil
}

// RuntimeScopeHash is hex(sha256(scope.Key()))[:16], the same 32-char shape
// as the session runtime hash so ValidHashName accepts it.
func RuntimeScopeHash(scope RuntimeScope) (string, error) {
	if err := scope.Validate(); err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(scope.Key()))
	return hex.EncodeToString(sum[:16]), nil
}

// RuntimeHomeRoot maps a scope to `<workspaceRoot>/.runner-home/<hash>`.
// It only joins paths; PrepareRuntimeHome enforces the canonical fail-closed
// filesystem checks.
func RuntimeHomeRoot(workspaceRoot string, scope RuntimeScope) (string, error) {
	hash, err := RuntimeScopeHash(scope)
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(strings.TrimSpace(workspaceRoot))
	if err != nil {
		return "", fmt.Errorf("resolve workspace root for runtime home: %w", err)
	}
	clean := filepath.Clean(abs)
	if clean == string(os.PathSeparator) {
		return "", fmt.Errorf("workspace root %q cannot be filesystem root for runtime home", clean)
	}
	return filepath.Join(clean, RunnerHomesDirName, hash), nil
}

// RuntimeHomePaths is the shared runtime HOME layout: the same five subdirs
// the per-session runtime used, plus scope.json at the root.
type RuntimeHomePaths struct {
	Root           string
	Home           string
	GHConfigDir    string
	XDGConfigHome  string
	CodexHome      string
	AcpxRuntimeDir string
}

// RuntimeHomePathsFor derives the standard layout below a scope root.
func RuntimeHomePathsFor(root string) RuntimeHomePaths {
	return RuntimeHomePaths{
		Root:           root,
		Home:           filepath.Join(root, "home"),
		GHConfigDir:    filepath.Join(root, "gh"),
		XDGConfigHome:  filepath.Join(root, "xdg"),
		CodexHome:      filepath.Join(root, "codex"),
		AcpxRuntimeDir: filepath.Join(root, "acpx-runtime"),
	}
}

// runtimeScopeFile is the persisted scope binding inside a runtime home root.
type runtimeScopeFile struct {
	SchemaVersion int       `json:"schema_version"`
	Hostname      string    `json:"hostname"`
	Realm         string    `json:"realm"`
	Repo          string    `json:"repo"`
	Runner        string    `json:"runner"`
	CreatedAt     time.Time `json:"created_at"`
}

// PrepareRuntimeHome creates (or validates) the shared runtime HOME for a
// scope. Every level is created 0700 fail-closed: non-symlink directories
// only, confined below the canonical workspace root, never colliding with a
// protected root entry or an existing file. An existing scope.json must match
// the requested scope exactly; a mismatch or a foreign schema fails closed.
func PrepareRuntimeHome(workspaceRoot string, scope RuntimeScope) (RuntimeHomePaths, error) {
	if err := scope.Validate(); err != nil {
		return RuntimeHomePaths{}, err
	}
	hash, err := RuntimeScopeHash(scope)
	if err != nil {
		return RuntimeHomePaths{}, err
	}
	canonical, err := Canonicalize(workspaceRoot)
	if err != nil {
		return RuntimeHomePaths{}, err
	}
	root, err := preparePrivateLevels(canonical, RunnerHomesDirName, hash)
	if err != nil {
		return RuntimeHomePaths{}, err
	}
	paths := RuntimeHomePathsFor(root)
	for _, dir := range []string{paths.Home, paths.GHConfigDir, paths.XDGConfigHome, paths.CodexHome, paths.AcpxRuntimeDir} {
		if _, err := preparePrivateLevels(canonical, RunnerHomesDirName, hash, filepath.Base(dir)); err != nil {
			return RuntimeHomePaths{}, err
		}
	}
	if err := ensureRuntimeScopeFile(paths.Root, scope); err != nil {
		return RuntimeHomePaths{}, err
	}
	return paths, nil
}

// ensureRuntimeScopeFile writes scope.json on first prepare and pins the
// binding afterwards: an existing file must name the identical scope.
func ensureRuntimeScopeFile(root string, scope RuntimeScope) error {
	path := filepath.Join(root, runtimeScopeFileName)
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		payload, err := json.MarshalIndent(runtimeScopeFile{
			SchemaVersion: runtimeScopeVersion,
			Hostname:      scope.Hostname,
			Realm:         scope.Realm,
			Repo:          scope.Repo,
			Runner:        scope.Runner,
			CreatedAt:     time.Now().UTC(),
		}, "", "  ")
		if err != nil {
			return err
		}
		return state.WriteAtomic(path, append(payload, '\n'))
	case err != nil:
		return fmt.Errorf("read runtime scope file: %w", err)
	}
	return compareRuntimeScopeData(path, data, scope)
}

// checkRuntimeScopeBinding validates an existing scope.json without writing:
// a missing file is not yet bound and passes.
func checkRuntimeScopeBinding(root string, scope RuntimeScope) error {
	path := filepath.Join(root, runtimeScopeFileName)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read runtime scope file: %w", err)
	}
	return compareRuntimeScopeData(path, data, scope)
}

// compareRuntimeScopeData fails closed on an unreadable, foreign-schema, or
// scope-mismatching binding, naming both scopes in the error.
func compareRuntimeScopeData(path string, data []byte, scope RuntimeScope) error {
	var existing runtimeScopeFile
	if err := json.Unmarshal(data, &existing); err != nil {
		return fmt.Errorf("runtime scope file %q is unreadable; refusing to reuse the home: %w", path, err)
	}
	if existing.SchemaVersion != runtimeScopeVersion {
		return fmt.Errorf("runtime scope file %q has schema version %d, this binary requires %d", path, existing.SchemaVersion, runtimeScopeVersion)
	}
	if existing.Hostname != scope.Hostname || existing.Realm != scope.Realm || existing.Repo != scope.Repo || existing.Runner != scope.Runner {
		return fmt.Errorf("runtime home %q is bound to scope hostname=%q realm=%q repo=%q runner=%q; refusing scope hostname=%q realm=%q repo=%q runner=%q",
			path, existing.Hostname, existing.Realm, existing.Repo, existing.Runner,
			scope.Hostname, scope.Realm, scope.Repo, scope.Runner)
	}
	return nil
}

// JobScratchRoot maps a job ID to `<workspaceRoot>/.job-scratch/<jobID>`. The
// job ID must have the exact dispatcher scratch identity shape.
func JobScratchRoot(workspaceRoot, jobID string) (string, error) {
	if !jobScratchIDPattern.MatchString(jobID) {
		return "", fmt.Errorf("job id %q is not a valid scratch identity", jobID)
	}
	abs, err := filepath.Abs(strings.TrimSpace(workspaceRoot))
	if err != nil {
		return "", fmt.Errorf("resolve workspace root for job scratch: %w", err)
	}
	clean := filepath.Clean(abs)
	if clean == string(os.PathSeparator) {
		return "", fmt.Errorf("workspace root %q cannot be filesystem root for job scratch", clean)
	}
	return filepath.Join(clean, JobScratchDirName, jobID), nil
}

// JobScratchPaths is the per-job disposable scratch layout.
type JobScratchPaths struct {
	Root     string
	Tmp      string
	GoTmp    string
	XDGData  string
	XDGState string
}

// PrepareJobScratch creates the job's scratch tree 0700 with the same
// fail-closed checks as PrepareRuntimeHome.
func PrepareJobScratch(workspaceRoot, jobID string) (JobScratchPaths, error) {
	if !jobScratchIDPattern.MatchString(jobID) {
		return JobScratchPaths{}, fmt.Errorf("job id %q is not a valid scratch identity", jobID)
	}
	canonical, err := Canonicalize(workspaceRoot)
	if err != nil {
		return JobScratchPaths{}, err
	}
	root, err := preparePrivateLevels(canonical, JobScratchDirName, jobID)
	if err != nil {
		return JobScratchPaths{}, err
	}
	paths := JobScratchPaths{
		Root:     root,
		Tmp:      filepath.Join(root, "tmp"),
		GoTmp:    filepath.Join(root, "go-tmp"),
		XDGData:  filepath.Join(root, "xdg-data"),
		XDGState: filepath.Join(root, "xdg-state"),
	}
	for _, dir := range []string{paths.Tmp, paths.GoTmp, paths.XDGData, paths.XDGState} {
		if _, err := preparePrivateLevels(canonical, JobScratchDirName, jobID, filepath.Base(dir)); err != nil {
			return JobScratchPaths{}, err
		}
	}
	return paths, nil
}

// preparePrivateLevels creates each component below the canonical root like
// the dispatcher's preparePrivateCanonicalDir: 0700, Lstat-verified
// non-symlink directories, and a final canonical confinement proof.
func preparePrivateLevels(canonicalRoot string, components ...string) (string, error) {
	current := canonicalRoot
	for _, component := range components {
		if protectedRootEntries[component] {
			return "", fmt.Errorf("entry %q collides with a protected root entry", component)
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		switch {
		case errors.Is(err, os.ErrNotExist):
			if err := os.Mkdir(current, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
				return "", fmt.Errorf("create private directory %q: %w", current, err)
			}
			info, err = os.Lstat(current)
			if err != nil {
				return "", fmt.Errorf("verify private directory %q: %w", current, err)
			}
		case err != nil:
			return "", fmt.Errorf("inspect private directory %q: %w", current, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", fmt.Errorf("path %q must be a non-symlink directory", current)
		}
		if err := os.Chmod(current, 0o700); err != nil {
			return "", fmt.Errorf("enforce private permissions on %q: %w", current, err)
		}
	}
	resolved, err := filepath.EvalSymlinks(current)
	if err != nil || filepath.Clean(resolved) != current {
		return "", fmt.Errorf("path %q must be canonical without symlink traversal", current)
	}
	confined, err := workspace.ValidatePathUnderRoot(canonicalRoot, current)
	if err != nil {
		return "", err
	}
	if confined != current {
		return "", fmt.Errorf("path %q escapes the canonical workspace root", current)
	}
	return current, nil
}
