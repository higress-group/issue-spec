package github

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNativeIssueSearchUsesContextRouteAndBoundedOptions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != "/api/v1/context/repos/acme/widget%20name%25%E4%B8%AD%E6%96%87/search/issues" {
			t.Fatalf("path=%q escaped=%q", r.URL.Path, r.URL.EscapedPath())
		}
		if r.Header.Get("Authorization") != "Bearer search-token" || r.URL.Query().Get("q") != "鉴权锁" ||
			r.URL.Query().Get("state") != "closed" || r.URL.Query().Get("source") != "issue" ||
			r.URL.Query().Get("stage") != "proposal" || r.URL.Query().Get("page") != "2" || r.URL.Query().Get("per_page") != "7" {
			t.Fatalf("headers=%v query=%v", r.Header, r.URL.Query())
		}
		_ = json.NewEncoder(w).Encode(NativeIssueSearchPage{Items: []NativeIssueSearchResult{{Organization: "acme",
			Repository: "widgets", Number: 17, Title: "鉴权锁"}}, Page: 2, PerPage: 7})
	}))
	defer server.Close()
	client, err := NewClientWithOptions(ClientOptions{Host: "issues.test", BaseURL: server.URL + "/api/v1",
		Token: "search-token", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	page, err := client.SearchNativeIssues(t.Context(), "acme/widget name%中文", NativeIssueSearchOptions{Query: "鉴权锁",
		State: "closed", Source: "all", Page: 2, PerPage: 7})
	if err != nil || len(page.Items) != 1 || page.Items[0].Number != 17 {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	if _, err := client.SearchNativeIssues(t.Context(), "acme/widgets", NativeIssueSearchOptions{}); err == nil {
		t.Fatal("empty search query accepted")
	}
	if _, err := client.SearchNativeIssues(t.Context(), "acme/widgets", NativeIssueSearchOptions{Query: "x", Source: "comments"}); err == nil {
		t.Fatal("comment search source accepted")
	}
	if _, err := client.SearchNativeIssues(t.Context(), "acme/widgets", NativeIssueSearchOptions{Query: "x", Stage: "design"}); err == nil {
		t.Fatal("Design search stage accepted")
	}
}
