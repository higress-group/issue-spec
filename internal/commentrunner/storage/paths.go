package storage

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/higress-group/issue-spec/internal/workspace"
)

// hashNameLength is the hex-encoded 16-byte physical hash used for runtime and
// pool directory names.
const hashNameLength = 32

// protectedRootEntries are root children the storage engine must never
// inventory or delete.
var protectedRootEntries = map[string]bool{
	StorageDirName: true,
	".locks":       true,
}

// ValidHashName reports whether name is a complete 16-byte lowercase hex hash.
// Partial matches are never accepted.
func ValidHashName(name string) bool {
	if len(name) != hashNameLength {
		return false
	}
	for _, r := range name {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

// ValidateInventoryEntry validates one `.sessions`/`.process-workspaces` child
// without following symlinks: exact hash name, confined to the canonical root,
// existing non-symlink directory. Anything else is rejected, never deleted.
func ValidateInventoryEntry(workspaceRoot, dirName, name string) (string, error) {
	canonicalRoot, err := Canonicalize(workspaceRoot)
	if err != nil {
		return "", err
	}
	return validateInventoryEntryFast(canonicalRoot, dirName, name)
}

// validateInventoryEntryFast is ValidateInventoryEntry with the root already
// canonicalized once per pass instead of per entry.
func validateInventoryEntryFast(canonicalRoot, dirName, name string) (string, error) {
	if dirName != SessionsDirName && dirName != ProcessPoolsDirName {
		return "", fmt.Errorf("unsupported inventory directory %q", dirName)
	}
	if protectedRootEntries[name] {
		return "", fmt.Errorf("protected entry %q is never inventoried", name)
	}
	if !ValidHashName(name) {
		return "", fmt.Errorf("entry %q is not a complete physical hash", name)
	}
	path := filepath.Join(canonicalRoot, dirName, name)
	confined, err := workspace.ValidatePathUnderRoot(canonicalRoot, path)
	if err != nil {
		return "", err
	}
	if confined != path {
		return "", fmt.Errorf("entry %q does not resolve to its literal path", name)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("entry %q is a symlink", name)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("entry %q is not a directory", name)
	}
	return path, nil
}

// ValidateDeletionTarget performs the final identity check immediately before
// a destructive remove: the target must still be the exact
// `.sessions/<hash>`/`.process-workspaces/<hash>` directory the engine
// classified, never the root, a protected directory, a symlink, or a file.
func ValidateDeletionTarget(workspaceRoot, target, expectedHash string) error {
	if strings.TrimSpace(target) == "" {
		return errors.New("deletion target is required")
	}
	if !ValidHashName(expectedHash) {
		return fmt.Errorf("deletion target hash %q is invalid", expectedHash)
	}
	clean := filepath.Clean(target)
	canonicalRoot, err := Canonicalize(workspaceRoot)
	if err != nil {
		return err
	}
	if clean == canonicalRoot {
		return fmt.Errorf("deletion target %q is the workspace root", target)
	}
	parent, base := filepath.Dir(clean), filepath.Base(clean)
	if base != expectedHash {
		return fmt.Errorf("deletion target %q does not match classified hash %q", target, expectedHash)
	}
	if parent != filepath.Join(canonicalRoot, SessionsDirName) && parent != filepath.Join(canonicalRoot, ProcessPoolsDirName) {
		return fmt.Errorf("deletion target %q is outside managed storage directories", target)
	}
	confined, err := workspace.ValidatePathUnderRoot(canonicalRoot, clean)
	if err != nil {
		return err
	}
	if confined != clean {
		return fmt.Errorf("deletion target %q escapes its literal path", target)
	}
	info, err := os.Lstat(clean)
	if errors.Is(err, os.ErrNotExist) {
		// Missing targets complete idempotently.
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("deletion target %q is a symlink", target)
	}
	if !info.IsDir() {
		return fmt.Errorf("deletion target %q is not a directory", target)
	}
	return nil
}
