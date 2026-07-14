package commands

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/higress-group/issue-spec/internal/auth"
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
	OK                bool                            `json:"ok"`
	Action            string                          `json:"action"`
	Code              string                          `json:"code,omitempty"`
	Message           string                          `json:"message,omitempty"`
	Repo              string                          `json:"repo"`
	Issue             int                             `json:"issue"`
	ProcessID         string                          `json:"process_id"`
	WorkspaceID       string                          `json:"workspace_id"`
	Generation        uint64                          `json:"generation"`
	LocalRevision     uint64                          `json:"local_revision"`
	State             processworkspace.LifecycleState `json:"state"`
	ExecutionClass    processworkspace.ExecutionClass `json:"execution_class"`
	Mode              processworkspace.WorkspaceMode  `json:"mode"`
	BaseSHA           string                          `json:"base_sha,omitempty"`
	Branch            string                          `json:"branch,omitempty"`
	DetachedRevision  string                          `json:"detached_revision,omitempty"`
	ResultCommit      string                          `json:"result_commit,omitempty"`
	IntegrationSHA    string                          `json:"integration_sha,omitempty"`
	RuntimeNamespace  string                          `json:"runtime_namespace,omitempty"`
	WorktreePath      string                          `json:"worktree_path,omitempty"`
	Registered        bool                            `json:"registered"`
	Present           bool                            `json:"present"`
	Dirty             bool                            `json:"dirty"`
	Head              string                          `json:"head,omitempty"`
	GitBranch         string                          `json:"git_branch,omitempty"`
	Problems          []string                        `json:"problems,omitempty"`
	Remote            workspaceRemoteResult           `json:"remote"`
	ReconcileRequired bool                            `json:"reconcile_required,omitempty"`
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

func (a *app) runWorkspacePrepare(ctx context.Context, args []string) int {
	fs := newFlagSet("workflow workspace prepare", a.err)
	flags := addWorkspaceCommandFlags(fs)
	base := fs.String("base", "", "exact integrated base revision")
	branch := fs.String("branch", "", "writable process branch")
	coordinator := fs.String("coordinator", "workspace-cli", "machine-local coordinator identity")
	var ownership stringListFlag
	fs.Var(&ownership, "write-ownership", "repository-relative owned path; repeat or comma-separate")
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
	class := model.ParseProcessExecutionClass(processID, target.artifact.URL, target.body)
	if class.Blocking() {
		return a.workspaceError(workspaceCommandResult{Repo: repo, Issue: issue, ProcessID: processID}, "execution_class_invalid", errors.New(model.CanonicalDiagnosticStrings(class.Diagnostics)[0]), *flags.jsonOut)
	}
	management := model.ParseProcessWorkspaceManagement(processID, target.artifact.URL, target.body)
	if management.Blocking() {
		return a.workspaceError(workspaceCommandResult{Repo: repo, Issue: issue, ProcessID: processID}, "workspace_management_invalid", errors.New(model.CanonicalDiagnosticStrings(management.Diagnostics)[0]), *flags.jsonOut)
	}
	if management.Explicit && management.Management == model.ProcessWorkspaceIndependent {
		return a.workspaceError(workspaceCommandResult{Repo: repo, Issue: issue, ProcessID: processID}, "workspace_management_independent", errors.New("independent PROCESS cannot enter the managed workspace lifecycle"), *flags.jsonOut)
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
		Owner: processworkspace.LeaseOwner{CoordinatorID: strings.TrimSpace(*coordinator), Token: strings.TrimSpace(*flags.ownerToken), PID: os.Getpid(), AcquiredAt: time.Now().UTC()}, LocalRevision: 1}
	existingLocal, localFound, err := manager.Store.Get(ctx, portable.WorkspaceID)
	if err != nil {
		return a.workspaceError(workspaceCommandResult{Repo: repo, Issue: issue, ProcessID: processID, WorkspaceID: portable.WorkspaceID}, "reservation_observation_failed", err, *flags.jsonOut)
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
		return a.workspaceLocalFailure(ctx, manager, inspection, repo, issue, processID, "prepare_failed", err, *flags.jsonOut)
	}
	updatedTarget, remoteResult, err := applyWorkspaceRemote(ctx, target, repo, issue, inspection.Lease.Portable)
	_ = updatedTarget
	if err != nil {
		result := workspaceResult(ctx, manager, inspection, repo, issue, processID, "prepared-local", remoteResult)
		result.ReconcileRequired = true
		return a.workspaceError(result, "remote_workspace_update_failed", err, *flags.jsonOut)
	}
	return a.outputWorkspace(workspaceResult(ctx, manager, inspection, repo, issue, processID, "prepared", remoteResult), *flags.jsonOut)
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
	if ok, code := a.parseFlagSet(fs, args); !ok {
		return code
	}
	repo, issue, processID, ok := a.validateWorkspaceFlags(flags, true)
	if !ok || strings.TrimSpace(*resultCommit) == "" {
		if ok {
			a.errorf("--result-commit is required\n")
		}
		return 2
	}
	return a.runWorkspaceLocalRemoteMutation(ctx, flags, repo, issue, processID, "complete", func(ctx context.Context, manager *processworkspace.Manager, target workspaceRemoteTarget, workspaceID string) (processworkspace.Inspection, error) {
		return manager.Complete(ctx, processworkspace.CompleteRequest{WorkspaceID: workspaceID,
			OwnerToken: strings.TrimSpace(*flags.ownerToken), ResultCommit: strings.TrimSpace(*resultCommit)})
	})
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
		ResultCommit: lease.Portable.ResultCommit, IntegrationSHA: lease.Portable.IntegrationSHA, RuntimeNamespace: lease.Portable.RuntimeNamespace,
		WorktreePath: lease.WorktreePath, Registered: inspection.Registered, Present: inspection.Present, Dirty: inspection.Dirty,
		Head: inspection.Head, GitBranch: inspection.Branch, Problems: append([]string(nil), inspection.Problems...), Remote: remote}
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
