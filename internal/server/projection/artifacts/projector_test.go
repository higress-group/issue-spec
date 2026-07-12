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
