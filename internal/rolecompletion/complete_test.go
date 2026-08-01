package rolecompletion

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/higress-group/issue-spec/internal/assignment"
)

func TestImplementationCompletionDerivesRunsPublishesAndSelfValidates(t *testing.T) {
	fixture := newGitFixture(t)
	packet := fixture.implementationPacket(t, []assignment.TestSelector{{ID: "sealed", Command: "git diff --quiet HEAD HEAD"}})
	packetPath := fixture.writeJSON(t, "packet.json", packet)
	decisionPath := fixture.writeRaw(t, "decision.json", []byte(`{"decisions":["keep v1"],"risks":["self-reported"],"rationale_draft":"role owned"}`))
	output := filepath.Join(fixture.root, "receipt.json")

	result, err := Complete(context.Background(), Request{AssignmentFile: packetPath, DecisionFile: decisionPath,
		Output: output, Agent: "Implementation Worker", WorkingDirectory: fixture.repo})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := assignment.ParseReceiptJSON(data)
	if err != nil || receipt.ValidateForAcceptance() != nil {
		t.Fatalf("receipt parse=%v acceptance=%v", err, receipt.ValidateForAcceptance())
	}
	if result.ReceiptID != receipt.ID || result.ReceiptDigest != receipt.ReceiptDigest || result.Revision != fixture.head ||
		receipt.ID != "receipt:implementation:"+packet.AssignmentDigest+":1" || receipt.BaseRevision != fixture.base || receipt.ResultRevision != fixture.head ||
		len(receipt.Implementation.ChangedPaths) != 1 || receipt.Implementation.ChangedPaths[0] != "marker.txt" ||
		len(receipt.Tests) != 1 || receipt.Tests[0].Command != packet.Assignment.Implementation.FocusedTests[0].Command ||
		receipt.Tests[0].Outcome != assignment.TestPassed || receipt.Provenance.Writer != "Implementation Worker" ||
		receipt.Provenance.Subject != "Implementation Worker" || receipt.Provenance.Source != "role-complete" {
		t.Fatalf("result=%+v receipt=%+v", result, receipt)
	}
	info, err := os.Lstat(output)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("output mode=%v err=%v", info.Mode(), err)
	}
}

func TestFailedSealedTestDoesNotReplaceExistingOutput(t *testing.T) {
	fixture := newGitFixture(t)
	command := "exit 23"
	if runtime.GOOS == "windows" {
		command = "exit /b 23"
	}
	packet := fixture.implementationPacket(t, []assignment.TestSelector{{ID: "must-pass", Command: command}})
	packetPath := fixture.writeJSON(t, "packet.json", packet)
	decisionPath := fixture.writeRaw(t, "decision.json", []byte(`{}`))
	output := fixture.writeRaw(t, "receipt.json", []byte("existing receipt bytes\n"))
	before, _ := os.ReadFile(output)
	if _, err := Complete(context.Background(), Request{AssignmentFile: packetPath, DecisionFile: decisionPath,
		Output: output, Agent: "Worker", WorkingDirectory: fixture.repo}); err == nil || !strings.Contains(err.Error(), "must-pass") {
		t.Fatalf("expected sealed test failure, got %v", err)
	}
	after, _ := os.ReadFile(output)
	if string(after) != string(before) {
		t.Fatalf("failed execution replaced existing output: %q", after)
	}
}

func TestPublicationFailureReturnsNoNewReceipt(t *testing.T) {
	fixture := newGitFixture(t)
	packet := fixture.implementationPacket(t, nil)
	output := filepath.Join(fixture.root, "never-published.json")
	service := New()
	service.Publish = func(string, string, assignment.Receipt) error { return os.ErrPermission }
	if _, err := service.Complete(context.Background(), Request{
		AssignmentFile: fixture.writeJSON(t, "packet.json", packet),
		DecisionFile:   fixture.writeRaw(t, "decision.json", []byte(`{}`)),
		Output:         output, Agent: "Worker", WorkingDirectory: fixture.repo,
	}); err == nil {
		t.Fatal("publication failure succeeded")
	}
	if _, err := os.Lstat(output); !os.IsNotExist(err) {
		t.Fatalf("publication failure left final output: %v", err)
	}
}

func TestPassingSealedTestThatDirtiesTreeCannotPublish(t *testing.T) {
	fixture := newGitFixture(t)
	command := "printf drift > role-test-drift.txt"
	if runtime.GOOS == "windows" {
		command = "echo drift>role-test-drift.txt"
	}
	packet := fixture.implementationPacket(t, []assignment.TestSelector{{ID: "dirty-pass", Command: command}})
	output := filepath.Join(fixture.root, "dirty-receipt.json")
	if _, err := Complete(context.Background(), Request{
		AssignmentFile: fixture.writeJSON(t, "dirty-packet.json", packet),
		DecisionFile:   fixture.writeRaw(t, "dirty-decision.json", []byte(`{}`)),
		Output:         output, Agent: "Worker", WorkingDirectory: fixture.repo,
	}); err == nil || !strings.Contains(err.Error(), "post-test Git observation") {
		t.Fatalf("passing dirty test was accepted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(fixture.repo, "role-test-drift.txt")); err != nil {
		t.Fatalf("completion repaired test-created drift: %v", err)
	}
	if _, err := os.Lstat(output); !os.IsNotExist(err) {
		t.Fatalf("dirty post-test state published output: %v", err)
	}
}

func TestPerTestGitObservationStopsBeforeLaterSelectorCanRestore(t *testing.T) {
	fixture := newGitFixture(t)
	driftPath := filepath.Join(fixture.repo, "transient-role-test-drift.txt")
	runner := &scriptedTestRunner{steps: []func() error{
		func() error { return os.WriteFile(driftPath, []byte("drift\n"), 0o600) },
		func() error { return os.Remove(driftPath) },
	}}
	packet := fixture.implementationPacket(t, []assignment.TestSelector{
		{ID: "01-drift", Command: "introduce transient drift"},
		{ID: "02-restore", Command: "restore transient drift"},
	})
	output := filepath.Join(fixture.root, "transient-drift-receipt.json")
	service := New()
	service.Tests = runner
	if _, err := service.Complete(context.Background(), Request{
		AssignmentFile: fixture.writeJSON(t, "transient-drift-packet.json", packet),
		DecisionFile:   fixture.writeRaw(t, "transient-drift-decision.json", []byte(`{}`)),
		Output:         output, Agent: "Worker", WorkingDirectory: fixture.repo,
	}); err == nil || !strings.Contains(err.Error(), `post-test Git observation after sealed test "01-drift"`) {
		t.Fatalf("transient drift was accepted: %v", err)
	}
	if len(runner.calls) != 1 || runner.calls[0] != "introduce transient drift" {
		t.Fatalf("test runner calls=%v, want only the drift selector", runner.calls)
	}
	if _, err := os.Stat(driftPath); err != nil {
		t.Fatalf("completion ran the restore selector or repaired drift: %v", err)
	}
	if _, err := os.Lstat(output); !os.IsNotExist(err) {
		t.Fatalf("transient drift published output: %v", err)
	}
}

func TestPassingSealedTestThatChangesDetachedSubjectCannotPublish(t *testing.T) {
	fixture := newGitFixture(t)
	runGit(t, fixture.repo, "checkout", "--detach", fixture.head)
	value := fixture.reviewAssignment([]assignment.TestSelector{{ID: "switch-pass", Command: "git checkout --detach " + fixture.base}})
	packet := fixture.packet(t, value, 2)
	output := filepath.Join(fixture.root, "switched-receipt.json")
	if _, err := Complete(context.Background(), Request{
		AssignmentFile: fixture.writeJSON(t, "switched-packet.json", packet),
		DecisionFile:   fixture.writeRaw(t, "switched-decision.json", []byte(`{"verdict":"approve"}`)),
		Output:         output, Agent: "Reviewer", WorkingDirectory: fixture.repo,
	}); err == nil || !strings.Contains(err.Error(), "post-test Git observation") {
		t.Fatalf("passing subject switch was accepted: %v", err)
	}
	if head := gitOutput(t, fixture.repo, "rev-parse", "HEAD"); head != fixture.base {
		t.Fatalf("completion repaired switched subject to %s", head)
	}
	if _, err := os.Lstat(output); !os.IsNotExist(err) {
		t.Fatalf("switched post-test subject published output: %v", err)
	}
}

func TestInvalidRoleDecisionsFailBeforeTestRunnerInvocation(t *testing.T) {
	longText := strings.Repeat("x", 4097)
	tests := []struct {
		name     string
		prepare  func(*testing.T, gitFixture) assignment.Assignment
		decision func() []byte
	}{
		{name: "implementation", prepare: func(t *testing.T, fixture gitFixture) assignment.Assignment {
			return fixture.implementationPacket(t, []assignment.TestSelector{{ID: "must-not-run", Command: "ignored"}}).Assignment
		}, decision: func() []byte {
			data, _ := json.Marshal(implementationDecision{RationaleDraft: longText})
			return data
		}},
		{name: "review", prepare: func(t *testing.T, fixture gitFixture) assignment.Assignment {
			runGit(t, fixture.repo, "checkout", "--detach", fixture.head)
			return fixture.reviewAssignment([]assignment.TestSelector{{ID: "must-not-run", Command: "ignored"}})
		}, decision: func() []byte { return []byte(`{"verdict":"invalid"}`) }},
		{name: "verification", prepare: func(t *testing.T, fixture gitFixture) assignment.Assignment {
			runGit(t, fixture.repo, "checkout", "--detach", fixture.head)
			return fixture.verificationAssignment([]assignment.TestSelector{{ID: "must-not-run", Command: "ignored"}})
		}, decision: func() []byte {
			data, _ := json.Marshal(verificationDecision{Summary: longText})
			return data
		}},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGitFixture(t)
			value := test.prepare(t, fixture)
			packet := fixture.packet(t, value, uint64(index+1))
			runner := &countingTestRunner{}
			service := New()
			service.Tests = runner
			output := filepath.Join(fixture.root, "invalid-decision-receipt.json")
			if _, err := service.Complete(context.Background(), Request{
				AssignmentFile: fixture.writeJSON(t, "invalid-packet.json", packet),
				DecisionFile:   fixture.writeRaw(t, "invalid-decision.json", test.decision()),
				Output:         output, Agent: "Role", WorkingDirectory: fixture.repo,
			}); err == nil || !strings.Contains(err.Error(), "decision semantics") {
				t.Fatalf("invalid decision error=%v", err)
			}
			if runner.calls != 0 {
				t.Fatalf("invalid decision invoked test runner %d times", runner.calls)
			}
			if _, err := os.Lstat(output); !os.IsNotExist(err) {
				t.Fatalf("invalid decision published output: %v", err)
			}
		})
	}
}

func TestReviewAndVerificationPreserveDecisionsAtDetachedSubject(t *testing.T) {
	fixture := newGitFixture(t)
	runGit(t, fixture.repo, "checkout", "--detach", fixture.head)
	design := fixture.designContext()
	review := assignment.Assignment{SchemaVersion: assignment.AssignmentSchemaVersion, ID: "review-assignment", Role: assignment.RoleReview,
		Repository: "acme/repo", Issue: 1, ProcessID: "PROCESS-1001", SubjectRevision: fixture.head,
		Scenarios: []assignment.ScenarioRef{{SpecID: "SPEC-1001", Scenario: "review"}}, DesignContext: &design,
		Policy: assignment.Policy{RequireExactRevision: true}, ResultSchemaVersion: assignment.ReceiptSchemaVersion,
		Review: &assignment.ReviewPayload{SnapshotRevision: fixture.head, DiffBaseRevision: fixture.base, Authors: []string{"Worker"}, Scope: []string{"marker.txt"},
			RequiredTests: []assignment.TestSelector{{ID: "review-test", Command: "git diff --quiet HEAD HEAD"}}}}
	reviewPacket := fixture.packet(t, review, 2)
	reviewOutput := filepath.Join(fixture.root, "review-receipt.json")
	if _, err := Complete(context.Background(), Request{AssignmentFile: fixture.writeJSON(t, "review-packet.json", reviewPacket),
		DecisionFile: fixture.writeRaw(t, "review-decision.json", []byte(`{"verdict":"approve","findings":[]}`)), Output: reviewOutput,
		Agent: "Independent Reviewer", WorkingDirectory: fixture.repo}); err != nil {
		t.Fatal(err)
	}
	reviewReceipt := parseReceipt(t, reviewOutput)
	if reviewReceipt.Review.Verdict != assignment.ReviewApprove || reviewReceipt.SubjectRevision != fixture.head || len(reviewReceipt.Tests) != 1 {
		t.Fatalf("review receipt=%+v", reviewReceipt)
	}

	verification := assignment.Assignment{SchemaVersion: assignment.AssignmentSchemaVersion, ID: "verification-assignment", Role: assignment.RoleVerification,
		Repository: "acme/repo", Issue: 1, ProcessID: "PROCESS-1002", SubjectRevision: fixture.head,
		Scenarios: []assignment.ScenarioRef{{SpecID: "SPEC-1001", Scenario: "verify"}},
		Policy:    assignment.Policy{RequireExactRevision: true}, ResultSchemaVersion: assignment.ReceiptSchemaVersion,
		Verification: &assignment.VerificationPayload{SubjectRevision: fixture.head,
			RequiredChecks: []assignment.CheckSelector{{Provider: "github", Name: "tests"}}}}
	verifyPacket := fixture.packet(t, verification, 3)
	verifyOutput := filepath.Join(fixture.root, "verify-receipt.json")
	if _, err := Complete(context.Background(), Request{AssignmentFile: fixture.writeJSON(t, "verify-packet.json", verifyPacket),
		DecisionFile: fixture.writeRaw(t, "verify-decision.json", []byte(`{"summary":"exact current"}`)), Output: verifyOutput,
		Agent: "Verifier", WorkingDirectory: fixture.repo}); err != nil {
		t.Fatal(err)
	}
	verifyReceipt := parseReceipt(t, verifyOutput)
	if verifyReceipt.Verification.Summary != "exact current" || len(verifyReceipt.Verification.CheckSelectors) != 1 ||
		verifyReceipt.Verification.CheckSelectors[0].Name != "tests" || len(verifyReceipt.Tests) != 0 {
		t.Fatalf("verification receipt=%+v", verifyReceipt)
	}
}

type gitFixture struct {
	root string
	repo string
	base string
	head string
}

func newGitFixture(t *testing.T) gitFixture {
	t.Helper()
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "init", "-b", "main")
	runGit(t, repo, "config", "user.name", "Test Worker")
	runGit(t, repo, "config", "user.email", "worker@example.com")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "base")
	base := gitOutput(t, repo, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(repo, "marker.txt"), []byte("result\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "marker.txt")
	runGit(t, repo, "commit", "-s", "-m", "implement role completion")
	return gitFixture{root: root, repo: repo, base: base, head: gitOutput(t, repo, "rev-parse", "HEAD")}
}

func (f gitFixture) designContext() assignment.DesignContext {
	return assignment.DesignContext{SourceURL: "https://example.com/acme/repo/issues/2", ReadMode: assignment.DesignReadModeCompleteIssueBody,
		Invariant: "derive producer evidence", ApplicableDecisions: []string{"keep v1"}, ImplementationDirection: "complete in one command",
		MustPreserve: []string{"acceptance separation"}, MustNot: []string{"repair receipts"}, MinimumVerification: []string{"run sealed tests"},
		ConflictPolicy: assignment.DesignConflictPolicyAuthoritativeStop}
}

func (f gitFixture) implementationPacket(t *testing.T, tests []assignment.TestSelector) assignment.Packet {
	t.Helper()
	design := f.designContext()
	value := assignment.Assignment{SchemaVersion: assignment.AssignmentSchemaVersion, ID: "implementation-assignment", Role: assignment.RoleImplementation,
		Repository: "acme/repo", Issue: 1, ProcessID: "PROCESS-1001", BaseRevision: f.base,
		Scenarios: []assignment.ScenarioRef{{SpecID: "SPEC-1001", Scenario: "complete"}}, DesignContext: &design,
		Policy: assignment.Policy{RequireExactRevision: true}, ResultSchemaVersion: assignment.ReceiptSchemaVersion,
		Implementation: &assignment.ImplementationPayload{Objective: "complete", Branch: "main", WriteOwnership: []string{"marker.txt"},
			Commit: assignment.CommitPolicy{RequireSingleCommit: true, RequireDCO: true}, FocusedTests: tests}}
	return f.packet(t, value, 1)
}

func (f gitFixture) reviewAssignment(tests []assignment.TestSelector) assignment.Assignment {
	design := f.designContext()
	return assignment.Assignment{SchemaVersion: assignment.AssignmentSchemaVersion, ID: "review-assignment", Role: assignment.RoleReview,
		Repository: "acme/repo", Issue: 1, ProcessID: "PROCESS-1002", SubjectRevision: f.head,
		Scenarios: []assignment.ScenarioRef{{SpecID: "SPEC-1001", Scenario: "review"}}, DesignContext: &design,
		Policy: assignment.Policy{RequireExactRevision: true}, ResultSchemaVersion: assignment.ReceiptSchemaVersion,
		Review: &assignment.ReviewPayload{SnapshotRevision: f.head, DiffBaseRevision: f.base, Authors: []string{"Worker"},
			Scope: []string{"marker.txt"}, RequiredTests: tests}}
}

func (f gitFixture) verificationAssignment(tests []assignment.TestSelector) assignment.Assignment {
	return assignment.Assignment{SchemaVersion: assignment.AssignmentSchemaVersion, ID: "verification-assignment", Role: assignment.RoleVerification,
		Repository: "acme/repo", Issue: 1, ProcessID: "PROCESS-1003", SubjectRevision: f.head,
		Scenarios: []assignment.ScenarioRef{{SpecID: "SPEC-1001", Scenario: "verify"}},
		Policy:    assignment.Policy{RequireExactRevision: true}, ResultSchemaVersion: assignment.ReceiptSchemaVersion,
		Verification: &assignment.VerificationPayload{SubjectRevision: f.head, RequiredTests: tests,
			RequiredChecks: []assignment.CheckSelector{{Provider: "github", Name: "tests"}}}}
}

func (f gitFixture) packet(t *testing.T, value assignment.Assignment, generation uint64) assignment.Packet {
	t.Helper()
	digest, err := assignment.AssignmentDigest(value)
	if err != nil {
		t.Fatal(err)
	}
	packet := assignment.Packet{Assignment: value, AssignmentDigest: digest, Generation: generation,
		Delivery: &assignment.DeliveryMetadata{WorktreePath: f.repo}}
	if err := packet.Validate(); err != nil {
		t.Fatal(err)
	}
	return packet
}

func (f gitFixture) writeJSON(t *testing.T, name string, value any) string {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return f.writeRaw(t, name, append(data, '\n'))
}

func (f gitFixture) writeRaw(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(f.root, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func parseReceipt(t *testing.T, path string) assignment.Receipt {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	value, err := assignment.ParseReceiptJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func runGit(t *testing.T, directory string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = directory
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
}

func gitOutput(t *testing.T, directory string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = directory
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(output))
}

type countingTestRunner struct{ calls int }

func (r *countingTestRunner) Run(context.Context, string, string) (CommandResult, error) {
	r.calls++
	return CommandResult{}, nil
}

type scriptedTestRunner struct {
	calls []string
	steps []func() error
}

func (r *scriptedTestRunner) Run(_ context.Context, _ string, command string) (CommandResult, error) {
	r.calls = append(r.calls, command)
	step := len(r.calls) - 1
	if step >= len(r.steps) {
		return CommandResult{}, nil
	}
	return CommandResult{}, r.steps[step]()
}
