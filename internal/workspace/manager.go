package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/higress-group/issue-spec/internal/model"
)

const maxDiagnosticLen = 220

type Git interface {
	Clone(repoURL, path string) error
	Checkout(path, ref string) error
	CurrentRef(path string) (string, error)
}

type FS interface {
	Stat(path string) (os.FileInfo, error)
	MkdirAll(path string, perm os.FileMode) error
	RemoveAll(path string) error
}

type Manager struct {
	root string
	git  Git
	fs   FS
}

func New(root string, git Git, fs FS) *Manager {
	return &Manager{root: filepath.Clean(root), git: git, fs: fs}
}

type WorkspaceSpec struct {
	RepoURL       string
	RepoKey       string
	WorkspaceID   string
	DefaultBranch string
	TrustedRef    string
}

func (m *Manager) PrepareNew(spec WorkspaceSpec) (*model.WorkspaceState, error) {
	if err := m.fs.MkdirAll(m.root, 0o755); err != nil {
		return nil, wrapDiagnostic("workspace root unavailable", err)
	}
	path := m.controlledPath(spec.RepoKey, spec.WorkspaceID)
	if _, err := m.fs.Stat(path); err == nil {
		return nil, fmt.Errorf("workspace already exists: %s", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, wrapDiagnostic("workspace access failed", err)
	}
	if err := m.git.Clone(spec.RepoURL, path); err != nil {
		return nil, wrapDiagnostic("workspace clone failed", err)
	}
	ref := strings.TrimSpace(spec.TrustedRef)
	if ref == "" {
		ref = strings.TrimSpace(spec.DefaultBranch)
	}
	if ref == "" {
		ref = "main"
	}
	if err := m.git.Checkout(path, ref); err != nil {
		return nil, wrapDiagnostic("workspace checkout failed", err)
	}
	return &model.WorkspaceState{
		ID:        spec.WorkspaceID,
		Path:      path,
		Repo:      spec.RepoKey,
		Branch:    ref,
		Locked:    false,
		UpdatedAt: time.Now(),
	}, nil
}

func (m *Manager) Resume(st model.WorkspaceState) error {
	if strings.TrimSpace(st.Path) == "" {
		return fmt.Errorf("workspace missing path")
	}
	info, err := m.fs.Stat(st.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("workspace missing on disk: %s", st.Path)
		}
		return wrapDiagnostic("workspace access failed", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("workspace path is not a directory: %s", st.Path)
	}
	if st.Branch != "" {
		ref, err := m.git.CurrentRef(st.Path)
		if err != nil {
			return wrapDiagnostic("workspace validation failed", err)
		}
		if ref != st.Branch {
			return fmt.Errorf("workspace ref mismatch: want %s got %s", st.Branch, ref)
		}
	}
	return nil
}

func (m *Manager) Lock(path string) (func() error, error) {
	lockPath := path + ".lock"
	if err := m.fs.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		return nil, wrapDiagnostic("workspace lock path unavailable", err)
	}
	if _, err := m.fs.Stat(lockPath); err == nil {
		return nil, fmt.Errorf("workspace lock busy: %s", lockPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, wrapDiagnostic("workspace lock access failed", err)
	}
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("workspace lock busy: %s", lockPath)
		}
		return nil, wrapDiagnostic("workspace lock failed", err)
	}
	_ = f.Close()
	released := false
	unlock := func() error {
		if released {
			return nil
		}
		released = true
		if err := m.fs.RemoveAll(lockPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return wrapDiagnostic("workspace unlock failed", err)
		}
		return nil
	}
	return unlock, nil
}

func (m *Manager) controlledPath(repoKey, workspaceID string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(repoKey) + ":" + strings.TrimSpace(workspaceID)))
	name := hex.EncodeToString(sum[:8])
	return filepath.Join(m.root, sanitize(repoKey), name)
}

func sanitize(v string) string {
	v = strings.TrimSpace(v)
	v = strings.NewReplacer("/", "_", string(filepath.Separator), "_", " ", "_", ":", "_").Replace(v)
	if v == "" {
		return "default"
	}
	return v
}

func wrapDiagnostic(prefix string, err error) error {
	if err == nil {
		return nil
	}
	msg := prefix + ": " + err.Error()
	if len(msg) > maxDiagnosticLen {
		msg = msg[:maxDiagnosticLen-3] + "..."
	}
	return errors.New(msg)
}
