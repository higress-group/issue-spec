package commands

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/higress-group/issue-spec/internal/assignment"
	"github.com/higress-group/issue-spec/internal/rolecompletion"
)

type roleFailure struct {
	OK    bool   `json:"ok"`
	Code  string `json:"code,omitempty"`
	Error string `json:"error"`
}

func (a *app) runRole(ctx context.Context, args []string) int {
	if len(args) == 0 || isHelpArg(args[0]) {
		a.errorf("usage: issue-spec role complete|verify-receipt [options]\n")
		return 2
	}
	switch args[0] {
	case "complete":
		return a.runRoleComplete(ctx, args[1:])
	case "verify-receipt":
		return a.runRoleVerifyReceipt(ctx, args[1:])
	default:
		a.errorf("unknown role command %q\n", args[0])
		return 2
	}
}

func (a *app) runRoleComplete(ctx context.Context, args []string) int {
	fs := newFlagSet("role complete", a.err)
	assignmentFile := fs.String("assignment-file", "", "absolute sealed assignment packet path outside the managed tree")
	decisionFile := fs.String("decision-file", "", "absolute closed role-decision JSON path outside the managed tree")
	output := fs.String("output", "", "absolute final receipt path outside the managed tree")
	agent := fs.String("agent", "", "non-Coordinator logical role name")
	jsonOutput := fs.Bool("json", false, "write compact structured result")
	if ok, code := a.parseFlagSet(fs, args); !ok {
		return code
	}
	if fs.NArg() != 0 {
		a.errorf("unexpected positional arguments: %s\n", strings.Join(fs.Args(), " "))
		return 2
	}
	if !*jsonOutput {
		a.errorf("--json is required for bounded role completion output\n")
		return 2
	}
	result, err := rolecompletion.Complete(ctx, rolecompletion.Request{
		AssignmentFile: *assignmentFile, DecisionFile: *decisionFile, Output: *output, Agent: *agent,
	})
	if err != nil {
		failure := roleFailure{OK: false, Error: err.Error()}
		if errors.Is(err, rolecompletion.ErrDeprecatedWorkflow) {
			failure.Code = deprecatedWorkflowCode
		}
		_ = a.outputJSON(failure)
		return 1
	}
	return a.outputJSON(result)
}

func (a *app) runRoleVerifyReceipt(_ context.Context, args []string) int {
	fs := newFlagSet("role verify-receipt", a.err)
	receiptFile := fs.String("receipt-file", "", "absolute receipt file to inspect without mutation")
	jsonOutput := fs.Bool("json", false, "write structured digest diagnostics")
	if ok, code := a.parseFlagSet(fs, args); !ok {
		return code
	}
	if fs.NArg() != 0 {
		a.errorf("unexpected positional arguments: %s\n", strings.Join(fs.Args(), " "))
		return 2
	}
	if !*jsonOutput {
		a.errorf("--json is required for receipt diagnostics\n")
		return 2
	}
	path := strings.TrimSpace(*receiptFile)
	if path == "" || path == "-" || !filepath.IsAbs(path) {
		_ = a.outputJSON(roleFailure{OK: false, Error: "--receipt-file must be an absolute regular file path and cannot be '-'"})
		return 1
	}
	info, err := os.Lstat(filepath.Clean(path))
	if err != nil {
		_ = a.outputJSON(roleFailure{OK: false, Error: "inspect receipt-file: " + err.Error()})
		return 1
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		_ = a.outputJSON(roleFailure{OK: false, Error: "--receipt-file must name a regular non-symlink file"})
		return 1
	}
	if info.Size() > 1<<20 {
		_ = a.outputJSON(roleFailure{OK: false, Error: "--receipt-file exceeds 1048576 bytes"})
		return 1
	}
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		_ = a.outputJSON(roleFailure{OK: false, Error: "read receipt-file: " + err.Error()})
		return 1
	}
	report := assignment.InspectReceiptJSON(data)
	if code := a.outputJSON(report); code != 0 {
		return code
	}
	if !report.Valid {
		return 1
	}
	return 0
}
