package testsupport

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// markerName is the fixed file name written into every owned run-scoped root.
const markerName = ".issue-spec-test-root.json"

// markerSchemaVersion is the current on-disk schema version for the marker file.
// Markers carrying a different version are treated as foreign and never removed.
const markerSchemaVersion = 1

// parentEnvVar is the environment variable used to configure the parent
// directory that owns all run-scoped test roots.
const parentEnvVar = "ISSUE_SPEC_TEST_TMP"

// marker is the on-disk ownership record stored at the root of every acquired
// run-scoped directory.
type marker struct {
	SchemaVersion int    `json:"schema_version"`
	RunID         string `json:"run_id"`
	PID           int    `json:"pid"`
	CreatedAt     string `json:"created_at"`
	Parent        string `json:"parent"`
}

var (
	// errMarkerMissing indicates no marker file was found at a candidate path.
	errMarkerMissing = errors.New("testsupport: ownership marker missing")
	// errMarkerMalformed indicates the marker file could not be parsed or failed
	// canonical validation.
	errMarkerMalformed = errors.New("testsupport: ownership marker malformed")
	// errOutOfParent indicates a candidate path is not contained within the
	// configured parent (including via symlink escape).
	errOutOfParent = errors.New("testsupport: candidate path escapes configured parent")
	// errRunActive indicates a candidate's owning run is still alive.
	errRunActive = errors.New("testsupport: candidate run is still alive")
)

// resolveParent returns the configured parent directory, canonicalized via
// EvalSymlinks. It uses ISSUE_SPEC_TEST_TMP when it names an existing absolute
// directory; otherwise it falls back to the process temp directory. Recovery and
// cleanup only ever operate under this single directory.
func resolveParent(lookup func(string) (string, bool)) (string, error) {
	if lookup == nil {
		lookup = os.LookupEnv
	}
	if raw, ok := lookup(parentEnvVar); ok {
		trimmed := strings.TrimSpace(raw)
		if trimmed != "" && filepath.IsAbs(trimmed) {
			if info, err := os.Stat(trimmed); err == nil && info.IsDir() {
				return filepath.EvalSymlinks(trimmed)
			}
		}
	}
	return filepath.EvalSymlinks(os.TempDir())
}

// markerPath returns the marker file path for a given root directory.
func markerPath(root string) string {
	return filepath.Join(root, markerName)
}

// writeMarker writes m atomically enough for tests into root.
func writeMarker(root string, m marker) error {
	data, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("marshal marker: %w", err)
	}
	return os.WriteFile(markerPath(root), data, 0o600)
}

// readMarker reads and validates the marker for root. It fails closed: any
// missing, unreadable, unparseable, or non-canonical marker returns an error so
// callers never remove an unverified path.
func readMarker(root string) (marker, error) {
	data, err := os.ReadFile(markerPath(root))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return marker{}, errMarkerMissing
		}
		return marker{}, fmt.Errorf("%w: %v", errMarkerMalformed, err)
	}
	var m marker
	if err := json.Unmarshal(data, &m); err != nil {
		return marker{}, fmt.Errorf("%w: %v", errMarkerMalformed, err)
	}
	if err := validateMarker(m); err != nil {
		return marker{}, err
	}
	return m, nil
}

// validateMarker enforces the canonical shape of a marker.
func validateMarker(m marker) error {
	if m.SchemaVersion != markerSchemaVersion {
		return fmt.Errorf("%w: schema_version %d", errMarkerMalformed, m.SchemaVersion)
	}
	if strings.TrimSpace(m.RunID) == "" {
		return fmt.Errorf("%w: empty run_id", errMarkerMalformed)
	}
	if m.PID <= 0 {
		return fmt.Errorf("%w: non-positive pid", errMarkerMalformed)
	}
	if strings.TrimSpace(m.Parent) == "" {
		return fmt.Errorf("%w: empty parent", errMarkerMalformed)
	}
	if _, ok := parseRunID(m.RunID); !ok {
		return fmt.Errorf("%w: unparseable run_id", errMarkerMalformed)
	}
	return nil
}

// containedRealPath resolves path via EvalSymlinks and verifies it is strictly
// contained within parent (which is assumed already canonical). It returns the
// resolved real path on success. A path equal to the parent, escaping it, or
// unresolvable fails closed.
func containedRealPath(parent, path string) (string, error) {
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("%w: %v", errOutOfParent, err)
	}
	rel, err := filepath.Rel(parent, real)
	if err != nil {
		return "", fmt.Errorf("%w: %v", errOutOfParent, err)
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("%w: %s", errOutOfParent, real)
	}
	return real, nil
}
