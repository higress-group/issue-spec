package rolecompletion

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"runtime"
)

type ExecCommandRunner struct{}

func (ExecCommandRunner) Run(ctx context.Context, command Command) (CommandResult, error) {
	cmd := exec.CommandContext(ctx, command.Name, command.Args...)
	cmd.Dir = command.Directory
	if len(command.Stdin) > 0 {
		cmd.Stdin = bytes.NewReader(command.Stdin)
	}
	stdout := &boundedBuffer{limit: maxDiagnosticBytes}
	stderr := &boundedBuffer{limit: maxDiagnosticBytes}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err := cmd.Run()
	result := CommandResult{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
	} else if err != nil {
		result.ExitCode = -1
	}
	return result, err
}

type ShellTestRunner struct{ Commands CommandRunner }

func (r ShellTestRunner) Run(ctx context.Context, directory, command string) (CommandResult, error) {
	if runtime.GOOS == "windows" {
		return r.Commands.Run(ctx, Command{Directory: directory, Name: "cmd.exe", Args: []string{"/D", "/S", "/C", command}})
	}
	return r.Commands.Run(ctx, Command{Directory: directory, Name: "/bin/sh", Args: []string{"-c", command}})
}

type boundedBuffer struct {
	data  []byte
	limit int
}

func (b *boundedBuffer) Write(value []byte) (int, error) {
	original := len(value)
	remaining := b.limit - len(b.data)
	if remaining > 0 {
		if len(value) > remaining {
			value = value[:remaining]
		}
		b.data = append(b.data, value...)
	}
	return original, nil
}

func (b *boundedBuffer) Bytes() []byte { return append([]byte(nil), b.data...) }
