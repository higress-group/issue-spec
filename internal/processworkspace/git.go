package processworkspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

type GitCommand struct {
	Binary string
	Dir    string
	Args   []string
	Stdin  []byte
}

type GitResult struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}

type GitRunner interface {
	Run(context.Context, GitCommand) (GitResult, error)
}

type ExecGitRunner struct{}

func (ExecGitRunner) Run(ctx context.Context, command GitCommand) (GitResult, error) {
	binary := strings.TrimSpace(command.Binary)
	if binary == "" {
		binary = "git"
	}
	cmd := exec.CommandContext(ctx, binary, command.Args...)
	cmd.Dir = command.Dir
	if len(command.Stdin) > 0 {
		cmd.Stdin = bytes.NewReader(command.Stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	result := GitResult{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
	} else if err != nil {
		result.ExitCode = -1
	}
	return result, err
}

type GitError struct {
	Operation string
	Args      []string
	Stderr    string
	Err       error
}

func (e *GitError) Error() string {
	detail := strings.TrimSpace(e.Stderr)
	if detail == "" {
		return fmt.Sprintf("%s: %v", e.Operation, e.Err)
	}
	return fmt.Sprintf("%s: %v: %s", e.Operation, e.Err, detail)
}

func (e *GitError) Unwrap() error { return e.Err }

type gitWorktree struct {
	Path     string
	Head     string
	Branch   string
	Detached bool
	Bare     bool
}

func parseWorktreePorcelain(data []byte) ([]gitWorktree, error) {
	fields := bytes.Split(data, []byte{0})
	var result []gitWorktree
	var current *gitWorktree
	flush := func() {
		if current != nil {
			result = append(result, *current)
			current = nil
		}
	}
	for _, raw := range fields {
		line := string(raw)
		if line == "" {
			flush()
			continue
		}
		key, value, _ := strings.Cut(line, " ")
		switch key {
		case "worktree":
			flush()
			if value == "" {
				return nil, errors.New("git worktree record has empty path")
			}
			current = &gitWorktree{Path: filepath.Clean(value)}
		case "HEAD":
			if current == nil {
				return nil, errors.New("git worktree HEAD precedes path")
			}
			current.Head = value
		case "branch":
			if current == nil {
				return nil, errors.New("git worktree branch precedes path")
			}
			current.Branch = value
		case "detached":
			if current == nil {
				return nil, errors.New("git worktree detached marker precedes path")
			}
			current.Detached = true
		case "bare":
			if current == nil {
				return nil, errors.New("git worktree bare marker precedes path")
			}
			current.Bare = true
		}
	}
	flush()
	for _, worktree := range result {
		if worktree.Path == "" || worktree.Head == "" {
			return nil, errors.New("git worktree record is incomplete")
		}
	}
	return result, nil
}

func fullBranchRef(branch string) (string, error) {
	branch = strings.TrimSpace(branch)
	if strings.HasPrefix(branch, "refs/heads/") {
		branch = strings.TrimPrefix(branch, "refs/heads/")
	}
	if !validBranch(branch) {
		return "", fmt.Errorf("invalid branch %q", branch)
	}
	return "refs/heads/" + branch, nil
}
