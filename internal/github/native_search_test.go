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
			r.URL.Query().Get("state") != "closed" || r.URL.Query().Get("source") != "comments" ||
			r.URL.Query().Get("stage") != "implement" || r.URL.Query().Get("page") != "2" || r.URL.Query().Get("per_page") != "7" {
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
		State: "closed", Source: "comments", Stage: "implement", Page: 2, PerPage: 7})
	if err != nil || len(page.Items) != 1 || page.Items[0].Number != 17 {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	if _, err := client.SearchNativeIssues(t.Context(), "acme/widgets", NativeIssueSearchOptions{}); err == nil {
		t.Fatal("empty search query accepted")
	}
}
