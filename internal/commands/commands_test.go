package commands

import (
	"bytes"
	"strings"
	"testing"
)

func TestTopLevelHelpRoutesDurableSpecAndOmitsRetiredWorkflow(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := Execute([]string{"durable-spec", "check", "--help"}, strings.NewReader(""), &out, &errOut); code != 0 ||
		!strings.Contains(out.String(), "issue-spec durable-spec check") {
		t.Fatalf("durable route code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	out.Reset()
	errOut.Reset()
	if code := Execute([]string{"--help"}, strings.NewReader(""), &out, &errOut); code != 0 {
		t.Fatalf("help code=%d stderr=%q", code, errOut.String())
	}
	for _, removed := range []string{"review sync", "verify submit", "pr rationale", "verify-closure", "archive durable-spec", "finalize"} {
		if strings.Contains(out.String(), removed) {
			t.Fatalf("normal help still exposes %q:\n%s", removed, out.String())
		}
	}
}

func TestRemovedCloseChangeIsFixedZeroWriteDeprecation(t *testing.T) {
	var out, errOut bytes.Buffer
	code := Execute([]string{"issue", "close-change", "--help", "--json"}, strings.NewReader(""), &out, &errOut)
	if code == 0 || !strings.Contains(out.String(), deprecatedWorkflowCode) {
		t.Fatalf("close-change code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
}
