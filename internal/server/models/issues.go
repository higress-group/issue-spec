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

type NewIssue struct {
	ID       uuid.UUID
	AuthorID *uuid.UUID
	Title    string
	Body     string
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
	ID          uuid.UUID
	Scope       RepoScope
	IssueID     uuid.UUID
	CommentID   uuid.UUID
	UserID      *uuid.UUID
	IdentityKey string
	ReactionKey string
	CreatedAt   time.Time
}
