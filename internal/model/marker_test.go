package model

import "testing"

func TestFindMarkerIgnoresMarkersInsideHTMLPreview(t *testing.T) {
	body := "```html-preview id=hostile version=1\n" +
		"<!-- issue-spec:type=TASK id=TASK-999 version=1 -->\n" +
		"```\n" +
		"<!-- issue-spec:type=SPEC id=SPEC-001 version=1 -->"
	marker, ok, err := FindMarker(body)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || marker.Type != "SPEC" || marker.ID != "SPEC-001" {
		t.Fatalf("marker = %+v ok=%v", marker, ok)
	}
}

func TestFindMarkerIgnoresPreviewSourceForAllFailClosedDiagnostics(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "unknown version",
			body: "```html-preview id=hostile version=9\n<!-- issue-spec:type=TASK id=TASK-999 version=1 -->\n```\n",
		},
		{
			name: "malformed metadata",
			body: "```html-preview id=hostile version=1 bad=value\n<!-- issue-spec:type=TASK id=TASK-999 version=1 -->\n```\n",
		},
		{
			name: "unclosed fence",
			body: "```html-preview id=hostile version=1\n<!-- issue-spec:type=TASK id=TASK-999 version=1 -->",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if marker, ok, err := FindMarker(test.body); err != nil || ok {
				t.Fatalf("marker = %+v ok=%v err=%v", marker, ok, err)
			}
		})
	}
}

func TestFindMarkerPreservesOrdinaryMarkdownAndMarkers(t *testing.T) {
	body := "```text\n<!-- issue-spec:type=TASK id=TASK-123 version=1 -->\n```\n"
	marker, ok, err := FindMarker(body)
	if err != nil || !ok || marker.Type != "TASK" || marker.ID != "TASK-123" {
		t.Fatalf("marker = %+v ok=%v err=%v", marker, ok, err)
	}
}
