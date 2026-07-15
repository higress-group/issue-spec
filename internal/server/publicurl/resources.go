package publicurl

import (
	"net/url"
	"strconv"
)

// RepositoryResourcePaths is the canonical path contract shared by the
// GitHub-compatible API and browser-facing repository surfaces.
type RepositoryResourcePaths struct {
	owner      string
	repository string
}

// RepositoryResource constructs paths for one owner/repository pair. Each
// name remains a single escaped path segment.
func RepositoryResource(owner, repository string) RepositoryResourcePaths {
	return RepositoryResourcePaths{owner: url.PathEscape(owner), repository: url.PathEscape(repository)}
}

func (p RepositoryResourcePaths) API() string {
	return "/repos/" + p.owner + "/" + p.repository
}

func (p RepositoryResourcePaths) Web() string {
	return "/" + p.owner + "/" + p.repository
}

func (p RepositoryResourcePaths) IssuesAPI() string {
	return p.API() + "/issues"
}

func (p RepositoryResourcePaths) IssuesWeb() string {
	return p.Web() + "/issues"
}

func (p RepositoryResourcePaths) IssueAPI(number int64) string {
	return p.IssuesAPI() + "/" + strconv.FormatInt(number, 10)
}

func (p RepositoryResourcePaths) IssueWeb(number int64) string {
	return p.IssuesWeb() + "/" + strconv.FormatInt(number, 10)
}

func (p RepositoryResourcePaths) CommentAPI(commentID int64) string {
	return p.API() + "/issues/comments/" + strconv.FormatInt(commentID, 10)
}
