package models

import (
	"errors"

	"github.com/google/uuid"
)

var (
	ErrOrganizationScopeRequired = errors.New("organization scope is required")
	ErrRepositoryScopeRequired   = errors.New("repository scope is required")
)

// OrgScope is the minimum tenant scope accepted by organization-owned stores.
type OrgScope struct {
	OrgID uuid.UUID
}

func (s OrgScope) Validate() error {
	if s.OrgID == uuid.Nil {
		return ErrOrganizationScopeRequired
	}
	return nil
}

// RepoScope deliberately carries both tenant identifiers. Repository-owned
// queries must use both values so an otherwise-valid UUID from another tenant
// cannot be used accidentally.
type RepoScope struct {
	OrgID  uuid.UUID
	RepoID uuid.UUID
}

func (s RepoScope) Validate() error {
	if s.OrgID == uuid.Nil {
		return ErrOrganizationScopeRequired
	}
	if s.RepoID == uuid.Nil {
		return ErrRepositoryScopeRequired
	}
	return nil
}
