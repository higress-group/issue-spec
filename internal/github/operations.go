package github

import "context"

// Operations is the command-facing GitHub API surface implemented by backends.
type Operations interface {
	GetUser(context.Context) (User, []string, error)
	CreateIssue(context.Context, string, string, string, []string) (Issue, error)
	GetIssue(context.Context, string, int) (Issue, error)
	UpdateIssue(context.Context, string, int, UpdateIssueOptions) (Issue, error)
	ListIssueComments(context.Context, string, int) ([]Comment, error)
	CreateComment(context.Context, string, int, string) (Comment, error)
	UpdateComment(context.Context, string, int64, string) (Comment, error)
	CreateLabel(context.Context, string, string, string, string) (LabelResult, error)
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
