package workspace

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/higress-group/issue-spec/internal/commentrunner/state"
)

const (
	retentionPolicyPrefix = "delete_after_last_used="
)

var ErrLocked = errors.New("workspace lock held")

var activeWorkspaceLocks = struct {
	sync.Mutex
	byPath map[string]*activeWorkspaceLock
}{byPath: map[string]*activeWorkspaceLock{}}

type activeWorkspaceLock struct {
	file  *os.File
	path  string
	token string
	jobID string
	mu    sync.Mutex
}

type Command struct {
	Binary string
	Args   []string
	Dir    string
}

type Result struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}

type Runner interface {
	Run(context.Context, Command) (Result, error)
}

type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, command Command) (Result, error) {
	binary := strings.TrimSpace(command.Binary)
	if binary == "" {
		binary = "git"
	}
	cmd := exec.CommandContext(ctx, binary, command.Args...)
	cmd.Dir = command.Dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	result := Result{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	if exitErr := new(exec.ExitError); errors.As(err, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
	} else if err != nil {
		result.ExitCode = -1
	}
	return result, err
}

type Manager struct {
	Root      string
	Retention time.Duration
	GitBinary string
	Runner    Runner
	Now       func() time.Time
	IDFunc    func(NewRequest) (string, error)
	TokenFunc func() (string, error)
}

type NewRequest struct {
	Repo            string
	CloneURL        string
	DefaultBranch   string
	Ref             string
	PublicSessionID string
	JobID           string
	WorkspaceID     string
	BranchName      string
}

type ResumeRequest struct {
	Repo      string
	CloneURL  string
	Workspace state.WorkspaceMetadata
}

type Binding struct {
	Workspace            state.WorkspaceMetadata `json:"workspace"`
	AcpxWorkingDirectory string                  `json:"acpx_working_directory"`
	SandboxWorkspacePath string                  `json:"sandbox_workspace_path"`
}

type DiagnosticError struct {
	Operation   string
	Diagnostics []string
	Err         error
}

func (e *DiagnosticError) Error() string {
	return e.Operation + ": " + e.Err.Error()
}

func (m Manager) PrepareNew(ctx context.Context, req NewRequest) (Binding, error) {
	nm, root, err := m.normalized()
	if err != nil {
		return Binding{}, err
	}
	if err := validateNewRequest(req); err != nil {
		return Binding{}, err
	}
	workspaceID := strings.TrimSpace(req.WorkspaceID)
	if workspaceID == "" {
		workspaceID, err = nm.IDFunc(req)
		if err != nil {
			return Binding{}, err
		}
	}
	path, err := workspacePath(root, workspaceID)
	if err != nil {
		return Binding{}, err
	}
	branch := strings.TrimSpace(req.BranchName)
	if branch == "" {
		branch = "issue-spec-" + workspaceID
	}
	if err := validateGitRef("branch", branch); err != nil {
		return Binding{}, err
	}
	baseRef := checkoutRef(req)
	if err := validateGitRef("checkout ref", baseRef); err != nil {
		return Binding{}, err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return Binding{}, err
	}
	if _, err := os.Stat(path); err == nil {
		return Binding{}, fmt.Errorf("workspace path already exists: %s", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return Binding{}, err
	}

	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(path)
		}
	}()
	if _, err := nm.runGit(ctx, "git clone", "", "clone", "--", req.CloneURL, path); err != nil {
		return Binding{}, err
	}
	if _, err := nm.runGit(ctx, "git checkout base ref", path, "checkout", "--force", baseRef); err != nil {
		return Binding{}, err
	}
	if _, err := nm.runGit(ctx, "git create workspace branch", path, "checkout", "-B", branch); err != nil {
		return Binding{}, err
	}
	head, err := nm.gitOutput(ctx, "git rev-parse HEAD", path, "rev-parse", "HEAD")
	if err != nil {
		return Binding{}, err
	}
	actualBranch, err := nm.gitOutput(ctx, "git rev-parse branch", path, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return Binding{}, err
	}

	now := nm.Now().UTC()
	workspace := state.WorkspaceMetadata{
		ID:              workspaceID,
		Path:            path,
		Repo:            strings.TrimSpace(req.Repo),
		CloneURL:        strings.TrimSpace(req.CloneURL),
		Branch:          actualBranch,
		Ref:             baseRef,
		CheckoutSHA:     head,
		CreatedAt:       now,
		LastUsedAt:      now,
		RetentionPolicy: retentionPolicy(nm.Retention),
		CleanupAfter:    now.Add(nm.Retention),
	}
	cleanup = false
	return Binding{Workspace: workspace, AcpxWorkingDirectory: path, SandboxWorkspacePath: path}, nil
}

func (m Manager) ResolveResume(ctx context.Context, req ResumeRequest) (Binding, error) {
	nm, root, err := m.normalized()
	if err != nil {
		return Binding{}, err
	}
	workspace := req.Workspace
	if strings.TrimSpace(workspace.ID) == "" || strings.TrimSpace(workspace.Path) == "" {
		return Binding{}, fmt.Errorf("stored workspace id and path are required")
	}
	path, err := validatePathUnderRoot(root, workspace.Path)
	if err != nil {
		return Binding{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return Binding{}, fmt.Errorf("workspace %q is not accessible: %w", path, err)
	}
	if !info.IsDir() {
		return Binding{}, fmt.Errorf("workspace %q is not a directory", path)
	}
	if req.Repo != "" && workspace.Repo != "" && strings.TrimSpace(req.Repo) != strings.TrimSpace(workspace.Repo) {
		return Binding{}, fmt.Errorf("workspace repo mismatch: stored %q requested %q", workspace.Repo, req.Repo)
	}
	top, err := nm.gitOutput(ctx, "git rev-parse toplevel", path, "rev-parse", "--show-toplevel")
	if err != nil {
		return Binding{}, err
	}
	topPath, err := validatePathUnderRoot(root, top)
	if err != nil {
		return Binding{}, err
	}
	if filepath.Clean(topPath) != filepath.Clean(path) {
		return Binding{}, fmt.Errorf("workspace git toplevel %q does not match stored path %q", topPath, path)
	}
	wantClone := strings.TrimSpace(req.CloneURL)
	if wantClone == "" {
		wantClone = strings.TrimSpace(workspace.CloneURL)
	}
	if wantClone != "" {
		gotClone, err := nm.gitOutput(ctx, "git remote origin", path, "remote", "get-url", "origin")
		if err != nil {
			return Binding{}, err
		}
		if gotClone != wantClone {
			return Binding{}, fmt.Errorf("workspace clone URL mismatch: stored %q found %q", wantClone, gotClone)
		}
	}
	if workspace.Branch != "" && workspace.Branch != "HEAD" {
		branch, err := nm.gitOutput(ctx, "git rev-parse branch", path, "rev-parse", "--abbrev-ref", "HEAD")
		if err != nil {
			return Binding{}, err
		}
		if branch != workspace.Branch {
			return Binding{}, fmt.Errorf("workspace branch mismatch: stored %q found %q", workspace.Branch, branch)
		}
	}
	now := nm.Now().UTC()
	workspace.Path = path
	workspace.LastUsedAt = now
	workspace.RetentionPolicy = retentionPolicy(nm.Retention)
	workspace.CleanupAfter = now.Add(nm.Retention)
	return Binding{Workspace: workspace, AcpxWorkingDirectory: path, SandboxWorkspacePath: path}, nil
}

type LockRequest struct {
	Repo            string
	PublicSessionID string
	JobID           string
	WorkspaceID     string
}

type lockRecord struct {
	Repo            string    `json:"repo"`
	PublicSessionID string    `json:"public_session_id"`
	JobID           string    `json:"job_id"`
	WorkspaceID     string    `json:"workspace_id,omitempty"`
	Token           string    `json:"token"`
	ProcessID       int       `json:"pid,omitempty"`
	AcquiredAt      time.Time `json:"acquired_at"`
}

func (m Manager) AcquireLock(ctx context.Context, req LockRequest) (state.SessionLock, error) {
	nm, root, err := m.normalized()
	if err != nil {
		return state.SessionLock{}, err
	}
	if strings.TrimSpace(req.Repo) == "" || strings.TrimSpace(req.PublicSessionID) == "" || strings.TrimSpace(req.JobID) == "" {
		return state.SessionLock{}, fmt.Errorf("lock requires repo, public session id, and job id")
	}
	if err := checkContext(ctx); err != nil {
		return state.SessionLock{}, err
	}
	lockPath, err := lockPath(root, req.Repo, req.PublicSessionID)
	if err != nil {
		return state.SessionLock{}, err
	}
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return state.SessionLock{}, err
	}
	if owner := activeWorkspaceLockOwner(lockPath); owner != "" {
		return state.SessionLock{}, &LockError{Path: lockPath, OwnerJobID: owner}
	}
	file, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return state.SessionLock{}, err
	}
	if err := tryLockFile(file); err != nil {
		owner := ""
		if existing, readErr := readLock(lockPath); readErr == nil {
			owner = existing.JobID
		}
		_ = file.Close()
		if lockUnavailable(err) {
			return state.SessionLock{}, &LockError{Path: lockPath, OwnerJobID: owner}
		}
		return state.SessionLock{}, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return state.SessionLock{}, err
	}
	var recovered time.Time
	now := nm.Now().UTC()
	if info.Size() > 0 {
		recovered = now
	}
	token, err := nm.TokenFunc()
	if err != nil {
		_ = file.Close()
		return state.SessionLock{}, err
	}
	record := lockRecord{
		Repo:            strings.TrimSpace(req.Repo),
		PublicSessionID: strings.TrimSpace(req.PublicSessionID),
		JobID:           strings.TrimSpace(req.JobID),
		WorkspaceID:     strings.TrimSpace(req.WorkspaceID),
		Token:           token,
		ProcessID:       os.Getpid(),
		AcquiredAt:      now,
	}
	active := &activeWorkspaceLock{file: file, path: lockPath, token: token, jobID: record.JobID}
	registered := false
	if owner := registerWorkspaceLock(lockPath, active); owner != "" {
		_ = file.Close()
		return state.SessionLock{}, &LockError{Path: lockPath, OwnerJobID: owner}
	}
	registered = true
	defer func() {
		if registered {
			return
		}
		unregisterWorkspaceLock(lockPath, active)
		_ = file.Close()
	}()
	if err := writeLockRecord(file, record); err != nil {
		registered = false
		return state.SessionLock{}, err
	}
	registered = true
	return state.SessionLock{
		OwnerJobID:         record.JobID,
		AcquiredAt:         record.AcquiredAt,
		WorkspaceLockToken: record.Token,
		WorkspaceLockPath:  lockPath,
		StaleRecoveredAt:   recovered,
	}, nil
}

type LockError struct {
	Path       string
	OwnerJobID string
}

func (e *LockError) Error() string {
	if e.OwnerJobID == "" {
		return fmt.Sprintf("%s: %v", e.Path, ErrLocked)
	}
	return fmt.Sprintf("%s: %v by %s", e.Path, ErrLocked, e.OwnerJobID)
}

func (e *LockError) Is(target error) bool {
	return target == ErrLocked
}

func (m Manager) ReleaseLock(lock state.SessionLock) error {
	_, root, err := m.normalized()
	if err != nil {
		return err
	}
	path, err := validatePathUnderRoot(root, lock.WorkspaceLockPath)
	if err != nil {
		return err
	}
	record, err := readLock(path)
	if errors.Is(err, os.ErrNotExist) {
		if active, ok := takeWorkspaceLock(path, lock); ok {
			active.mu.Lock()
			closeErr := active.file.Close()
			active.mu.Unlock()
			return closeErr
		}
		return nil
	}
	if err != nil {
		return err
	}
	if record.Token != lock.WorkspaceLockToken || record.JobID != lock.OwnerJobID {
		return fmt.Errorf("workspace lock token or owner mismatch")
	}
	if active, ok := takeWorkspaceLock(path, lock); ok {
		active.mu.Lock()
		removeErr := removeOpenLockPath(active.file, path)
		closeErr := active.file.Close()
		active.mu.Unlock()
		if closeErr != nil {
			return closeErr
		}
		return removeErr
	}
	if activeWorkspaceLockOwner(path) != "" {
		return fmt.Errorf("workspace lock token or owner mismatch")
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := tryLockFile(file); err != nil {
		_ = file.Close()
		if lockUnavailable(err) {
			return ErrLocked
		}
		return err
	}
	removeErr := removeOpenLockPath(file, path)
	closeErr := file.Close()
	if closeErr != nil {
		return closeErr
	}
	return removeErr
}

type CleanupRequest struct {
	Workspaces []state.WorkspaceMetadata
	ActiveIDs  map[string]bool
	DryRun     bool
	Now        time.Time
}

type CleanupResult struct {
	WorkspaceID string `json:"workspace_id"`
	Path        string `json:"path"`
	Action      string `json:"action"`
	Reason      string `json:"reason,omitempty"`
	Removed     bool   `json:"removed,omitempty"`
}

func (m Manager) Cleanup(ctx context.Context, req CleanupRequest) ([]CleanupResult, error) {
	nm, root, err := m.normalized()
	if err != nil {
		return nil, err
	}
	now := req.Now.UTC()
	if now.IsZero() {
		now = nm.Now().UTC()
	}
	var results []CleanupResult
	for _, workspace := range req.Workspaces {
		if err := checkContext(ctx); err != nil {
			return results, err
		}
		result := CleanupResult{WorkspaceID: workspace.ID, Path: workspace.Path}
		if req.ActiveIDs[workspace.ID] {
			result.Action, result.Reason = "kept", "active"
			results = append(results, result)
			continue
		}
		if workspace.Dirty || workspace.Uncertain {
			result.Action, result.Reason = "kept", "dirty_or_uncertain"
			results = append(results, result)
			continue
		}
		path, err := validatePathUnderRoot(root, workspace.Path)
		if err != nil {
			result.Action, result.Reason = "rejected", err.Error()
			results = append(results, result)
			return results, err
		}
		result.Path = path
		cleanupAfter := workspace.CleanupAfter
		lastUsed := workspace.LastUsedAt
		if lastUsed.IsZero() {
			lastUsed = workspace.CreatedAt
		}
		if !lastUsed.IsZero() {
			lastUsedCleanupAfter := lastUsed.Add(nm.Retention)
			if cleanupAfter.IsZero() || cleanupAfter.Before(lastUsedCleanupAfter) {
				cleanupAfter = lastUsedCleanupAfter
			}
		}
		if cleanupAfter.IsZero() {
			cleanupAfter = lastUsed.Add(nm.Retention)
		}
		if now.Before(cleanupAfter) {
			result.Action, result.Reason = "kept", "within_retention"
			results = append(results, result)
			continue
		}
		if req.DryRun {
			result.Action, result.Reason = "would_remove", "expired"
			results = append(results, result)
			continue
		}
		if err := os.RemoveAll(path); err != nil {
			result.Action, result.Reason = "failed", err.Error()
			results = append(results, result)
			return results, err
		}
		result.Action, result.Reason, result.Removed = "removed", "expired", true
		results = append(results, result)
	}
	return results, nil
}

func (m Manager) normalized() (Manager, string, error) {
	root := strings.TrimSpace(m.Root)
	if root == "" {
		return Manager{}, "", fmt.Errorf("workspace root is required")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return Manager{}, "", err
	}
	absRoot, err = canonicalPath(absRoot)
	if err != nil {
		return Manager{}, "", err
	}
	if m.Retention <= 0 {
		return Manager{}, "", fmt.Errorf("workspace retention must be positive")
	}
	if m.GitBinary == "" {
		m.GitBinary = "git"
	}
	if m.Runner == nil {
		m.Runner = ExecRunner{}
	}
	if m.Now == nil {
		m.Now = func() time.Time { return time.Now().UTC() }
	}
	if m.IDFunc == nil {
		m.IDFunc = defaultWorkspaceID
	}
	if m.TokenFunc == nil {
		m.TokenFunc = randomHex
	}
	return m, filepath.Clean(absRoot), nil
}

func validateNewRequest(req NewRequest) error {
	if strings.TrimSpace(req.Repo) == "" {
		return fmt.Errorf("repo is required")
	}
	if strings.TrimSpace(req.CloneURL) == "" {
		return fmt.Errorf("clone URL is required")
	}
	if checkoutRef(req) == "" {
		return fmt.Errorf("default branch or trusted ref is required")
	}
	if strings.TrimSpace(req.PublicSessionID) == "" {
		return fmt.Errorf("public session id is required")
	}
	if strings.TrimSpace(req.JobID) == "" {
		return fmt.Errorf("job id is required")
	}
	return nil
}

func checkoutRef(req NewRequest) string {
	if ref := strings.TrimSpace(req.Ref); ref != "" {
		return ref
	}
	return strings.TrimSpace(req.DefaultBranch)
}

func retentionPolicy(retention time.Duration) string {
	return retentionPolicyPrefix + retention.String()
}

func (m Manager) runGit(ctx context.Context, operation, dir string, args ...string) (Result, error) {
	result, err := m.Runner.Run(ctx, Command{Binary: m.GitBinary, Args: args, Dir: dir})
	if err != nil || result.ExitCode != 0 {
		if err == nil {
			err = fmt.Errorf("exit code %d", result.ExitCode)
		}
		return result, &DiagnosticError{Operation: operation, Diagnostics: diagnostics(result, err), Err: err}
	}
	return result, nil
}

func (m Manager) gitOutput(ctx context.Context, operation, dir string, args ...string) (string, error) {
	result, err := m.runGit(ctx, operation, dir, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(result.Stdout)), nil
}

func diagnostics(result Result, err error) []string {
	var out []string
	if err != nil {
		out = append(out, bound("error: "+err.Error()))
	}
	if result.ExitCode != 0 {
		out = append(out, fmt.Sprintf("exit_code=%d", result.ExitCode))
	}
	if len(bytes.TrimSpace(result.Stderr)) > 0 {
		out = append(out, bound("stderr: "+string(bytes.TrimSpace(result.Stderr))))
	}
	if len(bytes.TrimSpace(result.Stdout)) > 0 {
		out = append(out, bound("stdout: "+string(bytes.TrimSpace(result.Stdout))))
	}
	return out
}

func bound(value string) string {
	const limit = 1024
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "...(truncated)"
}

func defaultWorkspaceID(req NewRequest) (string, error) {
	token, err := randomHex()
	if err != nil {
		return "", err
	}
	id := "ws-" + token[:20]
	if err := validateIDSegment(id); err != nil {
		return "", err
	}
	return id, nil
}

func randomHex() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func workspacePath(root, id string) (string, error) {
	if err := validateIDSegment(id); err != nil {
		return "", err
	}
	path := filepath.Join(root, id)
	return validatePathUnderRoot(root, path)
}

func lockPath(root, repo, publicSessionID string) (string, error) {
	key := sha256.Sum256([]byte(strings.TrimSpace(repo) + "\x00" + strings.TrimSpace(publicSessionID)))
	path := filepath.Join(root, ".locks", hex.EncodeToString(key[:])+".lock")
	return validatePathUnderRoot(root, path)
}

func validateIDSegment(id string) error {
	id = strings.TrimSpace(id)
	if id == "" || id == "." || id == ".." {
		return fmt.Errorf("unsafe workspace id %q", id)
	}
	if strings.Contains(id, "/") || strings.Contains(id, "\\") || strings.Contains(id, "..") || strings.HasPrefix(id, ".") {
		return fmt.Errorf("unsafe workspace id %q", id)
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && r != '-' && r != '_' && r != '.' {
			return fmt.Errorf("unsafe workspace id %q", id)
		}
	}
	return nil
}

func validatePathUnderRoot(root, path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("path is required")
	}
	absRoot, err := canonicalPath(root)
	if err != nil {
		return "", err
	}
	absPath, err := canonicalPath(path)
	if err != nil {
		return "", err
	}
	absRoot = filepath.Clean(absRoot)
	absPath = filepath.Clean(absPath)
	rel, err := filepath.Rel(absRoot, absPath)
	if err != nil {
		return "", err
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("path %q escapes workspace root %q", path, absRoot)
	}
	return absPath, nil
}

func canonicalPath(path string) (string, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	absPath = filepath.Clean(absPath)
	evaluated, err := filepath.EvalSymlinks(absPath)
	if err == nil {
		return filepath.Clean(evaluated), nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	var missing []string
	probe := absPath
	for {
		evaluated, err := filepath.EvalSymlinks(probe)
		if err == nil {
			for i := len(missing) - 1; i >= 0; i-- {
				evaluated = filepath.Join(evaluated, missing[i])
			}
			return filepath.Clean(evaluated), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return absPath, nil
		}
		missing = append(missing, filepath.Base(probe))
		probe = parent
	}
}

func validateGitRef(label, ref string) error {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return fmt.Errorf("%s is required", label)
	}
	if strings.HasPrefix(ref, "-") || strings.Contains(ref, "..") || strings.Contains(ref, "@{") {
		return fmt.Errorf("unsafe %s %q", label, ref)
	}
	if strings.HasSuffix(ref, ".") || strings.HasSuffix(ref, "/") || strings.HasPrefix(ref, "/") {
		return fmt.Errorf("unsafe %s %q", label, ref)
	}
	for _, r := range ref {
		if unicode.IsSpace(r) || unicode.IsControl(r) || strings.ContainsRune("~^:?*[\\", r) {
			return fmt.Errorf("unsafe %s %q", label, ref)
		}
	}
	return nil
}

func activeWorkspaceLockOwner(path string) string {
	activeWorkspaceLocks.Lock()
	defer activeWorkspaceLocks.Unlock()
	active := activeWorkspaceLocks.byPath[path]
	if active == nil {
		return ""
	}
	return active.jobID
}

func registerWorkspaceLock(path string, active *activeWorkspaceLock) string {
	activeWorkspaceLocks.Lock()
	defer activeWorkspaceLocks.Unlock()
	if existing := activeWorkspaceLocks.byPath[path]; existing != nil {
		return existing.jobID
	}
	activeWorkspaceLocks.byPath[path] = active
	return ""
}

func unregisterWorkspaceLock(path string, active *activeWorkspaceLock) {
	activeWorkspaceLocks.Lock()
	defer activeWorkspaceLocks.Unlock()
	if activeWorkspaceLocks.byPath[path] == active {
		delete(activeWorkspaceLocks.byPath, path)
	}
}

func takeWorkspaceLock(path string, lock state.SessionLock) (*activeWorkspaceLock, bool) {
	activeWorkspaceLocks.Lock()
	defer activeWorkspaceLocks.Unlock()
	active := activeWorkspaceLocks.byPath[path]
	if active == nil || active.token != lock.WorkspaceLockToken || active.jobID != lock.OwnerJobID {
		return nil, false
	}
	delete(activeWorkspaceLocks.byPath, path)
	return active, true
}

func writeLockRecord(file *os.File, record lockRecord) error {
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := file.Truncate(0); err != nil {
		return err
	}
	if _, err := file.Seek(0, 0); err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		return err
	}
	return file.Sync()
}

func removeOpenLockPath(file *os.File, path string) error {
	same, err := sameOpenFilePath(file, path)
	if err != nil {
		return err
	}
	if !same {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func sameOpenFilePath(file *os.File, path string) (bool, error) {
	if file == nil {
		return false, nil
	}
	fileInfo, err := file.Stat()
	if err != nil {
		return false, err
	}
	pathInfo, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return os.SameFile(fileInfo, pathInfo), nil
}

func readLock(path string) (lockRecord, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return lockRecord{}, err
	}
	var record lockRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return lockRecord{}, err
	}
	return record, nil
}

func checkContext(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
