// Package bindings owns credential-free source bindings and mutable external
// references. All repository-owned operations require a composite tenant scope.
package bindings

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	adminservice "github.com/higress-group/issue-spec/internal/server/admin"
	"github.com/higress-group/issue-spec/internal/server/models"
)

type Visibility string

const (
	VisibilityRepository  Visibility = "repository"
	VisibilityMaintainers Visibility = "maintainers"
)

type Binding struct {
	ID                   uuid.UUID        `json:"id"`
	Scope                models.RepoScope `json:"-"`
	ProviderKey          string           `json:"provider_key"`
	ExternalRepositoryID string           `json:"external_repository_id"`
	CloneURL             string           `json:"clone_url"`
	WebURL               string           `json:"web_url"`
	DefaultBranch        string           `json:"default_branch"`
	Version              int64            `json:"version"`
	Active               bool             `json:"active"`
	CreatedAt            time.Time        `json:"created_at"`
	UpdatedAt            time.Time        `json:"updated_at"`
}

type CreateBindingVersionInput struct {
	ProviderKey          string `json:"provider_key"`
	ExternalRepositoryID string `json:"external_repository_id"`
	CloneURL             string `json:"clone_url"`
	WebURL               string `json:"web_url"`
	DefaultBranch        string `json:"default_branch"`
}

type EnsureBindingResult struct {
	Binding Binding `json:"binding"`
	Created bool    `json:"created"`
}

type Reference struct {
	ID                    uuid.UUID        `json:"id"`
	Scope                 models.RepoScope `json:"-"`
	IssueID               uuid.UUID        `json:"issue_id"`
	ProviderKey           string           `json:"provider_key"`
	RelationKind          string           `json:"relation_kind"`
	ExternalRepositoryID  string           `json:"external_repository_id"`
	ExternalID            string           `json:"external_id"`
	CanonicalURL          string           `json:"canonical_url"`
	Title                 *string          `json:"title,omitempty"`
	LifecycleState        string           `json:"lifecycle_state"`
	Visibility            Visibility       `json:"visibility"`
	Metadata              json.RawMessage  `json:"metadata,omitempty"`
	RepresentationVersion int64            `json:"representation_version"`
	CreatedAt             time.Time        `json:"created_at"`
	UpdatedAt             time.Time        `json:"updated_at"`
}

type UpsertReferenceInput struct {
	IssueID              uuid.UUID       `json:"issue_id"`
	ProviderKey          string          `json:"provider_key"`
	RelationKind         string          `json:"relation_kind"`
	ExternalRepositoryID string          `json:"external_repository_id"`
	ExternalID           string          `json:"external_id"`
	CanonicalURL         string          `json:"canonical_url"`
	Title                *string         `json:"title,omitempty"`
	LifecycleState       string          `json:"lifecycle_state"`
	Visibility           Visibility      `json:"visibility"`
	Metadata             json.RawMessage `json:"metadata,omitempty"`
	Refresh              bool            `json:"refresh,omitempty"`
	ExpectedVersion      *int64          `json:"expected_version,omitempty"`
}

type CodeChangeConflictReason string

const (
	CodeChangeConflictAmbiguousActiveReferences CodeChangeConflictReason = "ambiguous_active_references"
	CodeChangeConflictCanonicalURLDrift         CodeChangeConflictReason = "canonical_url_drift"
	CodeChangeConflictDifferentActiveChange     CodeChangeConflictReason = "different_active_change"
	CodeChangeConflictHiddenActiveReferences    CodeChangeConflictReason = "hidden_active_references"
	CodeChangeConflictInvalidActiveReference    CodeChangeConflictReason = "invalid_active_reference"
	CodeChangeConflictRefreshRequired           CodeChangeConflictReason = "refresh_required"
	CodeChangeConflictStaleReferenceVersion     CodeChangeConflictReason = "stale_reference_version"
)

// ReferenceIdentity is the credential-free, URL-free identity returned only
// for active code-change references visible to the caller. The reference ID
// supports repair through the existing list/delete operations.
type ReferenceIdentity struct {
	ID                    uuid.UUID `json:"id"`
	ProviderKey           string    `json:"provider_key"`
	ExternalRepositoryID  string    `json:"external_repository_id"`
	ExternalID            string    `json:"external_id"`
	RepresentationVersion int64     `json:"representation_version"`
}

// CodeChangeConflictError describes an expected relationship conflict without
// exposing stored canonical URLs or metadata. It remains compatible with the
// existing conflict mapping through errors.Is(err, admin.ErrConflict).
type CodeChangeConflictError struct {
	Reason     CodeChangeConflictReason `json:"reason"`
	References []ReferenceIdentity      `json:"references,omitempty"`
}

func (e *CodeChangeConflictError) Error() string {
	return fmt.Sprintf("active code-change conflict (%s)", e.Reason)
}

func (e *CodeChangeConflictError) Unwrap() error { return adminservice.ErrConflict }
