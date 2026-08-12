package storage

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// RuntimeHomeUsage splits one runtime home's bytes into the diagnostic
// classes: protected identity/config, rebuildable cache, and unknown.
type RuntimeHomeUsage struct {
	ProtectedBytes int64 `json:"protected_bytes"`
	CacheBytes     int64 `json:"cache_bytes"`
	UnknownBytes   int64 `json:"unknown_bytes"`
}

// Total sums all classes.
func (u RuntimeHomeUsage) Total() int64 { return u.ProtectedBytes + u.CacheBytes + u.UnknownBytes }

// RuntimeUsage is the per-root storage accounting view for the shared home
// resources: one home's classification plus total job scratch bytes.
type RuntimeUsage struct {
	Home         RuntimeHomeUsage `json:"home"`
	ScratchBytes int64            `json:"scratch_bytes"`
}

type runtimeUsageClass int

const (
	runtimeUsageUnknown runtimeUsageClass = iota
	runtimeUsageProtected
	runtimeUsageCache
)

// protectedHomeEntries below home/ hold agent identity and configuration:
// losing them breaks /resume or re-leaks credentials, so they are never
// evicted. Everything else under home/ that is not listed as cache is
// unknown and equally protected from eviction.
var protectedHomeEntries = map[string]bool{
	".acpx":      true,
	".qoder":     true,
	".claude":    true,
	".codex":     true,
	".config":    true,
	".ssh":       true,
	".gitconfig": true,
}

// cacheHomeEntries below home/ are rebuildable downloads.
var cacheHomeEntries = map[string]bool{
	".cache": true,
	".npm":   true,
	"go":     true,
}

// RuntimeCacheDirs lists the eviction-eligible cache subtrees of one runtime
// home in eviction priority order: the most rebuildable (_npx) first.
func RuntimeCacheDirs(home string) []string {
	return []string{
		filepath.Join(home, ".npm", "_npx"),
		filepath.Join(home, ".npm"),
		filepath.Join(home, ".cache"),
		filepath.Join(home, "go", "pkg", "mod"),
	}
}

// classifyRuntimeHomeRel classifies one regular file by its path relative to
// the scope root. The whole go/ tree counts as cache so GOPATH build caches
// and binaries never inflate protected bytes.
func classifyRuntimeHomeRel(rel string) runtimeUsageClass {
	rel = filepath.ToSlash(rel)
	if rel == runtimeScopeFileName {
		return runtimeUsageProtected
	}
	first, rest, _ := strings.Cut(rel, "/")
	switch first {
	case "gh", "xdg", "codex", "acpx-runtime":
		return runtimeUsageProtected
	case "home":
		entry, _, _ := strings.Cut(rest, "/")
		switch {
		case entry == ".claude.json":
			return runtimeUsageProtected
		case protectedHomeEntries[entry]:
			return runtimeUsageProtected
		case cacheHomeEntries[entry]:
			return runtimeUsageCache
		default:
			return runtimeUsageUnknown
		}
	default:
		return runtimeUsageUnknown
	}
}

// MeasureRuntimeHome walks one scope root without following symlinks and
// classifies regular-file bytes, the same rules as measureTreeBytes.
func MeasureRuntimeHome(root string) (RuntimeHomeUsage, error) {
	var usage RuntimeHomeUsage
	walkErr := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return infoErr
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		switch classifyRuntimeHomeRel(rel) {
		case runtimeUsageProtected:
			usage.ProtectedBytes += info.Size()
		case runtimeUsageCache:
			usage.CacheBytes += info.Size()
		default:
			usage.UnknownBytes += info.Size()
		}
		return nil
	})
	if walkErr != nil {
		return RuntimeHomeUsage{}, fmt.Errorf("measure runtime home %q: %w", root, walkErr)
	}
	return usage, nil
}

// MeasureJobScratch sums regular-file bytes below `.job-scratch`, skipping
// symlinks; a missing scratch base measures as zero.
func MeasureJobScratch(workspaceRoot string) (int64, error) {
	canonical, err := Canonicalize(workspaceRoot)
	if err != nil {
		return 0, err
	}
	base := filepath.Join(canonical, JobScratchDirName)
	if _, err := os.Lstat(base); errors.Is(err, os.ErrNotExist) {
		return 0, nil
	} else if err != nil {
		return 0, fmt.Errorf("inspect job scratch base: %w", err)
	}
	return measureTreeBytes(base), nil
}
