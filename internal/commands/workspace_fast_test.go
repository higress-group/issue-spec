package commands

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/higress-group/issue-spec/internal/assignment"
	"github.com/higress-group/issue-spec/internal/model"
	"github.com/higress-group/issue-spec/internal/processworkspace"
)

// These tests exercise command orchestration (success, failure, conflict,
// retry/idempotency, result projection, and error-code mapping) through the
// injected workspaceService seam. They never open a real process workspace and
// never spawn a Git process, so they run in the fast tier (`go test -short`).

// fakeWorkspaceService is a deterministic workspaceService. Lifecycle methods
// return scripted values; the lease Store is a real filesystem store rooted
// under a test temp dir, which requires only a directory (no Git repository).
type fakeWorkspaceService struct {
	store           *processworkspace.Store
	integrationRoot string

	prepareFn   func(context.Context, processworkspace.PrepareRequest) (processworkspace.Inspection, error)
	assignFn    func(context.Context, processworkspace.AssignmentRequest) (processworkspace.Inspection, assignment.Packet, error)
	inspectFn   func(context.Context, string) (processworkspace.Inspection, error)
	reconcileFn func(context.Context, string) (processworkspace.Inspection, error)
	completeFn  func(context.Context, processworkspace.CompleteRequest) (processworkspace.Inspection, error)
	integrateFn func(context.Context, processworkspace.IntegrateRequest) (processworkspace.IntegrationResult, error)
	cleanupFn   func(context.Context, string, string) (processworkspace.Inspection, error)

	calls []string
}

var _ workspaceService = (*fakeWorkspaceService)(nil)

func newFakeWorkspaceService(t *testing.T) *fakeWorkspaceService {
	t.Helper()
	store, err := processworkspace.OpenStore(filepath.Join(t.TempDir(), ".git"))
	if err != nil {
		t.Fatalf("open fake workspace store: %v", err)
	}
	return &fakeWorkspaceService{store: store, integrationRoot: "/fake/integration"}
}

func (f *fakeWorkspaceService) record(name string) { f.calls = append(f.calls, name) }

func (f *fakeWorkspaceService) Prepare(ctx context.Context, request processworkspace.PrepareRequest) (processworkspace.Inspection, error) {
	f.record("Prepare")
	if f.prepareFn != nil {
		return f.prepareFn(ctx, request)
	}
	return processworkspace.Inspection{Lease: request.Lease}, nil
}

func (f *fakeWorkspaceService) IssueAssignment(ctx context.Context, request processworkspace.AssignmentRequest) (processworkspace.Inspection, assignment.Packet, error) {
	f.record("IssueAssignment")
	if f.assignFn != nil {
		return f.assignFn(ctx, request)
	}
	return processworkspace.Inspection{}, assignment.Packet{}, nil
}

func (f *fakeWorkspaceService) Inspect(ctx context.Context, workspaceID string) (processworkspace.Inspection, error) {
	f.record("Inspect")
	if f.inspectFn != nil {
		return f.inspectFn(ctx, workspaceID)
	}
	return processworkspace.Inspection{}, nil
}

func (f *fakeWorkspaceService) Reconcile(ctx context.Context, workspaceID string) (processworkspace.Inspection, error) {
	f.record("Reconcile")
	if f.reconcileFn != nil {
		return f.reconcileFn(ctx, workspaceID)
	}
	return processworkspace.Inspection{}, nil
}

func (f *fakeWorkspaceService) Complete(ctx context.Context, request processworkspace.CompleteRequest) (processworkspace.Inspection, error) {
	f.record("Complete")
	if f.completeFn != nil {
		return f.completeFn(ctx, request)
	}
	return processworkspace.Inspection{}, nil
}

func (f *fakeWorkspaceService) Integrate(ctx context.Context, request processworkspace.IntegrateRequest) (processworkspace.IntegrationResult, error) {
	f.record("Integrate")
	if f.integrateFn != nil {
		return f.integrateFn(ctx, request)
	}
	return processworkspace.IntegrationResult{}, nil
}

func (f *fakeWorkspaceService) Cleanup(ctx context.Context, workspaceID, ownerToken string) (processworkspace.Inspection, error) {
	f.record("Cleanup")
	if f.cleanupFn != nil {
		return f.cleanupFn(ctx, workspaceID, ownerToken)
	}
	return processworkspace.Inspection{}, nil
}

func (f *fakeWorkspaceService) IntegrationRootPath() string { return f.integrationRoot }

func (f *fakeWorkspaceService) Store() *processworkspace.Store { return f.store }

func fastWritableLease(workspaceID string) processworkspace.LocalLease {
	now := time.Unix(100, 0).UTC()
	return processworkspace.LocalLease{
		Portable: processworkspace.PortableLease{
			SchemaVersion: processworkspace.LeaseSchemaVersion, WorkspaceID: workspaceID, Repository: "o/r", ProcessID: "PROCESS-004",
			ExecutionClass: processworkspace.ExecutionChangeBearing, Mode: processworkspace.ModeWritable, BaseSHA: strings.Repeat("a", 40),
			Branch: "issue-spec/" + workspaceID, WriteOwnership: []string{"internal/commands/**"}, RuntimeNamespace: workspaceID,
			State: processworkspace.StatePreparing, CreatedAt: now, UpdatedAt: now,
		},
		IntegrationRoot: "/fake/integration",
		Owner:           processworkspace.LeaseOwner{CoordinatorID: "coordinator", Token: "owner-secret", AcquiredAt: now},
		LocalRevision:   1,
	}
}

func fastExternalLease(workspaceID string) processworkspace.LocalLease {
	now := time.Unix(100, 0).UTC()
	return processworkspace.LocalLease{
		Portable: processworkspace.PortableLease{
			SchemaVersion: processworkspace.LeaseSchemaVersion, WorkspaceID: workspaceID, Repository: "o/r", ProcessID: "PROCESS-EXT",
			ExecutionClass: processworkspace.ExecutionExternal, Mode: processworkspace.ModeNone,
			State: processworkspace.StatePreparing, CreatedAt: now, UpdatedAt: now,
		},
		IntegrationRoot: "/fake/integration",
		Owner:           processworkspace.LeaseOwner{CoordinatorID: "coordinator", Token: "owner-secret", AcquiredAt: now},
		LocalRevision:   1,
	}
}

// TestFastInspectHelperProjectsScriptedInspection covers the success and
// result-projection scenarios: a found lease is inspected through the injected
// service and the scripted inspection is returned without any Git process.
func TestFastInspectHelperProjectsScriptedInspection(t *testing.T) {
	fake := newFakeWorkspaceService(t)
	lease := fastWritableLease("ws-process-004")
	if _, err := fake.store.Create(context.Background(), lease); err != nil {
		t.Fatalf("seed lease: %v", err)
	}
	prepared := lease
	prepared.Portable.State = processworkspace.StatePrepared
	fake.inspectFn = func(_ context.Context, id string) (processworkspace.Inspection, error) {
		if id != "ws-process-004" {
			t.Fatalf("unexpected workspace id %q", id)
		}
		return processworkspace.Inspection{Lease: prepared}, nil
	}

	inspection, err := inspectWorkspace(context.Background(), fake, "ws-process-004")
	if err != nil {
		t.Fatalf("inspect helper: %v", err)
	}
	if inspection.Lease.Portable.State != processworkspace.StatePrepared {
		t.Fatalf("state=%q want prepared", inspection.Lease.Portable.State)
	}
	if len(fake.calls) != 1 || fake.calls[0] != "Inspect" {
		t.Fatalf("calls=%v want single Inspect", fake.calls)
	}

	result := workspaceResult(context.Background(), fake, inspection, "o/r", 177, "PROCESS-004", "inspected", workspaceRemoteResult{})
	if result.WorkspaceID != "ws-process-004" || result.State != processworkspace.StatePrepared {
		t.Fatalf("projected result=%+v", result)
	}
}

// TestFastInspectHelperMapsMissingLease covers the error-code scenario: a lease
// absent from the store maps to ErrLeaseNotFound before any Inspect call.
func TestFastInspectHelperMapsMissingLease(t *testing.T) {
	fake := newFakeWorkspaceService(t)
	_, err := inspectWorkspace(context.Background(), fake, "ws-missing")
	if !errors.Is(err, processworkspace.ErrLeaseNotFound) {
		t.Fatalf("err=%v want ErrLeaseNotFound", err)
	}
	if len(fake.calls) != 0 {
		t.Fatalf("calls=%v want none (Inspect must be skipped)", fake.calls)
	}
}

// TestFastPrepareNoCheckoutIsIdempotent covers the retry/idempotency scenario:
// re-preparing the same reservation converges on the existing lease instead of
// failing.
func TestFastPrepareNoCheckoutIsIdempotent(t *testing.T) {
	fake := newFakeWorkspaceService(t)
	lease := fastExternalLease("ws-process-ext")

	first, err := prepareNoCheckout(context.Background(), fake, lease)
	if err != nil {
		t.Fatalf("first prepare: %v", err)
	}
	if first.Lease.Portable.State != processworkspace.StatePrepared {
		t.Fatalf("first state=%q want prepared", first.Lease.Portable.State)
	}

	second, err := prepareNoCheckout(context.Background(), fake, lease)
	if err != nil {
		t.Fatalf("idempotent prepare returned error: %v", err)
	}
	if second.Lease.Portable.WorkspaceID != first.Lease.Portable.WorkspaceID {
		t.Fatalf("idempotent prepare diverged: %q vs %q", second.Lease.Portable.WorkspaceID, first.Lease.Portable.WorkspaceID)
	}
}

// TestFastPrepareNoCheckoutRejectsConflictingReservation covers the conflict
// scenario: a different reservation for the same workspace id is refused.
func TestFastPrepareNoCheckoutRejectsConflictingReservation(t *testing.T) {
	fake := newFakeWorkspaceService(t)
	lease := fastExternalLease("ws-process-ext")
	if _, err := prepareNoCheckout(context.Background(), fake, lease); err != nil {
		t.Fatalf("seed prepare: %v", err)
	}

	conflicting := fastExternalLease("ws-process-ext")
	conflicting.Owner.Token = "different-owner"
	_, err := prepareNoCheckout(context.Background(), fake, conflicting)
	if err == nil || !strings.Contains(err.Error(), "another reservation") {
		t.Fatalf("conflict err=%v want reservation conflict", err)
	}
}

// TestFastWorkspaceInspectCommandUsesInjectedService covers an end-to-end
// command success path proving the openWorkspace seam is honored with no Git.
func TestFastWorkspaceInspectCommandUsesInjectedService(t *testing.T) {
	processBody := workspaceProcessBody(t, model.ProcessExecutionChangeBearing)
	backend := newWorkspaceCASBackend(processBody)
	app, out, errOut := transitionAppWithError(backend)

	fake := newFakeWorkspaceService(t)
	lease := fastWritableLease("ws-process-004")
	if _, err := fake.store.Create(context.Background(), lease); err != nil {
		t.Fatalf("seed lease: %v", err)
	}
	prepared := lease
	prepared.Portable.State = processworkspace.StatePrepared
	fake.inspectFn = func(context.Context, string) (processworkspace.Inspection, error) {
		return processworkspace.Inspection{Lease: prepared}, nil
	}
	app.openWorkspace = func(context.Context, string, string, processworkspace.ManagerOptions) (workspaceService, error) {
		return fake, nil
	}

	args := []string{"inspect", "--repo", "o/r", "--issue", "177", "--process", "PROCESS-004",
		"--integration-root", "/fake/integration", "--workspace-root", "/fake/managed", "--workspace-id", "ws-process-004", "--json"}
	if code := app.runWorkflowWorkspace(context.Background(), args); code != 0 {
		t.Fatalf("inspect code=%d out=%s err=%s", code, out.String(), errOut.String())
	}
	result := decodeWorkspaceResult(t, out)
	if !result.OK || result.WorkspaceID != "ws-process-004" || result.State != processworkspace.StatePrepared {
		t.Fatalf("command result=%+v", result)
	}
}

// TestFastWorkspaceInspectCommandMapsServiceError covers command-level error
// projection: a service failure maps to the inspect_failed error code.
func TestFastWorkspaceInspectCommandMapsServiceError(t *testing.T) {
	processBody := workspaceProcessBody(t, model.ProcessExecutionChangeBearing)
	backend := newWorkspaceCASBackend(processBody)
	app, out, _ := transitionAppWithError(backend)

	fake := newFakeWorkspaceService(t)
	lease := fastWritableLease("ws-process-004")
	if _, err := fake.store.Create(context.Background(), lease); err != nil {
		t.Fatalf("seed lease: %v", err)
	}
	fake.inspectFn = func(context.Context, string) (processworkspace.Inspection, error) {
		return processworkspace.Inspection{Lease: lease}, errors.New("scripted inspect failure")
	}
	app.openWorkspace = func(context.Context, string, string, processworkspace.ManagerOptions) (workspaceService, error) {
		return fake, nil
	}

	args := []string{"inspect", "--repo", "o/r", "--issue", "177", "--process", "PROCESS-004",
		"--integration-root", "/fake/integration", "--workspace-root", "/fake/managed", "--workspace-id", "ws-process-004", "--json"}
	if code := app.runWorkflowWorkspace(context.Background(), args); code == 0 {
		t.Fatalf("expected failure exit, out=%s", out.String())
	}
	result := decodeWorkspaceResult(t, out)
	if result.OK || result.Code != "inspect_failed" {
		t.Fatalf("error result=%+v want code=inspect_failed", result)
	}
}

func advisoryResultForPrepare(request processworkspace.PrepareRequest) processworkspace.Inspection {
	prepared := request.Lease
	prepared.Portable.State = processworkspace.StatePrepared
	return processworkspace.Inspection{Lease: prepared, OwnershipAdvisories: []processworkspace.OverlapAdvisory{{
		WorkspaceID: "ws-peer", ProcessID: "PROCESS-PEER",
		Overlaps: []processworkspace.OverlapAdvisoryEntry{
			{Entry: "internal/commands/**", PeerEntry: "internal/commands/peer.go"},
			{Entry: "internal/commands/**", PeerEntry: "docs/shared.md"},
		},
	}}}
}

func prepareArgs(jsonOut bool) []string {
	args := []string{"prepare", "--repo", "o/r", "--issue", "177", "--process", "PROCESS-004",
		"--integration-root", "/fake/integration", "--workspace-root", "/fake/managed",
		"--owner-token", "owner-secret", "--base", strings.Repeat("a", 40)}
	if jsonOut {
		args = append(args, "--json")
	}
	return args
}

// TestFastWorkspacePrepareCommandCarriesOwnershipAdvisories covers prepare
// success with advisory data: exit stays successful and the JSON result
// carries the other workspace identity, PROCESS, and overlapping entries.
func TestFastWorkspacePrepareCommandCarriesOwnershipAdvisories(t *testing.T) {
	processBody := workspaceProcessBody(t, model.ProcessExecutionChangeBearing)
	backend := newWorkspaceCASBackend(processBody)
	app, out, errOut := transitionAppWithError(backend)

	fake := newFakeWorkspaceService(t)
	fake.prepareFn = func(_ context.Context, request processworkspace.PrepareRequest) (processworkspace.Inspection, error) {
		return advisoryResultForPrepare(request), nil
	}
	app.openWorkspace = func(context.Context, string, string, processworkspace.ManagerOptions) (workspaceService, error) {
		return fake, nil
	}

	if code := app.runWorkflowWorkspace(context.Background(), prepareArgs(true)); code != 0 {
		t.Fatalf("prepare with advisory overlap must stay successful, code=%d out=%s err=%s", code, out.String(), errOut.String())
	}
	result := decodeWorkspaceResult(t, out)
	if !result.OK || result.Code != "" {
		t.Fatalf("prepare result=%+v", result)
	}
	want := []processworkspace.OverlapAdvisory{{
		WorkspaceID: "ws-peer", ProcessID: "PROCESS-PEER",
		Overlaps: []processworkspace.OverlapAdvisoryEntry{
			{Entry: "internal/commands/**", PeerEntry: "internal/commands/peer.go"},
			{Entry: "internal/commands/**", PeerEntry: "docs/shared.md"},
		},
	}}
	if !reflect.DeepEqual(result.OwnershipAdvisories, want) {
		t.Fatalf("advisories=%+v want %+v", result.OwnershipAdvisories, want)
	}
}

// TestFastWorkspacePrepareCommandPrintsInformationalAdvisoryLines covers the
// wording contract: human output prints one advisory line per overlapping
// workspace that names the peer identity, lists the overlapping entries, and
// states the integration-time consequence without instructing the reader to
// pause, stop, wait, or abandon the work.
func TestFastWorkspacePrepareCommandPrintsInformationalAdvisoryLines(t *testing.T) {
	processBody := workspaceProcessBody(t, model.ProcessExecutionChangeBearing)
	backend := newWorkspaceCASBackend(processBody)
	app, out, errOut := transitionAppWithError(backend)

	fake := newFakeWorkspaceService(t)
	fake.prepareFn = func(_ context.Context, request processworkspace.PrepareRequest) (processworkspace.Inspection, error) {
		return advisoryResultForPrepare(request), nil
	}
	app.openWorkspace = func(context.Context, string, string, processworkspace.ManagerOptions) (workspaceService, error) {
		return fake, nil
	}

	if code := app.runWorkflowWorkspace(context.Background(), prepareArgs(false)); code != 0 {
		t.Fatalf("human prepare with advisory overlap must stay successful, code=%d out=%s err=%s", code, out.String(), errOut.String())
	}
	line := advisoryLine(processworkspace.OverlapAdvisory{WorkspaceID: "ws-peer", ProcessID: "PROCESS-PEER",
		Overlaps: []processworkspace.OverlapAdvisoryEntry{
			{Entry: "internal/commands/**", PeerEntry: "internal/commands/peer.go"},
			{Entry: "internal/commands/**", PeerEntry: "docs/shared.md"},
		}})
	if !strings.Contains(out.String(), line) {
		t.Fatalf("human output missing advisory line %q: %s", line, out.String())
	}
	for _, forbidden := range []string{"pause", "stop", "wait", "abandon", "do not proceed"} {
		if strings.Contains(strings.ToLower(line), forbidden) {
			t.Fatalf("advisory line instructs the reader to %s: %q", forbidden, line)
		}
	}
}

// TestFastWorkspaceInspectCommandCarriesOwnershipAdvisories covers inspect
// success with advisory data: exit stays successful and the JSON result
// carries the same advisory projection as prepare.
func TestFastWorkspaceInspectCommandCarriesOwnershipAdvisories(t *testing.T) {
	processBody := workspaceProcessBody(t, model.ProcessExecutionChangeBearing)
	backend := newWorkspaceCASBackend(processBody)
	app, out, errOut := transitionAppWithError(backend)

	fake := newFakeWorkspaceService(t)
	lease := fastWritableLease("ws-process-004")
	if _, err := fake.store.Create(context.Background(), lease); err != nil {
		t.Fatalf("seed lease: %v", err)
	}
	prepared := lease
	prepared.Portable.State = processworkspace.StatePrepared
	fake.inspectFn = func(context.Context, string) (processworkspace.Inspection, error) {
		return processworkspace.Inspection{Lease: prepared, OwnershipAdvisories: []processworkspace.OverlapAdvisory{{
			WorkspaceID: "ws-peer", ProcessID: "PROCESS-PEER",
			Overlaps: []processworkspace.OverlapAdvisoryEntry{{Entry: "internal/commands/**", PeerEntry: "internal/commands/peer.go"}},
		}}}, nil
	}
	app.openWorkspace = func(context.Context, string, string, processworkspace.ManagerOptions) (workspaceService, error) {
		return fake, nil
	}

	args := []string{"inspect", "--repo", "o/r", "--issue", "177", "--process", "PROCESS-004",
		"--integration-root", "/fake/integration", "--workspace-root", "/fake/managed", "--workspace-id", "ws-process-004", "--json"}
	if code := app.runWorkflowWorkspace(context.Background(), args); code != 0 {
		t.Fatalf("inspect with advisory overlap must stay successful, code=%d out=%s err=%s", code, out.String(), errOut.String())
	}
	result := decodeWorkspaceResult(t, out)
	if !result.OK || len(result.OwnershipAdvisories) != 1 || result.OwnershipAdvisories[0].WorkspaceID != "ws-peer" ||
		result.OwnershipAdvisories[0].ProcessID != "PROCESS-PEER" || len(result.OwnershipAdvisories[0].Overlaps) != 1 {
		t.Fatalf("inspect result advisories=%+v", result.OwnershipAdvisories)
	}
}
