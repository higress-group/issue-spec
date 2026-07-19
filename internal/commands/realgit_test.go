package commands

import "testing"

// skipWithoutRealGit skips a real-Git contract or smoke test when the fast tier
// (`go test -short`) is selected. These tests spawn real Git processes and form
// the git-contract tier (see the Makefile targets and scripts/test-tier.sh).
// The fast tier exercises command orchestration through the injected
// workspaceService seam only, so it never starts a real Git process.
func skipWithoutRealGit(t testing.TB) {
	t.Helper()
	if testing.Short() {
		t.Skip("real-git contract test; excluded from the fast tier (run without -short)")
	}
}
