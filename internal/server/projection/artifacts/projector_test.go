package artifacts

import "testing"

func TestIssueMarkerRecognitionDoesNotRewriteRawBytes(t *testing.T) {
	raw := "<!-- issue-spec:issue=proposal change=raw-change version=1 -->\r\n  内容  \r\n"
	matches := issueMarker.FindStringSubmatch(raw)
	if len(matches) != 4 || matches[1] != "proposal" || matches[2] != "raw-change" || matches[3] != "1" {
		t.Fatalf("marker matches = %#v", matches)
	}
	if got := issueMarker.ReplaceAllString(raw, "$0"); got != raw {
		t.Fatalf("raw marker bytes changed: %q", got)
	}
	future := "<!-- issue-spec:issue=design change=x version=9 -->\n"
	if got := issueMarker.FindStringSubmatch(future); len(got) != 4 || got[3] != "9" {
		t.Fatalf("future marker was not surfaced for anomaly handling: %#v", got)
	}
}

func TestNextIssueScopedTypedCommentID(t *testing.T) {
	tests := []struct {
		name        string
		commentType string
		issueNumber int64
		ids         []string
		want        string
	}{
		{name: "first", commentType: "QUESTION", issueNumber: 44, want: "QUESTION-44001"},
		{
			name: "next after maximum sequence", commentType: "QUESTION", issueNumber: 44,
			ids:  []string{"QUESTION-44001", "QUESTION-44003", "QUESTION-1001", "SPEC-44009"},
			want: "QUESTION-44004",
		},
		{
			name: "ignore ambiguous legacy suffix", commentType: "QUESTION", issueNumber: 1,
			ids:  []string{"QUESTION-001", "QUESTION-10001"},
			want: "QUESTION-1001",
		},
		{
			name: "sequence exhausted", commentType: "PROCESS", issueNumber: 7,
			ids:  []string{"PROCESS-7999"},
			want: "",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := nextIssueScopedTypedCommentID(test.commentType, test.issueNumber, test.ids); got != test.want {
				t.Fatalf("nextIssueScopedTypedCommentID(%q, %d, %v) = %q, want %q",
					test.commentType, test.issueNumber, test.ids, got, test.want)
			}
		})
	}
}
