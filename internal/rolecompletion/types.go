package rolecompletion

import (
	"context"

	"github.com/higress-group/issue-spec/internal/assignment"
)

const maxDiagnosticBytes = 8 << 10

// Request is the complete caller-selected surface. All mechanical receipt
// facts are deliberately absent and are derived by Service.Complete.
type Request struct {
	AssignmentFile   string
	DecisionFile     string
	Output           string
	Agent            string
	WorkingDirectory string
}

type TestSummary struct {
	ID      string                 `json:"id"`
	Command string                 `json:"command"`
	Outcome assignment.TestOutcome `json:"outcome"`
}

// Result is the bounded identity returned after the final receipt has been
// atomically published and strictly re-read.
type Result struct {
	ReceiptPath   string          `json:"receipt_path"`
	ReceiptID     string          `json:"receipt_id"`
	ReceiptDigest string          `json:"receipt_digest"`
	Role          assignment.Role `json:"role"`
	Revision      string          `json:"revision"`
	Tests         []TestSummary   `json:"tests,omitempty"`
}

type Command struct {
	Directory string
	Name      string
	Args      []string
	Stdin     []byte
}

type CommandResult struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}

// CommandRunner is injectable so Git lifecycle and command failures can be
// exercised without weakening production observation.
type CommandRunner interface {
	Run(context.Context, Command) (CommandResult, error)
}

// TestRunner executes the exact sealed selector string in the role tree.
type TestRunner interface {
	Run(context.Context, string, string) (CommandResult, error)
}

type Service struct {
	Commands CommandRunner
	Tests    TestRunner
	Getwd    func() (string, error)
	Publish  func(string, string, assignment.Receipt) error
}

func New() *Service {
	runner := ExecCommandRunner{}
	return &Service{Commands: runner, Tests: ShellTestRunner{Commands: runner}, Publish: publishReceipt}
}

// Complete uses the production filesystem and command runners.
func Complete(ctx context.Context, request Request) (Result, error) {
	return New().Complete(ctx, request)
}
