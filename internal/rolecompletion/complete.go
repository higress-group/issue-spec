package rolecompletion

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"

	"github.com/higress-group/issue-spec/internal/assignment"
	"github.com/higress-group/issue-spec/internal/processworkspace"
)

const maxRoleInputBytes = 1 << 20

var fullRevision = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

type implementationDecision struct {
	Decisions      []string `json:"decisions,omitempty"`
	Risks          []string `json:"risks,omitempty"`
	RationaleDraft string   `json:"rationale_draft,omitempty"`
}

type reviewDecision struct {
	Verdict  assignment.ReviewVerdict `json:"verdict"`
	Findings []assignment.Finding     `json:"findings,omitempty"`
}

type verificationDecision struct {
	Summary string `json:"summary,omitempty"`
}

type observedRole struct {
	base         string
	revision     string
	changedPaths []string
	tests        []assignment.TestSelector
	checks       []assignment.CheckSelector
}

func (s *Service) Complete(ctx context.Context, request Request) (Result, error) {
	if s == nil {
		s = New()
	}
	if s.Commands == nil {
		s.Commands = ExecCommandRunner{}
	}
	if s.Tests == nil {
		s.Tests = ShellTestRunner{Commands: s.Commands}
	}
	agent := strings.TrimSpace(request.Agent)
	if agent == "" || strings.EqualFold(agent, "Coordinator") {
		return Result{}, errors.New("--agent must name a non-Coordinator role")
	}

	assignmentPath, assignmentData, err := readRoleInput(request.AssignmentFile, "assignment-file")
	if err != nil {
		return Result{}, err
	}
	packet, err := assignment.ParsePacketJSON(assignmentData)
	if err != nil {
		if _, bareErr := assignment.ParseAssignmentJSON(assignmentData); bareErr == nil {
			return Result{}, errors.New("role completion requires a sealed assignment packet with generation and delivery.worktree_path; issue it with workflow workspace prepare")
		}
		return Result{}, err
	}
	if packet.Delivery == nil || strings.TrimSpace(packet.Delivery.WorktreePath) == "" {
		return Result{}, errors.New("role completion requires packet delivery.worktree_path")
	}
	worktree, err := canonicalDirectory(packet.Delivery.WorktreePath, "delivery.worktree_path")
	if err != nil {
		return Result{}, err
	}
	workingDirectory := strings.TrimSpace(request.WorkingDirectory)
	if workingDirectory == "" {
		getwd := s.Getwd
		if getwd == nil {
			getwd = os.Getwd
		}
		workingDirectory, err = getwd()
		if err != nil {
			return Result{}, fmt.Errorf("resolve current working directory: %w", err)
		}
	}
	current, err := canonicalDirectory(workingDirectory, "current working directory")
	if err != nil {
		return Result{}, err
	}
	if current != worktree {
		return Result{}, fmt.Errorf("current working directory %s is not the sealed delivery worktree %s", current, worktree)
	}
	if pathInside(worktree, assignmentPath) {
		return Result{}, errors.New("--assignment-file must be outside the managed worktree")
	}

	decisionPath, decisionData, err := readRoleInput(request.DecisionFile, "decision-file")
	if err != nil {
		return Result{}, err
	}
	if pathInside(worktree, decisionPath) {
		return Result{}, errors.New("--decision-file must be outside the managed worktree")
	}
	output, err := resolveOutputPath(request.Output, worktree)
	if err != nil {
		return Result{}, err
	}
	if output == assignmentPath || output == decisionPath {
		return Result{}, errors.New("--output must differ from the sealed input files")
	}

	receipt := assignment.Receipt{
		SchemaVersion: assignment.ReceiptSchemaVersion, ID: receiptID(packet), AssignmentID: packet.Assignment.ID,
		AssignmentDigest: packet.AssignmentDigest, AssignmentGeneration: packet.Generation, Role: packet.Assignment.Role,
		ResultSchemaVersion: packet.Assignment.ResultSchemaVersion,
		Provenance: assignment.Provenance{Route: assignment.RouteRoleOwned, Assurance: assignment.AssuranceSelfReported,
			Writer: agent, Subject: agent, Source: "role-complete"},
	}
	if err := applyDecision(&receipt, decisionData); err != nil {
		return Result{}, err
	}
	if err := validateDecisionSemantics(receipt); err != nil {
		return Result{}, err
	}
	observed, err := s.observeRole(ctx, worktree, packet.Assignment)
	if err != nil {
		return Result{}, err
	}

	var testResults []assignment.TestResult
	for _, selector := range observed.tests {
		result := assignment.TestResult{ID: selector.ID, Command: selector.Command, Outcome: assignment.TestPassed, Assurance: assignment.AssuranceSelfReported}
		if selector.RevisionBinding != nil {
			resolved, err := assignment.ResolveTestSelector(selector, observed.revision)
			if err != nil {
				return Result{}, fmt.Errorf("resolve sealed test %q: %w", selector.ID, err)
			}
			result.Command = resolved.Command
			result.AssignedSelector = &resolved.AssignedSelector
			result.ResolvedRevision = resolved.ResolvedRevision
		}
		execution, runErr := s.Tests.Run(ctx, worktree, result.Command)
		if runErr != nil {
			return Result{}, testFailure(selector.ID, result.Command, execution, runErr)
		}
		testResults = append(testResults, result)
	}

	postTests, err := s.observeRole(ctx, worktree, packet.Assignment)
	if err != nil {
		return Result{}, fmt.Errorf("post-test Git observation: %w", err)
	}
	if !reflect.DeepEqual(postTests, observed) {
		return Result{}, errors.New("post-test Git observation differs from the exact pre-test role facts")
	}

	receipt.BaseRevision = postTests.base
	receipt.Tests = testResults
	if receipt.Role == assignment.RoleImplementation {
		receipt.ResultRevision = postTests.revision
		receipt.Implementation.ChangedPaths = append([]string(nil), postTests.changedPaths...)
	} else {
		receipt.SubjectRevision = postTests.revision
		if receipt.Role == assignment.RoleVerification {
			receipt.Verification.CheckSelectors = append([]assignment.CheckSelector(nil), postTests.checks...)
		}
	}

	sealed, err := assignment.SealReceipt(receipt)
	if err != nil {
		return Result{}, fmt.Errorf("seal role receipt: %w", err)
	}
	publish := s.Publish
	if publish == nil {
		publish = publishReceipt
	}
	if err := publish(output, worktree, sealed); err != nil {
		return Result{}, err
	}
	payload, err := readRegularFile(output, "published receipt")
	if err != nil {
		return Result{}, err
	}
	parsed, err := assignment.ParseReceiptJSON(payload)
	if err != nil {
		return Result{}, fmt.Errorf("self-validate published receipt: %w", err)
	}
	if !reflect.DeepEqual(parsed, sealed) || parsed.ID != sealed.ID || parsed.ReceiptDigest != sealed.ReceiptDigest ||
		parsed.AssignmentGeneration != packet.Generation || parsed.Role != packet.Assignment.Role || authoritativeRevision(parsed) != postTests.revision {
		return Result{}, errors.New("self-validate published receipt: final logical identity differs from the sealed result")
	}
	result := Result{ReceiptPath: output, ReceiptID: parsed.ID, ReceiptDigest: parsed.ReceiptDigest,
		Role: parsed.Role, Revision: postTests.revision}
	for _, test := range parsed.Tests {
		result.Tests = append(result.Tests, TestSummary{ID: test.ID, Command: test.Command, Outcome: test.Outcome})
	}
	return result, nil
}

func receiptID(packet assignment.Packet) string {
	return fmt.Sprintf("receipt:%s:%s:%d", packet.Assignment.Role, packet.AssignmentDigest, packet.Generation)
}

func applyDecision(receipt *assignment.Receipt, data []byte) error {
	switch receipt.Role {
	case assignment.RoleImplementation:
		var value implementationDecision
		if err := decodeDecision(data, &value); err != nil {
			return fmt.Errorf("parse implementation decision: %w", err)
		}
		receipt.Implementation = &assignment.ImplementationResult{Decisions: value.Decisions, Risks: value.Risks, RationaleDraft: value.RationaleDraft}
	case assignment.RoleReview:
		var value reviewDecision
		if err := decodeDecision(data, &value); err != nil {
			return fmt.Errorf("parse review decision: %w", err)
		}
		receipt.Review = &assignment.ReviewResult{Verdict: value.Verdict, Findings: value.Findings}
	case assignment.RoleVerification:
		var value verificationDecision
		if err := decodeDecision(data, &value); err != nil {
			return fmt.Errorf("parse verification decision: %w", err)
		}
		receipt.Verification = &assignment.VerificationResult{Summary: value.Summary}
	default:
		return fmt.Errorf("unsupported assignment role %q", receipt.Role)
	}
	return nil
}

// validateDecisionSemantics delegates the closed role judgment to the shared
// receipt model before any sealed command can have side effects. Placeholder
// mechanical facts satisfy only the fields that role completion derives later;
// the caller's semantic payload is preserved exactly for validation.
func validateDecisionSemantics(receipt assignment.Receipt) error {
	candidate := receipt
	candidate.BaseRevision = ""
	candidate.ResultRevision = ""
	candidate.SubjectRevision = ""
	candidate.Tests = nil
	switch candidate.Role {
	case assignment.RoleImplementation:
		implementation := *candidate.Implementation
		candidate.Implementation = &implementation
		candidate.BaseRevision = strings.Repeat("a", 40)
		candidate.ResultRevision = strings.Repeat("b", 40)
		candidate.Implementation.ChangedPaths = []string{"decision-validation-placeholder"}
	case assignment.RoleReview:
		candidate.SubjectRevision = strings.Repeat("a", 40)
	case assignment.RoleVerification:
		verification := *candidate.Verification
		candidate.Verification = &verification
		candidate.SubjectRevision = strings.Repeat("a", 40)
		candidate.Verification.CheckSelectors = []assignment.CheckSelector{{Provider: "decision-validation", Name: "placeholder"}}
	default:
		return fmt.Errorf("unsupported assignment role %q", candidate.Role)
	}
	if _, err := assignment.SealReceipt(candidate); err != nil {
		return fmt.Errorf("validate %s decision semantics: %w", candidate.Role, err)
	}
	return nil
}

func decodeDecision(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func (s *Service) observeRole(ctx context.Context, worktree string, value assignment.Assignment) (observedRole, error) {
	if err := s.validateCommonGit(ctx, worktree); err != nil {
		return observedRole{}, err
	}
	head, err := s.gitText(ctx, worktree, "resolve exact HEAD", nil, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return observedRole{}, err
	}
	if !fullRevision.MatchString(head) {
		return observedRole{}, fmt.Errorf("Git HEAD is not an exact lowercase full object ID: %q", head)
	}
	switch value.Role {
	case assignment.RoleImplementation:
		return s.observeImplementation(ctx, worktree, value, head)
	case assignment.RoleReview:
		if err := s.validateImmutableSubject(ctx, worktree, value.SubjectRevision, head); err != nil {
			return observedRole{}, err
		}
		resolvedBase, err := s.gitText(ctx, worktree, "resolve sealed review diff base", nil, "rev-parse", "--verify", value.Review.DiffBaseRevision+"^{commit}")
		if err != nil || resolvedBase != value.Review.DiffBaseRevision {
			return observedRole{}, errors.Join(errors.New("sealed review diff base does not resolve exactly"), err)
		}
		return observedRole{revision: head, tests: append([]assignment.TestSelector(nil), value.Review.RequiredTests...)}, nil
	case assignment.RoleVerification:
		if err := s.validateImmutableSubject(ctx, worktree, value.SubjectRevision, head); err != nil {
			return observedRole{}, err
		}
		return observedRole{revision: head, tests: append([]assignment.TestSelector(nil), value.Verification.RequiredTests...),
			checks: append([]assignment.CheckSelector(nil), value.Verification.RequiredChecks...)}, nil
	default:
		return observedRole{}, fmt.Errorf("unsupported assignment role %q", value.Role)
	}
}

func (s *Service) validateCommonGit(ctx context.Context, worktree string) error {
	root, err := s.gitText(ctx, worktree, "resolve Git worktree root", nil, "rev-parse", "--show-toplevel")
	if err != nil {
		return err
	}
	canonicalRoot, err := canonicalDirectory(root, "Git worktree root")
	if err != nil || canonicalRoot != worktree {
		return errors.Join(errors.New("sealed delivery path is not the exact Git worktree root"), err)
	}
	status, err := s.gitText(ctx, worktree, "inspect Git cleanliness", nil, "status", "--porcelain=v1", "--untracked-files=normal")
	if err != nil {
		return err
	}
	if status != "" {
		return errors.New("sealed role worktree must be clean")
	}
	listing, err := s.gitText(ctx, worktree, "inspect registered Git worktrees", nil, "worktree", "list", "--porcelain")
	if err != nil {
		return err
	}
	registered := false
	for _, line := range strings.Split(listing, "\n") {
		if !strings.HasPrefix(line, "worktree ") {
			continue
		}
		candidate, candidateErr := canonicalDirectory(strings.TrimPrefix(line, "worktree "), "registered Git worktree")
		if candidateErr == nil && candidate == worktree {
			registered = true
			break
		}
	}
	if !registered {
		return errors.New("sealed delivery worktree is not registered with Git")
	}
	return nil
}

func (s *Service) observeImplementation(ctx context.Context, worktree string, value assignment.Assignment, head string) (observedRole, error) {
	payload := value.Implementation
	branch, err := s.gitText(ctx, worktree, "inspect implementation branch", nil, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil || branch != payload.Branch {
		return observedRole{}, errors.Join(fmt.Errorf("implementation branch %q differs from sealed branch %q", branch, payload.Branch), err)
	}
	parents, err := s.gitText(ctx, worktree, "inspect result commit parents", nil, "rev-list", "--parents", "-n", "1", head)
	if err != nil {
		return observedRole{}, err
	}
	fields := strings.Fields(parents)
	if len(fields) != 2 || fields[0] != head || fields[1] != value.BaseRevision {
		return observedRole{}, errors.New("implementation result must be one non-merge commit directly based on the sealed base")
	}
	count, err := s.gitText(ctx, worktree, "count implementation commits", nil, "rev-list", "--count", value.BaseRevision+".."+head)
	if err != nil || count != "1" {
		return observedRole{}, errors.Join(fmt.Errorf("implementation result must contain exactly one commit, got %q", count), err)
	}
	if payload.Commit.RequireDCO {
		message, err := s.gitText(ctx, worktree, "read result commit message", nil, "show", "-s", "--format=%B", head)
		if err != nil {
			return observedRole{}, err
		}
		trailers, err := s.gitText(ctx, worktree, "parse result commit trailers", []byte(message+"\n"), "interpret-trailers", "--parse")
		if err != nil || !hasSignedOffBy(trailers) {
			return observedRole{}, errors.Join(errors.New("implementation result lacks a valid Signed-off-by trailer"), err)
		}
	}
	diff, err := s.git(ctx, worktree, "list implementation changed paths", nil, "diff", "--name-only", "--no-renames", "-z", value.BaseRevision, head, "--")
	if err != nil {
		return observedRole{}, err
	}
	var changed []string
	for _, path := range bytes.Split(diff.Stdout, []byte{0}) {
		if len(path) > 0 {
			changed = append(changed, string(path))
		}
	}
	if len(changed) == 0 {
		return observedRole{}, errors.New("implementation result commit is empty")
	}
	if err := processworkspace.ValidateManagedWriteScope(payload.WriteOwnership, payload.SharedTouchpoints, changed); err != nil {
		return observedRole{}, err
	}
	if err := s.validateGeneratorOutputs(ctx, worktree, head, payload.Generators); err != nil {
		return observedRole{}, err
	}
	sort.Strings(changed)
	return observedRole{base: value.BaseRevision, revision: head, changedPaths: changed,
		tests: append([]assignment.TestSelector(nil), payload.FocusedTests...)}, nil
}

func (s *Service) validateImmutableSubject(ctx context.Context, worktree, subject, head string) error {
	if head != subject {
		return fmt.Errorf("immutable snapshot HEAD %s differs from sealed subject revision %s", head, subject)
	}
	result, err := s.git(ctx, worktree, "verify detached immutable snapshot", nil, "symbolic-ref", "--quiet", "HEAD")
	if err == nil || result.ExitCode != 1 {
		return errors.New("review and verification completion require a detached immutable snapshot")
	}
	return nil
}

func (s *Service) validateGeneratorOutputs(ctx context.Context, worktree, revision string, generators []assignment.GeneratorPolicy) error {
	var treeFiles []string
	for _, generator := range generators {
		for _, output := range generator.RequiredOutputs {
			if _, err := s.git(ctx, worktree, "validate sealed generator output", nil, "cat-file", "-e", revision+":"+output); err != nil {
				return fmt.Errorf("required generator output %q is absent at the result revision: %w", output, err)
			}
		}
		for _, pattern := range generator.RequiredOutputGlobs {
			if treeFiles == nil {
				result, err := s.git(ctx, worktree, "list result tree files", nil, "ls-tree", "-r", "--name-only", "-z", revision, "--")
				if err != nil {
					return err
				}
				for _, value := range bytes.Split(result.Stdout, []byte{0}) {
					if len(value) > 0 {
						treeFiles = append(treeFiles, string(value))
					}
				}
			}
			matched, err := assignment.MatchAnyRequiredOutputPattern(pattern, treeFiles)
			if err != nil {
				return fmt.Errorf("validate required output pattern %q: %w", pattern, err)
			}
			if !matched {
				return fmt.Errorf("required output pattern %q matched no result-tree file", pattern)
			}
		}
	}
	return nil
}

func (s *Service) gitText(ctx context.Context, directory, operation string, stdin []byte, args ...string) (string, error) {
	result, err := s.git(ctx, directory, operation, stdin, args...)
	return strings.TrimSpace(string(result.Stdout)), err
}

func (s *Service) git(ctx context.Context, directory, operation string, stdin []byte, args ...string) (CommandResult, error) {
	result, err := s.Commands.Run(ctx, Command{Directory: directory, Name: "git", Args: append([]string(nil), args...), Stdin: stdin})
	if err != nil {
		return result, fmt.Errorf("%s: git %s failed (exit %d): %v: %s", operation, strings.Join(args, " "), result.ExitCode, err,
			strings.TrimSpace(string(result.Stderr)))
	}
	return result, nil
}

func hasSignedOffBy(trailers string) bool {
	for _, line := range strings.Split(trailers, "\n") {
		key, value, found := strings.Cut(line, ":")
		if !found || !strings.EqualFold(strings.TrimSpace(key), "Signed-off-by") {
			continue
		}
		value = strings.TrimSpace(value)
		open := strings.LastIndex(value, "<")
		if open > 0 && strings.HasSuffix(value, ">") && strings.Contains(value[open+1:len(value)-1], "@") {
			return true
		}
	}
	return false
}

func testFailure(id, command string, result CommandResult, err error) error {
	detail := strings.TrimSpace(string(result.Stderr))
	if detail == "" {
		detail = strings.TrimSpace(string(result.Stdout))
	}
	return fmt.Errorf("sealed test %q failed (exit %d), command %q: %v: %s", id, result.ExitCode, command, err, detail)
}

func authoritativeRevision(value assignment.Receipt) string {
	if value.Role == assignment.RoleImplementation {
		return value.ResultRevision
	}
	return value.SubjectRevision
}

func readRoleInput(path, name string) (string, []byte, error) {
	path = strings.TrimSpace(path)
	if path == "" || path == "-" || !filepath.IsAbs(path) {
		return "", nil, fmt.Errorf("--%s must be an absolute regular file path and cannot be '-'", name)
	}
	clean := filepath.Clean(path)
	info, err := os.Lstat(clean)
	if err != nil {
		return "", nil, fmt.Errorf("inspect %s: %w", name, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", nil, fmt.Errorf("--%s must name a regular non-symlink file", name)
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(clean))
	if err != nil {
		return "", nil, fmt.Errorf("resolve %s parent: %w", name, err)
	}
	resolved := filepath.Join(parent, filepath.Base(clean))
	data, err := readRegularFile(resolved, name)
	return resolved, data, err
}

func readRegularFile(path, name string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect %s: %w", name, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s must be a regular non-symlink file", name)
	}
	if info.Size() > maxRoleInputBytes {
		return nil, fmt.Errorf("%s exceeds %d bytes", name, maxRoleInputBytes)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", name, err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxRoleInputBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", name, err)
	}
	if len(data) > maxRoleInputBytes {
		return nil, fmt.Errorf("%s exceeds %d bytes", name, maxRoleInputBytes)
	}
	return data, nil
}

func canonicalDirectory(path, name string) (string, error) {
	path = strings.TrimSpace(path)
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("%s must be absolute", name)
	}
	info, err := os.Lstat(filepath.Clean(path))
	if err != nil {
		return "", fmt.Errorf("inspect %s: %w", name, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("%s must be a non-symlink directory", name)
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", name, err)
	}
	return filepath.Clean(resolved), nil
}

func resolveOutputPath(path, worktree string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" || path == "-" || !filepath.IsAbs(path) {
		return "", errors.New("--output must be an absolute file path and cannot be '-'")
	}
	clean := filepath.Clean(path)
	parent, err := canonicalDirectory(filepath.Dir(clean), "output parent")
	if err != nil {
		return "", err
	}
	resolved := filepath.Join(parent, filepath.Base(clean))
	if pathInside(worktree, resolved) {
		return "", errors.New("--output must be outside the managed worktree")
	}
	if info, err := os.Lstat(resolved); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return "", errors.New("--output must name a regular non-symlink file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("inspect output: %w", err)
	}
	return resolved, nil
}

func pathInside(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && (relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

func publishReceipt(output, worktree string, receipt assignment.Receipt) error {
	if err := receipt.Validate(); err != nil {
		return err
	}
	resolved, err := resolveOutputPath(output, worktree)
	if err != nil {
		return err
	}
	payload, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	parent := filepath.Dir(resolved)
	temporary, err := os.CreateTemp(parent, ".issue-spec-role-receipt-*")
	if err != nil {
		return fmt.Errorf("create receipt temporary file: %w", err)
	}
	temporaryName := temporary.Name()
	keep := false
	defer func() {
		_ = temporary.Close()
		if !keep {
			_ = os.Remove(temporaryName)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	if _, err := temporary.Write(payload); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	// Recheck the destination after all derivation and test execution. A
	// pre-existing non-regular target is never replaced.
	if info, err := os.Lstat(resolved); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return errors.New("--output changed to a non-regular or symlink target before publication")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(temporaryName, resolved); err != nil {
		return fmt.Errorf("atomically publish receipt: %w", err)
	}
	keep = true
	return nil
}
