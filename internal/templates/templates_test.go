package templates

import (
	"strings"
	"testing"
)

func TestProposalTemplateUsesSentinelNotBareTBD(t *testing.T) {
	_, body, _ := ProposalIssue("demo-change")
	for _, section := range []string{"## Background", "## Goals", "## Scope", "## Related Specs Analysis", "## Existing Assumptions Impact"} {
		content := sectionOf(t, body, section)
		if !strings.Contains(content, PlaceholderSentinel) {
			t.Errorf("proposal section %q missing placeholder sentinel; got %q", section, content)
		}
	}
	if strings.Contains(body, "\nTBD\n") || strings.Contains(body, "- TBD") {
		t.Errorf("proposal body still contains a bare TBD placeholder:\n%s", body)
	}
}

func TestDesignTemplateUsesSentinelNotBareTBD(t *testing.T) {
	_, body, _ := DesignIssue("demo-change", "21")
	for _, section := range []string{"## Current Implementation Locations", "## Impact Scope", "## Candidate Plans", "## Decisions"} {
		content := sectionOf(t, body, section)
		if !strings.Contains(content, PlaceholderSentinel) {
			t.Errorf("design section %q missing placeholder sentinel; got %q", section, content)
		}
	}
	if strings.Contains(body, "\nTBD\n") || strings.Contains(body, "- TBD") {
		t.Errorf("design body still contains a bare TBD placeholder:\n%s", body)
	}
}

func TestIssueTemplatesScaffoldProjectionTimingAndAuthority(t *testing.T) {
	_, proposal, _ := ProposalIssue("demo-change")
	_, design, _ := DesignIssue("demo-change", "21")
	_, implement, _ := ImplementIssue("demo-change", "22")
	tests := []struct {
		name      string
		body      string
		nextChild string
		content   []string
	}{
		{
			name: "proposal", body: proposal, nextChild: "SPEC",
			content: []string{"latest effective ANSWER remain authoritative", "affected person or operator", "concrete before/after case", "build a coverage ledger", "complete current problem", "do not emit only a delta or executive summary", "projection HTML source is excluded from default Agent context"},
		},
		{
			name: "design", body: design, nextChild: "TASK",
			content: []string{"latest effective ANSWER remain authoritative", "concrete request or operator case", "observable outcome", "meaningful failure path", "build a coverage ledger", "complete current architecture", "state, alternatives, compatibility", "do not assume the reviewer already knows omitted design information"},
		},
		{
			name: "implement", body: implement, nextChild: "PROCESS",
			content: []string{"latest effective ANSWER remain authoritative", "concrete acceptance case", "PROCESS sequence", "human-visible, verified outcome", "build a coverage ledger", "complete current invariant DAG", "SPEC/scenario coverage", "independent review/verify obligations", "do not emit only the increment since Design", "estimates and complexity do not define workflow semantics"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			section := sectionOf(t, tc.body, "## Human Review Projection")
			for _, want := range []string{
				"after the first QUESTION discovery/create pass",
				"before complete " + tc.nextChild,
				"ordinary and statusless",
			} {
				if !strings.Contains(section, want) {
					t.Fatalf("%s projection section missing %q:\n%s", tc.name, want, section)
				}
			}
			for _, want := range tc.content {
				if !strings.Contains(section, want) {
					t.Fatalf("%s projection section missing %q:\n%s", tc.name, want, section)
				}
			}
		})
	}
}

func TestIssueTemplatesOmitHTMLReviewSectionsWhenDisabled(t *testing.T) {
	options := WorkflowAuthoringOptions{HTMLReviewEnabled: false}
	_, proposal, _ := ProposalIssueWithOptions("demo-change", options)
	_, design, _ := DesignIssueWithOptions("demo-change", "21", options)
	_, implement, _ := ImplementIssueWithOptions("demo-change", "22", options)
	for _, test := range []struct {
		name     string
		body     string
		required []string
	}{
		{name: "proposal", body: proposal, required: []string{"## Open Questions", "## Capabilities"}},
		{name: "design", body: design, required: []string{"## Question Convergence Check", "## Current Implementation Locations", "## Confirmation Checklist"}},
		{name: "implement", body: implement, required: []string{"## PR Mode Decision", "## DAG Nodes and Dependencies", "## Global Review / Verify Status"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if strings.Contains(test.body, "## Human Review Projection") {
				t.Fatalf("disabled %s issue body retains HTML review section:\n%s", test.name, test.body)
			}
			for _, want := range test.required {
				if !strings.Contains(test.body, want) {
					t.Fatalf("disabled %s issue body lost %q:\n%s", test.name, want, test.body)
				}
			}
		})
	}
}

func sectionOf(t *testing.T, body, heading string) string {
	t.Helper()
	idx := strings.Index(body, heading+"\n")
	if idx < 0 {
		t.Fatalf("heading %q not found in body", heading)
	}
	rest := body[idx+len(heading)+1:]
	if next := strings.Index(rest, "\n## "); next >= 0 {
		return rest[:next]
	}
	return rest
}

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
