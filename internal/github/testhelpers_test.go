package github

import (
	"context"
)

type recordingCLIRunner struct {
	command ExternalCLICommand
	result  ExternalCLIResult
	err     error
}

func (r *recordingCLIRunner) RunCLI(_ context.Context, cmd ExternalCLICommand) (ExternalCLIResult, error) {
	r.command = cmd
	return r.result, r.err
}
