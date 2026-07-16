package github

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type NativeSearchOperations interface {
	GetNativeServerMetadata(context.Context) (NativeServerMetadata, error)
	SearchNativeIssues(context.Context, string, NativeIssueSearchOptions) (NativeIssueSearchPage, error)
}

type NativeIssueSearchOptions struct {
	Query   string
	State   string
	Source  string
	Stage   string
	Page    int
	PerPage int
}

type NativeIssueSearchPage struct {
	Items   []NativeIssueSearchResult `json:"items"`
	Page    int                       `json:"page"`
	PerPage int                       `json:"per_page"`
	Total   int64                     `json:"total"`
	HasNext bool                      `json:"has_next"`
}

type NativeIssueSearchResult struct {
	Organization string                    `json:"organization"`
	Repository   string                    `json:"repository"`
	Number       int64                     `json:"number"`
	Title        string                    `json:"title"`
	State        string                    `json:"state"`
	URL          string                    `json:"url"`
	UpdatedAt    time.Time                 `json:"updated_at"`
	Changes      []NativeIssueSearchChange `json:"changes"`
	Matches      []NativeIssueSearchMatch  `json:"matches"`
}

type NativeIssueSearchChange struct {
	Key     string `json:"key"`
	Stage   string `json:"stage"`
	Matched bool   `json:"matched"`
}

type NativeIssueSearchMatch struct {
	Source  string `json:"source"`
	Excerpt string `json:"excerpt"`
}

func (c *Client) SearchNativeIssues(ctx context.Context, repo string, options NativeIssueSearchOptions) (NativeIssueSearchPage, error) {
	parsed, err := ParseRepo(repo)
	if err != nil {
		return NativeIssueSearchPage{}, err
	}
	options.Query = strings.TrimSpace(options.Query)
	if options.Query == "" || len(options.Query) > 256 || options.Page < 0 || options.PerPage < 0 || options.PerPage > 50 {
		return NativeIssueSearchPage{}, errors.New("native issue search options are invalid")
	}
	query := url.Values{"q": []string{options.Query}}
	for key, value := range map[string]string{"state": options.State, "source": options.Source, "stage": options.Stage} {
		if value = strings.TrimSpace(value); value != "" {
			query.Set(key, value)
		}
	}
	if options.Page > 0 {
		query.Set("page", strconv.Itoa(options.Page))
	}
	if options.PerPage > 0 {
		query.Set("per_page", strconv.Itoa(options.PerPage))
	}
	var result NativeIssueSearchPage
	_, err = c.doRunnerJSON(ctx, http.MethodGet, "/context/repos/"+parsed+"/search/issues", query, nil, ConditionalRequest{}, false, &result)
	if err != nil {
		return NativeIssueSearchPage{}, err
	}
	if result.Items == nil || result.Page < 1 || result.PerPage < 1 || result.PerPage > 50 || result.Total < 0 {
		return NativeIssueSearchPage{}, errors.New("native issue search response is incomplete")
	}
	return result, nil
}

var _ NativeSearchOperations = (*Client)(nil)
