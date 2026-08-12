package github

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestRESTIssueSearchBuildsProposalTitleBodyQueryAndFiltersResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/search/issues" {
			t.Fatalf("request=%s %s", r.Method, r.URL.String())
		}
		wantQuery := `auth lock repo:acme/widgets is:issue is:closed in:title,body label:"issue-spec/proposal"`
		if r.URL.Query().Get("q") != wantQuery || r.URL.Query().Get("page") != "1" || r.URL.Query().Get("per_page") != "2" ||
			r.Header.Get("Accept") != githubTextMatchMediaType || r.Header.Get("Authorization") != "Bearer token" {
			t.Fatalf("headers=%v query=%v", r.Header, r.URL.Query())
		}
		_ = json.NewEncoder(w).Encode(githubIssueSearchResponse{TotalCount: 6, Items: []githubIssueSearchItem{
			{Number: 1, Title: "pull request", State: "closed", RepositoryURL: "https://api.github.com/repos/acme/widgets", Labels: []githubIssueSearchLabel{{Name: "issue-spec/proposal"}}, PullRequest: &struct{}{}},
			{Number: 2, Title: "other repo", State: "closed", RepositoryURL: "https://api.github.com/repos/other/widgets", Labels: []githubIssueSearchLabel{{Name: "issue-spec/proposal"}}},
			{Number: 3, Title: "wrong state", State: "open", RepositoryURL: "https://api.github.com/repos/acme/widgets", Labels: []githubIssueSearchLabel{{Name: "issue-spec/proposal"}}},
			{Number: 4, Title: "not a Proposal", State: "closed", RepositoryURL: "https://api.github.com/repos/acme/widgets", Labels: []githubIssueSearchLabel{{Name: "issue-spec/design"}}},
			{Number: 5, Title: "valid", State: "closed", HTMLURL: "https://github.com/acme/widgets/issues/5", RepositoryURL: "https://api.github.com/repos/acme/widgets",
				Labels: []githubIssueSearchLabel{{Name: "ISSUE-SPEC/PROPOSAL"}}, TextMatches: []githubIssueTextMatch{{ObjectType: "Issue", Property: "body", Fragment: "auth lock matched"}}},
			{Number: 6, Title: "comment-only provider drift", State: "closed", RepositoryURL: "https://api.github.com/repos/acme/widgets",
				Labels: []githubIssueSearchLabel{{Name: "issue-spec/proposal"}}, TextMatches: []githubIssueTextMatch{{ObjectType: "IssueComment", Property: "body", Fragment: "auth lock in comment"}}},
		}})
	}))
	defer server.Close()
	client := NewClientWithBaseURL("github.com", server.URL, "token", server.Client())
	page, err := client.SearchIssues(t.Context(), "acme/widgets", IssueSearchOptions{Query: "auth lock", State: "closed", Source: "all", Stage: "proposal", PerPage: 2})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || page.HasNext || len(page.Items) != 1 || page.Items[0].Number != 5 || len(page.Items[0].Matches) != 1 ||
		page.Items[0].Matches[0].Source != "issue" || page.Items[0].Matches[0].Excerpt != "auth lock matched" {
		t.Fatalf("page=%+v", page)
	}
}

func TestRESTIssueSearchEnforcesLimitAndBoundsExcerpts(t *testing.T) {
	longFragment := strings.Repeat("prefix ", 100) + "needle" + strings.Repeat(" suffix", 100)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(githubIssueSearchResponse{TotalCount: 3, Items: []githubIssueSearchItem{
			{Number: 1, Title: "one", State: "open", RepositoryURL: "https://api.github.com/repos/o/r", Labels: []githubIssueSearchLabel{{Name: "issue-spec/proposal"}}, TextMatches: []githubIssueTextMatch{{ObjectType: "Issue", Fragment: longFragment}}},
			{Number: 2, Title: "two", Body: "needle", State: "open", RepositoryURL: "https://api.github.com/repos/o/r", Labels: []githubIssueSearchLabel{{Name: "issue-spec/proposal"}}},
			{Number: 3, Title: "three", Body: "needle", State: "open", RepositoryURL: "https://api.github.com/repos/o/r", Labels: []githubIssueSearchLabel{{Name: "issue-spec/proposal"}}},
		}})
	}))
	defer server.Close()
	client := NewClientWithBaseURL("github.com", server.URL, "token", server.Client())
	page, err := client.SearchIssues(t.Context(), "o/r", IssueSearchOptions{Query: "needle", State: "all", Source: "all", PerPage: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 || !page.HasNext || len([]rune(page.Items[0].Matches[0].Excerpt)) > maxIssueSearchExcerptRunes+2 {
		t.Fatalf("page=%+v excerpt_runes=%d", page, len([]rune(page.Items[0].Matches[0].Excerpt)))
	}
}

func TestGitHubIssueSearchRejectsFiltersOutsideProposalTitleBodyScope(t *testing.T) {
	client := NewClient("github.com", "token")
	for _, options := range []IssueSearchOptions{
		{Query: "change", State: "all", Source: "comments", PerPage: 10},
		{Query: "change", State: "all", Source: "change", PerPage: 10},
		{Query: "change", State: "all", Stage: "design", PerPage: 10},
		{Query: "change", State: "all", Stage: "implement", PerPage: 10},
	} {
		if _, err := client.SearchIssues(t.Context(), "o/r", options); err == nil ||
			(!strings.Contains(err.Error(), "Proposal titles and bodies") && !strings.Contains(err.Error(), "stage must be proposal")) {
			t.Fatalf("options=%+v err=%v", options, err)
		}
	}
}

func TestGHBackendIssueSearchUsesSameRequestContract(t *testing.T) {
	runner := &recordingCLIRunner{result: ExternalCLIResult{Stdout: []byte(`{"total_count":1,"items":[{"number":7,"title":"match","body":"lock","state":"open","html_url":"https://github.com/o/r/issues/7","repository_url":"https://api.github.com/repos/o/r","labels":[{"name":"issue-spec/proposal"}]}]}`)}}
	backend := newTestGHBackend(t, "github.com", runner)
	page, err := backend.SearchIssues(t.Context(), "o/r", IssueSearchOptions{Query: "lock", State: "open", Source: "issue", PerPage: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].Number != 7 {
		t.Fatalf("page=%+v", page)
	}
	want := []string{"api", "--method", http.MethodGet, "--header", githubAPIVersion, "--header", "Accept: " + githubTextMatchMediaType,
		"/search/issues?page=1&per_page=10&q=lock+repo%3Ao%2Fr+is%3Aissue+is%3Aopen+in%3Atitle%2Cbody+label%3A%22issue-spec%2Fproposal%22"}
	if !reflect.DeepEqual(runner.command.Args, want) {
		t.Fatalf("args=%#v want=%#v", runner.command.Args, want)
	}
}
