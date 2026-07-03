package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/higress-group/issue-spec/internal/model"
)

func TestPrepareNewUsesControlledUniquePath(t *testing.T) {
	dir := t.TempDir()
	git := &fakeGit{}
	m := New(dir, git, osFS{})
	st, err := m.PrepareNew(WorkspaceSpec{RepoURL: "https://github.com/o/r.git", RepoKey: "o/r", WorkspaceID: "job-1", DefaultBranch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if st.Path == "" || !strings.HasPrefix(st.Path, filepath.Clean(dir)) {
		t.Fatalf("unexpected path: %q", st.Path)
	}
	if !strings.Contains(st.Path, "o_r") {
		t.Fatalf("path not controlled: %q", st.Path)
	}
	if st.Branch != "main" {
		t.Fatalf("branch = %q", st.Branch)
	}
	if len(git.clones) != 1 || len(git.checkouts) != 1 {
		t.Fatalf("git calls = %#v %#v", git.clones, git.checkouts)
	}
}

func TestPrepareNewPrefersTrustedRef(t *testing.T) {
	dir := t.TempDir()
	git := &fakeGit{}
	m := New(dir, git, osFS{})
	st, err := m.PrepareNew(WorkspaceSpec{RepoURL: "repo", RepoKey: "o/r", WorkspaceID: "job-2", DefaultBranch: "main", TrustedRef: "refs/heads/release"})
	if err != nil {
		t.Fatal(err)
	}
	if st.Branch != "refs/heads/release" {
		t.Fatalf("branch = %q", st.Branch)
	}
	if git.checkouts[0] != "refs/heads/release" {
		t.Fatalf("checkout ref = %q", git.checkouts[0])
	}
}

func TestResumeValidatesStoredWorkspace(t *testing.T) {
	dir := t.TempDir()
	ws := filepath.Join(dir, "ws")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	m := New(dir, &fakeGit{currentRef: map[string]string{ws: "main"}}, osFS{})
	if err := m.Resume(*workspaceState(ws, "main")); err != nil {
		t.Fatal(err)
	}
}

func TestResumeReportsMissingWorkspace(t *testing.T) {
	m := New(t.TempDir(), &fakeGit{}, osFS{})
	err := m.Resume(*workspaceState("/missing/ws", "main"))
	if err == nil || !strings.Contains(err.Error(), "missing on disk") {
		t.Fatalf("unexpected err: %v", err)
	}
}

func TestLockIsExclusiveAndRecoverable(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "ws.lock")
	if err := os.WriteFile(lockPath, []byte("busy"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := New(dir, &fakeGit{}, osFS{})
	if _, err := m.Lock(filepath.Join(dir, "ws")); err == nil || !strings.Contains(err.Error(), "lock busy") {
		t.Fatalf("expected busy, got %v", err)
	}
	if err := os.Remove(lockPath); err != nil {
		t.Fatal(err)
	}
	unlock, err := m.Lock(filepath.Join(dir, "ws"))
	if err != nil {
		t.Fatal(err)
	}
	if err := unlock(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(lockPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lock not removed: %v", err)
	}
}

func TestBoundedDiagnostics(t *testing.T) {
	m := New(t.TempDir(), &fakeGit{cloneErr: errors.New(strings.Repeat("x", 500))}, osFS{})
	_, err := m.PrepareNew(WorkspaceSpec{RepoURL: "repo", RepoKey: "o/r", WorkspaceID: "job", DefaultBranch: "main"})
	if err == nil || len(err.Error()) > maxDiagnosticLen {
		t.Fatalf("unexpected diagnostic: %v", err)
	}
}

type fakeGit struct {
	clones      []string
	checkouts   []string
	currentRef  map[string]string
	cloneErr    error
	checkoutErr error
}

func (f *fakeGit) Clone(_, path string) error {
	if f.cloneErr != nil {
		return f.cloneErr
	}
	f.clones = append(f.clones, path)
	return nil
}

func (f *fakeGit) Checkout(path, ref string) error {
	if f.checkoutErr != nil {
		return f.checkoutErr
	}
	f.checkouts = append(f.checkouts, ref)
	return nil
}

func (f *fakeGit) CurrentRef(path string) (string, error) {
	if f.currentRef == nil {
		return "", os.ErrNotExist
	}
	ref, ok := f.currentRef[path]
	if !ok {
		return "", os.ErrNotExist
	}
	return ref, nil
}

type osFS struct{}

func (osFS) Stat(path string) (os.FileInfo, error)        { return os.Stat(path) }
func (osFS) MkdirAll(path string, perm os.FileMode) error { return os.MkdirAll(path, perm) }
func (osFS) RemoveAll(path string) error                  { return os.RemoveAll(path) }

func workspaceState(path, branch string) *model.WorkspaceState {
	return &model.WorkspaceState{Path: path, Branch: branch}
}
