package github

import "context"

// IssueBackend is the issue-native surface. Self-hosted profiles implement
// this interface without pretending that their issue origin also hosts code.
type IssueBackend interface {
	GetUser(context.Context) (User, []string, error)
	CreateIssue(context.Context, string, string, string, []string) (Issue, error)
	GetIssue(context.Context, string, int) (Issue, error)
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
