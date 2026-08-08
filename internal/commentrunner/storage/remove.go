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
// (0700) and files owner rw (0600), but only where owner write is missing.
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
		if info.Mode().Perm()&0o300 != 0o300 {
			// Owner write+execute are needed to unlink children and to list
			// the directory, so relax it before descending.
			_ = root.Chmod(dir, 0o700)
		}
		entries, err := readOpenedRootDir(root, dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			switch {
			case entry.Type()&os.ModeSymlink != 0:
				// Never followed: removal unlinks the entry itself.
			case entry.IsDir():
				pending = append(pending, filepath.Join(dir, entry.Name()))
			default:
				entryInfo, err := entry.Info()
				if err != nil {
					continue
				}
				if entryInfo.Mode().Perm()&0o200 == 0 {
					_ = root.Chmod(filepath.Join(dir, entry.Name()), 0o600)
				}
			}
		}
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
