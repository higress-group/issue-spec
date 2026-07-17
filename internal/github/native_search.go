package github

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type NativeSearchOperations interface {
	GetNativeServerMetadata(context.Context) (NativeServerMetadata, error)
	SearchNativeIssues(context.Context, string, NativeIssueSearchOptions) (NativeIssueSearchPage, error)
}

// Native aliases preserve the existing self-hosted client API while the
// command-facing result contract is shared with GitHub Issue Search.
type NativeIssueSearchOptions = IssueSearchOptions
type NativeIssueSearchPage = IssueSearchPage
type NativeIssueSearchResult = IssueSearchResult
type NativeIssueSearchChange = IssueSearchChange
type NativeIssueSearchMatch = IssueSearchMatch

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
