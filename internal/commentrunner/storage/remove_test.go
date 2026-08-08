package storage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makeTreeReadOnly materializes the permission shape the Go module cache
// leaves behind: every directory 0555 and every file 0444.
func makeTreeReadOnly(t *testing.T, root string) {
	t.Helper()
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if entry.IsDir() {
			return os.Chmod(path, 0o555)
		}
		return os.Chmod(path, 0o444)
	}); err != nil {
		t.Fatalf("make %s read-only: %v", root, err)
	}
}

// TestMakeOpenedTreeWritableRelaxesReadOnlyTree pins the relaxation pass
// itself, independent of euid: directories become 0700 and files 0600 so a
// non-root owner can unlink the tree, while symlinks are never followed and
// nothing outside the opened root changes mode.
func TestMakeOpenedTreeWritableRelaxesReadOnlyTree(t *testing.T) {
	base := t.TempDir()
	t.Cleanup(func() { relaxTreeForCleanup(t, base) })
	tree := filepath.Join(base, "tree")
	outside := filepath.Join(base, "outside")
	writeFile(t, filepath.Join(tree, "sub", "nested", "ro.txt"), 8)
	writeFile(t, filepath.Join(tree, "ro.txt"), 8)
	writeFile(t, filepath.Join(outside, "target.txt"), 8)
	if err := os.Symlink(filepath.Join(outside, "target.txt"), filepath.Join(tree, "link")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	makeTreeReadOnly(t, tree)
	makeTreeReadOnly(t, outside)

	opened, err := os.OpenRoot(tree)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	makeOpenedTreeWritable(opened)
	if err := opened.Close(); err != nil {
		t.Fatalf("close root: %v", err)
	}

	for path, want := range map[string]os.FileMode{
		tree:                                 0o700,
		filepath.Join(tree, "sub"):           0o700,
		filepath.Join(tree, "sub", "nested"): 0o700,
		filepath.Join(tree, "sub", "nested", "ro.txt"): 0o600,
		filepath.Join(tree, "ro.txt"):                  0o600,
	} {
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatalf("lstat %s: %v", path, err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Fatalf("mode %s = %o, want %o", path, got, want)
		}
	}
	// The symlink is never followed: it survives as a link and its target
	// outside the opened root keeps its read-only mode.
	linkInfo, err := os.Lstat(filepath.Join(tree, "link"))
	if err != nil || linkInfo.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("link must remain a symlink: info=%v err=%v", linkInfo, err)
	}
	targetInfo, err := os.Lstat(filepath.Join(outside, "target.txt"))
	if err != nil {
		t.Fatalf("lstat outside target: %v", err)
	}
	if got := targetInfo.Mode().Perm(); got != 0o444 {
		t.Fatalf("outside target mode = %o, want 444 (never relaxed)", got)
	}
	outsideInfo, err := os.Lstat(outside)
	if err != nil {
		t.Fatalf("lstat outside dir: %v", err)
	}
	if got := outsideInfo.Mode().Perm(); got != 0o555 {
		t.Fatalf("outside dir mode = %o, want 555 (never relaxed)", got)
	}
}

// TestRemoveManagedTreeRemovesReadOnlySessionRuntime is the legacy-layout
// regression: a retired .sessions/<hash> runtime whose home holds a
// read-only Go module cache must still delete cleanly for the non-root
// service user. The shared removal helper relaxes the validated tree through
// the opened capability before unlinking, with every validation unchanged.
func TestRemoveManagedTreeRemovesReadOnlySessionRuntime(t *testing.T) {
	root := testRoot(t)
	t.Cleanup(func() { relaxTreeForCleanup(t, root) })
	hash := strings.Repeat("ab", 16)
	target := filepath.Join(root, SessionsDirName, hash)
	writeFile(t, filepath.Join(target, "home", "go", "pkg", "mod", "example.com", "dep@v1.0.0", "dep.go"), 24)
	makeTreeReadOnly(t, filepath.Join(target, "home"))

	if err := RemoveManagedTree(root, target, hash, nil); err != nil {
		t.Fatalf("RemoveManagedTree on a read-only runtime home: %v", err)
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatalf("session runtime must be gone, err=%v", err)
	}
}
