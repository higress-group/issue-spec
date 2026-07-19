package processworkspace

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/higress-group/issue-spec/internal/assignment"
)

const (
	baseSHA        = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	resultSHA      = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	integrationSHA = "cccccccccccccccccccccccccccccccccccccccc"
)

func assignmentDesignContext() *assignment.DesignContext {
	return &assignment.DesignContext{
		SourceURL: "https://github.com/higress-group/issue-spec/issues/296", ReadMode: assignment.DesignReadModeCompleteIssueBody,
		Invariant: "Stored role assignments preserve authoritative Design context.", ApplicableDecisions: []string{"D14"},
		ImplementationDirection: "Keep issuance strict while preserving historical storage readability.",
		MustPreserve:            []string{"canonical assignment authority"}, MustNot: []string{"synthesize design context"},
		MinimumVerification: []string{"focused processworkspace tests"}, ConflictPolicy: assignment.DesignConflictPolicyAuthoritativeStop,
	}
}

func TestPortableLeaseValidationAndLifecycle(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	lease := portableLease("ws-1", "PROCESS-001", "branch-1", []string{"internal/processworkspace/**"}, now)
	if err := lease.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := lease.Transition(StatePrepared, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	lease.ResultCommit = resultSHA
	if err := lease.Transition(StateWorkerComplete, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := lease.Transition(StateIntegrating, now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	lease.IntegrationSHA = integrationSHA
	if err := lease.Transition(StateIntegrated, now.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := lease.Transition(StateCleanupPending, now.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := lease.Transition(StateCleaned, now.Add(6*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if CanTransition(StateCleaned, StatePreparing, ModeWritable) {
		t.Fatal("cleaned lease must be terminal")
	}

	illegal := portableLease("ws-2", "PROCESS-002", "branch-2", []string{"internal/**"}, now)
	illegal.IntegrationSHA = integrationSHA
	if err := illegal.Transition(StateIntegrated, now.Add(time.Minute)); err == nil {
		t.Fatal("prepared evidence was bypassed")
	}
	if illegal.State != StatePreparing {
		t.Fatalf("failed transition mutated state to %s", illegal.State)
	}
}

func TestPortableLeaseModesAndStateEvidenceFailClosed(t *testing.T) {
	now := time.Unix(2000, 0).UTC()
	tests := []struct {
		name  string
		lease PortableLease
	}{
		{name: "review cannot be writable", lease: PortableLease{SchemaVersion: 1, WorkspaceID: "ws", Repository: "o/r", ProcessID: "PROCESS-001", ExecutionClass: ExecutionReview, Mode: ModeWritable, BaseSHA: baseSHA, Branch: "review", WriteOwnership: []string{"internal/**"}, RuntimeNamespace: "review", State: StatePreparing, CreatedAt: now, UpdatedAt: now}},
		{name: "snapshot revision must equal base", lease: PortableLease{SchemaVersion: 1, WorkspaceID: "ws", Repository: "o/r", ProcessID: "PROCESS-001", ExecutionClass: ExecutionReview, Mode: ModeSnapshot, BaseSHA: baseSHA, DetachedRevision: resultSHA, RuntimeNamespace: "review", State: StatePreparing, CreatedAt: now, UpdatedAt: now}},
		{name: "worker complete requires result", lease: func() PortableLease {
			lease := portableLease("ws", "PROCESS-001", "branch", []string{"internal/**"}, now)
			lease.State = StateWorkerComplete
			return lease
		}()},
		{name: "integrated requires integration sha", lease: func() PortableLease {
			lease := portableLease("ws", "PROCESS-001", "branch", []string{"internal/**"}, now)
			lease.State = StateIntegrated
			lease.ResultCommit = resultSHA
			return lease
		}()},
		{name: "ownership must already be normalized", lease: portableLease("ws", "PROCESS-001", "branch", []string{"z.go", "a.go"}, now)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.lease.Validate(); err == nil {
				t.Fatalf("invalid lease accepted: %+v", test.lease)
			}
		})
	}

	snapshot := PortableLease{SchemaVersion: 1, WorkspaceID: "review-1", Repository: "o/r", ProcessID: "PROCESS-009", ExecutionClass: ExecutionReview,
		Mode: ModeSnapshot, BaseSHA: baseSHA, DetachedRevision: baseSHA, RuntimeNamespace: "review-1", State: StatePreparing, CreatedAt: now, UpdatedAt: now}
	if err := snapshot.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := snapshot.Transition(StatePrepared, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := snapshot.Transition(StateWorkerComplete, now.Add(2*time.Minute)); err == nil {
		t.Fatal("snapshot entered worker-complete state")
	}
}

func TestExternalExecutionRequiresNoCheckoutWorkspaceMode(t *testing.T) {
	now := time.Unix(2300, 0).UTC()
	base := PortableLease{SchemaVersion: LeaseSchemaVersion, WorkspaceID: "external-1", Repository: "o/r", ProcessID: "PROCESS-EXT",
		ExecutionClass: ExecutionExternal, Mode: ModeNone, State: StatePreparing, CreatedAt: now, UpdatedAt: now}
	tests := []struct {
		name    string
		mode    WorkspaceMode
		mutate  func(*PortableLease)
		wantErr string
	}{
		{name: "none", mode: ModeNone},
		{name: "writable", mode: ModeWritable, wantErr: "external execution requires no-checkout workspace mode", mutate: func(lease *PortableLease) {
			lease.BaseSHA, lease.Branch = baseSHA, "external-write"
			lease.WriteOwnership, lease.RuntimeNamespace = []string{"internal/**"}, "external-write"
		}},
		{name: "snapshot", mode: ModeSnapshot, wantErr: "external execution requires no-checkout workspace mode", mutate: func(lease *PortableLease) {
			lease.BaseSHA, lease.DetachedRevision, lease.RuntimeNamespace = baseSHA, baseSHA, "external-snapshot"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			lease := base
			lease.Mode = test.mode
			if test.mutate != nil {
				test.mutate(&lease)
			}
			err := lease.Validate()
			if test.wantErr == "" && err != nil {
				t.Fatalf("external no-checkout lease rejected: %v", err)
			}
			if test.wantErr != "" && (err == nil || err.Error() != test.wantErr) {
				t.Fatalf("external %s mode error=%v want=%q", test.mode, err, test.wantErr)
			}
		})
	}
}

func TestLocalLeaseRequiresPathForObservedOrWorkerEvidence(t *testing.T) {
	now := time.Unix(2400, 0).UTC()
	for _, mutate := range []func(*LocalLease){
		func(lease *LocalLease) { lease.Observation.Registered = true },
		func(lease *LocalLease) { lease.Portable.ResultCommit = resultSHA },
		func(lease *LocalLease) { lease.Integration.ExpectedHead = baseSHA },
	} {
		lease := LocalLease{
			Portable:        portableLease("ws-evidence", "PROCESS-001", "branch", []string{"internal/**"}, now),
			IntegrationRoot: filepath.Clean(t.TempDir()),
			Owner:           LeaseOwner{CoordinatorID: "coordinator", Token: "token", AcquiredAt: now},
			LocalRevision:   1,
		}
		lease.Portable.State = StateCleanupPending
		mutate(&lease)
		if err := lease.Validate(); err == nil {
			t.Fatalf("worktree evidence accepted without local path: %+v", lease)
		}
	}
}

func TestWritableBranchValidationRejectsUnsafeGitRefs(t *testing.T) {
	now := time.Unix(2500, 0).UTC()
	for _, branch := range []string{
		"control\nchar",
		"feature/.hidden",
		"feature/component.lock",
		"feature/..",
		"feature//double",
		"@",
		"-option",
		"feature/trailing.",
		"feature/@{upstream}",
	} {
		t.Run(strings.ReplaceAll(branch, "/", "_"), func(t *testing.T) {
			lease := portableLease("ws-branch", "PROCESS-001", branch, []string{"internal/**"}, now)
			if err := lease.Validate(); err == nil {
				t.Fatalf("unsafe branch %q accepted", branch)
			}
		})
	}
	for _, branch := range []string{"feature/process-001", "codex/175-p001-lease", "release.v1"} {
		lease := portableLease("ws-valid", "PROCESS-001", branch, []string{"internal/**"}, now)
		if err := lease.Validate(); err != nil {
			t.Fatalf("valid branch %q rejected: %v", branch, err)
		}
	}
}

func TestOwnershipNormalizationContainmentAndOverlap(t *testing.T) {
	normalized, err := NormalizeOwnership([]string{"internal/z.go", "internal/pkg/**", "internal/z.go"})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(normalized, ","); got != "internal/pkg/**,internal/z.go" {
		t.Fatalf("normalized=%q", got)
	}
	for _, invalid := range []string{"", "/absolute", "../escape", "internal/*/bad", `internal\windows`} {
		if _, err := NormalizeOwnership([]string{invalid}); err == nil {
			t.Fatalf("accepted ownership %q", invalid)
		}
	}
	owned, err := OwnsPath([]string{"internal/pkg/**"}, "internal/pkg/file.go")
	if err != nil || !owned {
		t.Fatalf("owned=%v err=%v", owned, err)
	}
	owned, err = OwnsPath([]string{"internal/pkg/**"}, "internal/other.go")
	if err != nil || owned {
		t.Fatalf("owned=%v err=%v", owned, err)
	}
	overlap, err := OwnershipOverlaps([]string{"internal/pkg/**"}, []string{"internal/pkg/file.go"})
	if err != nil || !overlap {
		t.Fatalf("overlap=%v err=%v", overlap, err)
	}
	overlap, err = OwnershipOverlaps([]string{"internal/a/**"}, []string{"internal/b/**"})
	if err != nil || overlap {
		t.Fatalf("overlap=%v err=%v", overlap, err)
	}
}

func TestRegistryRejectsActiveLeaseCollisions(t *testing.T) {
	now := time.Unix(3000, 0).UTC()
	root := t.TempDir()
	left := localPreparedLease(t, root, "ws-a", "PROCESS-001", "branch-a", "worktree-a", []string{"internal/a/**"}, now)
	right := localPreparedLease(t, root, "ws-b", "PROCESS-002", "branch-b", "worktree-b", []string{"internal/b/**"}, now)
	base := NewRegistry()
	base.Leases[left.Portable.WorkspaceID] = left
	base.Leases[right.Portable.WorkspaceID] = right
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		mutate func(*LocalLease)
	}{
		{name: "branch", mutate: func(lease *LocalLease) { lease.Portable.Branch = left.Portable.Branch }},
		{name: "path", mutate: func(lease *LocalLease) { lease.WorktreePath = left.WorktreePath }},
		{name: "ownership", mutate: func(lease *LocalLease) { lease.Portable.WriteOwnership = []string{"internal/a/file.go"} }},
		{name: "exclusive resource", mutate: func(lease *LocalLease) {
			left.Portable.RuntimeResources = []RuntimeResource{{Kind: "port", Name: "api", Exclusive: true}}
			lease.Portable.RuntimeResources = []RuntimeResource{{Kind: "port", Name: "api"}}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := cloneLocalLease(right)
			test.mutate(&candidate)
			registry := NewRegistry()
			registry.Leases[left.Portable.WorkspaceID] = cloneLocalLease(left)
			registry.Leases[candidate.Portable.WorkspaceID] = candidate
			if err := registry.Validate(); err == nil {
				t.Fatalf("collision %s accepted", test.name)
			}
		})
	}

	left.Portable.IntegrationOwner = "generator"
	right.Portable.IntegrationOwner = "generator"
	right.Portable.WriteOwnership = []string{"internal/a/file.go"}
	registry := NewRegistry()
	registry.Leases[left.Portable.WorkspaceID] = left
	registry.Leases[right.Portable.WorkspaceID] = right
	if err := registry.Validate(); err != nil {
		t.Fatalf("shared integration owner should permit declared overlap: %v", err)
	}
}

func TestRegistryRejectsMixedRepositoryPhysicalConflicts(t *testing.T) {
	now := time.Unix(3500, 0).UTC()
	root := t.TempDir()
	left := localPreparedLease(t, root, "ws-a", "PROCESS-001", "branch-a", "worker-a", []string{"internal/a/**"}, now)
	right := localPreparedLease(t, root, "ws-b", "PROCESS-002", "branch-b", "worker-b", []string{"internal/b/**"}, now)
	right.Portable.Repository = "other/repository"

	for _, mutate := range []func(*LocalLease){
		func(lease *LocalLease) { lease.WorktreePath = left.WorktreePath },
		func(lease *LocalLease) { lease.Portable.Branch = left.Portable.Branch },
		func(*LocalLease) {},
	} {
		candidate := cloneLocalLease(right)
		mutate(&candidate)
		registry := NewRegistry()
		registry.Leases[left.Portable.WorkspaceID] = cloneLocalLease(left)
		registry.Leases[candidate.Portable.WorkspaceID] = candidate
		if err := registry.Validate(); err == nil {
			t.Fatal("one common-dir registry accepted mixed repository leases")
		}
	}
}

func TestPortableLeaseJSONContainsNoMachinePaths(t *testing.T) {
	lease := portableLease("ws-1", "PROCESS-001", "branch", []string{"internal/**"}, time.Unix(4000, 0).UTC())
	data, err := json.Marshal(lease)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"worktree_path", "integration_root", filepath.Clean(t.TempDir())} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("portable JSON leaked %q: %s", forbidden, data)
		}
	}
}

func portableLease(workspaceID, processID, branch string, ownership []string, now time.Time) PortableLease {
	return PortableLease{
		SchemaVersion: LeaseSchemaVersion, WorkspaceID: workspaceID, Repository: "o/r", ProcessID: processID,
		ExecutionClass: ExecutionChangeBearing, Mode: ModeWritable, BaseSHA: baseSHA, Branch: branch,
		WriteOwnership: ownership, RuntimeNamespace: workspaceID, State: StatePreparing, CreatedAt: now, UpdatedAt: now,
	}
}

func localPreparedLease(t *testing.T, root, workspaceID, processID, branch, directory string, ownership []string, now time.Time) LocalLease {
	t.Helper()
	portable := portableLease(workspaceID, processID, branch, ownership, now)
	portable.State = StatePrepared
	return LocalLease{
		Portable: portable, IntegrationRoot: filepath.Clean(root), WorktreePath: filepath.Join(root, directory),
		Owner: LeaseOwner{CoordinatorID: "coordinator", Token: "token-" + workspaceID, AcquiredAt: now}, LocalRevision: 1,
	}
}
