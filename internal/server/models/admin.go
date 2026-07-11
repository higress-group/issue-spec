package models

import (
	"time"

	"github.com/google/uuid"
)

type BasePermission string

const (
	BasePermissionNone     BasePermission = "none"
	BasePermissionRead     BasePermission = "read"
	BasePermissionTriage   BasePermission = "triage"
	BasePermissionWrite    BasePermission = "write"
	BasePermissionMaintain BasePermission = "maintain"
	BasePermissionAdmin    BasePermission = "admin"
)

func (p BasePermission) Valid() bool {
	switch p {
	case BasePermissionNone, BasePermissionRead, BasePermissionTriage, BasePermissionWrite,
		BasePermissionMaintain, BasePermissionAdmin:
		return true
	default:
		return false
	}
}

type Visibility string

const (
	VisibilityPublic   Visibility = "public"
	VisibilityInternal Visibility = "internal"
	VisibilityPrivate  Visibility = "private"
)

func (v Visibility) Valid() bool {
	return v == VisibilityPublic || v == VisibilityInternal || v == VisibilityPrivate
}

type ContributionPolicy string

const (
	ContributionDisabled      ContributionPolicy = "disabled"
	ContributionMembers       ContributionPolicy = "members"
	ContributionAuthenticated ContributionPolicy = "authenticated"
	ContributionPublic        ContributionPolicy = "public"
)

func (p ContributionPolicy) Valid() bool {
	switch p {
	case ContributionDisabled, ContributionMembers, ContributionAuthenticated, ContributionPublic:
		return true
	default:
		return false
	}
}

type MembershipState string

const (
	MembershipInvited   MembershipState = "invited"
	MembershipActive    MembershipState = "active"
	MembershipSuspended MembershipState = "suspended"
)

func (s MembershipState) Valid() bool {
	return s == MembershipInvited || s == MembershipActive || s == MembershipSuspended
}

type AdminOrganization struct {
	ID                            uuid.UUID      `json:"id"`
	Name                          string         `json:"name"`
	DisplayName                   string         `json:"display_name"`
	Description                   string         `json:"description"`
	BasePermission                BasePermission `json:"base_permission"`
	RepresentationVersion         int64          `json:"representation_version"`
	RepositoriesCollectionVersion int64          `json:"repositories_collection_version"`
	MembersCollectionVersion      int64          `json:"members_collection_version"`
	ArchivedAt                    *time.Time     `json:"archived_at,omitempty"`
	CreatedAt                     time.Time      `json:"created_at"`
	UpdatedAt                     time.Time      `json:"updated_at"`
}

type AdminMembership struct {
	ID                    uuid.UUID       `json:"id"`
	OrganizationID        uuid.UUID       `json:"organization_id"`
	UserID                uuid.UUID       `json:"user_id"`
	Role                  string          `json:"role"`
	State                 MembershipState `json:"state"`
	InvitedByUserID       *uuid.UUID      `json:"invited_by_user_id,omitempty"`
	InvitedAt             time.Time       `json:"invited_at"`
	ActivatedAt           *time.Time      `json:"activated_at,omitempty"`
	ArchivedAt            *time.Time      `json:"archived_at,omitempty"`
	RepresentationVersion int64           `json:"representation_version"`
	CreatedAt             time.Time       `json:"created_at"`
	UpdatedAt             time.Time       `json:"updated_at"`
}

type AdminRepository struct {
	Scope                          RepoScope          `json:"-"`
	ID                             uuid.UUID          `json:"id"`
	OrganizationID                 uuid.UUID          `json:"organization_id"`
	Name                           string             `json:"name"`
	DisplayName                    string             `json:"display_name"`
	Description                    string             `json:"description"`
	Visibility                     Visibility         `json:"visibility"`
	DefaultBranch                  string             `json:"default_branch"`
	ContributionPolicy             ContributionPolicy `json:"contribution_policy"`
	RepresentationVersion          int64              `json:"representation_version"`
	CollaboratorsCollectionVersion int64              `json:"collaborators_collection_version"`
	ArchivedAt                     *time.Time         `json:"archived_at,omitempty"`
	CreatedAt                      time.Time          `json:"created_at"`
	UpdatedAt                      time.Time          `json:"updated_at"`
}

type AdminCollaborator struct {
	ID                    uuid.UUID  `json:"id"`
	OrganizationID        uuid.UUID  `json:"organization_id"`
	RepositoryID          uuid.UUID  `json:"repository_id"`
	UserID                uuid.UUID  `json:"user_id"`
	Role                  string     `json:"role"`
	RepresentationVersion int64      `json:"representation_version"`
	ArchivedAt            *time.Time `json:"archived_at,omitempty"`
	CreatedAt             time.Time  `json:"created_at"`
	UpdatedAt             time.Time  `json:"updated_at"`
}

type AdminServiceAccount struct {
	ID                    uuid.UUID  `json:"id"`
	UserID                uuid.UUID  `json:"user_id"`
	OrganizationID        uuid.UUID  `json:"organization_id"`
	Name                  string     `json:"name"`
	Login                 string     `json:"login"`
	DisabledAt            *time.Time `json:"disabled_at,omitempty"`
	RepresentationVersion int64      `json:"representation_version"`
	CreatedAt             time.Time  `json:"created_at"`
	UpdatedAt             time.Time  `json:"updated_at"`
}

type AdminPAT struct {
	ID                    uuid.UUID   `json:"id"`
	OrganizationID        uuid.UUID   `json:"organization_id"`
	UserID                uuid.UUID   `json:"user_id"`
	Name                  string      `json:"name"`
	Prefix                string      `json:"token_prefix"`
	Scopes                []string    `json:"scopes"`
	RepositoryIDs         []uuid.UUID `json:"repository_ids"`
	ExpiresAt             *time.Time  `json:"expires_at,omitempty"`
	LastUsedAt            *time.Time  `json:"last_used_at,omitempty"`
	RevokedAt             *time.Time  `json:"revoked_at,omitempty"`
	RepresentationVersion int64       `json:"representation_version"`
	CreatedAt             time.Time   `json:"created_at"`
	UpdatedAt             time.Time   `json:"updated_at"`
}

type BootstrapStatus struct {
	Available     bool       `json:"available"`
	Completed     bool       `json:"completed"`
	CompletedByID *uuid.UUID `json:"completed_by_user_id,omitempty"`
	CompletedAt   *time.Time `json:"completed_at,omitempty"`
	Version       int64      `json:"representation_version"`
}
