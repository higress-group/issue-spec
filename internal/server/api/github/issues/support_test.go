package issues

import (
	"strings"
	"testing"
)

const typedHeader = `Agent: Coordinator
Type: QUESTION
ID: QUESTION-1004
Status: blocked
Scope: example-scope
`

func TestValidateTypedCommentMarker(t *testing.T) {
	cases := []struct {
		name         string
		body         string
		wantCount    int
		wantCode     string
		wantContains []string
	}{
		{
			name:      "ordinary body without marker",
			body:      "plain discussion text",
			wantCount: 0,
		},
		{
			name:      "valid typed comment at schema version 1",
			body:      "<!-- issue-spec:type=QUESTION id=QUESTION-1004 version=1 -->\n" + typedHeader,
			wantCount: 0,
		},
		{
			name:      "typed marker without explicit version defaults to 1",
			body:      "<!-- issue-spec:type=QUESTION id=QUESTION-1004 -->\n" + typedHeader,
			wantCount: 0,
		},
		{
			name:         "typed marker bumped to version 2",
			body:         "<!-- issue-spec:type=QUESTION id=QUESTION-1004 version=2 -->\n" + typedHeader,
			wantCount:    1,
			wantCode:     "invalid_marker_version",
			wantContains: []string{"version=2", "Resolution Log"},
		},
		{
			name:         "typed marker with unparseable version",
			body:         "<!-- issue-spec:type=QUESTION id=QUESTION-1004 version=abc -->\n" + typedHeader,
			wantCount:    1,
			wantCode:     "invalid",
			wantContains: []string{`invalid marker version "abc"`},
		},
		{
			name:         "typed marker missing id",
			body:         "<!-- issue-spec:type=QUESTION version=1 -->\n" + typedHeader,
			wantCount:    1,
			wantCode:     "invalid",
			wantContains: []string{"must include type and id"},
		},
		{
			name:         "marker without visible header mirrors projection drop",
			body:         "<!-- issue-spec:type=QUESTION id=QUESTION-1004 version=1 -->\nplain prose without a header",
			wantCount:    1,
			wantCode:     "invalid",
			wantContains: []string{"missing visible header"},
		},
		{
			name: "marker id contradicts header id",
			body: "<!-- issue-spec:type=QUESTION id=QUESTION-1004 version=1 -->\n" +
				strings.Replace(typedHeader, "ID: QUESTION-1004", "ID: QUESTION-1005", 1),
			wantCount:    1,
			wantCode:     "invalid",
			wantContains: []string{"does not match header id"},
		},
		{
			name:      "issue lineage marker is not a typed comment marker",
			body:      "<!-- issue-spec:issue=proposal change=some-change version=2 -->\ncontent",
			wantCount: 0,
		},
		{
			name:      "accepted review receipt marker version 2 stays writable",
			body:      "<!-- issue-spec:accepted-review-receipt version=2 -->\nreceipt body",
			wantCount: 0,
		},
		{
			name: "only the first marker is significant",
			body: "<!-- issue-spec:type=QUESTION id=QUESTION-1004 version=1 -->\n" + typedHeader +
				"\n<!-- issue-spec:type=QUESTION id=QUESTION-1099 version=2 -->\n",
			wantCount: 0,
		},
		{
			name:         "marker quoted in an ordinary code fence counts like projection",
			body:         "```\n<!-- issue-spec:type=QUESTION id=QUESTION-1004 version=2 -->\n```",
			wantCount:    1,
			wantCode:     "invalid_marker_version",
			wantContains: []string{"version=2"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			violations := ValidateTypedCommentMarker("IssueComment", tc.body)
			if len(violations) != tc.wantCount {
				t.Fatalf("violations = %d, want %d (%v)", len(violations), tc.wantCount, violations)
			}
			if tc.wantCount == 0 {
				return
			}
			violation := violations[0]
			if violation.Code != tc.wantCode {
				t.Fatalf("code = %q, want %q (%q)", violation.Code, tc.wantCode, violation.Message)
			}
			if violation.Resource != "IssueComment" || violation.Field != "body" {
				t.Fatalf("violation resource/field = %s/%s, want IssueComment/body", violation.Resource, violation.Field)
			}
			for _, want := range tc.wantContains {
				if !strings.Contains(violation.Message, want) {
					t.Fatalf("violation message %q does not contain %q", violation.Message, want)
				}
			}
		})
	}
}
