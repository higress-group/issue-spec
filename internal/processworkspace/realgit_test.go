package processworkspace

import (
	"flag"
	"os"
	"testing"

	"github.com/higress-group/issue-spec/internal/testsupport"
)

// skipWithoutRealGit marks a test as a real-Git contract test. These tests spawn
// a real git process to exercise the process-workspace lifecycle end to end and
// are deliberately excluded from the fast tier (go test -short). Pure tests
// (store, types, ownership) never call the git helpers and therefore stay in the
// fast tier.
func skipWithoutRealGit(t testing.TB) {
	t.Helper()
	if testing.Short() {
		t.Skip("real-git contract test; excluded from the fast tier (run without -short)")
	}
}

func TestMain(m *testing.M) {
	// flag.Parse populates testing.Short() so the tier reflects the -short flag.
	flag.Parse()
	tier := "full"
	if testing.Short() {
		tier = "fast"
	}
	os.Exit(testsupport.RunMain(m, tier))
}
