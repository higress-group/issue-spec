package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/higress-group/issue-spec/internal/assignment"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/model"
	"github.com/higress-group/issue-spec/internal/processworkspace"
	"github.com/higress-group/issue-spec/internal/templates"
	runnerworkspace "github.com/higress-group/issue-spec/internal/workspace"
)

func TestWorkspaceCommandRootsDefaultFromRunnerEnvAndExplicitFlagsOverride(t *testing.T) {
	t.Setenv(runnerworkspace.ProcessIntegrationRootEnv, "/runner/session-clone")
	t.Setenv(runnerworkspace.ProcessWorkspaceRootEnv, "/runner/process-pool")
	fs := newFlagSet("workflow workspace test", &bytes.Buffer{})
	flags := addWorkspaceCommandFlags(fs)
	if got, want := *flags.integration, "/runner/session-clone"; got != want {
		t.Fatalf("integration env default = %q, want %q", got, want)
	}
	if got, want := *flags.workspaceRoot, "/runner/process-pool"; got != want {
		t.Fatalf("workspace env default = %q, want %q", got, want)
	}
	if err := fs.Parse([]string{"--integration-root", "/standalone/integration", "--workspace-root", "/standalone/pool"}); err != nil {
		t.Fatal(err)
	}
	if got, want := *flags.integration, "/standalone/integration"; got != want {
		t.Fatalf("explicit integration root = %q, want %q", got, want)
	}
	if got, want := *flags.workspaceRoot, "/standalone/pool"; got != want {
		t.Fatalf("explicit workspace root = %q, want %q", got, want)
	}
}

func TestWorkspaceResultFileParsingAndCompletionConvergence(t *testing.T) {
	base, result := strings.Repeat("a", 40), strings.Repeat("b", 40)
	receipt, err := assignment.SealReceipt(assignment.Receipt{
		SchemaVersion: assignment.ReceiptSchemaVersion, ID: "receipt-process-004-1", AssignmentID: "assignment-process-004-1",
		AssignmentDigest: strings.Repeat("c", 64), AssignmentGeneration: 1, Role: assignment.RoleImplementation,
		ResultSchemaVersion: assignment.ReceiptSchemaVersion, BaseRevision: base, ResultRevision: result,
		Provenance:     assignment.Provenance{Route: assignment.RouteRoleOwned, Assurance: assignment.AssuranceSelfReported, Writer: "Worker", Subject: "Worker", Source: "role-result-file"},
		Implementation: &assignment.ImplementationResult{ChangedPaths: []string{"internal/commands/result.go"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(receipt)
	path := filepath.Join(t.TempDir(), "receipt.json")
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	parsed, err := readWorkspaceResultFile(path)
	if err != nil || parsed.ID != receipt.ID || parsed.ResultRevision != result {
		t.Fatalf("parsed receipt=%+v err=%v", parsed, err)
	}

	now := time.Unix(100, 0).UTC()
	binding := &processworkspace.AssignmentBinding{SchemaVersion: assignment.AssignmentSchemaVersion, AssignmentID: receipt.AssignmentID,
		Digest: receipt.AssignmentDigest, Role: assignment.RoleImplementation, BaseRevision: base, Generation: 1}
	remote := processworkspace.PortableLease{SchemaVersion: 1, WorkspaceID: "ws-process-004", Repository: "o/r", ProcessID: "PROCESS-004",
		ExecutionClass: processworkspace.ExecutionChangeBearing, Mode: processworkspace.ModeWritable, BaseSHA: base, Branch: "worker",
		WriteOwnership: []string{"internal/commands/**"}, RuntimeNamespace: "ws-process-004", Assignment: binding,
		State: processworkspace.StatePrepared, CreatedAt: now, UpdatedAt: now}
	local := processworkspace.LocalLease{Portable: remote}
	if err := validateCompletionConvergence(remote, local, "o/r"); err != nil {
		t.Fatal(err)
	}
	drifted := remote
	drifted.Assignment = &processworkspace.AssignmentBinding{SchemaVersion: binding.SchemaVersion, AssignmentID: binding.AssignmentID,
		Digest: strings.Repeat("d", 64), Role: binding.Role, BaseRevision: binding.BaseRevision, Generation: binding.Generation}
	if err := validateCompletionConvergence(drifted, local, "o/r"); err == nil || !strings.Contains(err.Error(), "assignment binding") {
		t.Fatalf("binding drift accepted: %v", err)
	}
}

func TestCompileWorkspaceAssignmentSupportsWritableReviewAndVerification(t *testing.T) {
	repo, base := workspaceGitRepository(t)
	backend := newWorkspaceCASBackend("")
	write := func(name, value string) {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
		workspaceGit(t, repo, "add", name)
		workspaceGit(t, repo, "commit", "-s", "-m", "change "+name)
	}
	write("worker.go", "package worker\n")
	subject := workspaceGitOutput(t, repo, "rev-parse", "HEAD")

	processBody := func(class model.ProcessExecutionClass, input assignment.ProcessInput) string {
		body, err := templates.ProcessComment(templates.ProcessCommentOptions{Common: templates.CommonOptions{ID: "PROCESS-006", Status: "in-progress"},
			Input: templates.ProcessInput{Title: "assignment", ParentTask: "TASK-004", ExecutionClass: class, Scope: "assignment",
				WriteOwnership: []string{"internal/commands/**"}, Assignment: &input, Covers: []string{"SPEC-001"}, Handoff: "ready"}})
		if err != nil {
			t.Fatal(err)
		}
		return body
	}
	scenarios := []assignment.ScenarioRef{{SpecID: "SPEC-001", Scenario: "Worker receives a bounded assignment"}}
	writable := processworkspace.LocalLease{Portable: processworkspace.PortableLease{SchemaVersion: 1, WorkspaceID: "ws-p006", Repository: "o/r", ProcessID: "PROCESS-006",
		ExecutionClass: processworkspace.ExecutionChangeBearing, Mode: processworkspace.ModeWritable, BaseSHA: base, Branch: "worker",
		WriteOwnership: []string{"internal/commands/**"}, RuntimeNamespace: "ws-p006", State: processworkspace.StatePrepared}, IntegrationRoot: repo}
	implementation, err := compileWorkspaceAssignment(t.Context(), backend, "o/r", 297, "PROCESS-006", model.ProcessExecutionChangeBearing,
		processBody(model.ProcessExecutionChangeBearing, assignment.ProcessInput{Objective: "implement issuance", DesignContext: workspaceDesignContext(), ScenarioSelectors: scenarios}), writable, "", "", scenarios, false)
	if err != nil || implementation.Role != assignment.RoleImplementation || len(implementation.Implementation.FocusedTests) != 0 {
		t.Fatalf("implementation=%+v err=%v", implementation, err)
	}
	if !reflect.DeepEqual(implementation.DesignContext, workspaceDesignContext()) {
		t.Fatalf("implementation design context changed: %+v", implementation.DesignContext)
	}
	missingDesign := processBody(model.ProcessExecutionChangeBearing, assignment.ProcessInput{Objective: "implement issuance", ScenarioSelectors: scenarios})
	if _, err := compileWorkspaceAssignment(t.Context(), backend, "o/r", 297, "PROCESS-006", model.ProcessExecutionChangeBearing,
		missingDesign, writable, "", "", scenarios, false); err == nil || !strings.Contains(err.Error(), "require design_context") {
		t.Fatalf("missing design context error = %v", err)
	}
	mismatched := workspaceDesignContext()
	mismatched.SourceURL = "https://github.com/o/r/issues/999"
	if _, err := compileWorkspaceAssignment(t.Context(), backend, "o/r", 297, "PROCESS-006", model.ProcessExecutionChangeBearing,
		processBody(model.ProcessExecutionChangeBearing, assignment.ProcessInput{Objective: "implement issuance", DesignContext: mismatched, ScenarioSelectors: scenarios}), writable, "", "", scenarios, false); err == nil || !strings.Contains(err.Error(), "does not match canonical Design URL") {
		t.Fatalf("mismatched design source error = %v", err)
	}
	for name, implementBody := range map[string]string{
		"missing":   "<!-- issue-spec:issue=implement change=portable-assignments version=1 -->",
		"ambiguous": "<!-- issue-spec:issue=implement change=portable-assignments version=1 -->\n- Design Issue: 296\n- Design Issue: 296",
	} {
		t.Run("design-source-"+name, func(t *testing.T) {
			invalidBackend := newWorkspaceCASBackend("")
			invalidBackend.getIssue = func(_ context.Context, _ string, issue int) (github.Issue, error) {
				return github.Issue{Number: issue, HTMLURL: fmt.Sprintf("https://github.com/o/r/issues/%d", issue), Body: implementBody}, nil
			}
			if _, err := compileWorkspaceAssignment(t.Context(), invalidBackend, "o/r", 297, "PROCESS-006", model.ProcessExecutionChangeBearing,
				processBody(model.ProcessExecutionChangeBearing, assignment.ProcessInput{Objective: "implement issuance", DesignContext: workspaceDesignContext(), ScenarioSelectors: scenarios}), writable, "", "", scenarios, false); err == nil || !strings.Contains(err.Error(), "Design Issue") {
				t.Fatalf("%s design source error = %v", name, err)
			}
		})
	}

	snapshot := processworkspace.LocalLease{Portable: processworkspace.PortableLease{SchemaVersion: 1, WorkspaceID: "ws-review", Repository: "o/r", ProcessID: "PROCESS-006",
		ExecutionClass: processworkspace.ExecutionReview, Mode: processworkspace.ModeSnapshot, BaseSHA: subject, DetachedRevision: subject,
		RuntimeNamespace: "ws-review", State: processworkspace.StatePrepared}, IntegrationRoot: repo}
	review, err := compileWorkspaceAssignment(t.Context(), backend, "o/r", 297, "PROCESS-006", model.ProcessExecutionReview,
		processBody(model.ProcessExecutionReview, assignment.ProcessInput{DesignContext: workspaceDesignContext(), ScenarioSelectors: scenarios}), snapshot, "", base, scenarios, false)
	if err != nil || review.Role != assignment.RoleReview || len(review.Review.Authors) != 1 || !reflect.DeepEqual(review.Review.Scope, []string{"worker.go"}) {
		t.Fatalf("review=%+v err=%v", review, err)
	}
	if !reflect.DeepEqual(review.DesignContext, workspaceDesignContext()) {
		t.Fatalf("review design context changed: %+v", review.DesignContext)
	}
	reviewJSON, err := assignment.CanonicalAssignmentJSON(review)
	if err != nil {
		t.Fatal(err)
	}
	reviewFile := filepath.Join(t.TempDir(), "review.json")
	if err := os.WriteFile(reviewFile, reviewJSON, 0o600); err != nil {
		t.Fatal(err)
	}
	fromFile, err := compileWorkspaceAssignment(t.Context(), backend, "o/r", 297, "PROCESS-006", model.ProcessExecutionReview,
		processBody(model.ProcessExecutionReview, assignment.ProcessInput{DesignContext: workspaceDesignContext(), ScenarioSelectors: scenarios}), snapshot, reviewFile, "", scenarios, false)
	if err != nil || !reflect.DeepEqual(fromFile.Review, review.Review) {
		t.Fatalf("review file=%+v err=%v", fromFile, err)
	}
	conflictingInput := workspaceDesignContext()
	conflictingInput.Invariant = "different authority"
	if _, err := compileWorkspaceAssignment(t.Context(), backend, "o/r", 297, "PROCESS-006", model.ProcessExecutionReview,
		processBody(model.ProcessExecutionReview, assignment.ProcessInput{DesignContext: conflictingInput, ScenarioSelectors: scenarios}), snapshot, reviewFile, "", scenarios, false); err == nil || !strings.Contains(err.Error(), "assignment file and PROCESS design_context differ") {
		t.Fatalf("conflicting assignment authorities error = %v", err)
	}
	review.Review.Authors = []string{"Omitted Author <omitted@example.com>"}
	reviewJSON, err = assignment.CanonicalAssignmentJSON(review)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(reviewFile, reviewJSON, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := compileWorkspaceAssignment(t.Context(), backend, "o/r", 297, "PROCESS-006", model.ProcessExecutionReview,
		processBody(model.ProcessExecutionReview, assignment.ProcessInput{DesignContext: workspaceDesignContext(), ScenarioSelectors: scenarios}), snapshot, reviewFile, "", scenarios, false); err == nil || !strings.Contains(err.Error(), "compiler-derived") {
		t.Fatalf("review file bypassed Git-derived authors: %v", err)
	}
	omittedScope := fromFile
	omittedScope.Review.Scope = []string{"different.go"}
	reviewJSON, err = assignment.CanonicalAssignmentJSON(omittedScope)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(reviewFile, reviewJSON, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := compileWorkspaceAssignment(t.Context(), backend, "o/r", 297, "PROCESS-006", model.ProcessExecutionReview,
		processBody(model.ProcessExecutionReview, assignment.ProcessInput{DesignContext: workspaceDesignContext(), ScenarioSelectors: scenarios}), snapshot, reviewFile, "", scenarios, false); err == nil || !strings.Contains(err.Error(), "compiler-derived") {
		t.Fatalf("review file bypassed Git-derived scope: %v", err)
	}
	invalidBase := fromFile
	invalidBase.Review.DiffBaseRevision = strings.Repeat("f", 40)
	reviewJSON, err = assignment.CanonicalAssignmentJSON(invalidBase)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(reviewFile, reviewJSON, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := compileWorkspaceAssignment(t.Context(), backend, "o/r", 297, "PROCESS-006", model.ProcessExecutionReview,
		processBody(model.ProcessExecutionReview, assignment.ProcessInput{DesignContext: workspaceDesignContext(), ScenarioSelectors: scenarios}), snapshot, reviewFile, "", scenarios, false); err == nil || !strings.Contains(err.Error(), "ancestor") {
		t.Fatalf("review file accepted non-ancestor diff base: %v", err)
	}

	verification, err := compileWorkspaceAssignment(t.Context(), backend, "o/r", 297, "PROCESS-006", model.ProcessExecutionVerification,
		processBody(model.ProcessExecutionVerification, assignment.ProcessInput{ScenarioSelectors: scenarios,
			RequiredTests: []assignment.TestSelector{{ID: "unit", Command: "go test ./internal/commands"}}}), snapshot, "", "", scenarios, false)
	if err != nil || verification.Role != assignment.RoleVerification || len(verification.Verification.RequiredTests) != 1 {
		t.Fatalf("verification=%+v err=%v", verification, err)
	}
}

func TestDeriveReviewAssignmentIncludesDeleteOnlyScope(t *testing.T) {
	repo, base := workspaceGitRepository(t)
	workspaceGit(t, repo, "rm", "README.md")
	workspaceGit(t, repo, "commit", "-s", "-m", "delete README")
	subject := workspaceGitOutput(t, repo, "rev-parse", "HEAD")
	authors, scope, err := deriveReviewAssignment(t.Context(), repo, base, subject)
	if err != nil || len(authors) != 1 || !reflect.DeepEqual(scope, []string{"README.md"}) {
		t.Fatalf("delete-only review authors=%v scope=%v err=%v", authors, scope, err)
	}
}

func TestAssignmentPacketOutputIsPrivateAndOutsideWorktree(t *testing.T) {
	root := t.TempDir()
	worktree := filepath.Join(root, "worktree")
	outDir := filepath.Join(root, "sidecar")
	if err := os.MkdirAll(worktree, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		t.Fatal(err)
	}
	value := assignment.Assignment{SchemaVersion: assignment.AssignmentSchemaVersion, ID: "packet-1", Role: assignment.RoleImplementation,
		Repository: "o/r", Issue: 297, ProcessID: "PROCESS-006", BaseRevision: strings.Repeat("a", 40),
		Scenarios: []assignment.ScenarioRef{{SpecID: "SPEC-001", Scenario: "packet"}}, DesignContext: workspaceDesignContext(), Policy: assignment.Policy{RequireExactRevision: true, MaxResultItems: 64},
		ResultSchemaVersion: assignment.ReceiptSchemaVersion, Implementation: &assignment.ImplementationPayload{Objective: "packet", Branch: "worker",
			WriteOwnership: []string{"internal/commands/**"}, Commit: assignment.CommitPolicy{RequireSingleCommit: true, RequireDCO: true}}}
	digest, err := assignment.AssignmentDigest(value)
	if err != nil {
		t.Fatal(err)
	}
	packet := assignment.Packet{Assignment: value, AssignmentDigest: digest, Generation: 1, Delivery: &assignment.DeliveryMetadata{WorktreePath: worktree}}
	packet.Delivery.WorktreePath = filepath.Join(root, "different-local-path")
	if got, err := assignment.AssignmentDigest(packet.Assignment); err != nil || got != digest {
		t.Fatalf("local delivery path changed portable digest: got=%s err=%v", got, err)
	}
	packet.Delivery.WorktreePath = worktree
	out := filepath.Join(outDir, "packet.json")
	if err := writeAssignmentPacket(out, worktree, packet); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("packet mode=%v", info.Mode())
	}
	if err := writeAssignmentPacket(filepath.Join(worktree, "packet.json"), worktree, packet); err == nil {
		t.Fatal("accepted packet output inside worktree")
	}
	if err := writeAssignmentPacket("-", worktree, packet); err == nil {
		t.Fatal("accepted stdout alias as packet file")
	}
	symlinkParent := filepath.Join(root, "linked")
	if err := os.Symlink(worktree, symlinkParent); err != nil {
		t.Fatal(err)
	}
	if err := writeAssignmentPacket(filepath.Join(symlinkParent, "escape.json"), worktree, packet); err == nil {
		t.Fatal("accepted symlink escape into worktree")
	}
}

func TestLoadCoveredScenarioCatalogExpandsConfirmedSpecsAndRejectsUnknownSelection(t *testing.T) {
	spec := func(id, status string, scenarios ...string) string {
		inputs := make([]templates.SpecScenarioInput, 0, len(scenarios))
		for _, title := range scenarios {
			inputs = append(inputs, templates.SpecScenarioInput{Title: title, When: "requested", Then: "handled"})
		}
		body, err := templates.SpecComment(templates.SpecCommentOptions{Common: templates.CommonOptions{ID: id, Status: status},
			Input: templates.SpecInput{Requirement: templates.SpecRequirementInput{Title: "assignment", Text: "The CLI MUST issue assignments."}, Scenarios: inputs}})
		if err != nil {
			t.Fatal(err)
		}
		return body
	}
	backend := newWorkspaceCASBackend("")
	backend.listIssueComments = func(context.Context, string, int) ([]github.Comment, error) {
		return []github.Comment{{ID: 1, Body: spec("SPEC-001", "confirmed", "one", "two")}, {ID: 2, Body: spec("SPEC-002", "confirmed", "other")}}, nil
	}
	process, err := templates.ProcessComment(templates.ProcessCommentOptions{Common: templates.CommonOptions{ID: "PROCESS-006", Status: "in-progress"},
		Input: templates.ProcessInput{Title: "catalog", ParentTask: "TASK-004", ExecutionClass: model.ProcessExecutionChangeBearing,
			WriteOwnership: []string{"internal/commands/**"}, Covers: []string{"SPEC-001"}, Handoff: "ready"}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := loadCoveredScenarioCatalog(t.Context(), backend, "o/r", 295, process)
	if err != nil || !reflect.DeepEqual(catalog, []assignment.ScenarioRef{{SpecID: "SPEC-001", Scenario: "one"}, {SpecID: "SPEC-001", Scenario: "two"}}) {
		t.Fatalf("catalog=%+v err=%v", catalog, err)
	}
	if err := validateAssignmentScenarios([]assignment.ScenarioRef{{SpecID: "SPEC-001", Scenario: "missing"}}, catalog); err == nil {
		t.Fatal("accepted scenario not present in confirmed covered SPEC")
	}
}

func TestWorkspacePreparePersistsAssignmentBeforePacketAndRedispatchesExplicitly(t *testing.T) {
	repo, base := workspaceGitRepository(t)
	specBody, err := templates.SpecComment(templates.SpecCommentOptions{Common: templates.CommonOptions{ID: "SPEC-001", Status: "confirmed"},
		Input: templates.SpecInput{Requirement: templates.SpecRequirementInput{Title: "assignment", Text: "The CLI MUST issue assignments."},
			Scenarios: []templates.SpecScenarioInput{{Title: "packet", When: "prepared", Then: "issued"}}}})
	if err != nil {
		t.Fatal(err)
	}
	processBody, err := templates.ProcessComment(templates.ProcessCommentOptions{Common: templates.CommonOptions{ID: "PROCESS-004", Status: "in-progress"},
		Input: templates.ProcessInput{Title: "workspace", ParentTask: "TASK-004", ExecutionClass: model.ProcessExecutionChangeBearing,
			WriteOwnership: []string{"internal/commands/**"}, Covers: []string{"SPEC-001"}, Handoff: "ready",
			Assignment: &assignment.ProcessInput{Objective: "issue a packet", DesignContext: workspaceDesignContext()}}})
	if err != nil {
		t.Fatal(err)
	}
	backend := newWorkspaceCASBackend(processBody)
	backend.listIssueComments = func(_ context.Context, _ string, issue int) ([]github.Comment, error) {
		if issue == 295 {
			return []github.Comment{{ID: 91, Body: specBody}}, nil
		}
		return []github.Comment{{ID: 77, HTMLURL: "https://example.test/process/77", Body: backend.body}}, nil
	}
	root := filepath.Join(t.TempDir(), "managed")
	packetPath := filepath.Join(t.TempDir(), "packet.json")
	args := append(workspaceBaseArgs(repo, root, "owner-secret"), "--base", base, "--issue-assignment", "--proposal", "295", "--assignment-out", packetPath, "--json")
	app, out, errOut := transitionAppWithError(backend)
	if code := app.runWorkflowWorkspace(t.Context(), append([]string{"prepare"}, args...)); code != 0 {
		t.Fatalf("prepare assignment code=%d out=%s err=%s", code, out.String(), errOut.String())
	}
	first := decodeWorkspaceResult(t, out)
	if first.Assignment == nil || first.Assignment.Generation != 1 || first.Assignment.Assignment.Scenarios[0].Scenario != "packet" {
		t.Fatalf("first assignment=%+v", first.Assignment)
	}
	remote := model.ParseProcessWorkspace("PROCESS-004", "", backend.body)
	if remote.Blocking() || remote.Workspace == nil || remote.Workspace.Assignment == nil || remote.Workspace.Assignment.Generation != 1 {
		t.Fatalf("remote binding=%+v", remote)
	}
	manager := openWorkspaceManager(t, repo, root)
	local, found, err := manager.Store.Get(t.Context(), first.WorkspaceID)
	if err != nil || !found || local.Assignment == nil || local.Portable.Assignment == nil {
		t.Fatalf("local assignment persisted found=%v lease=%+v err=%v", found, local, err)
	}

	app, out, errOut = transitionAppWithError(backend)
	redispatchArgs := append([]string{}, args...)
	redispatchArgs = append(redispatchArgs, "--redispatch-assignment", "--expected-assignment-generation", "1")
	if code := app.runWorkflowWorkspace(t.Context(), append([]string{"prepare"}, redispatchArgs...)); code != 0 {
		t.Fatalf("redispatch code=%d out=%s err=%s", code, out.String(), errOut.String())
	}
	second := decodeWorkspaceResult(t, out)
	if second.Assignment == nil || second.Assignment.Generation != 2 || second.Assignment.Assignment.ID == first.Assignment.Assignment.ID {
		t.Fatalf("redispatch assignment=%+v", second.Assignment)
	}
}

func TestWorkspaceReceiptImportRemainsInformationalThroughRecoveryIntegrationAndCleanup(t *testing.T) {
	t.Setenv(codexThreadIDEnv, "coordinator-chosen-worker-session")
	repo, base := workspaceGitRepository(t)
	specBody, err := templates.SpecComment(templates.SpecCommentOptions{Common: templates.CommonOptions{ID: "SPEC-001", Status: "confirmed"},
		Input: templates.SpecInput{Requirement: templates.SpecRequirementInput{Title: "receipt import", Text: "The CLI MUST keep Coordinator-imported receipt identity informational."},
			Scenarios: []templates.SpecScenarioInput{{Title: "honest fallback", When: "a result file is completed", Then: "the Git lifecycle advances without accepted role authority"}}}})
	if err != nil {
		t.Fatal(err)
	}
	processBody, err := templates.ProcessComment(templates.ProcessCommentOptions{Common: templates.CommonOptions{ID: "PROCESS-004", Status: "in-progress"},
		Input: templates.ProcessInput{Title: "receipt import", ParentTask: "TASK-004", ExecutionClass: model.ProcessExecutionChangeBearing,
			WriteOwnership: []string{"internal/commands/**"}, Covers: []string{"SPEC-001"}, Handoff: "ready",
			Assignment: &assignment.ProcessInput{Objective: "validate an informational result file", DesignContext: workspaceDesignContext()}}})
	if err != nil {
		t.Fatal(err)
	}
	processBody, err = model.StampTypedSessionMetadata(processBody, "coordinator-session", codexThreadIDEnv)
	if err != nil {
		t.Fatal(err)
	}
	backend := newWorkspaceCASBackend(processBody)
	backend.listIssueComments = func(_ context.Context, _ string, issue int) ([]github.Comment, error) {
		if issue == 295 {
			return []github.Comment{{ID: 91, Body: specBody}}, nil
		}
		return []github.Comment{{ID: 77, HTMLURL: "https://example.test/process/77", Body: backend.body}}, nil
	}
	root := filepath.Join(t.TempDir(), "managed")
	prepareArgs := append(workspaceBaseArgs(repo, root, "receipt-owner"), "--base", base, "--issue-assignment", "--proposal", "295", "--json")
	app, out, errOut := transitionAppWithError(backend)
	if code := app.runWorkflowWorkspace(t.Context(), append([]string{"prepare"}, prepareArgs...)); code != 0 {
		t.Fatalf("prepare code=%d out=%s err=%s", code, out.String(), errOut.String())
	}
	prepared := decodeWorkspaceResult(t, out)
	if prepared.Assignment == nil {
		t.Fatal("prepare omitted assignment packet")
	}
	resultPath := "internal/commands/receipt-import.txt"
	absoluteResult := filepath.Join(prepared.WorktreePath, filepath.FromSlash(resultPath))
	if err := os.MkdirAll(filepath.Dir(absoluteResult), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(absoluteResult, []byte("informational\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	workspaceGit(t, prepared.WorktreePath, "add", "--", resultPath)
	workspaceGit(t, prepared.WorktreePath, "commit", "-s", "-m", "validate receipt import")
	resultCommit := workspaceGitOutput(t, prepared.WorktreePath, "rev-parse", "HEAD")
	receipt, err := assignment.SealReceipt(assignment.Receipt{
		SchemaVersion: assignment.ReceiptSchemaVersion, ID: "receipt-process-004-informational-1",
		AssignmentID: prepared.Assignment.Assignment.ID, AssignmentDigest: prepared.Assignment.AssignmentDigest,
		AssignmentGeneration: prepared.Assignment.Generation, Role: assignment.RoleImplementation,
		ResultSchemaVersion: prepared.Assignment.Assignment.ResultSchemaVersion, BaseRevision: base, ResultRevision: resultCommit,
		Provenance: assignment.Provenance{Route: assignment.RouteRoleOwned, Assurance: assignment.AssuranceSelfReported,
			Writer: "Worker", Subject: "Worker", Source: "role-result-file"},
		Implementation: &assignment.ImplementationResult{ChangedPaths: []string{resultPath}},
	})
	if err != nil {
		t.Fatal(err)
	}
	receiptPayload, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	receiptPath := filepath.Join(t.TempDir(), "receipt.json")
	if err := os.WriteFile(receiptPath, receiptPayload, 0o600); err != nil {
		t.Fatal(err)
	}

	app, out, errOut = transitionAppWithError(backend)
	if code := app.runWorkflowWorkspace(t.Context(), []string{"submit-result"}); code != 2 ||
		!strings.Contains(errOut.String(), "unknown workflow workspace command") {
		t.Fatalf("disabled submit-result code=%d out=%s err=%s", code, out.String(), errOut.String())
	}
	manager := openWorkspaceManager(t, repo, root)
	unchanged, found, err := manager.Store.Get(t.Context(), prepared.WorkspaceID)
	if err != nil || !found || unchanged.Portable.State != processworkspace.StatePrepared ||
		unchanged.Portable.ResultCommit != "" {
		t.Fatalf("disabled route mutated lease=%+v found=%v err=%v", unchanged.Portable, found, err)
	}
	assertNoLocalAcceptedImplementationAuthority(t, unchanged)

	completeArgs := append([]string{"complete"}, workspaceBaseArgs(repo, root, "receipt-owner")...)
	completeArgs = append(completeArgs, "--result-file", receiptPath, "--json")
	backend.updateErr = errWorkspaceRemoteUnavailable
	app, out, _ = transitionAppWithError(backend)
	if code := app.runWorkflowWorkspace(t.Context(), completeArgs); code != 1 {
		t.Fatalf("partial complete code=%d out=%s", code, out.String())
	}
	partial := decodeWorkspaceResult(t, out)
	if partial.Code != "remote_workspace_update_failed" || !partial.ReconcileRequired ||
		partial.State != processworkspace.StateWorkerComplete || partial.ResultCommit != resultCommit ||
		partial.AcceptedReceiptID != "" || partial.AcceptedReceiptSubmission != nil {
		t.Fatalf("partial complete=%+v", partial)
	}
	if _, found, observeErr := model.ObserveAcceptedReceiptAuthority(backend.body, assignment.RoleImplementation); observeErr != nil || found {
		t.Fatalf("failed publication changed remote authority: found=%v err=%v", found, observeErr)
	}

	backend.updateErr = nil
	app, out, errOut = transitionAppWithError(backend)
	if code := app.runWorkflowWorkspace(t.Context(), completeArgs); code != 0 {
		t.Fatalf("complete recovery code=%d out=%s err=%s", code, out.String(), errOut.String())
	}
	assertRemoteHasNoImplementationReceiptAuthority(t, backend.body, processworkspace.StateWorkerComplete)

	app, out, errOut = transitionAppWithError(backend)
	if code := app.runWorkspaceIntegrate(t.Context(), workspaceIntegrateArgs(repo, root, "receipt-owner", base)); code != 0 {
		t.Fatalf("integrate code=%d out=%s err=%s", code, out.String(), errOut.String())
	}
	assertRemoteHasNoImplementationReceiptAuthority(t, backend.body, processworkspace.StateIntegrated)

	cleanupArgs := append([]string{"cleanup"}, workspaceBaseArgs(repo, root, "receipt-owner")...)
	cleanupArgs = append(cleanupArgs, "--json")
	app, out, errOut = transitionAppWithError(backend)
	if code := app.runWorkflowWorkspace(t.Context(), cleanupArgs); code != 0 {
		t.Fatalf("cleanup code=%d out=%s err=%s", code, out.String(), errOut.String())
	}
	assertRemoteHasNoImplementationReceiptAuthority(t, backend.body, processworkspace.StateCleaned)
	recovered, found, err := manager.Store.Get(t.Context(), prepared.WorkspaceID)
	if err != nil || !found {
		t.Fatalf("recovered lease=%+v found=%v err=%v", recovered.Portable, found, err)
	}
	assertNoLocalAcceptedImplementationAuthority(t, recovered)
}

func assertNoLocalAcceptedImplementationAuthority(t *testing.T, lease processworkspace.LocalLease) {
	t.Helper()
	if lease.AcceptedReceiptID != "" || lease.Portable.AcceptedReceiptID != "" ||
		lease.Portable.AcceptedReceiptDigest != "" || lease.Portable.AcceptedReceiptGeneration != 0 ||
		lease.Portable.AcceptedReceiptSubmission != nil {
		t.Fatalf("Coordinator import created accepted role evidence: %+v", lease)
	}
}

func assertRemoteHasNoImplementationReceiptAuthority(t *testing.T, body string, state processworkspace.LifecycleState) {
	t.Helper()
	if authority, found, err := model.ObserveAcceptedReceiptAuthority(body, assignment.RoleImplementation); err != nil || found {
		t.Fatalf("remote authority=%+v found=%v err=%v", authority, found, err)
	}
	parsed := model.ParseProcessWorkspace("PROCESS-004", "", body)
	if parsed.Blocking() || parsed.Workspace == nil || parsed.Workspace.State != state ||
		parsed.Workspace.AcceptedReceiptID != "" || parsed.Workspace.AcceptedReceiptDigest != "" ||
		parsed.Workspace.AcceptedReceiptGeneration != 0 || parsed.Workspace.AcceptedReceiptSubmission != nil {
		t.Fatalf("remote workspace=%+v", parsed)
	}
}
func TestWorkspacePrepareRemoteOnlyAssignmentRecoveryProvesBindingBeforeMutation(t *testing.T) {
	for _, name := range []string{"match", "mismatch", "generation-mismatch", "local-only"} {
		t.Run(name, func(t *testing.T) {
			repo, base := workspaceGitRepository(t)
			specBody, err := templates.SpecComment(templates.SpecCommentOptions{Common: templates.CommonOptions{ID: "SPEC-001", Status: "confirmed"},
				Input: templates.SpecInput{Requirement: templates.SpecRequirementInput{Title: "assignment", Text: "The CLI MUST recover assignments."},
					Scenarios: []templates.SpecScenarioInput{{Title: "recover packet", When: "retried", Then: "matched"}}}})
			if err != nil {
				t.Fatal(err)
			}
			processBody, err := templates.ProcessComment(templates.ProcessCommentOptions{Common: templates.CommonOptions{ID: "PROCESS-004", Status: "in-progress"},
				Input: templates.ProcessInput{Title: "recovery", ParentTask: "TASK-004", ExecutionClass: model.ProcessExecutionChangeBearing,
					WriteOwnership: []string{"internal/commands/**"}, Covers: []string{"SPEC-001"}, Handoff: "ready",
					Assignment: &assignment.ProcessInput{Objective: "recover exact packet", DesignContext: workspaceDesignContext()}}})
			if err != nil {
				t.Fatal(err)
			}
			backend := newWorkspaceCASBackend(processBody)
			backend.listIssueComments = func(_ context.Context, _ string, issue int) ([]github.Comment, error) {
				if issue == 295 {
					return []github.Comment{{ID: 91, Body: specBody}}, nil
				}
				return []github.Comment{{ID: 77, HTMLURL: "https://example.test/process/77", Body: backend.body}}, nil
			}
			root := filepath.Join(t.TempDir(), "managed")
			prepareArgs := append(workspaceBaseArgs(repo, root, "owner-secret"), "--base", base, "--json")
			app, out, errOut := transitionAppWithError(backend)
			if code := app.runWorkflowWorkspace(t.Context(), append([]string{"prepare"}, prepareArgs...)); code != 0 {
				t.Fatalf("legacy prepare code=%d out=%s err=%s", code, out.String(), errOut.String())
			}
			legacy := decodeWorkspaceResult(t, out)
			manager := openWorkspaceManager(t, repo, root)
			local, found, err := manager.Store.Get(t.Context(), legacy.WorkspaceID)
			if err != nil || !found || local.Assignment != nil || local.Portable.Assignment != nil {
				t.Fatalf("legacy local lease=%+v found=%v err=%v", local, found, err)
			}
			catalog := []assignment.ScenarioRef{{SpecID: "SPEC-001", Scenario: "recover packet"}}
			value, err := compileWorkspaceAssignment(t.Context(), backend, "o/r", 177, "PROCESS-004", model.ProcessExecutionChangeBearing,
				backend.body, local, "", "", catalog, false)
			if err != nil {
				t.Fatal(err)
			}
			digest, err := assignment.AssignmentDigest(value)
			if err != nil {
				t.Fatal(err)
			}
			if name == "mismatch" {
				digest = strings.Repeat("f", 64)
			}
			if name == "local-only" {
				if _, err := manager.Store.BindAssignment(t.Context(), legacy.WorkspaceID, value, false, nil); err != nil {
					t.Fatal(err)
				}
			} else {
				generation := uint64(1)
				if name == "generation-mismatch" {
					generation = 2
				}
				remoteLease := local.Portable
				remoteLease.Assignment = &processworkspace.AssignmentBinding{SchemaVersion: value.SchemaVersion, AssignmentID: value.ID,
					Digest: digest, Role: value.Role, BaseRevision: value.BaseRevision, SubjectRevision: value.SubjectRevision, Generation: generation}
				transition, err := model.ApplyTypedTransition(backend.body, model.TransitionRequest{ExpectedType: "PROCESS", ExpectedID: "PROCESS-004", Workspace: &remoteLease})
				if err != nil {
					t.Fatal(err)
				}
				backend.body = transition.Body
				backend.version++
			}
			backend.writes = 0

			issueArgs := append([]string{}, prepareArgs...)
			issueArgs = append(issueArgs, "--issue-assignment", "--proposal", "295")
			app, out, errOut = transitionAppWithError(backend)
			code := app.runWorkflowWorkspace(t.Context(), append([]string{"prepare"}, issueArgs...))
			after, afterFound, getErr := manager.Store.Get(t.Context(), legacy.WorkspaceID)
			if getErr != nil || !afterFound {
				t.Fatalf("local lease disappeared found=%v err=%v", afterFound, getErr)
			}
			if name == "local-only" {
				if code == 0 || backend.writes != 0 || after.Assignment == nil || after.Portable.Assignment == nil {
					t.Fatalf("local-only conflict mutated state code=%d writes=%d lease=%+v out=%s err=%s", code, backend.writes, after, out.String(), errOut.String())
				}
				var result workspaceCommandResult
				if err := json.Unmarshal(out.Bytes(), &result); err != nil || result.Code != "assignment_binding_conflict" {
					t.Fatalf("local-only result=%+v decode=%v", result, err)
				}
				return
			}
			if name == "mismatch" || name == "generation-mismatch" {
				if code == 0 || backend.writes != 0 || after.Assignment != nil || after.Portable.Assignment != nil {
					t.Fatalf("mismatch mutated state code=%d writes=%d lease=%+v out=%s err=%s", code, backend.writes, after, out.String(), errOut.String())
				}
				var result workspaceCommandResult
				if err := json.Unmarshal(out.Bytes(), &result); err != nil || result.Code != "assignment_recovery_mismatch" {
					t.Fatalf("mismatch result=%+v decode=%v", result, err)
				}
				return
			}
			if code != 0 || after.Assignment == nil || after.Portable.Assignment == nil ||
				after.Portable.Assignment.Digest != digest || after.Portable.Assignment.Generation != 1 {
				t.Fatalf("matching recovery code=%d writes=%d lease=%+v out=%s err=%s", code, backend.writes, after, out.String(), errOut.String())
			}
			remote := model.ParseProcessWorkspace("PROCESS-004", "", backend.body)
			if remote.Blocking() || remote.Workspace == nil || !reflect.DeepEqual(remote.Workspace.Assignment, after.Portable.Assignment) {
				t.Fatalf("matching recovery remote=%+v local=%+v", remote, after.Portable.Assignment)
			}
		})
	}
}

type workspaceCASBackend struct {
	fakeGitHubBackend
	body      string
	version   int64
	writes    int
	updateErr error
}

func newWorkspaceCASBackend(body string) *workspaceCASBackend {
	backend := &workspaceCASBackend{body: body, version: 1}
	backend.info = github.BackendInfo{Name: "workspace-cas", Kind: "test"}
	backend.listIssueComments = func(context.Context, string, int) ([]github.Comment, error) {
		return []github.Comment{{ID: 77, HTMLURL: "https://example.test/process/77", Body: backend.body}}, nil
	}
	backend.getIssue = func(_ context.Context, _ string, issue int) (github.Issue, error) {
		switch issue {
		case 296:
			return github.Issue{Number: 296, HTMLURL: "https://github.com/o/r/issues/296", Body: "<!-- issue-spec:issue=design change=portable-assignments version=1 -->\n- Proposal Issue: 295"}, nil
		case 295:
			return github.Issue{Number: 295, HTMLURL: "https://github.com/o/r/issues/295", Body: "<!-- issue-spec:issue=proposal change=portable-assignments version=1 -->"}, nil
		default:
			return github.Issue{Number: issue, HTMLURL: fmt.Sprintf("https://github.com/o/r/issues/%d", issue), Body: "<!-- issue-spec:issue=implement change=portable-assignments version=1 -->\n- Design Issue: 296"}, nil
		}
	}
	return backend
}

func workspaceDesignContext() *assignment.DesignContext {
	return &assignment.DesignContext{
		SourceURL: "https://github.com/o/r/issues/296", ReadMode: assignment.DesignReadModeCompleteIssueBody,
		Invariant: "The assignment follows the authoritative Design.", ApplicableDecisions: []string{"D14"},
		ImplementationDirection: "Preserve the structured design projection exactly.", MustPreserve: []string{"portable authority"},
		MustNot: []string{"reinterpret Design"}, MinimumVerification: []string{"focused tests"},
		ConflictPolicy: assignment.DesignConflictPolicyAuthoritativeStop,
	}
}

func (b *workspaceCASBackend) GetCommentRepresentation(context.Context, string, int64) (github.CommentRepresentation, error) {
	return github.CommentRepresentation{Comment: github.Comment{ID: 77, HTMLURL: "https://example.test/process/77", Body: b.body},
		RepresentationVersion: b.version, Guarantee: github.CommentMutationStrictConditional}, nil
}

func (b *workspaceCASBackend) UpdateCommentConditional(_ context.Context, _ string, _ int64, expected int64, body string) (github.CommentRepresentation, error) {
	if b.updateErr != nil {
		return github.CommentRepresentation{}, b.updateErr
	}
	if expected != b.version {
		return github.CommentRepresentation{}, &github.CommentMutationConflictError{Expected: expected, Current: b.version}
	}
	b.writes++
	b.version++
	b.body = body
	return github.CommentRepresentation{Comment: github.Comment{ID: 77, HTMLURL: "https://example.test/process/77", Body: body}, RepresentationVersion: b.version}, nil
}

func TestWorkflowWorkspaceRouteUsageHelpUnknownAndIntegrateFlags(t *testing.T) {
	app, _, errOut := transitionAppWithError(newWorkspaceCASBackend(workspaceProcessBody(t, model.ProcessExecutionChangeBearing)))
	if code := app.runWorkflowWorkspace(t.Context(), nil); code != 2 || !strings.Contains(errOut.String(), "complete|integrate|reconcile") {
		t.Fatalf("usage code=%d err=%q", code, errOut.String())
	}
	app, _, errOut = transitionAppWithError(newWorkspaceCASBackend(workspaceProcessBody(t, model.ProcessExecutionChangeBearing)))
	if code := app.runWorkflowWorkspace(t.Context(), []string{"unknown"}); code != 2 || !strings.Contains(errOut.String(), "unknown workflow workspace command") {
		t.Fatalf("unknown code=%d err=%q", code, errOut.String())
	}
	app, _, errOut = transitionAppWithError(newWorkspaceCASBackend(workspaceProcessBody(t, model.ProcessExecutionChangeBearing)))
	if code := app.runWorkflowWorkspace(t.Context(), []string{"integrate", "--help"}); code != 0 || strings.Contains(errOut.String(), "reserved") {
		t.Fatalf("integrate help code=%d err=%q", code, errOut.String())
	}
	app, _, errOut = transitionAppWithError(newWorkspaceCASBackend(workspaceProcessBody(t, model.ProcessExecutionChangeBearing)))
	if code := app.runWorkflowWorkspace(t.Context(), []string{"integrate"}); code != 2 || !strings.Contains(errOut.String(), "repo must be owner/name") || strings.Contains(errOut.String(), "reserved") {
		t.Fatalf("integrate required flags code=%d err=%q", code, errOut.String())
	}
}

func TestTopLevelUsageListsWorkspaceIntegrate(t *testing.T) {
	const command = "issue-spec workflow workspace prepare|inspect|complete|integrate|reconcile|cleanup"
	for _, test := range []struct {
		name string
		args []string
	}{
		{name: "no arguments"},
		{name: "top level help", args: []string{"--help"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			if code := Execute(test.args, strings.NewReader(""), &out, &errOut); code != 0 {
				t.Fatalf("Execute(%v) code=%d stderr=%q", test.args, code, errOut.String())
			}
			if !strings.Contains(out.String(), command) {
				t.Fatalf("Execute(%v) top-level usage missing %q:\n%s", test.args, command, out.String())
			}
			if errOut.Len() != 0 {
				t.Fatalf("Execute(%v) wrote stderr: %q", test.args, errOut.String())
			}
		})
	}
}

func TestWorkflowWorkspaceRouteInvokesIntegrationAdapter(t *testing.T) {
	repo, root, backend, base := completedWorkspaceForIntegration(t, "route-owner", "internal/commands/route-integrated.txt")
	app, out, errOut := transitionAppWithError(backend)
	args := append([]string{"integrate"}, workspaceIntegrateArgs(repo, root, "route-owner", base)...)
	if code := app.runWorkflowWorkspace(t.Context(), args); code != 0 {
		t.Fatalf("integrate route code=%d out=%s err=%s", code, out.String(), errOut.String())
	}
	result := decodeWorkspaceResult(t, out)
	if !result.OK || result.State != processworkspace.StateIntegrated || result.IntegrationSHA == "" || strings.Contains(errOut.String(), "reserved") {
		t.Fatalf("integrate route result=%+v err=%q", result, errOut.String())
	}
}

func TestWorkspacePrepareStrictCASIdempotentInspectAndLegacyMigration(t *testing.T) {
	repo, base := workspaceGitRepository(t)
	body := workspaceProcessBody(t, model.ProcessExecutionChangeBearing)
	backend := newWorkspaceCASBackend(body)
	root := filepath.Join(t.TempDir(), "managed")
	args := workspaceBaseArgs(repo, root, "owner-secret")
	args = append(args, "--base", base, "--json")

	app, out, errOut := transitionAppWithError(backend)
	if code := app.runWorkflow(t.Context(), append([]string{"workspace", "prepare"}, args...)); code != 0 {
		t.Fatalf("prepare code=%d out=%s err=%s", code, out.String(), errOut.String())
	}
	first := decodeWorkspaceResult(t, out)
	if !first.OK || first.State != processworkspace.StatePrepared || !first.Registered || first.Remote.Guarantee != github.CommentMutationStrictConditional || !first.Remote.Atomic {
		t.Fatalf("prepare result=%+v", first)
	}
	if strings.Contains(backend.body, first.WorktreePath) || strings.Contains(backend.body, "owner-secret") || strings.Contains(backend.body, "worktree_path") {
		t.Fatalf("remote PROCESS leaked local state:\n%s", backend.body)
	}
	parsed := model.ParseProcessWorkspace("PROCESS-004", "", backend.body)
	if parsed.Blocking() || parsed.Workspace == nil || parsed.Workspace.State != processworkspace.StatePrepared {
		t.Fatalf("legacy Workspace migration=%+v", parsed)
	}

	writes := backend.writes
	app, out, errOut = transitionAppWithError(backend)
	if code := app.runWorkflowWorkspace(t.Context(), append([]string{"prepare"}, args...)); code != 0 {
		t.Fatalf("idempotent prepare code=%d out=%s err=%s", code, out.String(), errOut.String())
	}
	if backend.writes != writes {
		t.Fatalf("idempotent prepare rewrote remote PROCESS: %d -> %d", writes, backend.writes)
	}

	app, out, errOut = transitionAppWithError(backend)
	inspectArgs := append([]string{"inspect"}, workspaceBaseArgs(repo, root, "")...)
	inspectArgs = append(inspectArgs, "--json")
	if code := app.runWorkflowWorkspace(t.Context(), inspectArgs); code != 0 {
		t.Fatalf("inspect code=%d out=%s err=%s", code, out.String(), errOut.String())
	}
	inspected := decodeWorkspaceResult(t, out)
	if inspected.Generation == 0 || inspected.State != processworkspace.StatePrepared || len(inspected.Problems) != 0 || strings.Contains(out.String(), "owner-secret") {
		t.Fatalf("unsafe/unstable inspect=%s", out.String())
	}
}

func TestWorkspacePrepareStrictCASConflictDoesNotReserve(t *testing.T) {
	repo, base := workspaceGitRepository(t)
	backend := newWorkspaceCASBackend(workspaceProcessBody(t, model.ProcessExecutionChangeBearing))
	backend.version = 4
	root := filepath.Join(t.TempDir(), "managed")
	args := append(workspaceBaseArgs(repo, root, "cas-owner"), "--base", base, "--expected-version", "3", "--json")
	app, out, _ := transitionAppWithError(backend)
	if code := app.runWorkflowWorkspace(t.Context(), append([]string{"prepare"}, args...)); code != 1 {
		t.Fatalf("CAS conflict code=%d out=%s", code, out.String())
	}
	manager := openWorkspaceManager(t, repo, root)
	if _, found, err := manager.Store.Get(t.Context(), "ws-process-004"); err != nil || found || backend.writes != 0 {
		t.Fatalf("CAS conflict mutated state: found=%v err=%v writes=%d", found, err, backend.writes)
	}
}

func TestWorkspacePrepareRejectsIndependentProcess(t *testing.T) {
	repo, base := workspaceGitRepository(t)
	body := workspaceProcessBody(t, model.ProcessExecutionChangeBearing)
	body = strings.Replace(body, "### Workspace Management\n\n- managed", "### Workspace Management\n\n- independent", 1)
	backend := newWorkspaceCASBackend(body)
	root := filepath.Join(t.TempDir(), "managed")
	args := append(workspaceBaseArgs(repo, root, "owner-secret"), "--base", base, "--json")
	app, out, errOut := transitionAppWithError(backend)
	if code := app.runWorkflowWorkspace(t.Context(), append([]string{"prepare"}, args...)); code != 1 {
		t.Fatalf("prepare code=%d out=%s err=%s", code, out.String(), errOut.String())
	}
	if backend.writes != 0 || !strings.Contains(out.String(), "workspace_management_independent") {
		t.Fatalf("independent PROCESS was prepared: writes=%d out=%s", backend.writes, out.String())
	}
}

func TestWorkspacePrepareRejectsInvalidManagedOwnershipBeforeReservation(t *testing.T) {
	repo, base := workspaceGitRepository(t)
	backend := newWorkspaceCASBackend(workspaceProcessBody(t, model.ProcessExecutionChangeBearing))
	root := filepath.Join(t.TempDir(), "managed")
	args := append(workspaceBaseArgs(repo, root, "strict-owner"), "--base", base, "--write-ownership", "internal/?.go", "--json")
	app, out, _ := transitionAppWithError(backend)
	if code := app.runWorkflowWorkspace(t.Context(), append([]string{"prepare"}, args...)); code != 1 {
		t.Fatalf("invalid ownership code=%d out=%s", code, out.String())
	}
	result := decodeWorkspaceResult(t, out)
	if result.Code != "reservation_invalid" || backend.writes != 0 {
		t.Fatalf("invalid ownership result=%+v writes=%d", result, backend.writes)
	}
	manager := openWorkspaceManager(t, repo, root)
	if _, found, err := manager.Store.Get(t.Context(), "ws-process-004"); err != nil || found {
		t.Fatalf("invalid ownership created lease: found=%v err=%v", found, err)
	}
}

func TestWorkspacePrepareRejectsBareTrackedDirectoryBeforeMutationAndAllowsExplicitOverride(t *testing.T) {
	repo, _ := workspaceGitRepository(t)
	trackedDir := filepath.Join(repo, "internal", "commands")
	if err := os.MkdirAll(trackedDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(trackedDir, "command.go"), []byte("package commands\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	workspaceGit(t, repo, "add", "--", "internal/commands/command.go")
	workspaceGit(t, repo, "commit", "-s", "-m", "tracked command directory")
	base := workspaceGitOutput(t, repo, "rev-parse", "HEAD")

	body := strings.Replace(workspaceProcessBody(t, model.ProcessExecutionChangeBearing),
		"- internal/commands/**", "- internal/commands", 1)
	if !strings.Contains(body, "- internal/commands\n") {
		t.Fatal("failed to create bare directory declaration fixture")
	}
	backend := newWorkspaceCASBackend(body)
	root := filepath.Join(t.TempDir(), "managed")
	beforeWorktrees := workspaceGitOutput(t, repo, "worktree", "list", "--porcelain")
	args := append(workspaceBaseArgs(repo, root, "tree-owner"), "--base", base, "--json")
	app, out, _ := transitionAppWithError(backend)
	if code := app.runWorkflowWorkspace(t.Context(), append([]string{"prepare"}, args...)); code != 1 {
		t.Fatalf("bare tree prepare code=%d out=%s", code, out.String())
	}
	result := decodeWorkspaceResult(t, out)
	for _, detail := range []string{"PROCESS-004", `"internal/commands"`, base, `"internal/commands/**"`} {
		if !strings.Contains(result.Message, detail) {
			t.Fatalf("bare tree diagnostic missing %q: %+v", detail, result)
		}
	}
	if result.Code != "reservation_invalid" || result.ReconcileRequired || backend.writes != 0 || backend.body != body {
		t.Fatalf("bare tree mutated reservation or remote state: result=%+v writes=%d", result, backend.writes)
	}
	manager := openWorkspaceManager(t, repo, root)
	if _, found, err := manager.Store.Get(t.Context(), "ws-process-004"); err != nil || found {
		t.Fatalf("bare tree created lease: found=%v err=%v", found, err)
	}
	if after := workspaceGitOutput(t, repo, "worktree", "list", "--porcelain"); after != beforeWorktrees {
		t.Fatalf("bare tree mutated worktrees:\nbefore=%s\nafter=%s", beforeWorktrees, after)
	}
	if branch := workspaceGitOutput(t, repo, "branch", "--list", "issue-spec/process-004"); branch != "" {
		t.Fatalf("bare tree created process branch %q", branch)
	}
	if _, err := os.Lstat(filepath.Join(root, "ws-process-004")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("bare tree created workspace path: %v", err)
	}

	overrideArgs := append(workspaceBaseArgs(repo, root, "tree-owner"), "--base", base,
		"--write-ownership", "internal/commands/**", "--json")
	app, out, errOut := transitionAppWithError(backend)
	if code := app.runWorkflowWorkspace(t.Context(), append([]string{"prepare"}, overrideArgs...)); code != 0 {
		t.Fatalf("explicit recursive override code=%d out=%s err=%s", code, out.String(), errOut.String())
	}
	prepared := decodeWorkspaceResult(t, out)
	parsed := model.ParseProcessWorkspace("PROCESS-004", "", backend.body)
	if !prepared.OK || prepared.State != processworkspace.StatePrepared || backend.writes != 1 || parsed.Workspace == nil ||
		!reflect.DeepEqual(parsed.Workspace.WriteOwnership, []string{"internal/commands/**"}) {
		t.Fatalf("explicit recursive override result=%+v writes=%d remote=%+v", prepared, backend.writes, parsed.Workspace)
	}
}

func TestWorkspacePrepareRejectsHistoricalPreparedLeaseBeforeRemoteRecovery(t *testing.T) {
	repo, _ := workspaceGitRepository(t)
	trackedDir := filepath.Join(repo, "internal", "commands")
	if err := os.MkdirAll(trackedDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(trackedDir, "command.go"), []byte("package commands\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	workspaceGit(t, repo, "add", "--", "internal/commands/command.go")
	workspaceGit(t, repo, "commit", "-s", "-m", "tracked command directory")
	base := workspaceGitOutput(t, repo, "rev-parse", "HEAD")

	body := strings.Replace(workspaceProcessBody(t, model.ProcessExecutionChangeBearing),
		"- internal/commands/**", "- internal/commands", 1)
	now := time.Unix(100, 0).UTC()
	remoteLease := processworkspace.PortableLease{
		SchemaVersion: processworkspace.LeaseSchemaVersion, WorkspaceID: "ws-process-004", Repository: "o/r", ProcessID: "PROCESS-004",
		ExecutionClass: processworkspace.ExecutionChangeBearing, Mode: processworkspace.ModeWritable, BaseSHA: base,
		Branch: "issue-spec/process-004", WriteOwnership: []string{"internal/commands"}, RuntimeNamespace: "ws-process-004",
		State: processworkspace.StatePreparing, CreatedAt: now, UpdatedAt: now,
	}
	transition, err := model.ApplyTypedTransition(body, model.TransitionRequest{
		ExpectedType: "PROCESS", ExpectedID: "PROCESS-004", Workspace: &remoteLease,
	})
	if err != nil {
		t.Fatal(err)
	}
	body = transition.Body
	backend := newWorkspaceCASBackend(body)
	root := filepath.Join(t.TempDir(), "managed")
	manager := openWorkspaceManager(t, repo, root)
	historical := processworkspace.LocalLease{
		Portable: remoteLease, IntegrationRoot: manager.IntegrationRoot,
		WorktreePath:  filepath.Join(manager.WorkspaceRoot, "ws-process-004"),
		Owner:         processworkspace.LeaseOwner{CoordinatorID: "historical-coordinator", Token: "historical-owner", AcquiredAt: now},
		LocalRevision: 1,
	}
	historical.Portable.State = processworkspace.StatePrepared
	if _, err := manager.Store.Create(t.Context(), historical); err != nil {
		t.Fatal(err)
	}
	beforeRegistry, err := manager.Store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	args := append(workspaceBaseArgs(repo, root, "historical-owner"), "--base", base, "--json")
	app, out, _ := transitionAppWithError(backend)
	if code := app.runWorkflowWorkspace(t.Context(), append([]string{"prepare"}, args...)); code != 1 {
		t.Fatalf("historical prepare code=%d out=%s", code, out.String())
	}
	result := decodeWorkspaceResult(t, out)
	if result.Code != "reservation_invalid" || result.State != processworkspace.StatePrepared ||
		!result.ReconcileRequired || backend.writes != 0 || backend.body != body {
		t.Fatalf("historical prepare did not fail closed: result=%+v writes=%d", result, backend.writes)
	}
	for _, detail := range []string{"PROCESS-004", `"internal/commands"`, base, `"internal/commands/**"`} {
		if !strings.Contains(result.Message, detail) {
			t.Fatalf("historical diagnostic missing %q: %+v", detail, result)
		}
	}
	afterRegistry, err := manager.Store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	stored, found, err := manager.Store.Get(t.Context(), "ws-process-004")
	if err != nil || !found || stored.Portable.State != processworkspace.StatePrepared ||
		!reflect.DeepEqual(stored.Portable.WriteOwnership, []string{"internal/commands"}) ||
		afterRegistry.Generation != beforeRegistry.Generation {
		t.Fatalf("historical prepare widened or mutated lease: stored=%+v found=%v err=%v generations=%d->%d",
			stored.Portable, found, err, beforeRegistry.Generation, afterRegistry.Generation)
	}
	if _, err := os.Lstat(historical.WorktreePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("historical prepare created missing worktree: %v", err)
	}
}

func TestProcessSectionListIgnoresExamplesAndRejectsDuplicateRealSections(t *testing.T) {
	body := workspaceProcessBody(t, model.ProcessExecutionChangeBearing)
	fenced := strings.Replace(body, "### Write Ownership", "```markdown\n### Write Ownership\n- repository/**\n```\n\n### Write Ownership", 1)
	values, err := processSectionList(fenced, "### Write Ownership")
	if err != nil || !reflect.DeepEqual(values, []string{"internal/commands/**"}) {
		t.Fatalf("fenced example values=%v err=%v", values, err)
	}
	indented := strings.Replace(body, "### Write Ownership", "    ### Write Ownership\n    - repository/**\n\n### Write Ownership", 1)
	values, err = processSectionList(indented, "### Write Ownership")
	if err != nil || !reflect.DeepEqual(values, []string{"internal/commands/**"}) {
		t.Fatalf("indented example values=%v err=%v", values, err)
	}
	duplicate := strings.Replace(body, "### Write Ownership", "### Write Ownership\n\n- first/**\n\n### Write Ownership", 1)
	if _, err := processSectionList(duplicate, "### Write Ownership"); err == nil || !strings.Contains(err.Error(), "multiple") {
		t.Fatalf("duplicate real section err=%v", err)
	}
}

func TestWorkspacePrepareRejectsLaterRemoteStateWithoutLocalLease(t *testing.T) {
	states := []processworkspace.LifecycleState{
		processworkspace.StateWorkerComplete, processworkspace.StateIntegrating, processworkspace.StateIntegrated,
		processworkspace.StateCleanupPending, processworkspace.StateCleaned, processworkspace.StateConflicted,
	}
	for _, lifecycle := range states {
		t.Run(string(lifecycle), func(t *testing.T) {
			repo, base := workspaceGitRepository(t)
			backend := newWorkspaceCASBackend(workspaceProcessBodyWithState(t, base, lifecycle))
			root := filepath.Join(t.TempDir(), "managed")
			args := append(workspaceBaseArgs(repo, root, "recovery-owner"), "--base", base, "--json")
			app, out, _ := transitionAppWithError(backend)
			if code := app.runWorkflowWorkspace(t.Context(), append([]string{"prepare"}, args...)); code != 1 {
				t.Fatalf("later state prepare code=%d out=%s", code, out.String())
			}
			result := decodeWorkspaceResult(t, out)
			if result.Code != "reservation_recovery_required" || !strings.Contains(result.Message, "inspect/reconcile") || backend.writes != 0 {
				t.Fatalf("later state result=%+v writes=%d", result, backend.writes)
			}
			manager := openWorkspaceManager(t, repo, root)
			if _, found, err := manager.Store.Get(t.Context(), "ws-process-004"); err != nil || found {
				t.Fatalf("later state created lease: found=%v err=%v", found, err)
			}
		})
	}
}

func TestWorkspacePrepareNonAtomicRequiresDigestAndReportsPartialFailure(t *testing.T) {
	repo, base := workspaceGitRepository(t)
	body := workspaceProcessBody(t, model.ProcessExecutionChangeBearing)
	current := body
	writes := 0
	plain := transitionFake(body)
	plain.listIssueComments = func(context.Context, string, int) ([]github.Comment, error) {
		return []github.Comment{{ID: 77, Body: current}}, nil
	}
	plain.updateComment = func(_ context.Context, _ string, _ int64, updated string) (github.Comment, error) {
		writes++
		current = updated
		return github.Comment{ID: 77, Body: updated}, nil
	}
	root := filepath.Join(t.TempDir(), "managed")
	baseArgs := workspaceBaseArgs(repo, root, "owner-nonatomic")
	app, out, _ := transitionAppWithError(plain)
	if code := app.runWorkflowWorkspace(t.Context(), append([]string{"prepare"}, append(baseArgs, "--base", base, "--json")...)); code != 1 {
		t.Fatalf("non-atomic prepare without acknowledgement code=%d out=%s", code, out.String())
	}
	manager := openWorkspaceManager(t, repo, root)
	if _, found, err := manager.Store.Get(t.Context(), "ws-process-004"); err != nil || found {
		t.Fatalf("remote precondition failure created reservation: found=%v err=%v", found, err)
	}

	args := append(baseArgs, "--base", base, "--allow-nonatomic", "--expected-digest", bodyDigest(body), "--json")
	app, out, errOut := transitionAppWithError(plain)
	if code := app.runWorkflowWorkspace(t.Context(), append([]string{"prepare"}, args...)); code != 0 {
		t.Fatalf("explicit non-atomic code=%d out=%s err=%s", code, out.String(), errOut.String())
	}
	result := decodeWorkspaceResult(t, out)
	if result.Remote.Atomic || result.Remote.Guarantee != github.CommentMutationNonAtomicSingleWriter || writes != 1 {
		t.Fatalf("non-atomic result=%+v writes=%d", result, writes)
	}

	// A remote failure occurs after the local prepared reservation and is
	// explicitly recoverable through reconcile.
	repo2, base2 := workspaceGitRepository(t)
	body2 := workspaceProcessBody(t, model.ProcessExecutionChangeBearing)
	strict := newWorkspaceCASBackend(body2)
	strict.updateErr = errors.New("remote unavailable")
	root2 := filepath.Join(t.TempDir(), "managed")
	partialArgs := append(workspaceBaseArgs(repo2, root2, "owner-partial"), "--base", base2, "--json")
	app, out, _ = transitionAppWithError(strict)
	if code := app.runWorkflowWorkspace(t.Context(), append([]string{"prepare"}, partialArgs...)); code != 1 {
		t.Fatalf("partial prepare code=%d out=%s", code, out.String())
	}
	partial := decodeWorkspaceResult(t, out)
	if !partial.ReconcileRequired || partial.State != processworkspace.StatePrepared {
		t.Fatalf("partial failure diagnostics=%+v", partial)
	}
	strict.updateErr = nil
	app, out, errOut = transitionAppWithError(strict)
	reconcileArgs := append([]string{"reconcile"}, workspaceBaseArgs(repo2, root2, "owner-partial")...)
	reconcileArgs = append(reconcileArgs, "--json")
	if code := app.runWorkflowWorkspace(t.Context(), reconcileArgs); code != 0 {
		t.Fatalf("reconcile code=%d out=%s err=%s", code, out.String(), errOut.String())
	}
	if parsed := model.ParseProcessWorkspace("PROCESS-004", "", strict.body); parsed.Workspace == nil || parsed.Workspace.State != processworkspace.StatePrepared {
		t.Fatalf("reconcile did not repair remote gap: %+v", parsed)
	}
}

func TestWorkspaceReviewSnapshotCompleteAndUnsafeCleanup(t *testing.T) {
	t.Run("review snapshot", func(t *testing.T) {
		repo, base := workspaceGitRepository(t)
		backend := newWorkspaceCASBackend(workspaceProcessBody(t, model.ProcessExecutionReview))
		root := filepath.Join(t.TempDir(), "managed")
		args := append(workspaceBaseArgs(repo, root, "review-owner"), "--base", base, "--json")
		app, out, errOut := transitionAppWithError(backend)
		if code := app.runWorkflowWorkspace(t.Context(), append([]string{"prepare"}, args...)); code != 0 {
			t.Fatalf("snapshot code=%d out=%s err=%s", code, out.String(), errOut.String())
		}
		result := decodeWorkspaceResult(t, out)
		if result.Mode != processworkspace.ModeSnapshot || result.Branch != "" || result.DetachedRevision != base || result.Head != base {
			t.Fatalf("snapshot result=%+v", result)
		}
	})

	t.Run("verification snapshot rejects completion", func(t *testing.T) {
		repo, base := workspaceGitRepository(t)
		backend := newWorkspaceCASBackend(workspaceProcessBody(t, model.ProcessExecutionVerification))
		root := filepath.Join(t.TempDir(), "managed")
		args := append(workspaceBaseArgs(repo, root, "verify-owner"), "--base", base, "--json")
		app, out, errOut := transitionAppWithError(backend)
		if code := app.runWorkflowWorkspace(t.Context(), append([]string{"prepare"}, args...)); code != 0 {
			t.Fatalf("verification snapshot code=%d out=%s err=%s", code, out.String(), errOut.String())
		}
		prepared := decodeWorkspaceResult(t, out)
		if prepared.Mode != processworkspace.ModeSnapshot || prepared.DetachedRevision != base || prepared.Head != base {
			t.Fatalf("verification snapshot result=%+v", prepared)
		}
		writes := backend.writes
		completeArgs := append([]string{"complete"}, workspaceBaseArgs(repo, root, "verify-owner")...)
		completeArgs = append(completeArgs, "--result-commit", base, "--json")
		app, out, _ = transitionAppWithError(backend)
		if code := app.runWorkflowWorkspace(t.Context(), completeArgs); code != 1 {
			t.Fatalf("snapshot complete code=%d out=%s", code, out.String())
		}
		result := decodeWorkspaceResult(t, out)
		if result.State != processworkspace.StatePrepared || backend.writes != writes {
			t.Fatalf("snapshot completion mutated state: result=%+v writes=%d->%d", result, writes, backend.writes)
		}
	})

	t.Run("orchestration has no checkout", func(t *testing.T) {
		repo, _ := workspaceGitRepository(t)
		backend := newWorkspaceCASBackend(workspaceProcessBody(t, model.ProcessExecutionOrchestration))
		root := filepath.Join(t.TempDir(), "managed")
		args := append(workspaceBaseArgs(repo, root, "orchestration-owner"), "--json")
		app, out, errOut := transitionAppWithError(backend)
		if code := app.runWorkflowWorkspace(t.Context(), append([]string{"prepare"}, args...)); code != 0 {
			t.Fatalf("orchestration code=%d out=%s err=%s", code, out.String(), errOut.String())
		}
		result := decodeWorkspaceResult(t, out)
		if result.Mode != processworkspace.ModeNone || result.WorktreePath != "" || result.Registered || result.Present {
			t.Fatalf("orchestration allocated checkout: %+v", result)
		}
	})

	t.Run("complete one commit", func(t *testing.T) {
		repo, base := workspaceGitRepository(t)
		backend := newWorkspaceCASBackend(workspaceProcessBody(t, model.ProcessExecutionChangeBearing))
		root := filepath.Join(t.TempDir(), "managed")
		args := append(workspaceBaseArgs(repo, root, "complete-owner"), "--base", base, "--json")
		app, out, errOut := transitionAppWithError(backend)
		if code := app.runWorkflowWorkspace(t.Context(), append([]string{"prepare"}, args...)); code != 0 {
			t.Fatalf("prepare code=%d err=%s", code, errOut.String())
		}
		prepared := decodeWorkspaceResult(t, out)
		if err := os.MkdirAll(filepath.Join(prepared.WorktreePath, "internal", "commands"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(prepared.WorktreePath, "internal", "commands", "result.txt"), []byte("result\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		workspaceGit(t, prepared.WorktreePath, "add", "internal/commands/result.txt")
		workspaceGit(t, prepared.WorktreePath, "commit", "-s", "-m", "worker result")
		resultCommit := workspaceGitOutput(t, prepared.WorktreePath, "rev-parse", "HEAD")
		app, out, errOut = transitionAppWithError(backend)
		completeArgs := append([]string{"complete"}, workspaceBaseArgs(repo, root, "complete-owner")...)
		completeArgs = append(completeArgs, "--result-commit", resultCommit, "--json")
		if code := app.runWorkflowWorkspace(t.Context(), completeArgs); code != 0 {
			t.Fatalf("complete code=%d out=%s err=%s", code, out.String(), errOut.String())
		}
		completed := decodeWorkspaceResult(t, out)
		if completed.State != processworkspace.StateWorkerComplete || completed.ResultCommit != resultCommit {
			t.Fatalf("complete result=%+v", completed)
		}
	})

	t.Run("complete rejects signed out-of-scope commit", func(t *testing.T) {
		repo, base := workspaceGitRepository(t)
		backend := newWorkspaceCASBackend(workspaceProcessBody(t, model.ProcessExecutionChangeBearing))
		root := filepath.Join(t.TempDir(), "managed")
		args := append(workspaceBaseArgs(repo, root, "scope-owner"), "--base", base, "--json")
		app, out, errOut := transitionAppWithError(backend)
		if code := app.runWorkflowWorkspace(t.Context(), append([]string{"prepare"}, args...)); code != 0 {
			t.Fatalf("prepare code=%d err=%s", code, errOut.String())
		}
		prepared := decodeWorkspaceResult(t, out)
		if err := os.WriteFile(filepath.Join(prepared.WorktreePath, "outside.txt"), []byte("outside\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		workspaceGit(t, prepared.WorktreePath, "add", "outside.txt")
		workspaceGit(t, prepared.WorktreePath, "commit", "-s", "-m", "out of scope")
		resultCommit := workspaceGitOutput(t, prepared.WorktreePath, "rev-parse", "HEAD")
		writes := backend.writes
		app, out, _ = transitionAppWithError(backend)
		completeArgs := append([]string{"complete"}, workspaceBaseArgs(repo, root, "scope-owner")...)
		completeArgs = append(completeArgs, "--result-commit", resultCommit, "--json")
		if code := app.runWorkflowWorkspace(t.Context(), completeArgs); code != 1 {
			t.Fatalf("out-of-scope complete code=%d out=%s", code, out.String())
		}
		result := decodeWorkspaceResult(t, out)
		if result.State != processworkspace.StatePrepared || backend.writes != writes || !strings.Contains(result.Message, "outside.txt") {
			t.Fatalf("out-of-scope result=%+v writes=%d->%d", result, writes, backend.writes)
		}
	})

	t.Run("unsafe cleanup preserves dirty worktree", func(t *testing.T) {
		repo, base := workspaceGitRepository(t)
		backend := newWorkspaceCASBackend(workspaceProcessBody(t, model.ProcessExecutionChangeBearing))
		root := filepath.Join(t.TempDir(), "managed")
		args := append(workspaceBaseArgs(repo, root, "cleanup-owner"), "--base", base, "--json")
		app, out, _ := transitionAppWithError(backend)
		if code := app.runWorkflowWorkspace(t.Context(), append([]string{"prepare"}, args...)); code != 0 {
			t.Fatalf("prepare code=%d out=%s", code, out.String())
		}
		prepared := decodeWorkspaceResult(t, out)
		dirty := filepath.Join(prepared.WorktreePath, "user-dirty.txt")
		if err := os.WriteFile(dirty, []byte("preserve"), 0o600); err != nil {
			t.Fatal(err)
		}
		app, out, _ = transitionAppWithError(backend)
		cleanupArgs := append([]string{"cleanup"}, workspaceBaseArgs(repo, root, "cleanup-owner")...)
		cleanupArgs = append(cleanupArgs, "--json")
		if code := app.runWorkflowWorkspace(t.Context(), cleanupArgs); code != 1 {
			t.Fatalf("unsafe cleanup code=%d out=%s", code, out.String())
		}
		if _, err := os.Stat(dirty); err != nil {
			t.Fatalf("dirty user file was removed: %v", err)
		}
		manager := openWorkspaceManager(t, repo, root)
		lease, found, err := manager.Store.Get(t.Context(), "ws-process-004")
		if err != nil || !found || lease.Portable.State != processworkspace.StateCleanupPending {
			t.Fatalf("unsafe cleanup lease=%+v found=%v err=%v", lease, found, err)
		}
		if err := os.Remove(dirty); err != nil {
			t.Fatal(err)
		}
		app, out, errOut := transitionAppWithError(backend)
		if code := app.runWorkflowWorkspace(t.Context(), cleanupArgs); code != 0 {
			t.Fatalf("cleanup retry code=%d out=%s err=%s", code, out.String(), errOut.String())
		}
		cleaned := decodeWorkspaceResult(t, out)
		if cleaned.State != processworkspace.StateCleaned {
			t.Fatalf("cleanup retry=%+v", cleaned)
		}
		if got := workspaceGitOutput(t, repo, "rev-parse", "refs/heads/issue-spec/process-004"); got != base {
			t.Fatalf("cleanup changed process branch: got=%s want=%s", got, base)
		}
		writes := backend.writes
		app, out, errOut = transitionAppWithError(backend)
		if code := app.runWorkflowWorkspace(t.Context(), cleanupArgs); code != 0 || backend.writes != writes {
			t.Fatalf("idempotent cleanup code=%d writes=%d->%d out=%s err=%s", code, writes, backend.writes, out.String(), errOut.String())
		}
	})
}

func workspaceProcessBody(t *testing.T, class model.ProcessExecutionClass) string {
	t.Helper()
	body, err := templates.ProcessComment(templates.ProcessCommentOptions{Common: templates.CommonOptions{ID: "PROCESS-004", Status: "in-progress"},
		Input: templates.ProcessInput{Title: "workspace", ParentTask: "TASK-002", ExecutionClass: class,
			WriteOwnership: []string{"internal/commands/**"}, Handoff: "N/A"}})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func workspaceProcessBodyWithState(t *testing.T, base string, lifecycle processworkspace.LifecycleState) string {
	t.Helper()
	body := workspaceProcessBody(t, model.ProcessExecutionChangeBearing)
	now := time.Unix(100, 0).UTC()
	workspace := processworkspace.PortableLease{
		SchemaVersion: processworkspace.LeaseSchemaVersion, WorkspaceID: "ws-process-004", Repository: "o/r", ProcessID: "PROCESS-004",
		ExecutionClass: processworkspace.ExecutionChangeBearing, Mode: processworkspace.ModeWritable, BaseSHA: base,
		Branch: "issue-spec/process-004", WriteOwnership: []string{"internal/commands/**"}, RuntimeNamespace: "ws-process-004",
		State: lifecycle, CreatedAt: now, UpdatedAt: now,
	}
	if lifecycle == processworkspace.StateWorkerComplete || lifecycle == processworkspace.StateIntegrating || lifecycle == processworkspace.StateIntegrated {
		workspace.ResultCommit = strings.Repeat("b", 40)
	}
	if lifecycle == processworkspace.StateIntegrated {
		workspace.IntegrationSHA = strings.Repeat("c", 40)
	}
	transition, err := model.ApplyTypedTransition(body, model.TransitionRequest{ExpectedType: "PROCESS", ExpectedID: "PROCESS-004", Workspace: &workspace})
	if err != nil {
		t.Fatal(err)
	}
	return transition.Body
}

func workspaceBaseArgs(repo, root, ownerToken string) []string {
	args := []string{"--repo", "o/r", "--issue", "177", "--process", "PROCESS-004", "--integration-root", repo, "--workspace-root", root}
	if ownerToken != "" {
		args = append(args, "--owner-token", ownerToken)
	}
	return args
}

func decodeWorkspaceResult(t *testing.T, out *bytes.Buffer) workspaceCommandResult {
	t.Helper()
	var result workspaceCommandResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("decode workspace result: %v\n%s", err, out.String())
	}
	return result
}

func workspaceGitRepository(t *testing.T) (string, string) {
	t.Helper()
	repo := filepath.Join(t.TempDir(), "integration")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	workspaceGit(t, repo, "init", "-b", "main")
	workspaceGit(t, repo, "config", "user.name", "Workspace Test")
	workspaceGit(t, repo, "config", "user.email", "workspace@example.com")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	workspaceGit(t, repo, "add", "README.md")
	workspaceGit(t, repo, "commit", "-s", "-m", "base")
	return repo, workspaceGitOutput(t, repo, "rev-parse", "HEAD")
}

func workspaceGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
}

func workspaceGitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	output, err := command.Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(output))
}

func openWorkspaceManager(t *testing.T, repo, root string) *processworkspace.Manager {
	t.Helper()
	manager, err := processworkspace.OpenManager(t.Context(), repo, root, processworkspace.ManagerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return manager
}
