package commands

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"testing"

	"github.com/higress-group/issue-spec/internal/github"
)

func TestE2ESmokeRealCLIPaths(t *testing.T) {
	if os.Getenv("ISSUE_SPEC_E2E") == "" {
		t.Skip("set ISSUE_SPEC_E2E=1 to run gated smoke tests")
	}
	for _, binary := range []string{"gh", "acpx", "codex", "claude"} {
		if _, err := exec.LookPath(binary); err != nil {
			t.Skipf("%s not available: %v", binary, err)
		}
	}

	ctx := context.Background()
	gh, err := github.NewGHCLI(github.GHCLIOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := gh.Authenticated(ctx, "github.com"); err != nil {
		t.Fatalf("gh auth status failed: %v", err)
	}
	if _, err := gh.Token(ctx, "github.com"); err != nil {
		t.Fatalf("gh auth token failed: %v", err)
	}
	for _, cmd := range []string{"acpx", "codex", "claude"} {
		c := exec.CommandContext(ctx, cmd, "--help")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("%s --help failed: %v\n%s", cmd, err, string(out))
		}
	}

	var out, errOut bytes.Buffer
	if code := Execute([]string{"runner", "poll", "--repo", "o/r", "--once", "--dry-run", "--unsafe-no-sandbox", "--bwrap-path", "", "--acpx-path", "", "--json"}, nil, &out, &errOut); code != 0 {
		t.Fatalf("runner smoke command failed: %d", code)
	}
}
