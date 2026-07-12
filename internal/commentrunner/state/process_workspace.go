package state

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/higress-group/issue-spec/internal/processworkspace"
)

const ProcessWorkspaceAssociationSchemaVersion = 2

var (
	associationIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)
	canonicalRuntimeID   = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
	canonicalHost        = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?$`)
)

type ProcessWorkspaceLifecycle string

const (
	ProcessWorkspaceAllocating     ProcessWorkspaceLifecycle = "allocating"
	ProcessWorkspacePrepared       ProcessWorkspaceLifecycle = "prepared"
	ProcessWorkspaceCleanupPending ProcessWorkspaceLifecycle = "cleanup-pending"
	ProcessWorkspaceReleased       ProcessWorkspaceLifecycle = "released"
	ProcessWorkspaceFailed         ProcessWorkspaceLifecycle = "failed"
)

type ProcessWorkspaceProviderIdentity struct {
	ProviderKey    string `json:"provider_key"`
	ServerInstance string `json:"server_instance"`
	Host           string `json:"host"`
}

func (p ProcessWorkspaceProviderIdentity) Validate() error {
	for name, value := range map[string]string{"provider key": p.ProviderKey, "server instance": p.ServerInstance} {
		if !canonicalRuntimeID.MatchString(value) || value != strings.ToLower(strings.TrimSpace(value)) {
			return fmt.Errorf("%s %q is not canonical", name, value)
		}
	}
	if p.Host != strings.ToLower(strings.TrimSpace(p.Host)) || !canonicalHost.MatchString(p.Host) ||
		strings.Contains(p.Host, "..") || strings.ContainsAny(p.Host, `/\\@:`) {
		return fmt.Errorf("provider host %q is not canonical", p.Host)
	}
	return nil
}

// ProcessWorkspaceAssociation is the portable replay and reservation record.
// It intentionally excludes local paths, owner tokens, credentials and host
// process identity.
type ProcessWorkspaceAssociation struct {
	SchemaVersion       int                                `json:"schema_version"`
	ReservationIdentity string                             `json:"reservation_identity"`
	ReservationID       string                             `json:"reservation_id"`
	Lifecycle           ProcessWorkspaceLifecycle          `json:"lifecycle"`
	NeedsReconcile      bool                               `json:"needs_reconcile,omitempty"`
	LastError           string                             `json:"last_error,omitempty"`
	WorkspaceID         string                             `json:"workspace_id"`
	Repository          string                             `json:"repository"`
	Provider            ProcessWorkspaceProviderIdentity   `json:"provider"`
	ProcessID           string                             `json:"process_id"`
	BaseSHA             string                             `json:"base_sha,omitempty"`
	Branch              string                             `json:"branch,omitempty"`
	ExecutionClass      processworkspace.ExecutionClass    `json:"execution_class"`
	Mode                processworkspace.WorkspaceMode     `json:"mode"`
	WriteOwnership      []string                           `json:"write_ownership,omitempty"`
	SharedTouchpoints   []string                           `json:"shared_touchpoints,omitempty"`
	IntegrationOwner    string                             `json:"integration_owner,omitempty"`
	LocalAssociationRef string                             `json:"local_association_ref,omitempty"`
	RuntimeNamespace    string                             `json:"runtime_namespace"`
	RuntimeResources    []processworkspace.RuntimeResource `json:"runtime_resources,omitempty"`
}

func (a *ProcessWorkspaceAssociation) UnmarshalJSON(data []byte) error {
	type alias ProcessWorkspaceAssociation
	var decoded alias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	legacy := decoded.SchemaVersion == 0 || decoded.SchemaVersion == 1
	if legacy {
		decoded.SchemaVersion = ProcessWorkspaceAssociationSchemaVersion
		decoded.Lifecycle = ProcessWorkspaceFailed
		decoded.Provider = ProcessWorkspaceProviderIdentity{ProviderKey: "legacy", ServerInstance: "legacy", Host: "legacy.invalid"}
	}
	if decoded.SchemaVersion != ProcessWorkspaceAssociationSchemaVersion {
		return fmt.Errorf("unsupported process workspace association schema version %d", decoded.SchemaVersion)
	}
	value := ProcessWorkspaceAssociation(decoded)
	if legacy {
		if value.ReservationIdentity == "" {
			value.ReservationIdentity = value.ExpectedReservationIdentity()
		}
		if value.ReservationID == "" {
			value.ReservationID = "reservation:legacy"
		}
	}
	*a = value
	return a.Validate()
}

func (a ProcessWorkspaceAssociation) ExpectedReservationIdentity() string {
	identity := a
	identity.ReservationID = ""
	identity.ReservationIdentity = ""
	identity.Lifecycle = ""
	identity.NeedsReconcile = false
	identity.LastError = ""
	payload, _ := json.Marshal(identity)
	digest := sha256.Sum256(payload)
	return "identity:" + hex.EncodeToString(digest[:16])
}

func (a ProcessWorkspaceAssociation) Validate() error {
	if a.SchemaVersion != ProcessWorkspaceAssociationSchemaVersion {
		return fmt.Errorf("unsupported process workspace association schema version %d", a.SchemaVersion)
	}
	if !associationIDPattern.MatchString(a.WorkspaceID) || !associationIDPattern.MatchString(a.ProcessID) {
		return errors.New("workspace id and process id must be safe identifiers")
	}
	if err := validateRepositoryIdentity(a.Repository); err != nil {
		return err
	}
	if err := a.Provider.Validate(); err != nil {
		return err
	}
	if !validProcessWorkspaceLifecycle(a.Lifecycle) {
		return fmt.Errorf("unsupported process workspace lifecycle %q", a.Lifecycle)
	}
	if a.ReservationIdentity == "" || a.ReservationIdentity != a.ExpectedReservationIdentity() {
		return errors.New("process workspace identity does not match portable replay fields")
	}
	if !associationIDPattern.MatchString(a.ReservationID) {
		return errors.New("process workspace reservation token is invalid")
	}
	if a.NeedsReconcile != (a.LastError != "") || (a.LastError != "" && !canonicalRuntimeID.MatchString(a.LastError)) {
		return errors.New("process workspace reconcile diagnostics are invalid")
	}
	if err := validateAssociationClassMode(a.ExecutionClass, a.Mode); err != nil {
		return err
	}
	if err := validateCanonicalRuntimeResources(a.RuntimeResources); err != nil {
		return err
	}
	if a.Lifecycle == ProcessWorkspaceFailed && a.Provider.ProviderKey == "legacy" {
		return nil
	}
	if a.RuntimeNamespace == "" || !canonicalRuntimeID.MatchString(a.RuntimeNamespace) || a.RuntimeNamespace != strings.ToLower(a.RuntimeNamespace) {
		return fmt.Errorf("runtime namespace %q is not canonical", a.RuntimeNamespace)
	}
	if err := processworkspace.ValidateManagedOwnership(a.WriteOwnership, a.SharedTouchpoints); err != nil {
		return err
	}
	switch a.Mode {
	case processworkspace.ModeWritable:
		if !fullAssociationSHA(a.BaseSHA) || strings.TrimSpace(a.Branch) == "" || len(a.WriteOwnership) == 0 || a.LocalAssociationRef == "" {
			return errors.New("writable association lacks replay fields")
		}
	case processworkspace.ModeSnapshot:
		if !fullAssociationSHA(a.BaseSHA) || a.Branch != "" || a.LocalAssociationRef == "" {
			return errors.New("snapshot association lacks replay fields")
		}
	case processworkspace.ModeNone:
		if a.BaseSHA != "" || a.Branch != "" || a.LocalAssociationRef != "" || len(a.WriteOwnership) != 0 || len(a.SharedTouchpoints) != 0 {
			return errors.New("no-checkout association carries Git workspace fields")
		}
	}
	if a.LocalAssociationRef != "" && (!associationIDPattern.MatchString(a.LocalAssociationRef) || strings.ContainsAny(a.LocalAssociationRef, `/\\`)) {
		return fmt.Errorf("invalid local association reference %q", a.LocalAssociationRef)
	}
	return nil
}

func validateRepositoryIdentity(repository string) error {
	if repository != strings.TrimSpace(repository) || repository == "" || strings.HasPrefix(repository, "/") ||
		strings.Contains(repository, "\\") || strings.Contains(repository, "://") || strings.Contains(repository, "@") ||
		strings.HasPrefix(strings.ToLower(repository), "file:") || strings.ContainsAny(repository, "?#") ||
		(len(repository) >= 2 && repository[1] == ':') {
		return fmt.Errorf("repository identity %q is not canonical", repository)
	}
	parts := strings.Split(repository, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" || parts[0] == "." || parts[0] == ".." || parts[1] == "." || parts[1] == ".." {
		return fmt.Errorf("repository identity %q must be owner/repository", repository)
	}
	return nil
}

func validateCanonicalRuntimeResources(resources []processworkspace.RuntimeResource) error {
	previous := ""
	for _, resource := range resources {
		if resource.Kind != strings.ToLower(strings.TrimSpace(resource.Kind)) || resource.Name != strings.ToLower(strings.TrimSpace(resource.Name)) ||
			!canonicalRuntimeID.MatchString(resource.Kind) || !canonicalRuntimeID.MatchString(resource.Name) {
			return fmt.Errorf("runtime resource %q/%q is not canonical", resource.Kind, resource.Name)
		}
		key := resource.Kind + "\x00" + resource.Name
		if previous != "" && key <= previous {
			return fmt.Errorf("runtime resources must be strictly sorted and unique at %s/%s", resource.Kind, resource.Name)
		}
		previous = key
	}
	return nil
}

func validateAssociationClassMode(class processworkspace.ExecutionClass, mode processworkspace.WorkspaceMode) error {
	switch class {
	case processworkspace.ExecutionChangeBearing:
		if mode != processworkspace.ModeWritable {
			return errors.New("change-bearing association requires writable mode")
		}
	case processworkspace.ExecutionReview, processworkspace.ExecutionVerification:
		if mode != processworkspace.ModeSnapshot {
			return fmt.Errorf("%s association requires snapshot mode", class)
		}
	case processworkspace.ExecutionOrchestration:
		if mode != processworkspace.ModeNone {
			return errors.New("orchestration association requires no-checkout mode")
		}
	case processworkspace.ExecutionExternal:
		if mode != processworkspace.ModeNone && mode != processworkspace.ModeWritable && mode != processworkspace.ModeSnapshot {
			return errors.New("external association has invalid adapter mode")
		}
	default:
		return fmt.Errorf("unsupported execution class %q", class)
	}
	return nil
}

func fullAssociationSHA(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, r := range value {
		if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
			return false
		}
	}
	return true
}

func validProcessWorkspaceLifecycle(value ProcessWorkspaceLifecycle) bool {
	switch value {
	case ProcessWorkspaceAllocating, ProcessWorkspacePrepared, ProcessWorkspaceCleanupPending, ProcessWorkspaceReleased, ProcessWorkspaceFailed:
		return true
	default:
		return false
	}
}

func activeProcessWorkspace(value ProcessWorkspaceLifecycle) bool {
	return value == ProcessWorkspaceAllocating || value == ProcessWorkspacePrepared || value == ProcessWorkspaceCleanupPending
}

type ProcessWorkspaceAssociations struct {
	SchemaVersion int                                    `json:"schema_version"`
	ByWorkspace   map[string]ProcessWorkspaceAssociation `json:"by_workspace,omitempty"`
}

func NewProcessWorkspaceAssociations() ProcessWorkspaceAssociations {
	return ProcessWorkspaceAssociations{SchemaVersion: ProcessWorkspaceAssociationSchemaVersion, ByWorkspace: map[string]ProcessWorkspaceAssociation{}}
}

func (s *ProcessWorkspaceAssociations) UnmarshalJSON(data []byte) error {
	type alias ProcessWorkspaceAssociations
	var decoded alias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	if decoded.SchemaVersion == 0 || decoded.SchemaVersion == 1 {
		decoded.SchemaVersion = ProcessWorkspaceAssociationSchemaVersion
	}
	if decoded.SchemaVersion != ProcessWorkspaceAssociationSchemaVersion {
		return fmt.Errorf("unsupported process workspace state schema version %d", decoded.SchemaVersion)
	}
	if decoded.ByWorkspace == nil {
		decoded.ByWorkspace = map[string]ProcessWorkspaceAssociation{}
	}
	*s = ProcessWorkspaceAssociations(decoded)
	return s.Validate()
}

func (s ProcessWorkspaceAssociations) Validate() error {
	if s.SchemaVersion != ProcessWorkspaceAssociationSchemaVersion {
		return fmt.Errorf("unsupported process workspace state schema version %d", s.SchemaVersion)
	}
	active := make([]ProcessWorkspaceAssociation, 0, len(s.ByWorkspace))
	for key, association := range s.ByWorkspace {
		if key != association.WorkspaceID {
			return fmt.Errorf("association key %q does not match workspace id %q", key, association.WorkspaceID)
		}
		if err := association.Validate(); err != nil {
			return fmt.Errorf("association %s: %w", key, err)
		}
		if activeProcessWorkspace(association.Lifecycle) {
			for _, existing := range active {
				if err := validateExclusiveRuntimeCollision(existing, association); err != nil {
					return err
				}
			}
			active = append(active, association)
		}
	}
	return nil
}

func (s *ProcessWorkspaceAssociations) Reserve(association ProcessWorkspaceAssociation) (ProcessWorkspaceAssociation, error) {
	if s == nil {
		return ProcessWorkspaceAssociation{}, errors.New("process workspace associations are nil")
	}
	if s.SchemaVersion == 0 {
		*s = NewProcessWorkspaceAssociations()
	}
	if association.Lifecycle != ProcessWorkspaceAllocating {
		return ProcessWorkspaceAssociation{}, errors.New("new reservation must be allocating")
	}
	if err := association.Validate(); err != nil {
		return ProcessWorkspaceAssociation{}, err
	}
	if existing, ok := s.ByWorkspace[association.WorkspaceID]; ok {
		if activeProcessWorkspace(existing.Lifecycle) && existing.ReservationIdentity == association.ReservationIdentity {
			return cloneProcessWorkspaceAssociation(existing), nil
		}
		if activeProcessWorkspace(existing.Lifecycle) {
			return ProcessWorkspaceAssociation{}, fmt.Errorf("workspace %s has a different reservation", association.WorkspaceID)
		}
		if existing.ReservationID == association.ReservationID {
			return ProcessWorkspaceAssociation{}, errors.New("terminal reservation token cannot be reused")
		}
	}
	for _, existing := range s.ByWorkspace {
		if !activeProcessWorkspace(existing.Lifecycle) || existing.WorkspaceID == association.WorkspaceID {
			continue
		}
		if err := validateExclusiveRuntimeCollision(existing, association); err != nil {
			return ProcessWorkspaceAssociation{}, err
		}
	}
	if s.ByWorkspace == nil {
		s.ByWorkspace = map[string]ProcessWorkspaceAssociation{}
	}
	s.ByWorkspace[association.WorkspaceID] = cloneProcessWorkspaceAssociation(association)
	return cloneProcessWorkspaceAssociation(association), nil
}

func validateExclusiveRuntimeCollision(left, right ProcessWorkspaceAssociation) error {
	for _, one := range left.RuntimeResources {
		for _, two := range right.RuntimeResources {
			if one.Kind == two.Kind && one.Name == two.Name && (one.Exclusive || two.Exclusive) {
				return fmt.Errorf("exclusive runtime resource collision %s/%s", one.Kind, one.Name)
			}
		}
	}
	return nil
}

func (s *ProcessWorkspaceAssociations) Transition(workspaceID, reservationID string, from, to ProcessWorkspaceLifecycle) (ProcessWorkspaceAssociation, error) {
	association, ok := s.ByWorkspace[workspaceID]
	if !ok || association.ReservationID != reservationID {
		return ProcessWorkspaceAssociation{}, errors.New("process workspace reservation CAS mismatch")
	}
	if association.Lifecycle == to {
		if to == ProcessWorkspacePrepared && (association.NeedsReconcile || association.LastError != "") {
			association.NeedsReconcile, association.LastError = false, ""
			s.ByWorkspace[workspaceID] = association
		}
		return cloneProcessWorkspaceAssociation(association), nil
	}
	if association.Lifecycle != from || !allowedAssociationTransition(from, to) {
		return ProcessWorkspaceAssociation{}, fmt.Errorf("process workspace lifecycle CAS mismatch: %s -> %s", association.Lifecycle, to)
	}
	association.Lifecycle = to
	if to == ProcessWorkspacePrepared {
		association.NeedsReconcile, association.LastError = false, ""
	}
	s.ByWorkspace[workspaceID] = association
	return cloneProcessWorkspaceAssociation(association), nil
}

func (s *ProcessWorkspaceAssociations) MarkFailure(workspaceID, reservationID, code string) (ProcessWorkspaceAssociation, error) {
	association, ok := s.ByWorkspace[workspaceID]
	if !ok || association.ReservationID != reservationID {
		return ProcessWorkspaceAssociation{}, errors.New("process workspace reservation CAS mismatch")
	}
	if !activeProcessWorkspace(association.Lifecycle) || !canonicalRuntimeID.MatchString(code) {
		return ProcessWorkspaceAssociation{}, errors.New("invalid process workspace failure marker")
	}
	association.NeedsReconcile, association.LastError = true, code
	s.ByWorkspace[workspaceID] = association
	return cloneProcessWorkspaceAssociation(association), nil
}

func (s *ProcessWorkspaceAssociations) ConfirmReleased(workspaceID, reservationID string) (ProcessWorkspaceAssociation, error) {
	association, ok := s.ByWorkspace[workspaceID]
	if !ok || association.ReservationID != reservationID {
		return ProcessWorkspaceAssociation{}, errors.New("process workspace reservation CAS mismatch")
	}
	if association.Lifecycle == ProcessWorkspaceReleased {
		return cloneProcessWorkspaceAssociation(association), nil
	}
	if association.Lifecycle != ProcessWorkspaceCleanupPending {
		return ProcessWorkspaceAssociation{}, errors.New("release confirmation requires cleanup-pending lifecycle")
	}
	association.Lifecycle = ProcessWorkspaceReleased
	association.NeedsReconcile, association.LastError = false, ""
	s.ByWorkspace[workspaceID] = association
	return cloneProcessWorkspaceAssociation(association), nil
}

func (s *ProcessWorkspaceAssociations) Delete(workspaceID, reservationID string) error {
	association, ok := s.ByWorkspace[workspaceID]
	if !ok {
		return nil
	}
	if association.ReservationID != reservationID || (association.Lifecycle != ProcessWorkspaceReleased && association.Lifecycle != ProcessWorkspaceFailed) {
		return errors.New("process workspace delete CAS mismatch")
	}
	delete(s.ByWorkspace, workspaceID)
	return nil
}

func allowedAssociationTransition(from, to ProcessWorkspaceLifecycle) bool {
	return (from == ProcessWorkspaceAllocating && to == ProcessWorkspacePrepared) ||
		((from == ProcessWorkspaceAllocating || from == ProcessWorkspacePrepared) && to == ProcessWorkspaceCleanupPending)
}

func (s ProcessWorkspaceAssociations) Get(workspaceID string) (ProcessWorkspaceAssociation, bool) {
	association, ok := s.ByWorkspace[workspaceID]
	return cloneProcessWorkspaceAssociation(association), ok
}

func cloneProcessWorkspaceAssociation(in ProcessWorkspaceAssociation) ProcessWorkspaceAssociation {
	in.WriteOwnership = append([]string(nil), in.WriteOwnership...)
	in.SharedTouchpoints = append([]string(nil), in.SharedTouchpoints...)
	in.RuntimeResources = append([]processworkspace.RuntimeResource(nil), in.RuntimeResources...)
	return in
}
