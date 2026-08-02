package gates

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestMergeAuthorityCannotReachLegacyWorkflowPackages(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}", "./internal/mergecheck", "./internal/mergeauthority")
	command.Dir = root
	output, err := command.Output()
	if err != nil {
		t.Fatalf("list merge-authority dependencies: %v", err)
	}
	dependencies := map[string]bool{}
	for _, dependency := range strings.Fields(string(output)) {
		dependencies[dependency] = true
	}
	for _, forbidden := range []string{
		"github.com/higress-group/issue-spec/internal/assignment",
		"github.com/higress-group/issue-spec/internal/evidence",
		"github.com/higress-group/issue-spec/internal/finalization",
		"github.com/higress-group/issue-spec/internal/gates",
		"github.com/higress-group/issue-spec/internal/processworkspace",
		"github.com/higress-group/issue-spec/internal/relationships",
		"github.com/higress-group/issue-spec/internal/rolecompletion",
	} {
		if dependencies[forbidden] {
			t.Fatalf("merge authority reaches retired workflow package %s", forbidden)
		}
	}
}
