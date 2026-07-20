package testsupport

import (
	"fmt"
	"os"
	"path/filepath"
)

// RecoveryReport summarizes a stale-recovery pass.
type RecoveryReport struct {
	// Recovered lists the paths of stale roots that were validated and removed.
	Recovered []string
	// Retained lists candidates that were deliberately kept, each with a reason.
	Retained []RetainedCandidate
}

// RetainedCandidate records a candidate that recovery declined to remove.
type RetainedCandidate struct {
	Path   string
	Reason string
}

// runAlive reports whether the run described by a marker could still be running.
// A marker whose boot id differs from the current boot cannot be alive; when the
// boot ids match (or are unknown) it defers to a live-PID probe. Any uncertainty
// resolves to "alive" so callers retain rather than remove the candidate.
func runAlive(m marker) bool {
	parsed, ok := parseRunID(m.RunID)
	if !ok {
		return true
	}
	if current := bootID(); current != "" && parsed.bootID != "" && parsed.bootID != current {
		return false
	}
	return pidAlive(m.PID)
}

// RecoverStale scans the direct children of the configured parent for ownership
// markers and removes each stale root whose marker is valid, whose owning run is
// provably not alive, whose run identity differs from the current run, and whose
// real path is contained within the configured parent. Every other candidate is
// retained with a diagnostic. It never recurses into arbitrary directories.
func (h *Harness) RecoverStale() (RecoveryReport, error) {
	var report RecoveryReport
	entries, err := os.ReadDir(h.parent)
	if err != nil {
		return report, fmt.Errorf("scan configured parent %s: %w", h.parent, err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		candidate := filepath.Join(h.parent, entry.Name())
		if _, err := os.Lstat(markerPath(candidate)); err != nil {
			// Unmarked directory: never touched.
			continue
		}
		m, err := readMarker(candidate)
		if err != nil {
			report.Retained = append(report.Retained, RetainedCandidate{Path: candidate, Reason: err.Error()})
			continue
		}
		if m.RunID == h.runID {
			// Belongs to the current run; managed by CleanupRoot/Finalize.
			continue
		}
		if _, err := containedRealPath(h.parent, candidate); err != nil {
			report.Retained = append(report.Retained, RetainedCandidate{Path: candidate, Reason: err.Error()})
			continue
		}
		if runAlive(m) {
			report.Retained = append(report.Retained, RetainedCandidate{Path: candidate, Reason: errRunActive.Error()})
			continue
		}
		if err := h.removeValidated(candidate, true); err != nil {
			report.Retained = append(report.Retained, RetainedCandidate{Path: candidate, Reason: err.Error()})
			continue
		}
		report.Recovered = append(report.Recovered, candidate)
		h.mu.Lock()
		h.rootsRecovered++
		h.mu.Unlock()
	}
	return report, nil
}
