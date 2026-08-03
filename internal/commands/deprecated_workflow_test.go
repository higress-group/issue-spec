package commands

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestDeprecatedWorkflowCommandsStopAtZeroWriteFrontDoor(t *testing.T) {
	commands := [][]string{
		{"review", "submit"},
		{"review", "sync"},
		{"review", "finding"},
		{"review", "reply"},
		{"verify"},
		{"verify", "submit"},
		{"pr", "rationale"},
		{"code-change", "rationale"},
		{"finalize", "preview"},
		{"finalize", "apply"},
		{"finalize", "detail"},
		{"archive", "durable-spec"},
		{"status", "--gate", "final"},
		{"status", "--gate=FINAL"},
	}
	for _, args := range commands {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			var out, errOut bytes.Buffer
			args = append(append([]string(nil), args...), "--json")
			if code := Execute(args, strings.NewReader("must-not-be-read"), &out, &errOut); code != 1 {
				t.Fatalf("exit code = %d, stdout=%q stderr=%q", code, out.String(), errOut.String())
			}
			var result deprecatedWorkflowResult
			if err := json.Unmarshal(out.Bytes(), &result); err != nil {
				t.Fatalf("decode structured result: %v; output=%q", err, out.String())
			}
			if result.OK || result.Code != deprecatedWorkflowCode || result.Command == "" || result.Message == "" {
				t.Fatalf("deprecated result = %+v", result)
			}
		})
	}
}

func TestDeprecatedWorkflowSelectionDoesNotCaptureUnrelatedOrFullyRemovedSurfaces(t *testing.T) {
	for _, args := range [][]string{
		{"code-change", "merge"},
		{"merge-check"}, {"durable-spec", "check"}, {"status", "--gate", "implement"},
	} {
		if result, deprecated := deprecatedWorkflowSelection(args); deprecated {
			t.Fatalf("unrelated or fully removed command %v selected as %+v", args, result)
		}
	}
}
