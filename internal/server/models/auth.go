package models

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

type UserStatus string

const (
	UserStatusActive   UserStatus = "active"
	UserStatusDisabled UserStatus = "disabled"
)

type User struct {
	ID          uuid.UUID
	Login       string
	DisplayName string
	Email       *string
	Status      UserStatus
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type IdentityProvider struct {
	ID        uuid.UUID
	Name      string
	Kind      string
	Issuer    string
	Enabled   bool
	Config    json.RawMessage
	CreatedAt time.Time
	UpdatedAt time.Time
}

// AuthProvider is the schema-aligned name; IdentityProvider remains as the
// public compatibility spelling for callers that model the adapter itself.
type AuthProvider = IdentityProvider

type UserIdentity struct {
	ID         uuid.UUID
	UserID     uuid.UUID
	ProviderID uuid.UUID
	Issuer     string
	Subject    string
	Claims     json.RawMessage
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

type OAuthLoginTransaction struct {
	ID                 uuid.UUID
	ProviderID         uuid.UUID
	StateHash          []byte
	NonceHash          []byte
	PKCEVerifierCipher []byte
	RedirectURI        string
	ReturnTo           *string
	ExpiresAt          time.Time
	ConsumedAt         *time.Time
	CreatedAt          time.Time
}

type Session struct {
	ID                uuid.UUID
	UserID            uuid.UUID
	TokenPrefix       string
	TokenHash         []byte
	CSRFHash          []byte
	UserAgent         *string
	RemoteAddress     *string
	IdleExpiresAt     time.Time
	AbsoluteExpiresAt time.Time
	LastSeenAt        time.Time
	RevokedAt         *time.Time
	CreatedAt         time.Time
}

type PersonalAccessToken struct {
	ID                    uuid.UUID
	UserID                uuid.UUID
	Name                  string
	TokenPrefix           string
	TokenHash             []byte
	RepresentationVersion int64
	ExpiresAt             *time.Time
	LastUsedAt            *time.Time
	RevokedAt             *time.Time
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

type PATScope struct {
	ID                    uuid.UUID
	PersonalAccessTokenID uuid.UUID
	Scope                 string
	CreatedAt             time.Time
}

type DelegatedToken struct {
	ID                    uuid.UUID
	UserID                uuid.UUID
	PersonalAccessTokenID *uuid.UUID
	Scope                 RepoScope
	JobID                 string
	Purpose               string
	TokenHash             []byte
	Audience              string
	Subject               string
	Claims                json.RawMessage
	ExpiresAt             time.Time
	UsedAt                *time.Time
	RevokedAt             *time.Time
	CreatedAt             time.Time
}

type Organization struct {
	ID                    uuid.UUID
	Name                  string
	DisplayName           string
	RepresentationVersion int64
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

type Repository struct {
	Scope                          RepoScope
	Name                           string
	DisplayName                    string
	NextIssueNumber                int64
	RepresentationVersion          int64
	IssuesCollectionVersion        int64
	LabelsCollectionVersion        int64
	ArtifactsCollectionVersion     int64
	WebhooksCollectionVersion      int64
	CommentsCollectionVersion      int64
	ReactionsCollectionVersion     int64
	BindingsCollectionVersion      int64
	ReferencesCollectionVersion    int64
	EvidenceCollectionVersion      int64
	CollaboratorsCollectionVersion int64
	SubscriptionsCollectionVersion int64
	CreatedAt                      time.Time
	UpdatedAt                      time.Time
}

type OrgMembership struct {
	ID                    uuid.UUID
	OrgID                 uuid.UUID
	UserID                uuid.UUID
	Role                  string
	RepresentationVersion int64
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

type RepoCollaborator struct {
	ID                    uuid.UUID
	Scope                 RepoScope
	UserID                uuid.UUID
	Role                  string
	RepresentationVersion int64
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

type RepoSubscription struct {
	ID                    uuid.UUID
	Scope                 RepoScope
	UserID                uuid.UUID
	Reason                string
	RepresentationVersion int64
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

type SiteRoleAssignment struct {
	ID                    uuid.UUID
	UserID                uuid.UUID
	Role                  string
	RepresentationVersion int64
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

type BootstrapState struct {
	ID                    uuid.UUID
	Completed             bool
	CompletedByUserID     *uuid.UUID
	CompletedAt           *time.Time
	RepresentationVersion int64
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

type AuditEvent struct {
	ID               uuid.UUID
	OrgID            *uuid.UUID
	RepoID           *uuid.UUID
	ActorUserID      *uuid.UUID
	ActorIdentityKey string
	Action           string
	ResourceType     string
	ResourceID       *uuid.UUID
	RequestID        string
	RemoteAddress    *string
	Metadata         json.RawMessage
	CreatedAt        time.Time
}
