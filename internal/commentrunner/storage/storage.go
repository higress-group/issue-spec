// Package storage owns the runner's physical-resource lifecycle outside
// managed Git workspaces: the independent `.storage/state.json` sidecar, the
// canonical root owner lock, exact `.sessions` runtime and `.process-workspaces`
// pool mapping, classification, recoverable deletion, and statfs admission.
//
// The sidecar is physical inventory only. RunnerState remains authoritative
// for sessions, jobs, locks, queues, and compaction; no retirement fields are
// ever written back to RunnerState.
package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/higress-group/issue-spec/internal/workspace"
)

const (
	// SidecarSchemaVersion is the current `.storage/state.json` schema. A newer
	// on-disk schema or a foreign root identity permits report-only inventory.
	SidecarSchemaVersion = 1

	// StorageDirName is the runner-private directory below the workspace root
	// holding the sidecar, owner lock, resource locks, and backups. It is never
	// inventoried for deletion.
	StorageDirName = ".storage"
	// SessionsDirName holds stable public-session runtime directories.
	SessionsDirName = ".sessions"
	// ProcessPoolsDirName holds session-scoped PROCESS workspace pool roots.
	ProcessPoolsDirName = ".process-workspaces"

	sidecarFileName = "state.json"
	sidecarLockName = "state.json.lock"
	ownerLockName   = "owner.lock"
	resourceLockDir = "locks"
	backupDirName   = "backups"
)

// DefaultOrphanGrace is the default observation window before an unmatched
// directory becomes deletion-eligible.
const DefaultOrphanGrace = 7 * 24 * time.Hour

type ResourceKind string

const (
	ResourceKindSessionRuntime     ResourceKind = "session_runtime"
	ResourceKindSessionProcessPool ResourceKind = "session_process_pool"
)

func (k ResourceKind) Valid() bool {
	return k == ResourceKindSessionRuntime || k == ResourceKindSessionProcessPool
}

// CleanupState is the sidecar-persisted deletion lifecycle of one resource:
// managed/orphan_observed/retired_known -> eligible -> deleting -> removed.
type CleanupState string

const (
	CleanupManaged        CleanupState = "managed"
	CleanupOrphanObserved CleanupState = "orphan_observed"
	CleanupRetiredKnown   CleanupState = "retired_known"
	CleanupEligible       CleanupState = "eligible"
	CleanupDeleting       CleanupState = "deleting"
	CleanupRemoved        CleanupState = "removed"
)

func (s CleanupState) Valid() bool {
	switch s {
	case "", CleanupManaged, CleanupOrphanObserved, CleanupRetiredKnown, CleanupEligible, CleanupDeleting, CleanupRemoved:
		return true
	default:
		return false
	}
}

// PhysicalResource is the sidecar record for one physical directory. Ownership
// fields (Repo/PublicSessionID) are set only when exact ownership has been
// proven; their presence is the prior-ownership evidence used after the owning
// session is pruned from RunnerState.
type PhysicalResource struct {
	ID               string       `json:"id"`
	Kind             ResourceKind `json:"kind"`
	Path             string       `json:"path"`
	Repo             string       `json:"repo,omitempty"`
	WorkspaceID      string       `json:"workspace_id,omitempty"`
	PublicSessionID  string       `json:"public_session_id,omitempty"`
	PhysicalHash     string       `json:"physical_hash,omitempty"`
	FirstObservedAt  time.Time    `json:"first_observed_at,omitempty"`
	CleanupState     CleanupState `json:"cleanup_state,omitempty"`
	CleanupAttemptID string       `json:"cleanup_attempt_id,omitempty"`
	LastCleanupError string       `json:"last_cleanup_error,omitempty"`
}

// Owned reports whether this entry carries proven ownership evidence.
func (r PhysicalResource) Owned() bool {
	return r.Repo != "" && r.PublicSessionID != ""
}

type StorageState struct {
	SchemaVersion int                         `json:"schema_version"`
	RootIdentity  string                      `json:"root_identity"`
	Resources     map[string]PhysicalResource `json:"resources"`
	UpdatedAt     time.Time                   `json:"updated_at,omitempty"`
}

// ResourceID builds the D2 identity string. Unowned inventory observations use
// empty repo/session segments until exact ownership is proven.
func ResourceID(kind ResourceKind, repo, publicSessionID, hash string) string {
	return string(kind) + ":" + strings.TrimSpace(repo) + ":" + strings.TrimSpace(publicSessionID) + ":" + strings.TrimSpace(hash)
}

// Canonicalize resolves a path exactly the way workspace roots are resolved:
// absolute, cleaned, symlink-evaluated, missing tails rejoined.
func Canonicalize(path string) (string, error) {
	return workspace.CanonicalPath(path)
}

// RootIdentity is sha256 of the canonical workspace root path.
func RootIdentity(workspaceRoot string) (string, error) {
	canonical, err := Canonicalize(workspaceRoot)
	if err != nil {
		return "", fmt.Errorf("canonicalize workspace root: %w", err)
	}
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:]), nil
}

// SessionRuntimeHash reproduces the dispatcher's stable runtime hash:
// sha256(repo \x00 publicSessionID \x00 Abs/Clean(workspacePath))[:16] hex.
// It deliberately does not evaluate symlinks, matching stableSessionRuntimeRoot.
func SessionRuntimeHash(repo, publicSessionID, workspacePath string) (string, error) {
	workspacePath = strings.TrimSpace(workspacePath)
	repo = strings.TrimSpace(repo)
	publicSessionID = strings.TrimSpace(publicSessionID)
	if workspacePath == "" || repo == "" || publicSessionID == "" {
		return "", fmt.Errorf("workspace path, repo, and public session id are required for session runtime hash")
	}
	absWorkspace, err := filepath.Abs(workspacePath)
	if err != nil {
		return "", fmt.Errorf("resolve workspace path for session runtime hash: %w", err)
	}
	cleanWorkspace := filepath.Clean(absWorkspace)
	sum := sha256.Sum256([]byte(repo + "\x00" + publicSessionID + "\x00" + cleanWorkspace))
	return hex.EncodeToString(sum[:16]), nil
}

// SessionRuntimeRoot maps a session workspace path to its stable runtime
// directory, exactly as the dispatcher does: a sibling `.sessions/<hash>`.
func SessionRuntimeRoot(workspacePath, repo, publicSessionID string) (string, error) {
	hash, err := SessionRuntimeHash(repo, publicSessionID, workspacePath)
	if err != nil {
		return "", err
	}
	absWorkspace, err := filepath.Abs(strings.TrimSpace(workspacePath))
	if err != nil {
		return "", fmt.Errorf("resolve workspace path for session runtime paths: %w", err)
	}
	cleanWorkspace := filepath.Clean(absWorkspace)
	runtimeBase := filepath.Dir(cleanWorkspace)
	if runtimeBase == cleanWorkspace {
		return "", fmt.Errorf("workspace path %q cannot be filesystem root for session runtime paths", cleanWorkspace)
	}
	return filepath.Join(runtimeBase, SessionsDirName, hash), nil
}

// SessionProcessPoolHash reproduces the dispatcher's pool hash, which is keyed
// by the symlink-evaluated canonical workspace path.
func SessionProcessPoolHash(repo, publicSessionID, canonicalWorkspace string) (string, error) {
	canonicalWorkspace = strings.TrimSpace(canonicalWorkspace)
	repo = strings.TrimSpace(repo)
	publicSessionID = strings.TrimSpace(publicSessionID)
	if canonicalWorkspace == "" || repo == "" || publicSessionID == "" {
		return "", fmt.Errorf("workspace path, repo, and public session id are required for process pool hash")
	}
	sum := sha256.Sum256([]byte(repo + "\x00" + publicSessionID + "\x00" + filepath.Clean(canonicalWorkspace)))
	return hex.EncodeToString(sum[:16]), nil
}

// RuntimeRootForHash places an already computed runtime hash under the root.
func RuntimeRootForHash(workspaceRoot, hash string) string {
	return filepath.Join(workspaceRoot, SessionsDirName, hash)
}

// ProcessPoolRootForHash places an already computed pool hash under the root.
func ProcessPoolRootForHash(workspaceRoot, hash string) string {
	return filepath.Join(workspaceRoot, ProcessPoolsDirName, hash)
}

// NewStorageState builds an empty sidecar bound to the given root identity.
func NewStorageState(rootIdentity string) StorageState {
	return StorageState{
		SchemaVersion: SidecarSchemaVersion,
		RootIdentity:  rootIdentity,
		Resources:     map[string]PhysicalResource{},
	}
}
