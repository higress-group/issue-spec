package processworkspace

import (
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
)

var ErrOwnershipViolation = errors.New("worker commit exceeds declared write ownership")

// ValidateManagedOwnership is the reservation-boundary validator shared by
// runner allocation and integration. Callers must use this before publishing a
// managed reservation; Complete and Integrate repeat it defensively so legacy
// registry entries created with the older permissive grammar fail closed.
func ValidateManagedOwnership(writeOwnership, sharedTouchpoints []string) error {
	if _, err := NormalizeManagedOwnership(writeOwnership); err != nil {
		return fmt.Errorf("write ownership: %w", err)
	}
	if _, err := NormalizeManagedOwnership(sharedTouchpoints); err != nil {
		return fmt.Errorf("shared touchpoints: %w", err)
	}
	return nil
}

// NormalizeManagedOwnership is the strict grammar used by managed workspaces.
// Entries are repository-relative exact files or directory prefixes ending in
// /**. Dot segments, platform-specific absolute paths, backslashes, and every
// other glob form are rejected before Git is invoked.
func NormalizeManagedOwnership(entries []string) ([]string, error) {
	set := map[string]struct{}{}
	for _, raw := range entries {
		entry := strings.TrimSpace(raw)
		if entry == "" || strings.HasPrefix(entry, "/") || strings.HasPrefix(entry, "~") ||
			strings.Contains(entry, "\\") || driveQualifiedPath(entry) {
			return nil, fmt.Errorf("invalid repository-relative ownership %q", raw)
		}
		prefix := strings.HasSuffix(entry, "/**")
		base := strings.TrimSuffix(entry, "/**")
		if base == "" || strings.ContainsAny(base, "*?[]") || strings.ContainsRune(base, 0) {
			return nil, fmt.Errorf("unsupported ownership glob %q", raw)
		}
		for _, component := range strings.Split(base, "/") {
			if component == "" || component == "." || component == ".." {
				return nil, fmt.Errorf("unsafe ownership path %q", raw)
			}
		}
		if clean := path.Clean(base); clean != base || clean == "." || strings.HasPrefix(clean, "../") {
			return nil, fmt.Errorf("unsafe ownership path %q", raw)
		}
		if prefix {
			base += "/**"
		}
		set[base] = struct{}{}
	}
	result := make([]string, 0, len(set))
	for entry := range set {
		result = append(result, entry)
	}
	sort.Strings(result)
	return result, nil
}

func ManagedOwnershipOverlaps(left, right []string) (bool, error) {
	a, err := NormalizeManagedOwnership(left)
	if err != nil {
		return false, err
	}
	b, err := NormalizeManagedOwnership(right)
	if err != nil {
		return false, err
	}
	for _, one := range a {
		for _, two := range b {
			if ownershipEntriesOverlap(one, two) {
				return true, nil
			}
		}
	}
	return false, nil
}

// ValidateManagedWriteScope validates paths against write ownership only.
// sharedTouchpoints is deliberately accepted for diagnostics but never grants
// permission to include a path in the worker commit.
func ValidateManagedWriteScope(writeOwnership, sharedTouchpoints, changedPaths []string) error {
	if err := ValidateManagedOwnership(writeOwnership, sharedTouchpoints); err != nil {
		return err
	}
	rules, err := NormalizeManagedOwnership(writeOwnership)
	if err != nil {
		return err
	}
	var unexpected []string
	for _, changed := range changedPaths {
		pathValue, err := normalizeChangedPath(changed)
		if err != nil {
			return err
		}
		owned := false
		for _, rule := range rules {
			if ownershipEntryContains(rule, pathValue) {
				owned = true
				break
			}
		}
		if !owned {
			unexpected = append(unexpected, pathValue)
		}
	}
	if len(unexpected) > 0 {
		sort.Strings(unexpected)
		return fmt.Errorf("%w: %s", ErrOwnershipViolation, strings.Join(unexpected, ", "))
	}
	return nil
}

func normalizeChangedPath(value string) (string, error) {
	if value == "" || value != strings.TrimSpace(value) || strings.HasPrefix(value, "/") || strings.Contains(value, "\\") || driveQualifiedPath(value) {
		return "", fmt.Errorf("invalid changed repository path %q", value)
	}
	for _, component := range strings.Split(value, "/") {
		if component == "" || component == "." || component == ".." {
			return "", fmt.Errorf("invalid changed repository path %q", value)
		}
	}
	if path.Clean(value) != value {
		return "", fmt.Errorf("invalid changed repository path %q", value)
	}
	return value, nil
}

func driveQualifiedPath(value string) bool {
	return len(value) >= 2 && value[1] == ':' && ((value[0] >= 'a' && value[0] <= 'z') || (value[0] >= 'A' && value[0] <= 'Z'))
}
