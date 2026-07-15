package models

import (
	"time"

	"github.com/google/uuid"
)

type IssueState string

const (
	IssueStateOpen   IssueState = "open"
	IssueStateClosed IssueState = "closed"
)

type Issue struct {
	ID                          uuid.UUID
	Scope                       RepoScope
	Number                      int64
	AuthorID                    *uuid.UUID
	Title                       string
	Body                        string
	State                       IssueState
	RepresentationVersion       int64
	CommentsCollectionVersion   int64
	LabelsCollectionVersion     int64
	BindingsCollectionVersion   int64
	ReferencesCollectionVersion int64
	EvidenceCollectionVersion   int64
	CreatedAt                   time.Time
	UpdatedAt                   time.Time
	ClosedAt                    *time.Time
}

type RepositoryResource struct {
	Scope                     RepoScope
	Owner                     string
	Name                      string
	Visibility                Visibility
	IssuesCollectionVersion   int64
	CommentsCollectionVersion int64
	UpdatedAt                 time.Time
}

type IssueSnapshot struct {
	Issue                       Issue
	AuthorLogin                 string
	AuthorDisplayName           string
	AuthorRepresentationVersion int64
	AuthorUpdatedAt             time.Time
	Labels                      []Label
	CommentCount                int
}

type IssuePage struct {
	Items             []IssueSnapshot
	Total             int
	CollectionVersion int64
	LastModified      time.Time
}

type IssueListOptions struct {
	State   *IssueState
	Labels  []string
	Since   *time.Time
	Page    int
	PerPage int
}

type NewIssue struct {
	ID       uuid.UUID
	AuthorID *uuid.UUID
	Title    string
	Body     string
	Labels   []string
}

type IssueUpdate struct {
	Title string
	Body  string
	State IssueState
}

type Comment struct {
	ID                         uuid.UUID
	Scope                      RepoScope
	IssueID                    uuid.UUID
	AuthorID                   *uuid.UUID
	Body                       string
	RepresentationVersion      int64
	ReactionsCollectionVersion int64
	CreatedAt                  time.Time
	UpdatedAt                  time.Time
}

type CommentSnapshot struct {
	Comment                     Comment
	IssueNumber                 int64
	AuthorLogin                 string
	AuthorDisplayName           string
	AuthorRepresentationVersion int64
	AuthorUpdatedAt             time.Time
	Reactions                   ReactionSummary
}

type CommentPage struct {
	Items             []CommentSnapshot
	Total             int
	CollectionVersion int64
	LastModified      time.Time
}

type CommentListOptions struct {
	IssueNumber *int64
	Since       *time.Time
	Page        int
	PerPage     int
}

type NewComment struct {
	ID          uuid.UUID
	IssueNumber int64
	AuthorID    *uuid.UUID
	Body        string
}

type Label struct {
	ID                    uuid.UUID
	Scope                 RepoScope
	Name                  string
	Color                 string
	Description           string
	RepresentationVersion int64
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

type IssueLabel struct {
	Scope            RepoScope
	IssueID          uuid.UUID
	LabelID          uuid.UUID
	AssignedByUserID *uuid.UUID
	CreatedAt        time.Time
}

type CommentReaction struct {
	ID              uuid.UUID
	CompatibilityID int64
	Scope           RepoScope
	IssueID         uuid.UUID
	CommentID       uuid.UUID
	UserID          *uuid.UUID
	AuthorLogin     string
	IdentityKey     string
	ReactionKey     string
	CreatedAt       time.Time
}

type ReactionSummary struct {
	TotalCount int
	PlusOne    int
	MinusOne   int
	Laugh      int
	Hooray     int
	Confused   int
	Heart      int
	Rocket     int
	Eyes       int
}

type ReactionPage struct {
	CommentID         uuid.UUID
	Items             []CommentReaction
	Total             int
	CollectionVersion int64
	LastModified      time.Time
}

type ReactionMutation struct {
	Reaction CommentReaction
	Comment  CommentSnapshot
	Created  bool
}

type LabelPage struct {
	Items             []Label
	Total             int
	CollectionVersion int64
	LastModified      time.Time
}

type NewLabel struct {
	ID          uuid.UUID
	Name        string
	Color       string
	Description string
}

type LabelUpdate struct {
	Name        string
	Color       string
	Description string
}

type RepositorySubscription struct {
	UserID                uuid.UUID
	Subscribed            bool
	Ignored               bool
	Reason                string
	RepresentationVersion int64
	CollectionVersion     int64
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

type ProtocolUser struct {
	ID        uuid.UUID
	Login     string
	Status    string
	UpdatedAt time.Time
}
