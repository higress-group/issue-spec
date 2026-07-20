// Package testsupport provides a test-only foundation for owned, run-scoped
// temporary resources and process-lifecycle management used by issue-spec
// integration tests.
//
// The package is intentionally not imported by any production code. It exists so
// that integration tests can:
//
//   - Acquire one verifiable run-scoped root per run, marked with a versioned
//     ownership marker (.issue-spec-test-root.json) that records the run
//     identity (pid, boot id, random), pid, creation time, and configured
//     parent.
//   - Spawn child processes attached to the run's own process group so the whole
//     process tree can be terminated and reaped on normal exit, timeout, and
//     cancellation.
//   - Clean up only validated owned roots, failing closed on missing or
//     malformed markers, active runs, out-of-parent paths, and symlink escape.
//   - Recover stale marked roots left behind by an uncatchable termination on
//     the next run, but only when the owning run is provably dead and the root
//     is contained within the configured parent.
//   - Emit a single machine-parseable TEST-TIER-SUMMARY line and assert that a
//     run leaves zero live current-run children and zero remaining current-run
//     roots.
//
// # Configured parent
//
// The configured parent is ISSUE_SPEC_TEST_TMP when it names an existing,
// absolute directory; otherwise it is the process temp directory
// (os.TempDir()). Recovery only ever scans the direct children of this single
// configured parent for markers. It never walks arbitrary OS temp directories.
//
// # Cross-platform behavior
//
// On Unix-like systems children are placed in a dedicated process group
// (SysProcAttr{Setpgid: true}) and the whole group is signalled on termination.
// On Windows there is no process-group equivalent here, so a documented fallback
// terminates the direct child process and process liveness is treated
// conservatively (uncertain runs are retained rather than recovered).
package testsupport
