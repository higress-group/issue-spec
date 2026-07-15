package publicurl

import (
	"net/url"
	"strconv"
	"strings"
)

var reservedRepositoryOwners = map[string]struct{}{
	"_repos": {}, "admin": {}, "api": {}, "assets": {}, "auth": {}, "bootstrap": {},
	"changes": {}, "issues": {}, "livez": {}, "login": {}, "metrics": {},
	"notifications": {}, "orgs": {}, "readyz": {}, "repos": {}, "settings": {},
	"user": {}, "users": {},
}

// IsCanonicalRepositoryOwner reports whether owner can safely occupy the
// browser's top-level /{owner}/{repository} namespace. Reserved names are
// rejected for new organizations; existing rows use the /_repos fallback.
func IsCanonicalRepositoryOwner(owner string) bool {
	owner = strings.ToLower(strings.TrimSpace(owner))
	if owner == "" {
		return false
	}
	_, reserved := reservedRepositoryOwners[owner]
	return !reserved
}

// RepositoryResourcePaths is the canonical path contract shared by the
// GitHub-compatible API and browser-facing repository surfaces.
type RepositoryResourcePaths struct {
	owner      string
	repository string
	fallback   bool
}

// RepositoryResource constructs paths for one owner/repository pair. Each
// name remains a single escaped path segment.
func RepositoryResource(owner, repository string) RepositoryResourcePaths {
	return RepositoryResourcePaths{owner: url.PathEscape(owner), repository: url.PathEscape(repository),
		fallback: !IsCanonicalRepositoryOwner(owner)}
}

func (p RepositoryResourcePaths) API() string {
	return "/repos/" + p.owner + "/" + p.repository
}

func (p RepositoryResourcePaths) Web() string {
	if p.fallback {
		return "/_repos/" + p.owner + "/" + p.repository
	}
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
