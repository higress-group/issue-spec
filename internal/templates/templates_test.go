package templates

import "testing"

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
