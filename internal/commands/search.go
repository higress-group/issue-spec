package commands

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/github"
)

type nativeSearchProvider interface {
	GetNativeServerMetadata(context.Context) (github.NativeServerMetadata, error)
	SearchNativeIssues(context.Context, string, github.NativeIssueSearchOptions) (github.NativeIssueSearchPage, error)
}

func defaultNewNativeSearchProvider(profile auth.Profile, token string) (nativeSearchProvider, error) {
	return github.NewClientWithOptions(github.ClientOptions{Host: profile.Hostname, BaseURL: profile.NativeAPIURL,
		Token: token, CAFile: profile.CAFile})
}

func (a *app) runSearch(ctx context.Context, args []string) int {
	if len(args) == 0 {
		a.errorf("usage: issue-spec search issues --repo owner/repo --query TEXT [options]\n")
		return 2
	}
	if args[0] != "issues" {
		a.errorf("unknown search command %q\n", args[0])
		return 2
	}
	return a.runSearchIssues(ctx, args[1:])
}

func (a *app) runSearchIssues(ctx context.Context, args []string) int {
	fs := newFlagSet("search issues", a.err)
	repoFlag := fs.String("repo", "", "repository owner/name on the issue backend")
	host := fs.String("hostname", "github.com", "issue backend hostname")
	query := fs.String("query", "", "full-text search query")
	state := fs.String("state", "all", "issue state: all, open, or closed")
	source := fs.String("source", "all", "match source: all, issue, comments, or change")
	stage := fs.String("stage", "", "change stage: proposal, design, or implement")
	limit := fs.Int("limit", 10, "maximum issue results (1-50)")
	if ok, code := a.parseFlagSet(fs, args); !ok {
		return code
	}
	if _, ok := a.validateRepo(*repoFlag); !ok {
		return 2
	}
	// Native search owns route escaping. Keep the validated repository raw so
	// special characters are encoded exactly once by SearchNativeIssues.
	repo := strings.TrimSpace(*repoFlag)
	options := github.NativeIssueSearchOptions{Query: strings.TrimSpace(*query), State: strings.ToLower(strings.TrimSpace(*state)),
		Source: strings.ToLower(strings.TrimSpace(*source)), Stage: strings.ToLower(strings.TrimSpace(*stage)), Page: 1, PerPage: *limit}
	if err := validateSearchOptions(options); err != nil {
		a.errorf("%v\n", err)
		return 2
	}
	profile, _, err := auth.ResolveProfile(a.profileName, *host)
	if err != nil {
		a.errorf("resolve issue backend profile: %v\n", err)
		return 1
	}
	if profile.Kind != auth.ProfileKindHosted {
		a.errorf("issue search requires a self-hosted issue-spec profile\n")
		return 1
	}
	token, err := auth.ResolveProfileToken(ctx, profile)
	if err != nil {
		a.errorf("auth required for issue search on %s: %v\n", profile.Hostname, err)
		return 1
	}
	if a.newNativeSearchProvider == nil {
		a.errorf("native issue search client is unavailable\n")
		return 1
	}
	provider, err := a.newNativeSearchProvider(profile, token.Value)
	if err != nil {
		a.errorf("configure native issue search: %v\n", err)
		return 1
	}
	metadata, err := provider.GetNativeServerMetadata(ctx)
	if err != nil {
		a.errorf("discover issue search capability: %v\n", err)
		return 1
	}
	if !metadata.Features.Search {
		a.errorf("issue search is disabled on the selected self-hosted server\n")
		return 1
	}
	page, err := provider.SearchNativeIssues(ctx, repo, options)
	if err != nil {
		a.errorf("search issues: %v\n", err)
		return 1
	}
	nonce, err := randomNonce()
	if err != nil {
		a.errorf("generate boundary nonce: %v\n", err)
		return 1
	}
	var output strings.Builder
	renderSearchResults(&output, nonce, untrustedRedactor(token.Value), profile.Name, page)
	fmt.Fprint(a.out, output.String())
	return 0
}

func validateSearchOptions(options github.NativeIssueSearchOptions) error {
	if options.Query == "" || len(options.Query) > 256 {
		return errors.New("--query must contain 1-256 bytes")
	}
	if options.State != "all" && options.State != "open" && options.State != "closed" {
		return errors.New("--state must be all, open, or closed")
	}
	if options.Source != "all" && options.Source != "issue" && options.Source != "comments" && options.Source != "change" {
		return errors.New("--source must be all, issue, comments, or change")
	}
	if options.Stage != "" && options.Stage != "proposal" && options.Stage != "design" && options.Stage != "implement" {
		return errors.New("--stage must be proposal, design, or implement")
	}
	if options.PerPage < 1 || options.PerPage > 50 {
		return errors.New("--limit must be between 1 and 50")
	}
	return nil
}

func renderSearchResults(output *strings.Builder, nonce string, redactor github.ExternalCLIRedactor, profile string, page github.NativeIssueSearchPage) {
	writeReadHeader(output, nonce)
	fmt.Fprintf(output, "\nresults: %d\ntotal: %d\nhas_next: %t\n", len(page.Items), page.Total, page.HasNext)
	for _, item := range page.Items {
		fmt.Fprintf(output, "\nissue: #%d\n", item.Number)
		writeTrustedField(output, redactor, "repository", item.Organization+"/"+item.Repository)
		writeTrustedField(output, redactor, "url", item.URL)
		writeTrustedField(output, redactor, "state", item.State)
		for _, change := range item.Changes {
			writeUntrustedField(output, nonce, redactor, "change", change.Key)
			writeTrustedField(output, redactor, "stage", change.Stage)
		}
		writeUntrustedField(output, nonce, redactor, "title", item.Title)
		for _, match := range item.Matches {
			writeTrustedField(output, redactor, "match_source", match.Source)
			writeUntrustedField(output, nonce, redactor, "excerpt", match.Excerpt)
		}
		next := "issue-spec"
		if strings.TrimSpace(profile) != "" {
			next += " --profile " + profile
		}
		next += fmt.Sprintf(" read issue --repo %s/%s --issue %d --comments", item.Organization, item.Repository, item.Number)
		writeTrustedField(output, redactor, "next", next)
	}
}
