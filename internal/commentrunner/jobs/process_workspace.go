package jobs

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/higress-group/issue-spec/internal/commentrunner/state"
	"github.com/higress-group/issue-spec/internal/model"
	"github.com/higress-group/issue-spec/internal/processworkspace"
)

var (
	normalizedRuntimeID             = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
	ErrProcessWorkspaceLeaseMissing = errors.New("durable process workspace association has no local lease")
	ErrProcessWorkspaceNotReady     = errors.New("process workspace is not ready for dispatch")
	ErrExternalWorkspaceUnsupported = errors.New("external process workspace adapter is not configured")
)

// Allocator is the PROCESS-scoped seam that PROCESS-012 wires into Dispatcher
// and the existing atomic RunnerState/FileStore transaction.
type Allocator interface {
	Allocate(context.Context, ProcessWorkspaceAllocationRequest) (ProcessWorkspaceAllocation, error)
	Inspect(context.Context, string) (ProcessWorkspaceAllocation, error)
	Reconcile(context.Context, string) (ProcessWorkspaceAllocation, error)
	BeginRelease(context.Context, string, string) (state.ProcessWorkspaceAssociation, error)
	ReleaseAfterCleanup(context.Context, string, string) (state.ProcessWorkspaceAssociation, error)
	CleanupAndRelease(context.Context, string, string) (state.ProcessWorkspaceAssociation, error)
}

type ProcessWorkspaceManager interface {
	Prepare(context.Context, processworkspace.PrepareRequest) (processworkspace.Inspection, error)
	Inspect(context.Context, string) (processworkspace.Inspection, error)
	Cleanup(context.Context, string, string) (processworkspace.Inspection, error)
}

type NoCheckoutLifecycle interface {
	Ready(context.Context, state.ProcessWorkspaceAssociation) (bool, error)
	Cleanup(context.Context, state.ProcessWorkspaceAssociation) (bool, error)
}

type noCheckoutPreflight interface {
	ValidateAssociation(context.Context, state.ProcessWorkspaceAssociation) error
}

// ProcessWorkspaceStateStore is the CAS-capable adapter contract for
// PROCESS-012. Each method must execute inside the same atomic runner-state
// transaction as jobs and cancellation state.
type ProcessWorkspaceStateStore interface {
	LoadProcessWorkspaces(context.Context) (state.ProcessWorkspaceAssociations, error)
	ReserveProcessWorkspace(context.Context, state.ProcessWorkspaceAssociation) (state.ProcessWorkspaceAssociation, error)
	TransitionProcessWorkspace(context.Context, string, string, state.ProcessWorkspaceLifecycle, state.ProcessWorkspaceLifecycle) (state.ProcessWorkspaceAssociation, error)
	MarkProcessWorkspaceFailure(context.Context, string, string, string) (state.ProcessWorkspaceAssociation, error)
	BeginReleaseProcessWorkspace(context.Context, string, string) (state.ProcessWorkspaceAssociation, error)
	ConfirmProcessWorkspaceReleased(context.Context, string, string) (state.ProcessWorkspaceAssociation, error)
	DeleteProcessWorkspace(context.Context, string, string) error
}

type ProcessWorkspaceAllocationRequest struct {
	Repository        string
	ProviderKey       string
	ServerInstance    string
	ProviderHost      string
	ProcessID         string
	WorkspaceID       string
	ExecutionClass    processworkspace.ExecutionClass
	ExternalMode      processworkspace.WorkspaceMode
	BaseSHA           string
	Branch            string
	WriteOwnership    []string
	SharedTouchpoints []string
	IntegrationOwner  string
	RuntimeResources  []processworkspace.RuntimeResource
	Owner             processworkspace.LeaseOwner
}

type ProcessWorkspaceAllocation struct {
	Association state.ProcessWorkspaceAssociation
	Inspection  *processworkspace.Inspection
	Generation  uint64
}

// TrustedProcessSelection is derived only from canonical typed artifacts and
// an exact structured command process id. An empty id is the /new coordinator
// case and intentionally has no PROCESS checkout assignment.
type TrustedProcessSelection struct {
	Artifact       model.Artifact
	ExecutionClass processworkspace.ExecutionClass
	Workspace      *model.ProcessWorkspace
}

func SelectTrustedProcessWorkspace(artifacts []model.Artifact, exactProcessID string) (*TrustedProcessSelection, error) {
	exactProcessID = strings.TrimSpace(exactProcessID)
	if exactProcessID == "" {
		return nil, nil
	}
	var matches []model.Artifact
	for _, artifact := range artifacts {
		if artifact.Comment.Type == "PROCESS" && artifact.Comment.Status != "superseded" && artifact.Comment.ID == exactProcessID {
			matches = append(matches, artifact)
		}
	}
	if len(matches) != 1 {
		return nil, fmt.Errorf("trusted PROCESS mapping for %s is ambiguous or missing: matches=%d", exactProcessID, len(matches))
	}
	class := model.ParseProcessExecutionClass(exactProcessID, matches[0].URL, matches[0].Comment.Body)
	if class.Blocking() || class.Class == "" {
		return nil, fmt.Errorf("trusted PROCESS %s has invalid execution class", exactProcessID)
	}
	workspace := model.ParseProcessWorkspace(exactProcessID, matches[0].URL, matches[0].Comment.Body)
	if workspace.Blocking() {
		return nil, fmt.Errorf("trusted PROCESS %s has invalid workspace metadata", exactProcessID)
	}
	return &TrustedProcessSelection{Artifact: matches[0], ExecutionClass: processworkspace.ExecutionClass(class.Class), Workspace: workspace.Workspace}, nil
}

type ManagerAllocator struct {
	Manager          ProcessWorkspaceManager
	NoCheckout       NoCheckoutLifecycle
	State            ProcessWorkspaceStateStore
	Now              func() time.Time
	ReservationToken func() (string, error)
}

func (a *ManagerAllocator) Allocate(ctx context.Context, request ProcessWorkspaceAllocationRequest) (ProcessWorkspaceAllocation, error) {
	if a == nil || a.State == nil {
		return ProcessWorkspaceAllocation{}, errors.New("process workspace state store is required")
	}
	mode, err := workspaceModeForClass(request.ExecutionClass, request.ExternalMode)
	if err != nil {
		return ProcessWorkspaceAllocation{}, err
	}
	// Strict P005 grammar is checked before resource normalization or any
	// durable reservation, closing the old ?/[] allocation bypass.
	if err := processworkspace.ValidateManagedOwnership(request.WriteOwnership, request.SharedTouchpoints); err != nil {
		return ProcessWorkspaceAllocation{}, fmt.Errorf("managed ownership: %w", err)
	}
	ownership, err := processworkspace.NormalizeManagedOwnership(request.WriteOwnership)
	if err != nil {
		return ProcessWorkspaceAllocation{}, err
	}
	shared, err := processworkspace.NormalizeManagedOwnership(request.SharedTouchpoints)
	if err != nil {
		return ProcessWorkspaceAllocation{}, err
	}
	resources, err := normalizeRuntimeResources(request.RuntimeResources)
	if err != nil {
		return ProcessWorkspaceAllocation{}, err
	}
	provider := state.ProcessWorkspaceProviderIdentity{
		ProviderKey: strings.TrimSpace(request.ProviderKey), ServerInstance: strings.TrimSpace(request.ServerInstance), Host: strings.TrimSpace(request.ProviderHost),
	}
	association := state.ProcessWorkspaceAssociation{
		SchemaVersion: state.ProcessWorkspaceAssociationSchemaVersion, Lifecycle: state.ProcessWorkspaceAllocating,
		WorkspaceID: strings.TrimSpace(request.WorkspaceID), Repository: strings.TrimSpace(request.Repository), Provider: provider,
		ProcessID: strings.TrimSpace(request.ProcessID), BaseSHA: strings.TrimSpace(request.BaseSHA), Branch: strings.TrimSpace(request.Branch),
		ExecutionClass: request.ExecutionClass, Mode: mode, WriteOwnership: ownership, SharedTouchpoints: shared,
		IntegrationOwner: strings.TrimSpace(request.IntegrationOwner), RuntimeResources: resources,
	}
	if mode != processworkspace.ModeWritable {
		association.Branch = ""
	}
	if mode != processworkspace.ModeNone {
		association.LocalAssociationRef = "lease:" + association.WorkspaceID
	}
	association.RuntimeNamespace = runtimeNamespace(association.Repository, provider, association.ProcessID, association.WorkspaceID)
	association.ReservationIdentity = association.ExpectedReservationIdentity()
	association.ReservationID, err = a.nextReservationToken()
	if err != nil {
		return ProcessWorkspaceAllocation{}, err
	}
	if err := association.Validate(); err != nil {
		return ProcessWorkspaceAllocation{}, err
	}
	if mode == processworkspace.ModeNone || request.ExecutionClass == processworkspace.ExecutionExternal {
		if a.NoCheckout == nil {
			return ProcessWorkspaceAllocation{}, errors.New("no-checkout workspace adapter is required")
		}
		if preflight, ok := a.NoCheckout.(noCheckoutPreflight); ok {
			if err := preflight.ValidateAssociation(ctx, association); err != nil {
				return ProcessWorkspaceAllocation{}, err
			}
		}
	}
	if mode != processworkspace.ModeNone && a.Manager == nil {
		return ProcessWorkspaceAllocation{}, errors.New("process workspace manager is required for checkout allocation")
	}

	now := time.Now().UTC()
	if a.Now != nil {
		now = a.Now().UTC()
	}
	portable := processworkspace.PortableLease{
		SchemaVersion: processworkspace.LeaseSchemaVersion, WorkspaceID: association.WorkspaceID, Repository: association.Repository,
		ProcessID: association.ProcessID, ExecutionClass: association.ExecutionClass, Mode: association.Mode, BaseSHA: association.BaseSHA,
		WriteOwnership: append([]string(nil), ownership...), SharedTouchpoints: append([]string(nil), shared...), IntegrationOwner: association.IntegrationOwner,
		RuntimeNamespace: association.RuntimeNamespace, RuntimeResources: append([]processworkspace.RuntimeResource(nil), resources...),
		State: processworkspace.StatePreparing, CreatedAt: now, UpdatedAt: now,
	}
	if mode == processworkspace.ModeWritable {
		portable.Branch = association.Branch
	}
	if mode == processworkspace.ModeSnapshot {
		portable.DetachedRevision = association.BaseSHA
	}
	if err := portable.Validate(); err != nil {
		return ProcessWorkspaceAllocation{}, err
	}

	reserved, err := a.State.ReserveProcessWorkspace(ctx, association)
	if err != nil {
		return ProcessWorkspaceAllocation{}, err
	}
	allocation := ProcessWorkspaceAllocation{Association: reserved}
	if reserved.Lifecycle == state.ProcessWorkspacePrepared {
		return a.Inspect(ctx, reserved.WorkspaceID)
	}
	if mode == processworkspace.ModeNone {
		return a.Inspect(ctx, reserved.WorkspaceID)
	}
	inspection, prepareErr := a.Manager.Prepare(ctx, processworkspace.PrepareRequest{Lease: processworkspace.LocalLease{
		Portable: portable, Owner: request.Owner, LocalRevision: 1,
	}})
	if prepareErr != nil {
		held, markErr := a.State.MarkProcessWorkspaceFailure(ctx, reserved.WorkspaceID, reserved.ReservationID, "manager_prepare_failed")
		allocation.Association = held
		allocation.Inspection = &inspection
		return allocation, errors.Join(prepareErr, markErr)
	}
	if association.ExecutionClass == processworkspace.ExecutionExternal {
		ready, readyErr := a.NoCheckout.Ready(ctx, reserved)
		if readyErr != nil || !ready {
			held, markErr := a.State.MarkProcessWorkspaceFailure(ctx, reserved.WorkspaceID, reserved.ReservationID, "adapter_not_ready")
			allocation.Association = held
			allocation.Inspection = &inspection
			return allocation, errors.Join(ErrProcessWorkspaceNotReady, readyErr, markErr)
		}
	}
	prepared, transitionErr := a.State.TransitionProcessWorkspace(ctx, reserved.WorkspaceID, reserved.ReservationID, state.ProcessWorkspaceAllocating, state.ProcessWorkspacePrepared)
	if transitionErr != nil {
		_, _ = a.State.MarkProcessWorkspaceFailure(ctx, reserved.WorkspaceID, reserved.ReservationID, "publication_failed")
	}
	allocation.Association = prepared
	allocation.Inspection = &inspection
	if transitionErr == nil {
		if current, loadErr := a.State.LoadProcessWorkspaces(ctx); loadErr == nil {
			allocation.Generation = current.Generation
		} else {
			transitionErr = loadErr
		}
	}
	return allocation, transitionErr
}

func (a *ManagerAllocator) Inspect(ctx context.Context, workspaceID string) (ProcessWorkspaceAllocation, error) {
	if a == nil || a.State == nil {
		return ProcessWorkspaceAllocation{}, errors.New("process workspace state store is required")
	}
	associations, err := a.State.LoadProcessWorkspaces(ctx)
	if err != nil {
		return ProcessWorkspaceAllocation{}, err
	}
	association, ok := associations.Get(strings.TrimSpace(workspaceID))
	if !ok {
		return ProcessWorkspaceAllocation{}, fmt.Errorf("process workspace association %q not found", workspaceID)
	}
	allocation := ProcessWorkspaceAllocation{Association: association, Generation: associations.Generation}
	if association.Lifecycle == state.ProcessWorkspaceFailed {
		return allocation, fmt.Errorf("%w: reservation is failed", ErrProcessWorkspaceLeaseMissing)
	}
	if association.Lifecycle == state.ProcessWorkspaceReleased {
		return allocation, nil
	}
	if association.Lifecycle == state.ProcessWorkspaceCleanupPending {
		return allocation, fmt.Errorf("%w: cleanup is pending", ErrProcessWorkspaceNotReady)
	}
	if association.Mode == processworkspace.ModeNone {
		if a.NoCheckout == nil {
			return a.markNotReady(ctx, allocation, "adapter_readiness_missing", ErrProcessWorkspaceNotReady)
		}
		ready, readyErr := a.NoCheckout.Ready(ctx, association)
		if readyErr != nil || !ready {
			return a.markNotReady(ctx, allocation, "adapter_not_ready", errors.Join(ErrProcessWorkspaceNotReady, readyErr))
		}
		return a.markPrepared(ctx, allocation)
	}
	if a.Manager == nil {
		return allocation, errors.New("process workspace manager is required for checkout inspection")
	}
	inspection, inspectErr := a.Manager.Inspect(ctx, association.WorkspaceID)
	if inspectErr != nil {
		if errors.Is(inspectErr, processworkspace.ErrLeaseNotFound) {
			return a.markNotReady(ctx, allocation, "local_lease_missing", fmt.Errorf("%w: %s", ErrProcessWorkspaceLeaseMissing, association.WorkspaceID))
		}
		return a.markNotReady(ctx, allocation, "manager_inspect_failed", inspectErr)
	}
	if !associationMatchesInspection(association, inspection) {
		allocation.Inspection = &inspection
		return a.markNotReady(ctx, allocation, "workspace_not_ready", ErrProcessWorkspaceNotReady)
	}
	allocation.Inspection = &inspection
	return a.markPrepared(ctx, allocation)
}

func (a *ManagerAllocator) Reconcile(ctx context.Context, workspaceID string) (ProcessWorkspaceAllocation, error) {
	return a.Inspect(ctx, workspaceID)
}

func (a *ManagerAllocator) BeginRelease(ctx context.Context, workspaceID, reservationID string) (state.ProcessWorkspaceAssociation, error) {
	return a.State.BeginReleaseProcessWorkspace(ctx, workspaceID, reservationID)
}

func (a *ManagerAllocator) ReleaseAfterCleanup(ctx context.Context, workspaceID, reservationID string) (state.ProcessWorkspaceAssociation, error) {
	return a.State.ConfirmProcessWorkspaceReleased(ctx, workspaceID, reservationID)
}

// CleanupAndRelease preserves the exclusive reservation until the local
// manager or provider adapter confirms cleanup. Retrying a cleanup-pending
// association is intentional and uses the same reservation token throughout.
func (a *ManagerAllocator) CleanupAndRelease(ctx context.Context, workspaceID, reservationID string) (state.ProcessWorkspaceAssociation, error) {
	if a == nil || a.State == nil {
		return state.ProcessWorkspaceAssociation{}, errors.New("process workspace state store is required")
	}
	associations, err := a.State.LoadProcessWorkspaces(ctx)
	if err != nil {
		return state.ProcessWorkspaceAssociation{}, err
	}
	association, ok := associations.Get(strings.TrimSpace(workspaceID))
	if !ok || association.ReservationID != strings.TrimSpace(reservationID) {
		return state.ProcessWorkspaceAssociation{}, errors.New("process workspace reservation CAS mismatch")
	}
	if association.Lifecycle == state.ProcessWorkspaceReleased {
		return association, nil
	}
	pending, err := a.BeginRelease(ctx, association.WorkspaceID, association.ReservationID)
	if err != nil {
		return state.ProcessWorkspaceAssociation{}, err
	}
	if pending.Mode == processworkspace.ModeNone {
		if a.NoCheckout == nil {
			return pending, errors.New("no-checkout cleanup adapter is required")
		}
		confirmed, cleanupErr := a.NoCheckout.Cleanup(ctx, pending)
		if cleanupErr != nil || !confirmed {
			if cleanupErr == nil {
				cleanupErr = errors.New("no-checkout cleanup was not confirmed")
			}
			return pending, cleanupErr
		}
	} else {
		if a.Manager == nil {
			return pending, errors.New("process workspace manager is required for checkout cleanup")
		}
		inspection, inspectErr := a.Manager.Inspect(ctx, pending.WorkspaceID)
		if inspectErr != nil {
			return pending, inspectErr
		}
		cleaned, cleanupErr := a.Manager.Cleanup(ctx, pending.WorkspaceID, inspection.Lease.Owner.Token)
		if cleanupErr != nil {
			return pending, cleanupErr
		}
		if cleaned.Lease.Portable.State != processworkspace.StateCleaned {
			return pending, errors.New("process workspace cleanup was not confirmed")
		}
		if pending.ExecutionClass == processworkspace.ExecutionExternal {
			if a.NoCheckout == nil {
				return pending, errors.New("external cleanup adapter is required")
			}
			confirmed, cleanupErr := a.NoCheckout.Cleanup(ctx, pending)
			if cleanupErr != nil || !confirmed {
				if cleanupErr == nil {
					cleanupErr = errors.New("external cleanup was not confirmed")
				}
				return pending, cleanupErr
			}
		}
	}
	return a.ReleaseAfterCleanup(ctx, pending.WorkspaceID, pending.ReservationID)
}

// ProcessWorkspaceAllocatorRequest carries only trusted runner configuration;
// the assigned PROCESS identity is supplied separately by the dispatcher.
type ProcessWorkspaceAllocatorRequest struct {
	IntegrationRoot string
}

type ProcessWorkspaceAllocatorProvider interface {
	ProcessWorkspaceAllocator(context.Context, ProcessWorkspaceAllocatorRequest) (Allocator, error)
}

// ProcessWorkspaceRuntime is injected through Dispatcher.Workspaces so P008
// can request an allocator without rediscovering FileStore or runner config.
// Composition alone does not activate dispatch: P008 must run the exact typed
// selector, allocation and durable job assignment before any ACPX call.
type ProcessWorkspaceRuntime struct {
	WorkspaceManager
	State            ProcessWorkspaceStateStore
	ManagedRoot      string
	ExternalAdapters map[string]NoCheckoutLifecycle
}

func NewProcessWorkspaceRuntime(workspaces WorkspaceManager, store ProcessWorkspaceStateStore, managedRoot string, external map[string]NoCheckoutLifecycle) (*ProcessWorkspaceRuntime, error) {
	if workspaces == nil || store == nil {
		return nil, errors.New("runner workspace manager and state adapter are required")
	}
	root, err := filepath.Abs(strings.TrimSpace(managedRoot))
	if err != nil || strings.TrimSpace(managedRoot) == "" {
		return nil, errors.New("managed process workspace root is required")
	}
	return &ProcessWorkspaceRuntime{WorkspaceManager: workspaces, State: store, ManagedRoot: root, ExternalAdapters: external}, nil
}

func (r *ProcessWorkspaceRuntime) ProcessWorkspaceAllocator(ctx context.Context, request ProcessWorkspaceAllocatorRequest) (Allocator, error) {
	if r == nil || r.State == nil {
		return nil, errors.New("process workspace runtime is required")
	}
	integrationRoot, err := filepath.Abs(strings.TrimSpace(request.IntegrationRoot))
	if err != nil || strings.TrimSpace(request.IntegrationRoot) == "" {
		return nil, errors.New("session integration root is required")
	}
	digest := sha256.Sum256([]byte(integrationRoot))
	workspaceRoot := filepath.Join(r.ManagedRoot, ".process-workspaces", hex.EncodeToString(digest[:8]))
	manager, err := processworkspace.OpenManager(ctx, integrationRoot, workspaceRoot, processworkspace.ManagerOptions{})
	if err != nil {
		return nil, err
	}
	return &ManagerAllocator{Manager: manager, NoCheckout: runtimeNoCheckout{external: r.ExternalAdapters}, State: r.State}, nil
}

type runtimeNoCheckout struct {
	external map[string]NoCheckoutLifecycle
}

func (r runtimeNoCheckout) Ready(ctx context.Context, association state.ProcessWorkspaceAssociation) (bool, error) {
	if association.ExecutionClass == processworkspace.ExecutionOrchestration {
		return true, nil
	}
	adapter := r.external[association.Provider.ProviderKey]
	if adapter == nil {
		return false, fmt.Errorf("%w: %s", ErrExternalWorkspaceUnsupported, association.Provider.ProviderKey)
	}
	return adapter.Ready(ctx, association)
}

func (r runtimeNoCheckout) Cleanup(ctx context.Context, association state.ProcessWorkspaceAssociation) (bool, error) {
	if association.ExecutionClass == processworkspace.ExecutionOrchestration {
		return true, nil
	}
	adapter := r.external[association.Provider.ProviderKey]
	if adapter == nil {
		return false, fmt.Errorf("%w: %s", ErrExternalWorkspaceUnsupported, association.Provider.ProviderKey)
	}
	return adapter.Cleanup(ctx, association)
}

func (r runtimeNoCheckout) ValidateAssociation(_ context.Context, association state.ProcessWorkspaceAssociation) error {
	if association.ExecutionClass == processworkspace.ExecutionOrchestration {
		return nil
	}
	if association.ExecutionClass != processworkspace.ExecutionExternal || r.external[association.Provider.ProviderKey] == nil {
		return fmt.Errorf("%w: %s", ErrExternalWorkspaceUnsupported, association.Provider.ProviderKey)
	}
	return nil
}

// MarkTerminalWorkspaceCleanupRequired is the P008 handoff used in every
// normal completed/failed terminal state transaction before post-terminal
// cleanup is attempted. It records intent without changing the reservation.
func MarkTerminalWorkspaceCleanupRequired(job *state.Job) {
	if job == nil || job.ProcessWorkspace == nil || job.ProcessWorkspace.CleanupState == state.ProcessWorkspaceAssignmentCleanupConfirmed {
		return
	}
	job.ProcessWorkspace.CleanupRequired = true
	job.ProcessWorkspace.CleanupState = state.ProcessWorkspaceAssignmentCleanupRequired
	job.ProcessWorkspace.LastError = ""
}

func (a *ManagerAllocator) markNotReady(ctx context.Context, allocation ProcessWorkspaceAllocation, code string, cause error) (ProcessWorkspaceAllocation, error) {
	held, markErr := a.State.MarkProcessWorkspaceFailure(ctx, allocation.Association.WorkspaceID, allocation.Association.ReservationID, code)
	allocation.Association = held
	return allocation, errors.Join(cause, markErr)
}

func (a *ManagerAllocator) markPrepared(ctx context.Context, allocation ProcessWorkspaceAllocation) (ProcessWorkspaceAllocation, error) {
	prepared, err := a.State.TransitionProcessWorkspace(ctx, allocation.Association.WorkspaceID, allocation.Association.ReservationID, allocation.Association.Lifecycle, state.ProcessWorkspacePrepared)
	allocation.Association = prepared
	return allocation, err
}

func associationMatchesInspection(association state.ProcessWorkspaceAssociation, inspection processworkspace.Inspection) bool {
	lease := inspection.Lease.Portable
	return lease.State == processworkspace.StatePrepared && inspection.Registered && inspection.Present && !inspection.Dirty && len(inspection.Problems) == 0 &&
		association.WorkspaceID == lease.WorkspaceID && association.Repository == lease.Repository && association.ProcessID == lease.ProcessID &&
		association.ExecutionClass == lease.ExecutionClass && association.Mode == lease.Mode && association.BaseSHA == lease.BaseSHA && association.Branch == lease.Branch &&
		association.IntegrationOwner == lease.IntegrationOwner && association.RuntimeNamespace == lease.RuntimeNamespace &&
		reflect.DeepEqual(association.WriteOwnership, lease.WriteOwnership) && reflect.DeepEqual(association.SharedTouchpoints, lease.SharedTouchpoints) &&
		reflect.DeepEqual(association.RuntimeResources, lease.RuntimeResources)
}

func (a *ManagerAllocator) nextReservationToken() (string, error) {
	if a.ReservationToken != nil {
		return a.ReservationToken()
	}
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return "reservation:" + hex.EncodeToString(value[:]), nil
}

func workspaceModeForClass(class processworkspace.ExecutionClass, external processworkspace.WorkspaceMode) (processworkspace.WorkspaceMode, error) {
	switch class {
	case processworkspace.ExecutionChangeBearing:
		return processworkspace.ModeWritable, nil
	case processworkspace.ExecutionReview, processworkspace.ExecutionVerification:
		return processworkspace.ModeSnapshot, nil
	case processworkspace.ExecutionOrchestration:
		return processworkspace.ModeNone, nil
	case processworkspace.ExecutionExternal:
		if external == processworkspace.ModeNone || external == processworkspace.ModeWritable || external == processworkspace.ModeSnapshot {
			return external, nil
		}
		return "", fmt.Errorf("external execution requires adapter workspace mode, got %q", external)
	default:
		return "", fmt.Errorf("unsupported execution class %q", class)
	}
}

func runtimeNamespace(repository string, provider state.ProcessWorkspaceProviderIdentity, processID, workspaceID string) string {
	source := provider.ProviderKey + "\x00" + provider.ServerInstance + "\x00" + provider.Host + "\x00" + strings.ToLower(repository) + "\x00" + processID + "\x00" + workspaceID
	sum := sha256.Sum256([]byte(source))
	return "process-" + hex.EncodeToString(sum[:10])
}

func normalizeRuntimeResources(resources []processworkspace.RuntimeResource) ([]processworkspace.RuntimeResource, error) {
	result := make([]processworkspace.RuntimeResource, 0, len(resources))
	seen := map[string]struct{}{}
	for _, resource := range resources {
		resource.Kind = strings.ToLower(strings.TrimSpace(resource.Kind))
		resource.Name = strings.ToLower(strings.TrimSpace(resource.Name))
		if !normalizedRuntimeID.MatchString(resource.Kind) || !normalizedRuntimeID.MatchString(resource.Name) {
			return nil, fmt.Errorf("invalid runtime resource %q/%q", resource.Kind, resource.Name)
		}
		key := resource.Kind + "\x00" + resource.Name
		if _, ok := seen[key]; ok {
			return nil, fmt.Errorf("duplicate runtime resource %s/%s", resource.Kind, resource.Name)
		}
		seen[key] = struct{}{}
		result = append(result, resource)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Kind != result[j].Kind {
			return result[i].Kind < result[j].Kind
		}
		return result[i].Name < result[j].Name
	})
	return result, nil
}
