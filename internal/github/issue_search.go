package github

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	githubTextMatchMediaType   = "application/vnd.github.text-match+json"
	maxIssueSearchMatches      = 4
	maxIssueSearchExcerptRunes = 320
)

// IssueSearchOperations is the optional GitHub issue-search capability used
// by the backend-neutral search command. IssueBackend deliberately remains
// issue CRUD only, so other backends do not have to pretend they can search.
type IssueSearchOperations interface {
	SearchIssues(context.Context, string, IssueSearchOptions) (IssueSearchPage, error)
}

type IssueSearchOptions struct {
	Query   string
	State   string
	Source  string
	Stage   string
	Page    int
	PerPage int
}

type IssueSearchPage struct {
	Items   []IssueSearchResult `json:"items"`
	Page    int                 `json:"page"`
	PerPage int                 `json:"per_page"`
	Total   int64               `json:"total"`
	HasNext bool                `json:"has_next"`
}

type IssueSearchResult struct {
	Organization string              `json:"organization"`
	Repository   string              `json:"repository"`
	Number       int64               `json:"number"`
	Title        string              `json:"title"`
	State        string              `json:"state"`
	URL          string              `json:"url"`
	UpdatedAt    time.Time           `json:"updated_at"`
	Changes      []IssueSearchChange `json:"changes"`
	Matches      []IssueSearchMatch  `json:"matches"`
}

type IssueSearchChange struct {
	Key     string `json:"key"`
	Stage   string `json:"stage"`
	Matched bool   `json:"matched"`
}

type IssueSearchMatch struct {
	Source  string `json:"source"`
	Excerpt string `json:"excerpt"`
}

type githubIssueSearchResponse struct {
	TotalCount int64                   `json:"total_count"`
	Items      []githubIssueSearchItem `json:"items"`
}

type githubIssueSearchItem struct {
	Number        int64                    `json:"number"`
	Title         string                   `json:"title"`
	Body          string                   `json:"body"`
	State         string                   `json:"state"`
	HTMLURL       string                   `json:"html_url"`
	RepositoryURL string                   `json:"repository_url"`
	UpdatedAt     time.Time                `json:"updated_at"`
	PullRequest   *struct{}                `json:"pull_request,omitempty"`
	Labels        []githubIssueSearchLabel `json:"labels"`
	TextMatches   []githubIssueTextMatch   `json:"text_matches"`
}

type githubIssueSearchLabel struct {
	Name string `json:"name"`
}

type githubIssueTextMatch struct {
	ObjectType string `json:"object_type"`
	Property   string `json:"property"`
	Fragment   string `json:"fragment"`
}

func (c *Client) SearchIssues(ctx context.Context, repo string, options IssueSearchOptions) (IssueSearchPage, error) {
	query, normalized, err := githubIssueSearchQuery(repo, options)
	if err != nil {
		return IssueSearchPage{}, err
	}
	var response githubIssueSearchResponse
	path := endpointWithQuery("/search/issues", url.Values{
		"q":        {query},
		"page":     {strconv.Itoa(normalized.Page)},
		"per_page": {strconv.Itoa(normalized.PerPage)},
	})
	if _, err := c.doWithHeaders(ctx, http.MethodGet, path, nil, &response,
		http.Header{"Accept": []string{githubTextMatchMediaType}}); err != nil {
		return IssueSearchPage{}, err
	}
	return normalizeGitHubIssueSearch(repo, normalized, response), nil
}

func (b *GHBackend) SearchIssues(ctx context.Context, repo string, options IssueSearchOptions) (IssueSearchPage, error) {
	query, normalized, err := githubIssueSearchQuery(repo, options)
	if err != nil {
		return IssueSearchPage{}, err
	}
	result, err := b.cli.RunAPI(ctx, b.Host, ExternalCLIAPIRequest{
		Operation: "SearchIssues",
		Method:    http.MethodGet,
		Endpoint:  "/search/issues",
		Query: url.Values{
			"q":        {query},
			"page":     {strconv.Itoa(normalized.Page)},
			"per_page": {strconv.Itoa(normalized.PerPage)},
		},
		Headers: http.Header{"Accept": []string{githubTextMatchMediaType}},
	})
	if err != nil {
		return IssueSearchPage{}, err
	}
	var response githubIssueSearchResponse
	if err := DecodeCLIJSON(result.Stdout, &response); err != nil {
		return IssueSearchPage{}, err
	}
	return normalizeGitHubIssueSearch(repo, normalized, response), nil
}

func githubIssueSearchQuery(repo string, options IssueSearchOptions) (string, IssueSearchOptions, error) {
	parsed, err := ParseRepo(repo)
	if err != nil {
		return "", IssueSearchOptions{}, err
	}
	parts := strings.Split(parsed, "/")
	owner, ownerErr := url.PathUnescape(parts[0])
	repository, repoErr := url.PathUnescape(parts[1])
	if ownerErr != nil || repoErr != nil {
		return "", IssueSearchOptions{}, errors.New("GitHub issue search repository is invalid")
	}
	options.Query = strings.TrimSpace(options.Query)
	options.State = strings.ToLower(strings.TrimSpace(options.State))
	options.Source = strings.ToLower(strings.TrimSpace(options.Source))
	options.Stage = strings.ToLower(strings.TrimSpace(options.Stage))
	if options.Source == "" {
		options.Source = "all"
	}
	if options.Page == 0 {
		options.Page = 1
	}
	if options.Query == "" || len(options.Query) > 256 || options.Page < 1 || options.PerPage < 1 || options.PerPage > 50 {
		return "", IssueSearchOptions{}, errors.New("GitHub issue search options are invalid")
	}
	if options.State != "all" && options.State != "open" && options.State != "closed" {
		return "", IssueSearchOptions{}, errors.New("GitHub issue search state is invalid")
	}
	if options.Source != "all" && options.Source != "issue" {
		return "", IssueSearchOptions{}, errors.New("GitHub issue search source must be all or issue; search is limited to Proposal titles and bodies")
	}
	if options.Stage != "" && options.Stage != "proposal" {
		return "", IssueSearchOptions{}, errors.New("GitHub issue search stage must be proposal")
	}
	options.Source = "issue"
	options.Stage = "proposal"

	qualifiers := []string{options.Query, "repo:" + owner + "/" + repository, "is:issue"}
	if options.State != "all" {
		qualifiers = append(qualifiers, "is:"+options.State)
	}
	qualifiers = append(qualifiers, "in:title,body", `label:"issue-spec/proposal"`)
	return strings.Join(qualifiers, " "), options, nil
}

func normalizeGitHubIssueSearch(repo string, options IssueSearchOptions, response githubIssueSearchResponse) IssueSearchPage {
	parts := strings.Split(strings.TrimSpace(repo), "/")
	if response.TotalCount < 0 {
		response.TotalCount = 0
	}
	page := IssueSearchPage{Items: []IssueSearchResult{}, Page: options.Page, PerPage: options.PerPage,
		Total: response.TotalCount}
	rejected := false
	for _, item := range response.Items {
		if item.PullRequest != nil || !githubSearchRepositoryMatches(item.RepositoryURL, repo) ||
			(options.State != "all" && !strings.EqualFold(item.State, options.State)) ||
			!githubSearchHasLabel(item.Labels, "issue-spec/proposal") {
			rejected = true
			continue
		}
		matches := githubIssueMatches(item, options)
		if len(item.TextMatches) > 0 && len(matches) == 0 {
			rejected = true
			continue
		}
		if len(page.Items) == options.PerPage {
			page.HasNext = true
			continue
		}
		page.Items = append(page.Items, IssueSearchResult{
			Organization: parts[0], Repository: parts[1], Number: item.Number, Title: item.Title,
			State: item.State, URL: item.HTMLURL, UpdatedAt: item.UpdatedAt,
			Matches: matches,
		})
	}
	if rejected {
		// Unexpected out-of-scope records make the provider total unsuitable for
		// display. Prefer a conservative bounded count over claiming they match.
		page.Total = int64(len(page.Items))
		page.HasNext = false
	} else if response.TotalCount > int64(options.Page*options.PerPage) {
		page.HasNext = true
	}
	return page
}

func githubSearchRepositoryMatches(repositoryURL, repo string) bool {
	parsed, err := ParseRepo(repo)
	if err != nil {
		return false
	}
	u, err := url.Parse(strings.TrimSpace(repositoryURL))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return false
	}
	return strings.HasSuffix(strings.TrimRight(u.EscapedPath(), "/"), "/repos/"+parsed)
}

func githubSearchHasLabel(labels []githubIssueSearchLabel, want string) bool {
	for _, label := range labels {
		if strings.EqualFold(strings.TrimSpace(label.Name), want) {
			return true
		}
	}
	return false
}

func githubIssueMatches(item githubIssueSearchItem, options IssueSearchOptions) []IssueSearchMatch {
	matches := make([]IssueSearchMatch, 0, min(len(item.TextMatches), maxIssueSearchMatches))
	for _, match := range item.TextMatches {
		if len(matches) == maxIssueSearchMatches {
			break
		}
		if !strings.EqualFold(match.ObjectType, "Issue") {
			continue
		}
		excerpt := boundedIssueSearchExcerpt(match.Fragment, options.Query)
		if excerpt != "" {
			matches = append(matches, IssueSearchMatch{Source: "issue", Excerpt: excerpt})
		}
	}
	if len(matches) == 0 && len(item.TextMatches) == 0 {
		if excerpt := boundedIssueSearchExcerpt(item.Title+"\n"+item.Body, options.Query); excerpt != "" {
			matches = append(matches, IssueSearchMatch{Source: "issue", Excerpt: excerpt})
		}
	}
	return matches
}

func boundedIssueSearchExcerpt(value, query string) string {
	value = strings.Join(strings.Fields(value), " ")
	if value == "" {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= maxIssueSearchExcerptRunes {
		return value
	}
	position := strings.Index(strings.ToLower(value), strings.ToLower(strings.TrimSpace(query)))
	start := 0
	if position >= 0 {
		start = utf8.RuneCountInString(value[:position]) - maxIssueSearchExcerptRunes/2
		if start < 0 {
			start = 0
		}
	}
	end := start + maxIssueSearchExcerptRunes
	if end > len(runes) {
		end = len(runes)
		start = max(0, end-maxIssueSearchExcerptRunes)
	}
	prefix, suffix := "", ""
	if start > 0 {
		prefix = "…"
	}
	if end < len(runes) {
		suffix = "…"
	}
	return prefix + string(runes[start:end]) + suffix
}

var _ IssueSearchOperations = (*Client)(nil)
var _ IssueSearchOperations = (*GHBackend)(nil)
