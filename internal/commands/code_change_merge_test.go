package commands

import (
	"errors"
	"strings"
	"testing"

	"github.com/higress-group/issue-spec/internal/codereview"
)

func TestCodeChangeMergeUsesProtectedMutationThenExactReconciliation(t *testing.T) {
	withMergeWorkflow(t)
	backend := &mergeCommandBackend{issueState: "open"}
	provider := newMergeCommandProvider(codereview.CheckSuccess, false)
	app, out, errOut := mergeCommandApp(t, backend, provider)
	code := app.runCodeChangeMerge(t.Context(), []string{"--repo", "o/r", "--issue", "1", "--pr", "7", "--expected-head", "head:2", "--json"})
	if code != 0 || provider.merges != 1 || provider.snapshots != 2 || backend.issueWrites != 1 || !strings.Contains(out.String(), `"merge_id": "merge:7"`) {
		t.Fatalf("exit=%d provider=%d/%d issue_writes=%d stdout=%q stderr=%q", code, provider.merges, provider.snapshots, backend.issueWrites, out.String(), errOut.String())
	}
}

func TestCodeChangeMergeDriftRejectsBeforeIssueReconciliation(t *testing.T) {
	withMergeWorkflow(t)
	backend := &mergeCommandBackend{issueState: "open"}
	provider := newMergeCommandProvider(codereview.CheckSuccess, false)
	provider.mergeErr = errors.New("same-head policy generation moved")
	app, out, _ := mergeCommandApp(t, backend, provider)
	code := app.runCodeChangeMerge(t.Context(), []string{"--repo", "o/r", "--issue", "1", "--pr", "7", "--expected-head", "head:2", "--json"})
	if code != 1 || provider.merges != 1 || backend.issueWrites != 0 || !strings.Contains(out.String(), "conditional_merge_rejected") {
		t.Fatalf("exit=%d merges=%d issue_writes=%d stdout=%q", code, provider.merges, backend.issueWrites, out.String())
	}
}
