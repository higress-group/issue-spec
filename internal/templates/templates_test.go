package templates

import (
	"strings"
	"testing"
)

func TestIssueTemplatesUseStandardizedTitles(t *testing.T) {
	tests := []struct {
		name string
		got  string
		want string
	}{
		{
			name: "proposal",
			got: func() string {
				title, _, _ := ProposalIssue("issue-title-style")
				return title
			}(),
			want: "Proposal: issue-title-style",
		},
		{
			name: "design",
			got: func() string {
				title, _, _ := DesignIssue("issue-title-style", "21")
				return title
			}(),
			want: "Design: issue-title-style",
		},
		{
			name: "implement",
			got: func() string {
				title, _, _ := ImplementIssue("issue-title-style", "103")
				return title
			}(),
			want: "Implement: issue-title-style",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Fatalf("title = %q, want %q", tt.got, tt.want)
			}
		})
	}
}

func TestIssueTemplatesDoNotIncludeIssueSpecFooter(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "proposal",
			body: func() string {
				_, body, _ := ProposalIssue("issue-title-style")
				return body
			}(),
		},
		{
			name: "design",
			body: func() string {
				_, body, _ := DesignIssue("issue-title-style", "21")
				return body
			}(),
		},
		{
			name: "implement",
			body: func() string {
				_, body, _ := ImplementIssue("issue-title-style", "103")
				return body
			}(),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if strings.Contains(tt.body, IssueSpecProjectURL) {
				t.Fatalf("default template body should stay footer-free:\n%s", tt.body)
			}
		})
	}
}

func TestAppendIssueSpecIssueFooter(t *testing.T) {
	body := AppendIssueSpecIssueFooter("# Proposal\n")
	if !strings.Contains(body, IssueBodyManagedByQuote) {
		t.Fatalf("body missing issue-spec footer:\n%s", body)
	}
	again := AppendIssueSpecIssueFooter(body)
	if strings.Count(again, IssueSpecProjectURL) != 1 {
		t.Fatalf("footer should not be duplicated:\n%s", again)
	}
}

func TestIssueTitleDerivesSubjectFromFinalBody(t *testing.T) {
	body := "<!-- issue-spec:issue=proposal change=issue-title-style version=1 -->\n# Proposal: standardize issue-spec issue titles\n\n## Metadata\n"
	got := IssueTitle("proposal", "issue-title-style", body, "")
	if want := "Proposal: standardize issue-spec issue titles"; got != want {
		t.Fatalf("title = %q, want %q", got, want)
	}
}

func TestIssueTitleStripsImplementDAGPrefix(t *testing.T) {
	body := "# Implement DAG: standardize issue-spec issue titles\n"
	got := IssueTitle("implement", "issue-title-style", body, "")
	if want := "Implement: standardize issue-spec issue titles"; got != want {
		t.Fatalf("title = %q, want %q", got, want)
	}
}

func TestIssueTitleExplicitOverrideWins(t *testing.T) {
	got := IssueTitle("proposal", "issue-title-style", "# Proposal: ignored\n", "Custom proposal title")
	if want := "Custom proposal title"; got != want {
		t.Fatalf("title = %q, want %q", got, want)
	}
}

func TestIssueTitleFallsBackToChangeName(t *testing.T) {
	got := IssueTitle("design", "issue-title-style", "No heading here.\n", "")
	if want := "Design: issue-title-style"; got != want {
		t.Fatalf("title = %q, want %q", got, want)
	}
}
