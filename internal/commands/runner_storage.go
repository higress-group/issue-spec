package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/higress-group/issue-spec/internal/commentrunner"
	"github.com/higress-group/issue-spec/internal/commentrunner/jobs"
	crstate "github.com/higress-group/issue-spec/internal/commentrunner/state"
	"github.com/higress-group/issue-spec/internal/commentrunner/storage"
	"github.com/higress-group/issue-spec/internal/processworkspace"
)

// runnerStorageService builds the shared storage service for one runner
// configuration and state store. Every destructive storage entry point routes
// through this one composition.
func runnerStorageService(cfg commentrunner.Config, store crstate.StateStore) (*storage.Service, error) {
	return runnerStorageServiceWithLoader(cfg, func(ctx context.Context) (crstate.RunnerState, error) {
		return store.Load(ctx)
	})
}

// runnerStorageServiceWithLoader builds the shared storage service around an
// arbitrary state loader so entry points without a long-lived store handle
// (sync poll) still share one service for the owner lifetime.
func runnerStorageServiceWithLoader(cfg commentrunner.Config, loader storage.StateLoader) (*storage.Service, error) {
	return storage.NewService(storage.ServiceConfig{
		WorkspaceRoot: cfg.WorkspaceRoot,
		StateLoader:   loader,
		PoolInspector: func(ctx context.Context, integrationRoot, poolRoot string) (storage.PoolInspection, error) {
			return processworkspace.InspectPool(ctx, integrationRoot, poolRoot, processworkspace.ManagerOptions{})
		},
		PoolRemover: func(ctx context.Context, integrationRoot, poolRoot string) (storage.PoolInspection, bool, error) {
			return processworkspace.RemoveEmptyPool(ctx, integrationRoot, poolRoot, processworkspace.ManagerOptions{})
		},
		RawStatePath: cfg.StatePath,
		MinFreeBytes: cfg.StorageMinFreeBytes,
		OrphanGrace:  cfg.StorageOrphanGrace.Duration,
	})
}

// runnerStorageLifecycle adapts the service to the dispatcher seam. A shared
// service from the context is reused across all entry points of one run. A
// construction failure stays fail-closed: admission, recording, and
// reconciliation all surface the error instead of silently disabling storage.
func runnerStorageLifecycle(ctx context.Context, cfg commentrunner.Config, store crstate.StateStore) jobs.StorageLifecycle {
	if shared, ok := storage.ServiceFromContext(ctx, cfg.WorkspaceRoot); ok {
		return shared
	}
	service, err := runnerStorageService(cfg, store)
	if err != nil {
		return failingStorageLifecycle{err: err}
	}
	return service
}

type failingStorageLifecycle struct{ err error }

func (f failingStorageLifecycle) AdmitDispatch(context.Context) error { return f.err }
func (f failingStorageLifecycle) RecordSessionResources(context.Context, string, string, string) error {
	return f.err
}
func (f failingStorageLifecycle) RecordSessionProcessPool(context.Context, string, string, string) error {
	return f.err
}
func (f failingStorageLifecycle) RecordRuntimeHome(context.Context, storage.RuntimeScope, storage.RuntimeHomePaths) error {
	return f.err
}
func (f failingStorageLifecycle) RecordJobScratch(context.Context, string, string, string) error {
	return f.err
}
func (f failingStorageLifecycle) CompleteJobScratch(context.Context, string, string) error {
	return f.err
}
func (f failingStorageLifecycle) ReconcileStorage(context.Context, bool, bool) (storage.Report, error) {
	return storage.Report{}, f.err
}
func (f failingStorageLifecycle) ReconcileJobScratch(context.Context, bool) (storage.RuntimeReconcileReport, error) {
	return storage.RuntimeReconcileReport{}, f.err
}

func (a *app) runRunnerStorage(ctx context.Context, args []string) int {
	if len(args) == 0 {
		a.printRunnerStorageUsage(a.err)
		return 2
	}
	if isHelpArg(args[0]) {
		a.printRunnerStorageUsage(a.out)
		return 0
	}
	switch args[0] {
	case "reconcile":
		return a.runRunnerStorageReconcile(ctx, args[1:])
	default:
		a.errorf("unknown runner storage command %q\n", args[0])
		return 2
	}
}

func (a *app) printRunnerStorageUsage(out io.Writer) {
	fmt.Fprintln(out, `Usage:
  issue-spec runner storage reconcile [options] --dry-run|--apply

Reconcile runner-managed storage (.sessions runtimes, .process-workspaces
pools, .runner-home shared runtime homes, and .job-scratch directories)
against current state using the shared classification and deletion
engine. Performs no issue polling. Defaults resolve through the same runner
scope path logic as runner poll.

Options:
  --state <path>           runner state path (default: repository/runner-scoped)
  --workspace-root <path>  managed workspace root (default: beside state path)
  --repo owner/name        repository for scoped defaults; repeatable
  --runner <login>         runner identity for scoped defaults
  --hostname <host>        GitHub hostname for scoped defaults
  --profile <name>         issue backend profile for scoped defaults
  --storage-orphan-grace <dur>  orphan observation window (default 168h)
  --dry-run                report classifications and would-delete actions only
  --apply                  apply recoverable deletions
  --evict-caches           with --apply: also reconcile stale job scratch and
                           evict rebuildable runtime caches, printing reclaimed bytes
  --json                   write the JSON report; the schema is stable: the
                           reconcile report is always nested under "report",
                           with "runtime" and "eviction" sections present only
                           when runtime data or --evict-caches results exist`)
}

func (a *app) runRunnerStorageReconcile(ctx context.Context, args []string) int {
	fs := newFlagSet("runner storage reconcile", a.err)
	var repoValues stringListFlag
	hostname := fs.String("hostname", "github.com", "GitHub hostname for scoped defaults")
	profile := fs.String("profile", "", "named issue backend profile for scoped defaults")
	runner := fs.String("runner", "", "runner identity for scoped defaults")
	statePath := fs.String("state", "", "runner state path")
	workspaceRoot := fs.String("workspace-root", "", "managed workspace root")
	orphanGrace := fs.Duration("storage-orphan-grace", storage.DefaultOrphanGrace, "orphan observation window before unmatched runtime deletion")
	dryRun := fs.Bool("dry-run", false, "report only, no mutations")
	apply := fs.Bool("apply", false, "apply recoverable deletions")
	evictCaches := fs.Bool("evict-caches", false, "with --apply: reconcile stale job scratch and evict rebuildable runtime caches")
	jsonOut := fs.Bool("json", false, "write JSON output (stable schema: report nested under \"report\", optional runtime/eviction sections)")
	fs.Var(&repoValues, "repo", "repository owner/name for scoped defaults; repeat or comma-separate")
	if argsContainHelp(args) {
		fs.SetOutput(a.out)
		fs.Usage()
		return 0
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dryRun == *apply {
		a.errorf("runner storage reconcile requires exactly one of --dry-run or --apply\n")
		return 2
	}
	if *evictCaches && !*apply {
		a.errorf("runner storage reconcile --evict-caches requires --apply\n")
		return 2
	}
	if *orphanGrace < 0 {
		a.errorf("--storage-orphan-grace must not be negative\n")
		return 2
	}
	seen := visitedFlags(fs)

	cfg, err := commentrunner.DefaultConfigFromEnv()
	if err != nil {
		a.errorf("%v\n", err)
		return 2
	}
	if a.profileName != "" {
		cfg.Profile = a.profileName
	}
	if seen["profile"] {
		cfg.Profile = strings.TrimSpace(*profile)
	}
	if seen["hostname"] {
		cfg.Hostname = strings.TrimSpace(*hostname)
	}
	if seen["repo"] {
		cfg.Repositories = repoValues.Values()
	}
	if seen["runner"] {
		cfg.RunnerIdentity = strings.TrimSpace(*runner)
	}
	if seen["state"] {
		cfg.StatePath = strings.TrimSpace(*statePath)
	}
	if seen["workspace-root"] {
		cfg.WorkspaceRoot = strings.TrimSpace(*workspaceRoot)
	}
	cfg.StorageOrphanGrace = commentrunner.NewDuration(*orphanGrace)
	cfg, err = commentrunner.ApplyDefaultRunnerScopePaths(cfg, seen["state"], seen["workspace-root"])
	if err != nil {
		a.errorf("runner storage reconcile scope: %v\n", err)
		return 2
	}
	if strings.TrimSpace(cfg.StatePath) == "" || strings.TrimSpace(cfg.WorkspaceRoot) == "" {
		a.errorf("runner storage reconcile requires --state and --workspace-root or scoped --repo/--runner defaults\n")
		return 2
	}

	owner, err := storage.AcquireOwner(cfg.WorkspaceRoot)
	if err != nil {
		if errors.Is(err, storage.ErrOwnerLocked) {
			a.errorf("runner storage reconcile: %v\n", err)
			return 1
		}
		a.errorf("runner storage reconcile owner: %v\n", err)
		return 1
	}
	defer owner.Release()
	ctx = storage.WithOwner(ctx, owner)

	// Preserve the exact raw bytes before any current-binary save normalizes or
	// compacts legacy state. Applied maintenance then persists current retention
	// semantics so the same invocation can retire newly pruned resources.
	if _, err := storage.EnsureRawStateBackup(cfg.WorkspaceRoot, cfg.StatePath); err != nil {
		a.errorf("runner storage reconcile backup: %v\n", err)
		return 1
	}
	store, err := crstate.OpenFileStore(cfg.StatePath)
	if err != nil {
		a.errorf("runner storage reconcile state: %v\n", err)
		return 1
	}
	defer store.Close()
	service, err := runnerStorageService(cfg, store)
	if err != nil {
		a.errorf("runner storage reconcile: %v\n", err)
		return 1
	}
	defer service.Close()
	if *apply {
		current, err := store.Load(ctx)
		if err != nil {
			a.errorf("runner storage reconcile state migration: %v\n", err)
			return 1
		}
		// Seed exact ownership while legacy sessions are still present. The
		// subsequent Save may prune them, after which the sidecar is the durable
		// ownership proof used by this same applied reconciliation.
		for _, session := range current.PublicSessions {
			if strings.TrimSpace(session.Workspace.Path) == "" {
				continue
			}
			if err := service.RecordSessionResources(ctx, session.Repo, session.PublicSessionID, session.Workspace.Path); err != nil {
				a.errorf("runner storage reconcile state migration inventory: %v\n", err)
				return 1
			}
		}
		if err := store.Save(ctx, current); err != nil {
			a.errorf("runner storage reconcile state migration: %v\n", err)
			return 1
		}
	}
	report, err := service.ReconcileStorage(ctx, *apply, true)
	if err != nil {
		a.errorf("runner storage reconcile: %v\n", err)
		return 1
	}
	var eviction *runtimeEvictionReport
	if *evictCaches {
		eviction = &runtimeEvictionReport{}
		scratchReport, err := service.ReconcileJobScratch(ctx, true)
		if err != nil {
			a.errorf("runner storage reconcile job scratch: %v\n", err)
			return 1
		}
		eviction.Scratch = scratchReport
		cacheReport, err := service.EvictRuntimeCaches(ctx, true)
		if err != nil {
			a.errorf("runner storage reconcile cache eviction: %v\n", err)
			return 1
		}
		eviction.Caches = cacheReport
	}
	runtimeUsage := collectRuntimeUsage(service)
	if *jsonOut {
		return a.outputJSON(runnerStorageReconcileOutput{Report: report, Runtime: runtimeUsage, Eviction: eviction})
	}
	printStorageReport(a.out, report, runtimeUsage, eviction)
	if report.ReportOnly {
		return 1
	}
	return 0
}

// runnerStorageReconcileOutput is the stable --json schema of runner storage
// reconcile: the main report is always nested under "report", and the
// "runtime"/"eviction" sections appear only when runtime home measurements or
// an --evict-caches pass exist. Consumers always decode the same top-level
// shape regardless of which sections a given run produced.
type runnerStorageReconcileOutput struct {
	Report   storage.Report         `json:"report"`
	Runtime  *runtimeUsageSection   `json:"runtime,omitempty"`
	Eviction *runtimeEvictionReport `json:"eviction,omitempty"`
}

// runtimeUsageSection is the measured runtime view: one line per recorded
// runner-scoped shared home plus total job-scratch bytes. It carries byte
// counts only, never file contents.
type runtimeUsageSection struct {
	Homes        []runtimeHomeUsageLine `json:"homes,omitempty"`
	ScratchBytes int64                  `json:"scratch_bytes"`
	Diagnostics  []string               `json:"diagnostics,omitempty"`
}

type runtimeHomeUsageLine struct {
	Hash           string `json:"hash"`
	Repo           string `json:"repo"`
	ProtectedBytes int64  `json:"protected_bytes"`
	CacheBytes     int64  `json:"cache_bytes"`
	UnknownBytes   int64  `json:"unknown_bytes"`
}

// runtimeEvictionReport carries the --evict-caches pass results: stale job
// scratch reconciliation plus runtime cache eviction.
type runtimeEvictionReport struct {
	Scratch storage.RuntimeReconcileReport `json:"job_scratch"`
	Caches  storage.RuntimeReconcileReport `json:"caches"`
}

// collectRuntimeUsage measures every recorded runner home and the job scratch
// base. It returns nil when no runner home records exist, so legacy-layout
// roots keep their previous human output and simply omit the JSON runtime
// section. Measurement failures degrade to diagnostics: the reconcile pass
// already succeeded and its report stays valid.
func collectRuntimeUsage(service *storage.Service) *runtimeUsageSection {
	records := make([]storage.PhysicalResource, 0)
	for _, resource := range service.Store().State().Resources {
		if resource.Kind == storage.ResourceKindRunnerHome {
			records = append(records, resource)
		}
	}
	if len(records) == 0 {
		return nil
	}
	sort.Slice(records, func(i, j int) bool { return records[i].ID < records[j].ID })
	section := &runtimeUsageSection{}
	for _, record := range records {
		usage, err := storage.MeasureRuntimeHome(record.Path)
		if err != nil {
			section.Diagnostics = append(section.Diagnostics, fmt.Sprintf("runtime home %s measurement: %v", record.PhysicalHash, err))
			continue
		}
		section.Homes = append(section.Homes, runtimeHomeUsageLine{
			Hash:           record.PhysicalHash,
			Repo:           record.Repo,
			ProtectedBytes: usage.ProtectedBytes,
			CacheBytes:     usage.CacheBytes,
			UnknownBytes:   usage.UnknownBytes,
		})
	}
	scratch, err := storage.MeasureJobScratch(service.Root())
	if err != nil {
		section.Diagnostics = append(section.Diagnostics, fmt.Sprintf("job scratch measurement: %v", err))
	} else {
		section.ScratchBytes = scratch
	}
	return section
}

func printStorageReport(out io.Writer, report storage.Report, runtimeUsage *runtimeUsageSection, eviction *runtimeEvictionReport) {
	mode := "apply"
	if report.DryRun {
		mode = "dry-run"
	}
	fmt.Fprintf(out, "storage reconcile: mode=%s sidecar=%s root=%s\n", mode, report.SidecarStatus, report.RootIdentity)
	fmt.Fprintf(out, "classes: protected=%d retired_known=%d orphan_observed=%d rejected=%d\n",
		report.CountByClass(storage.ClassProtected), report.CountByClass(storage.ClassRetiredKnown),
		report.CountByClass(storage.ClassOrphanObserved), report.CountByClass(storage.ClassRejected))
	fmt.Fprintf(out, "bytes: protected=%d retired_known=%d orphan_observed=%d rejected=%d\n",
		report.BytesByClass(storage.ClassProtected), report.BytesByClass(storage.ClassRetiredKnown),
		report.BytesByClass(storage.ClassOrphanObserved), report.BytesByClass(storage.ClassRejected))
	if report.ReclaimedBytes > 0 {
		fmt.Fprintf(out, "reclaimed: %d bytes\n", report.ReclaimedBytes)
	}
	for _, resource := range report.Resources {
		line := fmt.Sprintf("- %s %s class=%s action=%s", resource.ID, resource.Kind, resource.Class, resource.Action)
		if resource.Reason != "" {
			line += " reason=" + resource.Reason
		}
		if resource.AttemptID != "" {
			line += " attempt=" + resource.AttemptID
		}
		fmt.Fprintln(out, line)
	}
	for _, diagnostic := range report.Diagnostics {
		fmt.Fprintf(out, "diagnostic: %s\n", diagnostic)
	}
	if runtimeUsage != nil {
		fmt.Fprintf(out, "runtime: runner_homes=%d job_scratch=%d bytes\n", len(runtimeUsage.Homes), runtimeUsage.ScratchBytes)
		for _, home := range runtimeUsage.Homes {
			fmt.Fprintf(out, "- runner_home %s repo=%s protected=%d cache=%d unknown=%d\n",
				home.Hash, home.Repo, home.ProtectedBytes, home.CacheBytes, home.UnknownBytes)
		}
		for _, diagnostic := range runtimeUsage.Diagnostics {
			fmt.Fprintf(out, "diagnostic: %s\n", diagnostic)
		}
	}
	if eviction != nil {
		reclaimed := eviction.Scratch.ReclaimedBytes + eviction.Caches.ReclaimedBytes
		fmt.Fprintf(out, "evict-caches: reclaimed=%d bytes scratch_removed=%d caches_evicted=%d\n",
			reclaimed, len(eviction.Scratch.ScratchRemoved), len(eviction.Caches.CacheEvicted))
		for _, diagnostic := range eviction.Scratch.Diagnostics {
			fmt.Fprintf(out, "diagnostic: %s\n", diagnostic)
		}
		for _, diagnostic := range eviction.Caches.Diagnostics {
			fmt.Fprintf(out, "diagnostic: %s\n", diagnostic)
		}
	}
}
