package processworkspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/higress-group/issue-spec/internal/assignment"
)

const (
	LeaseSchemaVersion    = 1
	RegistrySchemaVersion = 1
)

type ExecutionClass string

const (
	ExecutionChangeBearing ExecutionClass = "change-bearing"
	ExecutionReview        ExecutionClass = "review"
	ExecutionVerification  ExecutionClass = "verification"
	ExecutionOrchestration ExecutionClass = "orchestration"
	ExecutionExternal      ExecutionClass = "external"
)

type WorkspaceMode string

const (
	ModeWritable WorkspaceMode = "writable"
	ModeSnapshot WorkspaceMode = "snapshot"
	ModeNone     WorkspaceMode = "none"
)

type LifecycleState string

const (
	StatePreparing      LifecycleState = "preparing"
	StatePrepared       LifecycleState = "prepared"
	StateWorkerComplete LifecycleState = "worker-complete"
	StateIntegrating    LifecycleState = "integrating"
	StateIntegrated     LifecycleState = "integrated"
	StateConflicted     LifecycleState = "conflicted"
	StateCleanupPending LifecycleState = "cleanup-pending"
	StateCleaned        LifecycleState = "cleaned"
)

type RuntimeResource struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Exclusive bool   `json:"exclusive,omitempty"`
}

// AssignmentBinding is the durable, portable identity of the role packet
// issued for a lease. The complete assignment is retained in LocalLease;
// machine-local delivery metadata is never part of this projection or digest.
type AssignmentBinding struct {
	SchemaVersion   string          `json:"schema_version"`
	AssignmentID    string          `json:"assignment_id"`
	Digest          string          `json:"assignment_digest"`
	Role            assignment.Role `json:"role"`
	BaseRevision    string          `json:"base_revision,omitempty"`
	SubjectRevision string          `json:"subject_revision,omitempty"`
	Generation      uint64          `json:"generation"`
}

// AcceptedReceiptBinding is the append-only portable authority retained after
// an implementation receipt has been validated. The full receipt is
// intentionally absent: only its immutable identity is projected to PROCESS.
type AcceptedReceiptBinding struct {
	ReceiptID            string                       `json:"receipt_id"`
	ReceiptDigest        string                       `json:"receipt_digest"`
	AssignmentGeneration uint64                       `json:"assignment_generation"`
	Submission           *RoleOwnedSubmissionEvidence `json:"submission,omitempty"`
}

const (
	AgentSessionSourceRuntimeNative = "CODEX_THREAD_ID"
	AgentSessionSourceParameter     = "agent-session-parameter"
	AgentSessionSourceCompatibility = "role-owned-compatibility"
)

// RoleOwnedSubmissionEvidence records self-reported metadata from a bounded
// role-owned command. Agent and session strings identify the publication
// route for compatibility; none of them is an independent provenance trust
// root or runtime attestation.
type RoleOwnedSubmissionEvidence struct {
	Agent              string               `json:"agent"`
	AgentSessionID     string               `json:"agent_session_id,omitempty"`
	AgentSessionSource string               `json:"agent_session_source"`
	Assurance          assignment.Assurance `json:"assurance"`
}

func (e RoleOwnedSubmissionEvidence) Validate() error {
	agent := strings.TrimSpace(e.Agent)
	if agent == "" || agent != e.Agent || strings.EqualFold(agent, "Coordinator") {
		return errors.New("role-owned submission requires one trimmed non-Coordinator agent")
	}
	if e.Assurance != assignment.AssuranceSelfReported {
		return errors.New("role-owned submission evidence must remain self-reported")
	}
	sessionID := strings.TrimSpace(e.AgentSessionID)
	if sessionID != e.AgentSessionID {
		return errors.New("role-owned submission session id must be trimmed")
	}
	switch e.AgentSessionSource {
	case AgentSessionSourceRuntimeNative, AgentSessionSourceParameter:
		if sessionID == "" {
			return errors.New("role-owned submission session source requires a session id")
		}
	case AgentSessionSourceCompatibility:
		if sessionID != "" {
			return errors.New("no-runtime role-owned compatibility cannot claim a session id")
		}
	default:
		return fmt.Errorf("unsupported role-owned submission session source %q", e.AgentSessionSource)
	}
	return nil
}

// ValidateRoleOwnedReceiptSubmission validates the existing direct role-owned
// publication compatibility route. The agent/session fields remain
// self-reported metadata and this helper must never authorize Coordinator
// import of caller-supplied receipt JSON.
func ValidateRoleOwnedReceiptSubmission(receipt assignment.Receipt, submission RoleOwnedSubmissionEvidence) error {
	if err := submission.Validate(); err != nil {
		return err
	}
	writer := strings.TrimSpace(receipt.Provenance.Writer)
	subject := strings.TrimSpace(receipt.Provenance.Subject)
	if writer == "" || subject == "" || !strings.EqualFold(writer, subject) ||
		!strings.EqualFold(writer, submission.Agent) {
		return errors.New("receipt writer and subject must match the role-owned submitting agent")
	}
	if strings.EqualFold(writer, "Coordinator") {
		return errors.New("receipt submission must be owned by a non-Coordinator role agent")
	}
	if receipt.Provenance.Route != assignment.RouteRoleOwned ||
		receipt.Provenance.Assurance != assignment.AssuranceSelfReported {
		return errors.New("version-1 role-owned receipt submission must remain self-reported")
	}
	for _, test := range receipt.Tests {
		if test.Assurance != assignment.AssuranceSelfReported {
			return fmt.Errorf("receipt test %s must remain self-reported", test.ID)
		}
	}
	return nil
}

// PortableLease is safe to project into PROCESS metadata. It deliberately has
// no absolute local path, PID, hostname, or lock token.
type PortableLease struct {
	SchemaVersion             int                          `json:"schema_version"`
	WorkspaceID               string                       `json:"workspace_id"`
	Repository                string                       `json:"repository"`
	ProcessID                 string                       `json:"process_id"`
	ExecutionClass            ExecutionClass               `json:"execution_class"`
	Mode                      WorkspaceMode                `json:"mode"`
	BaseSHA                   string                       `json:"base_sha,omitempty"`
	Branch                    string                       `json:"branch,omitempty"`
	DetachedRevision          string                       `json:"detached_revision,omitempty"`
	WriteOwnership            []string                     `json:"write_ownership,omitempty"`
	SharedTouchpoints         []string                     `json:"shared_touchpoints,omitempty"`
	IntegrationOwner          string                       `json:"integration_owner,omitempty"`
	RuntimeNamespace          string                       `json:"runtime_namespace,omitempty"`
	RuntimeResources          []RuntimeResource            `json:"runtime_resources,omitempty"`
	Assignment                *AssignmentBinding           `json:"assignment,omitempty"`
	AcceptedReceiptID         string                       `json:"accepted_receipt_id,omitempty"`
	AcceptedReceiptDigest     string                       `json:"accepted_receipt_digest,omitempty"`
	AcceptedReceiptGeneration uint64                       `json:"accepted_receipt_generation,omitempty"`
	AcceptedReceiptSubmission *RoleOwnedSubmissionEvidence `json:"accepted_receipt_submission,omitempty"`
	State                     LifecycleState               `json:"state"`
	ResultCommit              string                       `json:"result_commit,omitempty"`
	IntegrationSHA            string                       `json:"integration_sha,omitempty"`
	CreatedAt                 time.Time                    `json:"created_at"`
	UpdatedAt                 time.Time                    `json:"updated_at"`
	RetentionExpiresAt        time.Time                    `json:"retention_expires_at,omitempty"`
}

// MarshalJSON keeps the ownership marker's historical identity stable. The
// marker code deliberately clears State before hashing; accepted receipt
// authority is lifecycle evidence and is omitted only from that synthetic
// representation, never from a valid portable lease projection.
func (l PortableLease) MarshalJSON() ([]byte, error) {
	type portableLeaseJSON PortableLease
	projection := portableLeaseJSON(l)
	if l.State == "" {
		projection.AcceptedReceiptID = ""
		projection.AcceptedReceiptDigest = ""
		projection.AcceptedReceiptGeneration = 0
		projection.AcceptedReceiptSubmission = nil
	}
	return json.Marshal(projection)
}

type LeaseOwner struct {
	CoordinatorID string    `json:"coordinator_id"`
	AgentSession  string    `json:"agent_session,omitempty"`
	Token         string    `json:"token"`
	PID           int       `json:"pid,omitempty"`
	Hostname      string    `json:"hostname,omitempty"`
	AcquiredAt    time.Time `json:"acquired_at"`
}

type WorktreeObservation struct {
	Registered  bool      `json:"registered"`
	HeadSHA     string    `json:"head_sha,omitempty"`
	Branch      string    `json:"branch,omitempty"`
	Dirty       bool      `json:"dirty,omitempty"`
	InspectedAt time.Time `json:"inspected_at,omitempty"`
}

type IntegrationState struct {
	ExpectedHead string    `json:"expected_head,omitempty"`
	ObservedHead string    `json:"observed_head,omitempty"`
	StartedAt    time.Time `json:"started_at,omitempty"`
	CompletedAt  time.Time `json:"completed_at,omitempty"`
	LastError    string    `json:"last_error,omitempty"`
}

// LocalLease is machine-local registry state. Absolute paths and ownership
// credentials belong here and must never be copied into PortableLease.
type LocalLease struct {
	Portable          PortableLease          `json:"portable"`
	IntegrationRoot   string                 `json:"integration_root"`
	WorktreePath      string                 `json:"worktree_path,omitempty"`
	Owner             LeaseOwner             `json:"owner"`
	Observation       WorktreeObservation    `json:"observation,omitempty"`
	Integration       IntegrationState       `json:"integration,omitempty"`
	Assignment        *assignment.Assignment `json:"assignment,omitempty"`
	AcceptedReceiptID string                 `json:"accepted_receipt_id,omitempty"`
	LocalRevision     uint64                 `json:"local_revision"`
}

type Registry struct {
	SchemaVersion int                   `json:"schema_version"`
	Generation    uint64                `json:"generation"`
	UpdatedAt     time.Time             `json:"updated_at,omitempty"`
	Leases        map[string]LocalLease `json:"leases"`
}

var safeID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
var fullSHA = regexp.MustCompile(`^[0-9a-fA-F]{40}([0-9a-fA-F]{24})?$`)
var acceptedReceiptID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
var lowerSHA256 = regexp.MustCompile(`^[0-9a-f]{64}$`)

func NewRegistry() Registry {
	return Registry{SchemaVersion: RegistrySchemaVersion, Leases: map[string]LocalLease{}}
}

func (l PortableLease) Validate() error {
	if l.SchemaVersion != LeaseSchemaVersion {
		return fmt.Errorf("unsupported lease schema version %d", l.SchemaVersion)
	}
	if !safeID.MatchString(l.WorkspaceID) {
		return fmt.Errorf("invalid workspace id %q", l.WorkspaceID)
	}
	if strings.TrimSpace(l.Repository) == "" || strings.TrimSpace(l.Repository) != l.Repository {
		return errors.New("repository is required and must be trimmed")
	}
	if !safeID.MatchString(l.ProcessID) {
		return fmt.Errorf("invalid process id %q", l.ProcessID)
	}
	if err := validateClassMode(l.ExecutionClass, l.Mode); err != nil {
		return err
	}
	if !validLifecycleState(l.State) {
		return fmt.Errorf("unsupported lifecycle state %q", l.State)
	}
	if err := validateModeRevisions(l); err != nil {
		return err
	}
	ownership, err := NormalizeOwnership(l.WriteOwnership)
	if err != nil {
		return fmt.Errorf("write ownership: %w", err)
	}
	if !equalStrings(ownership, l.WriteOwnership) {
		return errors.New("write ownership must be normalized and sorted")
	}
	shared, err := NormalizeOwnership(l.SharedTouchpoints)
	if err != nil {
		return fmt.Errorf("shared touchpoints: %w", err)
	}
	if !equalStrings(shared, l.SharedTouchpoints) {
		return errors.New("shared touchpoints must be normalized and sorted")
	}
	if l.Mode == ModeWritable && len(l.WriteOwnership) == 0 {
		return errors.New("writable lease requires write ownership")
	}
	if l.Mode != ModeNone && !safeID.MatchString(l.RuntimeNamespace) {
		return fmt.Errorf("invalid runtime namespace %q", l.RuntimeNamespace)
	}
	if err := validateRuntimeResources(l.RuntimeResources); err != nil {
		return err
	}
	if err := validateAssignmentBinding(l); err != nil {
		return err
	}
	if err := validateAcceptedReceiptBinding(l); err != nil {
		return err
	}
	if l.CreatedAt.IsZero() || l.UpdatedAt.IsZero() || l.UpdatedAt.Before(l.CreatedAt) {
		return errors.New("lease timestamps are missing or out of order")
	}
	if !l.RetentionExpiresAt.IsZero() && l.RetentionExpiresAt.Before(l.CreatedAt) {
		return errors.New("retention expiration precedes lease creation")
	}
	return validateStateEvidence(l)
}

func (l *PortableLease) Transition(to LifecycleState, at time.Time) error {
	if l == nil {
		return errors.New("lease is nil")
	}
	if !CanTransition(l.State, to, l.Mode) {
		return fmt.Errorf("illegal workspace lifecycle transition %s -> %s", l.State, to)
	}
	previousState, previousUpdated := l.State, l.UpdatedAt
	l.State = to
	l.UpdatedAt = at.UTC()
	if err := l.Validate(); err != nil {
		l.State, l.UpdatedAt = previousState, previousUpdated
		return err
	}
	return nil
}

func CanTransition(from, to LifecycleState, mode WorkspaceMode) bool {
	if from == to {
		return validLifecycleState(from)
	}
	if to == StateConflicted && from != StateCleaned {
		return validLifecycleState(from)
	}
	if from == StateConflicted {
		return to == StatePreparing || to == StatePrepared || to == StateWorkerComplete || to == StateIntegrating || to == StateIntegrated || to == StateCleanupPending
	}
	switch from {
	case StatePreparing:
		return to == StatePrepared || to == StateCleanupPending
	case StatePrepared:
		if mode == ModeWritable && to == StateWorkerComplete {
			return true
		}
		return to == StateCleanupPending
	case StateWorkerComplete:
		return mode == ModeWritable && (to == StateIntegrating || to == StateIntegrated || to == StateCleanupPending)
	case StateIntegrating:
		return mode == ModeWritable && (to == StateIntegrated || to == StateCleanupPending)
	case StateIntegrated:
		return to == StateCleanupPending
	case StateCleanupPending:
		return to == StateCleaned
	default:
		return false
	}
}

func (l LocalLease) Validate() error {
	if err := l.Portable.Validate(); err != nil {
		return err
	}
	root, err := validateAbsolutePath("integration root", l.IntegrationRoot, true)
	if err != nil {
		return err
	}
	if root != l.IntegrationRoot {
		return errors.New("integration root must be canonical")
	}
	stateRequiresPath := l.Portable.State == StatePrepared || l.Portable.State == StateWorkerComplete ||
		l.Portable.State == StateIntegrating || l.Portable.State == StateIntegrated
	evidenceRequiresPath := l.Portable.State != StateCleaned && (l.Observation.Registered || l.Portable.ResultCommit != "" ||
		l.Portable.IntegrationSHA != "" || l.Integration.ExpectedHead != "" || l.Integration.ObservedHead != "" ||
		!l.Integration.StartedAt.IsZero() || !l.Integration.CompletedAt.IsZero())
	pathRequired := l.Portable.Mode != ModeNone && (stateRequiresPath || evidenceRequiresPath)
	worktree, err := validateAbsolutePath("worktree path", l.WorktreePath, pathRequired)
	if err != nil {
		return err
	}
	if worktree != l.WorktreePath {
		return errors.New("worktree path must be canonical")
	}
	if l.WorktreePath != "" && filepath.Clean(l.WorktreePath) == filepath.Clean(l.IntegrationRoot) {
		return errors.New("process worktree cannot be the integration root")
	}
	if l.Portable.State != StateCleaned {
		if strings.TrimSpace(l.Owner.CoordinatorID) == "" || strings.TrimSpace(l.Owner.Token) == "" || l.Owner.AcquiredAt.IsZero() {
			return errors.New("active local lease requires coordinator owner, token, and acquisition time")
		}
	}
	for _, sha := range []struct{ name, value string }{{"observed HEAD", l.Observation.HeadSHA}, {"expected integration HEAD", l.Integration.ExpectedHead}, {"observed integration HEAD", l.Integration.ObservedHead}} {
		if sha.value != "" && !fullSHA.MatchString(sha.value) {
			return fmt.Errorf("%s must be a full Git object id", sha.name)
		}
	}
	if !l.Integration.CompletedAt.IsZero() && (l.Integration.StartedAt.IsZero() || l.Integration.CompletedAt.Before(l.Integration.StartedAt)) {
		return errors.New("integration timestamps are out of order")
	}
	if l.LocalRevision == 0 {
		return errors.New("local revision must be positive")
	}
	if err := validateLocalAssignment(l); err != nil {
		return err
	}
	return nil
}

func validateAssignmentBinding(lease PortableLease) error {
	binding := lease.Assignment
	if binding == nil {
		return nil
	}
	if binding.SchemaVersion != assignment.AssignmentSchemaVersion {
		return fmt.Errorf("assignment binding has unsupported schema version %q", binding.SchemaVersion)
	}
	if !safeID.MatchString(binding.AssignmentID) {
		return fmt.Errorf("invalid assignment id %q", binding.AssignmentID)
	}
	if !lowerSHA256.MatchString(binding.Digest) {
		return errors.New("assignment binding digest must be a lowercase SHA-256 digest")
	}
	if binding.Generation == 0 {
		return errors.New("assignment binding generation must be positive")
	}
	if binding.Role != assignment.RoleImplementation && binding.Role != assignment.RoleReview && binding.Role != assignment.RoleVerification {
		return fmt.Errorf("unsupported assignment role %q", binding.Role)
	}
	for name, revision := range map[string]string{"base": binding.BaseRevision, "subject": binding.SubjectRevision} {
		if revision != "" && !fullSHA.MatchString(revision) {
			return fmt.Errorf("assignment %s revision must be a full Git object id", name)
		}
	}
	switch lease.ExecutionClass {
	case ExecutionChangeBearing:
		if binding.Role != assignment.RoleImplementation || binding.BaseRevision != lease.BaseSHA || binding.SubjectRevision != "" {
			return errors.New("change-bearing assignment binding must use implementation role and the lease base revision")
		}
	case ExecutionReview:
		if binding.Role != assignment.RoleReview || binding.SubjectRevision != lease.DetachedRevision || binding.BaseRevision != "" {
			return errors.New("review assignment binding must use review role and the lease subject revision")
		}
	case ExecutionVerification:
		if binding.Role != assignment.RoleVerification || binding.SubjectRevision != lease.DetachedRevision || binding.BaseRevision != "" {
			return errors.New("verification assignment binding must use verification role and the lease subject revision")
		}
	default:
		return fmt.Errorf("execution class %s cannot carry an assignment binding", lease.ExecutionClass)
	}
	return nil
}

func validateAcceptedReceiptBinding(lease PortableLease) error {
	if lease.AcceptedReceiptID == "" && lease.AcceptedReceiptDigest == "" && lease.AcceptedReceiptGeneration == 0 {
		if lease.AcceptedReceiptSubmission != nil {
			return errors.New("accepted receipt submission evidence requires accepted receipt authority")
		}
		return nil
	}
	if !acceptedReceiptID.MatchString(lease.AcceptedReceiptID) {
		return fmt.Errorf("invalid accepted receipt id %q", lease.AcceptedReceiptID)
	}
	if !lowerSHA256.MatchString(lease.AcceptedReceiptDigest) {
		return errors.New("accepted receipt digest must be a lowercase SHA-256 digest")
	}
	if lease.AcceptedReceiptGeneration == 0 {
		return errors.New("accepted receipt assignment generation must be positive")
	}
	if lease.Assignment == nil || lease.ExecutionClass != ExecutionChangeBearing || lease.Assignment.Role != assignment.RoleImplementation ||
		lease.AcceptedReceiptGeneration != lease.Assignment.Generation {
		return errors.New("accepted receipt must match the implementation assignment generation")
	}
	if lease.ResultCommit == "" {
		return errors.New("accepted receipt requires result commit evidence")
	}
	// A nil submission remains readable for already-persisted legacy receipt
	// authority. Every newly accepted receipt records non-nil evidence, and the
	// append-only binding prevents a later assurance upgrade.
	if lease.AcceptedReceiptSubmission != nil {
		if err := lease.AcceptedReceiptSubmission.Validate(); err != nil {
			return fmt.Errorf("accepted receipt submission: %w", err)
		}
	}
	return nil
}

func validateLocalAssignment(lease LocalLease) error {
	if lease.Portable.Assignment == nil && lease.Assignment == nil {
		if lease.AcceptedReceiptID != "" {
			return errors.New("accepted receipt requires an assignment binding")
		}
		return nil
	}
	if lease.Portable.Assignment != nil && lease.Assignment == nil {
		// A remote-only binding may be recovered only through explicit issuance,
		// which recompiles and proves the full assignment before delivery.
		return nil
	}
	if lease.Portable.Assignment == nil {
		return errors.New("local assignment requires a portable assignment binding")
	}
	if err := lease.Assignment.ValidateForStorageRead(); err != nil {
		return fmt.Errorf("local assignment: %w", err)
	}
	digest, err := assignment.AssignmentDigestForStorageRead(*lease.Assignment)
	if err != nil {
		return err
	}
	binding := lease.Portable.Assignment
	if binding.SchemaVersion != lease.Assignment.SchemaVersion || binding.AssignmentID != lease.Assignment.ID || binding.Digest != digest ||
		binding.Role != lease.Assignment.Role || binding.BaseRevision != lease.Assignment.BaseRevision || binding.SubjectRevision != lease.Assignment.SubjectRevision {
		return errors.New("portable assignment binding differs from local assignment")
	}
	if lease.AcceptedReceiptID != "" && !acceptedReceiptID.MatchString(lease.AcceptedReceiptID) {
		return fmt.Errorf("invalid accepted receipt id %q", lease.AcceptedReceiptID)
	}
	if lease.Portable.AcceptedReceiptID != "" && lease.AcceptedReceiptID != "" &&
		lease.AcceptedReceiptID != lease.Portable.AcceptedReceiptID {
		return errors.New("legacy accepted receipt id differs from portable accepted receipt authority")
	}
	return nil
}

func (r Registry) Validate() error {
	if r.SchemaVersion != RegistrySchemaVersion {
		return fmt.Errorf("unsupported registry schema version %d", r.SchemaVersion)
	}
	if r.Leases == nil {
		return errors.New("registry leases map is nil")
	}
	ids := make([]string, 0, len(r.Leases))
	for id := range r.Leases {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	repository := ""
	for index, id := range ids {
		lease := r.Leases[id]
		if id != lease.Portable.WorkspaceID {
			return fmt.Errorf("registry key %q does not match workspace id %q", id, lease.Portable.WorkspaceID)
		}
		if err := lease.Validate(); err != nil {
			return fmt.Errorf("lease %s: %w", id, err)
		}
		if repository == "" {
			repository = lease.Portable.Repository
		} else if lease.Portable.Repository != repository {
			return fmt.Errorf("registry mixes repositories %q and %q", repository, lease.Portable.Repository)
		}
		if lease.Portable.State == StateCleaned {
			continue
		}
		for _, otherID := range ids[index+1:] {
			other := r.Leases[otherID]
			if other.Portable.State == StateCleaned {
				continue
			}
			if err := activeLeaseConflict(lease, other); err != nil {
				return fmt.Errorf("leases %s and %s conflict: %w", id, otherID, err)
			}
		}
	}
	return nil
}

func NormalizeOwnership(entries []string) ([]string, error) {
	set := map[string]struct{}{}
	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		if entry == "" || strings.Contains(entry, "\\") || strings.HasPrefix(entry, "/") {
			return nil, fmt.Errorf("invalid repository-relative ownership %q", entry)
		}
		prefix := strings.HasSuffix(entry, "/**")
		base := strings.TrimSuffix(entry, "/**")
		if strings.Contains(base, "*") || base == "" {
			return nil, fmt.Errorf("unsupported ownership wildcard %q", entry)
		}
		clean := path.Clean(base)
		if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || clean != base {
			return nil, fmt.Errorf("unsafe ownership path %q", entry)
		}
		if prefix {
			clean += "/**"
		}
		set[clean] = struct{}{}
	}
	out := make([]string, 0, len(set))
	for entry := range set {
		out = append(out, entry)
	}
	sort.Strings(out)
	return out, nil
}

func OwnershipOverlaps(left, right []string) (bool, error) {
	a, err := NormalizeOwnership(left)
	if err != nil {
		return false, err
	}
	b, err := NormalizeOwnership(right)
	if err != nil {
		return false, err
	}
	for _, one := range a {
		for _, two := range b {
			if ownershipEntriesOverlap(one, two) {
				return true, nil
			}
		}
	}
	return false, nil
}

func OwnsPath(entries []string, repositoryPath string) (bool, error) {
	normalized, err := NormalizeOwnership(entries)
	if err != nil {
		return false, err
	}
	pathEntry, err := NormalizeOwnership([]string{repositoryPath})
	if err != nil || len(pathEntry) != 1 || strings.HasSuffix(pathEntry[0], "/**") {
		return false, fmt.Errorf("invalid repository path %q", repositoryPath)
	}
	for _, entry := range normalized {
		if ownershipEntryContains(entry, pathEntry[0]) {
			return true, nil
		}
	}
	return false, nil
}

func validateClassMode(class ExecutionClass, mode WorkspaceMode) error {
	switch class {
	case ExecutionChangeBearing:
		if mode != ModeWritable {
			return errors.New("change-bearing execution requires writable workspace mode")
		}
	case ExecutionReview, ExecutionVerification:
		if mode != ModeSnapshot {
			return fmt.Errorf("%s execution requires snapshot workspace mode", class)
		}
	case ExecutionOrchestration:
		if mode != ModeNone {
			return errors.New("orchestration execution requires no-checkout workspace mode")
		}
	case ExecutionExternal:
		if mode != ModeNone {
			return errors.New("external execution requires no-checkout workspace mode")
		}
	default:
		return fmt.Errorf("unsupported execution class %q", class)
	}
	return nil
}

func validateModeRevisions(l PortableLease) error {
	switch l.Mode {
	case ModeWritable:
		if !fullSHA.MatchString(l.BaseSHA) || !validBranch(l.Branch) || l.DetachedRevision != "" {
			return errors.New("writable lease requires full base SHA and branch, without detached revision")
		}
	case ModeSnapshot:
		if !fullSHA.MatchString(l.BaseSHA) || !fullSHA.MatchString(l.DetachedRevision) || !strings.EqualFold(l.BaseSHA, l.DetachedRevision) || l.Branch != "" {
			return errors.New("snapshot lease requires matching full base/detached revision and no branch")
		}
	case ModeNone:
		if l.BaseSHA != "" || l.Branch != "" || l.DetachedRevision != "" || l.ResultCommit != "" || l.IntegrationSHA != "" {
			return errors.New("no-checkout lease cannot carry Git revisions")
		}
	}
	return nil
}

func validateStateEvidence(l PortableLease) error {
	if l.ResultCommit != "" && !fullSHA.MatchString(l.ResultCommit) {
		return errors.New("result commit must be a full Git object id")
	}
	if l.IntegrationSHA != "" && !fullSHA.MatchString(l.IntegrationSHA) {
		return errors.New("integration SHA must be a full Git object id")
	}
	if l.Mode != ModeWritable && (l.State == StateWorkerComplete || l.State == StateIntegrating || l.State == StateIntegrated) {
		return fmt.Errorf("state %s requires writable workspace mode", l.State)
	}
	switch l.State {
	case StatePreparing, StatePrepared:
		if l.ResultCommit != "" || l.IntegrationSHA != "" {
			return fmt.Errorf("state %s cannot carry result or integration commit", l.State)
		}
	case StateWorkerComplete, StateIntegrating:
		if l.ResultCommit == "" || l.IntegrationSHA != "" {
			return fmt.Errorf("state %s requires result commit and no integration SHA", l.State)
		}
	case StateIntegrated:
		if l.ResultCommit == "" || l.IntegrationSHA == "" {
			return errors.New("integrated state requires result commit and integration SHA")
		}
	}
	return nil
}

func validateRuntimeResources(resources []RuntimeResource) error {
	seen := map[string]bool{}
	for _, resource := range resources {
		if !safeID.MatchString(resource.Kind) || !safeID.MatchString(resource.Name) {
			return fmt.Errorf("invalid runtime resource %q/%q", resource.Kind, resource.Name)
		}
		key := resource.Kind + "\x00" + resource.Name
		if seen[key] {
			return fmt.Errorf("duplicate runtime resource %s/%s", resource.Kind, resource.Name)
		}
		seen[key] = true
	}
	return nil
}

func validLifecycleState(state LifecycleState) bool {
	switch state {
	case StatePreparing, StatePrepared, StateWorkerComplete, StateIntegrating, StateIntegrated, StateConflicted, StateCleanupPending, StateCleaned:
		return true
	default:
		return false
	}
}

func validBranch(branch string) bool {
	if branch == "" || branch == "@" || strings.TrimSpace(branch) != branch || strings.HasPrefix(branch, "-") || strings.HasPrefix(branch, "/") || strings.HasSuffix(branch, "/") || strings.HasSuffix(branch, ".") {
		return false
	}
	if strings.ContainsAny(branch, " ~^:?*[\\") || strings.Contains(branch, "..") || strings.Contains(branch, "@{") || strings.Contains(branch, "//") {
		return false
	}
	for _, r := range branch {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	for _, component := range strings.Split(branch, "/") {
		if component == "" || strings.HasPrefix(component, ".") || strings.HasSuffix(component, ".lock") {
			return false
		}
	}
	return true
}

func validateAbsolutePath(name, value string, required bool) (string, error) {
	if value == "" && !required {
		return "", nil
	}
	if value == "" || !filepath.IsAbs(value) {
		return "", fmt.Errorf("%s must be an absolute path", name)
	}
	return filepath.Clean(value), nil
}

func activeLeaseConflict(left, right LocalLease) error {
	if left.WorktreePath != "" && filepath.Clean(left.WorktreePath) == filepath.Clean(right.WorktreePath) {
		return errors.New("active leases share a worktree path")
	}
	if left.Portable.Mode == ModeWritable && right.Portable.Mode == ModeWritable {
		if left.Portable.Branch == right.Portable.Branch {
			return errors.New("writable leases share a branch")
		}
	}
	if left.Portable.Repository != right.Portable.Repository {
		return nil
	}
	if left.Portable.ProcessID == right.Portable.ProcessID {
		return errors.New("same PROCESS has multiple active workspace leases")
	}
	if left.Portable.Mode == ModeWritable && right.Portable.Mode == ModeWritable {
		overlap, err := OwnershipOverlaps(append(left.Portable.WriteOwnership, left.Portable.SharedTouchpoints...), append(right.Portable.WriteOwnership, right.Portable.SharedTouchpoints...))
		if err != nil {
			return err
		}
		sharedOwner := left.Portable.IntegrationOwner != "" && left.Portable.IntegrationOwner == right.Portable.IntegrationOwner
		if overlap && !sharedOwner {
			return errors.New("writable leases have overlapping ownership without a shared integration owner")
		}
	}
	for _, one := range left.Portable.RuntimeResources {
		for _, two := range right.Portable.RuntimeResources {
			if one.Kind == two.Kind && one.Name == two.Name && (one.Exclusive || two.Exclusive) {
				return fmt.Errorf("exclusive runtime resource collision %s/%s", one.Kind, one.Name)
			}
		}
	}
	return nil
}

func ownershipEntriesOverlap(left, right string) bool {
	return ownershipEntryContains(left, strings.TrimSuffix(right, "/**")) || ownershipEntryContains(right, strings.TrimSuffix(left, "/**"))
}

func ownershipEntryContains(entry, target string) bool {
	if !strings.HasSuffix(entry, "/**") {
		return entry == target
	}
	prefix := strings.TrimSuffix(entry, "/**")
	return target == prefix || strings.HasPrefix(target, prefix+"/")
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
