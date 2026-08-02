package commands

import "context"

// runFinalize is intentionally only the zero-write tombstone for the removed
// history-finalization state machine. Historical plan types remain readable in
// internal/finalization for audit; no command can compile, apply, resume, or
// reinterpret them as merge authority.
func (a *app) runFinalize(_ context.Context, args []string) int {
	commandArgs := append([]string{"finalize"}, args...)
	if result, ok := deprecatedWorkflowSelection(commandArgs); ok {
		return a.outputDeprecatedWorkflow(result, commandArgs)
	}
	a.errorf("usage: issue-spec finalize preview|apply|detail ...\n")
	return 2
}
