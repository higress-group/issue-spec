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
// replaced pathname — is what gets removed.
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
