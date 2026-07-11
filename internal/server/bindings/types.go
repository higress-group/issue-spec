// Package bindings owns credential-free source bindings and mutable external
// references. All repository-owned operations require a composite tenant scope.
package bindings

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
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
}
