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

func chmod(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod %s to %o: %v", path, mode, err)
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("lstat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("mode %s = %o, want %o", path, got, want)
	}
}

// unknownTypeEntry simulates a directory entry read from a filesystem that
// reports DT_UNKNOWN (some FUSE/NFS/CIFS mounts): the dirent carries no type
// bits, and IsDir — derived from those bits — is false even for directories.
type unknownTypeEntry struct {
	name string
	path string
}

func (e unknownTypeEntry) Name() string               { return e.name }
func (e unknownTypeEntry) IsDir() bool                { return e.Type().IsDir() }
func (e unknownTypeEntry) Type() os.FileMode          { return 0 }
func (e unknownTypeEntry) Info() (os.FileInfo, error) { return os.Lstat(e.path) }

// TestRelaxOpenedTreeEntryClassifiesUnknownDirentType pins the DT_UNKNOWN
// fallback: an entry whose dirent carries no type must be classified through
// the opened capability, never trusted as a file. A subdirectory behind an
// unknown type is enqueued for its own relaxation pass — and not chmodded
// 0600, which would strand its children — a read-only file is relaxed, and a
// symlink is left alone.
func TestRelaxOpenedTreeEntryClassifiesUnknownDirentType(t *testing.T) {
	base := t.TempDir()
	t.Cleanup(func() { relaxTreeForCleanup(t, base) })
	tree := filepath.Join(base, "tree")
	outside := filepath.Join(base, "outside")
	writeFile(t, filepath.Join(tree, "subdir", "child.txt"), 8)
	writeFile(t, filepath.Join(tree, "plain.txt"), 8)
	writeFile(t, filepath.Join(outside, "target.txt"), 8)
	if err := os.Symlink(filepath.Join(outside, "target.txt"), filepath.Join(tree, "link")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	chmod(t, filepath.Join(tree, "subdir", "child.txt"), 0o444)
	chmod(t, filepath.Join(tree, "subdir"), 0o555)
	chmod(t, filepath.Join(tree, "plain.txt"), 0o444)
	chmod(t, filepath.Join(outside, "target.txt"), 0o444)

	opened, err := os.OpenRoot(tree)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	pending := relaxOpenedTreeEntry(opened, ".", unknownTypeEntry{name: "subdir", path: filepath.Join(tree, "subdir")}, nil)
	if len(pending) != 1 || pending[0] != "subdir" {
		t.Fatalf("unknown-type subdirectory must be enqueued for descent, pending=%v", pending)
	}
	// Enqueueing must not have relaxed the directory as if it were a file.
	assertMode(t, filepath.Join(tree, "subdir"), 0o555)
	pending = relaxOpenedTreeEntry(opened, ".", unknownTypeEntry{name: "plain.txt", path: filepath.Join(tree, "plain.txt")}, pending)
	if len(pending) != 1 {
		t.Fatalf("a file must not be enqueued, pending=%v", pending)
	}
	assertMode(t, filepath.Join(tree, "plain.txt"), 0o600)
	pending = relaxOpenedTreeEntry(opened, ".", unknownTypeEntry{name: "link", path: filepath.Join(tree, "link")}, pending)
	if len(pending) != 1 {
		t.Fatalf("a symlink must not be enqueued, pending=%v", pending)
	}
	if err := opened.Close(); err != nil {
		t.Fatalf("close root: %v", err)
	}
	linkInfo, err := os.Lstat(filepath.Join(tree, "link"))
	if err != nil || linkInfo.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("link must remain a symlink: info=%v err=%v", linkInfo, err)
	}
	assertMode(t, filepath.Join(outside, "target.txt"), 0o444)
}

// TestMakeOpenedTreeWritableRelaxesUnlistableDirectory pins the owner-read
// edge of the directory predicate: a 0300 directory (owner write+execute, no
// read) can have children unlinked by name but cannot be listed, and a 0500
// directory cannot be written into. Both must be relaxed to 0700 before the
// descent so their children are relaxed and the removal pass can unlink the
// whole tree. The mode assertions hold under any euid; the removal assertion
// is what fails for a non-root owner when the relaxation regresses.
func TestMakeOpenedTreeWritableRelaxesUnlistableDirectory(t *testing.T) {
	base := t.TempDir()
	t.Cleanup(func() { relaxTreeForCleanup(t, base) })
	tree := filepath.Join(base, "tree")
	writeFile(t, filepath.Join(tree, "wx", "nested", "deep.txt"), 8)
	writeFile(t, filepath.Join(tree, "wx", "child.txt"), 8)
	writeFile(t, filepath.Join(tree, "rx", "child.txt"), 8)
	// Chmod children before parents: once a directory drops owner read or
	// write, changing entries below it is only possible by traversal.
	chmod(t, filepath.Join(tree, "wx", "nested", "deep.txt"), 0o444)
	chmod(t, filepath.Join(tree, "wx", "child.txt"), 0o444)
	chmod(t, filepath.Join(tree, "wx", "nested"), 0o555)
	chmod(t, filepath.Join(tree, "wx"), 0o300)
	chmod(t, filepath.Join(tree, "rx", "child.txt"), 0o444)
	chmod(t, filepath.Join(tree, "rx"), 0o500)

	opened, err := os.OpenRoot(tree)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	makeOpenedTreeWritable(opened)
	if err := opened.Close(); err != nil {
		t.Fatalf("close root: %v", err)
	}

	for path, want := range map[string]os.FileMode{
		filepath.Join(tree, "wx"):                       0o700,
		filepath.Join(tree, "wx", "nested"):             0o700,
		filepath.Join(tree, "wx", "nested", "deep.txt"): 0o600,
		filepath.Join(tree, "wx", "child.txt"):          0o600,
		filepath.Join(tree, "rx"):                       0o700,
		filepath.Join(tree, "rx", "child.txt"):          0o600,
	} {
		assertMode(t, path, want)
	}

	// The relaxed tree removes cleanly through the capability.
	if err := removeOpenedTree(tree, nil); err != nil {
		t.Fatalf("removeOpenedTree on an unlistable-directory tree: %v", err)
	}
	if _, err := os.Lstat(tree); !os.IsNotExist(err) {
		t.Fatalf("tree must be gone, err=%v", err)
	}
}
