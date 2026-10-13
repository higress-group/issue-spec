package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/higress-group/issue-spec/internal/github"
)

func TestIssueListBoundsDefaultAndExplicitlyExpandsBodies(t *testing.T) {
	for _, tt := range []struct {
		args          []string
		count, first  int
		next, omitted bool
	}{
		{nil, 50, 1, true, true},
		{[]string{"--page", "2", "--limit", "50"}, 10, 51, false, true},
		{[]string{"--all", "--include-body"}, 60, 1, false, false},
	} {
		var out, errOut bytes.Buffer
		app := newApp(strings.NewReader(""), &out, &errOut)
		app.selectGitHubBackend = ghSelection
		app.newGitHubBackend = newFakeBackend(func(f *fakeGitHubBackend) {
			f.listIssues = func(context.Context, string, github.ListIssueOptions) ([]github.Issue, error) {
				var issues []github.Issue
				for i := 1; i <= 60; i++ {
					issues = append(issues, github.Issue{Number: i, Title: "selected change", Body: strings.Repeat("long body ", 5000)})
				}
				return issues, nil
			}
		})
		args := append([]string{"list", "--repo", "o/r", "--json"}, tt.args...)
		if code := app.runIssue(t.Context(), args); code != 0 {
			t.Fatal(errOut.String())
		}
		var result issueListResult
		if err := json.Unmarshal(out.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if len(result.Issues) != tt.count || result.Issues[0].Number != tt.first || result.HasNext != tt.next || result.BodyOmitted != tt.omitted || result.Total != 60 {
			t.Fatalf("invalid page metadata: %+v", result)
		}
		if tt.omitted && (out.Len() > 15000 || strings.Contains(out.String(), "long body")) {
			t.Fatalf("default output unexpectedly large: %d", out.Len())
		}
		if !tt.omitted && len(result.Issues[0].Body) != 50000 {
			t.Fatal("explicit body truncated")
		}
	}
}
