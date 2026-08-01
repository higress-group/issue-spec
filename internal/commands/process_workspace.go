package commands

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/higress-group/issue-spec/internal/model"
	"github.com/higress-group/issue-spec/internal/processworkspace"
)

// runWorkspaceIntegrate is the command adapter for the coordinator-owned
// integration operation. Routing is added separately by PROCESS-014; keeping
// the adapter here lets the Git mutation remain wholly owned by Manager.
func (a *app) runWorkspaceIntegrate(ctx context.Context, args []string) int {
	fs := newFlagSet("workflow workspace integrate", a.err)
	flags := addWorkspaceCommandFlags(fs)
	expectedHead := fs.String("expected-head", "", "exact coordinator integration HEAD before applying the worker result")
	if ok, code := a.parseFlagSet(fs, args); !ok {
		return code
	}
	repo, issue, processID, ok := a.validateWorkspaceFlags(flags, true)
	if !ok || strings.TrimSpace(*expectedHead) == "" {
		if ok {
			a.errorf("--expected-head is required\n")
		}
		return 2
	}

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
	remoteWorkspace := model.ParseProcessWorkspace(processID, target.artifact.URL, target.body)
	if !remoteWorkspace.Explicit || remoteWorkspace.Blocking() || remoteWorkspace.Workspace == nil {
		return a.workspaceError(workspaceCommandResult{Repo: repo, Issue: issue, ProcessID: processID}, "reservation_identity_mismatch",
			fmt.Errorf("remote PROCESS must contain one valid explicit Workspace reservation"), *flags.jsonOut)
	}
	remoteLease := processworkspace.PortableLease(*remoteWorkspace.Workspace)
	requestedWorkspaceID := strings.TrimSpace(*flags.workspaceID)
	if requestedWorkspaceID != "" && requestedWorkspaceID != remoteLease.WorkspaceID {
		return a.workspaceError(workspaceCommandResult{Repo: repo, Issue: issue, ProcessID: processID, WorkspaceID: requestedWorkspaceID}, "reservation_identity_mismatch",
			fmt.Errorf("requested workspace id %q differs from remote reservation %q", requestedWorkspaceID, remoteLease.WorkspaceID), *flags.jsonOut)
	}
	manager, err := a.openWorkspace(ctx, *flags.integration, *flags.workspaceRoot, processworkspace.ManagerOptions{})
	if err != nil {
		return a.workspaceError(workspaceCommandResult{Repo: repo, Issue: issue, ProcessID: processID}, "manager_open_failed", err, *flags.jsonOut)
	}
	workspaceID := remoteLease.WorkspaceID
	localLease, found, err := manager.Store().Get(ctx, workspaceID)
	if err != nil {
		return a.workspaceError(workspaceCommandResult{Repo: repo, Issue: issue, ProcessID: processID, WorkspaceID: workspaceID}, "reservation_observation_failed", err, *flags.jsonOut)
	}
	identityErr := validateIntegrationConvergence(remoteLease, localLease, repo, strings.TrimSpace(*expectedHead))
	if !found || strings.TrimSpace(*flags.ownerToken) != localLease.Owner.Token || identityErr != nil {
		inspection := processworkspace.Inspection{Lease: localLease}
		return a.workspaceError(workspaceResult(ctx, manager, inspection, repo, issue, processID, "integrate", workspaceRemoteResult{}), "reservation_identity_mismatch",
			fmt.Errorf("remote PROCESS Workspace, local lease, repository, owner, or lifecycle do not identify an integrable reservation"), *flags.jsonOut)
	}
	integrated, err := manager.Integrate(ctx, processworkspace.IntegrateRequest{
		WorkspaceID: workspaceID, OwnerToken: strings.TrimSpace(*flags.ownerToken), ExpectedHead: strings.TrimSpace(*expectedHead),
	})
	inspection := processworkspace.Inspection{Lease: integrated.Lease}
	if err != nil {
		return a.workspaceLocalFailure(ctx, manager, inspection, repo, issue, processID, "integration_failed", err, *flags.jsonOut)
	}
	if integrated.IntegrationSHA == "" || integrated.Lease.Portable.IntegrationSHA != integrated.IntegrationSHA {
		return a.workspaceLocalFailure(ctx, manager, inspection, repo, issue, processID, "integration_evidence_invalid",
			fmt.Errorf("integrated lease did not publish its exact integration SHA"), *flags.jsonOut)
	}

	_, remoteResult, err := applyWorkspaceRemote(ctx, target, repo, issue, integrated.Lease.Portable)
	action := "integrated"
	if integrated.AlreadyIntegrated {
		action = "integrated-already"
	}
	if err != nil {
		result := workspaceResult(ctx, manager, inspection, repo, issue, processID, action+"-local", remoteResult)
		result.ReconcileRequired = true
		return a.workspaceError(result, "remote_workspace_update_failed", err, *flags.jsonOut)
	}
	return a.outputWorkspace(workspaceResult(ctx, manager, inspection, repo, issue, processID, action, remoteResult), *flags.jsonOut)
}

func validateIntegrationConvergence(remote processworkspace.PortableLease, local processworkspace.LocalLease, repository, expectedHead string) error {
	portable := local.Portable
	immutableEqual := remote.SchemaVersion == portable.SchemaVersion && remote.WorkspaceID == portable.WorkspaceID && remote.Repository == portable.Repository &&
		remote.Repository == repository && remote.ProcessID == portable.ProcessID && remote.ExecutionClass == portable.ExecutionClass && remote.Mode == portable.Mode &&
		remote.BaseSHA == portable.BaseSHA && remote.Branch == portable.Branch && remote.DetachedRevision == portable.DetachedRevision &&
		slices.Equal(remote.WriteOwnership, portable.WriteOwnership) && slices.Equal(remote.SharedTouchpoints, portable.SharedTouchpoints) &&
		remote.IntegrationOwner == portable.IntegrationOwner && remote.RuntimeNamespace == portable.RuntimeNamespace &&
		slices.Equal(remote.RuntimeResources, portable.RuntimeResources) && remote.CreatedAt.Equal(portable.CreatedAt) &&
		remote.RetentionExpiresAt.Equal(portable.RetentionExpiresAt) && samePortableAssignmentBinding(remote.Assignment, portable.Assignment)
	if !immutableEqual || remote.ResultCommit == "" || remote.ResultCommit != portable.ResultCommit {
		return fmt.Errorf("immutable reservation or result commit differs")
	}
	if remote.AcceptedReceiptID != portable.AcceptedReceiptID || remote.AcceptedReceiptDigest != portable.AcceptedReceiptDigest ||
		remote.AcceptedReceiptGeneration != portable.AcceptedReceiptGeneration ||
		!sameAcceptedReceiptSubmission(remote.AcceptedReceiptSubmission, portable.AcceptedReceiptSubmission) {
		return fmt.Errorf("accepted receipt authority differs")
	}
	attemptStarted := func() bool {
		return local.Integration.ExpectedHead != "" && strings.EqualFold(local.Integration.ExpectedHead, expectedHead) &&
			!local.Integration.StartedAt.IsZero()
	}
	integratingAttemptMatches := func() bool {
		return attemptStarted() && strings.EqualFold(local.Integration.ObservedHead, expectedHead) &&
			local.Integration.CompletedAt.IsZero() && local.Integration.LastError == ""
	}
	integratedAttemptMatches := func() bool {
		return attemptStarted() && portable.IntegrationSHA != "" && strings.EqualFold(local.Integration.ObservedHead, portable.IntegrationSHA) &&
			!local.Integration.CompletedAt.IsZero() && local.Integration.LastError == ""
	}
	switch remote.State {
	case processworkspace.StateWorkerComplete:
		switch portable.State {
		case processworkspace.StateWorkerComplete:
			return nil
		case processworkspace.StateIntegrating:
			if integratingAttemptMatches() && portable.IntegrationSHA == "" {
				return nil
			}
		case processworkspace.StateIntegrated:
			// Local integration succeeded but remote publication failed. Retrying
			// is safe only for the exact recorded integration attempt.
			if integratedAttemptMatches() {
				return nil
			}
		}
	case processworkspace.StateIntegrating:
		if portable.State == processworkspace.StateIntegrating && integratingAttemptMatches() && portable.IntegrationSHA == "" {
			return nil
		}
		if portable.State == processworkspace.StateIntegrated && integratedAttemptMatches() {
			return nil
		}
	case processworkspace.StateIntegrated:
		if portable.State == processworkspace.StateIntegrated && integratedAttemptMatches() && remote.IntegrationSHA != "" &&
			strings.EqualFold(remote.IntegrationSHA, portable.IntegrationSHA) {
			return nil
		}
	case processworkspace.StateCleanupPending, processworkspace.StateCleaned, processworkspace.StateConflicted:
		return fmt.Errorf("remote lifecycle %s is not integration-ready", remote.State)
	}
	return fmt.Errorf("remote/local lifecycle convergence is unsafe: remote=%s local=%s", remote.State, portable.State)
}

func samePortableAssignmentBinding(left, right *processworkspace.AssignmentBinding) bool {
	return processworkspace.AssignmentBindingEqual(left, right)
}
