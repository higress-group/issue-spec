package github

import (
	"context"
	"errors"
	"fmt"
)

var (
	// ErrConditionalCommentMutationUnsupported is returned before mutation when
	// a backend cannot prove caller-version CAS support.
	ErrConditionalCommentMutationUnsupported = errors.New("conditional comment mutation is unsupported")
	ErrCommentMutationConflict               = errors.New("comment representation conflict")
)

const (
	HeaderExpectedRepresentationVersion = "X-Issue-Spec-Expected-Representation-Version"
	HeaderConditionalCommentMutation    = "X-Issue-Spec-Conditional-Comment-Mutation"
	ConditionalCommentMutationVersion   = "representation-version"
)

type CommentMutationGuarantee string

const (
	CommentMutationStrictConditional CommentMutationGuarantee = "strict-conditional"
	// CommentMutationNonAtomicSingleWriter is an explicit compatibility boundary
	// for P004. It is never selected implicitly by this package.
	CommentMutationNonAtomicSingleWriter CommentMutationGuarantee = "non-atomic-single-writer"
)

type CommentRepresentation struct {
	Comment               Comment                  `json:"comment"`
	RepresentationVersion int64                    `json:"representation_version"`
	ETag                  string                   `json:"etag,omitempty"`
	Guarantee             CommentMutationGuarantee `json:"guarantee"`
}

// IssueCommentObservation is an additive, read-only provider capability for
// resolving one comment locator without enumerating an issue timeline.
// RepresentationVersion is zero when the backend does not expose one.
type IssueCommentObservation struct {
	Comment               Comment `json:"comment"`
	RepresentationVersion int64   `json:"representation_version,omitempty"`
}

type IssueCommentObserver interface {
	ObserveIssueComment(context.Context, string, int64) (IssueCommentObservation, error)
}

type ConditionalCommentBackend interface {
	GetCommentRepresentation(context.Context, string, int64) (CommentRepresentation, error)
	UpdateCommentConditional(context.Context, string, int64, int64, string) (CommentRepresentation, error)
}

type CommentMutationConflictError struct {
	Expected int64
	Current  int64
}

func (e *CommentMutationConflictError) Error() string {
	return fmt.Sprintf("comment representation conflict: expected=%d current=%d", e.Expected, e.Current)
}

func (e *CommentMutationConflictError) Unwrap() error { return ErrCommentMutationConflict }

func RequireConditionalCommentBackend(backend IssueBackend) (ConditionalCommentBackend, error) {
	conditional, ok := backend.(ConditionalCommentBackend)
	if !ok {
		return nil, ErrConditionalCommentMutationUnsupported
	}
	return conditional, nil
}

// IssueBackend is the issue-native surface. Self-hosted profiles implement
// this interface without pretending that their issue origin also hosts code.
type IssueBackend interface {
	GetUser(context.Context) (User, []string, error)
	CreateIssue(context.Context, string, string, string, []string) (Issue, error)
	GetIssue(context.Context, string, int) (Issue, error)
	ListIssues(context.Context, string, ListIssueOptions) ([]Issue, error)
	UpdateIssue(context.Context, string, int, UpdateIssueOptions) (Issue, error)
	ListIssueComments(context.Context, string, int) ([]Comment, error)
	CreateComment(context.Context, string, int, string) (Comment, error)
	UpdateComment(context.Context, string, int64, string) (Comment, error)
	CreateLabel(context.Context, string, string, string, string) (LabelResult, error)
}

// GitHubCodeBackend is the legacy GitHub implementation of code review
// operations. External-code self-hosted profiles use codereview.Provider
// instead; core must not infer code-host capabilities from IssueBackend.
type GitHubCodeBackend interface {
	GetPullRequest(context.Context, string, int) (PullRequest, error)
	UpdatePullRequest(context.Context, string, int, UpdatePullRequestOptions) (PullRequest, error)
	CreatePullRequest(context.Context, string, CreatePullRequestOptions) (PullRequest, error)
	ListPullRequestFiles(context.Context, string, int) ([]PullRequestFile, error)
	ListPullRequestReviewComments(context.Context, string, int) ([]PullRequestReviewComment, error)
	CreatePullRequestReviewComment(context.Context, string, int, string, string, string, int, string) (PullRequestReviewComment, error)
	ReplyPullRequestReviewComment(context.Context, string, int, int64, string) (PullRequestReviewComment, error)
	GetCombinedStatus(context.Context, string, string) (CombinedStatus, error)
	ListCheckRuns(context.Context, string, string) ([]CheckRun, error)
}

// PullRequestCommitBackend is an optional GitHub code-host capability. Keeping
// it separate from GitHubCodeBackend lets issue-only and external-code
// backends remain honest about the surfaces they implement.
type PullRequestCommitBackend interface {
	ListPullRequestCommits(context.Context, string, int) ([]PullRequestCommit, error)
}

// Operations preserves the existing GitHub adapter contract while exposing
// the two independent boundaries to new profile-aware command code.
type Operations interface {
	IssueBackend
	GitHubCodeBackend
}

type BackendInfo struct {
	Name string
	Kind string
	Host string
}

type Backend interface {
	Operations
	BackendInfo() BackendInfo
}

var _ Operations = (*Client)(nil)
var _ Backend = (*Client)(nil)
