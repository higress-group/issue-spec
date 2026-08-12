package storage

import (
	"fmt"
	"os"
	"path/filepath"
)

// RemoveManagedTree removes the classified directory through an opened root
// capability. All recursive operations stay bound to the object opened before
// the optional hook; a pathname replacement is detected before removing the
// final directory entry and is never recursively traversed.
func RemoveManagedTree(workspaceRoot, target, expectedHash string, beforeRemove func()) error {
	if err := ValidateDeletionTarget(workspaceRoot, target, expectedHash); err != nil {
		return err
	}
	return removeOpenedTree(target, beforeRemove)
}

// removeOpenedTree removes an already validated target directory through an
// opened root capability. Callers must prove the target's identity and
// confinement first; this helper only guarantees the opened object — not a
// replaced pathname — is what gets removed. Before any entry is unlinked the
// validated tree is relaxed owner-writable through the same capability:
// tool-managed caches materialize read-only entries (the Go module cache
// writes directories 0555 and files 0444) and unlink requires a writable
// parent, so a non-root service user cannot remove such trees otherwise.
func removeOpenedTree(target string, beforeRemove func()) error {
	info, err := os.Lstat(target)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	targetRoot, err := os.OpenRoot(target)
	if err != nil {
		return fmt.Errorf("open deletion target capability: %w", err)
	}
	defer targetRoot.Close()
	openedInfo, err := targetRoot.Stat(".")
	if err != nil {
		return fmt.Errorf("stat deletion target capability: %w", err)
	}
	if !os.SameFile(info, openedInfo) {
		return fmt.Errorf("deletion target changed while opening capability")
	}
	if beforeRemove != nil {
		beforeRemove()
	}
	makeOpenedTreeWritable(targetRoot)
	entries, err := targetRoot.Open(".")
	if err != nil {
		return fmt.Errorf("open deletion target contents: %w", err)
	}
	names, readErr := entries.Readdirnames(-1)
	closeErr := entries.Close()
	if readErr != nil {
		return fmt.Errorf("read deletion target contents: %w", readErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close deletion target contents: %w", closeErr)
	}
	for _, name := range names {
		if err := targetRoot.RemoveAll(name); err != nil {
			return fmt.Errorf("remove deletion target child %q: %w", name, err)
		}
	}

	// The opened Root follows the original directory across renames. Only remove
	// the named directory entry if it still identifies that same object.
	current, err := os.Lstat(target)
	if os.IsNotExist(err) {
		return fmt.Errorf("deletion target moved during removal")
	}
	if err != nil {
		return fmt.Errorf("re-stat deletion target: %w", err)
	}
	if !os.SameFile(openedInfo, current) {
		return fmt.Errorf("deletion target identity changed before final removal")
	}
	parentRoot, err := os.OpenRoot(filepath.Dir(target))
	if err != nil {
		return fmt.Errorf("open managed parent capability: %w", err)
	}
	defer parentRoot.Close()
	if err := parentRoot.Remove(filepath.Base(target)); err != nil {
		return fmt.Errorf("remove verified deletion target: %w", err)
	}
	return nil
}

// makeOpenedTreeWritable relaxes every entry below an opened root so the
// removal pass can unlink it as a non-root owner: directories gain owner rwx
// (0700) whenever any owner bit is missing — write+execute unlink children,
// read lists the directory, so a 0300 directory is writable yet undescendable
// — and files gain owner rw (0600), but only where owner write is missing.
// Every access goes through the root capability, so the walk stays bound to
// the proven object even if the target pathname is replaced mid-pass.
// Symlinks are never followed or chmodded; the removal pass unlinks them
// through their relaxed parent directory. The pass is best-effort: an entry
// that resists relaxation (for example one owned by another user) is left
// for the removal pass, whose error remains authoritative.
func makeOpenedTreeWritable(root *os.Root) {
	pending := []string{"."}
	for len(pending) > 0 {
		dir := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		info, err := root.Lstat(dir)
		if err != nil || !info.IsDir() {
			continue
		}
		if info.Mode().Perm()&0o700 != 0o700 {
			// Owner write+execute are needed to unlink children and owner read
			// to list the directory, so relax on any missing owner bit before
			// descending.
			_ = root.Chmod(dir, 0o700)
		}
		entries, err := readOpenedRootDir(root, dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			pending = relaxOpenedTreeEntry(root, dir, entry, pending)
		}
	}
}

// relaxOpenedTreeEntry classifies one directory entry below the opened root
// and returns the updated descent queue: subdirectories are queued so their
// own pop relaxes them before they are listed, plain files missing owner
// write are relaxed in place, and symlinks are never followed or chmodded.
// An entry whose dirent carries no type (DT_UNKNOWN on some FUSE/NFS/CIFS
// mounts) lstat's through the same root capability instead of trusting the
// file fallback: a subdirectory misclassified as a file would be chmodded
// 0600 and strand its children behind an undescendable directory.
func relaxOpenedTreeEntry(root *os.Root, dir string, entry os.DirEntry, pending []string) []string {
	child := filepath.Join(dir, entry.Name())
	entryType := entry.Type()
	if entryType&os.ModeSymlink != 0 {
		// Never followed: removal unlinks the entry itself.
		return pending
	}
	if entryType.IsDir() {
		return append(pending, child)
	}
	if entryType != 0 {
		// Known non-directory type: relax the entry when owner write is missing.
		entryInfo, err := entry.Info()
		if err != nil {
			return pending
		}
		relaxOpenedFile(root, child, entryInfo.Mode())
		return pending
	}
	// The dirent type is unknown: classify through the capability.
	entryInfo, err := root.Lstat(child)
	if err != nil {
		return pending
	}
	mode := entryInfo.Mode()
	if mode&os.ModeSymlink != 0 {
		// A symlink behind an unknown dirent type is left alone.
		return pending
	}
	if mode.IsDir() {
		return append(pending, child)
	}
	relaxOpenedFile(root, child, mode)
	return pending
}

// relaxOpenedFile relaxes one non-directory entry below the opened root when
// owner write is missing, so the removal pass can unlink it.
func relaxOpenedFile(root *os.Root, path string, mode os.FileMode) {
	if mode.Perm()&0o200 == 0 {
		_ = root.Chmod(path, 0o600)
	}
}

// readOpenedRootDir lists one directory below an opened root.
func readOpenedRootDir(root *os.Root, dir string) ([]os.DirEntry, error) {
	opened, err := root.Open(dir)
	if err != nil {
		return nil, err
	}
	entries, readErr := opened.ReadDir(-1)
	closeErr := opened.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return entries, nil
}
