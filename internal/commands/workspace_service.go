package commands

import (
	"context"

	"github.com/higress-group/issue-spec/internal/assignment"
	"github.com/higress-group/issue-spec/internal/processworkspace"
)

// workspaceService is the narrow seam the command layer uses to drive a process
// workspace. It captures exactly the process-workspace lifecycle methods the
// command layer invokes on *processworkspace.Manager (Prepare, IssueAssignment,
// Inspect, Reconcile, Complete, Integrate, Cleanup) plus the integration-root
// accessor and the lease Store the command layer reads directly.
//
// Production wiring returns a real adapter over processworkspace.OpenManager, so
// default behavior is byte-for-byte identical. Tests inject a deterministic fake
// that never starts a real Git process, which is the basis of the fast command
// tier.
type workspaceService interface {
	Prepare(context.Context, processworkspace.PrepareRequest) (processworkspace.Inspection, error)
	IssueAssignment(context.Context, processworkspace.AssignmentRequest) (processworkspace.Inspection, assignment.Packet, error)
	Inspect(context.Context, string) (processworkspace.Inspection, error)
	Reconcile(context.Context, string) (processworkspace.Inspection, error)
	Complete(context.Context, processworkspace.CompleteRequest) (processworkspace.Inspection, error)
	Integrate(context.Context, processworkspace.IntegrateRequest) (processworkspace.IntegrationResult, error)
	Cleanup(context.Context, string, string) (processworkspace.Inspection, error)
	IntegrationRootPath() string
	Store() *processworkspace.Store
}

// managerWorkspaceService adapts a real *processworkspace.Manager to the
// workspaceService seam. It only exposes the integration root and lease store
// through accessors (the Manager keeps them as fields); every lifecycle method
// delegates directly to the manager, so production behavior is unchanged.
type managerWorkspaceService struct {
	manager *processworkspace.Manager
}

func (s managerWorkspaceService) Prepare(ctx context.Context, request processworkspace.PrepareRequest) (processworkspace.Inspection, error) {
	return s.manager.Prepare(ctx, request)
}

func (s managerWorkspaceService) IssueAssignment(ctx context.Context, request processworkspace.AssignmentRequest) (processworkspace.Inspection, assignment.Packet, error) {
	return s.manager.IssueAssignment(ctx, request)
}

func (s managerWorkspaceService) Inspect(ctx context.Context, workspaceID string) (processworkspace.Inspection, error) {
	return s.manager.Inspect(ctx, workspaceID)
}

func (s managerWorkspaceService) Reconcile(ctx context.Context, workspaceID string) (processworkspace.Inspection, error) {
	return s.manager.Reconcile(ctx, workspaceID)
}

func (s managerWorkspaceService) Complete(ctx context.Context, request processworkspace.CompleteRequest) (processworkspace.Inspection, error) {
	return s.manager.Complete(ctx, request)
}

func (s managerWorkspaceService) Integrate(ctx context.Context, request processworkspace.IntegrateRequest) (processworkspace.IntegrationResult, error) {
	return s.manager.Integrate(ctx, request)
}

func (s managerWorkspaceService) Cleanup(ctx context.Context, workspaceID, ownerToken string) (processworkspace.Inspection, error) {
	return s.manager.Cleanup(ctx, workspaceID, ownerToken)
}

func (s managerWorkspaceService) IntegrationRootPath() string {
	return s.manager.IntegrationRoot
}

func (s managerWorkspaceService) Store() *processworkspace.Store {
	return s.manager.Store
}

// defaultOpenWorkspace is the production factory: it opens a real process
// workspace manager and returns it behind the workspaceService seam with the
// exact arguments the command layer previously passed to
// processworkspace.OpenManager.
func defaultOpenWorkspace(ctx context.Context, integrationRoot, workspaceRoot string, opts processworkspace.ManagerOptions) (workspaceService, error) {
	manager, err := processworkspace.OpenManager(ctx, integrationRoot, workspaceRoot, opts)
	if err != nil {
		return nil, err
	}
	return managerWorkspaceService{manager: manager}, nil
}
