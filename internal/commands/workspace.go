package commands

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/higress-group/issue-spec/internal/assignment"
	"github.com/higress-group/issue-spec/internal/auth"
	changegraph "github.com/higress-group/issue-spec/internal/change"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/model"
	"github.com/higress-group/issue-spec/internal/processworkspace"
	runnerworkspace "github.com/higress-group/issue-spec/internal/workspace"
)

type workspaceCommandFlags struct {
	repoFlag        *string
	host            *string
	issueFlag       *string
	processID       *string
	integration     *string
	workspaceRoot   *string
	workspaceID     *string
	ownerToken      *string
	expectedVersion *int64
	expectedDigest  *string
	allowNonAtomic  *bool
	jsonOut         *bool
}

type workspaceRemoteTarget struct {
	client      github.Operations
	conditional github.ConditionalCommentBackend
	artifact    model.Artifact
	body        string
	version     int64
	guarantee   github.CommentMutationGuarantee
}

type workspaceRemoteResult struct {
	Action          string                          `json:"action"`
	Atomic          bool                            `json:"atomic"`
	Guarantee       github.CommentMutationGuarantee `json:"guarantee"`
	BeforeDigest    string                          `json:"before_digest"`
	AfterDigest     string                          `json:"after_digest"`
	ObservedVersion int64                           `json:"observed_version,omitempty"`
	CurrentVersion  int64                           `json:"current_version,omitempty"`
}

type workspaceCommandResult struct {
	OK                        bool                                          `json:"ok"`
	Action                    string                                        `json:"action"`
	Code                      string                                        `json:"code,omitempty"`
	Message                   string                                        `json:"message,omitempty"`
	Repo                      string                                        `json:"repo"`
	Issue                     int                                           `json:"issue"`
	ProcessID                 string                                        `json:"process_id"`
	WorkspaceID               string                                        `json:"workspace_id"`
	Generation                uint64                                        `json:"generation"`
	LocalRevision             uint64                                        `json:"local_revision"`
	State                     processworkspace.LifecycleState               `json:"state"`
	ExecutionClass            processworkspace.ExecutionClass               `json:"execution_class"`
	Mode                      processworkspace.WorkspaceMode                `json:"mode"`
	BaseSHA                   string                                        `json:"base_sha,omitempty"`
	Branch                    string                                        `json:"branch,omitempty"`
	DetachedRevision          string                                        `json:"detached_revision,omitempty"`
	ResultCommit              string                                        `json:"result_commit,omitempty"`
	IntegrationSHA            string                                        `json:"integration_sha,omitempty"`
	AcceptedReceiptID         string                                        `json:"accepted_receipt_id,omitempty"`
	AcceptedReceiptDigest     string                                        `json:"accepted_receipt_digest,omitempty"`
	AcceptedReceiptGeneration uint64                                        `json:"accepted_receipt_generation,omitempty"`
	AcceptedReceiptSubmission *processworkspace.RoleOwnedSubmissionEvidence `json:"accepted_receipt_submission,omitempty"`
	RuntimeNamespace          string                                        `json:"runtime_namespace,omitempty"`
	WorktreePath              string                                        `json:"worktree_path,omitempty"`
	Registered                bool                                          `json:"registered"`
	Present                   bool                                          `json:"present"`
	Dirty                     bool                                          `json:"dirty"`
	Head                      string                                        `json:"head,omitempty"`
	GitBranch                 string                                        `json:"git_branch,omitempty"`
	Problems                  []string                                      `json:"problems,omitempty"`
	Remote                    workspaceRemoteResult                         `json:"remote"`
	ReconcileRequired         bool                                          `json:"reconcile_required,omitempty"`
	Assignment                *assignment.Packet                            `json:"assignment,omitempty"`
}

func (a *app) runWorkflowWorkspace(ctx context.Context, args []string) int {
	if len(args) == 0 {
		a.errorf("usage: issue-spec workflow workspace prepare|inspect|complete|integrate|reconcile|cleanup ...\n")
		return 2
	}
	switch args[0] {
	case "prepare":
		return a.runWorkspacePrepare(ctx, args[1:])
	case "inspect":
		return a.runWorkspaceInspect(ctx, args[1:])
	case "complete":
		return a.runWorkspaceComplete(ctx, args[1:])
	case "reconcile":
		return a.runWorkspaceReconcile(ctx, args[1:])
	case "cleanup":
		return a.runWorkspaceCleanup(ctx, args[1:])
	case "integrate":
		return a.runWorkspaceIntegrate(ctx, args[1:])
	default:
		a.errorf("unknown workflow workspace command %q\n", args[0])
		return 2
	}
}

func addWorkspaceCommandFlags(fs *flag.FlagSet) workspaceCommandFlags {
	integrationRoot := strings.TrimSpace(os.Getenv(runnerworkspace.ProcessIntegrationRootEnv))
	if integrationRoot == "" {
		integrationRoot = "."
	}
	workspaceRoot := strings.TrimSpace(os.Getenv(runnerworkspace.ProcessWorkspaceRootEnv))
	return workspaceCommandFlags{
		repoFlag: fs.String("repo", "", "repository owner/name"), host: fs.String("hostname", "github.com", "issue hostname"),
		issueFlag: fs.String("issue", "", "implement issue number or URL"), processID: fs.String("process", "", "PROCESS id"),
		integration: fs.String("integration-root", integrationRoot, "coordinator Git integration root (default: $"+runnerworkspace.ProcessIntegrationRootEnv+")"), workspaceRoot: fs.String("workspace-root", workspaceRoot, "managed linked-worktree root (default: $"+runnerworkspace.ProcessWorkspaceRootEnv+")"),
		workspaceID: fs.String("workspace-id", "", "stable portable workspace id"), ownerToken: fs.String("owner-token", "", "machine-local lease owner token"),
		expectedVersion: fs.Int64("expected-version", 0, "caller-observed PROCESS representation version"),
		expectedDigest:  fs.String("expected-digest", "", "caller-observed PROCESS body SHA-256 digest"),
		allowNonAtomic:  fs.Bool("allow-nonatomic", false, "permit explicit digest-guarded single-writer fallback"), jsonOut: fs.Bool("json", false, "write JSON output"),
	}
}

func (a *app) validateWorkspaceFlags(flags workspaceCommandFlags, requireOwner bool) (string, int, string, bool) {
	repo, ok := a.validateRepo(*flags.repoFlag)
	if !ok {
		return "", 0, "", false
	}
	issue, err := parseIssueFlag(*flags.issueFlag, "issue")
	if err != nil {
		a.errorf("%v\n", err)
		return "", 0, "", false
	}
	processID := strings.TrimSpace(*flags.processID)
	if processID == "" {
		a.errorf("--process is required\n")
		return "", 0, "", false
	}
	if requireOwner && strings.TrimSpace(*flags.ownerToken) == "" {
		a.errorf("--owner-token is required\n")
		return "", 0, "", false
	}
	if *flags.expectedVersion < 0 || (*flags.expectedVersion > 0 && strings.TrimSpace(*flags.expectedDigest) != "") {
		a.errorf("--expected-version must be non-negative and is mutually exclusive with --expected-digest\n")
		return "", 0, "", false
	}
	return repo, issue, processID, true
}

func (a *app) loadWorkspaceRemote(ctx context.Context, flags workspaceCommandFlags, repo string, issue int, processID string) (workspaceRemoteTarget, error) {
	client, _, err := a.clientFor(ctx, *flags.host)
	if err != nil {
		return workspaceRemoteTarget{}, fmt.Errorf("auth required on %s: %w", auth.NormalizeHost(*flags.host), err)
	}
	artifact, body, err := findUniqueTransitionArtifactByID(ctx, client, repo, issue, processID)
	if err != nil {
		return workspaceRemoteTarget{}, err
	}
	target := workspaceRemoteTarget{client: client, artifact: artifact, body: body, guarantee: github.CommentMutationNonAtomicSingleWriter}
	conditional, capabilityErr := github.RequireConditionalCommentBackend(client)
	if capabilityErr == nil {
		observed, observeErr := conditional.GetCommentRepresentation(ctx, repo, artifact.CommentID)
		if observeErr == nil {
			if observed.Comment.ID != artifact.CommentID || model.ParseTypedComment(observed.Comment.Body).ID != processID {
				return workspaceRemoteTarget{}, errors.New("conditional PROCESS observation changed identity")
			}
			target.conditional, target.body, target.version, target.guarantee = conditional, observed.Comment.Body, observed.RepresentationVersion, github.CommentMutationStrictConditional
		} else if !errors.Is(observeErr, github.ErrConditionalCommentMutationUnsupported) {
			return workspaceRemoteTarget{}, observeErr
		}
	} else if !errors.Is(capabilityErr, github.ErrConditionalCommentMutationUnsupported) {
		return workspaceRemoteTarget{}, capabilityErr
	}
	return target, nil
}

func validateWorkspaceWriteBoundary(target workspaceRemoteTarget, flags workspaceCommandFlags) error {
	if target.conditional != nil {
		if *flags.expectedVersion > 0 && target.version != *flags.expectedVersion {
			return &github.CommentMutationConflictError{Expected: *flags.expectedVersion, Current: target.version}
		}
		if !digestMatches(target.body, *flags.expectedDigest) {
			return fmt.Errorf("comment body digest conflict: expected=%s current=%s", normalizeDigest(*flags.expectedDigest), bodyDigest(target.body))
		}
		return nil
	}
	if !*flags.allowNonAtomic || strings.TrimSpace(*flags.expectedDigest) == "" {
		return errors.New("backend lacks CAS; --allow-nonatomic and --expected-digest are both required")
	}
	if !digestMatches(target.body, *flags.expectedDigest) {
		return fmt.Errorf("comment body digest conflict: expected=%s current=%s", normalizeDigest(*flags.expectedDigest), bodyDigest(target.body))
	}
	return nil
}

func applyWorkspaceRemote(ctx context.Context, target workspaceRemoteTarget, repo string, issue int, workspace processworkspace.PortableLease) (workspaceRemoteTarget, workspaceRemoteResult, error) {
	remote := model.ParseProcessWorkspace(workspace.ProcessID, target.artifact.URL, target.body)
	if remote.Blocking() {
		return target, workspaceRemoteResult{}, errors.New("remote PROCESS has invalid Workspace metadata")
	}
	if remote.Workspace != nil {
		if err := validateAcceptedReceiptProjection(processworkspace.PortableLease(*remote.Workspace), workspace); err != nil {
			return target, workspaceRemoteResult{}, err
		}
	}
	transition, err := model.ApplyTypedTransition(target.body, model.TransitionRequest{ExpectedType: "PROCESS", ExpectedID: workspace.ProcessID, Workspace: (*model.ProcessWorkspace)(&workspace)})
	if err != nil {
		return target, workspaceRemoteResult{}, err
	}
	result := workspaceRemoteResult{Action: "unchanged", Atomic: target.conditional != nil, Guarantee: target.guarantee,
		BeforeDigest: bodyDigest(target.body), AfterDigest: bodyDigest(transition.Body), ObservedVersion: target.version, CurrentVersion: target.version}
	if !transition.Changed {
		return target, result, nil
	}
	if target.conditional != nil {
		updated, err := target.conditional.UpdateCommentConditional(ctx, repo, target.artifact.CommentID, target.version, transition.Body)
		if err != nil {
			return target, result, err
		}
		target.body, target.version = updated.Comment.Body, updated.RepresentationVersion
		result.Action, result.AfterDigest, result.CurrentVersion = "updated", bodyDigest(target.body), target.version
		return target, result, nil
	}
	updated, err := target.client.UpdateComment(ctx, repo, target.artifact.CommentID, transition.Body)
	if err != nil {
		return target, result, err
	}
	observed, err := observeCommentByID(ctx, target.client, repo, issue, target.artifact.CommentID)
	if err != nil {
		return target, result, fmt.Errorf("non-atomic post-write observation: %w", err)
	}
	if observed.Body != transition.Body {
		return target, result, errors.New("non-atomic PROCESS update was overwritten or did not persist")
	}
	target.body = observed.Body
	result.Action, result.AfterDigest = "updated", bodyDigest(observed.Body)
	if updated.HTMLURL != "" {
		target.artifact.URL = updated.HTMLURL
	}
	return target, result, nil
}

func validateAcceptedReceiptProjection(before, after processworkspace.PortableLease) error {
	if before.AcceptedReceiptID == "" {
		return nil
	}
	if before.AcceptedReceiptID != after.AcceptedReceiptID || before.AcceptedReceiptDigest != after.AcceptedReceiptDigest ||
		before.AcceptedReceiptGeneration != after.AcceptedReceiptGeneration ||
		!sameAcceptedReceiptSubmission(before.AcceptedReceiptSubmission, after.AcceptedReceiptSubmission) {
		return errors.New("remote accepted implementation receipt authority cannot be cleared or replaced")
	}
	return nil
}

func (a *app) runWorkspacePrepare(ctx context.Context, args []string) int {
	fs := newFlagSet("workflow workspace prepare", a.err)
	flags := addWorkspaceCommandFlags(fs)
	base := fs.String("base", "", "exact integrated base revision")
	branch := fs.String("branch", "", "writable process branch")
	coordinator := fs.String("coordinator", "workspace-cli", "machine-local coordinator identity")
	issueAssignment := fs.Bool("issue-assignment", false, "compile and persist a role assignment before returning")
	assignmentFile := fs.String("assignment-file", "", "validated portable assignment JSON (required when PROCESS fields cannot fully express the role packet)")
	assignmentDiffBase := fs.String("assignment-diff-base", "", "exact review diff base used to derive code authors and changed paths")
	proposalFlag := fs.String("proposal", "", "proposal issue number or URL used to resolve confirmed covered SPEC scenarios")
	assignmentOut := fs.String("assignment-out", "", "write the assignment packet atomically to an absolute path outside the worktree")
	redispatch := fs.Bool("redispatch-assignment", false, "explicitly advance an existing assignment generation")
	expectedAssignmentGeneration := fs.Uint64("expected-assignment-generation", 0, "current assignment generation required for redispatch")
	var ownership stringListFlag
	fs.Var(&ownership, "write-ownership", "repository-relative owned path; repeat or comma-separate")
	if ok, code := a.parseFlagSet(fs, args); !ok {
		return code
	}
	repo, issue, processID, ok := a.validateWorkspaceFlags(flags, true)
	if !ok {
		return 2
	}
	if (*assignmentOut != "" || *assignmentFile != "" || *assignmentDiffBase != "" || *proposalFlag != "" || *redispatch) && !*issueAssignment || *expectedAssignmentGeneration != 0 && !*redispatch {
		a.errorf("assignment input/output and redispatch flags require --issue-assignment; --expected-assignment-generation requires --redispatch-assignment\n")
		return 2
	}
	if *redispatch && *expectedAssignmentGeneration == 0 {
		a.errorf("--redispatch-assignment requires --expected-assignment-generation\n")
		return 2
	}
	proposalIssue := 0
	if *issueAssignment {
		var proposalErr error
		proposalIssue, proposalErr = parseIssueFlag(*proposalFlag, "proposal")
		if proposalErr != nil {
			a.errorf("%v\n", proposalErr)
			return 2
		}
	}
	target, err := a.loadWorkspaceRemote(ctx, flags, repo, issue, processID)
	if err != nil {
		return a.workspaceError(workspaceCommandResult{Repo: repo, Issue: issue, ProcessID: processID}, "process_observation_failed", err, *flags.jsonOut)
	}
	if err := validateWorkspaceWriteBoundary(target, flags); err != nil {
		return a.workspaceError(workspaceCommandResult{Repo: repo, Issue: issue, ProcessID: processID}, "remote_precondition_failed", err, *flags.jsonOut)
	}
	class := model.ParseProcessExecutionClass(processID, target.artifact.URL, target.body)
	if class.Blocking() {
		return a.workspaceError(workspaceCommandResult{Repo: repo, Issue: issue, ProcessID: processID}, "execution_class_invalid", errors.New(model.CanonicalDiagnosticStrings(class.Diagnostics)[0]), *flags.jsonOut)
	}
	if code, managementErr := managedWorkspaceLifecycleProblem(target, processID); managementErr != nil {
		return a.workspaceError(workspaceCommandResult{Repo: repo, Issue: issue, ProcessID: processID}, code, managementErr, *flags.jsonOut)
	}
	mode := modeForExecutionClass(class.Class)
	remoteWorkspace := model.ParseProcessWorkspace(processID, target.artifact.URL, target.body)
	if remoteWorkspace.Blocking() {
		return a.workspaceError(workspaceCommandResult{Repo: repo, Issue: issue, ProcessID: processID}, "workspace_metadata_invalid", errors.New(model.CanonicalDiagnosticStrings(remoteWorkspace.Diagnostics)[0]), *flags.jsonOut)
	}
	portable, err := preparePortableLease(repo, processID, class.Class, mode, target.body, remoteWorkspace.Workspace, *flags.workspaceID, *base, *branch, ownership.Values())
	if err != nil {
		return a.workspaceError(workspaceCommandResult{Repo: repo, Issue: issue, ProcessID: processID}, "reservation_invalid", err, *flags.jsonOut)
	}
	if err := processworkspace.ValidateManagedOwnership(portable.WriteOwnership, portable.SharedTouchpoints); err != nil {
		return a.workspaceError(workspaceCommandResult{Repo: repo, Issue: issue, ProcessID: processID, WorkspaceID: portable.WorkspaceID}, "reservation_invalid", err, *flags.jsonOut)
	}
	manager, err := processworkspace.OpenManager(ctx, *flags.integration, *flags.workspaceRoot, processworkspace.ManagerOptions{})
	if err != nil {
		return a.workspaceError(workspaceCommandResult{Repo: repo, Issue: issue, ProcessID: processID, WorkspaceID: portable.WorkspaceID}, "manager_open_failed", err, *flags.jsonOut)
	}
	local := processworkspace.LocalLease{Portable: portable, IntegrationRoot: manager.IntegrationRoot,
		Owner: processworkspace.LeaseOwner{CoordinatorID: strings.TrimSpace(*coordinator),
			Token: strings.TrimSpace(*flags.ownerToken), PID: os.Getpid(), AcquiredAt: time.Now().UTC()}, LocalRevision: 1}
	existingLocal, localFound, err := manager.Store.Get(ctx, portable.WorkspaceID)
	if err != nil {
		return a.workspaceError(workspaceCommandResult{Repo: repo, Issue: issue, ProcessID: processID, WorkspaceID: portable.WorkspaceID}, "reservation_observation_failed", err, *flags.jsonOut)
	}
	var scenarioCatalog []assignment.ScenarioRef
	var preflightAssignment *assignment.Assignment
	if localFound {
		remoteBinding, localBinding := portable.Assignment, existingLocal.Portable.Assignment
		if remoteBinding != nil && localBinding != nil && !reflect.DeepEqual(remoteBinding, localBinding) {
			return a.workspaceError(workspaceResult(ctx, manager, processworkspace.Inspection{Lease: existingLocal}, repo, issue, processID, "prepare", workspaceRemoteResult{}),
				"assignment_binding_conflict", errors.New("remote and local assignment bindings differ; inspect both reservations and reconcile explicitly"), *flags.jsonOut)
		}
		if remoteBinding == nil && localBinding != nil {
			return a.workspaceError(workspaceResult(ctx, manager, processworkspace.Inspection{Lease: existingLocal}, repo, issue, processID, "prepare", workspaceRemoteResult{}),
				"assignment_binding_conflict", errors.New("local assignment binding has no matching remote binding; inspect and reconcile explicitly"), *flags.jsonOut)
		}
		if remoteBinding != nil && localBinding == nil && !*issueAssignment {
			return a.workspaceError(workspaceResult(ctx, manager, processworkspace.Inspection{Lease: existingLocal}, repo, issue, processID, "prepare", workspaceRemoteResult{}),
				"assignment_recovery_required", errors.New("assignment binding exists on only one side; rerun with explicit --issue-assignment and matching structured input to reconcile"), *flags.jsonOut)
		}
	} else if portable.Assignment != nil && !*issueAssignment {
		return a.workspaceError(workspaceCommandResult{Repo: repo, Issue: issue, ProcessID: processID, WorkspaceID: portable.WorkspaceID,
			State: portable.State}, "assignment_recovery_required",
			errors.New("remote PROCESS has an assignment binding but the local assignment is absent; rerun with explicit --issue-assignment and matching structured input"), *flags.jsonOut)
	}
	if portable.Assignment != nil && (!localFound || existingLocal.Portable.Assignment == nil) {
		if *redispatch {
			return a.workspaceError(workspaceCommandResult{Repo: repo, Issue: issue, ProcessID: processID, WorkspaceID: portable.WorkspaceID,
				State: portable.State}, "assignment_recovery_required", errors.New("recover the exact remote assignment before redispatching it"), *flags.jsonOut)
		}
		scenarioCatalog, err = loadCoveredScenarioCatalog(ctx, target.client, repo, proposalIssue, target.body)
		if err != nil {
			return a.workspaceError(workspaceCommandResult{Repo: repo, Issue: issue, ProcessID: processID, WorkspaceID: portable.WorkspaceID,
				State: portable.State}, "assignment_scenarios_invalid", err, *flags.jsonOut)
		}
		compileLease := local
		compileLease.Portable = portable
		value, compileErr := compileWorkspaceAssignment(ctx, target.client, repo, issue, processID, class.Class, target.body, compileLease, *assignmentFile, *assignmentDiffBase, scenarioCatalog, false)
		if compileErr != nil {
			return a.workspaceError(workspaceCommandResult{Repo: repo, Issue: issue, ProcessID: processID, WorkspaceID: portable.WorkspaceID,
				State: portable.State}, "assignment_recovery_mismatch", compileErr, *flags.jsonOut)
		}
		recoveryGeneration := portable.Assignment.Generation
		if localFound {
			recoveryGeneration = 1
		}
		if localFound && portable.Assignment.Generation != recoveryGeneration {
			return a.workspaceError(workspaceResult(ctx, manager, processworkspace.Inspection{Lease: existingLocal}, repo, issue, processID, "prepare", workspaceRemoteResult{}),
				"assignment_recovery_mismatch", errors.New("existing legacy local lease can recover only the first assignment generation; inspect and reconcile explicitly"), *flags.jsonOut)
		}
		if bindingErr := validateCompiledAssignmentBinding(value, recoveryGeneration, *portable.Assignment); bindingErr != nil {
			return a.workspaceError(workspaceCommandResult{Repo: repo, Issue: issue, ProcessID: processID, WorkspaceID: portable.WorkspaceID,
				State: portable.State}, "assignment_recovery_mismatch", bindingErr, *flags.jsonOut)
		}
		preflightAssignment = &value
	}
	remoteLaterState := remoteWorkspace.Workspace != nil && portable.State != processworkspace.StatePreparing && portable.State != processworkspace.StatePrepared
	if remoteLaterState {
		result := workspaceCommandResult{Repo: repo, Issue: issue, ProcessID: processID, WorkspaceID: portable.WorkspaceID, State: portable.State}
		message := fmt.Sprintf("remote Workspace state %s cannot be prepared again; run workspace inspect/reconcile on the original coordinator or cleanup before preparing again", portable.State)
		if localFound {
			result = workspaceResult(ctx, manager, processworkspace.Inspection{Lease: existingLocal}, repo, issue, processID, "prepare", workspaceRemoteResult{})
		} else {
			message = fmt.Sprintf("remote Workspace state %s requires its existing local lease; run workspace inspect/reconcile on the original coordinator or cleanup before preparing again", portable.State)
		}
		return a.workspaceError(result, "reservation_recovery_required", errors.New(message), *flags.jsonOut)
	}
	var inspection processworkspace.Inspection
	if mode == processworkspace.ModeNone {
		inspection, err = prepareNoCheckout(ctx, manager, local)
	} else {
		if localFound && existingLocal.Portable.State == processworkspace.StatePrepared && sameWorkspaceReservation(existingLocal, local) {
			inspection, err = manager.Inspect(ctx, portable.WorkspaceID)
		} else {
			inspection, err = manager.Prepare(ctx, processworkspace.PrepareRequest{Lease: local})
		}
	}
	if err != nil {
		code := "prepare_failed"
		if errors.Is(err, processworkspace.ErrAmbiguousOwnership) {
			code = "reservation_invalid"
		}
		return a.workspaceLocalFailure(ctx, manager, inspection, repo, issue, processID, code, err, *flags.jsonOut)
	}
	var packet *assignment.Packet
	if *issueAssignment {
		if scenarioCatalog == nil {
			var catalogErr error
			scenarioCatalog, catalogErr = loadCoveredScenarioCatalog(ctx, target.client, repo, proposalIssue, target.body)
			if catalogErr != nil {
				return a.workspaceLocalFailure(ctx, manager, inspection, repo, issue, processID, "assignment_scenarios_invalid", catalogErr, *flags.jsonOut)
			}
		}
		var value assignment.Assignment
		if preflightAssignment != nil {
			value = *preflightAssignment
		} else {
			var compileErr error
			value, compileErr = compileWorkspaceAssignment(ctx, target.client, repo, issue, processID, class.Class, target.body, inspection.Lease, *assignmentFile, *assignmentDiffBase, scenarioCatalog, *redispatch)
			if compileErr != nil {
				return a.workspaceLocalFailure(ctx, manager, inspection, repo, issue, processID, "assignment_invalid", compileErr, *flags.jsonOut)
			}
		}
		var expected *uint64
		if *redispatch {
			expected = expectedAssignmentGeneration
		}
		issued, valuePacket, issueErr := manager.IssueAssignment(ctx, processworkspace.AssignmentRequest{WorkspaceID: portable.WorkspaceID,
			Assignment: value, Redispatch: *redispatch, ExpectedAssignmentGeneration: expected})
		if issueErr != nil {
			return a.workspaceLocalFailure(ctx, manager, issued, repo, issue, processID, "assignment_issuance_failed", issueErr, *flags.jsonOut)
		}
		inspection = issued
		packet = &valuePacket
	}
	updatedTarget, remoteResult, err := applyWorkspaceRemote(ctx, target, repo, issue, inspection.Lease.Portable)
	_ = updatedTarget
	if err != nil {
		result := workspaceResult(ctx, manager, inspection, repo, issue, processID, "prepared-local", remoteResult)
		result.ReconcileRequired = true
		return a.workspaceError(result, "remote_workspace_update_failed", err, *flags.jsonOut)
	}
	result := workspaceResult(ctx, manager, inspection, repo, issue, processID, "prepared", remoteResult)
	result.Assignment = packet
	if packet != nil && *assignmentOut != "" {
		if err := writeAssignmentPacket(*assignmentOut, inspection.Lease.WorktreePath, *packet); err != nil {
			return a.workspaceError(result, "assignment_output_failed", err, *flags.jsonOut)
		}
	}
	return a.outputWorkspace(result, *flags.jsonOut)
}

func compileWorkspaceAssignment(ctx context.Context, backend changegraph.Backend, repo string, issue int, processID string, class model.ProcessExecutionClass, body string, lease processworkspace.LocalLease, assignmentFile, diffBase string, scenarioCatalog []assignment.ScenarioRef, redispatch bool) (assignment.Assignment, error) {
	typed := model.ParseTypedComment(body)
	if len(typed.Errors) > 0 {
		return assignment.Assignment{}, fmt.Errorf("PROCESS assignment input is invalid: %s", strings.Join(typed.Errors, "; "))
	}
	input := assignment.ProcessInput{}
	if typed.Assignment != nil {
		input = *typed.Assignment
	}
	if strings.TrimSpace(assignmentFile) != "" {
		payload, err := os.ReadFile(assignmentFile)
		if err != nil {
			return assignment.Assignment{}, fmt.Errorf("read assignment file: %w", err)
		}
		value, err := assignment.ParseAssignmentJSON(payload)
		if err != nil {
			return assignment.Assignment{}, err
		}
		if err := validateAssignmentForLease(value, repo, issue, processID, class, lease, redispatch); err != nil {
			return assignment.Assignment{}, err
		}
		if input.DesignContext != nil && !reflect.DeepEqual(value.DesignContext, input.DesignContext) {
			return assignment.Assignment{}, errors.New("assignment file and PROCESS design_context differ")
		}
		if err := bindCanonicalDesignContext(ctx, backend, repo, issue, value.Role, value.DesignContext); err != nil {
			return assignment.Assignment{}, err
		}
		if value.Role == assignment.RoleReview {
			authors, scope, err := deriveReviewAssignment(ctx, lease.IntegrationRoot, value.Review.DiffBaseRevision, lease.Portable.DetachedRevision)
			if err != nil {
				return assignment.Assignment{}, err
			}
			if !reflect.DeepEqual(value.Review.Authors, authors) || !reflect.DeepEqual(value.Review.Scope, scope) {
				return assignment.Assignment{}, errors.New("review assignment file authors and scope must exactly match the compiler-derived Git revision range")
			}
		}
		if err := validateAssignmentScenarios(value.Scenarios, scenarioCatalog); err != nil {
			return assignment.Assignment{}, err
		}
		return value, nil
	}
	scenarios := append([]assignment.ScenarioRef(nil), input.ScenarioSelectors...)
	if len(scenarios) == 0 {
		scenarios = append([]assignment.ScenarioRef(nil), scenarioCatalog...)
	}
	if err := validateAssignmentScenarios(scenarios, scenarioCatalog); err != nil {
		return assignment.Assignment{}, err
	}
	generation := uint64(1)
	assignmentID := lease.Portable.WorkspaceID + "-assignment-1"
	if lease.Portable.Assignment != nil {
		generation = lease.Portable.Assignment.Generation
		assignmentID = lease.Portable.Assignment.AssignmentID
		if redispatch {
			generation++
			assignmentID = fmt.Sprintf("%s-assignment-%d", lease.Portable.WorkspaceID, generation)
		}
	}
	dependencies, err := processSectionList(body, "### Dependencies")
	if err != nil {
		return assignment.Assignment{}, err
	}
	handoff, err := processSectionText(body, "### Handoff")
	if err != nil {
		return assignment.Assignment{}, err
	}
	value := assignment.Assignment{SchemaVersion: assignment.AssignmentSchemaVersion, ID: assignmentID, Repository: repo, Issue: int64(issue), ProcessID: processID,
		Scenarios: scenarios, Dependencies: dependencies, Handoff: handoff, DesignContext: input.DesignContext,
		Policy: assignment.Policy{RequireExactRevision: true, MaxResultItems: 64}, ResultSchemaVersion: assignment.ReceiptSchemaVersion}
	switch class {
	case model.ProcessExecutionChangeBearing:
		objective := strings.TrimSpace(input.Objective)
		if objective == "" {
			return assignment.Assignment{}, errors.New("implementation assignment requires structured objective in `### Assignment` or --assignment-file")
		}
		commit := assignment.CommitPolicy{RequireSingleCommit: true, RequireDCO: true}
		if input.CommitPolicy != nil {
			commit = *input.CommitPolicy
		}
		value.Role, value.BaseRevision = assignment.RoleImplementation, lease.Portable.BaseSHA
		value.Implementation = &assignment.ImplementationPayload{Objective: objective, Branch: lease.Portable.Branch,
			WriteOwnership: append([]string(nil), lease.Portable.WriteOwnership...), SharedTouchpoints: append([]string(nil), lease.Portable.SharedTouchpoints...),
			Commit: commit, Generators: append([]assignment.GeneratorPolicy(nil), input.Generators...), FocusedTests: append([]assignment.TestSelector(nil), input.RequiredTests...)}
	case model.ProcessExecutionReview:
		authors, scope, err := deriveReviewAssignment(ctx, lease.IntegrationRoot, diffBase, lease.Portable.DetachedRevision)
		if err != nil {
			return assignment.Assignment{}, err
		}
		value.Role, value.SubjectRevision = assignment.RoleReview, lease.Portable.DetachedRevision
		value.Review = &assignment.ReviewPayload{SnapshotRevision: lease.Portable.DetachedRevision, DiffBaseRevision: strings.TrimSpace(diffBase), Authors: authors, Scope: scope}
	case model.ProcessExecutionVerification:
		value.Role, value.SubjectRevision = assignment.RoleVerification, lease.Portable.DetachedRevision
		value.Verification = &assignment.VerificationPayload{SubjectRevision: lease.Portable.DetachedRevision,
			RequiredTests: append([]assignment.TestSelector(nil), input.RequiredTests...), RequiredChecks: append([]assignment.CheckSelector(nil), input.RequiredChecks...)}
	default:
		return assignment.Assignment{}, fmt.Errorf("execution class %s does not issue a role assignment", class)
	}
	if err := bindCanonicalDesignContext(ctx, backend, repo, issue, value.Role, value.DesignContext); err != nil {
		return assignment.Assignment{}, err
	}
	if err := value.Validate(); err != nil {
		return assignment.Assignment{}, err
	}
	return value, nil
}

func bindCanonicalDesignContext(ctx context.Context, backend changegraph.Backend, repo string, issue int, role assignment.Role, design *assignment.DesignContext) error {
	if role == assignment.RoleVerification {
		if design != nil {
			return errors.New("verification assignment must not carry design_context")
		}
		return nil
	}
	if design == nil {
		return errors.New("implementation and review assignments require design_context")
	}
	if backend == nil {
		return errors.New("derive canonical Design source: issue backend is required")
	}
	located, err := changegraph.LocateFromImplement(ctx, backend, repo, issue)
	if err != nil {
		return fmt.Errorf("derive canonical Design source from Implement issue: %w", err)
	}
	if strings.TrimSpace(located.Design.URL) == "" {
		return errors.New("canonical Design issue has no source URL")
	}
	if design.SourceURL != located.Design.URL {
		return fmt.Errorf("design_context.source_url %q does not match canonical Design URL %q", design.SourceURL, located.Design.URL)
	}
	return nil
}

func validateCompiledAssignmentBinding(value assignment.Assignment, generation uint64, binding processworkspace.AssignmentBinding) error {
	digest, err := assignment.AssignmentDigest(value)
	if err != nil {
		return err
	}
	if binding.SchemaVersion != value.SchemaVersion || binding.AssignmentID != value.ID || binding.Digest != digest ||
		binding.Role != value.Role || binding.BaseRevision != value.BaseRevision || binding.SubjectRevision != value.SubjectRevision || binding.Generation != generation || generation == 0 {
		return errors.New("compiled assignment does not exactly match the authoritative remote binding")
	}
	return nil
}

func loadCoveredScenarioCatalog(ctx context.Context, client github.Operations, repo string, proposalIssue int, processBody string) ([]assignment.ScenarioRef, error) {
	covered, err := processSectionList(processBody, "### Covers")
	if err != nil {
		return nil, err
	}
	wanted := map[string]bool{}
	for _, id := range covered {
		if strings.HasPrefix(id, "SPEC-") {
			wanted[id] = true
		}
	}
	if len(wanted) == 0 {
		return nil, errors.New("assignment issuance requires at least one SPEC id in PROCESS `### Covers`")
	}
	comments, err := client.ListIssueComments(ctx, repo, proposalIssue)
	if err != nil {
		return nil, fmt.Errorf("load proposal SPEC scenarios: %w", err)
	}
	found := map[string]bool{}
	var result []assignment.ScenarioRef
	for _, comment := range comments {
		typed := model.ParseTypedComment(comment.Body)
		if typed.Type != "SPEC" || !wanted[typed.ID] {
			continue
		}
		if found[typed.ID] {
			return nil, fmt.Errorf("proposal contains multiple comments for covered %s", typed.ID)
		}
		found[typed.ID] = true
		if len(typed.Errors) > 0 || typed.Status != "confirmed" {
			return nil, fmt.Errorf("covered %s must be a canonical confirmed SPEC", typed.ID)
		}
		if diagnostics := model.SpecBodyErrors(model.LogicalBody(comment.Body)); len(diagnostics) > 0 {
			return nil, fmt.Errorf("covered %s is not canonical: %s", typed.ID, strings.Join(diagnostics, "; "))
		}
		scenarios := scenarioHeadings(comment.Body)
		if len(scenarios) == 0 {
			return nil, fmt.Errorf("covered %s has no canonical scenarios", typed.ID)
		}
		for _, scenario := range scenarios {
			result = append(result, assignment.ScenarioRef{SpecID: typed.ID, Scenario: scenario})
		}
	}
	for id := range wanted {
		if !found[id] {
			return nil, fmt.Errorf("covered %s was not found in proposal issue %d", id, proposalIssue)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].SpecID == result[j].SpecID {
			return result[i].Scenario < result[j].Scenario
		}
		return result[i].SpecID < result[j].SpecID
	})
	return result, nil
}

func scenarioHeadings(body string) []string {
	lines := strings.Split(model.LogicalBody(body), "\n")
	fence, width := byte(0), 0
	var result []string
	for _, line := range lines {
		marker, length, suffix, isFence := markdownFence(line)
		if fence != 0 {
			if isFence && marker == fence && length >= width && strings.TrimSpace(suffix) == "" {
				fence, width = 0, 0
			}
			continue
		}
		if isFence {
			fence, width = marker, length
			continue
		}
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "### Scenario:") {
			title := strings.TrimSpace(strings.TrimPrefix(trimmed, "### Scenario:"))
			if title != "" {
				result = append(result, title)
			}
		}
	}
	return result
}

func validateAssignmentScenarios(selected, catalog []assignment.ScenarioRef) error {
	if len(catalog) == 0 {
		return errors.New("proposal scenario catalog is empty")
	}
	allowed := map[assignment.ScenarioRef]bool{}
	for _, scenario := range catalog {
		allowed[scenario] = true
	}
	if len(selected) == 0 {
		return errors.New("assignment has no scenarios")
	}
	for _, scenario := range selected {
		if !allowed[scenario] {
			return fmt.Errorf("assignment scenario %s/%q is not a confirmed covered proposal scenario", scenario.SpecID, scenario.Scenario)
		}
	}
	return nil
}

func deriveReviewAssignment(ctx context.Context, integrationRoot, diffBase, subject string) ([]string, []string, error) {
	diffBase = strings.TrimSpace(diffBase)
	if !fullRevision(diffBase) {
		return nil, nil, errors.New("review assignment requires --assignment-diff-base with a full Git object id")
	}
	if err := exec.CommandContext(ctx, "git", "-C", integrationRoot, "merge-base", "--is-ancestor", diffBase, subject).Run(); err != nil {
		return nil, nil, errors.New("review assignment diff base must be an ancestor of the exact subject revision")
	}
	rangeSpec := diffBase + ".." + subject
	authorOutput, err := exec.CommandContext(ctx, "git", "-C", integrationRoot, "log", "--format=%an <%ae>", rangeSpec).Output()
	if err != nil {
		return nil, nil, fmt.Errorf("derive review authors from exact revision range: %w", err)
	}
	scopeOutput, err := exec.CommandContext(ctx, "git", "-C", integrationRoot, "diff", "--name-only", "--diff-filter=ACDMRT", diffBase, subject).Output()
	if err != nil {
		return nil, nil, fmt.Errorf("derive review scope from exact revision range: %w", err)
	}
	authors := uniqueNonEmptyLines(string(authorOutput))
	scope := uniqueNonEmptyLines(string(scopeOutput))
	if len(authors) == 0 || len(scope) == 0 {
		return nil, nil, errors.New("review revision range must contain at least one commit author and changed path")
	}
	return authors, scope, nil
}

func uniqueNonEmptyLines(value string) []string {
	seen := map[string]struct{}{}
	for _, line := range strings.Split(value, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			seen[line] = struct{}{}
		}
	}
	result := make([]string, 0, len(seen))
	for line := range seen {
		result = append(result, line)
	}
	sort.Strings(result)
	return result
}

func fullRevision(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, char := range value {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')) {
			return false
		}
	}
	return true
}

func validateAssignmentForLease(value assignment.Assignment, repo string, issue int, processID string, class model.ProcessExecutionClass, lease processworkspace.LocalLease, redispatch bool) error {
	wantRole := assignment.RoleImplementation
	wantBase, wantSubject := lease.Portable.BaseSHA, ""
	switch class {
	case model.ProcessExecutionReview:
		wantRole, wantBase, wantSubject = assignment.RoleReview, "", lease.Portable.DetachedRevision
	case model.ProcessExecutionVerification:
		wantRole, wantBase, wantSubject = assignment.RoleVerification, "", lease.Portable.DetachedRevision
	case model.ProcessExecutionChangeBearing:
	default:
		return fmt.Errorf("execution class %s does not issue a role assignment", class)
	}
	if value.Repository != repo || value.Issue != int64(issue) || value.ProcessID != processID || value.Role != wantRole ||
		value.BaseRevision != wantBase || value.SubjectRevision != wantSubject {
		return errors.New("assignment file repository, issue, PROCESS, role, or exact revision differs from the workspace lease")
	}
	if value.Role == assignment.RoleImplementation && (value.Implementation.Branch != lease.Portable.Branch ||
		!reflect.DeepEqual(value.Implementation.WriteOwnership, lease.Portable.WriteOwnership) ||
		!reflect.DeepEqual(value.Implementation.SharedTouchpoints, lease.Portable.SharedTouchpoints)) {
		return errors.New("implementation assignment branch or ownership differs from the workspace lease")
	}
	if lease.Portable.Assignment != nil {
		if !redispatch && value.ID != lease.Portable.Assignment.AssignmentID {
			return errors.New("assignment file id differs from the existing binding")
		}
		if redispatch && value.ID == lease.Portable.Assignment.AssignmentID {
			return errors.New("redispatch assignment file must use a distinct assignment id")
		}
	}
	return nil
}

func processSectionText(body, heading string) (string, error) {
	logical := model.LogicalBody(body)
	lines := strings.Split(logical, "\n")
	inside, headings := false, 0
	var content []string
	for _, line := range lines {
		if line == heading {
			headings++
			if headings > 1 {
				return "", fmt.Errorf("PROCESS has multiple `%s` sections", heading)
			}
			inside = true
			continue
		}
		if inside && strings.HasPrefix(line, "#") {
			inside = false
		}
		if inside {
			content = append(content, line)
		}
	}
	return strings.TrimSpace(strings.Join(content, "\n")), nil
}

func writeAssignmentPacket(path, worktree string, packet assignment.Packet) error {
	if path == "-" || !filepath.IsAbs(path) {
		return errors.New("--assignment-out must be an absolute file path and cannot be '-'")
	}
	if err := packet.Validate(); err != nil {
		return err
	}
	clean := filepath.Clean(path)
	parent, err := filepath.EvalSymlinks(filepath.Dir(clean))
	if err != nil {
		return fmt.Errorf("resolve assignment output parent: %w", err)
	}
	resolved := filepath.Join(parent, filepath.Base(clean))
	if worktree != "" {
		canonicalWorktree, err := filepath.EvalSymlinks(worktree)
		if err != nil {
			return fmt.Errorf("resolve worktree: %w", err)
		}
		relative, err := filepath.Rel(canonicalWorktree, resolved)
		if err != nil || relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return errors.New("--assignment-out must be outside the managed worktree")
		}
	}
	if info, err := os.Lstat(resolved); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return errors.New("--assignment-out must name a regular non-symlink file")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	payload, err := json.MarshalIndent(packet, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	temp, err := os.CreateTemp(parent, ".issue-spec-assignment-*")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tempName)
		}
	}()
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(payload); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempName, resolved); err != nil {
		return err
	}
	cleanup = false
	return os.Chmod(resolved, 0o600)
}

func preparePortableLease(repo, processID string, class model.ProcessExecutionClass, mode processworkspace.WorkspaceMode, body string, existing *model.ProcessWorkspace, workspaceID, base, branch string, ownership []string) (processworkspace.PortableLease, error) {
	if existing != nil {
		lease := processworkspace.PortableLease(*existing)
		if workspaceID != "" && workspaceID != lease.WorkspaceID || base != "" && !strings.EqualFold(base, lease.BaseSHA) || branch != "" && branch != lease.Branch {
			return processworkspace.PortableLease{}, errors.New("requested reservation differs from existing PROCESS Workspace")
		}
		return lease, nil
	}
	if workspaceID == "" {
		workspaceID = "ws-" + strings.ToLower(processID)
	}
	if len(ownership) == 0 {
		var err error
		ownership, err = processSectionList(body, "### Write Ownership")
		if err != nil {
			return processworkspace.PortableLease{}, err
		}
	}
	normalized, err := processworkspace.NormalizeOwnership(ownership)
	if err != nil {
		return processworkspace.PortableLease{}, err
	}
	now := time.Now().UTC()
	lease := processworkspace.PortableLease{SchemaVersion: processworkspace.LeaseSchemaVersion, WorkspaceID: workspaceID, Repository: repo,
		ProcessID: processID, ExecutionClass: processworkspace.ExecutionClass(class), Mode: mode, WriteOwnership: normalized,
		RuntimeNamespace: workspaceID, State: processworkspace.StatePreparing, CreatedAt: now, UpdatedAt: now}
	switch mode {
	case processworkspace.ModeWritable:
		if branch == "" {
			branch = "issue-spec/" + strings.ToLower(processID)
		}
		lease.BaseSHA, lease.Branch = strings.TrimSpace(base), branch
	case processworkspace.ModeSnapshot:
		lease.BaseSHA, lease.DetachedRevision = strings.TrimSpace(base), strings.TrimSpace(base)
	case processworkspace.ModeNone:
		lease.RuntimeNamespace = ""
	}
	if err := lease.Validate(); err != nil {
		return processworkspace.PortableLease{}, err
	}
	return lease, nil
}

func modeForExecutionClass(class model.ProcessExecutionClass) processworkspace.WorkspaceMode {
	switch class {
	case model.ProcessExecutionChangeBearing:
		return processworkspace.ModeWritable
	case model.ProcessExecutionReview, model.ProcessExecutionVerification:
		return processworkspace.ModeSnapshot
	default:
		return processworkspace.ModeNone
	}
}

func processSectionList(body, heading string) ([]string, error) {
	logical := model.LogicalBody(body)
	lines := strings.Split(logical, "\n")
	inside, headings := false, 0
	fence, fenceLength := byte(0), 0
	var values []string
	for _, line := range lines {
		marker, length, suffix, isFence := markdownFence(line)
		if fence != 0 {
			if isFence && marker == fence && length >= fenceLength && strings.TrimSpace(suffix) == "" {
				fence, fenceLength = 0, 0
			}
			continue
		}
		if isFence {
			fence, fenceLength = marker, length
			continue
		}
		if line == heading {
			headings++
			if headings > 1 {
				return nil, fmt.Errorf("PROCESS has multiple `%s` sections", heading)
			}
			inside = true
			continue
		}
		if inside && strings.HasPrefix(line, "#") {
			inside = false
			continue
		}
		if inside && strings.HasPrefix(line, "- ") {
			value := strings.TrimSpace(strings.TrimPrefix(line, "- "))
			if value != "" && !strings.EqualFold(value, "N/A") {
				values = append(values, value)
			}
		}
	}
	return values, nil
}

func markdownFence(line string) (byte, int, string, bool) {
	indent := 0
	for indent < len(line) && indent < 4 && line[indent] == ' ' {
		indent++
	}
	if indent > 3 || indent >= len(line) || (line[indent] != '`' && line[indent] != '~') {
		return 0, 0, "", false
	}
	marker := line[indent]
	end := indent
	for end < len(line) && line[end] == marker {
		end++
	}
	if end-indent < 3 {
		return 0, 0, "", false
	}
	return marker, end - indent, line[end:], true
}

func prepareNoCheckout(ctx context.Context, manager *processworkspace.Manager, lease processworkspace.LocalLease) (processworkspace.Inspection, error) {
	lease.Portable.State = processworkspace.StatePrepared
	created, err := manager.Store.Create(ctx, lease)
	if errors.Is(err, processworkspace.ErrLeaseExists) {
		existing, found, getErr := manager.Store.Get(ctx, lease.Portable.WorkspaceID)
		if getErr != nil || !found {
			return processworkspace.Inspection{}, errors.Join(err, getErr)
		}
		if !sameWorkspaceReservation(existing, lease) {
			return processworkspace.Inspection{Lease: existing}, errors.New("workspace id belongs to another reservation")
		}
		created = existing
		err = nil
	}
	return processworkspace.Inspection{Lease: created}, err
}

func sameWorkspaceReservation(left, right processworkspace.LocalLease) bool {
	a, b := left.Portable, right.Portable
	for _, lease := range []*processworkspace.PortableLease{&a, &b} {
		lease.State, lease.ResultCommit, lease.IntegrationSHA = "", "", ""
		lease.CreatedAt, lease.UpdatedAt, lease.RetentionExpiresAt = time.Time{}, time.Time{}, time.Time{}
		lease.Assignment = nil
	}
	return reflect.DeepEqual(a, b) && left.IntegrationRoot == right.IntegrationRoot && left.Owner.Token == right.Owner.Token
}

func (a *app) runWorkspaceInspect(ctx context.Context, args []string) int {
	fs := newFlagSet("workflow workspace inspect", a.err)
	flags := addWorkspaceCommandFlags(fs)
	if ok, code := a.parseFlagSet(fs, args); !ok {
		return code
	}
	repo, issue, processID, ok := a.validateWorkspaceFlags(flags, false)
	if !ok {
		return 2
	}
	target, err := a.loadWorkspaceRemote(ctx, flags, repo, issue, processID)
	if err != nil {
		return a.workspaceError(workspaceCommandResult{Repo: repo, Issue: issue, ProcessID: processID}, "process_observation_failed", err, *flags.jsonOut)
	}
	manager, err := processworkspace.OpenManager(ctx, *flags.integration, *flags.workspaceRoot, processworkspace.ManagerOptions{})
	if err != nil {
		return a.workspaceError(workspaceCommandResult{Repo: repo, Issue: issue, ProcessID: processID}, "manager_open_failed", err, *flags.jsonOut)
	}
	workspaceID := workspaceIDFromTarget(target, processID, *flags.workspaceID)
	inspection, err := inspectWorkspace(ctx, manager, workspaceID)
	remote := workspaceRemoteResult{Action: "observed", Atomic: target.conditional != nil, Guarantee: target.guarantee, BeforeDigest: bodyDigest(target.body), AfterDigest: bodyDigest(target.body), ObservedVersion: target.version, CurrentVersion: target.version}
	result := workspaceResult(ctx, manager, inspection, repo, issue, processID, "inspected", remote)
	appendRemoteWorkspaceProblems(&result, target.body, inspection.Lease.Portable, processID)
	if err != nil {
		return a.workspaceError(result, "inspect_failed", err, *flags.jsonOut)
	}
	return a.outputWorkspace(result, *flags.jsonOut)
}

func inspectWorkspace(ctx context.Context, manager *processworkspace.Manager, workspaceID string) (processworkspace.Inspection, error) {
	lease, found, err := manager.Store.Get(ctx, workspaceID)
	if err != nil || !found {
		if err == nil {
			err = processworkspace.ErrLeaseNotFound
		}
		return processworkspace.Inspection{Lease: lease}, err
	}
	if lease.Portable.Mode == processworkspace.ModeNone {
		return processworkspace.Inspection{Lease: lease}, nil
	}
	return manager.Inspect(ctx, workspaceID)
}

func (a *app) runWorkspaceComplete(ctx context.Context, args []string) int {
	fs := newFlagSet("workflow workspace complete", a.err)
	flags := addWorkspaceCommandFlags(fs)
	resultCommit := fs.String("result-commit", "", "exact worker result commit")
	resultFile := fs.String("result-file", "", "absolute path to a sealed implementation receipt")
	if ok, code := a.parseFlagSet(fs, args); !ok {
		return code
	}
	repo, issue, processID, ok := a.validateWorkspaceFlags(flags, true)
	commitProvided, fileProvided := strings.TrimSpace(*resultCommit) != "", strings.TrimSpace(*resultFile) != ""
	if !ok || commitProvided == fileProvided {
		if ok {
			a.errorf("exactly one of --result-file or --result-commit is required\n")
		}
		return 2
	}
	var receipt *assignment.Receipt
	if fileProvided {
		value, err := readWorkspaceResultFile(*resultFile)
		if err != nil {
			return a.workspaceError(workspaceCommandResult{Repo: repo, Issue: issue, ProcessID: processID}, "result_file_invalid", err, *flags.jsonOut)
		}
		receipt = &value
	}
	return a.runWorkspaceLocalRemoteMutation(ctx, flags, repo, issue, processID, "complete", func(ctx context.Context, manager *processworkspace.Manager, target workspaceRemoteTarget, workspaceID string) (processworkspace.Inspection, error) {
		local, found, err := manager.Store.Get(ctx, workspaceID)
		if err != nil || !found {
			if err == nil {
				err = processworkspace.ErrLeaseNotFound
			}
			return processworkspace.Inspection{Lease: local}, err
		}
		remote := model.ParseProcessWorkspace(processID, target.artifact.URL, target.body)
		if remote.Blocking() || remote.Workspace == nil {
			return processworkspace.Inspection{Lease: local}, errors.New("remote PROCESS lacks one valid authoritative Workspace reservation")
		}
		if err := validateCompletionConvergence(processworkspace.PortableLease(*remote.Workspace), local, repo); err != nil {
			return processworkspace.Inspection{Lease: local}, err
		}
		return manager.Complete(ctx, processworkspace.CompleteRequest{WorkspaceID: workspaceID,
			OwnerToken: strings.TrimSpace(*flags.ownerToken), ResultCommit: strings.TrimSpace(*resultCommit), Receipt: receipt})
	})
}

func roleOwnedSubmissionEvidence(agent string, _ writerSession) processworkspace.RoleOwnedSubmissionEvidence {
	return processworkspace.RoleOwnedSubmissionEvidence{Agent: strings.TrimSpace(agent), Assurance: assignment.AssuranceSelfReported}
}

func sameAcceptedReceiptSubmission(left, right *processworkspace.RoleOwnedSubmissionEvidence) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Agent == right.Agent && left.Assurance == right.Assurance
}

func readWorkspaceResultFile(path string) (assignment.Receipt, error) {
	path = strings.TrimSpace(path)
	if path == "-" || !filepath.IsAbs(path) {
		return assignment.Receipt{}, errors.New("--result-file must be an absolute regular file path and cannot be '-'")
	}
	info, err := os.Lstat(filepath.Clean(path))
	if err != nil {
		return assignment.Receipt{}, fmt.Errorf("inspect result file: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return assignment.Receipt{}, errors.New("--result-file must name a regular non-symlink file")
	}
	payload, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return assignment.Receipt{}, fmt.Errorf("read result file: %w", err)
	}
	value, err := assignment.ParseReceiptJSON(payload)
	if err != nil {
		return assignment.Receipt{}, err
	}
	if value.Role != assignment.RoleImplementation {
		return assignment.Receipt{}, errors.New("--result-file must contain an implementation receipt")
	}
	return value, nil
}

func validateCompletionConvergence(remote processworkspace.PortableLease, local processworkspace.LocalLease, repository string) error {
	portable := local.Portable
	immutableEqual := remote.SchemaVersion == portable.SchemaVersion && remote.WorkspaceID == portable.WorkspaceID && remote.Repository == portable.Repository &&
		remote.Repository == repository && remote.ProcessID == portable.ProcessID && remote.ExecutionClass == portable.ExecutionClass && remote.Mode == portable.Mode &&
		remote.BaseSHA == portable.BaseSHA && remote.Branch == portable.Branch && remote.DetachedRevision == portable.DetachedRevision &&
		slices.Equal(remote.WriteOwnership, portable.WriteOwnership) && slices.Equal(remote.SharedTouchpoints, portable.SharedTouchpoints) &&
		remote.IntegrationOwner == portable.IntegrationOwner && remote.RuntimeNamespace == portable.RuntimeNamespace &&
		slices.Equal(remote.RuntimeResources, portable.RuntimeResources) && remote.CreatedAt.Equal(portable.CreatedAt) &&
		remote.RetentionExpiresAt.Equal(portable.RetentionExpiresAt) && samePortableAssignmentBinding(remote.Assignment, portable.Assignment)
	if !immutableEqual {
		return errors.New("remote PROCESS Workspace differs from the authoritative local reservation or assignment binding")
	}
	if remote.ResultCommit != "" && !strings.EqualFold(remote.ResultCommit, portable.ResultCommit) ||
		remote.IntegrationSHA != "" && !strings.EqualFold(remote.IntegrationSHA, portable.IntegrationSHA) {
		return errors.New("remote PROCESS Workspace carries conflicting result or integration evidence")
	}
	if remote.AcceptedReceiptID != "" && (remote.AcceptedReceiptID != portable.AcceptedReceiptID ||
		remote.AcceptedReceiptDigest != portable.AcceptedReceiptDigest || remote.AcceptedReceiptGeneration != portable.AcceptedReceiptGeneration ||
		!sameAcceptedReceiptSubmission(remote.AcceptedReceiptSubmission, portable.AcceptedReceiptSubmission)) {
		return errors.New("remote PROCESS Workspace carries conflicting accepted receipt authority")
	}
	rank := map[processworkspace.LifecycleState]int{
		processworkspace.StatePrepared:       0,
		processworkspace.StateWorkerComplete: 1,
		processworkspace.StateIntegrating:    2,
		processworkspace.StateIntegrated:     3,
	}
	remoteRank, remoteOK := rank[remote.State]
	localRank, localOK := rank[portable.State]
	if !remoteOK || !localOK || remoteRank > localRank {
		return fmt.Errorf("remote/local completion lifecycle convergence is unsafe: remote=%s local=%s", remote.State, portable.State)
	}
	return nil
}

type workspaceMutation func(context.Context, *processworkspace.Manager, workspaceRemoteTarget, string) (processworkspace.Inspection, error)

func (a *app) runWorkspaceLocalRemoteMutation(ctx context.Context, flags workspaceCommandFlags, repo string, issue int, processID, action string, mutate workspaceMutation) int {
	target, err := a.loadWorkspaceRemote(ctx, flags, repo, issue, processID)
	if err != nil {
		return a.workspaceError(workspaceCommandResult{Repo: repo, Issue: issue, ProcessID: processID}, "process_observation_failed", err, *flags.jsonOut)
	}
	if err := validateWorkspaceWriteBoundary(target, flags); err != nil {
		return a.workspaceError(workspaceCommandResult{Repo: repo, Issue: issue, ProcessID: processID}, "remote_precondition_failed", err, *flags.jsonOut)
	}
	if code, managementErr := managedWorkspaceLifecycleProblem(target, processID); managementErr != nil {
		return a.workspaceError(workspaceCommandResult{Repo: repo, Issue: issue, ProcessID: processID}, code, managementErr, *flags.jsonOut)
	}
	manager, err := processworkspace.OpenManager(ctx, *flags.integration, *flags.workspaceRoot, processworkspace.ManagerOptions{})
	if err != nil {
		return a.workspaceError(workspaceCommandResult{Repo: repo, Issue: issue, ProcessID: processID}, "manager_open_failed", err, *flags.jsonOut)
	}
	workspaceID := workspaceIDFromTarget(target, processID, *flags.workspaceID)
	inspection, err := mutate(ctx, manager, target, workspaceID)
	if err != nil {
		return a.workspaceLocalFailure(ctx, manager, inspection, repo, issue, processID, action+"_failed", err, *flags.jsonOut)
	}
	_, remoteResult, err := applyWorkspaceRemote(ctx, target, repo, issue, inspection.Lease.Portable)
	if err != nil {
		result := workspaceResult(ctx, manager, inspection, repo, issue, processID, action+"-local", remoteResult)
		result.ReconcileRequired = true
		return a.workspaceError(result, "remote_workspace_update_failed", err, *flags.jsonOut)
	}
	return a.outputWorkspace(workspaceResult(ctx, manager, inspection, repo, issue, processID, action, remoteResult), *flags.jsonOut)
}

// managedWorkspaceLifecycleProblem rejects operations which allocate or mutate
// a managed workspace lease. Cleanup intentionally does not call this helper:
// an owner must be able to remove a historical managed lease after a PROCESS is
// changed to independent.
func managedWorkspaceLifecycleProblem(target workspaceRemoteTarget, processID string) (string, error) {
	management := model.ParseProcessWorkspaceManagement(processID, target.artifact.URL, target.body)
	if management.Blocking() {
		return "workspace_management_invalid", errors.New(model.CanonicalDiagnosticStrings(management.Diagnostics)[0])
	}
	if management.Explicit && management.Management == model.ProcessWorkspaceIndependent {
		return "workspace_management_independent", errors.New("independent PROCESS cannot enter the managed workspace lifecycle")
	}
	return "", nil
}

func (a *app) runWorkspaceReconcile(ctx context.Context, args []string) int {
	fs := newFlagSet("workflow workspace reconcile", a.err)
	flags := addWorkspaceCommandFlags(fs)
	if ok, code := a.parseFlagSet(fs, args); !ok {
		return code
	}
	repo, issue, processID, ok := a.validateWorkspaceFlags(flags, true)
	if !ok {
		return 2
	}
	return a.runWorkspaceLocalRemoteMutation(ctx, flags, repo, issue, processID, "reconciled", func(ctx context.Context, manager *processworkspace.Manager, _ workspaceRemoteTarget, workspaceID string) (processworkspace.Inspection, error) {
		lease, err := ownedWorkspaceLease(ctx, manager, workspaceID, *flags.ownerToken)
		if err != nil {
			return processworkspace.Inspection{Lease: lease}, err
		}
		if lease.Portable.Mode == processworkspace.ModeNone {
			return processworkspace.Inspection{Lease: lease}, nil
		}
		return manager.Reconcile(ctx, workspaceID)
	})
}

func (a *app) runWorkspaceCleanup(ctx context.Context, args []string) int {
	fs := newFlagSet("workflow workspace cleanup", a.err)
	flags := addWorkspaceCommandFlags(fs)
	if ok, code := a.parseFlagSet(fs, args); !ok {
		return code
	}
	repo, issue, processID, ok := a.validateWorkspaceFlags(flags, true)
	if !ok {
		return 2
	}
	target, err := a.loadWorkspaceRemote(ctx, flags, repo, issue, processID)
	if err != nil {
		return a.workspaceError(workspaceCommandResult{Repo: repo, Issue: issue, ProcessID: processID}, "process_observation_failed", err, *flags.jsonOut)
	}
	if err := validateWorkspaceWriteBoundary(target, flags); err != nil {
		return a.workspaceError(workspaceCommandResult{Repo: repo, Issue: issue, ProcessID: processID}, "remote_precondition_failed", err, *flags.jsonOut)
	}
	manager, err := processworkspace.OpenManager(ctx, *flags.integration, *flags.workspaceRoot, processworkspace.ManagerOptions{})
	if err != nil {
		return a.workspaceError(workspaceCommandResult{Repo: repo, Issue: issue, ProcessID: processID}, "manager_open_failed", err, *flags.jsonOut)
	}
	workspaceID := workspaceIDFromTarget(target, processID, *flags.workspaceID)
	lease, err := ownedWorkspaceLease(ctx, manager, workspaceID, *flags.ownerToken)
	if err != nil {
		return a.workspaceError(workspaceResult(ctx, manager, processworkspace.Inspection{Lease: lease}, repo, issue, processID, "cleanup", workspaceRemoteResult{}), "cleanup_owner_mismatch", err, *flags.jsonOut)
	}
	if lease.Portable.State != processworkspace.StateCleaned {
		if lease.Portable.State != processworkspace.StateCleanupPending {
			lease, err = manager.Store.Update(ctx, workspaceID, func(current *processworkspace.LocalLease) error {
				if current.Owner.Token != strings.TrimSpace(*flags.ownerToken) {
					return errors.New("lease owner token changed during cleanup")
				}
				current.Portable.State = processworkspace.StateCleanupPending
				return nil
			})
		}
		if err != nil {
			return a.workspaceError(workspaceResult(ctx, manager, processworkspace.Inspection{Lease: lease}, repo, issue, processID, "cleanup", workspaceRemoteResult{}), "cleanup_pending_failed", err, *flags.jsonOut)
		}
		var remotePending workspaceRemoteResult
		target, remotePending, err = applyWorkspaceRemote(ctx, target, repo, issue, lease.Portable)
		if err != nil {
			result := workspaceResult(ctx, manager, processworkspace.Inspection{Lease: lease}, repo, issue, processID, "cleanup-pending-local", remotePending)
			result.ReconcileRequired = true
			return a.workspaceError(result, "remote_cleanup_pending_failed", err, *flags.jsonOut)
		}
	}
	inspection, err := manager.Cleanup(ctx, workspaceID, *flags.ownerToken)
	if err != nil {
		return a.workspaceLocalFailure(ctx, manager, inspection, repo, issue, processID, "cleanup_failed", err, *flags.jsonOut)
	}
	_, remoteResult, err := applyWorkspaceRemote(ctx, target, repo, issue, inspection.Lease.Portable)
	if err != nil {
		result := workspaceResult(ctx, manager, inspection, repo, issue, processID, "cleaned-local", remoteResult)
		result.ReconcileRequired = true
		return a.workspaceError(result, "remote_cleanup_update_failed", err, *flags.jsonOut)
	}
	return a.outputWorkspace(workspaceResult(ctx, manager, inspection, repo, issue, processID, "cleaned", remoteResult), *flags.jsonOut)
}

func ownedWorkspaceLease(ctx context.Context, manager *processworkspace.Manager, workspaceID, ownerToken string) (processworkspace.LocalLease, error) {
	lease, found, err := manager.Store.Get(ctx, workspaceID)
	if err != nil {
		return lease, err
	}
	if !found {
		return lease, processworkspace.ErrLeaseNotFound
	}
	if strings.TrimSpace(ownerToken) == "" || ownerToken != lease.Owner.Token {
		return lease, errors.New("lease owner token mismatch")
	}
	return lease, nil
}

func workspaceIDFromTarget(target workspaceRemoteTarget, processID, requested string) string {
	if requested != "" {
		return requested
	}
	parsed := model.ParseProcessWorkspace(processID, target.artifact.URL, target.body)
	if parsed.Workspace != nil {
		return parsed.Workspace.WorkspaceID
	}
	return "ws-" + strings.ToLower(processID)
}

func appendRemoteWorkspaceProblems(result *workspaceCommandResult, body string, local processworkspace.PortableLease, processID string) {
	remote := model.ParseProcessWorkspace(processID, "", body)
	if remote.Blocking() {
		result.Problems = append(result.Problems, model.CanonicalDiagnosticStrings(remote.Diagnostics)...)
		return
	}
	if remote.Workspace == nil {
		result.Problems = append(result.Problems, "remote PROCESS has no Workspace metadata")
		return
	}
	if !reflect.DeepEqual(processworkspace.PortableLease(*remote.Workspace), local) {
		result.Problems = append(result.Problems, "remote PROCESS Workspace differs from local registry")
	}
}

func workspaceResult(ctx context.Context, manager *processworkspace.Manager, inspection processworkspace.Inspection, repo string, issue int, processID, action string, remote workspaceRemoteResult) workspaceCommandResult {
	registry, _ := manager.Store.Load(ctx)
	lease := inspection.Lease
	return workspaceCommandResult{OK: true, Action: action, Repo: repo, Issue: issue, ProcessID: processID, WorkspaceID: lease.Portable.WorkspaceID,
		Generation: registry.Generation, LocalRevision: lease.LocalRevision, State: lease.Portable.State, ExecutionClass: lease.Portable.ExecutionClass,
		Mode: lease.Portable.Mode, BaseSHA: lease.Portable.BaseSHA, Branch: lease.Portable.Branch, DetachedRevision: lease.Portable.DetachedRevision,
		ResultCommit: lease.Portable.ResultCommit, IntegrationSHA: lease.Portable.IntegrationSHA,
		AcceptedReceiptID: acceptedReceiptIDForResult(lease), AcceptedReceiptDigest: lease.Portable.AcceptedReceiptDigest,
		AcceptedReceiptGeneration: lease.Portable.AcceptedReceiptGeneration,
		AcceptedReceiptSubmission: lease.Portable.AcceptedReceiptSubmission, RuntimeNamespace: lease.Portable.RuntimeNamespace,
		WorktreePath: lease.WorktreePath, Registered: inspection.Registered, Present: inspection.Present, Dirty: inspection.Dirty,
		Head: inspection.Head, GitBranch: inspection.Branch, Problems: append([]string(nil), inspection.Problems...), Remote: remote}
}

func acceptedReceiptIDForResult(lease processworkspace.LocalLease) string {
	if lease.Portable.AcceptedReceiptID != "" {
		return lease.Portable.AcceptedReceiptID
	}
	return lease.AcceptedReceiptID
}

func (a *app) workspaceLocalFailure(ctx context.Context, manager *processworkspace.Manager, inspection processworkspace.Inspection, repo string, issue int, processID, code string, err error, jsonOut bool) int {
	if inspection.Lease.Portable.WorkspaceID != "" {
		if current, found, getErr := manager.Store.Get(ctx, inspection.Lease.Portable.WorkspaceID); getErr == nil && found {
			inspection.Lease = current
		}
	}
	result := workspaceResult(ctx, manager, inspection, repo, issue, processID, "failed", workspaceRemoteResult{})
	result.ReconcileRequired = inspection.Lease.Portable.WorkspaceID != ""
	return a.workspaceError(result, code, err, jsonOut)
}

func (a *app) workspaceError(result workspaceCommandResult, code string, err error, jsonOut bool) int {
	result.OK, result.Code, result.Message = false, code, err.Error()
	if jsonOut {
		_ = a.outputJSON(result)
	} else {
		a.errorf("%s: %v\n", code, err)
	}
	return 1
}

func (a *app) outputWorkspace(result workspaceCommandResult, jsonOut bool) int {
	if jsonOut {
		return a.outputJSON(result)
	}
	fmt.Fprintf(a.out, "%s workspace %s process=%s state=%s generation=%d\n", result.Action, result.WorkspaceID, result.ProcessID, result.State, result.Generation)
	if len(result.Problems) > 0 {
		fmt.Fprintf(a.out, "problems: %s\n", strings.Join(result.Problems, "; "))
	}
	return 0
}
